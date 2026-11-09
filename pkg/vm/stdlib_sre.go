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
	PatternType      *val.PyType
	MatchType        *val.PyType
	ScannerType      *val.PyType
	PatternErrorType *val.PyType
	sreOnce          sync.Once
)

type pyPattern struct {
	Header  val.ObjectHeader
	Source  val.Value
	Re      *sre.Regexp
	IsBytes bool
}

type pyMatch struct {
	Header     val.ObjectHeader
	PatternVal val.Value
	Pat        *pyPattern
	StringVal  val.Value
	In         []rune
	Caps       []int
	LastIndex  int
	Pos        int
	EndPos     int
}

type pyScanner struct {
	Header     val.ObjectHeader
	PatternVal val.Value
	Pat        *pyPattern
	StringVal  val.Value
	In         []rune
	Pos        int
	EndPos     int
	next       int
	mustAdv    bool
	done       bool
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
// parameters are left Unbound.
func bindArgs(fn string, args, kwnames []val.Value, names ...string) []val.Value {
	out := make([]val.Value, len(names))
	for i := range out {
		out[i] = val.UnboundVal()
	}
	npos := len(args) - len(kwnames)
	if npos > len(names) {
		panic("TypeError: " + fn + "() takes at most " + strconv.Itoa(len(names)) + " arguments (" + strconv.Itoa(npos) + " given)")
	}
	copy(out, args[:npos])
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
	panic("TypeError: '" + typeNameOf(v) + "' object cannot be interpreted as an integer")
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

// subject converts a str / bytes-like object to runes, enforcing that its type
// agrees with the pattern.
func (p *pyPattern) subject(v val.Value) []rune {
	switch {
	case v.IsAnyString():
		if p.IsBytes {
			panic("TypeError: cannot use a bytes pattern on a string-like object")
		}
		return []rune(v.AsString())
	case v.IsInstance() && val.IsSubclass(v.Type(), val.StrType):
		if p.IsBytes {
			panic("TypeError: cannot use a bytes pattern on a string-like object")
		}
		inst := v.AsInstance()
		if len(inst.Slots) > 0 && inst.Slots[0].IsAnyString() {
			return []rune(inst.Slots[0].AsString())
		}
		return []rune(v.String())
	case v.IsBytes() || v.IsByteArray() || v.IsMemoryView():
		if !p.IsBytes {
			panic("TypeError: cannot use a string pattern on a bytes-like object")
		}
		var data []byte
		switch {
		case v.IsBytes():
			data = v.AsBytes().Data
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
		var data []byte
		if len(inst.Slots) > 0 && inst.Slots[0].IsBytes() {
			data = inst.Slots[0].AsBytes().Data
		}
		rs := make([]rune, len(data))
		for i, b := range data {
			rs[i] = rune(b)
		}
		return rs
	}
	panic("TypeError: expected string or bytes-like object, got '" + typeNameOf(v) + "'")
}

func (p *pyPattern) slice(in []rune, s, e int) val.Value {
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
	panic(newPatternError(e.Msg, pattern, e.Pos))
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
	inst.SetAttr(val.InternString("args"), val.NewTupleVal(val.NewStringVal(full)))
	inst.SetAttr(val.InternString("msg"), val.NewStringVal(msg))
	inst.SetAttr(val.InternString("pattern"), pattern)
	inst.SetAttr(val.InternString("pos"), posV)
	inst.SetAttr(val.InternString("lineno"), linenoV)
	inst.SetAttr(val.InternString("colno"), colnoV)
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
			panic("ValueError: cannot process flags argument with a compiled pattern")
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
		panic("TypeError: first argument must be string or compiled pattern")
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
		}
		panic("re.error: " + err.Error())
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

func newMatch(patVal val.Value, p *pyPattern, strVal val.Value, in []rune, r *sre.Result, pos, endpos int) val.Value {
	m := &pyMatch{
		Header:     val.ObjectHeader{Kind: val.KindNative, Type: MatchType},
		PatternVal: patVal,
		Pat:        p,
		StringVal:  strVal,
		In:         in,
		Caps:       r.Caps,
		LastIndex:  r.LastIndex,
		Pos:        pos,
		EndPos:     endpos,
	}
	return val.ObjectVal(&m.Header)
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
		panic(val.NewException(val.IndexErrorType, "no such group"))
	}
	return idx
}

func (m *pyMatch) groupVal(idx int, def val.Value) val.Value {
	s, e := m.Caps[2*idx], m.Caps[2*idx+1]
	if s < 0 || e < 0 {
		return def
	}
	return m.Pat.slice(m.In, s, e)
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

// execFrom runs a search/match for the Pattern methods.
func (p *pyPattern) exec(in []rune, pos, endpos int, mode sre.Mode, mustAdv bool) *sre.Result {
	return p.Re.ExecFrom(in, pos, endpos, mode, mustAdv)
}

func (p *pyPattern) posArgs(ctx *val.ExecContext, in []rune, posV, endV val.Value) (int, int) {
	pos := clampInt(intArg(ctx, posV, 0), 0, len(in))
	end := clampInt(intArg(ctx, endV, int64(len(in))), 0, len(in))
	return pos, end
}

func (p *pyPattern) doMatch(ctx *val.ExecContext, patVal val.Value, mode sre.Mode, strVal, posV, endV val.Value) val.Value {
	in := p.subject(strVal)
	pos, end := p.posArgs(ctx, in, posV, endV)
	r := p.exec(in, pos, end, mode, false)
	if r == nil {
		return val.NoneVal()
	}
	return newMatch(patVal, p, strVal, in, r, pos, end)
}

// iterate drives the CPython 3.7+ iteration protocol used by findall,
// finditer, sub and split: after an empty match the next match must advance.
func (p *pyPattern) iterate(in []rune, pos, end int, fn func(r *sre.Result) bool) {
	mustAdv := false
	for {
		r := p.exec(in, pos, end, sre.ModeSearch, mustAdv)
		if r == nil {
			return
		}
		if !fn(r) {
			return
		}
		pos = r.Caps[1]
		mustAdv = r.Caps[1] == r.Caps[0]
	}
}

func (p *pyPattern) findall(ctx *val.ExecContext, strVal, posV, endV val.Value) val.Value {
	in := p.subject(strVal)
	pos, end := p.posArgs(ctx, in, posV, endV)
	var list []val.Value
	ng := p.Re.NumGroups
	grp := func(r *sre.Result, i int) val.Value {
		s, e := r.Caps[2*i], r.Caps[2*i+1]
		if s < 0 || e < 0 {
			return p.fromRunes(nil)
		}
		return p.slice(in, s, e)
	}
	p.iterate(in, pos, end, func(r *sre.Result) bool {
		switch ng {
		case 0:
			list = append(list, p.slice(in, r.Caps[0], r.Caps[1]))
		case 1:
			list = append(list, grp(r, 1))
		default:
			items := make([]val.Value, ng)
			for i := 1; i <= ng; i++ {
				items[i-1] = grp(r, i)
			}
			list = append(list, val.NewTupleVal(items...))
		}
		return true
	})
	return val.NewListVal(list...)
}

func (p *pyPattern) split(ctx *val.ExecContext, strVal, maxV val.Value) val.Value {
	in := p.subject(strVal)
	maxsplit := intArg(ctx, maxV, 0)
	if maxsplit < 0 {
		panic("ValueError: maxsplit must be non-negative")
	}
	var out []val.Value
	last := 0
	n := int64(0)
	p.iterate(in, 0, len(in), func(r *sre.Result) bool {
		if maxsplit > 0 && n >= maxsplit {
			return false
		}
		out = append(out, p.slice(in, last, r.Caps[0]))
		for i := 1; i <= p.Re.NumGroups; i++ {
			s, e := r.Caps[2*i], r.Caps[2*i+1]
			if s < 0 || e < 0 {
				out = append(out, val.NoneVal())
			} else {
				out = append(out, p.slice(in, s, e))
			}
		}
		n++
		last = r.Caps[1]
		return true
	})
	out = append(out, p.slice(in, last, len(in)))
	return val.NewListVal(out...)
}

func (p *pyPattern) sub(ctx *val.ExecContext, patVal, repl, strVal, countV val.Value) (val.Value, int64) {
	in := p.subject(strVal)
	count := intArg(ctx, countV, 0)
	count = max(count, 0)
	var tmpl *sre.Template
	var literal []rune
	isLiteral := false
	isCallable := false
	switch {
	case repl.IsAnyString():
		if p.IsBytes {
			panic("TypeError: expected a bytes-like object, str found")
		}
		rs := []rune(repl.AsString())
		if !containsRune(rs, '\\') {
			literal, isLiteral = rs, true
		} else {
			t, err := p.Re.ParseTemplate(rs)
			if err != nil {
				raiseReError(ctx, err.(*sre.Error), repl)
			}
			tmpl = t
		}
	case repl.IsBytes() || repl.IsByteArray():
		if !p.IsBytes {
			panic("TypeError: expected str instance, bytes found")
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
			}
			tmpl = t
		}
	default:
		isCallable = true
	}
	var out []rune
	last := 0
	n := int64(0)
	p.iterate(in, 0, len(in), func(r *sre.Result) bool {
		if count > 0 && n >= count {
			return false
		}
		out = append(out, in[last:r.Caps[0]]...)
		switch {
		case isCallable:
			mv := newMatch(patVal, p, strVal, in, r, 0, len(in))
			res := ctx.Call(repl, []val.Value{mv}, nil)
			out = append(out, p.valueRunes(res)...)
		case isLiteral:
			out = append(out, literal...)
		default:
			out = tmpl.Expand(out, in, r.Caps)
		}
		last = r.Caps[1]
		n++
		return true
	})
	out = append(out, in[last:]...)
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
	r := sc.Pat.exec(sc.In, sc.next, sc.EndPos, mode, sc.mustAdv)
	if r == nil {
		sc.done = true
		return val.NoneVal()
	}
	sc.mustAdv = r.Caps[1] == r.Caps[0]
	sc.next = r.Caps[1]
	return newMatch(sc.PatternVal, sc.Pat, sc.StringVal, sc.In, r, sc.Pos, sc.EndPos)
}

func (p *pyPattern) newScanner(ctx *val.ExecContext, patVal, strVal, posV, endV val.Value) *pyScanner {
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
			for _, k := range []string{"args", "msg", "pattern", "pos", "lineno", "colno"} {
				if v, ok := ne.AsInstance().GetAttr(val.InternString(k)); ok {
					self.SetAttr(val.InternString(k), v)
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
				b := bindArgs(name, args[1:], kwnames, "group")
				g := 0
				if !b[0].IsUnbound() {
					g = m.group(ctx, b[0])
				}
				return f(m.Caps[2*g], m.Caps[2*g+1])
			})
		}
		offsetFn("start", func(s, e int) val.Value { return val.Int64Val(int64(s)) })
		offsetFn("end", func(s, e int) val.Value { return val.Int64Val(int64(e)) })
		offsetFn("span", func(s, e int) val.Value { return val.NewTupleVal(val.Int64Val(int64(s)), val.Int64Val(int64(e))) })
		method(MatchType, "expand", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			m := asMatch(args[0])
			b := bindArgs("expand", args[1:], kwnames, "template")
			tv := required("expand", b[0], "template")
			rs := m.Pat.valueRunes(tv)
			t, err := m.Pat.Re.ParseTemplate(rs)
			if err != nil {
				raiseReError(ctx, err.(*sre.Error), tv)
			}
			return m.Pat.fromRunes(t.Expand(nil, m.In, m.Caps))
		})
		method(MatchType, "__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			m := asMatch(args[0])
			matched := val.PyRepr(ctx, m.groupVal(0, val.NoneVal()))
			return val.NewStringVal("<re.Match object; span=(" + strconv.Itoa(m.Caps[0]) + ", " + strconv.Itoa(m.Caps[1]) + "), match=" + matched + ">")
		})
		method(MatchType, "__copy__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return args[0]
		})
		method(MatchType, "__deepcopy__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return args[0]
		})
		prop(MatchType, "re", func(self val.Value) val.Value { return asMatch(self).PatternVal })
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
			items := make([]val.Value, 0, len(m.Caps)/2)
			for i := 0; i < len(m.Caps); i += 2 {
				items = append(items, val.NewTupleVal(val.Int64Val(int64(m.Caps[i])), val.Int64Val(int64(m.Caps[i+1]))))
			}
			return val.NewTupleVal(items...)
		})

		// ---- Scanner ----
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
			method(PatternType, spec.name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				p := asPattern(args[0])
				b := bindArgs(spec.name, args[1:], kwnames, "string", "pos", "endpos")
				return p.doMatch(ctx, args[0], spec.mode, required(spec.name, b[0], "string"), b[1], b[2])
			})
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
			return val.NewIterValWithNext(func() (val.Value, bool) {
				m := sc.step(ctx, sre.ModeSearch)
				if m.IsNone() {
					return val.NoneVal(), false
				}
				return m, true
			})
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
			res, _ := p.sub(ctx, args[0], required("sub", b[0], "repl"), required("sub", b[1], "string"), b[2])
			return res
		})
		method(PatternType, "subn", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			p := asPattern(args[0])
			b := bindArgs("subn", args[1:], kwnames, "repl", "string", "count")
			res, n := p.sub(ctx, args[0], required("subn", b[0], "repl"), required("subn", b[1], "string"), b[2])
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
			pv, p := pat(ctx, required(spec.name, b[0], "pattern"), b[2])
			return p.doMatch(ctx, pv, spec.mode, required(spec.name, b[1], "string"), val.UnboundVal(), val.UnboundVal())
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
		return val.NewIterValWithNext(func() (val.Value, bool) {
			m := sc.step(ctx, sre.ModeSearch)
			if m.IsNone() {
				return val.NoneVal(), false
			}
			return m, true
		})
	})
	fn("split", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		b := bindArgs("split", args, kwnames, "pattern", "string", "maxsplit", "flags")
		_, p := pat(ctx, required("split", b[0], "pattern"), b[3])
		return p.split(ctx, required("split", b[1], "string"), b[2])
	})
	fn("sub", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		b := bindArgs("sub", args, kwnames, "pattern", "repl", "string", "count", "flags")
		pv, p := pat(ctx, required("sub", b[0], "pattern"), b[4])
		res, _ := p.sub(ctx, pv, required("sub", b[1], "repl"), required("sub", b[2], "string"), b[3])
		return res
	})
	fn("subn", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		b := bindArgs("subn", args, kwnames, "pattern", "repl", "string", "count", "flags")
		pv, p := pat(ctx, required("subn", b[0], "pattern"), b[4])
		res, n := p.sub(ctx, pv, required("subn", b[1], "repl"), required("subn", b[2], "string"), b[3])
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
