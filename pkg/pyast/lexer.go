package pyast

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SyntaxError is raised (via panic inside the parser, returned to callers) for
// malformed source.
type SyntaxError struct {
	Kind      string // SyntaxError, IndentationError, TabError
	Msg       string
	Lineno    int // 1-based
	Offset    int // 1-based character column
	EndLineno int
	EndOffset int
	Text      string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("%s: %s (line %d)", e.Kind, e.Msg, e.Lineno)
}

// Source holds the program text and a line table.
type Source struct {
	Src   []byte
	lines []int // byte offset of each line start
}

// NewSource indexes src.
func NewSource(src []byte) *Source {
	s := &Source{Src: src, lines: []int{0}}
	for i := 0; i < len(src); i++ {
		switch src[i] {
		case '\n':
			s.lines = append(s.lines, i+1)
		case '\r':
			if i+1 < len(src) && src[i+1] == '\n' {
				i++
			}
			s.lines = append(s.lines, i+1)
		}
	}
	return s
}

// Pos converts a byte offset to a 1-based line and 0-based byte column.
func (s *Source) Pos(off int) (line, col int) {
	i := max(0, sort.Search(len(s.lines), func(i int) bool { return s.lines[i] > off })-1)
	return i + 1, off - s.lines[i]
}

// LineText returns the text of the 1-based line (without newline).
func (s *Source) LineText(line int) string {
	if line < 1 || line > len(s.lines) {
		return ""
	}
	a := s.lines[line-1]
	b := len(s.Src)
	if line < len(s.lines) {
		b = s.lines[line]
	}
	return strings.TrimRight(string(s.Src[a:b]), "\r\n")
}

func (s *Source) syntaxErrAt(kind, msg string, off int) *SyntaxError {
	line, col := s.Pos(off)
	text := s.LineText(line)
	if col > len(text) {
		col = len(text)
	}
	char := utf8.RuneCountInString(text[:col]) + 1
	return &SyntaxError{Kind: kind, Msg: msg, Lineno: line, Offset: char, EndLineno: line, EndOffset: char + 1, Text: text}
}

// TokKind is a token class.
type TokKind uint8

const (
	TEOF TokKind = iota
	TName
	TNumber
	TString
	TOp
	TNewline
	TIndent
	TDedent
)

// Token is a lexical token; Start/End are byte offsets.
type Token struct {
	Kind       TokKind
	Start, End int
}

// Lexer produces tokens on demand.
type Lexer struct {
	S       *Source
	pos     int
	depth   int // bracket nesting; >0 suppresses NEWLINE/INDENT
	indents []int
	atBOL   bool
	pending []Token
	done    bool
	emitted bool // any non-layout token emitted on current logical line
	// subExpr lexers (f-string replacement fields) never emit layout tokens.
	sub bool
}

// NewLexer starts lexing at the beginning of s.
func NewLexer(s *Source) *Lexer {
	return &Lexer{S: s, indents: []int{0}, atBOL: true}
}

// NewSubLexer lexes an expression embedded at offset pos (f-string field).
func NewSubLexer(s *Source, pos int) *Lexer {
	return &Lexer{S: s, pos: pos, depth: 1, sub: true, indents: []int{0}}
}

func (l *Lexer) errAt(kind, msg string, off int) {
	panic(l.S.syntaxErrAt(kind, msg, off))
}

func isIdentStart(c rune) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= 0x80 && (unicode.IsLetter(c) || unicode.Is(unicode.Nl, c) || unicode.Is(unicode.Other_ID_Start, c)))
}

func isIdentCont(c rune) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9') || (c >= 0x80 && (unicode.IsDigit(c) || unicode.Is(unicode.Mn, c) || unicode.Is(unicode.Mc, c) || unicode.Is(unicode.Nd, c) || unicode.Is(unicode.Pc, c) || unicode.Is(unicode.Other_ID_Continue, c)))
}

// Next returns the next token.
func (l *Lexer) Next() Token {
	if len(l.pending) > 0 {
		t := l.pending[0]
		l.pending = l.pending[1:]
		return t
	}
	src := l.S.Src
	for {
		if l.atBOL && l.depth == 0 && !l.sub {
			l.atBOL = false
			if t, ok := l.lineStart(); ok {
				return t
			}
			if len(l.pending) > 0 {
				t := l.pending[0]
				l.pending = l.pending[1:]
				return t
			}
		}
		// skip whitespace
		for l.pos < len(src) {
			c := src[l.pos]
			if c == ' ' || c == '\t' || c == '\f' {
				l.pos++
				continue
			}
			if c == '\\' {
				if l.pos+1 < len(src) && src[l.pos+1] == '\n' {
					l.pos += 2
					continue
				}
				if l.pos+2 < len(src) && src[l.pos+1] == '\r' && src[l.pos+2] == '\n' {
					l.pos += 3
					continue
				}
				if l.pos+1 >= len(src) {
					l.errAt("SyntaxError", "unexpected EOF while parsing", l.pos)
				}
			}
			break
		}
		if l.pos >= len(src) {
			return l.eof()
		}
		c := src[l.pos]
		switch c {
		case '#':
			for l.pos < len(src) && src[l.pos] != '\n' && src[l.pos] != '\r' {
				l.pos++
			}
			continue
		case '\n', '\r':
			start := l.pos
			l.pos++
			if c == '\r' && l.pos < len(src) && src[l.pos] == '\n' {
				l.pos++
			}
			if l.depth > 0 || l.sub {
				continue
			}
			l.atBOL = true
			if l.emitted {
				l.emitted = false
				return Token{Kind: TNewline, Start: start, End: l.pos}
			}
			continue
		}
		l.emitted = true
		start := l.pos
		r, size := rune(c), 1
		if c >= 0x80 {
			r, size = utf8.DecodeRune(src[l.pos:])
		}
		switch {
		case isIdentStart(r):
			// possible string prefix
			e := l.pos + size
			for e < len(src) {
				r2, s2 := rune(src[e]), 1
				if r2 >= 0x80 {
					r2, s2 = utf8.DecodeRune(src[e:])
				}
				if !isIdentCont(r2) {
					break
				}
				e += s2
			}
			if e < len(src) && (src[e] == '"' || src[e] == '\'') && e-l.pos <= 2 && validPrefix(src[l.pos:e]) {
				end := l.scanString(l.pos, e)
				l.pos = end
				return Token{Kind: TString, Start: start, End: end}
			}
			l.pos = e
			return Token{Kind: TName, Start: start, End: e}
		case c >= '0' && c <= '9', c == '.' && l.pos+1 < len(src) && src[l.pos+1] >= '0' && src[l.pos+1] <= '9':
			end := l.scanNumber()
			l.pos = end
			return Token{Kind: TNumber, Start: start, End: end}
		case c == '"' || c == '\'':
			end := l.scanString(l.pos, l.pos)
			l.pos = end
			return Token{Kind: TString, Start: start, End: end}
		}
		// operators
		n := l.opLen()
		if n == 0 {
			l.errAt("SyntaxError", fmt.Sprintf("invalid character %q (U+%04X)", r, r), l.pos)
		}
		op := string(src[l.pos : l.pos+n])
		switch op {
		case "(", "[", "{":
			l.depth++
		case ")", "]", "}":
			if l.depth > 0 {
				l.depth--
			} else if !l.sub {
				l.errAt("SyntaxError", fmt.Sprintf("unmatched '%s'", op), l.pos)
			}
		}
		l.pos += n
		return Token{Kind: TOp, Start: start, End: l.pos}
	}
}

func validPrefix(p []byte) bool {
	switch strings.ToLower(string(p)) {
	case "r", "u", "b", "br", "rb", "f", "fr", "rf", "t", "tr", "rt":
		return true
	}
	return false
}

func (l *Lexer) eof() Token {
	if l.done {
		return Token{Kind: TEOF, Start: len(l.S.Src), End: len(l.S.Src)}
	}
	l.done = true
	n := len(l.S.Src)
	if l.sub {
		return Token{Kind: TEOF, Start: n, End: n}
	}
	if l.depth > 0 {
		l.errAt("SyntaxError", "unexpected EOF while parsing", n)
	}
	if l.emitted {
		l.emitted = false
		l.pending = append(l.pending, Token{Kind: TNewline, Start: n, End: n})
	}
	for len(l.indents) > 1 {
		l.indents = l.indents[:len(l.indents)-1]
		l.pending = append(l.pending, Token{Kind: TDedent, Start: n, End: n})
	}
	l.pending = append(l.pending, Token{Kind: TEOF, Start: n, End: n})
	t := l.pending[0]
	l.pending = l.pending[1:]
	return t
}

// lineStart measures indentation at the start of a logical line and emits
// INDENT/DEDENT tokens as needed. Blank and comment-only lines are skipped.
func (l *Lexer) lineStart() (Token, bool) {
	src := l.S.Src
	for {
		col := 0
		p := l.pos
	ws:
		for p < len(src) {
			switch src[p] {
			case ' ':
				col++
			case '\t':
				col = (col/8 + 1) * 8
			case '\f':
				col = 0
			default:
				break ws
			}
			p++
		}
		if p >= len(src) {
			l.pos = p
			return Token{}, false
		}
		switch src[p] {
		case '#':
			for p < len(src) && src[p] != '\n' && src[p] != '\r' {
				p++
			}
			if p < len(src) {
				if src[p] == '\r' && p+1 < len(src) && src[p+1] == '\n' {
					p++
				}
				p++
			}
			l.pos = p
			continue
		case '\n', '\r':
			if src[p] == '\r' && p+1 < len(src) && src[p+1] == '\n' {
				p++
			}
			p++
			l.pos = p
			continue
		case '\\':
			// a continuation line right at BOL: treat as ordinary indentation start
		}
		start := l.pos
		l.pos = p
		top := l.indents[len(l.indents)-1]
		switch {
		case col > top:
			l.indents = append(l.indents, col)
			return Token{Kind: TIndent, Start: start, End: p}, true
		case col < top:
			for len(l.indents) > 1 && col < l.indents[len(l.indents)-1] {
				l.indents = l.indents[:len(l.indents)-1]
				l.pending = append(l.pending, Token{Kind: TDedent, Start: p, End: p})
			}
			if l.indents[len(l.indents)-1] != col {
				l.pending = nil
				l.errAt("IndentationError", "unindent does not match any outer indentation level", p)
			}
			t := l.pending[0]
			l.pending = l.pending[1:]
			return t, true
		}
		return Token{}, false
	}
}

var ops3 = []string{"**=", "//=", ">>=", "<<=", "..."}
var ops2 = []string{"**", "//", ">>", "<<", "<=", ">=", "==", "!=", "->", "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "@=", ":="}

func (l *Lexer) opLen() int {
	src := l.S.Src[l.pos:]
	if len(src) >= 3 {
		s := string(src[:3])
		if slices.Contains(ops3, s) {
			return 3
		}
	}
	if len(src) >= 2 {
		s := string(src[:2])
		if slices.Contains(ops2, s) {
			return 2
		}
	}
	switch src[0] {
	case '+', '-', '*', '/', '%', '@', '&', '|', '^', '~', '<', '>', '(', ')', '[', ']', '{', '}', ',', ':', ';', '.', '=':
		return 1
	case '!':
		if l.sub {
			return 1
		}
	}
	return 0
}

func (l *Lexer) scanNumber() int {
	src := l.S.Src
	p := l.pos
	digits := func(ok func(byte) bool) {
		for p < len(src) && (ok(src[p]) || (src[p] == '_' && p+1 < len(src) && ok(src[p+1]))) {
			p++
		}
	}
	dec := func(c byte) bool { return c >= '0' && c <= '9' }
	if src[p] == '0' && p+1 < len(src) {
		switch src[p+1] {
		case 'x', 'X':
			p += 2
			digits(func(c byte) bool { return hexVal(c) >= 0 })
			return p
		case 'o', 'O':
			p += 2
			digits(func(c byte) bool { return c >= '0' && c <= '7' })
			return p
		case 'b', 'B':
			p += 2
			digits(func(c byte) bool { return c == '0' || c == '1' })
			return p
		}
	}
	digits(dec)
	if p < len(src) && src[p] == '.' {
		p++
		digits(dec)
	}
	if p < len(src) && (src[p] == 'e' || src[p] == 'E') {
		q := p + 1
		if q < len(src) && (src[q] == '+' || src[q] == '-') {
			q++
		}
		if q < len(src) && dec(src[q]) {
			p = q
			digits(dec)
		}
	}
	if p < len(src) && (src[p] == 'j' || src[p] == 'J') {
		p++
	}
	return p
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// scanString scans a string literal whose prefix starts at pstart and whose
// opening quote is at q; returns the end offset.
func (l *Lexer) scanString(pstart, q int) int {
	src := l.S.Src
	quote := src[q]
	raw, interp := false, false
	for i := pstart; i < q; i++ {
		switch src[i] | 0x20 {
		case 'r':
			raw = true
		case 'f', 't':
			interp = true
		}
	}
	triple := q+2 < len(src) && src[q+1] == quote && src[q+2] == quote
	p := q + 1
	if triple {
		p = q + 3
	}
	if interp {
		return l.scanFString(pstart, p, quote, triple, raw)
	}
	for p < len(src) {
		c := src[p]
		switch {
		case c == '\\':
			p += 2
			continue
		case c == quote:
			if !triple {
				return p + 1
			}
			if p+2 < len(src) && src[p+1] == quote && src[p+2] == quote {
				return p + 3
			}
			if p+2 == len(src)-0 && false {
				return p
			}
		case (c == '\n' || c == '\r') && !triple:
			l.errAt("SyntaxError", "unterminated string literal (detected at line "+itoa(l.lineOf(p))+")", pstart)
		}
		p++
	}
	if triple {
		l.errAt("SyntaxError", "unterminated triple-quoted string literal (detected at line "+itoa(l.lineOf(len(src)))+")", pstart)
	}
	l.errAt("SyntaxError", "unterminated string literal (detected at line "+itoa(l.lineOf(len(src)))+")", pstart)
	return p
}

func (l *Lexer) lineOf(off int) int {
	ln, _ := l.S.Pos(off)
	return ln
}

func itoa(i int) string { return strconv.Itoa(i) }

func (l *Lexer) scanFString(pstart, p int, quote byte, triple, raw bool) int {
	src := l.S.Src
	for p < len(src) {
		c := src[p]
		switch {
		case c == '\\':
			if !raw && p+2 < len(src) && src[p+1] == 'N' && src[p+2] == '{' {
				for p < len(src) && src[p] != '}' {
					p++
				}
				p++
				continue
			}
			if p+1 < len(src) && (src[p+1] == '{' || src[p+1] == '}') {
				p++
				continue
			}
			p += 2
			continue
		case c == quote:
			if !triple {
				return p + 1
			}
			if p+2 < len(src) && src[p+1] == quote && src[p+2] == quote {
				return p + 3
			}
		case c == '{':
			if p+1 < len(src) && src[p+1] == '{' {
				p += 2
				continue
			}
			p = l.scanField(p+1, pstart)
			continue
		case (c == '\n' || c == '\r') && !triple:
			l.errAt("SyntaxError", "unterminated f-string literal (detected at line "+itoa(l.lineOf(p))+")", pstart)
		}
		p++
	}
	l.errAt("SyntaxError", "unterminated f-string literal (detected at line "+itoa(l.lineOf(len(src)))+")", pstart)
	return p
}

// scanField scans a replacement field body starting after '{' and returns the
// offset just after the matching '}'.
func (l *Lexer) scanField(p, pstart int) int {
	src := l.S.Src
	depth := 0
	for p < len(src) {
		c := src[p]
		switch {
		case c == '\'' || c == '"':
			p = l.scanString(p, p)
			continue
		case c < 0x80 && isIdentStart(rune(c)):
			e := p + 1
			for e < len(src) && src[e] < 0x80 && isIdentCont(rune(src[e])) {
				e++
			}
			if e < len(src) && (src[e] == '"' || src[e] == '\'') && e-p <= 2 && validPrefix(src[p:e]) {
				p = l.scanString(p, e)
				continue
			}
			p = e
			continue
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']':
			depth--
		case c == '}':
			if depth <= 0 {
				return p + 1
			}
			depth--
		case c == '#':
			for p < len(src) && src[p] != '\n' {
				p++
			}
			continue
		case c == '!' && p+1 < len(src) && src[p+1] == '=':
			p += 2
			continue
		case c == ':' && depth == 0:
			return l.scanSpec(p+1, pstart)
		}
		p++
	}
	l.errAt("SyntaxError", "f-string: expecting '}'", pstart)
	return p
}

func (l *Lexer) scanSpec(p, pstart int) int {
	src := l.S.Src
	for p < len(src) {
		switch src[p] {
		case '{':
			p = l.scanField(p+1, pstart)
			continue
		case '}':
			return p + 1
		}
		p++
	}
	l.errAt("SyntaxError", "f-string: expecting '}'", pstart)
	return p
}
