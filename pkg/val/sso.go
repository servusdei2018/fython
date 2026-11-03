package val

import "unsafe"

const (
	fnvOffset32 uint32 = 2166136261
	fnvPrime32  uint32 = 16777619
)

// HashString computes the 32-bit FNV-1a hash of a string.
// Guarantees a non-zero hash value so 0 can represent "uncomputed" in ObjectHeader.Hash.
func HashString(s string) uint32 {
	h := fnvOffset32
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= fnvPrime32
	}
	if h == 0 {
		return 1
	}
	return h
}

// InlineStrHash computes the 32-bit FNV-1a hash directly from a KindInlineStr Value (0 allocations).
func InlineStrHash(v Value) uint32 {
	num := v.num
	n := int(num & 0xFF)
	h := fnvOffset32
	for i := 0; i < n; i++ {
		b := uint8(num >> ((i + 1) * 8))
		h ^= uint32(b)
		h *= fnvPrime32
	}
	if h == 0 {
		return 1
	}
	return h
}

// SmallStringVal packs a string of length 0 <= len(s) <= 7 into Value.num (0 allocations).
// Byte 0 holds uint64(len(s)), and bytes 1..7 hold the string characters.
// Panics if len(s) > 7.
func SmallStringVal(s string) Value {
	n := len(s)
	if n > 7 {
		panic("val.SmallStringVal: string length exceeds 7 bytes")
	}
	num := uint64(n)
	for i := 0; i < n; i++ {
		num |= uint64(s[i]) << ((i + 1) * 8)
	}
	return Value{
		num: num,
		ptr: &tagInlineStr,
	}
}

// NewStringVal routes strings of length <= 7 bytes to SmallStringVal (0 allocations)
// and strings > 7 bytes to a heap-allocated *PyString.
func NewStringVal(s string) Value {
	if len(s) <= 7 {
		return SmallStringVal(s)
	}
	obj := &PyString{
		Header: ObjectHeader{
			Kind: KindString,
			Hash: HashString(s),
		},
		Data: s,
	}
	return Value{
		num: 0,
		ptr: &obj.Header,
	}
}

// StringEquals compares two string Values for equality.
// Fast path: if both are KindInlineStr, returns a.num == b.num in a single 64-bit comparison (0 allocations).
func StringEquals(a, b Value) bool {
	if a.ptr == &tagInlineStr && b.ptr == &tagInlineStr {
		return a.num == b.num
	}
	if a.ptr.Kind == KindString && b.ptr.Kind == KindString {
		if a.ptr == b.ptr {
			return true
		}
		sa := (*PyString)(unsafe.Pointer(a.ptr))
		sb := (*PyString)(unsafe.Pointer(b.ptr))
		if sa.Header.Hash != 0 && sb.Header.Hash != 0 && sa.Header.Hash != sb.Header.Hash {
			return false
		}
		return sa.Data == sb.Data
	}
	// Mixed inline and heap string (in case a PyString was constructed with len <= 7).
	if a.ptr == &tagInlineStr && b.ptr.Kind == KindString {
		return inlineEqualsHeapString(a.num, (*PyString)(unsafe.Pointer(b.ptr)).Data)
	}
	if a.ptr.Kind == KindString && b.ptr == &tagInlineStr {
		return inlineEqualsHeapString(b.num, (*PyString)(unsafe.Pointer(a.ptr)).Data)
	}
	return false
}

func inlineEqualsHeapString(num uint64, s string) bool {
	n := int(num & 0xFF)
	if len(s) != n {
		return false
	}
	for i := 0; i < n; i++ {
		if s[i] != byte(num>>((i+1)*8)) {
			return false
		}
	}
	return true
}

// StringLen returns the byte length of a KindInlineStr or KindString Value (0 allocations).
func (v Value) StringLen() int {
	if v.ptr == &tagInlineStr {
		return int(v.num & 0xFF)
	}
	if v.ptr.Kind == KindString {
		return len((*PyString)(unsafe.Pointer(v.ptr)).Data)
	}
	return 0
}

// AsString returns a Go string for a KindInlineStr or KindString Value.
func (v Value) AsString() string {
	if v.ptr == &tagInlineStr {
		n := int(v.num & 0xFF)
		if n == 0 {
			return ""
		}
		var buf [7]byte
		for i := 0; i < n; i++ {
			buf[i] = byte(v.num >> ((i + 1) * 8))
		}
		return string(buf[:n])
	}
	if v.ptr.Kind == KindString {
		return (*PyString)(unsafe.Pointer(v.ptr)).Data
	}
	return ""
}
