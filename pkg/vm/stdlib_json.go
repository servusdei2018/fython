package vm

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/servusdei2018/fython/pkg/val"
)

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

func (p *jsonParser) parseValue() (val.Value, error) {
	p.skipWhitespace()
	if p.pos >= len(p.src) {
		return val.NoneVal(), errors.New("json: unexpected end of JSON input")
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
		return val.NoneVal(), fmt.Errorf("json: invalid character %q looking for beginning of value", p.src[p.pos])
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
			return val.NoneVal(), errors.New("json: expected object key string")
		}

		keyVal, err := p.parseString(true)
		if err != nil {
			return val.NoneVal(), err
		}

		p.skipWhitespace()
		if p.pos >= len(p.src) || p.src[p.pos] != ':' {
			return val.NoneVal(), errors.New("json: expected ':' after key")
		}
		p.pos++ // consume ':'

		valElem, err := p.parseValue()
		if err != nil {
			return val.NoneVal(), err
		}

		dict.Set(keyVal, valElem)

		p.skipWhitespace()
		if p.pos >= len(p.src) {
			return val.NoneVal(), errors.New("json: unexpected end of JSON input in object")
		}

		if p.src[p.pos] == '}' {
			p.pos++
			return dictVal, nil
		}
		if p.src[p.pos] == ',' {
			p.pos++
			continue
		}
		return val.NoneVal(), fmt.Errorf("json: expected ',' or '}' in object, got %q", p.src[p.pos])
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
			return val.NoneVal(), errors.New("json: unexpected end of JSON input in array")
		}

		if p.src[p.pos] == ']' {
			p.pos++
			return val.NewListVal(items...), nil
		}
		if p.src[p.pos] == ',' {
			p.pos++
			continue
		}
		return val.NoneVal(), fmt.Errorf("json: expected ',' or ']' in array, got %q", p.src[p.pos])
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
			decoded, err := p.decodeEscapes(raw)
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
			return val.NoneVal(), errors.New("json: unescaped control character in string")
		}
		p.pos++
	}

	return val.NoneVal(), errors.New("json: unterminated string")
}

func (p *jsonParser) decodeEscapes(raw []byte) ([]byte, error) {
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
					return nil, errors.New("json: incomplete \\u escape")
				}
				r, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
				if err != nil {
					return nil, errors.New("json: invalid \\u escape")
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
	return val.NoneVal(), fmt.Errorf("json: invalid literal looking for %q", literal)
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
		sb.WriteString(strconv.FormatFloat(f, 'g', -1, 64))
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
		for _, e := range d.Entries {
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
			if indent > 0 {
				sb.WriteString(": ")
			} else {
				sb.WriteString(": ")
			}
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

func writeIndent(sb *strings.Builder, indent, level int) {
	total := indent * level
	for i := 0; i < total; i++ {
		sb.WriteByte(' ')
	}
}

func writeJSONString(sb *strings.Builder, s string) {
	sb.WriteByte('"')
	for i := 0; i < len(s); i++ {
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
				sb.WriteString(fmt.Sprintf(`\u%04x`, b))
			} else {
				sb.WriteByte(b)
			}
		}
	}
	sb.WriteByte('"')
}

// createJSONModule builds the native json PyModule Value.
func (e *Engine) createJSONModule() val.Value {
	modVal := val.NewModuleVal("json", "")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("loads"), val.NewBuiltinFuncVal("loads", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: loads() takes at least 1 argument")
		}
		var rawBytes []byte
		a := args[0]
		if a.IsAnyString() {
			rawBytes = []byte(a.AsString())
		} else if a.IsBytes() {
			rawBytes = a.AsBytes().Data
		} else if a.IsByteArray() {
			rawBytes = a.AsByteArray().Data
		} else if a.IsMemoryView() {
			rawBytes = a.AsMemoryView().Buf
		} else {
			panic("TypeError: the JSON object must be str, bytes or bytearray, not " + a.Type().Name)
		}

		p := newJSONParser(rawBytes, e.internPool)
		res, err := p.parseValue()
		if err != nil {
			panic("ValueError: " + err.Error())
		}
		return res
	}))

	mod.Dict.Set(val.InternString("dumps"), val.NewBuiltinFuncVal("dumps", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: dumps() takes at least 1 argument")
		}
		indent := 0
		if len(args) >= 2 && args[1].IsInt64() {
			indent = int(args[1].AsInt64())
		}
		res, err := jsonDumps(args[0], indent)
		if err != nil {
			panic(err.Error())
		}
		return val.NewStringVal(res)
	}))

	mod.Dict.Set(val.InternString("load"), val.NewBuiltinFuncVal("load", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: load() takes at least 1 argument")
		}
		fp := args[0]
		readMethod := val.GetAttrCtx(ctx, fp, val.InternString("read"))
		content := ctx.Invoke0(readMethod)
		var rawBytes []byte
		if content.IsAnyString() {
			rawBytes = []byte(content.AsString())
		} else if content.IsBytes() {
			rawBytes = content.AsBytes().Data
		} else {
			panic("TypeError: fp.read() must return str or bytes")
		}
		p := newJSONParser(rawBytes, e.internPool)
		res, err := p.parseValue()
		if err != nil {
			panic("ValueError: " + err.Error())
		}
		return res
	}))

	mod.Dict.Set(val.InternString("dump"), val.NewBuiltinFuncVal("dump", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: dump() takes at least 2 arguments")
		}
		indent := 0
		if len(args) >= 3 && args[2].IsInt64() {
			indent = int(args[2].AsInt64())
		}
		resStr, err := jsonDumps(args[0], indent)
		if err != nil {
			panic(err.Error())
		}
		fp := args[1]
		writeMethod := val.GetAttrCtx(ctx, fp, val.InternString("write"))
		ctx.Invoke1(writeMethod, val.NewStringVal(resStr))
		return val.NoneVal()
	}))

	return modVal
}
