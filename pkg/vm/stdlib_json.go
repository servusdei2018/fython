package vm

import (
	"errors"
	"fmt"
	"math"
	"math/big"
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

var errJSONHookRaised = errors.New("json hook raised python exception")

type jsonLoadOpts struct {
	objectHook      val.Value
	objectPairsHook val.Value
	parseFloat      val.Value
	parseInt        val.Value
	parseConstant   val.Value
	strict          bool
	hasHooks        bool
}

func defaultJSONLoadOpts() jsonLoadOpts {
	return jsonLoadOpts{strict: true}
}

func extractJSONLoadOpts(args []val.Value, kwnames []val.Value) jsonLoadOpts {
	opts := defaultJSONLoadOpts()
	if len(kwnames) == 0 {
		return opts
	}
	if v, ok := kwArg(args, kwnames, "object_hook"); ok && !v.IsNone() && !v.IsUnbound() {
		opts.objectHook = v
		opts.hasHooks = true
	}
	if v, ok := kwArg(args, kwnames, "object_pairs_hook"); ok && !v.IsNone() && !v.IsUnbound() {
		opts.objectPairsHook = v
		opts.hasHooks = true
	}
	if v, ok := kwArg(args, kwnames, "parse_float"); ok && !v.IsNone() && !v.IsUnbound() {
		opts.parseFloat = v
		opts.hasHooks = true
	}
	if v, ok := kwArg(args, kwnames, "parse_int"); ok && !v.IsNone() && !v.IsUnbound() {
		opts.parseInt = v
		opts.hasHooks = true
	}
	if v, ok := kwArg(args, kwnames, "parse_constant"); ok && !v.IsNone() && !v.IsUnbound() {
		opts.parseConstant = v
		opts.hasHooks = true
	}
	if v, ok := kwArg(args, kwnames, "strict"); ok {
		opts.strict = v.IsTruthy()
	}
	return opts
}

// jsonParser provides single-pass, allocation-optimized JSON parsing into fython Values.
type jsonParser struct {
	src         []byte
	pos         int
	pool        *val.InternPool
	scratch     []byte
	kvScratch   []val.DictEntry
	elemScratch []val.Value
	vm          *VM
	ctx         *val.ExecContext
	opts        jsonLoadOpts
}

func acquireJSONParser(ctx *val.ExecContext, src []byte, pool *val.InternPool, opts jsonLoadOpts) *jsonParser {
	var vm *VM
	if ctx != nil && ctx.VM != nil && !val.MultiThreadedFlag.Load() {
		if v, ok := ctx.VM.(*VM); ok {
			vm = v
			if v.jsonParser != nil {
				p := v.jsonParser
				v.jsonParser = nil
				p.src = src
				p.pos = 0
				p.pool = pool
				p.scratch = p.scratch[:0]
				p.kvScratch = p.kvScratch[:0]
				p.elemScratch = p.elemScratch[:0]
				p.vm = vm
				p.ctx = ctx
				p.opts = opts
				return p
			}
		}
	}
	return &jsonParser{
		src:         src,
		pool:        pool,
		scratch:     make([]byte, 0, 64),
		kvScratch:   make([]val.DictEntry, 0, 32),
		elemScratch: make([]val.Value, 0, 32),
		vm:          vm,
		ctx:         ctx,
		opts:        opts,
	}
}

func releaseJSONParser(ctx *val.ExecContext, p *jsonParser) {
	if p == nil {
		return
	}
	p.src = nil
	p.pool = nil
	p.ctx = nil
	p.opts = jsonLoadOpts{}
	if len(p.kvScratch) > 0 {
		clear(p.kvScratch)
		p.kvScratch = p.kvScratch[:0]
	}
	if len(p.elemScratch) > 0 {
		clear(p.elemScratch)
		p.elemScratch = p.elemScratch[:0]
	}
	if cap(p.scratch) > 65536 {
		p.scratch = nil
	}
	if cap(p.kvScratch) > 4096 {
		p.kvScratch = nil
	}
	if cap(p.elemScratch) > 8192 {
		p.elemScratch = nil
	}
	if ctx != nil && ctx.VM != nil && !val.MultiThreadedFlag.Load() {
		if vm, ok := ctx.VM.(*VM); ok && vm.jsonParser == nil {
			vm.jsonParser = p
		}
	}
}

func (p *jsonParser) acquireSmallDict(n int) val.Value {
	if p.vm != nil && !val.MultiThreadedFlag.Load() && n <= 20 {
		return p.vm.acquireSmallDict(n, false)
	}
	return val.NewDictVal(n)
}

func (p *jsonParser) invokeHook(hook val.Value, arg val.Value) (val.Value, error) {
	if p.ctx == nil {
		return arg, nil
	}
	res := p.ctx.Invoke1(hook, arg)
	if p.ctx.HasException() {
		return val.NoneVal(), errJSONHookRaised
	}
	return res, nil
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
	case 'N':
		if p.pos+3 <= len(p.src) && p.src[p.pos+1] == 'a' && p.src[p.pos+2] == 'N' {
			p.pos += 3
			if p.opts.hasHooks && !p.opts.parseConstant.IsUnbound() && !p.opts.parseConstant.IsNone() {
				return p.invokeHook(p.opts.parseConstant, val.InternString("NaN"))
			}
			return val.Float64Val(math.NaN()), nil
		}
		return val.NoneVal(), p.errNow("Expecting value")
	case 'I':
		if p.pos+8 <= len(p.src) && string(p.src[p.pos:p.pos+8]) == "Infinity" {
			p.pos += 8
			if p.opts.hasHooks && !p.opts.parseConstant.IsUnbound() && !p.opts.parseConstant.IsNone() {
				return p.invokeHook(p.opts.parseConstant, val.InternString("Infinity"))
			}
			return val.Float64Val(math.Inf(1)), nil
		}
		return val.NoneVal(), p.errNow("Expecting value")
	case '-':
		if p.pos+9 <= len(p.src) && p.src[p.pos+1] == 'I' && string(p.src[p.pos+1:p.pos+9]) == "Infinity" {
			p.pos += 9
			if p.opts.hasHooks && !p.opts.parseConstant.IsUnbound() && !p.opts.parseConstant.IsNone() {
				return p.invokeHook(p.opts.parseConstant, val.InternString("-Infinity"))
			}
			return val.Float64Val(math.Inf(-1)), nil
		}
		return p.parseNumber()
	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return p.parseNumber()
	default:
		return val.NoneVal(), p.errNow("Expecting value")
	}
}

func (p *jsonParser) parseObject() (val.Value, error) {
	p.pos++ // consume '{'
	p.skipWhitespace()
	if p.pos < len(p.src) && p.src[p.pos] == '}' {
		p.pos++
		if p.opts.hasHooks {
			if !p.opts.objectPairsHook.IsUnbound() && !p.opts.objectPairsHook.IsNone() {
				return p.invokeHook(p.opts.objectPairsHook, val.NewListVal())
			}
			if !p.opts.objectHook.IsUnbound() && !p.opts.objectHook.IsNone() {
				return p.invokeHook(p.opts.objectHook, p.acquireSmallDict(0))
			}
		}
		return p.acquireSmallDict(0), nil
	}

	base := len(p.kvScratch)
	for {
		p.skipWhitespace()
		if p.pos >= len(p.src) || p.src[p.pos] != '"' {
			clear(p.kvScratch[base:])
			p.kvScratch = p.kvScratch[:base]
			return val.NoneVal(), p.errNow("Expecting property name enclosed in double quotes")
		}

		keyVal, err := p.parseString(true)
		if err != nil {
			clear(p.kvScratch[base:])
			p.kvScratch = p.kvScratch[:base]
			return val.NoneVal(), err
		}

		p.skipWhitespace()
		if p.pos >= len(p.src) || p.src[p.pos] != ':' {
			clear(p.kvScratch[base:])
			p.kvScratch = p.kvScratch[:base]
			return val.NoneVal(), p.errNow("Expecting ':' delimiter")
		}
		p.pos++ // consume ':'

		valElem, err := p.parseValue()
		if err != nil {
			clear(p.kvScratch[base:])
			p.kvScratch = p.kvScratch[:base]
			return val.NoneVal(), err
		}

		p.kvScratch = append(p.kvScratch, val.DictEntry{Key: keyVal, Value: valElem})

		p.skipWhitespace()
		if p.pos >= len(p.src) {
			clear(p.kvScratch[base:])
			p.kvScratch = p.kvScratch[:base]
			return val.NoneVal(), p.errNow("Expecting ',' delimiter")
		}

		if p.src[p.pos] == '}' {
			p.pos++
			entries := p.kvScratch[base:]
			n := len(entries)
			if p.opts.hasHooks && !p.opts.objectPairsHook.IsUnbound() && !p.opts.objectPairsHook.IsNone() {
				pairs := make([]val.Value, n)
				for i, e := range entries {
					pairs[i] = val.NewTupleVal(e.Key, e.Value)
				}
				clear(p.kvScratch[base:])
				p.kvScratch = p.kvScratch[:base]
				return p.invokeHook(p.opts.objectPairsHook, val.NewListFromOwnedSlice(pairs))
			}

			dictVal := p.acquireSmallDict(n)
			d := dictVal.AsDict()
			for i := range entries {
				d.PutNew(entries[i].Key, entries[i].Value)
			}
			clear(p.kvScratch[base:])
			p.kvScratch = p.kvScratch[:base]
			if p.opts.hasHooks && !p.opts.objectHook.IsUnbound() && !p.opts.objectHook.IsNone() {
				return p.invokeHook(p.opts.objectHook, dictVal)
			}
			return dictVal, nil
		}
		if p.src[p.pos] == ',' {
			commaPos := p.pos
			p.pos++
			p.skipWhitespace()
			if p.pos < len(p.src) && p.src[p.pos] == '}' {
				clear(p.kvScratch[base:])
				p.kvScratch = p.kvScratch[:base]
				return val.NoneVal(), p.errAt(commaPos, "Illegal trailing comma before end of object")
			}
			continue
		}
		clear(p.kvScratch[base:])
		p.kvScratch = p.kvScratch[:base]
		return val.NoneVal(), p.errNow("Expecting ',' delimiter")
	}
}

func (p *jsonParser) parseArray() (val.Value, error) {
	p.pos++ // consume '['
	p.skipWhitespace()
	if p.pos < len(p.src) && p.src[p.pos] == ']' {
		p.pos++
		return val.NewListVal(), nil
	}

	base := len(p.elemScratch)
	for {
		elem, err := p.parseValue()
		if err != nil {
			clear(p.elemScratch[base:])
			p.elemScratch = p.elemScratch[:base]
			return val.NoneVal(), err
		}
		p.elemScratch = append(p.elemScratch, elem)

		p.skipWhitespace()
		if p.pos >= len(p.src) {
			clear(p.elemScratch[base:])
			p.elemScratch = p.elemScratch[:base]
			return val.NoneVal(), p.errNow("Expecting ',' delimiter")
		}

		if p.src[p.pos] == ']' {
			p.pos++
			res := val.NewListFromSlice(p.elemScratch[base:])
			clear(p.elemScratch[base:])
			p.elemScratch = p.elemScratch[:base]
			return res, nil
		}
		if p.src[p.pos] == ',' {
			commaPos := p.pos
			p.pos++
			p.skipWhitespace()
			if p.pos < len(p.src) && p.src[p.pos] == ']' {
				clear(p.elemScratch[base:])
				p.elemScratch = p.elemScratch[:base]
				return val.NoneVal(), p.errAt(commaPos, "Illegal trailing comma before end of array")
			}
			continue
		}
		clear(p.elemScratch[base:])
		p.elemScratch = p.elemScratch[:base]
		return val.NoneVal(), p.errNow("Expecting ',' delimiter")
	}
}

func (p *jsonParser) materializeParsedString(b []byte, isKey bool) val.Value {
	if isKey && len(b) <= 32 {
		return val.InternBytes(b)
	}
	return val.NewStringFromBytes(b)
}

func (p *jsonParser) parseString(isKey bool) (val.Value, error) {
	p.pos++ // consume opening '"'
	start := p.pos
	hasEscapes := false

	for p.pos < len(p.src) {
		span := val.FindJSONEscape(p.src[p.pos:])
		p.pos += span
		if p.pos >= len(p.src) {
			break
		}
		b := p.src[p.pos]
		if b == '"' {
			raw := p.src[start:p.pos]
			p.pos++ // consume closing '"'
			if !hasEscapes {
				return p.materializeParsedString(raw, isKey), nil
			}
			decoded, err := p.decodeEscapes(raw, start)
			if err != nil {
				return val.NoneVal(), err
			}
			return p.materializeParsedString(decoded, isKey), nil
		}
		if b == '\\' {
			if p.pos+1 >= len(p.src) {
				return val.NoneVal(), p.errAt(start-1, "Unterminated string starting at")
			}
			hasEscapes = true
			p.pos += 2
			continue
		}
		// Control character (< 0x20)
		if p.opts.strict {
			return val.NoneVal(), p.errNow("Invalid control character at")
		}
		p.pos++
	}

	return val.NoneVal(), p.errAt(start-1, "Unterminated string starting at")
}

func parseHex4(b []byte) (uint16, bool) {
	var v uint16
	for i := range 4 {
		c := b[i]
		var d uint16
		switch {
		case c >= '0' && c <= '9':
			d = uint16(c - '0')
		case c >= 'a' && c <= 'f':
			d = uint16(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = uint16(c-'A') + 10
		default:
			return 0, false
		}
		v = (v << 4) | d
	}
	return v, true
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
					return nil, p.errAt(base+i-1, "Invalid \\uXXXX escape")
				}
				r, ok := parseHex4(raw[i+1 : i+5])
				if !ok {
					return nil, p.errAt(base+i-1, "Invalid \\uXXXX escape")
				}
				i += 4
				// Combine UTF-16 surrogate pairs; lone surrogates pass through encodeRune.
				if r >= 0xD800 && r <= 0xDBFF {
					if i+6 < len(raw) && raw[i+1] == '\\' && raw[i+2] == 'u' {
						r2, ok2 := parseHex4(raw[i+3 : i+7])
						if ok2 && r2 >= 0xDC00 && r2 <= 0xDFFF {
							combined := utf16.DecodeRune(rune(r), rune(r2))
							var utf8Buf [4]byte
							n := encodeRune(combined, utf8Buf[:])
							p.scratch = append(p.scratch, utf8Buf[:n]...)
							i += 7
							continue
						}
					}
				}
				var utf8Buf [4]byte
				n := encodeRune(rune(r), utf8Buf[:])
				p.scratch = append(p.scratch, utf8Buf[:n]...)
			default:
				return nil, p.errAt(base+i-1, "Invalid \\escape")
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
	hasExp := false
	neg := false

	if p.src[p.pos] == '-' {
		neg = true
		p.pos++
	}

	if p.pos >= len(p.src) || p.src[p.pos] < '0' || p.src[p.pos] > '9' {
		return val.NoneVal(), p.errAt(start, "Expecting value")
	}

	var uval uint64
	overflow := false
	digitStart := p.pos
	if p.src[p.pos] == '0' {
		p.pos++
	} else {
		const maxDiv10 = uint64(math.MaxInt64) / 10
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			d := uint64(p.src[p.pos] - '0')
			if !overflow {
				if uval > maxDiv10 || (uval == maxDiv10 && ((neg && d > 8) || (!neg && d > 7))) {
					overflow = true
				} else {
					uval = uval*10 + d
				}
			}
			p.pos++
		}
	}
	digits := p.pos - digitStart

	var fracVal uint64
	fracDigits := 0
	if p.pos < len(p.src) && p.src[p.pos] == '.' {
		dotPos := p.pos
		p.pos++
		fracStart := p.pos
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			fracVal = fracVal*10 + uint64(p.src[p.pos]-'0')
			p.pos++
		}
		fracDigits = p.pos - fracStart
		if fracDigits == 0 {
			p.pos = dotPos
		} else {
			isFloat = true
		}
	}

	if p.pos < len(p.src) && (p.src[p.pos] == 'e' || p.src[p.pos] == 'E') {
		expPos := p.pos
		p.pos++
		if p.pos < len(p.src) && (p.src[p.pos] == '+' || p.src[p.pos] == '-') {
			p.pos++
		}
		expDigits := 0
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			expDigits++
			p.pos++
		}
		if expDigits == 0 {
			p.pos = expPos
		} else {
			isFloat = true
			hasExp = true
		}
	}

	if !isFloat {
		if p.opts.hasHooks && !p.opts.parseInt.IsUnbound() && !p.opts.parseInt.IsNone() {
			return p.invokeHook(p.opts.parseInt, val.NewStringFromBytes(p.src[start:p.pos]))
		}
		if !overflow {
			if neg {
				return val.Int64Val(-int64(uval)), nil
			}
			return val.Int64Val(int64(uval)), nil
		}
		raw := p.src[start:p.pos]
		numStr := unsafe.String(unsafe.SliceData(raw), len(raw))
		bi, ok := new(big.Int).SetString(numStr, 10)
		if !ok {
			return val.NoneVal(), p.errAt(start, "Expecting value")
		}
		return val.NewBigIntVal(bi), nil
	}

	if p.opts.hasHooks && !p.opts.parseFloat.IsUnbound() && !p.opts.parseFloat.IsNone() {
		return p.invokeHook(p.opts.parseFloat, val.NewStringFromBytes(p.src[start:p.pos]))
	}
	if !hasExp && !overflow && digits > 0 && fracDigits > 0 && digits+fracDigits <= 15 && fracDigits <= 6 {
		pow10 := [7]float64{1, 10, 100, 1000, 10000, 100000, 1000000}[fracDigits]
		f := float64(uval) + float64(fracVal)/pow10
		if neg {
			f = -f
		}
		return val.Float64Val(f), nil
	}

	raw := p.src[start:p.pos]
	numStr := unsafe.String(unsafe.SliceData(raw), len(raw))
	f, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return val.NoneVal(), err
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
		var stackBuf [512]byte
		buf = stackBuf[:0]
	}
	var out []byte
	var err error
	if !opts.hasIndent && !opts.sortKeys && opts.ensureASCII && opts.allowNaN &&
		opts.itemSep == ", " && opts.keySep == ": " &&
		(opts.defaultFn.IsUnbound() || opts.defaultFn.IsNone()) {
		out, err = appendJSONValueDefault(ctx, buf, v)
	} else {
		out, err = appendJSONValue(ctx, buf, v, opts, 0)
	}
	if err != nil {
		if vmPtr != nil && vmPtr.jsonDumpBuf == nil {
			if out != nil && cap(out) <= 65536 {
				vmPtr.jsonDumpBuf = out[:0]
			} else if cap(buf) <= 65536 && cap(buf) > 0 {
				vmPtr.jsonDumpBuf = buf[:0]
			}
		}
		return val.NoneVal(), err
	}
	var res val.Value
	if len(out) <= 7 {
		res = val.SmallStringFromBytes(out)
	} else {
		res = val.NewStringVal(string(out))
	}
	if vmPtr != nil && vmPtr.jsonDumpBuf == nil {
		if out != nil && cap(out) <= 65536 {
			vmPtr.jsonDumpBuf = out[:0]
		} else if cap(buf) <= 65536 && cap(buf) > 0 {
			vmPtr.jsonDumpBuf = buf[:0]
		}
	}
	return res, nil
}

// appendJSONValueDefault is the specialized fast path for json.dumps(v) with
// default options (ensure_ascii=True, sort_keys=False, indent=None, allow_nan=True,
// separators=(', ', ': '), default=None).
func appendJSONValueDefault(ctx *val.ExecContext, dst []byte, v val.Value) ([]byte, error) {
	if v.IsInlineStr() {
		return appendJSONStringVal(dst, v, true), nil
	}
	hdr := v.HeaderPtr()
	if hdr == nil {
		return append(dst, "null"...), nil
	}
	switch hdr.Kind {
	case val.KindNone:
		return append(dst, "null"...), nil
	case val.KindBool:
		if v.AsBool() {
			return append(dst, "true"...), nil
		}
		return append(dst, "false"...), nil
	case val.KindInt64:
		return strconv.AppendInt(dst, v.AsInt64(), 10), nil
	case val.KindFloat64:
		f := v.AsFloat64()
		if math.IsNaN(f) {
			return append(dst, "NaN"...), nil
		}
		if math.IsInf(f, 0) {
			if f < 0 {
				return append(dst, "-Infinity"...), nil
			}
			return append(dst, "Infinity"...), nil
		}
		return val.AppendFloatRepr(dst, f), nil
	case val.KindString:
		ps := v.AsStringObj()
		if ps.Buf != nil {
			return appendJSONStringBytes(dst, ps.Buf, true), nil
		}
		return appendJSONStringBytes(dst, unsafe.Slice(unsafe.StringData(ps.Data), len(ps.Data)), true), nil
	case val.KindList, val.KindTuple:
		var items []val.Value
		if hdr.Kind == val.KindList {
			items = v.AsList().Items
		} else {
			items = v.AsTuple().Items
		}
		if len(items) == 0 {
			return append(dst, "[]"...), nil
		}
		dst = append(dst, '[')
		for i, item := range items {
			if i > 0 {
				dst = append(dst, ',', ' ')
			}
			var err error
			dst, err = appendJSONValueDefault(ctx, dst, item)
			if err != nil {
				return dst, err
			}
		}
		return append(dst, ']'), nil
	case val.KindDict:
		d := v.AsDict()
		if d.Live == nil && !d.Shared && !val.MultiThreadedFlag.Load() {
			if d.Len() == 0 {
				return append(dst, "{}"...), nil
			}
			dst = append(dst, '{')
			first := true
			for i := range d.Entries {
				e := &d.Entries[i]
				if !e.Active {
					continue
				}
				if first {
					first = false
				} else {
					dst = append(dst, ',', ' ')
				}
				k := e.Key
				switch {
				case k.IsString():
					ps := k.AsStringObj()
					if ps.Buf != nil {
						dst = appendJSONStringBytes(dst, ps.Buf, true)
					} else {
						dst = appendJSONStringBytes(dst, unsafe.Slice(unsafe.StringData(ps.Data), len(ps.Data)), true)
					}
				case k.IsInlineStr():
					dst = appendJSONStringVal(dst, k, true)
				default:
					var err error
					dst, err = appendJSONDictKey(dst, k, jsonDumpOpts{ensureASCII: true, allowNaN: true})
					if err != nil {
						return dst, err
					}
				}
				dst = append(dst, ':', ' ')
				var err error
				dst, err = appendJSONValueDefault(ctx, dst, e.Value)
				if err != nil {
					return dst, err
				}
			}
			return append(dst, '}'), nil
		}
		return appendJSONValue(ctx, dst, v, jsonDumpOpts{ensureASCII: true, allowNaN: true, itemSep: ", ", keySep: ": "}, 0)
	}
	return appendJSONValue(ctx, dst, v, jsonDumpOpts{ensureASCII: true, allowNaN: true, itemSep: ", ", keySep: ": "}, 0)
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

func decodeJSONBytes(ctx *val.ExecContext, pool *val.InternPool, rawBytes []byte, opts jsonLoadOpts) val.Value {
	p := acquireJSONParser(ctx, rawBytes, pool, opts)
	res, err := p.parseValue()
	if err == nil {
		p.skipWhitespace()
		if p.pos < len(p.src) {
			err = p.errNow("Extra data")
		}
	}
	releaseJSONParser(ctx, p)
	if err != nil {
		if errors.Is(err, errJSONHookRaised) {
			return val.NoneVal()
		}
		decErr := newJSONDecodeError(err, rawBytes)
		if ctx != nil {
			ctx.Raise(decErr)
			return val.NoneVal()
		}
		panic(decErr)
	}
	return res
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

		return decodeJSONBytes(ctx, e.internPool, rawBytes, extractJSONLoadOpts(args, kwnames))
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
		if ctx != nil && ctx.HasException() {
			return val.NoneVal()
		}
		var rawBytes []byte
		switch {
		case content.IsAnyString():
			s := content.AsString()
			rawBytes = unsafe.Slice(unsafe.StringData(s), len(s))
		case content.IsBytes():
			rawBytes = content.AsBytes().Data
		default:
			ctx.Raise(val.NewException(val.TypeErrorType, "fp.read() must return str or bytes"))
			return val.NoneVal()
		}
		return decodeJSONBytes(ctx, e.internPool, rawBytes, extractJSONLoadOpts(args, kwnames))
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
