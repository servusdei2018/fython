package vm

import (
	"slices"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/sre"
	"github.com/servusdei2018/fython/pkg/val"
)

// Native `_sre` / `re` implementation on top of the pkg/sre backtracking
// engine. Offsets exposed to Python are character indices for str and byte
// indices for bytes.

var (
	PatternType             *val.PyType
	MatchType               *val.PyType
	ScannerType             *val.PyType
	PatternErrorType        *val.PyType
	patternMatchBuiltin     val.Value
	patternSearchBuiltin    val.Value
	patternFullMatchBuiltin val.Value
	sreOnce                 sync.Once
)

const flagMatchASCII uint8 = 1 << 1

type pyPattern struct {
	Header  val.ObjectHeader
	Source  val.Value
	Re      *sre.Regexp
	IsBytes bool
}

type pyMatch struct {
	Header     val.ObjectHeader
	Pat        *pyPattern
	StringVal  val.Value
	extCaps    []int32
	inlineCaps [12]int32
	Pos        int32
	EndPos     int32
	LastIndex  int32
	numCaps    int32
}

var (
	_ [0 - unsafe.Offsetof(pyMatch{}.Header)]byte
	_ [128 - unsafe.Sizeof(pyMatch{})]byte
	_ [unsafe.Sizeof(pyMatch{}) - 128]byte
)

type pyScanner struct {
	Header      val.ObjectHeader
	PatternVal  val.Value
	Pat         *pyPattern
	StringVal   val.Value
	In          []rune
	InASCII     []byte
	inlineASCII [7]byte
	Pos         int
	EndPos      int
	next        int
	mustAdv     bool
	done        bool
}

func asPattern(v val.Value) *pyPattern {
	if v.Kind() != val.KindNative || v.HeaderPtr().Type != PatternType {
		panic("TypeError: descriptor requires a 're.Pattern' object")
	}
	return (*pyPattern)(unsafe.Pointer(v.HeaderPtr()))
}

func asMatch(v val.Value) *pyMatch {
	if v.Kind() != val.KindNative || v.HeaderPtr().Type != MatchType {
		panic("TypeError: descriptor requires a 're.Match' object")
	}
	return (*pyMatch)(unsafe.Pointer(v.HeaderPtr()))
}

func asScanner(v val.Value) *pyScanner {
	return (*pyScanner)(unsafe.Pointer(v.HeaderPtr()))
}

// bindArgs resolves positional and keyword arguments against names. Missing
// parameters are left Unbound. Returns a fixed-size value array to avoid heap allocations.
func bindArgs(fn string, args, kwnames []val.Value, names ...string) [8]val.Value {
	var out [8]val.Value
	for i := range names {
		out[i] = val.UnboundVal()
	}
	if len(kwnames) == 0 {
		if len(args) > len(names) {
			panic("TypeError: " + fn + "() takes at most " + strconv.Itoa(len(names)) + " arguments (" + strconv.Itoa(len(args)) + " given)")
		}
		copy(out[:len(args)], args)
		return out
	}
	npos := len(args) - len(kwnames)
	if npos > len(names) {
		panic("TypeError: " + fn + "() takes at most " + strconv.Itoa(len(names)) + " arguments (" + strconv.Itoa(npos) + " given)")
	}
	copy(out[:npos], args[:npos])
	for i, kn := range kwnames {
		name := kn.AsString()
		idx := -1
		for j, n := range names {
			if n == name {
				idx = j
				break
			}
		}
		if idx < 0 {
			panic("TypeError: " + fn + "() got an unexpected keyword argument '" + name + "'")
		}
		if !out[idx].IsUnbound() {
			panic("TypeError: argument for " + fn + "() given by name ('" + name + "') and position (" + strconv.Itoa(idx+1) + ")")
		}
		out[idx] = args[npos+i]
	}
	return out
}

func required(fn string, v val.Value, name string) val.Value {
	if v.IsUnbound() {
		panic("TypeError: " + fn + "() missing required argument '" + name + "'")
	}
	return v
}

func intArg(ctx *val.ExecContext, v val.Value, def int64) int64 {
	if v.IsUnbound() || v.IsNone() {
		return def
	}
	if n, ok := intOf(ctx, v); ok {
		return n
	}
	ctx.Raise(val.NewException(val.TypeErrorType, "'"+typeNameOf(v)+"' object cannot be interpreted as an integer"))
	return 0
}

func clampInt(n int64, lo, hi int) int {
	if n < int64(lo) {
		return lo
	}
	if n > int64(hi) {
		return hi
	}
	return int(n)
}

// stringRunes returns a cached []rune view for a string Value when possible.
func stringRunes(v val.Value) []rune {
	if v.Kind() == val.KindString {
		return (*val.PyString)(unsafe.Pointer(v.HeaderPtr())).Runes()
	}
	if v.IsInstance() {
		inst := v.AsInstance()
		if len(inst.Slots) > 0 && inst.Slots[0].IsAnyString() {
			return stringRunes(inst.Slots[0])
		}
	}
	return []rune(v.AsString())
}

// subject converts a str / bytes-like object to runes, enforcing that its type
// agrees with the pattern.
func (p *pyPattern) subject(v val.Value) []rune {
	switch {
	case v.IsAnyString():
		if p.IsBytes {
			panic("TypeError: cannot use a bytes pattern on a string-like object")
		}
		return stringRunes(v)
	case v.IsInstance() && val.IsSubclass(v.Type(), val.StrType):
		if p.IsBytes {
			panic("TypeError: cannot use a bytes pattern on a string-like object")
		}
		inst := v.AsInstance()
		if len(inst.Slots) > 0 && inst.Slots[0].IsAnyString() {
			return stringRunes(inst.Slots[0])
		}
		return []rune(v.String())
	case v.IsBytes() || v.IsByteArray() || v.IsMemoryView():
		if !p.IsBytes {
			panic("TypeError: cannot use a string pattern on a bytes-like object")
		}
		if v.IsBytes() {
			return v.AsBytes().Runes()
		}
		var data []byte
		switch {
		case v.IsByteArray():
			data = v.AsByteArray().Data
		default:
			data = v.AsMemoryView().Buf
		}
		rs := make([]rune, len(data))
		for i, b := range data {
			rs[i] = rune(b)
		}
		return rs
	case v.IsInstance() && val.IsSubclass(v.Type(), val.BytesType):
		if !p.IsBytes {
			panic("TypeError: cannot use a string pattern on a bytes-like object")
		}
		inst := v.AsInstance()
		if len(inst.Slots) > 0 && inst.Slots[0].IsBytes() {
			return inst.Slots[0].AsBytes().Runes()
		}
		return []rune{}
	}
	panic("TypeError: expected string or bytes-like object, got '" + typeNameOf(v) + "'")
}

// subjectMatchFast decodes strVal for non-reentrant match calls into per-VM
// reusable rune storage so repeated matches never allocate heap buffers.
func (vm *VM) subjectMatchFast(v val.Value) []rune {
	if val.IsInlineStrASCII(v) {
		raw := v.RawNum()
		n := int(raw & 0xFF)
		for i := range n {
			vm.reInlineBuf[i] = rune((raw >> ((uint(i) + 1) * 8)) & 0xFF)
		}
		return vm.reInlineBuf[:n]
	}
	if v.IsString() {
		ps := v.AsStringObj()
		if ps.Buf == nil {
			s := ps.Data
			n := len(s)
			if ps == vm.lastReStr && n == vm.lastReStrLen {
				return vm.lastReRunes
			}
			if n == 0 {
				return vm.reInlineBuf[:0]
			}
			if cap(vm.lastReRunes) < n {
				capNeed := max(n, 64)
				vm.lastReRunes = make([]rune, n, capNeed)
			} else {
				vm.lastReRunes = vm.lastReRunes[:n]
			}
			dst := vm.lastReRunes
			ascii := true
			for i := range n {
				b := s[i]
				if b >= 0x80 {
					ascii = false
					break
				}
				dst[i] = rune(b)
			}
			if !ascii {
				dst = dst[:0]
				for _, r := range s {
					dst = append(dst, r)
				}
				vm.lastReRunes = dst
			}
			vm.lastReStr = ps
			vm.lastReStrLen = n
			return dst
		}
	}
	return nil
}

func (vm *VM) isPyStringASCII(ps *val.PyString) bool {
	n := len(ps.Data)
	for i := range vm.reASCIICache {
		e := &vm.reASCIICache[i]
		if e.ps == ps && e.strLen == n {
			return e.isASCII
		}
	}
	ascii := val.IsASCII(ps.Data)
	idx := vm.reASCIICachePos & 3
	vm.reASCIICache[idx] = reASCIIEntry{ps: ps, strLen: n, isASCII: ascii}
	vm.reASCIICachePos = idx + 1
	return ascii
}

func asciiSubjectBytesCtx(ctx *val.ExecContext, v val.Value, inlineBuf *[7]byte) ([]byte, bool) {
	if val.IsInlineStrASCII(v) {
		raw := v.RawNum()
		n := int(raw & 0xFF)
		for i := range n {
			inlineBuf[i] = byte((raw >> ((uint(i) + 1) * 8)) & 0xFF)
		}
		return inlineBuf[:n], true
	}
	if v.IsString() {
		ps := v.AsStringObj()
		if ps.Buf == nil {
			if ctx != nil && !val.MultiThreadedFlag.Load() {
				if vm, ok := ctx.VM.(*VM); ok && vm != nil {
					if vm.isPyStringASCII(ps) {
						return unsafe.Slice(unsafe.StringData(ps.Data), len(ps.Data)), true
					}
					return nil, false
				}
			}
			if val.IsASCII(ps.Data) {
				return unsafe.Slice(unsafe.StringData(ps.Data), len(ps.Data)), true
			}
		}
		return nil, false
	}
	if v.IsInstance() && val.IsSubclass(v.Type(), val.StrType) {
		inst := v.AsInstance()
		if len(inst.Slots) > 0 && inst.Slots[0].IsAnyString() {
			return asciiSubjectBytesCtx(ctx, inst.Slots[0], inlineBuf)
		}
	}
	return nil, false
}

func sliceASCIIVal(strVal val.Value, in []byte, s, e int) val.Value {
	if val.IsInlineStrASCII(strVal) {
		return val.SliceInlineStrASCII(strVal, s, e)
	}
	if s == 0 && e == len(in) && strVal.IsAnyString() && strVal.Type() == val.StrType {
		return strVal
	}
	if e-s <= 7 {
		return val.SmallStringFromBytes(in[s:e])
	}
	return val.NewStringVal(string(in[s:e]))
}

func (p *pyPattern) subjectInto(v val.Value, buf *[7]rune) []rune {
	if !p.IsBytes && val.IsInlineStrASCII(v) {
		raw := v.RawNum()
		n := int(raw & 0xFF)
		for i := range n {
			buf[i] = rune((raw >> ((uint(i) + 1) * 8)) & 0xFF)
		}
		return buf[:n]
	}
	return p.subject(v)
}

func (p *pyPattern) sliceSubject(strVal val.Value, in []rune, s, e int) val.Value {
	if !p.IsBytes && val.IsInlineStrASCII(strVal) {
		return val.SliceInlineStrASCII(strVal, s, e)
	}
	if !p.IsBytes && strVal.IsAnyString() {
		orig := strVal.AsString()
		if len(orig) == len(in) {
			if s == 0 && e == len(orig) && strVal.Type() == val.StrType {
				return strVal
			}
			return val.NewStringVal(orig[s:e])
		}
	}
	return p.fromRunes(in[s:e])
}

func (p *pyPattern) fromRunes(rs []rune) val.Value {
	if p.IsBytes {
		b := make([]byte, len(rs))
		for i, r := range rs {
			b[i] = byte(r)
		}
		return val.NewBytesVal(b)
	}
	return val.NewStringVal(string(rs))
}

func raiseReError(ctx *val.ExecContext, e *sre.Error, pattern val.Value) {
	ctx.Raise(newPatternError(e.Msg, pattern, e.Pos))
}

func newPatternError(msg string, pattern val.Value, pos int) val.Value {
	exc := val.NewException(PatternErrorType, msg)
	inst := exc.AsInstance()
	full := msg
	posV, linenoV, colnoV := val.NoneVal(), val.NoneVal(), val.NoneVal()
	if !pattern.IsNone() && pos >= 0 {
		full = msg + " at position " + strconv.Itoa(pos)
		posV = val.Int64Val(int64(pos))
		var text []rune
		if pattern.IsAnyString() {
			text = []rune(pattern.AsString())
		} else if pattern.IsBytes() {
			for _, b := range pattern.AsBytes().Data {
				text = append(text, rune(b))
			}
		}
		if pos > len(text) {
			pos = len(text)
		}
		lineno, last := 1, -1
		for i := range pos {
			if text[i] == '\n' {
				lineno++
				last = i
			}
		}
		colno := pos - last
		linenoV, colnoV = val.Int64Val(int64(lineno)), val.Int64Val(int64(colno))
		if slices.Contains(text, '\n') {
			full += " (line " + strconv.Itoa(lineno) + ", column " + strconv.Itoa(colno) + ")"
		}
	}
	inst.SetAttr(nameArgs, val.NewTupleVal(val.NewStringVal(full)))
	inst.SetAttr(nameMsg, val.NewStringVal(msg))
	inst.SetAttr(namePattern, pattern)
	inst.SetAttr(namePos, posV)
	inst.SetAttr(nameLineno, linenoV)
	inst.SetAttr(nameColno, colnoV)
	return exc
}

type patKey struct {
	pat     string
	flags   int
	isBytes bool
}

var (
	patCache   = map[patKey]*pyPattern{}
	patCacheMu sync.Mutex
)

const patCacheMax = 512

// compilePattern compiles (or fetches from cache) a pattern from a str/bytes value.
func compilePattern(ctx *val.ExecContext, pattern val.Value, flags int64) val.Value {
	initSRETypes()
	if pattern.Kind() == val.KindNative && pattern.HeaderPtr().Type == PatternType {
		if flags != 0 {
			ctx.Raise(val.NewException(val.ValueErrorType, "cannot process flags argument with a compiled pattern"))
			return val.NoneVal()
		}
		return pattern
	}
	var src []rune
	isBytes := false
	switch {
	case pattern.IsAnyString():
		src = []rune(pattern.AsString())
	case pattern.IsBytes() || pattern.IsByteArray():
		isBytes = true
		var data []byte
		if pattern.IsBytes() {
			data = pattern.AsBytes().Data
		} else {
			data = pattern.AsByteArray().Data
		}
		src = make([]rune, len(data))
		for i, b := range data {
			src[i] = rune(b)
		}
	default:
		ctx.Raise(val.NewException(val.TypeErrorType, "first argument must be string or compiled pattern"))
		return val.NoneVal()
	}
	key := patKey{pat: string(src), flags: int(flags), isBytes: isBytes}
	patCacheMu.Lock()
	if p, ok := patCache[key]; ok {
		patCacheMu.Unlock()
		return val.ObjectVal(&p.Header)
	}
	patCacheMu.Unlock()
	re, err := sre.Compile(src, int(flags), isBytes)
	if err != nil {
		if se, ok := err.(*sre.Error); ok {
			raiseReError(ctx, se, pattern)
			return val.NoneVal()
		}
		ctx.Raise(newPatternError(err.Error(), pattern, -1))
		return val.NoneVal()
	}
	srcVal := pattern
	if pattern.IsByteArray() {
		srcVal = val.NewBytesVal(append([]byte(nil), pattern.AsByteArray().Data...))
	}
	p := &pyPattern{
		Header:  val.ObjectHeader{Kind: val.KindNative, Type: PatternType},
		Source:  srcVal,
		Re:      re,
		IsBytes: isBytes,
	}
	patCacheMu.Lock()
	if len(patCache) >= patCacheMax {
		patCache = map[patKey]*pyPattern{}
	}
	patCache[key] = p
	patCacheMu.Unlock()
	return val.ObjectVal(&p.Header)
}

func purgeCache() {
	patCacheMu.Lock()
	patCache = map[patKey]*pyPattern{}
	patCacheMu.Unlock()
}

func newMatch(ctx *val.ExecContext, p *pyPattern, strVal val.Value, caps []int32, lastIndex, pos, endpos int, flags uint8) val.Value {
	var m *pyMatch
	if ctx != nil && !val.MultiThreadedFlag.Load() {
		if vm, ok := ctx.VM.(*VM); ok && vm != nil {
			m = vm.acquireMatch()
		}
	}
	if m == nil {
		m = &pyMatch{}
	}
	m.Header = val.ObjectHeader{Kind: val.KindNative, Flags: flags, Type: MatchType}
	m.Pat = p
	m.StringVal = strVal
	m.Pos = int32(pos)
	m.EndPos = int32(endpos)
	m.LastIndex = int32(lastIndex)
	m.numCaps = int32(len(caps))
	if len(caps) <= len(m.inlineCaps) {
		copy(m.inlineCaps[:], caps)
		m.extCaps = nil
	} else {
		c := make([]int32, len(caps))
		copy(c, caps)
		m.extCaps = c
	}
	return val.ObjectVal(&m.Header)
}

func (m *pyMatch) capBounds(idx int) (int, int) {
	if m.extCaps == nil {
		return int(m.inlineCaps[2*idx]), int(m.inlineCaps[2*idx+1])
	}
	return int(m.extCaps[2*idx]), int(m.extCaps[2*idx+1])
}

func (m *pyMatch) allCaps() []int32 {
	if m.extCaps == nil {
		return m.inlineCaps[:m.numCaps]
	}
	return m.extCaps
}

func (m *pyMatch) group(ctx *val.ExecContext, g val.Value) int {
	idx := -1
	switch {
	case g.IsInt64() || g.IsBool():
		idx = int(g.AsInt64())
	case g.IsAnyString():
		if gi, ok := m.Pat.Re.GroupIndex[g.AsString()]; ok {
			idx = gi
		}
	default:
		if n, ok := intOf(ctx, g); ok {
			idx = int(n)
		}
	}
	if idx < 0 || idx > m.Pat.Re.NumGroups {
		ctx.Raise(val.NewException(val.IndexErrorType, "no such group"))
		return 0
	}
	return idx
}

func (m *pyMatch) groupVal(idx int, def val.Value) val.Value {
	s, e := m.capBounds(idx)
	if s < 0 || e < 0 {
		return def
	}
	if !m.Pat.IsBytes {
		if val.IsInlineStrASCII(m.StringVal) {
			return val.SliceInlineStrASCII(m.StringVal, s, e)
		}
		if (m.Header.Flags&flagMatchASCII) != 0 && m.StringVal.IsString() {
			ps := m.StringVal.AsStringObj()
			if s == 0 && e == len(ps.Data) && m.StringVal.Type() == val.StrType {
				return m.StringVal
			}
			if e-s >= 0 && e-s <= 7 {
				return val.SmallStringVal(ps.Data[s:e])
			}
			return val.NewStringVal(ps.Data[s:e])
		}
	}
	return m.Pat.sliceSubject(m.StringVal, m.Pat.subject(m.StringVal), s, e)
}

func (m *pyMatch) lastGroupName() val.Value {
	if m.LastIndex < 0 {
		return val.NoneVal()
	}
	if name := m.Pat.Re.GroupNames[m.LastIndex]; name != "" {
		return val.NewStringVal(name)
	}
	return val.NoneVal()
}

func (p *pyPattern) posArgs(ctx *val.ExecContext, in []rune, posV, endV val.Value) (int, int) {
	pos := clampInt(intArg(ctx, posV, 0), 0, len(in))
	end := clampInt(intArg(ctx, endV, int64(len(in))), 0, len(in))
	return pos, end
}

//go:noinline
func (p *pyPattern) doMatchInlineASCII(ctx *val.ExecContext, mode sre.Mode, strVal, posV, endV val.Value, boolOnly bool, extraFlags uint8) val.Value {
	var inlineBuf [7]byte
	raw := strVal.RawNum()
	n := int(raw & 0xFF)
	for i := range n {
		inlineBuf[i] = byte((raw >> ((uint(i) + 1) * 8)) & 0xFF)
	}
	inBytes := inlineBuf[:n]
	pos := clampInt(intArg(ctx, posV, 0), 0, n)
	end := clampInt(intArg(ctx, endV, int64(n)), 0, n)
	var capsBuf [32]int32
	caps, last, matched := p.Re.ExecASCIIInto(inBytes, pos, end, mode, false, capsBuf[:0])
	if !matched {
		return val.NoneVal()
	}
	if boolOnly {
		return val.BoolVal(true)
	}
	return newMatch(ctx, p, strVal, caps, last, pos, end, extraFlags)
}

func (p *pyPattern) doMatch(ctx *val.ExecContext, mode sre.Mode, strVal, posV, endV val.Value) val.Value {
	var boolOnly bool
	var extraFlags uint8
	if ctx != nil && !val.MultiThreadedFlag.Load() {
		if vm, ok := ctx.VM.(*VM); ok && vm != nil {
			if vm.matchBoolOnly {
				boolOnly = true
				vm.matchBoolOnly = false
			} else if vm.matchEphemeral {
				extraFlags = val.FlagEphemeralMatch
				vm.matchEphemeral = false
			}
		}
	}
	if !p.IsBytes {
		switch {
		case strVal.IsString():
			ps := strVal.AsStringObj()
			if ps.Buf == nil {
				var isASCII bool
				if ctx != nil && !val.MultiThreadedFlag.Load() {
					if vm, ok := ctx.VM.(*VM); ok && vm != nil {
						isASCII = vm.isPyStringASCII(ps)
					} else {
						isASCII = val.IsASCII(ps.Data)
					}
				} else {
					isASCII = val.IsASCII(ps.Data)
				}
				if isASCII {
					inBytes := unsafe.Slice(unsafe.StringData(ps.Data), len(ps.Data))
					pos := clampInt(intArg(ctx, posV, 0), 0, len(inBytes))
					end := clampInt(intArg(ctx, endV, int64(len(inBytes))), 0, len(inBytes))
					var capsBuf [32]int32
					caps, last, matched := p.Re.ExecASCIIInto(inBytes, pos, end, mode, false, capsBuf[:0])
					if !matched {
						return val.NoneVal()
					}
					if boolOnly {
						return val.BoolVal(true)
					}
					return newMatch(ctx, p, strVal, caps, last, pos, end, flagMatchASCII|extraFlags)
				}
			}
		case val.IsInlineStrASCII(strVal):
			return p.doMatchInlineASCII(ctx, mode, strVal, posV, endV, boolOnly, extraFlags)
		case strVal.IsInstance() && val.IsSubclass(strVal.Type(), val.StrType):
			inst := strVal.AsInstance()
			if len(inst.Slots) > 0 && inst.Slots[0].IsAnyString() {
				var inlineBuf [7]byte
				if inBytes, ok := asciiSubjectBytesCtx(ctx, inst.Slots[0], &inlineBuf); ok {
					pos := clampInt(intArg(ctx, posV, 0), 0, len(inBytes))
					end := clampInt(intArg(ctx, endV, int64(len(inBytes))), 0, len(inBytes))
					var capsBuf [32]int32
					caps, last, matched := p.Re.ExecASCIIInto(inBytes, pos, end, mode, false, capsBuf[:0])
					if !matched {
						return val.NoneVal()
					}
					if boolOnly {
						return val.BoolVal(true)
					}
					return newMatch(ctx, p, strVal, caps, last, pos, end, extraFlags)
				}
			}
		}
	}
	var in []rune
	if !p.IsBytes && ctx != nil && !val.MultiThreadedFlag.Load() &&
		(posV.IsNone() || posV.IsInt64()) && (endV.IsNone() || endV.IsInt64()) {
		if vm, ok := ctx.VM.(*VM); ok && vm != nil {
			in = vm.subjectMatchFast(strVal)
		}
	}
	if in == nil {
		in = p.subject(strVal)
	}
	pos, end := p.posArgs(ctx, in, posV, endV)
	var capsBuf [32]int32
	caps, last, ok := p.Re.ExecInto(in, pos, end, mode, false, capsBuf[:0])
	if !ok {
		return val.NoneVal()
	}
	if boolOnly {
		return val.BoolVal(true)
	}
	return newMatch(ctx, p, strVal, caps, last, pos, end, extraFlags)
}

func (p *pyPattern) iterateASCII(in []byte, pos, end int, fn func(caps []int32, last int) bool) {
	var capsBuf [32]int32
	mustAdv := false
	for {
		caps, last, ok := p.Re.ExecASCIIInto(in, pos, end, sre.ModeSearch, mustAdv, capsBuf[:0])
		if !ok {
			return
		}
		if !fn(caps, last) {
			return
		}
		pos = int(caps[1])
		mustAdv = caps[1] == caps[0]
	}
}

// iterate drives the CPython 3.7+ iteration protocol used by findall,
// finditer, sub and split: after an empty match the next match must advance.
func (p *pyPattern) iterate(in []rune, pos, end int, fn func(caps []int32, last int) bool) {
	var capsBuf [32]int32
	mustAdv := false
	for {
		caps, last, ok := p.Re.ExecInto(in, pos, end, sre.ModeSearch, mustAdv, capsBuf[:0])
		if !ok {
			return
		}
		if !fn(caps, last) {
			return
		}
		pos = int(caps[1])
		mustAdv = caps[1] == caps[0]
	}
}

func (p *pyPattern) findall(ctx *val.ExecContext, strVal, posV, endV val.Value) val.Value {
	if !p.IsBytes {
		var inlineBuf [7]byte
		if inBytes, ok := asciiSubjectBytesCtx(ctx, strVal, &inlineBuf); ok {
			pos := clampInt(intArg(ctx, posV, 0), 0, len(inBytes))
			end := clampInt(intArg(ctx, endV, int64(len(inBytes))), 0, len(inBytes))
			var list []val.Value
			ng := p.Re.NumGroups
			grp := func(caps []int32, i int) val.Value {
				s, e := int(caps[2*i]), int(caps[2*i+1])
				if s < 0 || e < 0 {
					return val.SmallStringVal("")
				}
				return sliceASCIIVal(strVal, inBytes, s, e)
			}
			p.iterateASCII(inBytes, pos, end, func(caps []int32, last int) bool {
				switch ng {
				case 0:
					list = append(list, sliceASCIIVal(strVal, inBytes, int(caps[0]), int(caps[1])))
				case 1:
					list = append(list, grp(caps, 1))
				default:
					items := make([]val.Value, ng)
					for i := 1; i <= ng; i++ {
						items[i-1] = grp(caps, i)
					}
					list = append(list, val.NewTupleFromSlice(items))
				}
				return true
			})
			return val.NewListVal(list...)
		}
	}
	var buf [7]rune
	in := p.subjectInto(strVal, &buf)
	pos, end := p.posArgs(ctx, in, posV, endV)
	var list []val.Value
	ng := p.Re.NumGroups
	grp := func(caps []int32, i int) val.Value {
		s, e := int(caps[2*i]), int(caps[2*i+1])
		if s < 0 || e < 0 {
			return p.fromRunes(nil)
		}
		return p.sliceSubject(strVal, in, s, e)
	}
	p.iterate(in, pos, end, func(caps []int32, last int) bool {
		switch ng {
		case 0:
			list = append(list, p.sliceSubject(strVal, in, int(caps[0]), int(caps[1])))
		case 1:
			list = append(list, grp(caps, 1))
		default:
			items := make([]val.Value, ng)
			for i := 1; i <= ng; i++ {
				items[i-1] = grp(caps, i)
			}
			list = append(list, val.NewTupleFromSlice(items))
		}
		return true
	})
	return val.NewListVal(list...)
}

func (p *pyPattern) split(ctx *val.ExecContext, strVal, maxV val.Value) val.Value {
	if !p.IsBytes {
		var inlineBuf [7]byte
		if inBytes, ok := asciiSubjectBytesCtx(ctx, strVal, &inlineBuf); ok {
			maxsplit := intArg(ctx, maxV, 0)
			if maxsplit < 0 {
				ctx.Raise(val.NewException(val.ValueErrorType, "maxsplit must be non-negative"))
				return val.NoneVal()
			}
			var out []val.Value
			lastPos := 0
			n := int64(0)
			p.iterateASCII(inBytes, 0, len(inBytes), func(caps []int32, last int) bool {
				if maxsplit > 0 && n >= maxsplit {
					return false
				}
				out = append(out, sliceASCIIVal(strVal, inBytes, lastPos, int(caps[0])))
				for i := 1; i <= p.Re.NumGroups; i++ {
					s, e := int(caps[2*i]), int(caps[2*i+1])
					if s < 0 || e < 0 {
						out = append(out, val.NoneVal())
					} else {
						out = append(out, sliceASCIIVal(strVal, inBytes, s, e))
					}
				}
				n++
				lastPos = int(caps[1])
				return true
			})
			out = append(out, sliceASCIIVal(strVal, inBytes, lastPos, len(inBytes)))
			return val.NewListVal(out...)
		}
	}
	var buf [7]rune
	in := p.subjectInto(strVal, &buf)
	maxsplit := intArg(ctx, maxV, 0)
	if maxsplit < 0 {
		ctx.Raise(val.NewException(val.ValueErrorType, "maxsplit must be non-negative"))
		return val.NoneVal()
	}
	var out []val.Value
	lastPos := 0
	n := int64(0)
	p.iterate(in, 0, len(in), func(caps []int32, last int) bool {
		if maxsplit > 0 && n >= maxsplit {
			return false
		}
		out = append(out, p.sliceSubject(strVal, in, lastPos, int(caps[0])))
		for i := 1; i <= p.Re.NumGroups; i++ {
			s, e := int(caps[2*i]), int(caps[2*i+1])
			if s < 0 || e < 0 {
				out = append(out, val.NoneVal())
			} else {
				out = append(out, p.sliceSubject(strVal, in, s, e))
			}
		}
		n++
		lastPos = int(caps[1])
		return true
	})
	out = append(out, p.sliceSubject(strVal, in, lastPos, len(in)))
	return val.NewListVal(out...)
}

func (p *pyPattern) sub(ctx *val.ExecContext, repl, strVal, countV val.Value) (val.Value, int64) {
	var buf [7]rune
	in := p.subjectInto(strVal, &buf)
	count := intArg(ctx, countV, 0)
	count = max(count, 0)
	var tmpl *sre.Template
	var literal []rune
	isLiteral := false
	isCallable := false
	switch {
	case repl.IsAnyString():
		if p.IsBytes {
			ctx.Raise(val.NewException(val.TypeErrorType, "expected a bytes-like object, str found"))
			return val.NoneVal(), 0
		}
		rs := []rune(repl.AsString())
		if !containsRune(rs, '\\') {
			literal, isLiteral = rs, true
		} else {
			t, err := p.Re.ParseTemplate(rs)
			if err != nil {
				raiseReError(ctx, err.(*sre.Error), repl)
				return val.NoneVal(), 0
			}
			tmpl = t
		}
	case repl.IsBytes() || repl.IsByteArray():
		if !p.IsBytes {
			ctx.Raise(val.NewException(val.TypeErrorType, "expected str instance, bytes found"))
			return val.NoneVal(), 0
		}
		var data []byte
		if repl.IsBytes() {
			data = repl.AsBytes().Data
		} else {
			data = repl.AsByteArray().Data
		}
		rs := make([]rune, len(data))
		for i, b := range data {
			rs[i] = rune(b)
		}
		if !containsRune(rs, '\\') {
			literal, isLiteral = rs, true
		} else {
			t, err := p.Re.ParseTemplate(rs)
			if err != nil {
				raiseReError(ctx, err.(*sre.Error), repl)
				return val.NoneVal(), 0
			}
			tmpl = t
		}
	default:
		isCallable = true
	}
	var out []rune
	lastPos := 0
	n := int64(0)
	p.iterate(in, 0, len(in), func(caps []int32, last int) bool {
		if count > 0 && n >= count {
			return false
		}
		out = append(out, in[lastPos:int(caps[0])]...)
		switch {
		case isCallable:
			mv := newMatch(ctx, p, strVal, caps, last, 0, len(in), val.FlagEphemeralMatch)
			res := ctx.Invoke1(repl, mv)
			if vm, ok := ctx.VM.(*VM); ok && vm != nil && !val.MultiThreadedFlag.Load() {
				m := (*pyMatch)(unsafe.Pointer(mv.HeaderPtr()))
				if (m.Header.Flags&val.FlagEphemeralMatch) != 0 && res.HeaderPtr() != &m.Header {
					vm.releaseMatch(m)
				}
			}
			out = append(out, p.valueRunes(res)...)
		case isLiteral:
			out = append(out, literal...)
		default:
			out = tmpl.ExpandInt32(out, in, caps)
		}
		lastPos = int(caps[1])
		n++
		return true
	})
	out = append(out, in[lastPos:]...)
	return p.fromRunes(out), n
}

// valueRunes converts a callable replacement result to runes.
func (p *pyPattern) valueRunes(v val.Value) []rune {
	if v.IsNone() {
		return nil
	}
	switch {
	case v.IsAnyString() && !p.IsBytes:
		return []rune(v.AsString())
	case v.IsBytes() && p.IsBytes:
		data := v.AsBytes().Data
		rs := make([]rune, len(data))
		for i, b := range data {
			rs[i] = rune(b)
		}
		return rs
	}
	if p.IsBytes {
		panic("TypeError: expected a bytes-like object, " + typeNameOf(v) + " found")
	}
	panic("TypeError: expected str instance, " + typeNameOf(v) + " found")
}

func containsRune(rs []rune, r rune) bool {
	return slices.Contains(rs, r)
}

func (sc *pyScanner) step(ctx *val.ExecContext, mode sre.Mode) val.Value {
	if sc.done || sc.next > sc.EndPos {
		return val.NoneVal()
	}
	var capsBuf [32]int32
	var caps []int32
	var last int
	var ok bool
	if sc.InASCII != nil {
		caps, last, ok = sc.Pat.Re.ExecASCIIInto(sc.InASCII, sc.next, sc.EndPos, mode, sc.mustAdv, capsBuf[:0])
	} else {
		caps, last, ok = sc.Pat.Re.ExecInto(sc.In, sc.next, sc.EndPos, mode, sc.mustAdv, capsBuf[:0])
	}
	if !ok {
		sc.done = true
		return val.NoneVal()
	}
	sc.mustAdv = caps[1] == caps[0]
	sc.next = int(caps[1])
	var flags uint8
	if sc.InASCII != nil && sc.StringVal.IsString() {
		flags = flagMatchASCII
	}
	return newMatch(ctx, sc.Pat, sc.StringVal, caps, last, sc.Pos, sc.EndPos, flags)
}

func (p *pyPattern) newScanner(ctx *val.ExecContext, patVal, strVal, posV, endV val.Value) *pyScanner {
	if !p.IsBytes {
		sc := &pyScanner{
			Header:     val.ObjectHeader{Kind: val.KindNative, Type: ScannerType},
			PatternVal: patVal,
			Pat:        p,
			StringVal:  strVal,
		}
		if inBytes, ok := asciiSubjectBytesCtx(ctx, strVal, &sc.inlineASCII); ok {
			pos := clampInt(intArg(ctx, posV, 0), 0, len(inBytes))
			end := clampInt(intArg(ctx, endV, int64(len(inBytes))), 0, len(inBytes))
			sc.InASCII = inBytes
			sc.Pos = pos
			sc.EndPos = end
			sc.next = pos
			return sc
		}
	}
	in := p.subject(strVal)
	pos, end := p.posArgs(ctx, in, posV, endV)
	return &pyScanner{
		Header:     val.ObjectHeader{Kind: val.KindNative, Type: ScannerType},
		PatternVal: patVal,
		Pat:        p,
		StringVal:  strVal,
		In:         in,
		Pos:        pos,
		EndPos:     end,
		next:       pos,
	}
}

var reFlagNames = []struct {
	flag int
	name string
}{
	{sre.FlagASCII, "re.ASCII"},
	{sre.FlagIgnoreCase, "re.IGNORECASE"},
	{sre.FlagLocale, "re.LOCALE"},
	{sre.FlagUnicode, "re.UNICODE"},
	{sre.FlagMultiline, "re.MULTILINE"},
	{sre.FlagDotAll, "re.DOTALL"},
	{sre.FlagVerbose, "re.VERBOSE"},
	{sre.FlagDebug, "re.DEBUG"},
}

func patternRepr(ctx *val.ExecContext, p *pyPattern) string {
	flags := p.Re.Flags
	if !p.IsBytes {
		flags &^= sre.FlagUnicode
	}
	var names []string
	for _, f := range reFlagNames {
		if flags&f.flag != 0 {
			names = append(names, f.name)
			flags &^= f.flag
		}
	}
	if flags != 0 {
		names = append(names, "0x"+strconv.FormatInt(int64(flags), 16))
	}
	pr := val.PyRepr(ctx, p.Source)
	if len(pr) > 200 {
		pr = pr[:200]
	}
	if len(names) == 0 {
		return "re.compile(" + pr + ")"
	}
	return "re.compile(" + pr + ", " + strings.Join(names, "|") + ")"
}

func method(t *val.PyType, name string, fn val.VectorcallFunc) {
	t.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal(name, fn))
}

func prop(t *val.PyType, name string, get func(self val.Value) val.Value) {
	getter := val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return get(args[0])
	})
	t.Dict.Set(val.InternString(name), val.NewPropertyVal(getter, val.NoneVal(), val.NoneVal()))
}

func initSRETypes() {
	sreOnce.Do(func() {
		PatternErrorType = val.MustNewType("PatternError", []*val.PyType{val.ExceptionType}, nil)
		PatternErrorType.Dict.Set(val.InternString("__module__"), val.NewStringVal("re"))
		method(PatternErrorType, "__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			b := bindArgs("PatternError", args[1:], kwnames, "msg", "pattern", "pos")
			msg := required("PatternError", b[0], "msg")
			pattern, pos := val.NoneVal(), -1
			if !b[1].IsUnbound() {
				pattern = b[1]
			}
			if !b[2].IsUnbound() && !b[2].IsNone() {
				pos = int(b[2].AsInt64())
			}
			ne := newPatternError(msg.AsString(), pattern, pos)
			self := args[0].AsInstance()
			for _, k := range [6]val.Value{nameArgs, nameMsg, namePattern, namePos, nameLineno, nameColno} {
				if v, ok := ne.AsInstance().GetAttr(k); ok {
					self.SetAttr(k, v)
				}
			}
			return val.NoneVal()
		})

		MatchType = val.MustNewType("Match", []*val.PyType{val.ObjectType}, nil)
		PatternType = val.MustNewType("Pattern", []*val.PyType{val.ObjectType}, nil)
		ScannerType = val.MustNewType("SRE_Scanner", []*val.PyType{val.ObjectType}, nil)
		for _, t := range []*val.PyType{MatchType, PatternType} {
			t.Dict.Set(val.InternString("__module__"), val.NewStringVal("re"))
		}

		// ---- Match ----
		method(MatchType, "group", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			m := asMatch(args[0])
			switch len(args) {
			case 1:
				return m.groupVal(0, val.NoneVal())
			case 2:
				return m.groupVal(m.group(ctx, args[1]), val.NoneVal())
			}
			items := make([]val.Value, 0, len(args)-1)
			for _, a := range args[1:] {
				items = append(items, m.groupVal(m.group(ctx, a), val.NoneVal()))
			}
			return val.NewTupleVal(items...)
		})
		method(MatchType, "__getitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			m := asMatch(args[0])
			return m.groupVal(m.group(ctx, args[1]), val.NoneVal())
		})
		MatchType.Slots.MpSubscript = func(ctx *val.ExecContext, o, key val.Value) val.Value {
			m := asMatch(o)
			return m.groupVal(m.group(ctx, key), val.NoneVal())
		}
		method(MatchType, "groups", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			m := asMatch(args[0])
			b := bindArgs("groups", args[1:], kwnames, "default")
			def := val.NoneVal()
			if !b[0].IsUnbound() {
				def = b[0]
			}
			n := m.Pat.Re.NumGroups
			items := make([]val.Value, n)
			for i := 1; i <= n; i++ {
				items[i-1] = m.groupVal(i, def)
			}
			return val.NewTupleVal(items...)
		})
		method(MatchType, "groupdict", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			m := asMatch(args[0])
			b := bindArgs("groupdict", args[1:], kwnames, "default")
			def := val.NoneVal()
			if !b[0].IsUnbound() {
				def = b[0]
			}
			res := val.NewDictVal(len(m.Pat.Re.GroupIndex))
			rd := res.AsDict()
			for gi := 1; gi <= m.Pat.Re.NumGroups; gi++ {
				if name := m.Pat.Re.GroupNames[gi]; name != "" {
					rd.Set(val.NewStringVal(name), m.groupVal(gi, def))
				}
			}
			return res
		})
		offsetFn := func(name string, f func(s, e int) val.Value) {
			method(MatchType, name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				m := asMatch(args[0])
				g := 0
				if len(kwnames) == 0 && len(args) <= 2 {
					if len(args) == 2 {
						g = m.group(ctx, args[1])
					}
				} else {
					b := bindArgs(name, args[1:], kwnames, "group")
					if !b[0].IsUnbound() {
						g = m.group(ctx, b[0])
					}
				}
				s, e := m.capBounds(g)
				return f(s, e)
			})
		}
		offsetFn("start", func(s, e int) val.Value { return val.Int64Val(int64(s)) })
		offsetFn("end", func(s, e int) val.Value { return val.Int64Val(int64(e)) })
		offsetFn("span", func(s, e int) val.Value { return val.NewTuple2(val.Int64Val(int64(s)), val.Int64Val(int64(e))) })
		method(MatchType, "expand", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			m := asMatch(args[0])
			b := bindArgs("expand", args[1:], kwnames, "template")
			tv := required("expand", b[0], "template")
			rs := m.Pat.valueRunes(tv)
			t, err := m.Pat.Re.ParseTemplate(rs)
			if err != nil {
				raiseReError(ctx, err.(*sre.Error), tv)
				return val.NoneVal()
			}
			var buf [7]rune
			in := m.Pat.subjectInto(m.StringVal, &buf)
			return m.Pat.fromRunes(t.ExpandInt32(nil, in, m.allCaps()))
		})
		method(MatchType, "__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			m := asMatch(args[0])
			s, e := m.capBounds(0)
			matched := val.PyRepr(ctx, m.groupVal(0, val.NoneVal()))
			return val.NewStringVal("<re.Match object; span=(" + strconv.Itoa(s) + ", " + strconv.Itoa(e) + "), match=" + matched + ">")
		})
		method(MatchType, "__copy__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return args[0]
		})
		method(MatchType, "__deepcopy__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return args[0]
		})
		prop(MatchType, "re", func(self val.Value) val.Value { return val.ObjectVal(&asMatch(self).Pat.Header) })
		prop(MatchType, "string", func(self val.Value) val.Value { return asMatch(self).StringVal })
		prop(MatchType, "pos", func(self val.Value) val.Value { return val.Int64Val(int64(asMatch(self).Pos)) })
		prop(MatchType, "endpos", func(self val.Value) val.Value { return val.Int64Val(int64(asMatch(self).EndPos)) })
		prop(MatchType, "lastindex", func(self val.Value) val.Value {
			m := asMatch(self)
			if m.LastIndex < 0 {
				return val.NoneVal()
			}
			return val.Int64Val(int64(m.LastIndex))
		})
		prop(MatchType, "lastgroup", func(self val.Value) val.Value { return asMatch(self).lastGroupName() })
		prop(MatchType, "regs", func(self val.Value) val.Value {
			m := asMatch(self)
			n := int(m.numCaps) / 2
			items := make([]val.Value, n)
			for i := range n {
				s, e := m.capBounds(i)
				items[i] = val.NewTuple2(val.Int64Val(int64(s)), val.Int64Val(int64(e)))
			}
			return val.NewTupleFromSlice(items)
		})

		// ---- Scanner ----
		ScannerType.Slots.TpIter = func(ctx *val.ExecContext, o val.Value) val.Value {
			return o
		}
		ScannerType.Slots.TpIterNext = func(ctx *val.ExecContext, o val.Value) (val.Value, val.IterStatus) {
			m := asScanner(o).step(ctx, sre.ModeSearch)
			if m.IsNone() {
				return val.NoneVal(), val.IterExhausted
			}
			return m, val.IterYielded
		}
		method(ScannerType, "__iter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return args[0]
		})
		method(ScannerType, "__next__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			m := asScanner(args[0]).step(ctx, sre.ModeSearch)
			if m.IsNone() {
				ctx.Raise(val.NewException(val.StopIterationType, ""))
				return val.NoneVal()
			}
			return m
		})
		method(ScannerType, "match", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return asScanner(args[0]).step(ctx, sre.ModeMatch)
		})
		method(ScannerType, "search", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return asScanner(args[0]).step(ctx, sre.ModeSearch)
		})
		prop(ScannerType, "pattern", func(self val.Value) val.Value { return asScanner(self).PatternVal })

		// ---- Pattern ----
		for _, spec := range []struct {
			name string
			mode sre.Mode
		}{{"match", sre.ModeMatch}, {"prefixmatch", sre.ModeMatch}, {"fullmatch", sre.ModeFullMatch}, {"search", sre.ModeSearch}} {
			mode := spec.mode
			name := spec.name
			fnVal := val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				p := asPattern(args[0])
				if len(kwnames) == 0 && len(args) >= 2 && len(args) <= 4 {
					var posV, endV val.Value
					if len(args) >= 3 {
						posV = args[2]
					}
					if len(args) == 4 {
						endV = args[3]
					}
					return p.doMatch(ctx, mode, args[1], posV, endV)
				}
				b := bindArgs(name, args[1:], kwnames, "string", "pos", "endpos")
				return p.doMatch(ctx, mode, required(name, b[0], "string"), b[1], b[2])
			})
			PatternType.Dict.Set(val.InternString(name), fnVal)
			switch name {
			case "match":
				patternMatchBuiltin = fnVal
			case "search":
				patternSearchBuiltin = fnVal
			case "fullmatch":
				patternFullMatchBuiltin = fnVal
			}
		}
		method(PatternType, "findall", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			p := asPattern(args[0])
			b := bindArgs("findall", args[1:], kwnames, "string", "pos", "endpos")
			return p.findall(ctx, required("findall", b[0], "string"), b[1], b[2])
		})
		method(PatternType, "finditer", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			p := asPattern(args[0])
			b := bindArgs("finditer", args[1:], kwnames, "string", "pos", "endpos")
			sc := p.newScanner(ctx, args[0], required("finditer", b[0], "string"), b[1], b[2])
			return val.ObjectVal(&sc.Header)
		})
		method(PatternType, "scanner", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			p := asPattern(args[0])
			b := bindArgs("scanner", args[1:], kwnames, "string", "pos", "endpos")
			sc := p.newScanner(ctx, args[0], required("scanner", b[0], "string"), b[1], b[2])
			return val.ObjectVal(&sc.Header)
		})
		method(PatternType, "split", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			p := asPattern(args[0])
			b := bindArgs("split", args[1:], kwnames, "string", "maxsplit")
			return p.split(ctx, required("split", b[0], "string"), b[1])
		})
		method(PatternType, "sub", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			p := asPattern(args[0])
			b := bindArgs("sub", args[1:], kwnames, "repl", "string", "count")
			res, _ := p.sub(ctx, required("sub", b[0], "repl"), required("sub", b[1], "string"), b[2])
			return res
		})
		method(PatternType, "subn", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			p := asPattern(args[0])
			b := bindArgs("subn", args[1:], kwnames, "repl", "string", "count")
			res, n := p.sub(ctx, required("subn", b[0], "repl"), required("subn", b[1], "string"), b[2])
			return val.NewTupleVal(res, val.Int64Val(n))
		})
		method(PatternType, "__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NewStringVal(patternRepr(ctx, asPattern(args[0])))
		})
		method(PatternType, "__eq__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if args[1].Kind() != val.KindNative || args[1].HeaderPtr().Type != PatternType {
				return val.BoolVal(false)
			}
			a, b := asPattern(args[0]), asPattern(args[1])
			return val.BoolVal(a.IsBytes == b.IsBytes && a.Re.Flags == b.Re.Flags && val.Equals(a.Source, b.Source))
		})
		method(PatternType, "__hash__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			a := asPattern(args[0])
			return val.Int64Val(int64(val.HashValue(a.Source)) ^ int64(a.Re.Flags))
		})
		method(PatternType, "__copy__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return args[0]
		})
		method(PatternType, "__deepcopy__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return args[0]
		})
		prop(PatternType, "pattern", func(self val.Value) val.Value { return asPattern(self).Source })
		prop(PatternType, "flags", func(self val.Value) val.Value { return val.Int64Val(int64(asPattern(self).Re.Flags)) })
		prop(PatternType, "groups", func(self val.Value) val.Value { return val.Int64Val(int64(asPattern(self).Re.NumGroups)) })
		prop(PatternType, "groupindex", func(self val.Value) val.Value {
			p := asPattern(self)
			d := val.NewDictVal(len(p.Re.GroupIndex))
			for gi := 1; gi <= p.Re.NumGroups; gi++ {
				if name := p.Re.GroupNames[gi]; name != "" {
					d.AsDict().Set(val.NewStringVal(name), val.Int64Val(int64(gi)))
				}
			}
			return val.NewMappingProxyVal(d)
		})
	})
}

func reEscape(v val.Value) val.Value {
	const special = "()[]{}?*+-|^$\\.&~# \t\n\r\v\f"
	switch {
	case v.IsAnyString():
		var sb strings.Builder
		for _, r := range v.AsString() {
			if r < 128 && strings.ContainsRune(special, r) {
				sb.WriteByte('\\')
			}
			sb.WriteRune(r)
		}
		return val.NewStringVal(sb.String())
	case v.IsInstance() && val.IsSubclass(v.Type(), val.StrType):
		s := v.String()
		inst := v.AsInstance()
		if len(inst.Slots) > 0 && inst.Slots[0].IsAnyString() {
			s = inst.Slots[0].AsString()
		}
		var sb strings.Builder
		for _, r := range s {
			if r < 128 && strings.ContainsRune(special, r) {
				sb.WriteByte('\\')
			}
			sb.WriteRune(r)
		}
		return val.NewStringVal(sb.String())
	case v.IsBytes() || v.IsByteArray():
		var data []byte
		if v.IsBytes() {
			data = v.AsBytes().Data
		} else {
			data = v.AsByteArray().Data
		}
		out := make([]byte, 0, len(data))
		for _, b := range data {
			if b < 128 && strings.IndexByte(special, b) >= 0 {
				out = append(out, '\\')
			}
			out = append(out, b)
		}
		return val.NewBytesVal(out)
	case v.IsInstance() && val.IsSubclass(v.Type(), val.BytesType):
		var data []byte
		inst := v.AsInstance()
		if len(inst.Slots) > 0 && inst.Slots[0].IsBytes() {
			data = inst.Slots[0].AsBytes().Data
		}
		out := make([]byte, 0, len(data))
		for _, b := range data {
			if b < 128 && strings.IndexByte(special, b) >= 0 {
				out = append(out, '\\')
			}
			out = append(out, b)
		}
		return val.NewBytesVal(out)
	}
	panic("TypeError: expected string or bytes-like object, got '" + typeNameOf(v) + "'")
}

func (e *Engine) createSREModule() val.Value {
	initSRETypes()

	modVal := val.NewModuleVal("_sre", "")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("MAGIC"), val.Int64Val(20230612))
	mod.Dict.Set(val.InternString("CODESIZE"), val.Int64Val(4))
	mod.Dict.Set(val.InternString("MAXREPEAT"), val.Int64Val(sre.MaxRepeat))
	mod.Dict.Set(val.InternString("MAXGROUPS"), val.Int64Val(2147483647))
	mod.Dict.Set(val.InternString("compile"), val.NewBuiltinFuncVal("compile", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		b := bindArgs("compile", args, kwnames, "pattern", "flags")
		return compilePattern(ctx, required("compile", b[0], "pattern"), intArg(ctx, b[1], 0))
	}))
	return modVal
}

func (e *Engine) createReModule() val.Value {
	initSRETypes()

	modVal := val.NewModuleVal("re", "")
	mod := modVal.AsModule()
	set := func(name string, v val.Value) { mod.Dict.Set(val.InternString(name), v) }

	for _, f := range []struct {
		names []string
		v     int64
	}{
		{[]string{"NOFLAG"}, 0},
		{[]string{"A", "ASCII"}, sre.FlagASCII},
		{[]string{"I", "IGNORECASE"}, sre.FlagIgnoreCase},
		{[]string{"L", "LOCALE"}, sre.FlagLocale},
		{[]string{"M", "MULTILINE"}, sre.FlagMultiline},
		{[]string{"S", "DOTALL"}, sre.FlagDotAll},
		{[]string{"U", "UNICODE"}, sre.FlagUnicode},
		{[]string{"X", "VERBOSE"}, sre.FlagVerbose},
		{[]string{"DEBUG"}, sre.FlagDebug},
	} {
		for _, n := range f.names {
			set(n, val.Int64Val(f.v))
		}
	}
	set("Pattern", val.TypeVal(PatternType))
	set("Match", val.TypeVal(MatchType))
	set("PatternError", val.TypeVal(PatternErrorType))
	set("error", val.TypeVal(PatternErrorType))

	pat := func(ctx *val.ExecContext, pattern val.Value, flags val.Value) (val.Value, *pyPattern) {
		pv := compilePattern(ctx, pattern, intArg(ctx, flags, 0))
		if ctx != nil && ctx.HasException() {
			return val.NoneVal(), nil
		}
		return pv, asPattern(pv)
	}
	fn := func(name string, f val.VectorcallFunc) {
		set(name, val.NewBuiltinFuncVal(name, f))
	}
	fn("compile", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		b := bindArgs("compile", args, kwnames, "pattern", "flags")
		pv, _ := pat(ctx, required("compile", b[0], "pattern"), b[1])
		return pv
	})
	for _, spec := range []struct {
		name string
		mode sre.Mode
	}{{"match", sre.ModeMatch}, {"prefixmatch", sre.ModeMatch}, {"fullmatch", sre.ModeFullMatch}, {"search", sre.ModeSearch}} {
		fn(spec.name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			b := bindArgs(spec.name, args, kwnames, "pattern", "string", "flags")
			_, p := pat(ctx, required(spec.name, b[0], "pattern"), b[2])
			return p.doMatch(ctx, spec.mode, required(spec.name, b[1], "string"), val.UnboundVal(), val.UnboundVal())
		})
	}
	fn("findall", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		b := bindArgs("findall", args, kwnames, "pattern", "string", "flags")
		_, p := pat(ctx, required("findall", b[0], "pattern"), b[2])
		return p.findall(ctx, required("findall", b[1], "string"), val.UnboundVal(), val.UnboundVal())
	})
	fn("finditer", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		b := bindArgs("finditer", args, kwnames, "pattern", "string", "flags")
		pv, p := pat(ctx, required("finditer", b[0], "pattern"), b[2])
		sc := p.newScanner(ctx, pv, required("finditer", b[1], "string"), val.UnboundVal(), val.UnboundVal())
		return val.ObjectVal(&sc.Header)
	})
	fn("split", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		b := bindArgs("split", args, kwnames, "pattern", "string", "maxsplit", "flags")
		_, p := pat(ctx, required("split", b[0], "pattern"), b[3])
		return p.split(ctx, required("split", b[1], "string"), b[2])
	})
	fn("sub", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		b := bindArgs("sub", args, kwnames, "pattern", "repl", "string", "count", "flags")
		_, p := pat(ctx, required("sub", b[0], "pattern"), b[4])
		res, _ := p.sub(ctx, required("sub", b[1], "repl"), required("sub", b[2], "string"), b[3])
		return res
	})
	fn("subn", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		b := bindArgs("subn", args, kwnames, "pattern", "repl", "string", "count", "flags")
		_, p := pat(ctx, required("subn", b[0], "pattern"), b[4])
		res, n := p.sub(ctx, required("subn", b[1], "repl"), required("subn", b[2], "string"), b[3])
		return val.NewTupleVal(res, val.Int64Val(n))
	})
	fn("escape", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		b := bindArgs("escape", args, kwnames, "pattern")
		return reEscape(required("escape", b[0], "pattern"))
	})
	fn("purge", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		purgeCache()
		return val.NoneVal()
	})
	return modVal
}
