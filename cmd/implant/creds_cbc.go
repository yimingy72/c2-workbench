//go:build darwin || linux

package main

// Chromium on macOS and Linux encrypts values with AES-128-CBC ("v10" scheme)
// using a key derived from a password: the Keychain item on macOS, or the
// static "peanuts" string on older Linux builds. The cipher/PBKDF2 helpers are
// shared by both platforms; only key acquisition differs.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"errors"
)

var errNoKey = errors.New("no key material")

// deriveKey runs PBKDF2-HMAC-SHA1, the derivation Chromium uses for the
// "saltysalt" scheme (1003 iterations on macOS, 1 on the legacy Linux key).
func deriveKey(password string, salt string, iter, size int) ([]byte, error) {
	key, err := pbkdf2.Key(sha1.New, password, []byte(salt), iter, size)
	if err != nil {
		return nil, err
	}
	return key, nil
}

// cbcDecrypt opens a "v10" prefixed AES-CBC value with a 16-space IV and
// removes PKCS#7 padding.
func cbcDecrypt(key, blob []byte) ([]byte, bool) {
	if len(key) == 0 || len(blob) < 3+16 {
		return nil, false
	}
	if string(blob[:3]) != "v10" {
		return nil, false
	}
	ct := blob[3:]
	if len(ct)%aes.BlockSize != 0 || len(ct) == 0 {
		return nil, false
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, false
	}
	iv := make([]byte, aes.BlockSize)
	for i := range iv {
		iv[i] = ' '
	}
	out := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ct)
	if n := int(out[len(out)-1]); n > 0 && n <= aes.BlockSize && n <= len(out) {
		out = out[:len(out)-n]
	}
	return out, true
}
