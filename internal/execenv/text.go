package execenv

import (
	"bytes"
	"encoding/binary"
	"strings"
	"unicode/utf16"
)

// decodeWSL turns wsl.exe's own output into text. wsl.exe writes UTF-16LE
// when its output is a pipe, unless WSL_UTF8=1 is honoured (WSL 0.64 and
// later); a Linux program run through it writes whatever it writes, normally
// UTF-8. Both are accepted, with or without a byte order mark.
func decodeWSL(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte{0xff, 0xfe}):
		return utf16le(b[2:])
	case bytes.HasPrefix(b, []byte{0xef, 0xbb, 0xbf}):
		return string(b[3:])
	case looksUTF16LE(b):
		return utf16le(b)
	}
	return string(b)
}

// looksUTF16LE recognises UTF-16LE without a byte order mark: in the mostly
// ASCII text wsl.exe prints, most odd bytes are zero, which UTF-8 text never has.
func looksUTF16LE(b []byte) bool {
	if len(b) < 2 {
		return false
	}
	zeros, pairs := 0, len(b)/2
	for i := 1; i < len(b); i += 2 {
		if b[i] == 0 && b[i-1] != 0 {
			zeros++
		}
	}
	return zeros*2 > pairs
}

func utf16le(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, binary.LittleEndian.Uint16(b[i:]))
	}
	return string(utf16.Decode(u))
}

// lines splits decoded output into trimmed, non-empty lines.
func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		if l = strings.TrimSpace(strings.Trim(l, "\x00")); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// firstLine is the first non-empty line, for an error message.
func firstLine(b []byte) string {
	ls := lines(decodeWSL(b))
	if len(ls) == 0 {
		return ""
	}
	if r := []rune(ls[0]); len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return ls[0]
}
