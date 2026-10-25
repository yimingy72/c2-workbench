package protocol

import (
	"encoding/binary"
	"strconv"
	"strings"
)

func EncodeGetArg(offset, maxlen int64, path string) []byte {
	if offset < 0 {
		offset = 0
	}
	if maxlen < 0 {
		maxlen = 0
	}
	return []byte(strconv.FormatInt(offset, 10) + "\n" + strconv.FormatInt(maxlen, 10) + "\n" + path)
}

func ParseGetArg(arg string) (offset, maxlen int64, path string) {
	arg = strings.TrimRight(arg, "\x00")
	p1 := strings.IndexByte(arg, '\n')
	if p1 < 0 {
		return 0, 0, arg
	}
	p2 := strings.IndexByte(arg[p1+1:], '\n')
	if p2 < 0 {
		return 0, 0, arg
	}
	p2 += p1 + 1
	off, err1 := strconv.ParseInt(arg[:p1], 10, 64)
	max, err2 := strconv.ParseInt(arg[p1+1:p2], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, arg
	}
	if off < 0 {
		off = 0
	}
	if max < 0 {
		max = 0
	}
	return off, max, arg[p2+1:]
}

func PutOffset(payload []byte) (path string, offset int64, ok bool) {
	if len(payload) < 3 || payload[0] != 0 {
		return "", 0, false
	}
	n := int(binary.LittleEndian.Uint16(payload[1:3]))
	if n == 0 || 3+n > len(payload) {
		return "", 0, false
	}
	path = string(payload[3 : 3+n])
	if 3+n+8 <= len(payload) {
		offset = int64(binary.LittleEndian.Uint64(payload[3+n : 3+n+8]))
	}
	return path, offset, true
}
