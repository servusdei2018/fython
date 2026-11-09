package pyast

import (
	"bytes"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/servusdei2018/fython/pkg/unicodedata"
)

// ---- numbers --------------------------------------------------------------

func (p *Parser) parseNumber(t Token) any {
	s := p.txt(t)
	clean := strings.ReplaceAll(s, "_", "")
	bad := func() {
		p.failAt("SyntaxError", "invalid decimal literal", t.Start)
	}
	if n := len(clean); n > 0 && (clean[n-1] == 'j' || clean[n-1] == 'J') {
		f, err := strconv.ParseFloat(clean[:n-1], 64)
		if err != nil && !isRangeErr(err) {
			bad()
		}
		return complex(0, f)
	}
	if len(clean) > 2 && clean[0] == '0' {
		base := 0
		switch clean[1] {
		case 'x', 'X':
			base = 16
		case 'o', 'O':
			base = 8
		case 'b', 'B':
			base = 2
		}
		if base != 0 {
			b, ok := new(big.Int).SetString(clean[2:], base)
			if !ok {
				p.failAt("SyntaxError", "invalid digit in numeric literal", t.Start)
			}
			return BigOrInt(b)
		}
	}
	if strings.ContainsAny(clean, ".eE") {
		f, err := strconv.ParseFloat(clean, 64)
		if err != nil && !isRangeErr(err) {
			bad()
		}
		return f
	}
	if len(clean) > 1 && clean[0] == '0' && strings.Trim(clean, "0") != "" {
		p.failAt("SyntaxError", "leading zeros in decimal integer literals are not permitted; use an 0o prefix for octal integers", t.Start)
	}
	b, ok := new(big.Int).SetString(clean, 10)
	if !ok {
		bad()
	}
	return BigOrInt(b)
}

func isRangeErr(err error) bool {
	ne, ok := err.(*strconv.NumError)
	return ok && ne.Err == strconv.ErrRange
}

var _ = math.Inf

// ---- escape decoding ------------------------------------------------------

func hexAt(s []byte, i, n int) (int, bool) {
	if i+n > len(s) {
		return 0, false
	}
	v := 0
	for k := range n {
		h := hexVal(s[i+k])
		if h < 0 {
			return 0, false
		}
		v = v<<4 | h
	}
	return v, true
}

// decodeEscapes decodes backslash escapes of a str (isBytes=false) or bytes
// literal body.
func decodeEscapes(s []byte, isBytes bool) []byte {
	if !slices.Contains(s, '\\') {
		return s
	}
	out := make([]byte, 0, len(s))
	put := func(r int) {
		if isBytes {
			out = append(out, byte(r))
			return
		}
		if r >= 0xD800 && r <= 0xDFFF {
			out = append(out, byte(0xE0|(r>>12)), byte(0x80|((r>>6)&0x3F)), byte(0x80|(r&0x3F)))
			return
		}
		out = utf8.AppendRune(out, rune(r))
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			out = append(out, c)
			continue
		}
		i++
		switch e := s[i]; e {
		case '\n':
		case '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
		case '\\', '\'', '"':
			out = append(out, e)
		case 'a':
			out = append(out, 7)
		case 'b':
			out = append(out, 8)
		case 'f':
			out = append(out, 12)
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'v':
			out = append(out, 11)
		case '0', '1', '2', '3', '4', '5', '6', '7':
			v := int(e - '0')
			for k := 0; k < 2 && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '7'; k++ {
				i++
				v = v*8 + int(s[i]-'0')
			}
			if isBytes {
				v &= 0xFF
			}
			put(v)
		case 'x':
			if v, ok := hexAt(s, i+1, 2); ok {
				i += 2
				put(v)
			} else {
				out = append(out, '\\', 'x')
			}
		case 'u', 'U':
			if isBytes {
				out = append(out, '\\', e)
				break
			}
			n := 4
			if e == 'U' {
				n = 8
			}
			v, ok := hexAt(s, i+1, n)
			if !ok || v > 0x10FFFF {
				out = append(out, '\\', e)
				break
			}
			i += n
			if v >= 0xD800 && v < 0xDC00 && i+6 < len(s) && s[i+1] == '\\' && s[i+2] == 'u' {
				if lo, ok := hexAt(s, i+3, 4); ok && lo >= 0xDC00 && lo <= 0xDFFF {
					v = 0x10000 + (v-0xD800)<<10 + (lo - 0xDC00)
					i += 6
				}
			}
			put(v)
		case 'N':
			if isBytes {
				out = append(out, '\\', 'N')
				break
			}
			if i+1 < len(s) && s[i+1] == '{' {
				if end := bytes.IndexByte(s[i+2:], '}'); end >= 0 {
					if r, ok := unicodedata.Lookup(string(s[i+2 : i+2+end])); ok {
						i += 2 + end
						put(int(r))
						break
					}
				}
			}
			out = append(out, '\\', 'N')
		default:
			out = append(out, '\\', e)
		}
	}
	return out
}

// ---- string atoms ---------------------------------------------------------

type strHeader struct {
	raw, bytes, fstr, tmpl, u bool
	bodyStart, bodyEnd        int
}

func (p *Parser) strHeader(t Token) strHeader {
	src := p.S.Src
	var h strHeader
	i := t.Start
	for src[i] != '"' && src[i] != '\'' {
		switch src[i] | 0x20 {
		case 'r':
			h.raw = true
		case 'b':
			h.bytes = true
		case 'f':
			h.fstr = true
		case 't':
			h.tmpl = true
		case 'u':
			h.u = true
		}
		i++
	}
	q := src[i]
	if i+2 < t.End && src[i+1] == q && src[i+2] == q && t.End-i >= 6 {
		h.bodyStart, h.bodyEnd = i+3, t.End-3
	} else {
		h.bodyStart, h.bodyEnd = i+1, t.End-1
	}
	return h
}

func (p *Parser) parseStrings() *Node {
	start := p.cur().Start
	var toks []Token
	for p.cur().Kind == TString {
		toks = append(toks, p.advance())
	}
	var hs []strHeader
	anyBytes, anyStr, anyF, anyT := false, false, false, false
	for _, t := range toks {
		h := p.strHeader(t)
		hs = append(hs, h)
		switch {
		case h.bytes:
			anyBytes = true
		case h.tmpl:
			anyT = true
		case h.fstr:
			anyF = true
			anyStr = true
		default:
			anyStr = true
		}
	}
	if anyBytes && (anyStr || anyT) {
		p.failAt("SyntaxError", "cannot mix bytes and nonbytes literals", start)
	}
	if anyT && (anyF || anyStr) {
		p.failAt("SyntaxError", "cannot mix t-string literals with string or bytes literals", start)
	}
	if anyBytes {
		var out []byte
		for i, t := range toks {
			h := hs[i]
			body := p.S.Src[h.bodyStart:h.bodyEnd]
			for _, c := range body {
				if c >= 0x80 {
					p.failAt("SyntaxError", "bytes can only contain ASCII literal characters", t.Start)
				}
			}
			if h.raw {
				out = append(out, body...)
			} else {
				out = append(out, decodeEscapes(body, true)...)
			}
		}
		if out == nil {
			out = []byte{}
		}
		return p.done(New("Constant", out, nil), start)
	}
	if !anyF && !anyT {
		var sb strings.Builder
		for i := range toks {
			h := hs[i]
			body := p.S.Src[h.bodyStart:h.bodyEnd]
			if h.raw {
				sb.Write(body)
			} else {
				sb.Write(decodeEscapes(body, false))
			}
		}
		var kind any
		if hs[0].u {
			kind = "u"
		}
		return p.done(New("Constant", sb.String(), kind), start)
	}
	// interpolated: assemble parts
	var parts []*Node
	for i, t := range toks {
		h := hs[i]
		if h.fstr || h.tmpl {
			parts = append(parts, p.fstringBody(h.bodyStart, h.bodyEnd, h.raw, h.tmpl, false)...)
			continue
		}
		body := p.S.Src[h.bodyStart:h.bodyEnd]
		text := string(body)
		if !h.raw {
			text = string(decodeEscapes(body, false))
		}
		c := p.doneRange(New("Constant", text, nil), t.Start, t.End)
		parts = append(parts, c)
	}
	values := mergeConstants(parts)
	if anyT {
		return p.done(New("TemplateStr", values), start)
	}
	return p.done(New("JoinedStr", values), start)
}

// mergeConstants joins adjacent string Constants and drops empty ones.
func mergeConstants(parts []*Node) []*Node {
	var out []*Node
	for _, n := range parts {
		if n.Spec.Name == "Constant" {
			s := n.F[0].(string)
			if s == "" {
				continue
			}
			if k := len(out) - 1; k >= 0 && out[k].Spec.Name == "Constant" {
				prev := out[k]
				prev.F[0] = prev.F[0].(string) + s
				prev.Pos[PEndLineno], prev.Pos[PEndCol] = n.Pos[PEndLineno], n.Pos[PEndCol]
				continue
			}
		}
		out = append(out, n)
	}
	return out
}

// fstringBody parses the text between pos and end as f-string content. When
// inSpec is set it stops at the unmatched '}' and its offset is returned via
// p.specEnd.
func (p *Parser) fstringBody(pos, end int, raw, tmpl, inSpec bool) []*Node {
	parts, _ := p.fstringParts(pos, end, raw, tmpl, inSpec)
	return mergeConstants(parts)
}

func (p *Parser) fstringParts(pos, end int, raw, tmpl, inSpec bool) ([]*Node, int) {
	src := p.S.Src
	var parts []*Node
	var lit []byte
	litStart := -1
	litEnd := 0
	flush := func() {
		if litStart < 0 {
			return
		}
		text := string(lit)
		if !raw {
			text = string(decodeEscapes(lit, false))
		}
		parts = append(parts, p.doneRange(New("Constant", text, nil), litStart, litEnd))
		lit = lit[:0]
		litStart = -1
	}
	addLit := func(b []byte, from, to int) {
		if litStart < 0 {
			litStart = from
		}
		lit = append(lit, b...)
		litEnd = to
	}
	for pos < end {
		c := src[pos]
		switch {
		case c == '\\' && !raw && pos+2 < end && src[pos+1] == 'N' && src[pos+2] == '{':
			e := pos
			for e < end && src[e] != '}' {
				e++
			}
			if e < end {
				e++
			}
			addLit(src[pos:e], pos, e)
			pos = e
		case c == '\\' && pos+1 < end:
			if src[pos+1] == '{' || src[pos+1] == '}' {
				addLit(src[pos:pos+1], pos, pos+1)
				pos++
			} else {
				addLit(src[pos:pos+2], pos, pos+2)
				pos += 2
			}
		case c == '{':
			if !inSpec && pos+1 < end && src[pos+1] == '{' {
				addLit([]byte{'{'}, pos, pos+2)
				pos += 2
				continue
			}
			flush()
			nodes, np := p.fstringField(pos, end, raw, tmpl)
			parts = append(parts, nodes...)
			pos = np
		case c == '}':
			if inSpec {
				flush()
				return parts, pos
			}
			if pos+1 < end && src[pos+1] == '}' {
				addLit([]byte{'}'}, pos, pos+2)
				pos += 2
				continue
			}
			p.failAt("SyntaxError", "f-string: single '}' is not allowed", pos)
		default:
			addLit(src[pos:pos+1], pos, pos+1)
			pos++
		}
	}
	flush()
	return parts, pos
}

// fstringField parses one replacement field starting at the '{' at off.
func (p *Parser) fstringField(off, end int, raw, tmpl bool) ([]*Node, int) {
	src := p.S.Src
	sub := &Parser{S: p.S, lx: NewSubLexer(p.S, off+1), lastEnd: off + 1}
	if t := sub.cur(); t.Kind == TEOF || sub.isOp(t, "}") || sub.isOp(t, "!") || sub.isOp(t, ":") {
		p.failAt("SyntaxError", "f-string: valid expression required before '}'", off)
	}
	var value *Node
	if sub.atKw("yield") {
		value = sub.parseYield()
	} else {
		value = sub.parseStarExpressionsF()
	}
	exprEnd := sub.lastEnd
	var out []*Node
	debug := false
	t := sub.cur()
	if sub.isOp(t, "=") {
		debug = true
		sub.advance()
		t = sub.cur()
	}
	// text between expression and '=' retained for Interpolation.str
	strEnd := t.Start
	if debug {
		strEnd = sub.toks[sub.i-1].Start
	}
	debugEnd := t.Start // includes whitespace after '='
	conv := -1
	if sub.isOp(t, "!") {
		sub.advance()
		ct := sub.cur()
		if ct.Kind != TName || ct.End-ct.Start != 1 || !strings.ContainsRune("rsa", rune(src[ct.Start])) {
			p.failAt("SyntaxError", "f-string: invalid conversion character: expected 's', 'r', or 'a'", ct.Start)
		}
		conv = int(src[ct.Start])
		sub.advance()
		t = sub.cur()
	}
	var spec *Node
	var closeAt int
	switch {
	case sub.isOp(t, ":") || sub.isOp(t, ":="):
		colon := t.Start
		specStart := colon + 1
		sparts, sp := p.fstringParts(specStart, end, raw, tmpl, true)
		if sp >= end || src[sp] != '}' {
			p.failAt("SyntaxError", "f-string: expecting '}'", off)
		}
		closeAt = sp
		spec = New("JoinedStr", mergeConstants(sparts))
		p.doneRange(spec, colon, sp)
	case sub.isOp(t, "}"):
		closeAt = t.Start
	default:
		p.failAt("SyntaxError", "f-string: expecting '}'", t.Start)
	}
	_ = exprEnd
	if debug {
		text := string(src[off+1 : debugEnd])
		out = append(out, p.doneRange(New("Constant", text, nil), off+1, debugEnd))
		if conv == -1 && spec == nil {
			conv = 'r'
		}
	}
	var specV any
	if spec != nil {
		specV = spec
	}
	var node *Node
	if tmpl {
		node = New("Interpolation", value, string(src[off+1:strEnd]), conv, specV)
	} else {
		node = New("FormattedValue", value, conv, specV)
	}
	p.doneRange(node, off, closeAt+1)
	out = append(out, node)
	return out, closeAt + 1
}

// parseStarExpressionsF parses the expression of an f-string field. Unlike a
// statement-level expression list it must not treat '=' / '!' / ':' specially;
// those terminate naturally because they cannot continue an expression.
func (p *Parser) parseStarExpressionsF() *Node {
	return p.parseStarExpressions()
}
