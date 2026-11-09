package val

import (
	"strconv"
	"sync"
	"unicode/utf8"
	"unsafe"
)

const (
	fnvOffset32 uint32 = 2166136261
	fnvPrime32  uint32 = 16777619
)

// HashString computes the 32-bit FNV-1a hash of a string.
// Guarantees a non-zero hash value so 0 can represent "uncomputed" in ObjectHeader.Hash.
func HashString(s string) uint32 {
	h := fnvOffset32
	for i := range len(s) {
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
	for i := range n {
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
	for i := range n {
		num |= uint64(s[i]) << ((i + 1) * 8)
	}
	return Value{
		num: num,
		ptr: &tagInlineStr,
	}
}

// SmallStringFromBytes packs a byte slice of length 0 <= len(b) <= 7 into Value.num (0 allocations).
// Byte 0 holds uint64(len(b)), and bytes 1..7 hold the characters.
// Panics if len(b) > 7.
func SmallStringFromBytes(b []byte) Value {
	n := len(b)
	if n > 7 {
		panic("val.SmallStringFromBytes: byte slice length exceeds 7 bytes")
	}
	num := uint64(n)
	for i := range n {
		num |= uint64(b[i]) << ((i + 1) * 8)
	}
	return Value{
		num: num,
		ptr: &tagInlineStr,
	}
}

// HashBytes computes the 32-bit FNV-1a hash of a byte slice.
func HashBytes(b []byte) uint32 {
	h := fnvOffset32
	for i := range b {
		h ^= uint32(b[i])
		h *= fnvPrime32
	}
	if h == 0 {
		return 1
	}
	return h
}

// AppendValueBytes appends the UTF-8 bytes of string-like Value v into dst without heap allocations.
// If v is KindInlineStr, it unpacks the 1–7 bytes directly from v.num using bit shifts.
// If v is KindString (*PyString), it views the underlying bytes via unsafe.Slice without copying.
func AppendValueBytes(dst []byte, v Value) ([]byte, bool) {
	if v.ptr == &tagInlineStr {
		n := int(v.num & 0xFF)
		for i := range n {
			dst = append(dst, byte(v.num>>((i+1)*8)))
		}
		return dst, true
	}
	if v.ptr != nil && v.ptr.Kind == KindString {
		ps := (*PyString)(unsafe.Pointer(v.ptr))
		return append(dst, ps.Data...), true
	}
	if v.ptr != nil && v.ptr.Kind == KindInstance {
		inst := (*PyInstance)(unsafe.Pointer(v.ptr))
		if inst.Header.Type != nil && StrType != nil && IsSubclass(inst.Header.Type, StrType) {
			if len(inst.Slots) > 0 && inst.Slots[0].IsAnyString() {
				return AppendValueBytes(dst, inst.Slots[0])
			}
		}
	}
	return dst, false
}

// StringByteLen returns the byte length of a KindInlineStr or KindString Value (0 allocations).
func (v Value) StringByteLen() int {
	if v.ptr == &tagInlineStr {
		return int(v.num & 0xFF)
	}
	if v.ptr != nil && v.ptr.Kind == KindString {
		return len((*PyString)(unsafe.Pointer(v.ptr)).Data)
	}
	if v.ptr != nil && v.ptr.Kind == KindInstance {
		inst := (*PyInstance)(unsafe.Pointer(v.ptr))
		if inst.Header.Type != nil && StrType != nil && IsSubclass(inst.Header.Type, StrType) {
			if len(inst.Slots) > 0 && inst.Slots[0].IsAnyString() {
				return inst.Slots[0].StringByteLen()
			}
		}
	}
	return 0
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
			Type: StrType,
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
		if (a.ptr.Flags & b.ptr.Flags & FlagInterned) != 0 {
			return false
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
	for i := range n {
		if s[i] != byte(num>>((i+1)*8)) {
			return false
		}
	}
	return true
}

// StringLen returns the character length of a KindInlineStr or KindString Value (0 allocations for ASCII).
func (v Value) StringLen() int {
	s := v.AsString()
	if isASCII(s) {
		return len(s)
	}
	return utf8.RuneCountInString(s)
}

// AsString returns a Go string for a KindInlineStr or KindString Value.
func (v Value) AsString() string {
	if v.ptr == nil {
		return ""
	}
	if v.ptr == &tagInlineStr {
		n := int(v.num & 0xFF)
		if n == 0 {
			return ""
		}
		var buf [7]byte
		for i := range n {
			buf[i] = byte(v.num >> ((i + 1) * 8))
		}
		return string(buf[:n])
	}
	if v.ptr.Kind == KindString {
		return (*PyString)(unsafe.Pointer(v.ptr)).Data
	}
	if v.ptr.Kind == KindInstance {
		inst := (*PyInstance)(unsafe.Pointer(v.ptr))
		if inst.Header.Type != nil && StrType != nil && IsSubclass(inst.Header.Type, StrType) {
			if len(inst.Slots) > 0 && inst.Slots[0].IsAnyString() {
				return inst.Slots[0].AsString()
			}
		}
	}
	return ""
}

const internShardCount = 64

type internShard struct {
	mu      sync.RWMutex
	buckets []*PyString
	mask    uint32
	count   int
}

func (s *internShard) grow() {
	newCap := max(32, uint32(len(s.buckets)*2))
	newBuckets := make([]*PyString, newCap)
	newMask := newCap - 1
	for _, b := range s.buckets {
		if b == nil {
			continue
		}
		idx := b.Header.Hash & newMask
		for newBuckets[idx] != nil {
			idx = (idx + 1) & newMask
		}
		newBuckets[idx] = b
	}
	s.buckets = newBuckets
	s.mask = newMask
}

// InternPool manages a sharded open-addressed table of canonicalized heap *PyString objects
// with FlagInterned set on ObjectHeader.Flags, providing scalable concurrent interning.
type InternPool struct {
	shards [internShardCount]internShard
}

// NewInternPool constructs a new InternPool with 64 independent shards.
func NewInternPool(capacity int) *InternPool {
	shardCap := uint32(32)
	perShard := capacity / internShardCount
	for int(shardCap)*2 < perShard*3 {
		shardCap <<= 1
	}
	p := &InternPool{}
	for i := range internShardCount {
		p.shards[i] = internShard{
			buckets: make([]*PyString, shardCap),
			mask:    shardCap - 1,
		}
	}
	return p
}

// Intern returns a canonicalized string Value for s.
// Strings <= 7 bytes return a 0-allocation KindInlineStr.
// Strings >= 8 bytes return a heap-allocated KindString with FlagInterned set.
func (p *InternPool) Intern(s string) Value {
	if len(s) <= 7 {
		return SmallStringVal(s)
	}
	h := HashString(s)
	shardIdx := h & (internShardCount - 1)
	shard := &p.shards[shardIdx]

	shard.mu.RLock()
	if len(shard.buckets) > 0 {
		idx := h & shard.mask
		for {
			b := shard.buckets[idx]
			if b == nil {
				break
			}
			if b.Header.Hash == h && b.Data == s {
				shard.mu.RUnlock()
				return Value{num: 0, ptr: &b.Header}
			}
			idx = (idx + 1) & shard.mask
		}
	}
	shard.mu.RUnlock()

	shard.mu.Lock()
	defer shard.mu.Unlock()

	if len(shard.buckets) == 0 {
		shard.buckets = make([]*PyString, 32)
		shard.mask = 31
	}

	idx := h & shard.mask
	for {
		b := shard.buckets[idx]
		if b == nil {
			break
		}
		if b.Header.Hash == h && b.Data == s {
			return Value{num: 0, ptr: &b.Header}
		}
		idx = (idx + 1) & shard.mask
	}

	if (shard.count+1)*3 > len(shard.buckets)*2 {
		shard.grow()
		idx = h & shard.mask
		for shard.buckets[idx] != nil {
			idx = (idx + 1) & shard.mask
		}
	}

	obj := &PyString{
		Header: ObjectHeader{
			Kind:  KindString,
			Flags: FlagInterned,
			Hash:  h,
			Type:  StrType,
		},
		Data: s,
	}
	shard.buckets[idx] = obj
	shard.count++
	return Value{num: 0, ptr: &obj.Header}
}

// InternBytes returns a canonicalized string Value for byte slice b.
// Strings <= 7 bytes return a 0-allocation KindInlineStr.
// Strings >= 8 bytes return a heap-allocated KindString with FlagInterned set.
func (p *InternPool) InternBytes(b []byte) Value {
	if len(b) <= 7 {
		return SmallStringFromBytes(b)
	}
	h := HashBytes(b)
	shardIdx := h & (internShardCount - 1)
	shard := &p.shards[shardIdx]

	shard.mu.RLock()
	if len(shard.buckets) > 0 {
		idx := h & shard.mask
		for {
			bucket := shard.buckets[idx]
			if bucket == nil {
				break
			}
			if bucket.Header.Hash == h && bucket.Data == string(b) {
				shard.mu.RUnlock()
				return Value{num: 0, ptr: &bucket.Header}
			}
			idx = (idx + 1) & shard.mask
		}
	}
	shard.mu.RUnlock()

	shard.mu.Lock()
	defer shard.mu.Unlock()

	if len(shard.buckets) == 0 {
		shard.buckets = make([]*PyString, 32)
		shard.mask = 31
	}

	idx := h & shard.mask
	for {
		bucket := shard.buckets[idx]
		if bucket == nil {
			break
		}
		if bucket.Header.Hash == h && bucket.Data == string(b) {
			return Value{num: 0, ptr: &bucket.Header}
		}
		idx = (idx + 1) & shard.mask
	}

	if (shard.count+1)*3 > len(shard.buckets)*2 {
		shard.grow()
		idx = h & shard.mask
		for shard.buckets[idx] != nil {
			idx = (idx + 1) & shard.mask
		}
	}

	str := string(b)
	obj := &PyString{
		Header: ObjectHeader{
			Kind:  KindString,
			Flags: FlagInterned,
			Hash:  h,
			Type:  StrType,
		},
		Data: str,
	}
	shard.buckets[idx] = obj
	shard.count++

	return Value{num: 0, ptr: &obj.Header}
}

// InternValue returns a canonicalized string Value for v.
// If v is already KindInlineStr or a KindString with FlagInterned set, it returns v with 0 allocations.
func (p *InternPool) InternValue(v Value) Value {
	if v.ptr == &tagInlineStr {
		return v
	}
	if v.ptr.Kind == KindString {
		if (v.ptr.Flags & FlagInterned) != 0 {
			return v
		}
		return p.Intern((*PyString)(unsafe.Pointer(v.ptr)).Data)
	}
	panic("val.InternValue: value is not a string")
}

var defaultInternPool = NewInternPool(256)

// InternString interns s in the process-wide default InternPool.
func InternString(s string) Value {
	return defaultInternPool.Intern(s)
}

// InternValue canonicalizes a string Value in the process-wide default InternPool.
func InternValue(v Value) Value {
	return defaultInternPool.InternValue(v)
}

// IntToStringVal formats an integer to a string Value.
// For numbers 0 <= n <= 9,999,999 (and small negative numbers), it packs the ASCII digits directly
// into a KindInlineStr (0 allocations).
func IntToStringVal(n int64) Value {
	if n >= 0 && n <= 9999999 {
		var buf [8]byte
		i := len(buf)
		temp := n
		if temp == 0 {
			i--
			buf[i] = '0'
		} else {
			for temp > 0 {
				i--
				buf[i] = byte('0' + (temp % 10))
				temp /= 10
			}
		}
		return SmallStringFromBytes(buf[i:])
	}
	if n < 0 && n >= -999999 {
		var buf [8]byte
		i := len(buf)
		temp := -n
		for temp > 0 {
			i--
			buf[i] = byte('0' + (temp % 10))
			temp /= 10
		}
		i--
		buf[i] = '-'
		return SmallStringFromBytes(buf[i:])
	}
	var stackBuf [32]byte
	b := strconv.AppendInt(stackBuf[:0], n, 10)
	dataBuf := make([]byte, len(b))
	copy(dataBuf, b)
	s := unsafe.String(unsafe.SliceData(dataBuf), len(b))
	ps := &PyString{
		Header: ObjectHeader{
			Kind: KindString,
			Hash: HashString(s),
			Type: StrType,
		},
		Data: s,
	}
	return Value{num: 0, ptr: &ps.Header}
}

// ConcatStrings concatenates two string-like Values.
// If the combined length <= 7, it returns a KindInlineStr with 0 allocations.
// For larger strings, it copies directly into an allocated buffer with extra headroom
// for chained in-place additions, avoiding redundant Go string copies.
func ConcatStrings(a, b Value) Value {
	lenA := a.StringByteLen()
	lenB := b.StringByteLen()
	totalLen := lenA + lenB
	if totalLen <= 7 {
		var buf [8]byte
		dst, _ := AppendValueBytes(buf[:0], a)
		dst, _ = AppendValueBytes(dst, b)
		return SmallStringFromBytes(dst)
	}
	capSize := max(128, totalLen)
	buf := make([]byte, totalLen, capSize)
	var pos int
	switch {
	case a.ptr == &tagInlineStr:
		n := int(a.num & 0xFF)
		for i := range n {
			buf[i] = byte(a.num >> ((i + 1) * 8))
		}
		pos = n
	case a.ptr.Kind == KindString:
		data := (*PyString)(unsafe.Pointer(a.ptr)).Data
		copy(buf, data)
		pos = len(data)
	default:
		s := a.AsString()
		copy(buf, s)
		pos = len(s)
	}
	switch {
	case b.ptr == &tagInlineStr:
		n := int(b.num & 0xFF)
		for i := range n {
			buf[pos+i] = byte(b.num >> ((i + 1) * 8))
		}
	case b.ptr.Kind == KindString:
		data := (*PyString)(unsafe.Pointer(b.ptr)).Data
		copy(buf[pos:], data)
	default:
		s := b.AsString()
		copy(buf[pos:], s)
	}
	s := unsafe.String(unsafe.SliceData(buf), totalLen)
	ps := &PyString{
		Header: ObjectHeader{
			Kind: KindString,
			Hash: HashString(s),
			Type: StrType,
		},
		Data: s,
		Buf:  buf,
	}
	return Value{num: 0, ptr: &ps.Header}
}
