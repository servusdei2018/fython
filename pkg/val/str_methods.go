package val

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
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
		return v.Type() == SetType
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
		if fn, owner, ok := v.Type().LookupMRO(InternString("__repr__")); ok && owner != ObjectType {
			res := ctx.Invoke1(fn, v)
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
	if v.Kind() == KindMappingProxy {
		return "mappingproxy(" + PyRepr(ctx, v.AsMappingProxy().Mapping) + ")"
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
			sb.WriteString(PyRepr(ctx, it))
		}
		if v.Kind() == KindTuple && len(items) == 1 {
			sb.WriteByte(',')
		}
		sb.WriteString(close)
		return sb.String()
	case KindDict:
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
			sb.WriteString(PyRepr(ctx, e.Key))
			sb.WriteString(": ")
			sb.WriteString(PyRepr(ctx, e.Value))
		}
		sb.WriteByte('}')
		return sb.String()
	}
	return v.String()
}

// InstallNativeRepr gives t its own __repr__ method. Without one the type
// inherits object.__repr__, and code that keys a table on type.__repr__
// (pprint does) sees every builtin type as the same entry.
func InstallNativeRepr(t *PyType) {
	t.Dict.Set(InternString("__repr__"), NewBuiltinFuncVal(t.Name+".__repr__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) != 1 {
			panic("TypeError: expected 0 arguments, got " + strconv.Itoa(len(args)-1))
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
		case KindList, KindTuple, KindDict:
			return PyRepr(ctx, v)
		}
		return v.String()
	}
	if ctx != nil {
		if fn, owner, ok := v.Type().LookupMRO(InternString("__str__")); ok && owner != ObjectType {
			res := ctx.Invoke1(fn, v)
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
		if fn, owner, ok := v.Type().LookupMRO(InternString("__format__")); ok && owner != ObjectType && owner != StrType {
			res := ctx.Invoke2(fn, v, NewStringVal(spec))
			if s, ok := strMethodArg(res); ok {
				return s
			}
			return res.String()
		}
		if s, ok := strMethodArg(v); ok {
			return FormatValue(NewStringVal(s), spec, 0).AsString()
		}
		if spec != "" {
			panic("TypeError: unsupported format string passed to " + v.Type().Name + ".__format__")
		}
		return PyStr(ctx, v)
	}
	return FormatValue(v, spec, 0).AsString()
}

// strFormatImpl implements str.format / str.format_map.
func strFormatImpl(ctx *ExecContext, format string, pos []Value, kw func(string) (Value, bool), depth int) string {
	if depth > 2 {
		panic("ValueError: Max string recursion exceeded")
	}
	var sb strings.Builder
	autoIdx := 0
	n := len(format)
	for i := 0; i < n; {
		c := format[i]
		if c == '}' {
			if i+1 < n && format[i+1] == '}' {
				sb.WriteByte('}')
				i += 2
				continue
			}
			panic("ValueError: Single '}' encountered in format string")
		}
		if c != '{' {
			sb.WriteByte(c)
			i++
			continue
		}
		if i+1 < n && format[i+1] == '{' {
			sb.WriteByte('{')
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
			panic("ValueError: expected '}' before end of string")
		}
		field := format[i+1 : j]
		i = j + 1

		// Split field_name[!conv][:spec]
		fieldName, conv, spec := field, byte(0), ""
		bracket := 0
		for k := 0; k < len(field); k++ {
			ch := field[k]
			if ch == '[' {
				bracket++
			} else if ch == ']' {
				bracket--
			} else if bracket == 0 && (ch == '!' || ch == ':') {
				fieldName = field[:k]
				rest := field[k:]
				if ch == '!' {
					if len(rest) < 2 {
						panic("ValueError: end of string while looking for conversion specifier")
					}
					conv = rest[1]
					rest = rest[2:]
					if len(rest) > 0 {
						if rest[0] != ':' {
							panic("ValueError: expected ':' after conversion specifier")
						}
						spec = rest[1:]
					}
				} else {
					spec = rest[1:]
				}
				break
			}
		}
		if strings.IndexByte(spec, '{') >= 0 {
			spec = strFormatImpl(ctx, spec, pos, kw, depth+1)
		}

		// Resolve the first component of field name.
		first := fieldName
		restIdx := len(fieldName)
		for k := 0; k < len(fieldName); k++ {
			if fieldName[k] == '.' || fieldName[k] == '[' {
				first = fieldName[:k]
				restIdx = k
				break
			}
		}
		var obj Value
		if first == "" {
			if autoIdx < 0 {
				panic("ValueError: cannot switch from manual field specification to automatic field numbering")
			}
			if autoIdx >= len(pos) {
				panic("IndexError: Replacement index " + strconv.Itoa(autoIdx) + " out of range for positional args tuple")
			}
			obj = pos[autoIdx]
			autoIdx++
		} else if idx, err := strconv.Atoi(first); err == nil {
			if autoIdx > 0 {
				panic("ValueError: cannot switch from automatic field numbering to manual field specification")
			}
			autoIdx = -1
			if idx >= len(pos) {
				panic("IndexError: Replacement index " + first + " out of range for positional args tuple")
			}
			obj = pos[idx]
		} else {
			v, ok := kw(first)
			if !ok {
				panic("KeyError: " + PyStrRepr(first))
			}
			obj = v
		}
		// Attribute / index chain.
		rest := fieldName[restIdx:]
		for len(rest) > 0 {
			if rest[0] == '.' {
				k := 1
				for k < len(rest) && rest[k] != '.' && rest[k] != '[' {
					k++
				}
				obj = GetAttrCtx(ctx, obj, InternString(rest[1:k]))
				rest = rest[k:]
			} else if rest[0] == '[' {
				k := strings.IndexByte(rest, ']')
				if k < 0 {
					panic("ValueError: Missing ']' in format string")
				}
				key := rest[1:k]
				var keyVal Value
				if ki, err := strconv.Atoi(key); err == nil {
					keyVal = Int64Val(int64(ki))
				} else {
					keyVal = NewStringVal(key)
				}
				obj = GetItemCtx(ctx, obj, keyVal)
				rest = rest[k+1:]
			} else {
				panic("ValueError: Only '.' or '[' may follow ']' in format field specifier")
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
			panic("ValueError: Unknown conversion specifier " + string(conv))
		}
		sb.WriteString(PyFormat(ctx, obj, spec))
	}
	return sb.String()
}

// PyASCII computes ascii(v).
func PyASCII(ctx *ExecContext, v Value) string {
	return asciiEscape(PyRepr(ctx, v))
}

func asciiEscape(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if r < 128 {
			sb.WriteRune(r)
		} else if r <= 0xff {
			sb.WriteString("\\x")
			h := strconv.FormatInt(int64(r), 16)
			if len(h) < 2 {
				sb.WriteByte('0')
			}
			sb.WriteString(h)
		} else if r <= 0xffff {
			h := strconv.FormatInt(int64(r), 16)
			sb.WriteString("\\u" + strings.Repeat("0", 4-len(h)) + h)
		} else {
			h := strconv.FormatInt(int64(r), 16)
			sb.WriteString("\\U" + strings.Repeat("0", 8-len(h)) + h)
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
			if !(r == '_' || unicode.IsLetter(r)) {
				return false
			}
		} else if !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) || unicode.Is(unicode.Nd, r) || unicode.Is(unicode.Pc, r)) {
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

// strSliceBounds normalizes optional start/end args (rune indices) into byte offsets.
func strSliceBounds(s string, args []Value) (string, int) {
	runes := []rune(s)
	ln := len(runes)
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
	return string(runes[start:end]), start
}

func strFindImpl(s, sub string, extra []Value, last bool) int {
	ascii := isASCII(s)
	if ascii && len(extra) == 0 {
		if last {
			return strings.LastIndex(s, sub)
		}
		return strings.Index(s, sub)
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
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
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
	StrType.Dict.Set(InternString("isascii"), strPredicate("isascii", true, func(r rune) bool { return r < 128 }))
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
			if unicode.IsUpper(r) || unicode.IsTitle(r) {
				if prevCased {
					return BoolVal(false)
				}
				prevCased, cased = true, true
			} else if unicode.IsLower(r) {
				if !prevCased {
					return BoolVal(false)
				}
				prevCased, cased = true, true
			} else {
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
			panic("TypeError: center expected at least 1 argument, got 0")
		}
		return NewStringVal(strPad(s, strIntArg(args[1], 0), strFillArg(args, 2), '^'))
	})
	set("ljust", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("ljust", args)
		if len(args) < 2 {
			panic("TypeError: ljust expected at least 1 argument, got 0")
		}
		return NewStringVal(strPad(s, strIntArg(args[1], 0), strFillArg(args, 2), '<'))
	})
	set("rjust", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("rjust", args)
		if len(args) < 2 {
			panic("TypeError: rjust expected at least 1 argument, got 0")
		}
		return NewStringVal(strPad(s, strIntArg(args[1], 0), strFillArg(args, 2), '>'))
	})
	set("zfill", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("zfill", args)
		if len(args) < 2 {
			panic("TypeError: zfill() takes exactly one argument (0 given)")
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
			panic("TypeError: index expected at least 1 argument, got 0")
		}
		sub, _ := strMethodArg(args[1])
		idx := strFindImpl(s, sub, args[2:], false)
		if idx < 0 {
			panic("ValueError: substring not found")
		}
		return Int64Val(int64(idx))
	})
	set("rindex", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("rindex", args)
		if len(args) < 2 {
			panic("TypeError: rindex expected at least 1 argument, got 0")
		}
		sub, _ := strMethodArg(args[1])
		idx := strFindImpl(s, sub, args[2:], true)
		if idx < 0 {
			panic("ValueError: substring not found")
		}
		return Int64Val(int64(idx))
	})
	set("find", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("find", args)
		if len(args) < 2 {
			panic("TypeError: find expected at least 1 argument, got 0")
		}
		sub, _ := strMethodArg(args[1])
		return Int64Val(int64(strFindImpl(s, sub, args[2:], false)))
	})
	set("rfind", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("rfind", args)
		if len(args) < 2 {
			panic("TypeError: rfind expected at least 1 argument, got 0")
		}
		sub, _ := strMethodArg(args[1])
		return Int64Val(int64(strFindImpl(s, sub, args[2:], true)))
	})
	set("count", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("count", args)
		if len(args) < 2 {
			panic("TypeError: count expected at least 1 argument, got 0")
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
			panic("TypeError: partition() takes exactly one argument (0 given)")
		}
		sep, _ := strMethodArg(args[1])
		if sep == "" {
			panic("ValueError: empty separator")
		}
		if before, after, ok := strings.Cut(s, sep); ok {
			return NewTupleVal(NewStringVal(before), NewStringVal(sep), NewStringVal(after))
		}
		return NewTupleVal(NewStringVal(s), NewStringVal(""), NewStringVal(""))
	})
	set("rpartition", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("rpartition", args)
		if len(args) < 2 {
			panic("TypeError: rpartition() takes exactly one argument (0 given)")
		}
		sep, _ := strMethodArg(args[1])
		if sep == "" {
			panic("ValueError: empty separator")
		}
		if idx := strings.LastIndex(s, sep); idx >= 0 {
			return NewTupleVal(NewStringVal(s[:idx]), NewStringVal(sep), NewStringVal(s[idx+len(sep):]))
		}
		return NewTupleVal(NewStringVal(""), NewStringVal(""), NewStringVal(s))
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
				return stringsToList(strSplitWhitespace(s, maxsplit, fromRight))
			}
			sep, _ := strMethodArg(sepV)
			if sep == "" {
				panic("ValueError: empty separator")
			}
			if maxsplit < 0 {
				return stringsToList(strings.Split(s, sep))
			}
			if !fromRight {
				return stringsToList(strings.SplitN(s, sep, maxsplit+1))
			}
			var parts []string
			rest := s
			for len(parts) < maxsplit {
				idx := strings.LastIndex(rest, sep)
				if idx < 0 {
					break
				}
				parts = append(parts, rest[idx+len(sep):])
				rest = rest[:idx]
			}
			parts = append(parts, rest)
			for l, r := 0, len(parts)-1; l < r; l, r = l+1, r-1 {
				parts[l], parts[r] = parts[r], parts[l]
			}
			return stringsToList(parts)
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
		var parts []string
		start := 0
		for i := 0; i < len(s); {
			r, sz := utf8.DecodeRuneInString(s[i:])
			isBreak := false
			brLen := sz
			switch r {
			case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
				isBreak = true
			case '\r':
				isBreak = true
				if i+1 < len(s) && s[i+1] == '\n' {
					brLen = 2
				}
			}
			if isBreak {
				if keep {
					parts = append(parts, s[start:i+brLen])
				} else {
					parts = append(parts, s[start:i])
				}
				i += brLen
				start = i
				continue
			}
			i += sz
		}
		if start < len(s) {
			parts = append(parts, s[start:])
		}
		return stringsToList(parts)
	})

	// Strip variants accepting None.
	stripImpl := func(name string, mode int) func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		return func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
			s := strSelf(name, args)
			if len(args) > 1 && !args[1].IsNone() {
				cut, ok := strMethodArg(args[1])
				if !ok {
					panic("TypeError: " + name + " arg must be None or str")
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
			panic("TypeError: removeprefix() takes exactly one argument (0 given)")
		}
		p, _ := strMethodArg(args[1])
		return NewStringVal(strings.TrimPrefix(s, p))
	})
	set("removesuffix", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("removesuffix", args)
		if len(args) < 2 {
			panic("TypeError: removesuffix() takes exactly one argument (0 given)")
		}
		p, _ := strMethodArg(args[1])
		return NewStringVal(strings.TrimSuffix(s, p))
	})

	set("translate", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("translate", args)
		if len(args) < 2 {
			panic("TypeError: translate() takes exactly one argument (0 given)")
		}
		table := args[1]
		var sb strings.Builder
		for _, r := range s {
			var mapped Value
			found := false
			if table.IsDict() {
				mapped, found = table.AsDict().Get(Int64Val(int64(r)))
			} else {
				func() {
					defer func() {
						if rec := recover(); rec != nil {
							found = false
						}
					}()
					mapped = GetItemCtx(ctx, table, Int64Val(int64(r)))
					if ctx != nil && ctx.HasException() {
						ctx.ClearException()
						found = false
						return
					}
					found = true
				}()
			}
			if !found {
				sb.WriteRune(r)
				continue
			}
			switch {
			case mapped.IsNone():
			case mapped.IsInt64():
				sb.WriteRune(rune(mapped.AsInt64()))
			default:
				ms, _ := strMethodArg(mapped)
				sb.WriteString(ms)
			}
		}
		return NewStringVal(sb.String())
	})

	set("format", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		pargs, kw := splitStrKw(args, kwnames)
		s := strSelf("format", pargs)
		return NewStringVal(strFormatImpl(ctx, s, pargs[1:], func(name string) (Value, bool) {
			v, ok := kw[name]
			return v, ok
		}, 0))
	})
	set("format_map", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		s := strSelf("format_map", args)
		if len(args) < 2 {
			panic("TypeError: format_map() takes exactly one argument (0 given)")
		}
		mapping := args[1]
		return NewStringVal(strFormatImpl(ctx, s, nil, func(name string) (Value, bool) {
			return GetItemCtx(ctx, mapping, NewStringVal(name)), true
		}, 0))
	})
}
