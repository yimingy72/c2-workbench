package protocol

import "testing"

func TestParseGetArg(t *testing.T) {
	off, max, path := ParseGetArg(`C:\readme.txt`)
	if off != 0 || max != 0 || path != `C:\readme.txt` {
		t.Fatalf("%d %d %q", off, max, path)
	}
	off, max, path = ParseGetArg("12\n256\nC:\\a.txt")
	if off != 12 || max != 256 || path != `C:\a.txt` {
		t.Fatalf("%d %d %q", off, max, path)
	}
	b := EncodeGetArg(8, 32, "/tmp/x")
	off, max, path = ParseGetArg(string(b))
	if off != 8 || max != 32 || path != "/tmp/x" {
		t.Fatalf("%d %d %q", off, max, path)
	}
}

func TestPutOffset(t *testing.T) {
	path := "/tmp/x"
	p := make([]byte, 3+len(path)+8)
	p[0] = 0
	p[1] = byte(len(path))
	copy(p[3:], path)
	p[3+len(path)] = 10
	got, off, ok := PutOffset(p)
	if !ok || got != path || off != 10 {
		t.Fatalf("%q %d %v", got, off, ok)
	}
}
