package vm

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// jsonDecodeError describes a malformed JSON document: the CPython-style
// message and the character offset the failure was detected at.
type jsonDecodeError struct {
	msg string
	pos int
}

func (e *jsonDecodeError) Error() string { return e.msg }

var (
	jsonDecodeErrorOnce sync.Once
	jsonDecodeErrorType *val.PyType
)

// initJSONDecodeErrorType creates the json.JSONDecodeError exception type once
// per process so that identity comparisons keep working across engines.
func initJSONDecodeErrorType() *val.PyType {
	jsonDecodeErrorOnce.Do(func() {
		jsonDecodeErrorType = val.MustNewType("JSONDecodeError", []*val.PyType{val.ValueErrorType}, nil)
		jsonDecodeErrorType.Dict.Set(val.InternString("msg"), val.NoneVal())
		jsonDecodeErrorType.Dict.Set(val.InternString("doc"), val.NoneVal())
		jsonDecodeErrorType.Dict.Set(val.InternString("pos"), val.NoneVal())
		jsonDecodeErrorType.Dict.Set(val.InternString("lineno"), val.NoneVal())
		jsonDecodeErrorType.Dict.Set(val.InternString("colno"), val.NoneVal())
		jsonDecodeErrorType.Dict.Set(val.InternString("__init__"), val.NewBuiltinFuncVal("__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 4 {
				ctx.Raise(val.NewException(val.TypeErrorType, "JSONDecodeError() takes exactly 3 arguments"))
				return val.NoneVal()
			}
			self := args[0]
			msg := args[1].AsString()
			doc := args[2].AsString()
			pos := int(args[3].AsInt64())
			pos = min(max(pos, 0), len(doc))
			lineno := strings.Count(doc[:pos], "\n") + 1
			colno := pos - strings.LastIndex(doc[:pos], "\n")
			errmsg := fmt.Sprintf("%s: line %d column %d (char %d)", msg, lineno, colno, pos)
			if self.IsInstance() {
				inst := self.AsInstance()
				inst.SetAttr(nameArgs, val.NewTupleVal(val.NewStringVal(errmsg)))
				inst.SetAttr(nameMsg, val.NewStringVal(msg))
				inst.SetAttr(nameDoc, val.NewStringVal(doc))
				inst.SetAttr(namePos, val.Int64Val(int64(pos)))
				inst.SetAttr(nameLineno, val.Int64Val(int64(lineno)))
				inst.SetAttr(nameColno, val.Int64Val(int64(colno)))
			}
			return val.NoneVal()
		}))
		jsonDecodeErrorType.InvalidateVersion()
	})
	return jsonDecodeErrorType
}

// newJSONDecodeError builds a json.JSONDecodeError instance for a malformed
// document, mirroring CPython's message layout.
func newJSONDecodeError(err error, doc []byte) val.Value {
	msg := "Expecting value"
	pos := 0
	if decErr, ok := errors.AsType[*jsonDecodeError](err); ok {
		msg = decErr.msg
		pos = decErr.pos
	} else if err != nil {
		msg = err.Error()
	}
	pos = min(max(pos, 0), len(doc))
	docStr := string(doc)
	lineno := strings.Count(docStr[:pos], "\n") + 1
	colno := pos - strings.LastIndex(docStr[:pos], "\n")
	errmsg := fmt.Sprintf("%s: line %d column %d (char %d)", msg, lineno, colno, pos)
	inst := val.NewInstanceOfType(initJSONDecodeErrorType())
	inst.AsInstance().SetAttr(nameArgs, val.NewTupleVal(val.NewStringVal(errmsg)))
	inst.AsInstance().SetAttr(nameMsg, val.NewStringVal(msg))
	inst.AsInstance().SetAttr(nameDoc, val.NewStringVal(docStr))
	inst.AsInstance().SetAttr(namePos, val.Int64Val(int64(pos)))
	inst.AsInstance().SetAttr(nameLineno, val.Int64Val(int64(lineno)))
	inst.AsInstance().SetAttr(nameColno, val.Int64Val(int64(colno)))
	inst.AsInstance().SetAttr(nameTraceback, val.NoneVal())
	inst.AsInstance().SetAttr(nameCause, val.NoneVal())
	inst.AsInstance().SetAttr(nameContext, val.NoneVal())
	inst.AsInstance().SetAttr(nameSuppressContext, val.BoolVal(false))
	return val.ObjectVal(&inst.AsInstance().Header)
}

// jsonParser provides direct, allocation-optimized JSON parsing into fython Values.
type jsonParser struct {
	src         []byte
	pos         int
	pool        *val.InternPool
	scratch     []byte
	dictCapHint int
	entrySlab   []val.DictEntry
	dictSlab    []val.PyDict
	listSlab    []val.PyList
	valSlab     []val.Value
	strSlab     []val.PyString
	byteSlab    []byte
	vm          *VM
}

func acquireJSONParser(ctx *val.ExecContext, src []byte, pool *val.InternPool) *jsonParser {
	if ctx != nil && ctx.VM != nil && !val.MultiThreadedFlag.Load() {
		if vm, ok := ctx.VM.(*VM); ok && vm.jsonParser != nil {
			p := vm.jsonParser
			vm.jsonParser = nil
			p.src = src
			p.pos = 0
			p.pool = pool
			p.scratch = p.scratch[:0]
			p.dictCapHint = 5
			p.vm = vm
			return p
		}
		if vm, ok := ctx.VM.(*VM); ok {
			return &jsonParser{
				src:         src,
				pos:         0,
				pool:        pool,
				scratch:     make([]byte, 0, 64),
				dictCapHint: 5,
				vm:          vm,
			}
		}
	}
	return &jsonParser{
		src:         src,
		pos:         0,
		pool:        pool,
		scratch:     make([]byte, 0, 64),
		dictCapHint: 5,
	}
}

func releaseJSONParser(ctx *val.ExecContext, p *jsonParser) {
	if p == nil {
		return
	}
	p.src = nil
	p.pool = nil
	if ctx != nil && ctx.VM != nil && !val.MultiThreadedFlag.Load() {
		if vm, ok := ctx.VM.(*VM); ok && vm.jsonParser == nil {
			vm.jsonParser = p
		}
	}
}

func (p *jsonParser) allocString(raw []byte) val.Value {
	if len(raw) > 1024 {
		return val.NewStringVal(string(raw))
	}
	if len(p.strSlab) >= cap(p.strSlab) {
		p.strSlab = make([]val.PyString, 0, 256)
	}
	idx := len(p.strSlab)
	p.strSlab = p.strSlab[:idx+1]
	ps := &p.strSlab[idx]
	if len(p.byteSlab)+len(raw) > cap(p.byteSlab) {
		p.byteSlab = make([]byte, 0, 4096)
	}
	bStart := len(p.byteSlab)
	p.byteSlab = append(p.byteSlab, raw...)
	s := unsafe.String(&p.byteSlab[bStart], len(raw))
	ps.Header = val.ObjectHeader{
		Kind: val.KindString,
		Hash: val.HashString(s),
		Type: val.StrType,
	}
	ps.Data = s
	return val.ObjectVal(&ps.Header)
}

func (p *jsonParser) allocDict(capHint int) *val.PyDict {
	if len(p.dictSlab) >= cap(p.dictSlab) {
		p.dictSlab = make([]val.PyDict, 0, 256)
	}
	idx := len(p.dictSlab)
	p.dictSlab = p.dictSlab[:idx+1]
	d := &p.dictSlab[idx]
	tableCap := 8
	for tableCap*2 < capHint*3 {
		tableCap <<= 1
	}
	maxDense := tableCap * 2 / 3
	val.InitArenaDict(d, capHint, p.allocEntries(maxDense))
	return d
}

func (p *jsonParser) allocDictVal(capHint int) (*val.PyDict, val.Value) {
	if p.vm != nil && !val.MultiThreadedFlag.Load() && capHint > 0 && capHint <= 10 {
		hint := max(capHint, 1)
		dv := p.vm.acquireSmallDict(hint, false)
		if d := dv.AsDict(); d != nil {
			return d, dv
		}
	}
	d := p.allocDict(capHint)
	return d, val.DictVal(d)
}

func (p *jsonParser) allocList(items []val.Value) val.Value {
	if len(p.listSlab) >= cap(p.listSlab) {
		p.listSlab = make([]val.PyList, 0, 256)
	}
	idx := len(p.listSlab)
	p.listSlab = p.listSlab[:idx+1]
	l := &p.listSlab[idx]
	l.Header = val.ObjectHeader{Kind: val.KindList, Type: val.ListType}
	l.Items = items
	return val.ObjectVal(&l.Header)
}

func (p *jsonParser) allocValSlice(capNeeded int) []val.Value {
	if capNeeded > 256 {
		return make([]val.Value, 0, capNeeded)
	}
	if len(p.valSlab)+capNeeded > cap(p.valSlab) {
		p.valSlab = make([]val.Value, 0, 1024)
	}
	start := len(p.valSlab)
	end := start + capNeeded
	p.valSlab = p.valSlab[:end]
	return p.valSlab[start:start:end]
}

func (p *jsonParser) allocEntries(capNeeded int) []val.DictEntry {
	if capNeeded > 256 {
		return make([]val.DictEntry, 0, capNeeded)
	}
	if len(p.entrySlab)+capNeeded > cap(p.entrySlab) {
		p.entrySlab = make([]val.DictEntry, 0, 1024)
	}
	start := len(p.entrySlab)
	end := start + capNeeded
	p.entrySlab = p.entrySlab[:end]
	return p.entrySlab[start:start:end]
}

func (p *jsonParser) skipWhitespace() {
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case ' ', '\t', '\r', '\n':
			p.pos++
		default:
			return
		}
	}
}

// errAt reports a decoding failure at a known character offset.
func (p *jsonParser) errAt(pos int, msg string) error {
	if pos < 0 {
		pos = 0
	}
	if pos > len(p.src) {
		pos = len(p.src)
	}
	return &jsonDecodeError{msg: msg, pos: pos}
}

// errNow reports a decoding failure at the current character offset.
func (p *jsonParser) errNow(msg string) error {
	return p.errAt(p.pos, msg)
}

func (p *jsonParser) parseValue() (val.Value, error) {
	p.skipWhitespace()
	if p.pos >= len(p.src) {
		return val.NoneVal(), p.errNow("Expecting value")
	}

	switch p.src[p.pos] {
	case '{':
		return p.parseObject()
	case '[':
		return p.parseArray()
	case '"':
		return p.parseString(false)
	case 't':
		if p.pos+4 <= len(p.src) && p.src[p.pos+1] == 'r' && p.src[p.pos+2] == 'u' && p.src[p.pos+3] == 'e' {
			p.pos += 4
			return val.BoolVal(true), nil
		}
		return p.parseLiteral("true", val.BoolVal(true))
	case 'f':
		if p.pos+5 <= len(p.src) && p.src[p.pos+1] == 'a' && p.src[p.pos+2] == 'l' && p.src[p.pos+3] == 's' && p.src[p.pos+4] == 'e' {
			p.pos += 5
			return val.BoolVal(false), nil
		}
		return p.parseLiteral("false", val.BoolVal(false))
	case 'n':
		if p.pos+4 <= len(p.src) && p.src[p.pos+1] == 'u' && p.src[p.pos+2] == 'l' && p.src[p.pos+3] == 'l' {
			p.pos += 4
			return val.NoneVal(), nil
		}
		return p.parseLiteral("null", val.NoneVal())
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return p.parseNumber()
	default:
		return val.NoneVal(), p.errNow("Expecting value")
	}
}

func (p *jsonParser) parseObject() (val.Value, error) {
	p.pos++ // consume '{'
	dict, dictVal := p.allocDictVal(p.dictCapHint)

	p.skipWhitespace()
	if p.pos < len(p.src) && p.src[p.pos] == '}' {
		p.pos++
		return dictVal, nil
	}

	for {
		p.skipWhitespace()
		if p.pos >= len(p.src) || p.src[p.pos] != '"' {
			return val.NoneVal(), p.errNow("Expecting property name enclosed in double quotes")
		}

		keyVal, err := p.parseString(true)
		if err != nil {
			return val.NoneVal(), err
		}

		p.skipWhitespace()
		if p.pos >= len(p.src) || p.src[p.pos] != ':' {
			return val.NoneVal(), p.errNow("Expecting ':' delimiter")
		}
		p.pos++ // consume ':'

		valElem, err := p.parseValue()
		if err != nil {
			return val.NoneVal(), err
		}

		dict.PutNew(keyVal, valElem)

		p.skipWhitespace()
		if p.pos >= len(p.src) {
			return val.NoneVal(), p.errNow("Expecting ',' delimiter")
		}

		if p.src[p.pos] == '}' {
			p.pos++
			if n := len(dict.Entries); n > p.dictCapHint && n <= 10 {
				p.dictCapHint = n + 1
			}
			return dictVal, nil
		}
		if p.src[p.pos] == ',' {
			commaPos := p.pos
			p.pos++
			p.skipWhitespace()
			if p.pos < len(p.src) && p.src[p.pos] == '}' {
				return val.NoneVal(), p.errAt(commaPos, "Illegal trailing comma before end of object")
			}
			continue
		}
		return val.NoneVal(), p.errNow("Expecting ',' delimiter")
	}
}

func (p *jsonParser) parseArray() (val.Value, error) {
	p.pos++ // consume '['
	items := p.allocValSlice(4)

	p.skipWhitespace()
	if p.pos < len(p.src) && p.src[p.pos] == ']' {
		p.pos++
		return p.allocList(items), nil
	}

	for {
		elem, err := p.parseValue()
		if err != nil {
			return val.NoneVal(), err
		}
		items = append(items, elem)

		p.skipWhitespace()
		if p.pos >= len(p.src) {
			return val.NoneVal(), p.errNow("Expecting ',' delimiter")
		}

		if p.src[p.pos] == ']' {
			p.pos++
			return p.allocList(items), nil
		}
		if p.src[p.pos] == ',' {
			commaPos := p.pos
			p.pos++
			p.skipWhitespace()
			if p.pos < len(p.src) && p.src[p.pos] == ']' {
				return val.NoneVal(), p.errAt(commaPos, "Illegal trailing comma before end of array")
			}
			continue
		}
		return val.NoneVal(), p.errNow("Expecting ',' delimiter")
	}
}

func (p *jsonParser) parseString(isKey bool) (val.Value, error) {
	p.pos++ // consume opening '"'
	start := p.pos
	hasEscapes := false

	for p.pos < len(p.src) {
		b := p.src[p.pos]
		if b == '"' {
			raw := p.src[start:p.pos]
			p.pos++ // consume closing '"'

			if !hasEscapes {
				if len(raw) <= 7 {
					// Small String Optimization: 0 heap allocations, 0 string allocations!
					return val.SmallStringFromBytes(raw), nil
				}
				if isKey {
					return val.InternBytes(raw), nil
				}
				return p.allocString(raw), nil
			}

			// Has escapes: decode via scratch buffer
			decoded, err := p.decodeEscapes(raw, start)
			if err != nil {
				return val.NoneVal(), err
			}
			if isKey || len(decoded) <= 7 {
				if len(decoded) <= 7 {
					return val.SmallStringFromBytes(decoded), nil
				}
				if p.pool != nil {
					return p.pool.InternBytes(decoded), nil
				}
				return val.InternString(string(decoded)), nil
			}
			return val.NewStringVal(string(decoded)), nil
		}
		if b == '\\' {
			hasEscapes = true
			p.pos += 2
			continue
		}
		if b < 0x20 {
			return val.NoneVal(), p.errNow("Invalid control character at")
		}
		p.pos++
	}

	return val.NoneVal(), p.errAt(start-1, "Unterminated string starting at")
}

// decodeEscapes decodes the escape sequences of a JSON string literal. base
// is the offset of raw within the source document, so that failures can be
// reported at the right character position.
func (p *jsonParser) decodeEscapes(raw []byte, base int) ([]byte, error) {
	p.scratch = p.scratch[:0]
	i := 0
	for i < len(raw) {
		if raw[i] == '\\' && i+1 < len(raw) {
			i++
			switch raw[i] {
			case '"':
				p.scratch = append(p.scratch, '"')
			case '\\':
				p.scratch = append(p.scratch, '\\')
			case '/':
				p.scratch = append(p.scratch, '/')
			case 'b':
				p.scratch = append(p.scratch, '\b')
			case 'f':
				p.scratch = append(p.scratch, '\f')
			case 'n':
				p.scratch = append(p.scratch, '\n')
			case 'r':
				p.scratch = append(p.scratch, '\r')
			case 't':
				p.scratch = append(p.scratch, '\t')
			case 'u':
				if i+4 >= len(raw) {
					return nil, p.errAt(base+i, "Invalid \\uXXXX escape")
				}
				r, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
				if err != nil {
					return nil, p.errAt(base+i, "Invalid \\uXXXX escape")
				}
				i += 4
				// Handle surrogate pairs
				if utf16.IsSurrogate(rune(r)) {
					if i+6 < len(raw) && raw[i+1] == '\\' && raw[i+2] == 'u' {
						r2, err2 := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
						if err2 == nil {
							combined := utf16.DecodeRune(rune(r), rune(r2))
							var utf8Buf [4]byte
							n := encodeRune(combined, utf8Buf[:])
							p.scratch = append(p.scratch, utf8Buf[:n]...)
							i += 6
							i++
							continue
						}
					}
				}
				var utf8Buf [4]byte
				n := encodeRune(rune(r), utf8Buf[:])
				p.scratch = append(p.scratch, utf8Buf[:n]...)
			default:
				p.scratch = append(p.scratch, '\\', raw[i])
			}
			i++
		} else {
			p.scratch = append(p.scratch, raw[i])
			i++
		}
	}
	return p.scratch, nil
}

func encodeRune(r rune, p []byte) int {
	if uint32(r) <= 0x7F {
		p[0] = byte(r)
		return 1
	}
	if uint32(r) <= 0x7FF {
		p[0] = 0xC0 | byte(r>>6)
		p[1] = 0x80 | (byte(r) & 0x3F)
		return 2
	}
	if uint32(r) <= 0xFFFF {
		p[0] = 0xE0 | byte(r>>12)
		p[1] = 0x80 | (byte(r>>6) & 0x3F)
		p[2] = 0x80 | (byte(r) & 0x3F)
		return 3
	}
	p[0] = 0xF0 | byte(r>>18)
	p[1] = 0x80 | (byte(r>>12) & 0x3F)
	p[2] = 0x80 | (byte(r>>6) & 0x3F)
	p[3] = 0x80 | (byte(r) & 0x3F)
	return 4
}

func (p *jsonParser) parseNumber() (val.Value, error) {
	start := p.pos
	isFloat := false
	neg := false

	if p.src[p.pos] == '-' {
		neg = true
		p.pos++
	}

	var uval uint64
	digitStart := p.pos
	for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
		uval = uval*10 + uint64(p.src[p.pos]-'0')
		p.pos++
	}
	digits := p.pos - digitStart

	if p.pos < len(p.src) && p.src[p.pos] == '.' {
		isFloat = true
		p.pos++
		fracStart := p.pos
		var fracVal uint64
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			fracVal = fracVal*10 + uint64(p.src[p.pos]-'0')
			p.pos++
		}
		fracDigits := p.pos - fracStart
		if (p.pos >= len(p.src) || (p.src[p.pos] != 'e' && p.src[p.pos] != 'E')) &&
			digits > 0 && fracDigits > 0 && digits+fracDigits <= 15 && fracDigits <= 6 {
			pow10 := [7]float64{1, 10, 100, 1000, 10000, 100000, 1000000}[fracDigits]
			f := float64(uval) + float64(fracVal)/pow10
			if neg {
				f = -f
			}
			return val.Float64Val(f), nil
		}
	}

	if p.pos < len(p.src) && (p.src[p.pos] == 'e' || p.src[p.pos] == 'E') {
		isFloat = true
		p.pos++
		if p.pos < len(p.src) && (p.src[p.pos] == '+' || p.src[p.pos] == '-') {
			p.pos++
		}
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			p.pos++
		}
	}

	if !isFloat && digits > 0 && digits <= 18 {
		if neg {
			return val.Int64Val(-int64(uval)), nil
		}
		return val.Int64Val(int64(uval)), nil
	}

	raw := p.src[start:p.pos]
	numStr := unsafe.String(unsafe.SliceData(raw), len(raw))
	if isFloat {
		f, err := strconv.ParseFloat(numStr, 64)
		if err != nil {
			return val.NoneVal(), err
		}
		return val.Float64Val(f), nil
	}

	i, err := strconv.ParseInt(numStr, 10, 64)
	if err == nil {
		return val.Int64Val(i), nil
	}
	// Big int promotion on overflow
	f, ferr := strconv.ParseFloat(numStr, 64)
	if ferr != nil {
		return val.NoneVal(), ferr
	}
	return val.Float64Val(f), nil
}

func (p *jsonParser) parseLiteral(literal string, result val.Value) (val.Value, error) {
	if p.pos+len(literal) <= len(p.src) && string(p.src[p.pos:p.pos+len(literal)]) == literal {
		p.pos += len(literal)
		return result, nil
	}
	return val.NoneVal(), p.errNow("Expecting value")
}

const jsonHexDigits = "0123456789abcdef"

type jsonDumpOpts struct {
	indentStr   string
	hasIndent   bool
	sortKeys    bool
	ensureASCII bool
	allowNaN    bool
	itemSep     string
	keySep      string
	defaultFn   val.Value
}

func defaultJSONDumpOpts() jsonDumpOpts {
	return jsonDumpOpts{
		ensureASCII: true,
		allowNaN:    true,
		itemSep:     ", ",
		keySep:      ": ",
	}
}

func extractJSONDumpOpts(posArgs []val.Value, allArgs []val.Value, kwnames []val.Value, indentPosIdx int) jsonDumpOpts {
	opts := defaultJSONDumpOpts()
	indentVal := val.NoneVal()
	if len(posArgs) > indentPosIdx {
		indentVal = posArgs[indentPosIdx]
	}
	if v, ok := kwArg(allArgs, kwnames, "indent"); ok {
		indentVal = v
	}
	hasSepArg := false
	if !indentVal.IsNone() {
		if indentVal.IsInt64() {
			n := int(indentVal.AsInt64())
			if n >= 0 {
				opts.hasIndent = true
				opts.indentStr = strings.Repeat(" ", n)
				opts.itemSep = ","
			} else {
				opts.hasIndent = true
				opts.indentStr = ""
				opts.itemSep = ","
			}
		} else if indentVal.IsAnyString() {
			opts.hasIndent = true
			opts.indentStr = indentVal.AsString()
			opts.itemSep = ","
		}
	}
	if v, ok := kwArg(allArgs, kwnames, "sort_keys"); ok && v.IsTruthy() {
		opts.sortKeys = true
	}
	if v, ok := kwArg(allArgs, kwnames, "ensure_ascii"); ok {
		opts.ensureASCII = v.IsTruthy()
	}
	if v, ok := kwArg(allArgs, kwnames, "allow_nan"); ok {
		opts.allowNaN = v.IsTruthy()
	}
	if v, ok := kwArg(allArgs, kwnames, "default"); ok && !v.IsNone() {
		opts.defaultFn = v
	}
	if v, ok := kwArg(allArgs, kwnames, "separators"); ok && !v.IsNone() {
		hasSepArg = true
		if v.IsTuple() && len(v.AsTuple().Items) == 2 {
			opts.itemSep = v.AsTuple().Items[0].AsString()
			opts.keySep = v.AsTuple().Items[1].AsString()
		} else if v.IsList() && len(v.AsList().Items) == 2 {
			opts.itemSep = v.AsList().Items[0].AsString()
			opts.keySep = v.AsList().Items[1].AsString()
		}
	}
	_ = hasSepArg
	return opts
}

func raiseJSONError(ctx *val.ExecContext, err error) {
	if ctx == nil || ctx.HasException() || err == nil {
		return
	}
	msg := err.Error()
	if rest, ok := strings.CutPrefix(msg, "ValueError: "); ok {
		ctx.Raise(val.NewException(val.ValueErrorType, rest))
		return
	}
	if rest, ok := strings.CutPrefix(msg, "TypeError: "); ok {
		ctx.Raise(val.NewException(val.TypeErrorType, rest))
		return
	}
	ctx.Raise(val.NewException(val.ValueErrorType, msg))
}

func jsonDumpsValue(ctx *val.ExecContext, v val.Value, opts jsonDumpOpts) (val.Value, error) {
	var vmPtr *VM
	var buf []byte
	if ctx != nil && ctx.VM != nil && !val.MultiThreadedFlag.Load() {
		if vm, ok := ctx.VM.(*VM); ok && vm != nil {
			vmPtr = vm
			if vm.jsonDumpBuf != nil {
				buf = vm.jsonDumpBuf[:0]
				vm.jsonDumpBuf = nil
			}
		}
	}
	if buf == nil {
		var stackBuf [256]byte
		buf = stackBuf[:0]
	}
	var out []byte
	if vmPtr != nil {
		defer func() {
			if vmPtr.jsonDumpBuf == nil {
				if out != nil && cap(out) <= 65536 {
					vmPtr.jsonDumpBuf = out[:0]
				} else if cap(buf) <= 65536 && cap(buf) > 0 {
					vmPtr.jsonDumpBuf = buf[:0]
				}
			}
		}()
	}
	var err error
	out, err = appendJSONValue(ctx, buf, v, opts, 0)
	if err != nil {
		return val.NoneVal(), err
	}
	if len(out) <= 7 {
		return val.SmallStringFromBytes(out), nil
	}
	return val.NewStringVal(string(out)), nil
}

func appendJSONValue(ctx *val.ExecContext, dst []byte, v val.Value, opts jsonDumpOpts, level int) ([]byte, error) {
	if v.IsNone() {
		return append(dst, "null"...), nil
	}
	if v.IsBool() {
		if v.AsBool() {
			return append(dst, "true"...), nil
		}
		return append(dst, "false"...), nil
	}
	if v.IsInt64() {
		return strconv.AppendInt(dst, v.AsInt64(), 10), nil
	}
	if v.IsFloat64() {
		f := v.AsFloat64()
		if math.IsNaN(f) {
			if !opts.allowNaN {
				return dst, errors.New("ValueError: Out of range float values are not JSON compliant")
			}
			return append(dst, "NaN"...), nil
		}
		if math.IsInf(f, 0) {
			if !opts.allowNaN {
				return dst, errors.New("ValueError: Out of range float values are not JSON compliant")
			}
			if f < 0 {
				return append(dst, "-Infinity"...), nil
			}
			return append(dst, "Infinity"...), nil
		}
		return val.AppendFloatRepr(dst, f), nil
	}
	if v.IsAnyString() {
		return appendJSONStringVal(dst, v, opts.ensureASCII), nil
	}
	if v.IsList() || v.IsTuple() {
		var items []val.Value
		if v.IsList() {
			items = v.AsList().Items
		} else {
			items = v.AsTuple().Items
		}
		if len(items) == 0 {
			return append(dst, "[]"...), nil
		}
		dst = append(dst, '[')
		if opts.hasIndent {
			dst = append(dst, '\n')
		}
		for i, item := range items {
			if opts.hasIndent {
				for range level + 1 {
					dst = append(dst, opts.indentStr...)
				}
			}
			var err error
			dst, err = appendJSONValue(ctx, dst, item, opts, level+1)
			if err != nil {
				return dst, err
			}
			if i+1 < len(items) {
				dst = append(dst, opts.itemSep...)
			}
			if opts.hasIndent {
				dst = append(dst, '\n')
			}
		}
		if opts.hasIndent {
			for range level {
				dst = append(dst, opts.indentStr...)
			}
		}
		return append(dst, ']'), nil
	}
	if v.IsDict() {
		d := v.AsDict()
		if opts.sortKeys {
			entries := d.ActiveEntries()
			if len(entries) == 0 {
				return append(dst, "{}"...), nil
			}
			sort.Slice(entries, func(i, j int) bool {
				return entries[i].Key.AsString() < entries[j].Key.AsString()
			})
			dst = append(dst, '{')
			if opts.hasIndent {
				dst = append(dst, '\n')
			}
			for i, e := range entries {
				if opts.hasIndent {
					for range level + 1 {
						dst = append(dst, opts.indentStr...)
					}
				}
				var err error
				dst, err = appendJSONDictKey(dst, e.Key, opts)
				if err != nil {
					return dst, err
				}
				dst = append(dst, opts.keySep...)
				dst, err = appendJSONValue(ctx, dst, e.Value, opts, level+1)
				if err != nil {
					return dst, err
				}
				if i+1 < len(entries) {
					dst = append(dst, opts.itemSep...)
				}
				if opts.hasIndent {
					dst = append(dst, '\n')
				}
			}
			if opts.hasIndent {
				for range level {
					dst = append(dst, opts.indentStr...)
				}
			}
			return append(dst, '}'), nil
		}

		first := true
		dst = append(dst, '{')
		err := d.ForEachActiveErr(func(k, elem val.Value) error {
			if first {
				first = false
				if opts.hasIndent {
					dst = append(dst, '\n')
				}
			} else {
				dst = append(dst, opts.itemSep...)
				if opts.hasIndent {
					dst = append(dst, '\n')
				}
			}
			if opts.hasIndent {
				for range level + 1 {
					dst = append(dst, opts.indentStr...)
				}
			}
			var err error
			dst, err = appendJSONDictKey(dst, k, opts)
			if err != nil {
				return err
			}
			dst = append(dst, opts.keySep...)
			dst, err = appendJSONValue(ctx, dst, elem, opts, level+1)
			return err
		})
		if err != nil {
			return dst, err
		}
		if !first && opts.hasIndent {
			dst = append(dst, '\n')
			for range level {
				dst = append(dst, opts.indentStr...)
			}
		}
		return append(dst, '}'), nil
	}
	if ctx != nil && !opts.defaultFn.IsUnbound() && !opts.defaultFn.IsNone() {
		converted := ctx.Invoke1(opts.defaultFn, v)
		if ctx.HasException() {
			return dst, errors.New("TypeError: default() failed")
		}
		return appendJSONValue(ctx, dst, converted, opts, level)
	}
	return dst, fmt.Errorf("TypeError: Object of type %s is not JSON serializable", v.Type().Name)
}

func appendJSONDictKey(dst []byte, k val.Value, opts jsonDumpOpts) ([]byte, error) {
	if k.IsAnyString() {
		return appendJSONStringVal(dst, k, opts.ensureASCII), nil
	}
	if k.IsNone() {
		return append(dst, `"null"`...), nil
	}
	if k.IsBool() {
		if k.AsBool() {
			return append(dst, `"true"`...), nil
		}
		return append(dst, `"false"`...), nil
	}
	if k.IsInt64() {
		dst = append(dst, '"')
		dst = strconv.AppendInt(dst, k.AsInt64(), 10)
		return append(dst, '"'), nil
	}
	if k.IsFloat64() {
		f := k.AsFloat64()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			if !opts.allowNaN {
				return dst, errors.New("ValueError: Out of range float values are not JSON compliant")
			}
			if math.IsNaN(f) {
				return append(dst, `"NaN"`...), nil
			}
			if f < 0 {
				return append(dst, `"-Infinity"`...), nil
			}
			return append(dst, `"Infinity"`...), nil
		}
		dst = append(dst, '"')
		dst = val.AppendFloatRepr(dst, f)
		return append(dst, '"'), nil
	}
	return dst, fmt.Errorf("TypeError: keys must be str, int, float, bool or None, not %s", k.Type().Name)
}

func appendJSONStringVal(dst []byte, v val.Value, ensureASCII bool) []byte {
	if v.IsInlineStr() {
		raw := v.RawNum()
		n := int(raw & 0xFF)
		var inlineBuf [7]byte
		for i := range n {
			inlineBuf[i] = byte(raw >> ((uint(i) + 1) * 8))
		}
		return appendJSONStringBytes(dst, inlineBuf[:n], ensureASCII)
	}
	if v.IsString() {
		s := v.AsString()
		return appendJSONStringBytes(dst, unsafe.Slice(unsafe.StringData(s), len(s)), ensureASCII)
	}
	s := v.AsString()
	return appendJSONStringBytes(dst, unsafe.Slice(unsafe.StringData(s), len(s)), ensureASCII)
}

func appendJSONHex4(dst []byte, u uint16) []byte {
	return append(dst, '\\', 'u',
		jsonHexDigits[(u>>12)&0xF],
		jsonHexDigits[(u>>8)&0xF],
		jsonHexDigits[(u>>4)&0xF],
		jsonHexDigits[u&0xF],
	)
}

func appendJSONStringBytes(dst []byte, b []byte, ensureASCII bool) []byte {
	dst = append(dst, '"')
	i := 0
	n := len(b)
	for i < n {
		var span int
		if ensureASCII {
			span = val.FindJSONEscapeASCII(b[i:])
		} else {
			span = val.FindJSONEscape(b[i:])
		}
		if span > 0 {
			dst = append(dst, b[i:i+span]...)
			i += span
			if i >= n {
				break
			}
		}
		c := b[i]
		if c >= 0x80 && ensureASCII {
			r, sz := utf8.DecodeRune(b[i:])
			if r <= 0xFFFF {
				dst = appendJSONHex4(dst, uint16(r))
			} else {
				r1, r2 := utf16.EncodeRune(r)
				dst = appendJSONHex4(dst, uint16(r1))
				dst = appendJSONHex4(dst, uint16(r2))
			}
			i += sz
			continue
		}
		i++
		switch c {
		case '"':
			dst = append(dst, `\"`...)
		case '\\':
			dst = append(dst, `\\`...)
		case '\n':
			dst = append(dst, `\n`...)
		case '\r':
			dst = append(dst, `\r`...)
		case '\t':
			dst = append(dst, `\t`...)
		case '\b':
			dst = append(dst, `\b`...)
		case '\f':
			dst = append(dst, `\f`...)
		default:
			dst = appendJSONHex4(dst, uint16(c))
		}
	}
	return append(dst, '"')
}

func applyJSONObjectHook(ctx *val.ExecContext, v val.Value, hook val.Value) val.Value {
	if hook.IsNone() || hook.IsUnbound() {
		return v
	}
	if v.IsList() {
		lst := v.AsList()
		for i, item := range lst.Items {
			lst.Items[i] = applyJSONObjectHook(ctx, item, hook)
		}
		return v
	}
	if v.IsDict() {
		d := v.AsDict()
		for i := range d.Entries {
			if d.Entries[i].Active {
				d.Entries[i].Value = applyJSONObjectHook(ctx, d.Entries[i].Value, hook)
			}
		}
		return ctx.Invoke1(hook, v)
	}
	return v
}

// createJSONModule builds the native json PyModule Value.
func (e *Engine) createJSONModule() val.Value {
	modVal := val.NewModuleVal("json", "stdlib:lib/json/__init__.py")
	mod := modVal.AsModule()
	mod.Dict.Set(val.InternString("__package__"), val.NewStringVal("json"))
	mod.Dict.Set(val.InternString("__path__"), val.NewListVal(val.NewStringVal("stdlib:lib/json")))
	jsonDecErrType := initJSONDecodeErrorType()
	mod.Dict.Set(val.InternString("JSONDecodeError"), val.TypeVal(jsonDecErrType))

	mod.Dict.Set(val.InternString("loads"), val.NewBuiltinFuncVal("loads", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			if v, ok := kwArg(args, kwnames, "s"); ok {
				pa = []val.Value{v}
			} else {
				ctx.Raise(val.NewException(val.TypeErrorType, "loads() takes at least 1 argument"))
				return val.NoneVal()
			}
		}
		var rawBytes []byte
		a := pa[0]
		switch {
		case a.IsAnyString():
			s := a.AsString()
			rawBytes = unsafe.Slice(unsafe.StringData(s), len(s))
		case a.IsBytes():
			rawBytes = a.AsBytes().Data
		case a.IsByteArray():
			rawBytes = a.AsByteArray().Data
		case a.IsMemoryView():
			rawBytes = a.AsMemoryView().Buf
		default:
			ctx.Raise(val.NewException(val.TypeErrorType, "the JSON object must be str, bytes or bytearray, not"+a.Type().Name))
			return val.NoneVal()
		}

		p := acquireJSONParser(ctx, rawBytes, e.internPool)
		res, err := p.parseValue()
		releaseJSONParser(ctx, p)
		if err != nil {
			panic(newJSONDecodeError(err, rawBytes))
		}
		if hook, ok := kwArg(args, kwnames, "object_hook"); ok && !hook.IsNone() {
			res = applyJSONObjectHook(ctx, res, hook)
		}
		return res
	}))

	mod.Dict.Set(val.InternString("dumps"), val.NewBuiltinFuncVal("dumps", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			if v, ok := kwArg(args, kwnames, "obj"); ok {
				pa = []val.Value{v}
			} else {
				ctx.Raise(val.NewException(val.TypeErrorType, "dumps() takes at least 1 argument"))
				return val.NoneVal()
			}
		}
		opts := extractJSONDumpOpts(pa, args, kwnames, 1)
		res, err := jsonDumpsValue(ctx, pa[0], opts)
		if err != nil {
			raiseJSONError(ctx, err)
			return val.NoneVal()
		}
		return res
	}))

	mod.Dict.Set(val.InternString("load"), val.NewBuiltinFuncVal("load", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			if v, ok := kwArg(args, kwnames, "fp"); ok {
				pa = []val.Value{v}
			} else {
				ctx.Raise(val.NewException(val.TypeErrorType, "load() takes at least 1 argument"))
				return val.NoneVal()
			}
		}
		fp := pa[0]
		readMethod := val.GetAttrCtx(ctx, fp, nameRead)
		content := ctx.Invoke0(readMethod)
		var rawBytes []byte
		switch {
		case content.IsAnyString():
			rawBytes = []byte(content.AsString())
		case content.IsBytes():
			rawBytes = content.AsBytes().Data
		default:
			ctx.Raise(val.NewException(val.TypeErrorType, "fp.read() must return str or bytes"))
			return val.NoneVal()
		}
		p := acquireJSONParser(ctx, rawBytes, e.internPool)
		res, err := p.parseValue()
		releaseJSONParser(ctx, p)
		if err != nil {
			panic(newJSONDecodeError(err, rawBytes))
		}
		if hook, ok := kwArg(args, kwnames, "object_hook"); ok && !hook.IsNone() {
			res = applyJSONObjectHook(ctx, res, hook)
		}
		return res
	}))

	mod.Dict.Set(val.InternString("dump"), val.NewBuiltinFuncVal("dump", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 2 {
			ctx.Raise(val.NewException(val.TypeErrorType, "dump() takes at least 2 arguments"))
			return val.NoneVal()
		}
		opts := extractJSONDumpOpts(pa, args, kwnames, 2)
		resVal, err := jsonDumpsValue(ctx, pa[0], opts)
		if err != nil {
			raiseJSONError(ctx, err)
			return val.NoneVal()
		}
		fp := pa[1]
		writeMethod := val.GetAttrCtx(ctx, fp, nameWrite)
		ctx.Invoke1(writeMethod, resVal)
		return val.NoneVal()
	}))

	return modVal
}
