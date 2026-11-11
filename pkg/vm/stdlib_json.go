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
				panic("TypeError: JSONDecodeError() takes exactly 3 arguments")
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
				inst.SetAttr(val.InternString("args"), val.NewTupleVal(val.NewStringVal(errmsg)))
				inst.SetAttr(val.InternString("msg"), val.NewStringVal(msg))
				inst.SetAttr(val.InternString("doc"), val.NewStringVal(doc))
				inst.SetAttr(val.InternString("pos"), val.Int64Val(int64(pos)))
				inst.SetAttr(val.InternString("lineno"), val.Int64Val(int64(lineno)))
				inst.SetAttr(val.InternString("colno"), val.Int64Val(int64(colno)))
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
	inst.AsInstance().SetAttr(val.InternString("args"), val.NewTupleVal(val.NewStringVal(errmsg)))
	inst.AsInstance().SetAttr(val.InternString("msg"), val.NewStringVal(msg))
	inst.AsInstance().SetAttr(val.InternString("doc"), val.NewStringVal(docStr))
	inst.AsInstance().SetAttr(val.InternString("pos"), val.Int64Val(int64(pos)))
	inst.AsInstance().SetAttr(val.InternString("lineno"), val.Int64Val(int64(lineno)))
	inst.AsInstance().SetAttr(val.InternString("colno"), val.Int64Val(int64(colno)))
	inst.AsInstance().SetAttr(val.InternString("__traceback__"), val.NoneVal())
	inst.AsInstance().SetAttr(val.InternString("__cause__"), val.NoneVal())
	inst.AsInstance().SetAttr(val.InternString("__context__"), val.NoneVal())
	inst.AsInstance().SetAttr(val.InternString("__suppress_context__"), val.BoolVal(false))
	return val.ObjectVal(&inst.AsInstance().Header)
}

// jsonParser provides direct, allocation-optimized JSON parsing into fython Values.
type jsonParser struct {
	src     []byte
	pos     int
	pool    *val.InternPool
	scratch []byte
}

func newJSONParser(src []byte, pool *val.InternPool) *jsonParser {
	return &jsonParser{
		src:     src,
		pos:     0,
		pool:    pool,
		scratch: make([]byte, 0, 64),
	}
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
		return p.parseLiteral("true", val.BoolVal(true))
	case 'f':
		return p.parseLiteral("false", val.BoolVal(false))
	case 'n':
		return p.parseLiteral("null", val.NoneVal())
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return p.parseNumber()
	default:
		return val.NoneVal(), p.errNow("Expecting value")
	}
}

func (p *jsonParser) parseObject() (val.Value, error) {
	p.pos++ // consume '{'
	dictVal := val.NewDictVal(8)
	dict := dictVal.AsDict()

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

		dict.Set(keyVal, valElem)

		p.skipWhitespace()
		if p.pos >= len(p.src) {
			return val.NoneVal(), p.errNow("Expecting ',' delimiter")
		}

		if p.src[p.pos] == '}' {
			p.pos++
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
	items := make([]val.Value, 0, 8)

	p.skipWhitespace()
	if p.pos < len(p.src) && p.src[p.pos] == ']' {
		p.pos++
		return val.NewListVal(items...), nil
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
			return val.NewListVal(items...), nil
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
				if isKey || len(raw) <= 7 {
					if len(raw) <= 7 {
						// Small String Optimization: 0 heap allocations, 0 string allocations!
						return val.SmallStringFromBytes(raw), nil
					}
					if p.pool != nil {
						// String interning path: 0 duplicate heap strings!
						return p.pool.InternBytes(raw), nil
					}
					return val.InternString(string(raw)), nil
				}
				return val.NewStringVal(string(raw)), nil
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

	if p.src[p.pos] == '-' {
		p.pos++
	}

	for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
		p.pos++
	}

	if p.pos < len(p.src) && p.src[p.pos] == '.' {
		isFloat = true
		p.pos++
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			p.pos++
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

	numStr := string(p.src[start:p.pos])
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

// jsonDumps serializes a Value directly to JSON string without reflection.
func jsonDumps(v val.Value, indent int) (string, error) {
	var sb strings.Builder
	err := writeJSONValue(&sb, v, indent, 0)
	if err != nil {
		return "", err
	}
	return sb.String(), nil
}

func writeJSONValue(sb *strings.Builder, v val.Value, indent int, level int) error {
	if v.IsNone() {
		sb.WriteString("null")
		return nil
	}
	if v.IsBool() {
		if v.AsBool() {
			sb.WriteString("true")
		} else {
			sb.WriteString("false")
		}
		return nil
	}
	if v.IsInt64() {
		sb.WriteString(strconv.FormatInt(v.AsInt64(), 10))
		return nil
	}
	if v.IsFloat64() {
		f := v.AsFloat64()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return errors.New("ValueError: Out of range float values are not JSON compliant")
		}
		sb.WriteString(val.FloatRepr(f))
		return nil
	}
	if v.IsAnyString() {
		writeJSONString(sb, v.AsString())
		return nil
	}
	if v.IsBytes() || v.IsByteArray() || v.IsMemoryView() {
		return fmt.Errorf("TypeError: Object of type %s is not JSON serializable", v.Type().Name)
	}
	if v.IsList() {
		items := v.AsList().Items
		if len(items) == 0 {
			sb.WriteString("[]")
			return nil
		}
		sb.WriteString("[")
		if indent > 0 {
			sb.WriteString("\n")
		}
		for i, item := range items {
			if indent > 0 {
				writeIndent(sb, indent, level+1)
			}
			if err := writeJSONValue(sb, item, indent, level+1); err != nil {
				return err
			}
			if i+1 < len(items) {
				sb.WriteString(",")
			}
			if indent > 0 {
				sb.WriteString("\n")
			} else if i+1 < len(items) {
				sb.WriteString(" ")
			}
		}
		if indent > 0 {
			writeIndent(sb, indent, level)
		}
		sb.WriteString("]")
		return nil
	}
	if v.IsTuple() {
		items := v.AsTuple().Items
		if len(items) == 0 {
			sb.WriteString("[]")
			return nil
		}
		sb.WriteString("[")
		if indent > 0 {
			sb.WriteString("\n")
		}
		for i, item := range items {
			if indent > 0 {
				writeIndent(sb, indent, level+1)
			}
			if err := writeJSONValue(sb, item, indent, level+1); err != nil {
				return err
			}
			if i+1 < len(items) {
				sb.WriteString(",")
			}
			if indent > 0 {
				sb.WriteString("\n")
			} else if i+1 < len(items) {
				sb.WriteString(" ")
			}
		}
		if indent > 0 {
			writeIndent(sb, indent, level)
		}
		sb.WriteString("]")
		return nil
	}
	if v.IsDict() {
		d := v.AsDict()
		first := true
		sb.WriteString("{")
		entries := d.ActiveEntries()
		for _, e := range entries {
			if !e.Active {
				continue
			}
			if first {
				first = false
				if indent > 0 {
					sb.WriteString("\n")
				}
			} else {
				sb.WriteString(",")
				if indent > 0 {
					sb.WriteString("\n")
				} else {
					sb.WriteString(" ")
				}
			}
			if indent > 0 {
				writeIndent(sb, indent, level+1)
			}
			writeJSONString(sb, e.Key.AsString())
			sb.WriteString(": ")
			if err := writeJSONValue(sb, e.Value, indent, level+1); err != nil {
				return err
			}
		}
		if !first && indent > 0 {
			sb.WriteString("\n")
			writeIndent(sb, indent, level)
		}
		sb.WriteString("}")
		return nil
	}

	return fmt.Errorf("TypeError: Object of type %s is not JSON serializable", v.Type().Name)
}

type jsonDumpOpts struct {
	indentStr string
	hasIndent bool
	sortKeys  bool
	itemSep   string
	keySep    string
}

func extractJSONDumpOpts(posArgs []val.Value, allArgs []val.Value, kwnames []val.Value, indentPosIdx int) jsonDumpOpts {
	opts := jsonDumpOpts{
		itemSep: ", ",
		keySep:  ": ",
	}
	indentVal := val.NoneVal()
	if len(posArgs) > indentPosIdx {
		indentVal = posArgs[indentPosIdx]
	}
	if v, ok := kwArg(allArgs, kwnames, "indent"); ok {
		indentVal = v
	}
	if !indentVal.IsNone() {
		if indentVal.IsInt64() {
			n := int(indentVal.AsInt64())
			if n >= 0 {
				opts.hasIndent = true
				opts.indentStr = strings.Repeat(" ", n)
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
	if v, ok := kwArg(allArgs, kwnames, "separators"); ok {
		if v.IsTuple() && len(v.AsTuple().Items) == 2 {
			opts.itemSep = v.AsTuple().Items[0].AsString()
			opts.keySep = v.AsTuple().Items[1].AsString()
		} else if v.IsList() && len(v.AsList().Items) == 2 {
			opts.itemSep = v.AsList().Items[0].AsString()
			opts.keySep = v.AsList().Items[1].AsString()
		}
	}
	return opts
}

func jsonDumpsWithOpts(v val.Value, opts jsonDumpOpts) (string, error) {
	if !opts.hasIndent && !opts.sortKeys && opts.itemSep == ", " && opts.keySep == ": " {
		return jsonDumps(v, 0)
	}
	var sb strings.Builder
	sb.Grow(64)
	if err := writeJSONValueWithOpts(&sb, v, opts, 0); err != nil {
		return "", err
	}
	return sb.String(), nil
}

func writeJSONValueWithOpts(sb *strings.Builder, v val.Value, opts jsonDumpOpts, level int) error {
	if v.IsNone() || v.IsBool() || v.IsInt64() || v.IsFloat64() || v.IsAnyString() || v.IsBytes() || v.IsByteArray() || v.IsMemoryView() {
		return writeJSONValue(sb, v, 0, level)
	}
	writeLvlIndent := func(lvl int) {
		for range lvl {
			sb.WriteString(opts.indentStr)
		}
	}
	if v.IsList() || v.IsTuple() {
		var items []val.Value
		if v.IsList() {
			items = v.AsList().Items
		} else {
			items = v.AsTuple().Items
		}
		if len(items) == 0 {
			sb.WriteString("[]")
			return nil
		}
		sb.WriteString("[")
		if opts.hasIndent {
			sb.WriteString("\n")
		}
		for i, item := range items {
			if opts.hasIndent {
				writeLvlIndent(level + 1)
			}
			if err := writeJSONValueWithOpts(sb, item, opts, level+1); err != nil {
				return err
			}
			if i+1 < len(items) {
				sb.WriteString(opts.itemSep)
			}
			if opts.hasIndent {
				sb.WriteString("\n")
			}
		}
		if opts.hasIndent {
			writeLvlIndent(level)
		}
		sb.WriteString("]")
		return nil
	}
	if v.IsDict() {
		d := v.AsDict()
		var active []val.DictEntry
		for _, e := range d.ActiveEntries() {
			if e.Active {
				active = append(active, e)
			}
		}
		if opts.sortKeys {
			sort.Slice(active, func(i, j int) bool {
				return active[i].Key.AsString() < active[j].Key.AsString()
			})
		}
		if len(active) == 0 {
			sb.WriteString("{}")
			return nil
		}
		sb.WriteString("{")
		if opts.hasIndent {
			sb.WriteString("\n")
		}
		for i, e := range active {
			if opts.hasIndent {
				writeLvlIndent(level + 1)
			}
			writeJSONString(sb, e.Key.AsString())
			sb.WriteString(opts.keySep)
			if err := writeJSONValueWithOpts(sb, e.Value, opts, level+1); err != nil {
				return err
			}
			if i+1 < len(active) {
				sb.WriteString(opts.itemSep)
			}
			if opts.hasIndent {
				sb.WriteString("\n")
			}
		}
		if opts.hasIndent {
			writeLvlIndent(level)
		}
		sb.WriteString("}")
		return nil
	}
	return fmt.Errorf("TypeError: Object of type %s is not JSON serializable", v.Type().Name)
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

func writeIndent(sb *strings.Builder, indent, level int) {
	total := indent * level
	for range total {
		sb.WriteByte(' ')
	}
}

func writeJSONString(sb *strings.Builder, s string) {
	sb.WriteByte('"')
	for i := range len(s) {
		b := s[i]
		switch b {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		case '\b':
			sb.WriteString(`\b`)
		case '\f':
			sb.WriteString(`\f`)
		default:
			if b < 0x20 {
				fmt.Fprintf(sb, `\u%04x`, b)
			} else {
				sb.WriteByte(b)
			}
		}
	}
	sb.WriteByte('"')
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
				panic("TypeError: loads() takes at least 1 argument")
			}
		}
		var rawBytes []byte
		a := pa[0]
		switch {
		case a.IsAnyString():
			rawBytes = []byte(a.AsString())
		case a.IsBytes():
			rawBytes = a.AsBytes().Data
		case a.IsByteArray():
			rawBytes = a.AsByteArray().Data
		case a.IsMemoryView():
			rawBytes = a.AsMemoryView().Buf
		default:
			panic("TypeError: the JSON object must be str, bytes or bytearray, not " + a.Type().Name)
		}

		p := newJSONParser(rawBytes, e.internPool)
		res, err := p.parseValue()
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
				panic("TypeError: dumps() takes at least 1 argument")
			}
		}
		opts := extractJSONDumpOpts(pa, args, kwnames, 1)
		res, err := jsonDumpsWithOpts(pa[0], opts)
		if err != nil {
			panic(err.Error())
		}
		return val.NewStringVal(res)
	}))

	mod.Dict.Set(val.InternString("load"), val.NewBuiltinFuncVal("load", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			if v, ok := kwArg(args, kwnames, "fp"); ok {
				pa = []val.Value{v}
			} else {
				panic("TypeError: load() takes at least 1 argument")
			}
		}
		fp := pa[0]
		readMethod := val.GetAttrCtx(ctx, fp, val.InternString("read"))
		content := ctx.Invoke0(readMethod)
		var rawBytes []byte
		switch {
		case content.IsAnyString():
			rawBytes = []byte(content.AsString())
		case content.IsBytes():
			rawBytes = content.AsBytes().Data
		default:
			panic("TypeError: fp.read() must return str or bytes")
		}
		p := newJSONParser(rawBytes, e.internPool)
		res, err := p.parseValue()
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
			panic("TypeError: dump() takes at least 2 arguments")
		}
		opts := extractJSONDumpOpts(pa, args, kwnames, 2)
		resStr, err := jsonDumpsWithOpts(pa[0], opts)
		if err != nil {
			panic(err.Error())
		}
		fp := pa[1]
		writeMethod := val.GetAttrCtx(ctx, fp, val.InternString("write"))
		ctx.Invoke1(writeMethod, val.NewStringVal(resStr))
		return val.NoneVal()
	}))

	return modVal
}
