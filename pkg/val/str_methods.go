package val

import (
	"bytes"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
	"unsafe"
)

// strMethodArg extracts the underlying Go string from a str or str-subclass instance.
func strMethodArg(v Value) (string, bool) {
	if v.IsAnyString() {
		return v.AsString(), true
	}
	if v.IsInstance() {
		inst := v.AsInstance()
		if len(inst.Slots) > 0 && inst.Slots[0].IsAnyString() {
			return inst.Slots[0].AsString(), true
		}
	}
	return "", false
}

func strSelf(name string, args []Value) string {
	if len(args) < 1 {
		panic("TypeError: descriptor '" + name + "' of 'str' object needs an argument")
	}
	s, ok := strMethodArg(args[0])
	if !ok {
		panic("TypeError: descriptor '" + name + "' requires a 'str' object but received a '" + args[0].Type().Name + "'")
	}
	return s
}

// splitStrKw separates positional args from keyword args (values trail args).
func splitStrKw(args []Value, kwnames []Value) ([]Value, map[string]Value) {
	if len(kwnames) == 0 {
		return args, nil
	}
	n := len(args) - len(kwnames)
	kw := make(map[string]Value, len(kwnames))
	for i, k := range kwnames {
		kw[k.AsString()] = args[n+i]
	}
	return args[:n], kw
}

func isPrimitiveReprKind(v Value) bool {
	switch v.Kind() {
	case KindNone, KindBool, KindInt64, KindFloat64, KindInlineStr, KindString:
		return true
	case KindList:
		return v.Type() == ListType
	case KindTuple:
		return v.Type() == TupleType
	case KindDict:
		return v.Type() == DictType
	case KindSet:
		return v.Type() == SetType || v.Type() == FrozenSetType
	}
	return false
}

// PyStrRepr returns the Python repr of a Go string.
func PyStrRepr(s string) string {
	quote := byte('\'')
	if strings.IndexByte(s, '\'') >= 0 && strings.IndexByte(s, '"') < 0 {
		quote = '"'
	}
	var sb strings.Builder
	sb.Grow(len(s) + 2)
	sb.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == rune(quote):
			sb.WriteByte('\\')
			sb.WriteRune(r)
		case r == '\\':
			sb.WriteString("\\\\")
		case r == '\n':
			sb.WriteString("\\n")
		case r == '\r':
			sb.WriteString("\\r")
		case r == '\t':
			sb.WriteString("\\t")
		case r < 0x20 || r == 0x7f:
			sb.WriteString("\\x")
			h := strconv.FormatInt(int64(r), 16)
			if len(h) < 2 {
				sb.WriteByte('0')
			}
			sb.WriteString(h)
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteByte(quote)
	return sb.String()
}

// PyRepr computes repr(v), dispatching to user-defined __repr__ where present.
func PyRepr(ctx *ExecContext, v Value) string {
	if ctx != nil && !isPrimitiveReprKind(v) {
		if fn, owner, ok := v.Type().LookupMRO(strRepr); ok && owner != ObjectType {
			var res Value
			if descrGet := fn.Type().Slots.TpDescrGet; descrGet != nil {
				bound := descrGet(ctx, fn, v, v.Type())
				res = ctx.Invoke0(bound)
			} else {
				res = ctx.Invoke1(fn, v)
			}
			if s, ok := strMethodArg(res); ok {
				return s
			}
			return res.String()
		}
	}
	return NativeRepr(ctx, v)
}

// NativeRepr formats v without consulting its own type's __repr__. Builtin
// types install it as their __repr__ so the method never calls itself.
// Container elements still dispatch through PyRepr.
func NativeRepr(ctx *ExecContext, v Value) string {
	var seenBuf [8]*ObjectHeader
	return nativeReprRec(ctx, v, seenBuf[:0])
}

func reprSeenContains(seen []*ObjectHeader, ptr *ObjectHeader) bool {
	return slices.Contains(seen, ptr)
}

func reprElem(ctx *ExecContext, v Value, seen []*ObjectHeader) string {
	if ctx != nil && !isPrimitiveReprKind(v) {
		if fn, owner, ok := v.Type().LookupMRO(strRepr); ok && owner != ObjectType {
			if v.ptr != nil && (v.ptr.Kind == KindDict || v.ptr.Kind == KindList || v.ptr.Kind == KindDictView) {
				if v.Type() == DictType || v.Type() == ListType || v.Type() == DictKeysType || v.Type() == DictValuesType || v.Type() == DictItemsType {
					return nativeReprRec(ctx, v, seen)
				}
			}
			var res Value
			if descrGet := fn.Type().Slots.TpDescrGet; descrGet != nil {
				bound := descrGet(ctx, fn, v, v.Type())
				res = ctx.Invoke0(bound)
			} else {
				res = ctx.Invoke1(fn, v)
			}
			if s, ok := strMethodArg(res); ok {
				return s
			}
			return res.String()
		}
	}
	return nativeReprRec(ctx, v, seen)
}

func nativeReprRec(ctx *ExecContext, v Value, seen []*ObjectHeader) string {
	if v.Kind() == KindMappingProxy {
		return "mappingproxy(" + reprElem(ctx, v.AsMappingProxy().Mapping, seen) + ")"
	}
	switch v.Kind() {
	case KindComplex:
		c := v.AsComplex()
		return ComplexRepr(c.Real, c.Imag)
	case KindInlineStr, KindString:
		return PyStrRepr(v.AsString())
	case KindList, KindTuple:
		var items []Value
		open, close := "[", "]"
		if v.Kind() == KindList {
			if reprSeenContains(seen, v.ptr) {
				return "[...]"
			}
			seen = append(seen, v.ptr)
			items = v.AsList().Items
		} else {
			items = v.AsTuple().Items
			open, close = "(", ")"
		}
		var sb strings.Builder
		sb.WriteString(open)
		for i, it := range items {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(reprElem(ctx, it, seen))
		}
		if v.Kind() == KindTuple && len(items) == 1 {
			sb.WriteByte(',')
		}
		sb.WriteString(close)
		return sb.String()
	case KindDict:
		if reprSeenContains(seen, v.ptr) {
			return "{...}"
		}
		seen = append(seen, v.ptr)
		var sb strings.Builder
		sb.WriteByte('{')
		first := true
		for _, e := range v.AsDict().ActiveEntries() {
			if e.Key.IsUnbound() {
				continue
			}
			if !first {
				sb.WriteString(", ")
			}
			first = false
			sb.WriteString(reprElem(ctx, e.Key, seen))
			sb.WriteString(": ")
			sb.WriteString(reprElem(ctx, e.Value, seen))
		}
		sb.WriteByte('}')
		return sb.String()
	case KindDictView:
		dv := v.AsDictView()
		if reprSeenContains(seen, v.ptr) {
			return "..."
		}
		seen = append(seen, v.ptr)
		var sb strings.Builder
		switch dv.ViewKind {
		case DictViewKeys:
			sb.WriteString("dict_keys([")
		case DictViewValues:
			sb.WriteString("dict_values([")
		default:
			sb.WriteString("dict_items([")
		}
		first := true
		for _, e := range dv.Dict.ActiveEntries() {
			if e.Key.IsUnbound() {
				continue
			}
			if !first {
				sb.WriteString(", ")
			}
			first = false
			switch dv.ViewKind {
			case DictViewKeys:
				sb.WriteString(reprElem(ctx, e.Key, seen))
			case DictViewValues:
				sb.WriteString(reprElem(ctx, e.Value, seen))
			default:
				sb.WriteByte('(')
				sb.WriteString(reprElem(ctx, e.Key, seen))
				sb.WriteString(", ")
				sb.WriteString(reprElem(ctx, e.Value, seen))
				sb.WriteByte(')')
			}
		}
		sb.WriteString("])")
		return sb.String()
	case KindSet:
		s := v.AsSet()
		tp := v.Type()
		isBaseSet := tp == nil || tp == SetType
		if s == nil || s.Len() == 0 {
			if isBaseSet {
				return "set()"
			}
			return tp.Name + "()"
		}
		var sb strings.Builder
		if !isBaseSet {
			sb.WriteString(tp.Name)
			sb.WriteByte('(')
		}
		sb.WriteByte('{')
		first := true
		for _, item := range s.Items() {
			if !first {
				sb.WriteString(", ")
			}
			first = false
			sb.WriteString(reprElem(ctx, item, seen))
		}
		sb.WriteByte('}')
		if !isBaseSet {
			sb.WriteByte(')')
		}
		return sb.String()
	}
	return v.String()
}

// InstallNativeRepr gives t its own __repr__ method. Without one the type
// inherits object.__repr__, and code that keys a table on type.__repr__
// (pprint does) sees every builtin type as the same entry.
func InstallNativeRepr(t *PyType) {
	t.Dict.Set(strRepr, NewBuiltinFuncVal(t.Name+".__repr__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) != 1 {
			ctx.Raise(NewException(TypeErrorType, "expected 0 arguments, got"+strconv.Itoa(len(args)-1)))
			return NoneVal()
		}
		return NewStringVal(NativeRepr(ctx, args[0]))
	}))
	t.InvalidateVersion()
}

// PyStr computes str(v), dispatching to user-defined __str__/__repr__ where present.
func PyStr(ctx *ExecContext, v Value) string {
	if v.IsAnyString() {
		return v.AsString()
	}
	if isPrimitiveReprKind(v) {
		switch v.Kind() {
		case KindList, KindTuple, KindDict, KindSet:
			return PyRepr(ctx, v)
		}
		return v.String()
	}
	if ctx != nil {
		if fn, owner, ok := v.Type().LookupMRO(strStr); ok && owner != ObjectType {
			var res Value
			if descrGet := fn.Type().Slots.TpDescrGet; descrGet != nil {
				bound := descrGet(ctx, fn, v, v.Type())
				res = ctx.Invoke0(bound)
			} else {
				res = ctx.Invoke1(fn, v)
			}
			if s, ok := strMethodArg(res); ok {
				return s
			}
			return res.String()
		}
		return PyRepr(ctx, v)
	}
	return v.String()
}

// PyFormat computes format(v, spec), dispatching to user-defined __format__ where present.
func PyFormat(ctx *ExecContext, v Value, spec string) string {
	if ctx != nil && !isPrimitiveReprKind(v) {
		if fn, owner, ok := v.Type().LookupMRO(strFormat); ok && owner != ObjectType && owner != StrType {
			res := ctx.Invoke2(fn, v, NewStringVal(spec))
			if s, ok := strMethodArg(res); ok {
				return s
			}
			return res.String()
		}
		if s, ok := strMethodArg(v); ok {
			return FormatValueCtx(ctx, NewStringVal(s), spec, 0).AsString()
		}
		if spec != "" {
			ctx.Raise(NewException(TypeErrorType, "unsupported format string passed to "+v.Type().Name+".__format__"))
			return ""
		}
		return PyStr(ctx, v)
	}
	return FormatValueCtx(ctx, v, spec, 0).AsString()
}

// parseASCIIDecimal parses a non-negative ASCII decimal integer without
// allocating a strconv.NumError on non-numeric strings.
func parseASCIIDecimal(s string) (int, bool) {
	if len(s) == 0 {
		return 0, false
	}
	const maxDiv10 = int(^uint(0)>>1) / 10
	const maxMod10 = int(^uint(0)>>1) % 10
	n := 0
	for i := range len(s) {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		d := int(c - '0')
		if n > maxDiv10 || (n == maxDiv10 && d > maxMod10) {
			return 0, false
		}
		n = n*10 + d
	}
	return n, true
}

// strFormatAppend implements str.format / str.format_map into dst.
func strFormatAppend(ctx *ExecContext, dst []byte, format string, pos []Value, kw func(string) (Value, bool), depth int) ([]byte, bool) {
	if depth > 2 {
		ctx.Raise(NewException(ValueErrorType, "Max string recursion exceeded"))
		return dst, false
	}
	autoIdx := 0
	n := len(format)
	for i := 0; i < n; {
		litStart := i
		for i < n && format[i] != '{' && format[i] != '}' {
			i++
		}
		if i > litStart {
			dst = append(dst, format[litStart:i]...)
		}
		if i >= n {
			break
		}
		c := format[i]
		if c == '}' {
			if i+1 < n && format[i+1] == '}' {
				dst = append(dst, '}')
				i += 2
				continue
			}
			ctx.Raise(NewException(ValueErrorType, "Single '}' encountered in format string"))
			return dst, false
		}
		if i+1 < n && format[i+1] == '{' {
			dst = append(dst, '{')
			i += 2
			continue
		}
		// Parse replacement field up to matching '}'.
		j := i + 1
		nest := 1
		for j < n {
			if format[j] == '{' {
				nest++
			} else if format[j] == '}' {
				nest--
				if nest == 0 {
					break
				}
			}
			j++
		}
		if j >= n {
			ctx.Raise(NewException(ValueErrorType, "expected '}' before end of string"))
			return dst, false
		}
		field := format[i+1 : j]
		i = j + 1

		// Split field_name[!conv][:spec]
		fieldName, conv, spec := field, byte(0), ""
		bracket := 0
	loopSplit:
		for k := range len(field) {
			ch := field[k]
			switch {
			case ch == '[':
				bracket++
			case ch == ']':
				bracket--
			case bracket == 0 && (ch == '!' || ch == ':'):
				fieldName = field[:k]
				rest := field[k:]
				if ch == '!' {
					if len(rest) < 2 {
						ctx.Raise(NewException(ValueErrorType, "end of string while looking for conversion specifier"))
						return dst, false
					}
					conv = rest[1]
					rest = rest[2:]
					if len(rest) > 0 {
						if rest[0] != ':' {
							ctx.Raise(NewException(ValueErrorType, "expected ':' after conversion specifier"))
							return dst, false
						}
						spec = rest[1:]
					}
				} else {
					spec = rest[1:]
				}
				break loopSplit
			}
		}
		if strings.IndexByte(spec, '{') >= 0 {
			spec = strFormatImpl(ctx, spec, pos, kw, depth+1)
		}

		// Resolve the first component of field name.
		first := fieldName
		restIdx := len(fieldName)
		for k := range len(fieldName) {
			if fieldName[k] == '.' || fieldName[k] == '[' {
				first = fieldName[:k]
				restIdx = k
				break
			}
		}
		var obj Value
		if first == "" {
			if autoIdx < 0 {
				ctx.Raise(NewException(ValueErrorType, "cannot switch from manual field specification to automatic field numbering"))
				return dst, false
			}
			if autoIdx >= len(pos) {
				ctx.Raise(NewException(IndexErrorType, "Replacement index"+strconv.Itoa(autoIdx)+" out of range for positional args tuple"))
				return dst, false
			}
			obj = pos[autoIdx]
			autoIdx++
		} else if idx, ok := parseASCIIDecimal(first); ok {
			if autoIdx > 0 {
				ctx.Raise(NewException(ValueErrorType, "cannot switch from automatic field numbering to manual field specification"))
				return dst, false
			}
			autoIdx = -1
			if idx >= len(pos) {
				ctx.Raise(NewException(IndexErrorType, "Replacement index"+first+" out of range for positional args tuple"))
				return dst, false
			}
			obj = pos[idx]
		} else {
			v, ok := kw(first)
			if !ok {
				ctx.Raise(NewException(KeyErrorType, PyStrRepr(first)))
				return dst, false
			}
			obj = v
		}
		// Attribute / index chain.
		rest := fieldName[restIdx:]
		for len(rest) > 0 {
			switch rest[0] {
			case '.':
				k := 1
				for k < len(rest) && rest[k] != '.' && rest[k] != '[' {
					k++
				}
				obj = GetAttrCtx(ctx, obj, InternString(rest[1:k]))
				rest = rest[k:]
			case '[':
				k := strings.IndexByte(rest, ']')
				if k < 0 {
					ctx.Raise(NewException(ValueErrorType, "Missing ']' in format string"))
					return dst, false
				}
				key := rest[1:k]
				var keyVal Value
				if ki, ok := parseASCIIDecimal(key); ok {
					keyVal = Int64Val(int64(ki))
				} else {
					keyVal = NewStringVal(key)
				}
				obj = GetItemCtx(ctx, obj, keyVal)
				rest = rest[k+1:]
			default:
				ctx.Raise(NewException(ValueErrorType, "Only '.' or '[' may follow ']' in format field specifier"))
				return dst, false
			}
		}
		switch conv {
		case 0:
		case 'r':
			obj = NewStringVal(PyRepr(ctx, obj))
		case 's':
			obj = NewStringVal(PyStr(ctx, obj))
		case 'a':
			obj = NewStringVal(asciiEscape(PyRepr(ctx, obj)))
		default:
			ctx.Raise(NewException(ValueErrorType, "Unknown conversion specifier"+string(conv)))
			return dst, false
		}
		if spec == "" {
			switch {
			case obj.IsInlineStr():
				dst, _ = AppendValueBytes(dst, obj)
				continue
			case obj.IsString() && obj.Type() == StrType:
				dst = append(dst, obj.AsStringObj().Data...)
				continue
			case obj.IsInt64():
				dst = strconv.AppendInt(dst, obj.AsInt64(), 10)
				continue
			case obj.IsBool():
				if obj.AsBool() {
					dst = append(dst, "True"...)
				} else {
					dst = append(dst, "False"...)
				}
				continue
			case obj.IsFloat64():
				dst = AppendFloatRepr(dst, obj.AsFloat64())
				continue
			case obj.IsNone():
				dst = append(dst, "None"...)
				continue
			}
		}
		dst = append(dst, PyFormat(ctx, obj, spec)...)
	}
	return dst, true
}

// strFormatImpl implements str.format / str.format_map returning a Go string.
func strFormatImpl(ctx *ExecContext, format string, pos []Value, kw func(string) (Value, bool), depth int) string {
	var stackBuf [128]byte
	var buf []byte
	if len(format)+32 <= len(stackBuf) {
		buf = stackBuf[:0]
	} else {
		buf = make([]byte, 0, len(format)+32)
	}
	buf, ok := strFormatAppend(ctx, buf, format, pos, kw, depth)
	if !ok {
		return ""
	}
	return string(buf)
}

func strFormatValueImpl(ctx *ExecContext, format string, pos []Value, kw func(string) (Value, bool)) Value {
	var stackBuf [128]byte
	var buf []byte
	if len(format)+32 <= len(stackBuf) {
		buf = stackBuf[:0]
	} else {
		buf = make([]byte, 0, len(format)+32)
	}
	buf, ok := strFormatAppend(ctx, buf, format, pos, kw, 0)
	if !ok {
		return SmallStringVal("")
	}
	return NewStringFromBytes(buf)
}

// PyASCII computes ascii(v).
func PyASCII(ctx *ExecContext, v Value) string {
	return asciiEscape(PyRepr(ctx, v))
}

func asciiEscape(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r < 128:
			sb.WriteRune(r)
		case r <= 0xff:
			sb.WriteString("\\x")
			h := strconv.FormatInt(int64(r), 16)
			if len(h) < 2 {
				sb.WriteByte('0')
			}
			sb.WriteString(h)
		case r <= 0xffff:
			h := strconv.FormatInt(int64(r), 16)
			sb.WriteString("\\u")
			sb.WriteString(strings.Repeat("0", 4-len(h)))
			sb.WriteString(h)
		default:
			h := strconv.FormatInt(int64(r), 16)
			sb.WriteString("\\U")
			sb.WriteString(strings.Repeat("0", 8-len(h)))
			sb.WriteString(h)
		}
	}
	return sb.String()
}

func isPyIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return false
			}
		} else if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.Is(unicode.Mn, r) && !unicode.Is(unicode.Mc, r) && !unicode.Is(unicode.Nd, r) && !unicode.Is(unicode.Pc, r) {
			return false
		}
	}
	return true
}

func strPredicate(name string, empty bool, pred func(rune) bool) Value {
	return NewBuiltinFuncVal(name, func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf(name, args)
		if s == "" {
			return BoolVal(empty)
		}
		for _, r := range s {
			if !pred(r) {
				return BoolVal(false)
			}
		}
		return BoolVal(true)
	})
}

func strIntArg(v Value, def int) int {
	if v.IsNone() {
		return def
	}
	if v.IsInt64() {
		return int(v.AsInt64())
	}
	if v.IsBool() {
		if v.AsBool() {
			return 1
		}
		return 0
	}
	panic("TypeError: slice indices must be integers or None or have an __index__ method")
}

// strSliceBounds normalizes optional start/end args (rune indices) into a substring slice and start rune offset.
func strSliceBounds(s string, args []Value) (string, int) {
	if isASCII(s) {
		ln := len(s)
		start, end := 0, ln
		if len(args) > 0 {
			start = strIntArg(args[0], 0)
		}
		if len(args) > 1 {
			end = strIntArg(args[1], ln)
		}
		if start < 0 {
			start += ln
			if start < 0 {
				start = 0
			}
		}
		if end < 0 {
			end += ln
			if end < 0 {
				end = 0
			}
		}
		if end > ln {
			end = ln
		}
		if start > end {
			return "", -1
		}
		return s[start:end], start
	}
	ln := utf8.RuneCountInString(s)
	start, end := 0, ln
	if len(args) > 0 {
		start = strIntArg(args[0], 0)
	}
	if len(args) > 1 {
		end = strIntArg(args[1], ln)
	}
	if start < 0 {
		start += ln
		if start < 0 {
			start = 0
		}
	}
	if end < 0 {
		end += ln
		if end < 0 {
			end = 0
		}
	}
	if end > ln {
		end = ln
	}
	if start > end {
		return "", -1
	}
	byteStart, byteEnd := 0, len(s)
	curRune := 0
	for i := 0; i < len(s); {
		if curRune == start {
			byteStart = i
		}
		if curRune == end {
			byteEnd = i
			break
		}
		_, sz := utf8.DecodeRuneInString(s[i:])
		i += sz
		curRune++
	}
	if start == ln {
		byteStart = len(s)
	}
	return s[byteStart:byteEnd], start
}

func strFindImpl(s, sub string, extra []Value, last bool) int {
	ascii := isASCII(s)
	if ascii {
		if len(extra) == 0 {
			if last {
				return strings.LastIndex(s, sub)
			}
			return strings.Index(s, sub)
		}
		ln := len(s)
		start, end := 0, ln
		if len(extra) > 0 {
			start = strIntArg(extra[0], 0)
		}
		if len(extra) > 1 {
			end = strIntArg(extra[1], ln)
		}
		if start < 0 {
			start += ln
			if start < 0 {
				start = 0
			}
		}
		if end < 0 {
			end += ln
			if end < 0 {
				end = 0
			}
		}
		if end > ln {
			end = ln
		}
		if start > end {
			return -1
		}
		var idx int
		if last {
			idx = strings.LastIndex(s[start:end], sub)
		} else {
			idx = strings.Index(s[start:end], sub)
		}
		if idx < 0 {
			return -1
		}
		return start + idx
	}
	seg, off := strSliceBounds(s, extra)
	if off < 0 {
		return -1
	}
	var idx int
	if last {
		idx = strings.LastIndex(seg, sub)
	} else {
		idx = strings.Index(seg, sub)
	}
	if idx < 0 {
		return -1
	}
	return off + utf8.RuneCountInString(seg[:idx])
}

func isASCII(s string) bool {
	return IsASCII(s)
}

func strPad(s string, width int, fill rune, align byte) string {
	ln := utf8.RuneCountInString(s)
	if width <= ln {
		return s
	}
	pad := width - ln
	f := string(fill)
	switch align {
	case '<':
		return s + strings.Repeat(f, pad)
	case '>':
		return strings.Repeat(f, pad) + s
	default:
		left := pad / 2
		if pad%2 == 1 && width%2 == 1 {
			left++
		}
		return strings.Repeat(f, left) + s + strings.Repeat(f, pad-left)
	}
}

func strFillArg(args []Value, idx int) rune {
	if len(args) > idx {
		fs, ok := strMethodArg(args[idx])
		if !ok || utf8.RuneCountInString(fs) != 1 {
			panic("TypeError: The fill character must be exactly one character long")
		}
		r, _ := utf8.DecodeRuneInString(fs)
		return r
	}
	return ' '
}

func strSplitWhitespace(s string, maxsplit int, fromRight bool) []string {
	if maxsplit < 0 {
		return strings.Fields(s)
	}
	var parts []string
	if !fromRight {
		rest := strings.TrimLeftFunc(s, unicode.IsSpace)
		for rest != "" {
			if len(parts) == maxsplit {
				parts = append(parts, rest)
				return parts
			}
			idx := strings.IndexFunc(rest, unicode.IsSpace)
			if idx < 0 {
				parts = append(parts, rest)
				return parts
			}
			parts = append(parts, rest[:idx])
			rest = strings.TrimLeftFunc(rest[idx:], unicode.IsSpace)
		}
		return parts
	}
	rest := strings.TrimRightFunc(s, unicode.IsSpace)
	for rest != "" {
		if len(parts) == maxsplit {
			parts = append(parts, rest)
			break
		}
		idx := strings.LastIndexFunc(rest, unicode.IsSpace)
		if idx < 0 {
			parts = append(parts, rest)
			break
		}
		_, sz := utf8.DecodeRuneInString(rest[idx:])
		parts = append(parts, rest[idx+sz:])
		rest = strings.TrimRightFunc(rest[:idx], unicode.IsSpace)
	}
	for l, r := 0, len(parts)-1; l < r; l, r = l+1, r-1 {
		parts[l], parts[r] = parts[r], parts[l]
	}
	return parts
}

func strSplitASCIIWhitespaceDirect(s string, maxsplit int) (Value, bool) {
	n := len(s)
	// Fast-scan to verify ASCII and count tokens in one pass.
	wordCount := 0
	inWord := false
	for i := range n {
		c := s[i]
		if c >= 0x80 {
			return Value{}, false
		}
		isSpace := c == ' ' || (c >= '\t' && c <= '\r')
		if !isSpace {
			if !inWord {
				wordCount++
				inWord = true
			}
		} else {
			inWord = false
		}
	}
	if maxsplit >= 0 && wordCount > maxsplit+1 {
		wordCount = maxsplit + 1
	}
	items := make([]Value, 0, wordCount)
	i := 0
	for i < n {
		for i < n && (s[i] == ' ' || (s[i] >= '\t' && s[i] <= '\r')) {
			i++
		}
		if i >= n {
			break
		}
		if maxsplit >= 0 && len(items) == maxsplit {
			sub := s[i:]
			if len(sub) <= 7 {
				items = append(items, SmallStringVal(sub))
			} else {
				obj := &PyString{Header: ObjectHeader{Kind: KindString, Type: StrType}, Data: sub}
				items = append(items, Value{ptr: &obj.Header})
			}
			break
		}
		start := i
		for i < n && s[i] != ' ' && (s[i] < '\t' || s[i] > '\r') {
			i++
		}
		sub := s[start:i]
		if len(sub) <= 7 {
			items = append(items, SmallStringVal(sub))
		} else {
			obj := &PyString{Header: ObjectHeader{Kind: KindString, Type: StrType}, Data: sub}
			items = append(items, Value{ptr: &obj.Header})
		}
	}
	lst := &PyList{Header: ObjectHeader{Kind: KindList, Type: ListType}, Items: items}
	return Value{ptr: &lst.Header}, true
}

func strSplitUnicodeWhitespaceDirect(s string, maxsplit int, fromRight bool) Value {
	if fromRight && maxsplit >= 0 {
		return stringsToList(strSplitWhitespace(s, maxsplit, true))
	}
	n := len(s)
	est := 8
	if maxsplit >= 0 && maxsplit+1 < est {
		est = maxsplit + 1
	}
	items := make([]Value, 0, est)
	i := 0
	for i < n {
		// Skip leading whitespace.
		for i < n {
			b := s[i]
			if b < 0x80 {
				if b == ' ' || (b >= '\t' && b <= '\r') {
					i++
					continue
				}
				break
			}
			r, sz := utf8.DecodeRuneInString(s[i:])
			if !unicode.IsSpace(r) {
				break
			}
			i += sz
		}
		if i >= n {
			break
		}
		if maxsplit >= 0 && len(items) == maxsplit {
			items = append(items, NewStringVal(s[i:]))
			break
		}
		start := i
		for i < n {
			b := s[i]
			if b < 0x80 {
				if b == ' ' || (b >= '\t' && b <= '\r') {
					break
				}
				i++
				continue
			}
			r, sz := utf8.DecodeRuneInString(s[i:])
			if unicode.IsSpace(r) {
				break
			}
			i += sz
		}
		items = append(items, NewStringVal(s[start:i]))
	}
	return NewListFromOwnedSlice(items)
}

func strRsplitSepDirect(s, sep string, maxsplit int) Value {
	if maxsplit == 0 {
		items := make([]Value, 1)
		items[0] = NewStringVal(s)
		return NewListFromOwnedSlice(items)
	}
	if maxsplit < 0 {
		return strSplitSepDirect(s, sep, -1)
	}
	items := make([]Value, 0, min(maxsplit+1, 16))
	rest := s
	sepLen := len(sep)
	if sepLen == 1 {
		b := sep[0]
		for len(items) < maxsplit {
			idx := strings.LastIndexByte(rest, b)
			if idx < 0 {
				break
			}
			items = append(items, NewStringVal(rest[idx+1:]))
			rest = rest[:idx]
		}
	} else {
		for len(items) < maxsplit {
			idx := strings.LastIndex(rest, sep)
			if idx < 0 {
				break
			}
			items = append(items, NewStringVal(rest[idx+sepLen:]))
			rest = rest[:idx]
		}
	}
	items = append(items, NewStringVal(rest))
	for l, r := 0, len(items)-1; l < r; l, r = l+1, r-1 {
		items[l], items[r] = items[r], items[l]
	}
	return NewListFromOwnedSlice(items)
}

func strSplitSepDirect(s, sep string, maxsplit int) Value {
	if maxsplit == 0 {
		items := make([]Value, 1)
		items[0] = NewStringVal(s)
		lst := &PyList{Header: ObjectHeader{Kind: KindList, Type: ListType}, Items: items}
		return Value{ptr: &lst.Header}
	}
	var count int
	if maxsplit < 0 {
		count = strings.Count(s, sep) + 1
	} else {
		count = min(maxsplit+1, strings.Count(s, sep)+1)
	}
	items := make([]Value, 0, count)
	if len(sep) == 1 {
		b := sep[0]
		rest := s
		for maxsplit < 0 || len(items) < maxsplit {
			idx := strings.IndexByte(rest, b)
			if idx < 0 {
				break
			}
			sub := rest[:idx]
			if len(sub) <= 7 && isASCII(sub) {
				items = append(items, SmallStringVal(sub))
			} else {
				obj := &PyString{Header: ObjectHeader{Kind: KindString, Type: StrType}, Data: sub}
				items = append(items, Value{ptr: &obj.Header})
			}
			rest = rest[idx+1:]
		}
		if len(rest) <= 7 && isASCII(rest) {
			items = append(items, SmallStringVal(rest))
		} else {
			obj := &PyString{Header: ObjectHeader{Kind: KindString, Type: StrType}, Data: rest}
			items = append(items, Value{ptr: &obj.Header})
		}
	} else {
		sepLen := len(sep)
		rest := s
		for maxsplit < 0 || len(items) < maxsplit {
			idx := strings.Index(rest, sep)
			if idx < 0 {
				break
			}
			sub := rest[:idx]
			if len(sub) <= 7 && isASCII(sub) {
				items = append(items, SmallStringVal(sub))
			} else {
				obj := &PyString{Header: ObjectHeader{Kind: KindString, Type: StrType}, Data: sub}
				items = append(items, Value{ptr: &obj.Header})
			}
			rest = rest[idx+sepLen:]
		}
		if len(rest) <= 7 && isASCII(rest) {
			items = append(items, SmallStringVal(rest))
		} else {
			obj := &PyString{Header: ObjectHeader{Kind: KindString, Type: StrType}, Data: rest}
			items = append(items, Value{ptr: &obj.Header})
		}
	}
	lst := &PyList{Header: ObjectHeader{Kind: KindList, Type: ListType}, Items: items}
	return Value{ptr: &lst.Header}
}

func stringsToList(parts []string) Value {
	items := make([]Value, len(parts))
	for i, p := range parts {
		items[i] = NewStringVal(p)
	}
	return NewListVal(items...)
}

func initStrExtraMethods() {
	set := func(name string, fn func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value) {
		StrType.Dict.Set(InternString(name), NewBuiltinFuncVal(name, fn))
	}

	// Predicates.
	StrType.Dict.Set(InternString("isdigit"), strPredicate("isdigit", false, unicode.IsDigit))
	StrType.Dict.Set(InternString("isdecimal"), strPredicate("isdecimal", false, func(r rune) bool { return unicode.Is(unicode.Nd, r) }))
	StrType.Dict.Set(InternString("isnumeric"), strPredicate("isnumeric", false, unicode.IsNumber))
	StrType.Dict.Set(InternString("isalpha"), strPredicate("isalpha", false, unicode.IsLetter))
	StrType.Dict.Set(InternString("isalnum"), strPredicate("isalnum", false, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }))
	StrType.Dict.Set(InternString("isspace"), strPredicate("isspace", false, unicode.IsSpace))
	set("isascii", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		return BoolVal(isASCII(strSelf("isascii", args)))
	})
	StrType.Dict.Set(InternString("isprintable"), strPredicate("isprintable", true, func(r rune) bool { return r == ' ' || (unicode.IsPrint(r)) }))

	set("isidentifier", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		return BoolVal(isPyIdentifier(strSelf("isidentifier", args)))
	})
	set("isupper", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("isupper", args)
		cased := false
		for _, r := range s {
			if unicode.IsLower(r) || unicode.IsTitle(r) {
				return BoolVal(false)
			}
			if unicode.IsUpper(r) {
				cased = true
			}
		}
		return BoolVal(cased)
	})
	set("islower", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("islower", args)
		cased := false
		for _, r := range s {
			if unicode.IsUpper(r) || unicode.IsTitle(r) {
				return BoolVal(false)
			}
			if unicode.IsLower(r) {
				cased = true
			}
		}
		return BoolVal(cased)
	})
	set("istitle", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("istitle", args)
		cased, prevCased := false, false
		for _, r := range s {
			switch {
			case unicode.IsUpper(r) || unicode.IsTitle(r):
				if prevCased {
					return BoolVal(false)
				}
				prevCased, cased = true, true
			case unicode.IsLower(r):
				if !prevCased {
					return BoolVal(false)
				}
				prevCased, cased = true, true
			default:
				prevCased = false
			}
		}
		return BoolVal(cased)
	})

	// Case transforms.
	set("title", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("title", args)
		var sb strings.Builder
		prevCased := false
		for _, r := range s {
			if prevCased {
				sb.WriteRune(unicode.ToLower(r))
			} else {
				sb.WriteRune(unicode.ToTitle(r))
			}
			prevCased = unicode.IsLetter(r)
		}
		return NewStringVal(sb.String())
	})
	set("capitalize", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("capitalize", args)
		if s == "" {
			return NewStringVal("")
		}
		r, sz := utf8.DecodeRuneInString(s)
		return NewStringVal(string(unicode.ToTitle(r)) + strings.ToLower(s[sz:]))
	})
	set("swapcase", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("swapcase", args)
		return NewStringVal(strings.Map(func(r rune) rune {
			if unicode.IsUpper(r) {
				return unicode.ToLower(r)
			}
			if unicode.IsLower(r) {
				return unicode.ToUpper(r)
			}
			return r
		}, s))
	})
	set("casefold", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("casefold", args)
		return NewStringVal(strings.ReplaceAll(strings.ToLower(s), "ß", "ss"))
	})

	// Padding / alignment.
	set("center", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("center", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "center expected at least 1 argument, got 0"))
			return NoneVal()
		}
		return NewStringVal(strPad(s, strIntArg(args[1], 0), strFillArg(args, 2), '^'))
	})
	set("ljust", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("ljust", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "ljust expected at least 1 argument, got 0"))
			return NoneVal()
		}
		return NewStringVal(strPad(s, strIntArg(args[1], 0), strFillArg(args, 2), '<'))
	})
	set("rjust", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("rjust", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "rjust expected at least 1 argument, got 0"))
			return NoneVal()
		}
		return NewStringVal(strPad(s, strIntArg(args[1], 0), strFillArg(args, 2), '>'))
	})
	set("zfill", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("zfill", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "zfill() takes exactly one argument (0 given)"))
			return NoneVal()
		}
		width := strIntArg(args[1], 0)
		ln := utf8.RuneCountInString(s)
		if width <= ln {
			return NewStringVal(s)
		}
		pad := strings.Repeat("0", width-ln)
		if s != "" && (s[0] == '+' || s[0] == '-') {
			return NewStringVal(s[:1] + pad + s[1:])
		}
		return NewStringVal(pad + s)
	})
	set("expandtabs", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		pargs, kw := splitStrKw(args, kwnames)
		s := strSelf("expandtabs", pargs)
		tab := 8
		if len(pargs) > 1 {
			tab = strIntArg(pargs[1], 8)
		} else if v, ok := kw["tabsize"]; ok {
			tab = strIntArg(v, 8)
		}
		var sb strings.Builder
		col := 0
		for _, r := range s {
			switch r {
			case '\t':
				if tab > 0 {
					n := tab - col%tab
					sb.WriteString(strings.Repeat(" ", n))
					col += n
				}
			case '\n', '\r':
				sb.WriteRune(r)
				col = 0
			default:
				sb.WriteRune(r)
				col++
			}
		}
		return NewStringVal(sb.String())
	})

	// Searching.
	set("index", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("index", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "index expected at least 1 argument, got 0"))
			return NoneVal()
		}
		sub, _ := strMethodArg(args[1])
		idx := strFindImpl(s, sub, args[2:], false)
		if idx < 0 {
			ctx.Raise(NewException(ValueErrorType, "substring not found"))
			return NoneVal()
		}
		return Int64Val(int64(idx))
	})
	set("rindex", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("rindex", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "rindex expected at least 1 argument, got 0"))
			return NoneVal()
		}
		sub, _ := strMethodArg(args[1])
		idx := strFindImpl(s, sub, args[2:], true)
		if idx < 0 {
			ctx.Raise(NewException(ValueErrorType, "substring not found"))
			return NoneVal()
		}
		return Int64Val(int64(idx))
	})
	set("find", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("find", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "find expected at least 1 argument, got 0"))
			return NoneVal()
		}
		sub, _ := strMethodArg(args[1])
		return Int64Val(int64(strFindImpl(s, sub, args[2:], false)))
	})
	set("rfind", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("rfind", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "rfind expected at least 1 argument, got 0"))
			return NoneVal()
		}
		sub, _ := strMethodArg(args[1])
		return Int64Val(int64(strFindImpl(s, sub, args[2:], true)))
	})
	set("count", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("count", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "count expected at least 1 argument, got 0"))
			return NoneVal()
		}
		sub, _ := strMethodArg(args[1])
		if len(args) > 2 {
			seg, off := strSliceBounds(s, args[2:])
			if off < 0 {
				return Int64Val(0)
			}
			s = seg
		}
		return Int64Val(int64(strings.Count(s, sub)))
	})

	// Splitting / partitioning.
	set("partition", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("partition", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "partition() takes exactly one argument (0 given)"))
			return NoneVal()
		}
		sep, _ := strMethodArg(args[1])
		if sep == "" {
			ctx.Raise(NewException(ValueErrorType, "empty separator"))
			return NoneVal()
		}
		if before, after, ok := strings.Cut(s, sep); ok {
			sepV := args[1]
			if !sepV.IsInlineStr() && sepV.Type() != StrType {
				sepV = NewStringVal(sep)
			}
			return NewTupleVal(NewStringVal(before), sepV, NewStringVal(after))
		}
		selfV := args[0]
		if !selfV.IsInlineStr() && selfV.Type() != StrType {
			selfV = NewStringVal(s)
		}
		return NewTupleVal(selfV, SmallStringVal(""), SmallStringVal(""))
	})
	set("rpartition", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("rpartition", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "rpartition() takes exactly one argument (0 given)"))
			return NoneVal()
		}
		sep, _ := strMethodArg(args[1])
		if sep == "" {
			ctx.Raise(NewException(ValueErrorType, "empty separator"))
			return NoneVal()
		}
		if idx := strings.LastIndex(s, sep); idx >= 0 {
			sepV := args[1]
			if !sepV.IsInlineStr() && sepV.Type() != StrType {
				sepV = NewStringVal(sep)
			}
			return NewTupleVal(NewStringVal(s[:idx]), sepV, NewStringVal(s[idx+len(sep):]))
		}
		selfV := args[0]
		if !selfV.IsInlineStr() && selfV.Type() != StrType {
			selfV = NewStringVal(s)
		}
		return NewTupleVal(SmallStringVal(""), SmallStringVal(""), selfV)
	})
	splitImpl := func(name string, fromRight bool) func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		return func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
			pargs, kw := splitStrKw(args, kwnames)
			s := strSelf(name, pargs)
			sepV, maxV := NoneVal(), Int64Val(-1)
			if len(pargs) > 1 {
				sepV = pargs[1]
			} else if v, ok := kw["sep"]; ok {
				sepV = v
			}
			if len(pargs) > 2 {
				maxV = pargs[2]
			} else if v, ok := kw["maxsplit"]; ok {
				maxV = v
			}
			maxsplit := strIntArg(maxV, -1)
			if sepV.IsNone() {
				if !fromRight {
					if res, ok := strSplitASCIIWhitespaceDirect(s, maxsplit); ok {
						return res
					}
				}
				return strSplitUnicodeWhitespaceDirect(s, maxsplit, fromRight)
			}
			sep, _ := strMethodArg(sepV)
			if sep == "" {
				ctx.Raise(NewException(ValueErrorType, "empty separator"))
				return NoneVal()
			}
			if !fromRight {
				return strSplitSepDirect(s, sep, maxsplit)
			}
			return strRsplitSepDirect(s, sep, maxsplit)
		}
	}
	set("split", splitImpl("split", false))
	set("rsplit", splitImpl("rsplit", true))
	set("splitlines", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		pargs, kw := splitStrKw(args, kwnames)
		s := strSelf("splitlines", pargs)
		keep := false
		if len(pargs) > 1 {
			keep = pargs[1].IsTruthy()
		} else if v, ok := kw["keepends"]; ok {
			keep = v.IsTruthy()
		}
		if len(s) == 0 {
			return NewListVal()
		}
		nLines := 1
		for i := 0; i < len(s); i++ {
			switch s[i] {
			case '\n':
				nLines++
			case '\r':
				nLines++
				if i+1 < len(s) && s[i+1] == '\n' {
					i++
				}
			}
		}
		items := make([]Value, 0, nLines)
		start := 0
		for i := 0; i < len(s); {
			b := s[i]
			if b < 0x80 {
				if b == '\n' || b == '\v' || b == '\f' || b == 0x1c || b == 0x1d || b == 0x1e {
					if keep {
						items = append(items, NewStringVal(s[start:i+1]))
					} else {
						items = append(items, NewStringVal(s[start:i]))
					}
					i++
					start = i
					continue
				}
				if b == '\r' {
					brLen := 1
					if i+1 < len(s) && s[i+1] == '\n' {
						brLen = 2
					}
					if keep {
						items = append(items, NewStringVal(s[start:i+brLen]))
					} else {
						items = append(items, NewStringVal(s[start:i]))
					}
					i += brLen
					start = i
					continue
				}
				i++
				continue
			}
			r, sz := utf8.DecodeRuneInString(s[i:])
			if r == 0x85 || r == 0x2028 || r == 0x2029 {
				if keep {
					items = append(items, NewStringVal(s[start:i+sz]))
				} else {
					items = append(items, NewStringVal(s[start:i]))
				}
				i += sz
				start = i
				continue
			}
			i += sz
		}
		if start < len(s) {
			items = append(items, NewStringVal(s[start:]))
		}
		return NewListFromOwnedSlice(items)
	})

	// Strip variants accepting None.
	isASCIIWhitespace := func(c byte) bool {
		return c == ' ' || (c >= '\t' && c <= '\r')
	}
	stripImpl := func(name string, mode int) func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		return func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
			if len(args) > 0 && IsInlineStrASCII(args[0]) {
				n := int(args[0].num & 0xFF)
				start, stop := 0, n
				if len(args) > 1 && !args[1].IsNone() {
					if !args[1].IsAnyString() {
						ctx.Raise(NewException(TypeErrorType, name+" arg must be None or str"))
						return NoneVal()
					}
					var cutBuf [7]byte
					cut := valueStringBytes(args[1], &cutBuf)
					if mode == 0 || mode == 1 {
						for start < stop && bytes.IndexByte(cut, byte(args[0].num>>((start+1)*8))) >= 0 {
							start++
						}
					}
					if mode == 0 || mode == 2 {
						for stop > start && bytes.IndexByte(cut, byte(args[0].num>>(stop*8))) >= 0 {
							stop--
						}
					}
					return SliceInlineStrASCII(args[0], start, stop)
				}
				if mode == 0 || mode == 1 {
					for start < stop && isASCIIWhitespace(byte(args[0].num>>((start+1)*8))) {
						start++
					}
				}
				if mode == 0 || mode == 2 {
					for stop > start && isASCIIWhitespace(byte(args[0].num>>(stop*8))) {
						stop--
					}
				}
				return SliceInlineStrASCII(args[0], start, stop)
			}
			s := strSelf(name, args)
			if len(args) > 1 && !args[1].IsNone() {
				cut, ok := strMethodArg(args[1])
				if !ok {
					ctx.Raise(NewException(TypeErrorType, name+" arg must be None or str"))
					return NoneVal()
				}
				switch mode {
				case 0:
					return NewStringVal(strings.Trim(s, cut))
				case 1:
					return NewStringVal(strings.TrimLeft(s, cut))
				default:
					return NewStringVal(strings.TrimRight(s, cut))
				}
			}
			switch mode {
			case 0:
				return NewStringVal(strings.TrimSpace(s))
			case 1:
				return NewStringVal(strings.TrimLeftFunc(s, unicode.IsSpace))
			default:
				return NewStringVal(strings.TrimRightFunc(s, unicode.IsSpace))
			}
		}
	}
	set("strip", stripImpl("strip", 0))
	set("lstrip", stripImpl("lstrip", 1))
	set("rstrip", stripImpl("rstrip", 2))

	set("removeprefix", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("removeprefix", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "removeprefix() takes exactly one argument (0 given)"))
			return NoneVal()
		}
		p, _ := strMethodArg(args[1])
		return NewStringVal(strings.TrimPrefix(s, p))
	})
	set("removesuffix", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("removesuffix", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "removesuffix() takes exactly one argument (0 given)"))
			return NoneVal()
		}
		p, _ := strMethodArg(args[1])
		return NewStringVal(strings.TrimSuffix(s, p))
	})

	set("translate", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("translate", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "translate() takes exactly one argument (0 given)"))
			return NoneVal()
		}
		return strTranslate(ctx, args[0], s, args[1])
	})

	set("format", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		pargs, kw := splitStrKw(args, kwnames)
		s := strSelf("format", pargs)
		return strFormatValueImpl(ctx, s, pargs[1:], func(name string) (Value, bool) {
			v, ok := kw[name]
			return v, ok
		})
	})
	set("format_map", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("format_map", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "format_map() takes exactly one argument (0 given)"))
			return NoneVal()
		}
		mapping := args[1]
		return strFormatValueImpl(ctx, s, nil, func(name string) (Value, bool) {
			return GetItemCtx(ctx, mapping, NewStringVal(name)), true
		})
	})
}

const (
	trUnchanged   uint8 = 0
	trDelete      uint8 = 1
	trASCIIByte   uint8 = 2
	trRune        uint8 = 3
	trString      uint8 = 4
	trInvalidInt  uint8 = 5
	trInvalidType uint8 = 6
)

func strTranslateOwnedBytes(out []byte) Value {
	if len(out) <= 7 {
		return SmallStringFromBytes(out)
	}
	return NewStringVal(unsafe.String(unsafe.SliceData(out), len(out)))
}

// strCaseTransform implements str.lower (toUpper=false) and str.upper (toUpper=true)
// with an ASCII fast path that avoids strings.Map overhead on ASCII bytes and returns
// selfVal directly when unchanged and selfVal is an exact str.
func strCaseTransform(selfVal Value, s string, toUpper bool) Value {
	n := len(s)
	if n == 0 {
		if selfVal.IsInlineStr() || selfVal.Type() == StrType {
			return selfVal
		}
		return SmallStringVal("")
	}
	firstDiff := -1
	nonASCII := false
	if toUpper {
		for i := range n {
			c := s[i]
			if c >= 0x80 {
				firstDiff = i
				nonASCII = true
				break
			}
			if c >= 'a' && c <= 'z' {
				firstDiff = i
				break
			}
		}
	} else {
		for i := range n {
			c := s[i]
			if c >= 0x80 {
				firstDiff = i
				nonASCII = true
				break
			}
			if c >= 'A' && c <= 'Z' {
				firstDiff = i
				break
			}
		}
	}
	if firstDiff < 0 {
		if selfVal.IsInlineStr() || selfVal.Type() == StrType {
			return selfVal
		}
		return NewStringVal(s)
	}
	if !nonASCII {
		out := make([]byte, n)
		copy(out, s[:firstDiff])
		i := firstDiff
		if toUpper {
			for ; i < n; i++ {
				c := s[i]
				if c >= 0x80 {
					nonASCII = true
					break
				}
				if c >= 'a' && c <= 'z' {
					c -= 'a' - 'A'
				}
				out[i] = c
			}
		} else {
			for ; i < n; i++ {
				c := s[i]
				if c >= 0x80 {
					nonASCII = true
					break
				}
				if c >= 'A' && c <= 'Z' {
					c += 'a' - 'A'
				}
				out[i] = c
			}
		}
		if !nonASCII {
			return strTranslateOwnedBytes(out)
		}
		buf := out[:i]
		return strCaseTransformUTF8Tail(buf, s[i:], toUpper)
	}
	buf := make([]byte, 0, n)
	buf = append(buf, s[:firstDiff]...)
	return strCaseTransformUTF8Tail(buf, s[firstDiff:], toUpper)
}

func strCaseTransformUTF8Tail(buf []byte, rest string, toUpper bool) Value {
	for i := 0; i < len(rest); {
		c := rest[i]
		if c < 0x80 {
			if toUpper {
				if c >= 'a' && c <= 'z' {
					c -= 'a' - 'A'
				}
			} else {
				if c >= 'A' && c <= 'Z' {
					c += 'a' - 'A'
				}
			}
			buf = append(buf, c)
			i++
			continue
		}
		r, sz := utf8.DecodeRuneInString(rest[i:])
		var mapped rune
		if toUpper {
			mapped = unicode.ToUpper(r)
		} else {
			mapped = unicode.ToLower(r)
		}
		if mapped == r {
			buf = append(buf, rest[i:i+sz]...)
		} else {
			buf = utf8.AppendRune(buf, mapped)
		}
		i += sz
	}
	return strTranslateOwnedBytes(buf)
}

func appendTranslatedValue(ctx *ExecContext, out []byte, mapped Value) ([]byte, bool) {
	switch {
	case mapped.IsNone():
		return out, true
	case mapped.IsInt64():
		cp := mapped.AsInt64()
		if cp < 0 || cp > 0x10FFFF {
			ctx.Raise(NewException(ValueErrorType, "character mapping must be in range(0x110000)"))
			return out, false
		}
		return utf8.AppendRune(out, rune(cp)), true
	case mapped.IsBool():
		if mapped.AsBool() {
			return append(out, 1), true
		}
		return append(out, 0), true
	default:
		if ms, ok := strMethodArg(mapped); ok {
			return append(out, ms...), true
		}
		ctx.Raise(NewException(TypeErrorType, "character mapping must return integer, None or str"))
		return out, false
	}
}

func strTranslate(ctx *ExecContext, selfVal Value, s string, table Value) Value {
	if len(s) == 0 {
		if selfVal.IsInlineStr() || selfVal.Type() == StrType {
			return selfVal
		}
		return SmallStringVal("")
	}
	if table.IsDict() && table.Type() == DictType {
		return strTranslateDict(ctx, s, table.AsDict())
	}
	return strTranslateMapping(ctx, s, table)
}

func strTranslateDict(ctx *ExecContext, s string, d *PyDict) Value {
	var asciiKind [128]uint8
	var asciiOut [128]byte
	var asciiRune [128]rune
	var asciiStrIdx [128]uint16
	for i := range 128 {
		asciiOut[i] = byte(i)
	}
	var multiStrs []string
	pure1to1 := true
	asciiOutOnly := true
	hasNonASCIIKey := false
	hasComplexKey := false

	recordEntry := func(k, v Value) {
		var cp int64
		switch {
		case k.IsInt64():
			cp = k.AsInt64()
		case k.IsBool():
			if k.AsBool() {
				cp = 1
			}
		case k.IsFloat64():
			f := k.AsFloat64()
			if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || f < 0 || f > 0x10FFFF {
				return
			}
			cp = int64(f)
		default:
			hasComplexKey = true
			return
		}
		if cp < 0 || cp > 0x10FFFF {
			return
		}
		if cp >= 128 {
			hasNonASCIIKey = true
			return
		}
		switch {
		case v.IsNone():
			asciiKind[cp] = trDelete
			pure1to1 = false
		case v.IsInt64():
			rc := v.AsInt64()
			switch {
			case rc < 0 || rc > 0x10FFFF:
				asciiKind[cp] = trInvalidInt
				pure1to1 = false
			case rc < 128:
				asciiKind[cp] = trASCIIByte
				asciiOut[cp] = byte(rc)
			default:
				asciiKind[cp] = trRune
				asciiRune[cp] = rune(rc)
				pure1to1 = false
				asciiOutOnly = false
			}
		case v.IsBool():
			asciiKind[cp] = trASCIIByte
			if v.AsBool() {
				asciiOut[cp] = 1
			} else {
				asciiOut[cp] = 0
			}
		default:
			if ms, ok := strMethodArg(v); ok {
				switch {
				case len(ms) == 0:
					asciiKind[cp] = trDelete
					pure1to1 = false
				case len(ms) == 1 && ms[0] < 0x80:
					asciiKind[cp] = trASCIIByte
					asciiOut[cp] = ms[0]
				default:
					asciiKind[cp] = trString
					asciiStrIdx[cp] = uint16(len(multiStrs))
					multiStrs = append(multiStrs, ms)
					pure1to1 = false
					asciiOutOnly = false
				}
			} else {
				asciiKind[cp] = trInvalidType
				pure1to1 = false
			}
		}
	}

	if d.Live != nil {
		for _, it := range d.liveItems() {
			recordEntry(it.Key, it.Value)
		}
	} else {
		locked := d.rlockDict()
		if sp := d.split; sp != nil {
			sk := sp.keys
			for i := range sk.Count {
				recordEntry(sk.Keys[i], sp.vals[i])
			}
		} else {
			for i := range d.Entries {
				if d.Entries[i].Active {
					recordEntry(d.Entries[i].Key, d.Entries[i].Value)
				}
			}
		}
		if locked {
			d.mu.RUnlock()
		}
	}

	if hasComplexKey {
		out := make([]byte, 0, len(s))
		for _, r := range s {
			mapped, found := d.Get(Int64Val(int64(r)))
			if !found {
				out = utf8.AppendRune(out, r)
				continue
			}
			var ok bool
			out, ok = appendTranslatedValue(ctx, out, mapped)
			if !ok {
				return NoneVal()
			}
		}
		return strTranslateOwnedBytes(out)
	}

	if isASCII(s) {
		if pure1to1 {
			out := make([]byte, len(s))
			for i := range len(s) {
				out[i] = asciiOut[s[i]]
			}
			return strTranslateOwnedBytes(out)
		}
		out := make([]byte, 0, len(s))
		if asciiOutOnly {
			for i := range len(s) {
				b := s[i]
				switch asciiKind[b] {
				case trUnchanged:
					out = append(out, b)
				case trASCIIByte:
					out = append(out, asciiOut[b])
				case trDelete:
				case trInvalidInt:
					ctx.Raise(NewException(ValueErrorType, "character mapping must be in range(0x110000)"))
					return NoneVal()
				default:
					ctx.Raise(NewException(TypeErrorType, "character mapping must return integer, None or str"))
					return NoneVal()
				}
			}
			return strTranslateOwnedBytes(out)
		}
		for i := range len(s) {
			b := s[i]
			switch asciiKind[b] {
			case trUnchanged:
				out = append(out, b)
			case trASCIIByte:
				out = append(out, asciiOut[b])
			case trDelete:
			case trRune:
				out = utf8.AppendRune(out, asciiRune[b])
			case trString:
				out = append(out, multiStrs[asciiStrIdx[b]]...)
			case trInvalidInt:
				ctx.Raise(NewException(ValueErrorType, "character mapping must be in range(0x110000)"))
				return NoneVal()
			default:
				ctx.Raise(NewException(TypeErrorType, "character mapping must return integer, None or str"))
				return NoneVal()
			}
		}
		return strTranslateOwnedBytes(out)
	}

	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		b := s[i]
		if b < 0x80 {
			switch asciiKind[b] {
			case trUnchanged:
				out = append(out, b)
			case trASCIIByte:
				out = append(out, asciiOut[b])
			case trDelete:
			case trRune:
				out = utf8.AppendRune(out, asciiRune[b])
			case trString:
				out = append(out, multiStrs[asciiStrIdx[b]]...)
			case trInvalidInt:
				ctx.Raise(NewException(ValueErrorType, "character mapping must be in range(0x110000)"))
				return NoneVal()
			default:
				ctx.Raise(NewException(TypeErrorType, "character mapping must return integer, None or str"))
				return NoneVal()
			}
			i++
			continue
		}
		r, sz := utf8.DecodeRuneInString(s[i:])
		if !hasNonASCIIKey {
			out = append(out, s[i:i+sz]...)
			i += sz
			continue
		}
		mapped, found := d.Get(Int64Val(int64(r)))
		if !found {
			out = append(out, s[i:i+sz]...)
			i += sz
			continue
		}
		var ok bool
		out, ok = appendTranslatedValue(ctx, out, mapped)
		if !ok {
			return NoneVal()
		}
		i += sz
	}
	return strTranslateOwnedBytes(out)
}

func strTranslateMapping(ctx *ExecContext, s string, table Value) Value {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		var mapped Value
		found := false
		var rethrow any
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					if v, ok := rec.(Value); ok && v.Type() != nil && (IsSubclass(v.Type(), LookupErrorType) || IsSubclass(v.Type(), KeyErrorType)) {
						found = false
						return
					}
					rethrow = rec
				}
			}()
			mapped = GetItemCtx(ctx, table, Int64Val(int64(r)))
			if ctx != nil && ctx.HasException() {
				exc := ctx.ActiveException
				if exc.Type() != nil && (IsSubclass(exc.Type(), LookupErrorType) || IsSubclass(exc.Type(), KeyErrorType)) {
					ctx.ClearException()
					found = false
					return
				}
				return
			}
			found = true
		}()
		if rethrow != nil {
			panic(rethrow)
		}
		if ctx != nil && ctx.HasException() {
			return NoneVal()
		}
		if !found {
			out = utf8.AppendRune(out, r)
			continue
		}
		var ok bool
		out, ok = appendTranslatedValue(ctx, out, mapped)
		if !ok {
			return NoneVal()
		}
	}
	return strTranslateOwnedBytes(out)
}
