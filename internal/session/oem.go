package session

import (
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func decodeOEM(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if utf8.Valid(b) {
		return string(b)
	}
	out, err := simplifiedchinese.GB18030.NewDecoder().Bytes(b)
	if err != nil || len(out) == 0 {
		return string(b)
	}
	return string(out)
}
