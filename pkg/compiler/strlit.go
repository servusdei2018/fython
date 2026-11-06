package compiler

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"github.com/servusdei2018/fython/pkg/unicodedata"
)

// splitLiteral separates a string-literal token (prefix letters, quotes, body)
// into its raw/bytes flags and the body between the quotes.
func splitLiteral(s string) (body string, raw, isBytes bool) {
	i := 0
	for i < len(s) && s[i] != '\'' && s[i] != '"' {
		switch s[i] {
		case 'r', 'R':
			raw = true
		case 'b', 'B':
			isBytes = true
		}
		i++
	}
	s = s[i:]
	if len(s) >= 6 && (strings.HasPrefix(s, `"""`) || strings.HasPrefix(s, `'''`)) && s[len(s)-3:] == s[:3] {
		return s[3 : len(s)-3], raw, isBytes
	}
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1], raw, isBytes
	}
	return s, raw, isBytes
}

// decodeStringToken decodes a complete str literal token (with prefix and quotes).
func decodeStringToken(tok string) string {
	body, raw, _ := splitLiteral(tok)
	if raw {
		return body
	}
	return string(decodeEscapes([]byte(body), false))
}

// decodeBytesToken decodes a complete bytes literal token.
func decodeBytesToken(tok string) []byte {
	body, raw, _ := splitLiteral(tok)
	if raw {
		return []byte(body)
	}
	return decodeEscapes([]byte(body), true)
}

// decodeFStringPart decodes a literal chunk of an f/t-string (no quotes).
func decodeFStringPart(s string, raw bool) string {
	if s == "{{" || s == "}}" {
		return s[:1]
	}
	if raw {
		return s
	}
	return string(decodeEscapes([]byte(s), false))
}

func isOct(c byte) bool { return c >= '0' && c <= '7' }

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

func parseHexN(s []byte, i, n int) (int, bool) {
	if i+n > len(s) {
		return 0, false
	}
	v := 0
	for k := 0; k < n; k++ {
		h := hexVal(s[i+k])
		if h < 0 {
			return 0, false
		}
		v = v<<4 | h
	}
	return v, true
}

func decodeEscapes(s []byte, isBytes bool) []byte {
	if !containsByte(s, '\\') {
		return s
	}
	out := make([]byte, 0, len(s))
	putRune := func(r int) {
		if isBytes {
			out = append(out, byte(r))
			return
		}
		if r >= 0xD800 && r <= 0xDFFF {
			r = utf8.RuneError
		}
		out = utf8.AppendRune(out, rune(r))
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			out = append(out, c)
			continue
		}
		i++
		switch e := s[i]; e {
		case '\n':
		case '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
		case '\\', '\'', '"':
			out = append(out, e)
		case 'a':
			out = append(out, 7)
		case 'b':
			out = append(out, 8)
		case 'f':
			out = append(out, 12)
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'v':
			out = append(out, 11)
		case '0', '1', '2', '3', '4', '5', '6', '7':
			v := int(e - '0')
			for k := 0; k < 2 && i+1 < len(s) && isOct(s[i+1]); k++ {
				i++
				v = v*8 + int(s[i]-'0')
			}
			if isBytes {
				v &= 0xFF
			}
			putRune(v)
		case 'x':
			if v, ok := parseHexN(s, i+1, 2); ok {
				i += 2
				putRune(v)
			} else {
				out = append(out, '\\', 'x')
			}
		case 'u', 'U':
			if isBytes {
				out = append(out, '\\', e)
				break
			}
			n := 4
			if e == 'U' {
				n = 8
			}
			v, ok := parseHexN(s, i+1, n)
			if !ok || v > 0x10FFFF {
				out = append(out, '\\', e)
				break
			}
			i += n
			if v >= 0xD800 && v < 0xDC00 && i+6 < len(s) && s[i+1] == '\\' && s[i+2] == 'u' {
				if lo, ok := parseHexN(s, i+3, 4); ok && lo >= 0xDC00 && lo <= 0xDFFF {
					v = 0x10000 + (v-0xD800)<<10 + (lo - 0xDC00)
					i += 6
				}
			}
			putRune(v)
		case 'N':
			if isBytes {
				out = append(out, '\\', 'N')
				break
			}
			if i+1 < len(s) && s[i+1] == '{' {
				if end := bytes.IndexByte(s[i+2:], '}'); end >= 0 {
					if r, ok := unicodedata.Lookup(string(s[i+2 : i+2+end])); ok {
						i += 2 + end
						putRune(int(r))
						break
					}
				}
			}
			out = append(out, '\\', 'N')
		default:
			out = append(out, '\\', e)
		}
	}
	return out
}

func containsByte(s []byte, b byte) bool {
	for _, c := range s {
		if c == b {
			return true
		}
	}
	return false
}

func isBytesToken(s string) bool {
	for i := 0; i < len(s) && i < 2 && s[i] != '\'' && s[i] != '"'; i++ {
		if s[i] == 'b' || s[i] == 'B' {
			return true
		}
	}
	return false
}
