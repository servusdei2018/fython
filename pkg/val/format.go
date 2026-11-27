package val

import (
	"bytes"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This file implements Python's Format Specification Mini-Language:
//
//	[[fill]align][sign][z][#][0][width][grouping_option][.precision][type]
//
// for int, float, complex and str. Errors panic with "ValueError: ..." or
// "TypeError: ..." strings like the rest of the runtime.

type fmtSpec struct {
	fill       rune
	fillSet    bool
	align      byte // '<' '>' '^' '=' or 0 when unspecified
	sign       byte // '+' '-' ' ' or 0
	z          bool
	alt        bool
	zero       bool
	width      int // -1 when unspecified
	group      byte
	groupAfter bool // grouping option appeared (',' or '_')
	prec       int  // -1 when unspecified
	typ        byte // 0 when unspecified
}

func isAlignChar(r rune) bool { return r == '<' || r == '>' || r == '^' || r == '=' }

func fmtValueErrorCtx(ctx *ExecContext, msg string) {
	RaiseValueError(ctx, msg)
}

// parseFormatSpecCtx parses spec. tname is the Python type name used in errors.
// defaultAlign is '<' for strings and '>' for numbers.
func parseFormatSpecCtx(ctx *ExecContext, spec, tname string, defaultAlign byte) (fmtSpec, bool) {
	fs := fmtSpec{fill: ' ', width: -1, prec: -1}
	rs := []rune(spec)
	i := 0
	n := len(rs)
	if n >= 2 && isAlignChar(rs[1]) {
		fs.fill, fs.fillSet, fs.align = rs[0], true, byte(rs[1])
		i = 2
	} else if n >= 1 && isAlignChar(rs[0]) {
		fs.align = byte(rs[0])
		i = 1
	}
	if i < n && (rs[i] == '+' || rs[i] == '-' || rs[i] == ' ') {
		fs.sign = byte(rs[i])
		i++
	}
	if i < n && rs[i] == 'z' {
		fs.z = true
		i++
	}
	if i < n && rs[i] == '#' {
		fs.alt = true
		i++
	}
	if !fs.fillSet && i < n && rs[i] == '0' {
		fs.fill = '0'
		fs.zero = true
		if fs.align == 0 && defaultAlign == '>' {
			fs.align = '='
		}
		i++
	}
	readInt := func(what string) (int, bool) {
		start := i
		v := 0
		for i < n && rs[i] >= '0' && rs[i] <= '9' {
			v = v*10 + int(rs[i]-'0')
			if v > math.MaxInt32 {
				fmtValueErrorCtx(ctx, "Too many decimal digits in format string")
				return 0, false
			}
			i++
		}
		if i == start {
			return -1, true
		}
		return v, true
	}
	w, ok := readInt("width")
	if !ok {
		return fs, false
	}
	if w >= 0 {
		fs.width = w
	}
	if i < n && (rs[i] == ',' || rs[i] == '_') {
		fs.group = byte(rs[i])
		fs.groupAfter = true
		i++
		if i < n && (rs[i] == ',' || rs[i] == '_') {
			if byte(rs[i]) != fs.group {
				fmtValueErrorCtx(ctx, "Cannot specify both ',' and '_'.")
				return fs, false
			}
			fmtValueErrorCtx(ctx, "Cannot specify '"+string(rs[i])+"' more than once")
			return fs, false
		}
	}
	if i < n && rs[i] == '.' {
		i++
		p, ok := readInt("precision")
		if !ok {
			return fs, false
		}
		if p < 0 {
			fmtValueErrorCtx(ctx, "Format specifier missing precision")
			return fs, false
		}
		fs.prec = p
	}
	if i < n && (rs[i] == ',' || rs[i] == '_') {
		if fs.groupAfter {
			fmtValueErrorCtx(ctx, "Cannot specify both ',' and '_'.")
			return fs, false
		}
		fmtValueErrorCtx(ctx, "Invalid format specifier '"+spec+"' for object of type '"+tname+"'")
		return fs, false
	}
	if n-i > 1 {
		fmtValueErrorCtx(ctx, "Invalid format specifier '"+spec+"' for object of type '"+tname+"'")
		return fs, false
	}
	if n-i == 1 {
		fs.typ = byte(rs[i])
		if rs[i] > 127 {
			fs.typ = '?'
		}
	}
	if fs.groupAfter {
		switch fs.typ {
		case 0, 'd', 'e', 'f', 'g', 'E', 'G', '%', 'F':
		case 'b', 'o', 'x', 'X':
			if fs.group == ',' {
				fmtValueErrorCtx(ctx, "Cannot specify ',' with '"+string(fs.typ)+"'.")
				return fs, false
			}
		default:
			if fs.typ > 32 && fs.typ < 128 {
				fmtValueErrorCtx(ctx, "Cannot specify '"+string(fs.group)+"' with '"+string(fs.typ)+"'.")
				return fs, false
			}
			fmtValueErrorCtx(ctx, "Cannot specify '"+string(fs.group)+"' with '\\x"+strconv.FormatInt(int64(fs.typ), 16)+"'.")
			return fs, false
		}
	}
	if fs.align == 0 {
		fs.align = defaultAlign
	}
	return fs, true
}

func parseFormatSpecBytesCtx(ctx *ExecContext, spec []byte, tname string, defaultAlign byte) (fmtSpec, bool) {
	for _, b := range spec {
		if b >= 0x80 {
			return parseFormatSpecCtx(ctx, string(spec), tname, defaultAlign)
		}
	}
	fs := fmtSpec{fill: ' ', width: -1, prec: -1}
	i := 0
	n := len(spec)
	if n >= 2 && isAlignChar(rune(spec[1])) {
		fs.fill, fs.fillSet, fs.align = rune(spec[0]), true, spec[1]
		i = 2
	} else if n >= 1 && isAlignChar(rune(spec[0])) {
		fs.align = spec[0]
		i = 1
	}
	if i < n && (spec[i] == '+' || spec[i] == '-' || spec[i] == ' ') {
		fs.sign = spec[i]
		i++
	}
	if i < n && spec[i] == 'z' {
		fs.z = true
		i++
	}
	if i < n && spec[i] == '#' {
		fs.alt = true
		i++
	}
	if !fs.fillSet && i < n && spec[i] == '0' {
		fs.fill = '0'
		fs.zero = true
		if fs.align == 0 && defaultAlign == '>' {
			fs.align = '='
		}
		i++
	}
	if i < n && spec[i] >= '0' && spec[i] <= '9' {
		v := 0
		for i < n && spec[i] >= '0' && spec[i] <= '9' {
			v = v*10 + int(spec[i]-'0')
			if v > math.MaxInt32 {
				fmtValueErrorCtx(ctx, "Too many decimal digits in format string")
				return fs, false
			}
			i++
		}
		fs.width = v
	}
	if i < n && (spec[i] == ',' || spec[i] == '_') {
		fs.group = spec[i]
		fs.groupAfter = true
		i++
		if i < n && (spec[i] == ',' || spec[i] == '_') {
			if spec[i] != fs.group {
				fmtValueErrorCtx(ctx, "Cannot specify both ',' and '_'.")
				return fs, false
			}
			fmtValueErrorCtx(ctx, "Cannot specify '"+string(spec[i])+"' more than once")
			return fs, false
		}
	}
	if i < n && spec[i] == '.' {
		i++
		start := i
		v := 0
		for i < n && spec[i] >= '0' && spec[i] <= '9' {
			v = v*10 + int(spec[i]-'0')
			if v > math.MaxInt32 {
				fmtValueErrorCtx(ctx, "Too many decimal digits in format string")
				return fs, false
			}
			i++
		}
		if i == start {
			fmtValueErrorCtx(ctx, "Format specifier missing precision")
			return fs, false
		}
		fs.prec = v
	}
	if i < n && (spec[i] == ',' || spec[i] == '_') {
		if fs.groupAfter {
			fmtValueErrorCtx(ctx, "Cannot specify both ',' and '_'.")
			return fs, false
		}
		fmtValueErrorCtx(ctx, "Invalid format specifier '"+string(spec)+"' for object of type '"+tname+"'")
		return fs, false
	}
	if n-i > 1 {
		fmtValueErrorCtx(ctx, "Invalid format specifier '"+string(spec)+"' for object of type '"+tname+"'")
		return fs, false
	}
	if n-i == 1 {
		fs.typ = spec[i]
	}
	if fs.groupAfter {
		switch fs.typ {
		case 0, 'd', 'e', 'f', 'g', 'E', 'G', '%', 'F':
		case 'b', 'o', 'x', 'X':
			if fs.group == ',' {
				fmtValueErrorCtx(ctx, "Cannot specify ',' with '"+string(fs.typ)+"'.")
				return fs, false
			}
		default:
			if fs.typ > 32 && fs.typ < 128 {
				fmtValueErrorCtx(ctx, "Cannot specify '"+string(fs.group)+"' with '"+string(fs.typ)+"'.")
				return fs, false
			}
			fmtValueErrorCtx(ctx, "Cannot specify '"+string(fs.group)+"' with '\\x"+strconv.FormatInt(int64(fs.typ), 16)+"'.")
			return fs, false
		}
	}
	if fs.align == 0 {
		fs.align = defaultAlign
	}
	return fs, true
}

// FormatInt64SpecBytesCtx formats n according to spec bytes without escaping spec.
func FormatInt64SpecBytesCtx(ctx *ExecContext, n int64, spec []byte) Value {
	if len(spec) == 0 || (len(spec) == 1 && spec[0] == 'd') {
		return IntToStringVal(n)
	}
	fs, ok := parseFormatSpecBytesCtx(ctx, spec, "int", '>')
	if !ok {
		return SmallStringVal("")
	}
	// Fast path for common int64 specs without grouping or float coercion.
	if !fs.groupAfter && !fs.z && fs.fill < 0x80 && fs.prec < 0 &&
		(fs.typ == 0 || fs.typ == 'd' || fs.typ == 'x' || fs.typ == 'X' || fs.typ == 'o' || fs.typ == 'b') &&
		(fs.align == '>' || fs.align == '<' || fs.align == '=') {
		neg := n < 0
		var u uint64
		if neg {
			u = uint64(-n)
		} else {
			u = uint64(n)
		}
		base := 10
		switch fs.typ {
		case 'x', 'X':
			base = 16
		case 'o':
			base = 8
		case 'b':
			base = 2
		}
		var numBuf [72]byte
		digits := strconv.AppendUint(numBuf[:0], u, base)
		if fs.typ == 'X' {
			for k := range digits {
				if digits[k] >= 'a' && digits[k] <= 'f' {
					digits[k] -= 'a' - 'A'
				}
			}
		}
		var sign byte
		switch {
		case neg:
			sign = '-'
		case fs.sign == '+':
			sign = '+'
		case fs.sign == ' ':
			sign = ' '
		}
		var prefix string
		if fs.alt {
			switch fs.typ {
			case 'x':
				prefix = "0x"
			case 'X':
				prefix = "0X"
			case 'o':
				prefix = "0o"
			case 'b':
				prefix = "0b"
			}
		}
		signLen := 0
		if sign != 0 {
			signLen = 1
		}
		contentLen := signLen + len(prefix) + len(digits)
		pad := 0
		if fs.width > contentLen {
			pad = fs.width - contentLen
		}
		var outBuf [128]byte
		var out []byte
		if contentLen+pad <= len(outBuf) {
			out = outBuf[:0]
		} else {
			out = make([]byte, 0, contentLen+pad)
		}
		fill := byte(fs.fill)
		switch fs.align {
		case '<':
			if sign != 0 {
				out = append(out, sign)
			}
			out = append(out, prefix...)
			out = append(out, digits...)
			out = appendPadBytes(out, fill, pad)
		case '=':
			if sign != 0 {
				out = append(out, sign)
			}
			out = append(out, prefix...)
			out = appendPadBytes(out, fill, pad)
			out = append(out, digits...)
		default: // '>'
			out = appendPadBytes(out, fill, pad)
			if sign != 0 {
				out = append(out, sign)
			}
			out = append(out, prefix...)
			out = append(out, digits...)
		}
		return NewStringFromBytes(out)
	}
	return NewStringVal(FormatIntSpecCtx(ctx, big.NewInt(n), string(spec)))
}

// FormatFloatSpecBytesCtx formats f according to spec bytes without escaping spec.
func FormatFloatSpecBytesCtx(ctx *ExecContext, f float64, spec []byte) Value {
	if len(spec) == 0 {
		var buf [48]byte
		return NewStringFromBytes(AppendFloatRepr(buf[:0], f))
	}
	fs, ok := parseFormatSpecBytesCtx(ctx, spec, "float", '>')
	if !ok {
		return SmallStringVal("")
	}
	// Fast path for common finite fixed-point/scientific specs without grouping or z-flag.
	if !fs.groupAfter && !fs.z && fs.fill < 0x80 &&
		(fs.typ == 'f' || fs.typ == 'F' || fs.typ == 'e' || fs.typ == 'E') &&
		(fs.align == '>' || fs.align == '<' || fs.align == '=') &&
		!math.IsNaN(f) && !math.IsInf(f, 0) {
		ps := pctSpec{
			left:  fs.align == '<',
			plus:  fs.sign == '+',
			space: fs.sign == ' ',
			zero:  fs.align == '=' && fs.fill == '0',
			alt:   fs.alt,
			width: fs.width,
			prec:  fs.prec,
		}
		if fs.align != '=' || fs.fill == '0' {
			if fs.fill == ' ' || fs.fill == '0' {
				var outBuf [128]byte
				out := appendPctFloat64(outBuf[:0], f, fs.typ, ps)
				return NewStringFromBytes(out)
			}
		}
	}
	return NewStringVal(formatFloatSpec(ctx, f, fs, "float"))
}

// FormatStrSpecBytesCtx formats s according to spec bytes without escaping spec.
func FormatStrSpecBytesCtx(ctx *ExecContext, s string, spec []byte) Value {
	if len(spec) == 0 {
		return NewStringVal(s)
	}
	fs, ok := parseFormatSpecBytesCtx(ctx, spec, "str", '<')
	if !ok {
		return SmallStringVal("")
	}
	if fs.typ != 0 && fs.typ != 's' {
		unknownFormatCodeCtx(ctx, rune(fs.typ), "str")
		return SmallStringVal("")
	}
	if fs.groupAfter {
		fmtValueErrorCtx(ctx, "Cannot specify '"+string(fs.group)+"' with 's'.")
		return SmallStringVal("")
	}
	if fs.sign == ' ' {
		fmtValueErrorCtx(ctx, "Space not allowed in string format specifier")
		return SmallStringVal("")
	}
	if fs.sign != 0 {
		fmtValueErrorCtx(ctx, "Sign not allowed in string format specifier")
		return SmallStringVal("")
	}
	if fs.z {
		fmtValueErrorCtx(ctx, "Negative zero coercion (z) not allowed in string format specifier")
		return SmallStringVal("")
	}
	if fs.alt {
		fmtValueErrorCtx(ctx, "Alternate form (#) not allowed in string format specifier")
		return SmallStringVal("")
	}
	if fs.align == '=' {
		fmtValueErrorCtx(ctx, "'=' alignment not allowed in string format specifier")
		return SmallStringVal("")
	}
	if fs.prec >= 0 && utf8.RuneCountInString(s) > fs.prec {
		r := []rune(s)
		s = string(r[:fs.prec])
	}
	return NewStringVal(padFormatted(s, fs))
}

func unknownFormatCodeCtx(ctx *ExecContext, spec rune, tname string) {
	c := string(spec)
	if spec > 0x7e || spec < 0x20 {
		fmtValueErrorCtx(ctx, "Unknown format code '\\x"+strconv.FormatInt(int64(spec), 16)+"' for object of type '"+tname+"'")
		return
	}
	fmtValueErrorCtx(ctx, "Unknown format code '"+c+"' for object of type '"+tname+"'")
}

// padFormatted pads s (rune-counted) to width using the fill/alignment of fs.
func padFormatted(s string, fs fmtSpec) string {
	if fs.width < 0 {
		return s
	}
	cur := utf8.RuneCountInString(s)
	if cur >= fs.width {
		return s
	}
	pad := fs.width - cur
	f := string(fs.fill)
	switch fs.align {
	case '<':
		return s + strings.Repeat(f, pad)
	case '^':
		l := pad / 2
		return strings.Repeat(f, l) + s + strings.Repeat(f, pad-l)
	default: // '>' (and '=' when handled by the caller)
		return strings.Repeat(f, pad) + s
	}
}

// groupDigits inserts sep every gs digits (from the right), zero-extending to
// minWidth the way CPython's _PyUnicode_InsertThousandsGrouping does.
func groupDigits(d string, sep string, gs int, minWidth int) string {
	if sep == "" {
		gs = math.MaxInt32
	}
	remaining := len(d)
	pos := len(d)
	var parts []string
	for {
		l := gs
		m := max(1, remaining, minWidth)
		if m < l {
			l = m
		}
		nZeros := max(0, l-remaining)
		nChars := max(0, min(remaining, l))
		parts = append(parts, strings.Repeat("0", nZeros)+d[pos-nChars:pos])
		pos -= nChars
		remaining -= nChars
		minWidth -= l
		if remaining <= 0 && minWidth <= 0 {
			break
		}
		minWidth -= len(sep)
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, sep)
}

// assembleNumber joins sign, prefix, integer digits and the remaining tail,
// applying grouping, zero padding and alignment.
func assembleNumber(fs fmtSpec, neg bool, prefix, digits, tail string, groupSize int) string {
	sign := ""
	switch {
	case neg:
		sign = "-"
	case fs.sign == '+':
		sign = "+"
	case fs.sign == ' ':
		sign = " "
	}
	sep := ""
	if fs.group != 0 {
		sep = string(fs.group)
	}
	minWidth := 0
	if fs.fill == '0' && fs.align == '=' && fs.width >= 0 {
		minWidth = fs.width - utf8.RuneCountInString(sign+prefix+tail)
	}
	body := digits
	if digits != "" {
		body = groupDigits(digits, sep, groupSize, minWidth)
	}
	if fs.align == '=' {
		head := sign + prefix
		rest := body + tail
		total := utf8.RuneCountInString(head) + utf8.RuneCountInString(rest)
		if fs.width > total {
			rest = strings.Repeat(string(fs.fill), fs.width-total) + rest
		}
		return head + rest
	}
	return padFormatted(sign+prefix+body+tail, fs)
}

// ---------------------------------------------------------------------------
// str

// FormatStrSpec implements str.__format__.
func FormatStrSpec(s string, spec string) string {
	return FormatStrSpecCtx(nil, s, spec)
}

func FormatStrSpecCtx(ctx *ExecContext, s string, spec string) string {
	if spec == "" {
		return s
	}
	fs, ok := parseFormatSpecCtx(ctx, spec, "str", '<')
	if !ok {
		return ""
	}
	if fs.typ != 0 && fs.typ != 's' {
		unknownFormatCodeCtx(ctx, rune(fs.typ), "str")
		return ""
	}
	if fs.groupAfter {
		fmtValueErrorCtx(ctx, "Cannot specify '"+string(fs.group)+"' with 's'.")
		return ""
	}
	if fs.sign == ' ' {
		fmtValueErrorCtx(ctx, "Space not allowed in string format specifier")
		return ""
	}
	if fs.sign != 0 {
		fmtValueErrorCtx(ctx, "Sign not allowed in string format specifier")
		return ""
	}
	if fs.z {
		fmtValueErrorCtx(ctx, "Negative zero coercion (z) not allowed in string format specifier")
		return ""
	}
	if fs.alt {
		fmtValueErrorCtx(ctx, "Alternate form (#) not allowed in string format specifier")
		return ""
	}
	if fs.align == '=' {
		fmtValueErrorCtx(ctx, "'=' alignment not allowed in string format specifier")
		return ""
	}
	if fs.prec >= 0 && utf8.RuneCountInString(s) > fs.prec {
		r := []rune(s)
		s = string(r[:fs.prec])
	}
	return padFormatted(s, fs)
}

// ---------------------------------------------------------------------------
// int

func intTypeDefaultGroup(typ byte) int {
	switch typ {
	case 'b', 'o', 'x', 'X':
		return 4
	}
	return 3
}

// FormatIntSpec implements int.__format__ for an arbitrary-precision integer.
func FormatIntSpec(n *big.Int, spec string) string {
	return formatIntSpecT(nil, n, spec, "int")
}

func FormatIntSpecCtx(ctx *ExecContext, n *big.Int, spec string) string {
	return formatIntSpecT(ctx, n, spec, "int")
}

// FormatBoolSpec implements the integer formatting of bool values.
func FormatBoolSpec(b bool, spec string) string {
	return FormatBoolSpecCtx(nil, b, spec)
}

func FormatBoolSpecCtx(ctx *ExecContext, b bool, spec string) string {
	n := int64(0)
	if b {
		n = 1
	}
	return formatIntSpecT(ctx, big.NewInt(n), spec, "bool")
}

func formatIntSpecT(ctx *ExecContext, n *big.Int, spec, tname string) string {
	fs, ok := parseFormatSpecCtx(ctx, spec, tname, '>')
	if !ok {
		return ""
	}
	typ := fs.typ
	switch typ {
	case 0, 'd', 'b', 'o', 'x', 'X', 'c', 'n':
	case 'e', 'E', 'f', 'F', 'g', 'G', '%':
		f, _ := new(big.Float).SetInt(n).Float64()
		if math.IsInf(f, 0) {
			RaiseOverflowError(ctx, "int too large to convert to float")
			return ""
		}
		return formatFloatSpec(ctx, f, fs, tname)
	default:
		unknownFormatCodeCtx(ctx, rune(typ), tname)
		return ""
	}
	if fs.prec >= 0 {
		fmtValueErrorCtx(ctx, "Precision not allowed in integer format specifier")
		return ""
	}
	if fs.z {
		fmtValueErrorCtx(ctx, "Negative zero coercion (z) not allowed in integer format specifier")
		return ""
	}
	if typ == 'c' {
		if fs.sign != 0 {
			fmtValueErrorCtx(ctx, "Sign not allowed with integer format specifier 'c'")
			return ""
		}
		if fs.alt {
			fmtValueErrorCtx(ctx, "Alternate form (#) not allowed with integer format specifier 'c'")
			return ""
		}
		if !n.IsInt64() {
			RaiseOverflowError(ctx, "Python int too large to convert to C long")
			return ""
		}
		if n.Int64() < 0 || n.Int64() > 0x10ffff {
			RaiseOverflowError(ctx, "%c arg not in range(0x110000)")
			return ""
		}
		return padFormatted(string(rune(n.Int64())), fs)
	}
	base := 10
	prefix := ""
	switch typ {
	case 'b':
		base = 2
		if fs.alt {
			prefix = "0b"
		}
	case 'o':
		base = 8
		if fs.alt {
			prefix = "0o"
		}
	case 'x':
		base = 16
		if fs.alt {
			prefix = "0x"
		}
	case 'X':
		base = 16
		if fs.alt {
			prefix = "0X"
		}
	}
	neg := n.Sign() < 0
	digits := new(big.Int).Abs(n).Text(base)
	if typ == 'X' {
		digits = strings.ToUpper(digits)
	}
	return assembleNumber(fs, neg, prefix, digits, "", intTypeDefaultGroup(typ))
}

// FormatInt64Spec is FormatIntSpec for machine integers.
func FormatInt64Spec(n int64, spec string) string {
	return FormatInt64SpecCtx(nil, n, spec)
}

func FormatInt64SpecCtx(ctx *ExecContext, n int64, spec string) string {
	if spec == "" || spec == "d" {
		return strconv.FormatInt(n, 10)
	}
	return FormatIntSpecCtx(ctx, big.NewInt(n), spec)
}

// ---------------------------------------------------------------------------
// float

// FloatRepr returns Python's repr() of a float.
func FloatRepr(f float64) string {
	if math.IsNaN(f) {
		return "nan"
	}
	if math.IsInf(f, 1) {
		return "inf"
	}
	if math.IsInf(f, -1) {
		return "-inf"
	}
	neg := math.Signbit(f)
	body := floatBody(math.Abs(f), 'r', -1, false, true)
	if neg {
		return "-" + body
	}
	return body
}

// AppendFloatRepr appends Python's repr() of a finite float to dst without heap allocations.
func AppendFloatRepr(dst []byte, f float64) []byte {
	if math.IsNaN(f) {
		return append(dst, "nan"...)
	}
	if math.IsInf(f, 1) {
		return append(dst, "inf"...)
	}
	if math.IsInf(f, -1) {
		return append(dst, "-inf"...)
	}
	if math.Signbit(f) {
		dst = append(dst, '-')
		f = -f
	}
	if f == 0 {
		return append(dst, "0.0"...)
	}
	var buf [32]byte
	s := strconv.AppendFloat(buf[:0], f, 'e', -1, 64)
	ei := bytes.IndexByte(s, 'e')
	exp := 0
	expNeg := false
	for i := ei + 1; i < len(s); i++ {
		c := s[i]
		if c == '-' {
			expNeg = true
		} else if c != '+' {
			exp = exp*10 + int(c-'0')
		}
	}
	if expNeg {
		exp = -exp
	}
	decpt := exp + 1
	// Strip '.' from s[:ei] in place to get pure digits.
	var digsBuf [24]byte
	dn := 0
	for i := range ei {
		if s[i] != '.' {
			digsBuf[dn] = s[i]
			dn++
		}
	}
	for dn > 1 && digsBuf[dn-1] == '0' {
		dn--
	}
	digs := digsBuf[:dn]
	if decpt <= -4 || decpt > 16 {
		dst = append(dst, digs[0])
		if len(digs) > 1 {
			dst = append(dst, '.')
			dst = append(dst, digs[1:]...)
		}
		e := decpt - 1
		dst = append(dst, 'e')
		if e < 0 {
			dst = append(dst, '-')
			e = -e
		} else {
			dst = append(dst, '+')
		}
		if e < 10 {
			dst = append(dst, '0')
		}
		return strconv.AppendInt(dst, int64(e), 10)
	}
	switch {
	case decpt <= 0:
		dst = append(dst, "0."...)
		for range -decpt {
			dst = append(dst, '0')
		}
		return append(dst, digs...)
	case decpt >= len(digs):
		dst = append(dst, digs...)
		for range decpt - len(digs) {
			dst = append(dst, '0')
		}
		return append(dst, ".0"...)
	default:
		dst = append(dst, digs[:decpt]...)
		dst = append(dst, '.')
		return append(dst, digs[decpt:]...)
	}
}

// splitE returns the significant digits and decimal point position
// (value = 0.DIGITS * 10^decpt) of f formatted with the given 'e' precision
// (-1 for the shortest round-tripping representation).
func splitE(f float64, prec int) (digs string, decpt int) {
	s := strconv.FormatFloat(f, 'e', prec, 64)
	ei := strings.IndexByte(s, 'e')
	exp, _ := strconv.Atoi(s[ei+1:])
	mant := strings.Replace(s[:ei], ".", "", 1)
	return mant, exp + 1
}

func expSuffix(decpt int, upper bool) string {
	e := decpt - 1
	sign := "+"
	if e < 0 {
		sign = "-"
		e = -e
	}
	d := strconv.Itoa(e)
	if len(d) < 2 {
		d = "0" + d
	}
	if upper {
		return "E" + sign + d
	}
	return "e" + sign + d
}

// floatBody formats a finite, non-negative float. typ is one of
// e f g r (repr) and addDot0 requests CPython's "at least one decimal".
func floatBody(f float64, typ byte, prec int, alt bool, addDot0 bool) string {
	switch typ {
	case 'e':
		if prec < 0 {
			prec = 6
		}
		s := strconv.FormatFloat(f, 'e', prec, 64)
		if alt && prec == 0 {
			s = strings.Replace(s, "e", ".e", 1)
		}
		return s
	case 'f':
		if prec < 0 {
			prec = 6
		}
		s := strconv.FormatFloat(f, 'f', prec, 64)
		if alt && prec == 0 {
			s += "."
		}
		return s
	}
	// 'g' and 'r'
	var digs string
	var decpt int
	var useExp bool
	if typ == 'r' {
		digs, decpt = splitE(f, -1)
		if f == 0 {
			digs, decpt = "0", 1
		}
		useExp = decpt <= -4 || decpt > 16
	} else {
		if prec < 0 {
			prec = 6
		}
		if prec == 0 {
			prec = 1
		}
		threshold := prec
		if addDot0 {
			threshold = prec - 1
		}
		digs, decpt = splitE(f, prec-1)
		if f == 0 {
			decpt = 1
		}
		useExp = decpt <= -4 || decpt > threshold
		if alt && len(digs) < prec {
			digs += strings.Repeat("0", prec-len(digs))
		}
	}
	strip := typ == 'r' || !alt
	if strip {
		digs = strings.TrimRight(digs, "0")
		if digs == "" {
			digs = "0"
		}
	}
	if useExp {
		m := digs[:1]
		if len(digs) > 1 {
			m += "." + digs[1:]
		} else if alt {
			m += "."
		}
		return m + expSuffix(decpt, false)
	}
	var s string
	switch {
	case decpt <= 0:
		s = "0." + strings.Repeat("0", -decpt) + digs
	case decpt >= len(digs):
		s = digs + strings.Repeat("0", decpt-len(digs))
		if addDot0 {
			s += ".0"
		} else if alt {
			s += "."
		}
	default:
		s = digs[:decpt] + "." + digs[decpt:]
	}
	return s
}

func hasNonZeroDigit(body string) bool {
	for i := range len(body) {
		c := body[i]
		if c == 'e' || c == 'E' {
			break
		}
		if c >= '1' && c <= '9' {
			return true
		}
	}
	return false
}

// FormatFloatSpec implements float.__format__.
func FormatFloatSpec(f float64, spec string) string {
	return FormatFloatSpecCtx(nil, f, spec)
}

func FormatFloatSpecCtx(ctx *ExecContext, f float64, spec string) string {
	if spec == "" {
		return FloatRepr(f)
	}
	fs, ok := parseFormatSpecCtx(ctx, spec, "float", '>')
	if !ok {
		return ""
	}
	return formatFloatSpec(ctx, f, fs, "float")
}

// formatFloatParts renders f per fs and returns (negative, integer digits, tail)
// ready for assembleNumber.
func formatFloatParts(f float64, fs fmtSpec, noDot0 bool) (neg bool, digits, tail string) {
	typ := fs.typ
	upper := typ == 'E' || typ == 'F' || typ == 'G'
	neg = math.Signbit(f) && !math.IsNaN(f)
	af := math.Abs(f)
	if math.IsInf(af, 0) || math.IsNaN(af) {
		s := "inf"
		if math.IsNaN(af) {
			s = "nan"
		}
		if upper {
			s = strings.ToUpper(s)
		}
		if typ == '%' {
			s += "%"
		}
		return neg, "", s
	}
	prec := fs.prec
	if typ == '%' {
		af *= 100
	}
	var body string
	switch typ {
	case 'e', 'E':
		body = floatBody(af, 'e', prec, fs.alt, false)
	case 'f', 'F', '%':
		body = floatBody(af, 'f', prec, fs.alt, false)
	case 'g', 'G', 'n':
		body = floatBody(af, 'g', prec, fs.alt, false)
	default: // type omitted
		if prec < 0 {
			body = floatBody(af, 'r', -1, fs.alt, !noDot0)
		} else {
			body = floatBody(af, 'g', prec, fs.alt, !noDot0)
		}
	}
	if upper {
		body = strings.ToUpper(body)
	}
	if typ == '%' {
		body += "%"
	}
	if neg && fs.z && !hasNonZeroDigit(body) {
		neg = false
	}
	i := 0
	for i < len(body) && body[i] >= '0' && body[i] <= '9' {
		i++
	}
	return neg, body[:i], body[i:]
}

func formatFloatSpec(ctx *ExecContext, f float64, fs fmtSpec, tname string) string {
	switch fs.typ {
	case 0, 'e', 'E', 'f', 'F', 'g', 'G', '%', 'n':
	default:
		unknownFormatCodeCtx(ctx, rune(fs.typ), tname)
		return ""
	}
	if fs.typ == 'n' && fs.groupAfter {
		fmtValueErrorCtx(ctx, "Cannot specify '"+string(fs.group)+"' with 'n'.")
		return ""
	}
	neg, digits, tail := formatFloatParts(f, fs, false)
	if fs.align == '=' && fs.fill == '0' && digits == "" {
		// inf/nan have no digits to zero-extend; pad like CPython does.
		return assembleNumber(fs, neg, "", "", tail, 3)
	}
	return assembleNumber(fs, neg, "", digits, tail, 3)
}

// ---------------------------------------------------------------------------
// complex

// FormatComplexSpec implements complex.__format__.
func FormatComplexSpec(re, im float64, spec string) string {
	return FormatComplexSpecCtx(nil, re, im, spec)
}

func FormatComplexSpecCtx(ctx *ExecContext, re, im float64, spec string) string {
	if spec == "" {
		return ComplexRepr(re, im)
	}
	fs, ok := parseFormatSpecCtx(ctx, spec, "complex", '>')
	if !ok {
		return ""
	}
	switch fs.typ {
	case 0, 'e', 'E', 'f', 'F', 'g', 'G', 'n':
	default:
		unknownFormatCodeCtx(ctx, rune(fs.typ), "complex")
		return ""
	}
	if fs.zero || (fs.fillSet && fs.fill == '0' && fs.align == '=') {
		fmtValueErrorCtx(ctx, "Zero padding is not allowed in complex format specifier")
		return ""
	}
	if fs.align == '=' {
		fmtValueErrorCtx(ctx, "'=' alignment flag is not allowed in complex format specifier")
		return ""
	}
	// Render each part without width/alignment.
	part := fs
	part.width = -1
	part.align = '>'
	part.fill = ' '
	part.zero = false
	skipRe := false
	if fs.typ == 0 {
		// Like repr: "1+2j" without parentheses when a precision/type is set.
		if re == 0 && !math.Signbit(re) {
			skipRe = true
		}
		if fs.prec < 0 {
			part.typ = 0
		}
	}
	var sb strings.Builder
	paren := fs.typ == 0 && !skipRe
	if !skipRe {
		neg, d, t := formatFloatParts(re, part, true)
		sb.WriteString(assembleNumber(part, neg, "", d, t, 3))
	}
	imPart := part
	if !skipRe {
		imPart.sign = '+'
	}
	neg, d, t := formatFloatParts(im, imPart, true)
	sb.WriteString(assembleNumber(imPart, neg, "", d, t, 3))
	sb.WriteByte('j')
	s := sb.String()
	if paren {
		s = "(" + s + ")"
	}
	return padFormatted(s, fs)
}

// ComplexRepr is Python's repr(complex).
func ComplexRepr(re, im float64) string {
	imStr := FloatRepr(im)
	if !math.IsNaN(im) && !math.IsInf(im, 0) {
		imStr = strings.TrimSuffix(imStr, ".0")
	}
	if re == 0 && !math.Signbit(re) {
		return imStr + "j"
	}
	reStr := strings.TrimSuffix(FloatRepr(re), ".0")
	if !strings.HasPrefix(imStr, "-") {
		imStr = "+" + imStr
	}
	return "(" + reStr + imStr + "j)"
}
