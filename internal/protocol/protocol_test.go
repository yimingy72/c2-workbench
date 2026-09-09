package protocol

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	payload := []byte("whoami")
	raw := Encode(CmdExec, payload)
	f, err := Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != CmdExec {
		t.Fatalf("type %d", f.Type)
	}
	if !bytes.Equal(f.Payload, payload) {
		t.Fatalf("payload %q", f.Payload)
	}
}

func TestEncodeEmptyPayload(t *testing.T) {
	raw := Encode(CmdHeartbeat, nil)
	if len(raw) != 8 {
		t.Fatalf("len %d", len(raw))
	}
	f, err := Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != CmdHeartbeat || len(f.Payload) != 0 {
		t.Fatalf("%+v", f)
	}
}

func TestDecodeBadMagic(t *testing.T) {
	raw := Encode(CmdPing, []byte("x"))
	raw[0] = 0x00
	_, err := Decode(bytes.NewReader(raw))
	if !errors.Is(err, ErrMagic) {
		t.Fatalf("err %v", err)
	}
}

func TestDecodeTooBig(t *testing.T) {
	raw := Encode(CmdExec, []byte("x"))
	raw[4] = 0xFF
	raw[5] = 0xFF
	raw[6] = 0x00
	raw[7] = 0x01 // 65535+ something > 8192
	_, err := Decode(bytes.NewReader(raw))
	if !errors.Is(err, ErrTooBig) {
		t.Fatalf("err %v", err)
	}
}

func TestDecodeShort(t *testing.T) {
	_, err := Decode(bytes.NewReader([]byte{0xA1}))
	if !errors.Is(err, io.ErrUnexpectedEOF) && err != io.EOF {
		t.Fatalf("err %v", err)
	}
}

func TestCryptMatchesCImplant(t *testing.T) {
	// C: d[i] ^= xkey[(i + seed) & 31] with seed = type
	buf := []byte{0x00, 0x01, 0x02, 0x03}
	Crypt(buf, CmdExec) // 0x02
	if buf[0] != (0x00 ^ XKey[(0+2)&31]) {
		t.Fatalf("byte0 %02x", buf[0])
	}
	Crypt(buf, CmdExec)
	if !bytes.Equal(buf, []byte{0x00, 0x01, 0x02, 0x03}) {
		t.Fatalf("not involution %x", buf)
	}
}

func TestRegisterRoundTrip(t *testing.T) {
	in := Register{
		Ver:      1,
		PID:      4242,
		Hostname: "WIN-TEST",
		Username: "alice",
		OS:       "windows",
		Arch:     "x64",
		IP:       "10.211.55.3",
	}
	out, err := ParseRegister(in.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("%+v vs %+v", out, in)
	}
}

func TestRegisterEmptyStrings(t *testing.T) {
	in := Register{Ver: 1, PID: 1}
	out, err := ParseRegister(in.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if out.PID != 1 || out.Hostname != "" {
		t.Fatalf("%+v", out)
	}
}

func TestParseRegisterShort(t *testing.T) {
	if _, err := ParseRegister([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseRegisterTruncatedString(t *testing.T) {
	b := []byte{1, 0, 0, 0, 0, 5, 0, 'a', 'b'} // hostname len=5 but only 2 bytes
	if _, err := ParseRegister(b); err == nil {
		t.Fatal("expected error")
	}
}
