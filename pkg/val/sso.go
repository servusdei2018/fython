package val

import (
	"bytes"
	"hash/maphash"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
	"unsafe"
)

var strHashSeed = maphash.MakeSeed()

func finishHash64(h uint64) uint32 {
	r := uint32(h ^ (h >> 32))
	if r == 0 {
		return 1
	}
	return r
}

// HashString computes a 32-bit hash of a string using Go's hardware-accelerated maphash.
// Guarantees a non-zero hash value so 0 can represent "uncomputed" in ObjectHeader.Hash.
func HashString(s string) uint32 {
	return finishHash64(maphash.String(strHashSeed, s))
}

// InlineStrHash computes the 32-bit hash directly from a KindInlineStr Value (0 allocations).
func InlineStrHash(v Value) uint32 {
	num := v.num
	n := int(num & 0xFF)
	var buf [7]byte
	for i := range n {
		buf[i] = byte(num >> ((i + 1) * 8))
	}
	return finishHash64(maphash.Bytes(strHashSeed, buf[:n]))
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

// HashBytes computes a 32-bit hash of a byte slice using Go's hardware-accelerated maphash.
func HashBytes(b []byte) uint32 {
	return finishHash64(maphash.Bytes(strHashSeed, b))
}

// inlineStrSlice unpacks inline string bytes into buf and returns the subslice.
// Unlike unsafe.String, returning a slice of buf passed directly to bytes.* or
// Builder.Write does not cause buf to escape when the slice is not retained.
func inlineStrSlice(v Value, buf *[7]byte) []byte {
	n := int(v.num & 0xFF)
	for i := range n {
		buf[i] = byte(v.num >> ((i + 1) * 8))
	}
	return buf[:n]
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

// StringLen returns the character length of a KindInlineStr, KindString, or str subclass Value (0 allocations).
func (v Value) StringLen() int {
	if v.ptr == &tagInlineStr {
		if (v.num & 0x8080808080808000) == 0 {
			return int(v.num & 0xFF)
		}
		var buf [7]byte
		return utf8.RuneCount(inlineStrSlice(v, &buf))
	}
	if v.ptr != nil && v.ptr.Kind == KindString {
		s := (*PyString)(unsafe.Pointer(v.ptr)).Data
		if isASCII(s) {
			return len(s)
		}
		return utf8.RuneCountInString(s)
	}
	var buf [7]byte
	if b := valueStringBytes(v, &buf); b != nil {
		for _, c := range b {
			if c >= 0x80 {
				return utf8.RuneCount(b)
			}
		}
		return len(b)
	}
	s := v.AsString()
	if isASCII(s) {
		return len(s)
	}
	return utf8.RuneCountInString(s)
}

// WriteToBuilder writes the UTF-8 bytes of string-like Value v into b without heap allocations.
func (v Value) WriteToBuilder(b *strings.Builder) bool {
	if v.ptr == &tagInlineStr {
		var buf [7]byte
		b.Write(inlineStrSlice(v, &buf))
		return true
	}
	if v.ptr != nil && v.ptr.Kind == KindString {
		b.WriteString((*PyString)(unsafe.Pointer(v.ptr)).Data)
		return true
	}
	if v.ptr != nil && v.ptr.Kind == KindInstance {
		inst := (*PyInstance)(unsafe.Pointer(v.ptr))
		if inst.Header.Type != nil && StrType != nil && IsSubclass(inst.Header.Type, StrType) {
			if len(inst.Slots) > 0 && inst.Slots[0].IsAnyString() {
				inner := inst.Slots[0]
				if inner.ptr == &tagInlineStr {
					var buf [7]byte
					b.Write(inlineStrSlice(inner, &buf))
					return true
				}
				if inner.ptr != nil && inner.ptr.Kind == KindString {
					b.WriteString((*PyString)(unsafe.Pointer(inner.ptr)).Data)
					return true
				}
				b.WriteString(inner.AsString())
				return true
			}
		}
	}
	return false
}

func valueStringBytes(v Value, buf *[7]byte) []byte {
	if v.ptr == &tagInlineStr {
		return inlineStrSlice(v, buf)
	}
	if v.ptr != nil && v.ptr.Kind == KindString {
		s := (*PyString)(unsafe.Pointer(v.ptr)).Data
		return unsafe.Slice(unsafe.StringData(s), len(s))
	}
	if v.ptr != nil && v.ptr.Kind == KindInstance {
		inst := (*PyInstance)(unsafe.Pointer(v.ptr))
		if inst.Header.Type != nil && StrType != nil && IsSubclass(inst.Header.Type, StrType) {
			if len(inst.Slots) > 0 {
				inner := inst.Slots[0]
				if inner.ptr == &tagInlineStr {
					return inlineStrSlice(inner, buf)
				}
				if inner.ptr != nil && inner.ptr.Kind == KindString {
					s := (*PyString)(unsafe.Pointer(inner.ptr)).Data
					return unsafe.Slice(unsafe.StringData(s), len(s))
				}
			}
		}
	}
	return nil
}

// StringCompare lexicographically compares two string Values (0 allocations).
func StringCompare(a, b Value) int {
	if a.ptr == &tagInlineStr && b.ptr == &tagInlineStr && a.num == b.num {
		return 0
	}
	var bufA, bufB [7]byte
	return bytes.Compare(valueStringBytes(a, &bufA), valueStringBytes(b, &bufB))
}

// StringContains reports whether item is a substring of container (0 allocations).
func StringContains(container, item Value) bool {
	var bufA, bufB [7]byte
	return bytes.Contains(valueStringBytes(container, &bufA), valueStringBytes(item, &bufB))
}

// IsInlineStrASCII reports whether v is a KindInlineStr containing only ASCII bytes (< 0x80).
func IsInlineStrASCII(v Value) bool {
	return v.ptr == &tagInlineStr && (v.num&0x8080808080808000) == 0
}

// SliceInlineStrASCII returns the byte slice [start:stop] of an ASCII KindInlineStr directly
// from v.num using bit shifts (0 allocations).
func SliceInlineStrASCII(v Value, start, stop int) Value {
	n := int(v.num & 0xFF)
	if start < 0 {
		start = 0
	}
	if stop > n {
		stop = n
	}
	if start >= stop {
		return Value{num: 0, ptr: &tagInlineStr}
	}
	slen := stop - start
	payload := (v.num >> uint(start*8)) & (^uint64(0) >> uint((7-slen)*8))
	return Value{
		num: (payload &^ 0xFF) | uint64(slen),
		ptr: &tagInlineStr,
	}
}

// ascii1Backing holds every single-byte string; ascii2Backing holds every
// two-byte ASCII string. Lookups slice these immutable strings, so no
// per-string allocation or header table is needed.
var (
	ascii1Backing = buildAscii1()
	ascii2Backing = buildAscii2()
)

func buildAscii1() string {
	b := make([]byte, 256)
	for i := range b {
		b[i] = byte(i)
	}
	return unsafe.String(unsafe.SliceData(b), len(b))
}

func buildAscii2() string {
	b := make([]byte, 0, 128*128*2)
	for i := range 128 {
		for j := range 128 {
			b = append(b, byte(i), byte(j))
		}
	}
	return unsafe.String(unsafe.SliceData(b), len(b))
}

func ascii1(b byte) string {
	return unsafe.String((*byte)(unsafe.Add(unsafe.Pointer(unsafe.StringData(ascii1Backing)), int(b))), 1)
}

func ascii2(b0, b1 byte) string {
	o := (int(b0)<<7 | int(b1)) << 1
	return unsafe.String((*byte)(unsafe.Add(unsafe.Pointer(unsafe.StringData(ascii2Backing)), o)), 2)
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
		if n == 1 {
			return ascii1(byte(v.num >> 8))
		}
		if n == 2 {
			b0 := byte(v.num >> 8)
			b1 := byte(v.num >> 16)
			if b0 < 128 && b1 < 128 {
				return ascii2(b0, b1)
			}
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
	if v.ptr != nil && v.ptr.Kind == KindString {
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

// InternBytes interns byte slice b in the process-wide default InternPool.
func InternBytes(b []byte) Value {
	return defaultInternPool.InternBytes(b)
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

// TryConcatInPlace concatenates the heap string a with b, reusing the spare
// capacity that a's backing buffer was allocated with.
//
// Python strings are immutable, so the bytes a already exposes must never
// change. The returned Value is therefore always a freshly allocated
// *PyString header that owns the grown buffer; a keeps its own Data, and
// hence its original length and bytes, so every alias of a keeps observing
// the original string.
//
// The backing buffer is claimed with a compare-and-swap, so at most one
// goroutine can ever read or write it: concurrent concatenations of the same
// string fall back to ConcatStrings instead of racing on the shared bytes.
//
// ok reports whether the buffer could be reused; when it is false the caller
// must fall back to ConcatStrings.
func TryConcatInPlace(a, b Value) (Value, bool) {
	if a.ptr == nil || a.ptr.Kind != KindString {
		return NoneVal(), false
	}
	psA := (*PyString)(unsafe.Pointer(a.ptr))
	oldLen := len(psA.Data)
	bLen := b.StringByteLen()
	// Claim the buffer before touching it: from here on exactly one
	// goroutine owns Buf, so the bytes behind a are never mutated twice.
	if !atomic.CompareAndSwapUint32(&psA.BufClaimed, 0, 1) {
		return NoneVal(), false
	}
	buf := psA.Buf
	if buf == nil || len(buf) < oldLen || cap(buf) < oldLen+bLen {
		return NoneVal(), false
	}
	grown, ok := AppendValueBytes(buf[:oldLen], b)
	if !ok || len(grown) != oldLen+bLen {
		return NoneVal(), false
	}
	s := unsafe.String(unsafe.SliceData(grown), len(grown))
	ps := &PyString{
		Header: ObjectHeader{
			Kind: KindString,
			Hash: HashString(s),
			Type: StrType,
		},
		Data: s,
		Buf:  grown,
	}
	return Value{num: 0, ptr: &ps.Header}, true
}

// DetachStringBuf releases the spare capacity of a heap string so that the
// string can no longer be extended in place.
//
// It is called whenever a string becomes reachable from more than one place:
// stored into a variable, a container, an attribute, or duplicated on the
// evaluation stack. The buffer is claimed first, so the release is performed
// by at most one goroutine and never races with a concurrent concatenation.
func DetachStringBuf(v Value) {
	if v.ptr == nil || v.ptr.Kind != KindString {
		return
	}
	ps := (*PyString)(unsafe.Pointer(v.ptr))
	if atomic.CompareAndSwapUint32(&ps.BufClaimed, 0, 1) {
		ps.Buf = nil
	}
}

// StringEquals reports whether v holds the string s. It never allocates, so it
// can be used on the hot paths that compare a constant against a name.
func (v Value) StringEquals(s string) bool {
	if v.ptr == nil {
		return s == ""
	}
	if v.ptr == &tagInlineStr {
		n := int(v.num & 0xFF)
		if n != len(s) {
			return false
		}
		for i := range n {
			if byte(v.num>>((i+1)*8)) != s[i] {
				return false
			}
		}
		return true
	}
	if v.ptr.Kind == KindString {
		return (*PyString)(unsafe.Pointer(v.ptr)).Data == s
	}
	return false
}

// StringHasBytePrefix reports whether string Value v begins with byte b (0 allocations).
func (v Value) StringHasBytePrefix(b byte) bool {
	if v.ptr == &tagInlineStr {
		return (v.num&0xFF) > 0 && byte(v.num>>8) == b
	}
	if v.ptr != nil && v.ptr.Kind == KindString {
		s := (*PyString)(unsafe.Pointer(v.ptr)).Data
		return len(s) > 0 && s[0] == b
	}
	return false
}

// StringStartsWithTwoBytes reports whether string Value v begins with bytes b0, b1 (0 allocations).
func (v Value) StringStartsWithTwoBytes(b0, b1 byte) bool {
	if v.ptr == &tagInlineStr {
		return (v.num&0xFF) >= 2 && byte(v.num>>8) == b0 && byte(v.num>>16) == b1
	}
	if v.ptr != nil && v.ptr.Kind == KindString {
		s := (*PyString)(unsafe.Pointer(v.ptr)).Data
		return len(s) >= 2 && s[0] == b0 && s[1] == b1
	}
	return false
}
