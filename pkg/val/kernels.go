package val

import (
	"encoding/binary"
	"math/bits"
	"unsafe"
)

// IsASCII reports whether every byte in s is < 0x80.
func IsASCII(s string) bool {
	if len(s) == 0 {
		return true
	}
	return isASCIIBytes(unsafe.Slice(unsafe.StringData(s), len(s)))
}

// IsASCIIBytes reports whether every byte in b is < 0x80.
func IsASCIIBytes(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	return isASCIIBytes(b)
}

// IsASCIIString reports whether string-like Value v consists exclusively of ASCII bytes.
func IsASCIIString(v Value) bool {
	if v.ptr == &tagInlineStr {
		return (v.num & 0x8080808080808000) == 0
	}
	if v.ptr != nil && v.ptr.Kind == KindString {
		return IsASCII((*PyString)(unsafe.Pointer(v.ptr)).Data)
	}
	if v.ptr != nil && v.ptr.Kind == KindInstance {
		inst := (*PyInstance)(unsafe.Pointer(v.ptr))
		if inst.Header.Type != nil && StrType != nil && IsSubclass(inst.Header.Type, StrType) {
			if len(inst.Slots) > 0 && inst.Slots[0].IsAnyString() {
				return IsASCIIString(inst.Slots[0])
			}
		}
	}
	return false
}

// FindJSONEscape returns the byte index of the first byte in b that requires
// JSON escaping (< 0x20, '"', or '\\'), or len(b) if all bytes are clean.
func FindJSONEscape(b []byte) int {
	return findJSONEscape(b)
}

// FindJSONEscapeASCII returns the byte index of the first byte in b that requires
// JSON escaping under ensure_ascii=True (< 0x20, '"', '\\', or >= 0x80), or len(b).
func FindJSONEscapeASCII(b []byte) int {
	return findJSONEscapeASCII(b)
}

func isASCIIBytesScalar(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 {
			return false
		}
	}
	return true
}

func findJSONEscapeScalar(b []byte) int {
	for i, c := range b {
		if c < 0x20 || c == '"' || c == '\\' {
			return i
		}
	}
	return len(b)
}

func findJSONEscapeASCIIScalar(b []byte) int {
	for i, c := range b {
		if c < 0x20 || c == '"' || c == '\\' || c >= 0x80 {
			return i
		}
	}
	return len(b)
}

const (
	swarLo80  uint64 = 0x8080808080808080
	swarLo01  uint64 = 0x0101010101010101
	swarLo20  uint64 = 0x2020202020202020
	swarQuote uint64 = 0x2222222222222222
	swarSlash uint64 = 0x5c5c5c5c5c5c5c5c
)

func isASCIIBytesSWAR(b []byte) bool {
	n := len(b)
	i := 0
	for ; i+32 <= n; i += 32 {
		w0 := binary.LittleEndian.Uint64(b[i:])
		w1 := binary.LittleEndian.Uint64(b[i+8:])
		w2 := binary.LittleEndian.Uint64(b[i+16:])
		w3 := binary.LittleEndian.Uint64(b[i+24:])
		if (w0|w1|w2|w3)&swarLo80 != 0 {
			return false
		}
	}
	for ; i+8 <= n; i += 8 {
		if binary.LittleEndian.Uint64(b[i:])&swarLo80 != 0 {
			return false
		}
	}
	for ; i < n; i++ {
		if b[i] >= 0x80 {
			return false
		}
	}
	return true
}

func findJSONEscapeSWAR(b []byte) int {
	n := len(b)
	i := 0
	for ; i+8 <= n; i += 8 {
		w := binary.LittleEndian.Uint64(b[i:])
		xq := w ^ swarQuote
		xs := w ^ swarSlash
		m := ((xq - swarLo01) & ^xq & swarLo80) |
			((xs - swarLo01) & ^xs & swarLo80) |
			((w - swarLo20) & ^w & swarLo80)
		if m != 0 {
			return i + (bits.TrailingZeros64(m) >> 3)
		}
	}
	return i + findJSONEscapeScalar(b[i:])
}

func findJSONEscapeASCIISWAR(b []byte) int {
	n := len(b)
	i := 0
	for ; i+8 <= n; i += 8 {
		w := binary.LittleEndian.Uint64(b[i:])
		xq := w ^ swarQuote
		xs := w ^ swarSlash
		m := ((xq - swarLo01) & ^xq & swarLo80) |
			((xs - swarLo01) & ^xs & swarLo80) |
			(((w - swarLo20) | w) & swarLo80)
		if m != 0 {
			return i + (bits.TrailingZeros64(m) >> 3)
		}
	}
	return i + findJSONEscapeASCIIScalar(b[i:])
}
