package session

import "testing"

func TestDecodeOEMGBK(t *testing.T) {
	// "版本" in GBK
	b := []byte{0xB0, 0xE6, 0xB1, 0xBE}
	got := decodeOEM(b)
	if got != "版本" {
		t.Fatalf("got %q", got)
	}
}

func TestDecodeOEMUTF8(t *testing.T) {
	s := "C:\\Users\\yym\\Desktop>"
	if decodeOEM([]byte(s)) != s {
		t.Fatal("utf8 passthrough")
	}
}
