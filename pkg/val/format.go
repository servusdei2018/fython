package val

import (
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

func fmtValueError(msg string) { panic("ValueError: " + msg) }

// parseFormatSpec parses spec. tname is the Python type name used in errors.
// defaultAlign is '<' for strings and '>' for numbers.
func parseFormatSpec(spec, tname string, defaultAlign byte) fmtSpec {
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
	readInt := func(what string) int {
		start := i
		v := 0
		for i < n && rs[i] >= '0' && rs[i] <= '9' {
			v = v*10 + int(rs[i]-'0')
			if v > math.MaxInt32 {
				fmtValueError("Too many decimal digits in format string")
			}
			i++
		}
		if i == start {
			return -1
		}
		return v
	}
	if w := readInt("width"); w >= 0 {
		fs.width = w
	}
	if i < n && (rs[i] == ',' || rs[i] == '_') {
		fs.group = byte(rs[i])
		fs.groupAfter = true
		i++
		if i < n && (rs[i] == ',' || rs[i] == '_') {
			if byte(rs[i]) != fs.group {
				fmtValueError("Cannot specify both ',' and '_'.")
			}
			fmtValueError("Cannot specify '" + string(rs[i]) + "' more than once")
		}
	}
	if i < n && rs[i] == '.' {
		i++
		p := readInt("precision")
		if p < 0 {
			fmtValueError("Format specifier missing precision")
		}
		fs.prec = p
	}
	if i < n && (rs[i] == ',' || rs[i] == '_') {
		if fs.groupAfter {
			fmtValueError("Cannot specify both ',' and '_'.")
		}
		fmtValueError("Invalid format specifier '" + spec + "' for object of type '" + tname + "'")
	}
	if n-i > 1 {
		fmtValueError("Invalid format specifier '" + spec + "' for object of type '" + tname + "'")
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
				fmtValueError("Cannot specify ',' with '" + string(fs.typ) + "'.")
			}
		default:
			if fs.typ > 32 && fs.typ < 128 {
				fmtValueError("Cannot specify '" + string(fs.group) + "' with '" + string(fs.typ) + "'.")
			}
			fmtValueError("Cannot specify '" + string(fs.group) + "' with '\\x" + strconv.FormatInt(int64(fs.typ), 16) + "'.")
		}
	}
	if fs.align == 0 {
		fs.align = defaultAlign
	}
	return fs
}

func unknownFormatCode(fs fmtSpec, spec rune, tname string) {
	c := string(spec)
	if spec > 0x7e || spec < 0x20 {
		fmtValueError("Unknown format code '\\x" + strconv.FormatInt(int64(spec), 16) + "' for object of type '" + tname + "'")
	}
	fmtValueError("Unknown format code '" + c + "' for object of type '" + tname + "'")
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
	if spec == "" {
		return s
	}
	fs := parseFormatSpec(spec, "str", '<')
	if fs.typ != 0 && fs.typ != 's' {
		unknownFormatCode(fs, rune(fs.typ), "str")
	}
	if fs.groupAfter {
		fmtValueError("Cannot specify '" + string(fs.group) + "' with 's'.")
	}
	if fs.sign == ' ' {
		fmtValueError("Space not allowed in string format specifier")
	}
	if fs.sign != 0 {
		fmtValueError("Sign not allowed in string format specifier")
	}
	if fs.z {
		fmtValueError("Negative zero coercion (z) not allowed in string format specifier")
	}
	if fs.alt {
		fmtValueError("Alternate form (#) not allowed in string format specifier")
	}
	if fs.align == '=' {
		fmtValueError("'=' alignment not allowed in string format specifier")
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
	return formatIntSpecT(n, spec, "int")
}

// FormatBoolSpec implements the integer formatting of bool values.
func FormatBoolSpec(b bool, spec string) string {
	n := int64(0)
	if b {
		n = 1
	}
	return formatIntSpecT(big.NewInt(n), spec, "bool")
}

func formatIntSpecT(n *big.Int, spec, tname string) string {
	fs := parseFormatSpec(spec, tname, '>')
	typ := fs.typ
	switch typ {
	case 0, 'd', 'b', 'o', 'x', 'X', 'c', 'n':
	case 'e', 'E', 'f', 'F', 'g', 'G', '%':
		f, _ := new(big.Float).SetInt(n).Float64()
		if math.IsInf(f, 0) {
			panic("OverflowError: int too large to convert to float")
		}
		return formatFloatSpec(f, fs, tname)
	default:
		unknownFormatCode(fs, rune(typ), tname)
	}
	if fs.prec >= 0 {
		fmtValueError("Precision not allowed in integer format specifier")
	}
	if fs.z {
		fmtValueError("Negative zero coercion (z) not allowed in integer format specifier")
	}
	if typ == 'c' {
		if fs.sign != 0 {
			fmtValueError("Sign not allowed with integer format specifier 'c'")
		}
		if fs.alt {
			fmtValueError("Alternate form (#) not allowed with integer format specifier 'c'")
		}
		if !n.IsInt64() {
			panic("OverflowError: Python int too large to convert to C long")
		}
		if n.Int64() < 0 || n.Int64() > 0x10ffff {
			panic("OverflowError: %c arg not in range(0x110000)")
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
	if spec == "" || spec == "d" {
		return strconv.FormatInt(n, 10)
	}
	return FormatIntSpec(big.NewInt(n), spec)
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
	if spec == "" {
		return FloatRepr(f)
	}
	fs := parseFormatSpec(spec, "float", '>')
	return formatFloatSpec(f, fs, "float")
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

func formatFloatSpec(f float64, fs fmtSpec, tname string) string {
	switch fs.typ {
	case 0, 'e', 'E', 'f', 'F', 'g', 'G', '%', 'n':
	default:
		unknownFormatCode(fs, rune(fs.typ), tname)
	}
	if fs.typ == 'n' && fs.groupAfter {
		fmtValueError("Cannot specify '" + string(fs.group) + "' with 'n'.")
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
	if spec == "" {
		return ComplexRepr(re, im)
	}
	fs := parseFormatSpec(spec, "complex", '>')
	switch fs.typ {
	case 0, 'e', 'E', 'f', 'F', 'g', 'G', 'n':
	default:
		unknownFormatCode(fs, rune(fs.typ), "complex")
	}
	if fs.zero || (fs.fillSet && fs.fill == '0' && fs.align == '=') {
		fmtValueError("Zero padding is not allowed in complex format specifier")
	}
	if fs.align == '=' {
		fmtValueError("'=' alignment flag is not allowed in complex format specifier")
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
