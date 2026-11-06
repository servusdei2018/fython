// Package sre implements a Python-compatible backtracking regular expression
// engine: a parser for Python `re` syntax, a compiler to a compact program and
// a backtracking matcher with an explicit stack.
package sre

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/servusdei2018/fython/pkg/unicodedata"
)

// Flag values mirror CPython's re module.
const (
	FlagIgnoreCase = 2
	FlagLocale     = 4
	FlagMultiline  = 8
	FlagDotAll     = 16
	FlagUnicode    = 32
	FlagVerbose    = 64
	FlagDebug      = 128
	FlagASCII      = 256
)

// MaxRepeat is CPython's MAXREPEAT; counts at or above it mean "unbounded".
const MaxRepeat = 4294967295

// Error is a pattern compilation error.
type Error struct {
	Msg string
	Pos int // -1 when unknown
}

func (e *Error) Error() string {
	if e.Pos >= 0 {
		return e.Msg + " at position " + strconv.Itoa(e.Pos)
	}
	return e.Msg
}

type nodeKind uint8

const (
	nEmpty nodeKind = iota
	nLit
	nAny
	nSet
	nCat
	nAlt
	nGroup
	nRepeat
	nLook
	nBackref
	nAtomic
	nCond
	nAt
)

type atKind uint8

const (
	atBOL atKind = iota // ^ (non multiline)
	atBOLMulti
	atEOL // $ (non multiline)
	atEOLMulti
	atBOS // \A
	atEOS // \Z
	atWordB
	atNotWordB
)

type node struct {
	kind       nodeKind
	r          rune
	fold       bool // ignore case
	ascii      bool // ASCII semantics for categories / ignore-case
	dotall     bool
	set        *charSet
	kids       []*node
	idx        int // capture index / backref / cond group
	min, max   int // repeat bounds, max<0 means unbounded
	greedy     bool
	possessive bool
	ahead      bool
	neg        bool
	at         atKind
	paren      bool // result of a parenthesised group (for repeat checks)
	pos        int
}

type catKind uint8

const (
	catNone catKind = iota
	catDigit
	catNotDigit
	catSpace
	catNotSpace
	catWord
	catNotWord
)

type csItem struct {
	lo, hi rune
	cat    catKind
}

type charSet struct {
	items  []csItem
	negate bool
	fold   bool
	ascii  bool
	// ascii lookup table for runes < 128, precomputed at compile time.
	table [128]bool
}

type parser struct {
	src      []rune
	pos      int
	isBytes  bool
	flags    int
	ngroups  int
	names    map[string]int
	groupNm  map[int]string
	open     map[int]bool
	condRefs []condRef
	inLookB  int
	rawName  bool
}

type condRef struct {
	gid int
	pos int
}

func (p *parser) errf(pos int, format string, args ...any) *Error {
	return &Error{Msg: fmt.Sprintf(format, args...), Pos: pos}
}

func (p *parser) more() bool { return p.pos < len(p.src) }

func (p *parser) peek() rune {
	if p.pos < len(p.src) {
		return p.src[p.pos]
	}
	return -1
}

func (p *parser) peekAt(n int) rune {
	if p.pos+n < len(p.src) {
		return p.src[p.pos+n]
	}
	return -1
}

func (p *parser) match(r rune) bool {
	if p.pos < len(p.src) && p.src[p.pos] == r {
		p.pos++
		return true
	}
	return false
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }
func isOct(r rune) bool   { return r >= '0' && r <= '7' }
func isHex(r rune) bool {
	return isDigit(r) || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}
func isASCIILetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func isIdentStart(r rune) bool { return r == '_' || unicode.IsLetter(r) }
func isIdentChar(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) || unicode.Is(unicode.Nl, r)
}

func (p *parser) ignoreCase() bool { return p.flags&FlagIgnoreCase != 0 }
func (p *parser) asciiMode() bool {
	return p.isBytes || p.flags&FlagASCII != 0 || p.flags&FlagLocale != 0
}

// parseResult is the output of parsing.
type parseResult struct {
	root    *node
	flags   int
	ngroups int
	names   map[string]int
	groupNm map[int]string
}

func parse(src []rune, flags int, isBytes bool) (*parseResult, error) {
	p := &parser{
		src:     src,
		isBytes: isBytes,
		flags:   flags,
		names:   map[string]int{},
		groupNm: map[int]string{},
		open:    map[int]bool{},
	}
	if err := p.checkFlags(flags, 0); err != nil {
		return nil, err
	}
	root, err := p.parseAlt(true)
	if err != nil {
		return nil, err
	}
	if p.more() {
		if p.peek() == ')' {
			return nil, p.errf(p.pos, "unbalanced parenthesis")
		}
		return nil, p.errf(p.pos, "unexpected end of pattern")
	}
	for _, c := range p.condRefs {
		if c.gid >= p.ngroups+1 {
			return nil, p.errf(c.pos, "invalid group reference %d", c.gid)
		}
	}
	return &parseResult{root: root, flags: p.flags, ngroups: p.ngroups, names: p.names, groupNm: p.groupNm}, nil
}

func (p *parser) checkFlags(flags int, pos int) *Error {
	if p.isBytes {
		if flags&FlagUnicode != 0 {
			return p.errf(-1, "cannot use UNICODE flag with a bytes pattern")
		}
		if flags&FlagLocale != 0 && flags&FlagASCII != 0 {
			return p.errf(-1, "ASCII and LOCALE flags are incompatible")
		}
	} else {
		if flags&FlagLocale != 0 {
			return p.errf(-1, "cannot use LOCALE flag with a str pattern")
		}
		if flags&FlagASCII != 0 && flags&FlagUnicode != 0 {
			return p.errf(-1, "ASCII and UNICODE flags are incompatible")
		}
	}
	return nil
}

// parseAlt parses alternatives until ')' or end of pattern.
func (p *parser) parseAlt(top bool) (*node, error) {
	var branches []*node
	first := top
	for {
		seq, err := p.parseSeq(first)
		if err != nil {
			return nil, err
		}
		branches = append(branches, seq)
		first = false
		if !p.match('|') {
			break
		}
	}
	if len(branches) == 1 {
		return branches[0], nil
	}
	return &node{kind: nAlt, kids: branches}, nil
}

func (p *parser) skipVerbose() {
	for p.more() {
		c := p.peek()
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f' {
			p.pos++
		} else if c == '#' {
			for p.more() && p.peek() != '\n' {
				p.pos++
			}
		} else {
			return
		}
	}
}

func (p *parser) parseSeq(first bool) (*node, error) {
	var items []*node
	for {
		if p.flags&FlagVerbose != 0 {
			p.skipVerbose()
		}
		if !p.more() {
			break
		}
		c := p.peek()
		if c == '|' || c == ')' {
			break
		}
		start := p.pos
		p.pos++
		var item *node
		switch c {
		case '*', '+', '?', '{':
			mn, mx := 0, -1
			switch c {
			case '+':
				mn = 1
			case '?':
				mx = 1
			case '{':
				p.pos = start
				lo, hi, ok := p.tryBraces()
				if !ok {
					p.pos = start + 1
					item = p.lit(c)
					break
				}
				mn, mx = lo, hi
				if mx >= 0 && mx < mn {
					return nil, p.errf(start+1, "min repeat greater than max repeat")
				}
			}
			if item != nil {
				break
			}
			if len(items) == 0 {
				return nil, p.errf(start, "nothing to repeat")
			}
			prev := items[len(items)-1]
			if prev.kind == nAt && !prev.paren {
				return nil, p.errf(start, "nothing to repeat")
			}
			if prev.kind == nRepeat && !prev.paren {
				return nil, p.errf(start, "multiple repeat")
			}
			greedy, poss := true, false
			if p.match('?') {
				greedy = false
			} else if p.match('+') {
				poss = true
			}
			items[len(items)-1] = &node{kind: nRepeat, kids: []*node{prev}, min: mn, max: mx, greedy: greedy, possessive: poss, pos: start}
			continue
		case '.':
			item = &node{kind: nAny, dotall: p.flags&FlagDotAll != 0}
		case '[':
			set, err := p.parseClass(start)
			if err != nil {
				return nil, err
			}
			item = &node{kind: nSet, set: set}
		case '(':
			n, isGlobal, err := p.parseGroup(start, first && len(items) == 0)
			if err != nil {
				return nil, err
			}
			if isGlobal {
				continue
			}
			item = n
			if item == nil {
				continue
			}
		case '^':
			if p.flags&FlagMultiline != 0 {
				item = &node{kind: nAt, at: atBOLMulti}
			} else {
				item = &node{kind: nAt, at: atBOL}
			}
		case '$':
			if p.flags&FlagMultiline != 0 {
				item = &node{kind: nAt, at: atEOLMulti}
			} else {
				item = &node{kind: nAt, at: atEOL}
			}
		case '\\':
			n, err := p.parseEscape(start)
			if err != nil {
				return nil, err
			}
			item = n
		default:
			item = p.lit(c)
		}
		items = append(items, item)
	}
	switch len(items) {
	case 0:
		return &node{kind: nEmpty}, nil
	case 1:
		return items[0], nil
	}
	return &node{kind: nCat, kids: items}, nil
}

func (p *parser) lit(r rune) *node {
	n := &node{kind: nLit, r: r}
	if p.ignoreCase() {
		n.fold = true
		n.ascii = p.asciiMode()
	}
	return n
}

// tryBraces attempts to parse {m}, {m,}, {,n} or {m,n} starting at the '{'
// located at p.pos (or p.pos-1 when called after consuming it). On success it
// consumes the quantifier and returns bounds.
func (p *parser) tryBraces() (lo, hi int, ok bool) {
	save := p.pos
	i := p.pos
	if i < len(p.src) && p.src[i] == '{' {
		i++
	}
	if i < len(p.src) && p.src[i] == '}' {
		p.pos = save
		return 0, 0, false
	}
	var los, his strings.Builder
	for i < len(p.src) && isDigit(p.src[i]) {
		los.WriteRune(p.src[i])
		i++
	}
	if i < len(p.src) && p.src[i] == ',' {
		i++
		for i < len(p.src) && isDigit(p.src[i]) {
			his.WriteRune(p.src[i])
			i++
		}
	} else {
		his.WriteString(los.String())
	}
	if i >= len(p.src) || p.src[i] != '}' {
		p.pos = save
		return 0, 0, false
	}
	i++
	lo, hi = 0, -1
	if los.Len() > 0 {
		v, err := strconv.ParseUint(los.String(), 10, 64)
		if err != nil || v >= MaxRepeat {
			lo = MaxRepeat
		} else {
			lo = int(v)
		}
	}
	if his.Len() > 0 {
		v, err := strconv.ParseUint(his.String(), 10, 64)
		if err != nil || v >= MaxRepeat {
			hi = -1
		} else {
			hi = int(v)
		}
	}
	p.pos = i
	return lo, hi, true
}

func (p *parser) parseName(term rune, kind string) (string, error) {
	start := p.pos
	for p.more() && p.peek() != term {
		p.pos++
	}
	if !p.more() {
		if term == '>' {
			return "", p.errf(start, "missing >, unterminated name")
		}
		return "", p.errf(start, "missing ), unterminated name")
	}
	name := string(p.src[start:p.pos])
	p.pos++
	if name == "" {
		return "", p.errf(start, "missing %s", kind)
	}
	if !p.rawName && !validIdent(name) {
		return "", p.errf(start, "bad character in group name %s", pyQuote(name))
	}
	return name, nil
}

func validIdent(s string) bool {
	for i, r := range []rune(s) {
		if i == 0 {
			if !isIdentStart(r) {
				return false
			}
		} else if !isIdentChar(r) {
			return false
		}
	}
	return s != ""
}

func pyQuote(s string) string { return "'" + s + "'" }

// parseGroup parses after '('. It returns the node, whether the group was a
// global flags group (no node produced), and any error.
func (p *parser) parseGroup(start int, atStart bool) (*node, bool, error) {
	if !p.match('?') {
		// plain capture group
		p.ngroups++
		gid := p.ngroups
		return p.finishCapture(start, gid)
	}
	c := p.peek()
	if c < 0 {
		return nil, false, p.errf(p.pos, "unexpected end of pattern")
	}
	p.pos++
	switch c {
	case 'P':
		if p.match('<') {
			name, err := p.parseName('>', "group name")
			if err != nil {
				return nil, false, err
			}
			if prev, dup := p.names[name]; dup {
				return nil, false, p.errf(start+3, "redefinition of group name %s as group %d; was group %d", pyQuote(name), p.ngroups+1, prev)
			}
			p.ngroups++
			gid := p.ngroups
			p.names[name] = gid
			p.groupNm[gid] = name
			return p.finishCapture(start, gid)
		}
		if p.match('=') {
			name, err := p.parseName(')', "group name")
			if err != nil {
				return nil, false, err
			}
			gid, ok := p.names[name]
			if !ok {
				return nil, false, p.errf(start+4, "unknown group name %s", pyQuote(name))
			}
			if p.open[gid] {
				return nil, false, p.errf(start, "cannot refer to an open group")
			}
			n := &node{kind: nBackref, idx: gid, fold: p.ignoreCase(), ascii: p.asciiMode()}
			return n, false, nil
		}
		return nil, false, p.errf(start+2, "unknown extension ?P%s", string(p.peek()))
	case ':':
		n, err := p.parseBody(start)
		if err != nil {
			return nil, false, err
		}
		return paren(n), false, nil
	case '#':
		for p.more() && p.peek() != ')' {
			p.pos++
		}
		if !p.match(')') {
			return nil, false, p.errf(start, "missing ), unterminated comment")
		}
		return nil, false, nil
	case '=', '!':
		n, err := p.parseBody(start)
		if err != nil {
			return nil, false, err
		}
		return &node{kind: nLook, ahead: true, neg: c == '!', kids: []*node{n}, paren: true}, false, nil
	case '<':
		d := p.peek()
		if d != '=' && d != '!' {
			return nil, false, p.errf(start+2, "unknown extension ?<%s", string(d))
		}
		p.pos++
		p.inLookB++
		n, err := p.parseBody(start)
		p.inLookB--
		if err != nil {
			return nil, false, err
		}
		return &node{kind: nLook, ahead: false, neg: d == '!', kids: []*node{n}, paren: true, pos: start}, false, nil
	case '>':
		n, err := p.parseBody(start)
		if err != nil {
			return nil, false, err
		}
		return &node{kind: nAtomic, kids: []*node{n}, paren: true}, false, nil
	case '(':
		return p.parseCond(start)
	default:
		if strings.ContainsRune("aiLmsux-", c) {
			p.pos--
			return p.parseFlagsGroup(start, atStart)
		}
		return nil, false, p.errf(start+1, "unknown extension ?%s", string(c))
	}
}

func paren(n *node) *node {
	if n.paren {
		cp := *n
		return &cp
	}
	n.paren = true
	return n
}

// finishCapture parses the body of a capture group and builds the group node.
func (p *parser) finishCapture(start, gid int) (*node, bool, error) {
	p.open[gid] = true
	n, err := p.parseBody(start)
	delete(p.open, gid)
	if err != nil {
		return nil, false, err
	}
	return &node{kind: nGroup, idx: gid, kids: []*node{n}, paren: true}, false, nil
}

// parseBody parses alternatives and the closing ')'.
func (p *parser) parseBody(start int) (*node, error) {
	savedFlags := p.flags
	n, err := p.parseAlt(false)
	p.flags = savedFlags
	if err != nil {
		return nil, err
	}
	if !p.match(')') {
		return nil, p.errf(start, "missing ), unterminated subpattern")
	}
	return n, nil
}

func (p *parser) parseCond(start int) (*node, bool, error) {
	// after "(?("
	nameStart := p.pos
	p.rawName = true
	name, err := p.parseName(')', "group name")
	p.rawName = false
	if err != nil {
		return nil, false, err
	}
	var gid int
	if allDigits(name) {
		v, _ := strconv.Atoi(name)
		gid = v
		if gid == 0 {
			return nil, false, p.errf(nameStart, "bad group number")
		}
		p.condRefs = append(p.condRefs, condRef{gid: gid, pos: nameStart})
	} else {
		if !validIdent(name) {
			return nil, false, p.errf(nameStart, "bad character in group name %s", pyQuote(name))
		}
		g, ok := p.names[name]
		if !ok {
			return nil, false, p.errf(nameStart, "unknown group name %s", pyQuote(name))
		}
		gid = g
	}
	saved := p.flags
	yes, err := p.parseSeq(false)
	if err != nil {
		return nil, false, err
	}
	var no *node
	if p.match('|') {
		no, err = p.parseSeq(false)
		if err != nil {
			return nil, false, err
		}
		if p.peek() == '|' {
			return nil, false, p.errf(p.pos, "conditional backref with more than two branches")
		}
	}
	p.flags = saved
	if !p.match(')') {
		return nil, false, p.errf(start, "missing ), unterminated subpattern")
	}
	if no == nil {
		no = &node{kind: nEmpty}
	}
	return &node{kind: nCond, idx: gid, kids: []*node{yes, no}, paren: true}, false, nil
}

func allDigits(s string) bool {
	for _, r := range s {
		if !isDigit(r) {
			return false
		}
	}
	return s != ""
}

func (p *parser) parseFlagsGroup(start int, atStart bool) (*node, bool, error) {
	add, del := 0, 0
	negative := false
	flagPos := p.pos
	for {
		c := p.peek()
		if c < 0 {
			return nil, false, p.errf(p.pos, "missing -, : or )")
		}
		if c == ')' || c == ':' || c == '-' {
			break
		}
		f := 0
		switch c {
		case 'a':
			f = FlagASCII
		case 'i':
			f = FlagIgnoreCase
		case 'L':
			f = FlagLocale
		case 'm':
			f = FlagMultiline
		case 's':
			f = FlagDotAll
		case 'u':
			f = FlagUnicode
		case 'x':
			f = FlagVerbose
		default:
			if unicode.IsLetter(c) {
				return nil, false, p.errf(p.pos, "unknown flag")
			}
			return nil, false, p.errf(p.pos, "missing -, : or )")
		}
		add |= f
		p.pos++
	}
	if p.peek() == '-' {
		p.pos++
		negative = true
		for {
			c := p.peek()
			if c < 0 {
				return nil, false, p.errf(p.pos, "missing :")
			}
			if c == ':' {
				break
			}
			f := 0
			switch c {
			case 'i':
				f = FlagIgnoreCase
			case 'm':
				f = FlagMultiline
			case 's':
				f = FlagDotAll
			case 'x':
				f = FlagVerbose
			case 'a', 'L', 'u':
				return nil, false, p.errf(p.pos, "bad inline flags: cannot turn off flags 'a', 'u' and 'L'")
			case ')':
				return nil, false, p.errf(p.pos, "missing :")
			default:
				if unicode.IsLetter(c) {
					return nil, false, p.errf(p.pos, "unknown flag")
				}
				return nil, false, p.errf(p.pos, "missing :")
			}
			del |= f
			p.pos++
		}
		if del == 0 {
			return nil, false, p.errf(p.pos, "missing flag")
		}
	}
	term := p.peek()
	p.pos++
	if add&del != 0 {
		return nil, false, p.errf(flagPos, "bad inline flags: flag turned on and off")
	}
	if add&FlagASCII != 0 && add&FlagUnicode != 0 {
		return nil, false, p.errf(flagPos, "bad inline flags: flags 'a', 'u' and 'L' are incompatible")
	}
	if add&FlagASCII != 0 && add&FlagLocale != 0 || add&FlagUnicode != 0 && add&FlagLocale != 0 {
		return nil, false, p.errf(flagPos, "bad inline flags: flags 'a', 'u' and 'L' are incompatible")
	}
	if p.isBytes && add&FlagUnicode != 0 {
		return nil, false, p.errf(flagPos, "bad inline flags: cannot use 'u' flag with a bytes pattern")
	}
	if !p.isBytes && add&FlagLocale != 0 {
		return nil, false, p.errf(flagPos, "bad inline flags: cannot use 'L' flag with a str pattern")
	}
	if term == ')' {
		if negative {
			return nil, false, p.errf(p.pos-1, "missing :")
		}
		if !atStart {
			return nil, false, p.errf(start, "global flags not at the start of the expression")
		}
		p.flags |= add
		if err := p.checkFlags(p.flags, start); err != nil {
			return nil, false, err
		}
		return nil, true, nil
	}
	// scoped flags: (?flags-flags:...)
	cur := p.flags
	if add&(FlagASCII|FlagUnicode|FlagLocale) != 0 {
		cur &^= FlagASCII | FlagUnicode | FlagLocale
	}
	cur = (cur | add) &^ del
	if err := p.checkFlags(cur, start); err != nil {
		return nil, false, err
	}
	saved := p.flags
	p.flags = cur
	n, err := p.parseAlt(false)
	p.flags = saved
	if err != nil {
		return nil, false, err
	}
	if !p.match(')') {
		return nil, false, p.errf(start, "missing ), unterminated subpattern")
	}
	return paren(n), false, nil
}

func (p *parser) hexEscape(start int, kind rune, n int) (rune, error) {
	if p.pos+n > len(p.src) {
		return 0, p.errf(start, "incomplete escape \\%c%s", kind, string(p.src[p.pos:]))
	}
	digits := p.src[p.pos : p.pos+n]
	for _, d := range digits {
		if !isHex(d) {
			return 0, p.errf(start, "incomplete escape \\%c%s", kind, string(digits))
		}
	}
	v, _ := strconv.ParseUint(string(digits), 16, 32)
	p.pos += n
	if kind == 'U' && v > 0x10FFFF {
		return 0, p.errf(start, "bad escape \\U%s", string(digits))
	}
	return rune(v), nil
}

func (p *parser) simpleEscape(c rune) (rune, bool) {
	switch c {
	case 'a':
		return 7, true
	case 'f':
		return 12, true
	case 'n':
		return 10, true
	case 'r':
		return 13, true
	case 't':
		return 9, true
	case 'v':
		return 11, true
	case '\\':
		return '\\', true
	}
	return 0, false
}

// parseEscape parses an escape outside a character class, after the backslash.
func (p *parser) parseEscape(start int) (*node, error) {
	if !p.more() {
		return nil, p.errf(start, "bad escape (end of pattern)")
	}
	c := p.src[p.pos]
	p.pos++
	if v, ok := p.simpleEscape(c); ok {
		return p.lit(v), nil
	}
	switch c {
	case 'A':
		return &node{kind: nAt, at: atBOS}, nil
	case 'Z', 'z':
		return &node{kind: nAt, at: atEOS}, nil
	case 'b':
		return &node{kind: nAt, at: atWordB, ascii: p.asciiMode()}, nil
	case 'B':
		return &node{kind: nAt, at: atNotWordB, ascii: p.asciiMode()}, nil
	case 'd', 'D', 's', 'S', 'w', 'W':
		cat := catOf(c)
		set := &charSet{items: []csItem{{cat: cat}}, ascii: p.asciiMode()}
		return &node{kind: nSet, set: set}, nil
	case 'x':
		v, err := p.hexEscape(start, 'x', 2)
		if err != nil {
			return nil, err
		}
		return p.lit(v), nil
	case 'u', 'U':
		if p.isBytes {
			return nil, p.errf(start, "bad escape \\%c", c)
		}
		n := 4
		if c == 'U' {
			n = 8
		}
		v, err := p.hexEscape(start, c, n)
		if err != nil {
			return nil, err
		}
		return p.lit(v), nil
	case 'N':
		v, err := p.namedEscape(start)
		if err != nil {
			return nil, err
		}
		return p.lit(v), nil
	case '0':
		v := rune(0)
		for i := 0; i < 2 && isOct(p.peek()); i++ {
			v = v*8 + (p.src[p.pos] - '0')
			p.pos++
		}
		return p.lit(v), nil
	}
	if c >= '1' && c <= '9' {
		if isDigit(p.peek()) {
			d := p.src[p.pos]
			if isOct(c) && isOct(d) && isOct(p.peekAt(1)) {
				v := (c-'0')*64 + (d-'0')*8 + (p.src[p.pos+1] - '0')
				p.pos += 2
				if v > 0377 {
					return nil, p.errf(start, "octal escape value \\%s outside of range 0-0o377", string(p.src[start+1:p.pos]))
				}
				return p.lit(v), nil
			}
			p.pos++
			gid := int(c-'0')*10 + int(d-'0')
			return p.backref(start, gid)
		}
		return p.backref(start, int(c-'0'))
	}
	if isASCIILetter(c) {
		return nil, p.errf(start, "bad escape \\%c", c)
	}
	return p.lit(c), nil
}

func (p *parser) backref(start, gid int) (*node, error) {
	if gid > p.ngroups {
		return nil, p.errf(start, "invalid group reference %d", gid)
	}
	if p.open[gid] {
		return nil, p.errf(start, "cannot refer to an open group")
	}
	return &node{kind: nBackref, idx: gid, fold: p.ignoreCase(), ascii: p.asciiMode()}, nil
}

func catOf(c rune) catKind {
	switch c {
	case 'd':
		return catDigit
	case 'D':
		return catNotDigit
	case 's':
		return catSpace
	case 'S':
		return catNotSpace
	case 'w':
		return catWord
	case 'W':
		return catNotWord
	}
	return catNone
}

// classEscape parses an escape inside a character class.
func (p *parser) classEscape(start int) (rune, catKind, error) {
	if !p.more() {
		return 0, 0, p.errf(start, "bad escape (end of pattern)")
	}
	c := p.src[p.pos]
	p.pos++
	if v, ok := p.simpleEscape(c); ok {
		return v, catNone, nil
	}
	switch c {
	case 'b':
		return 8, catNone, nil
	case 'd', 'D', 's', 'S', 'w', 'W':
		return 0, catOf(c), nil
	case 'x':
		v, err := p.hexEscape(start, 'x', 2)
		return v, catNone, err
	case 'u', 'U':
		if p.isBytes {
			return 0, 0, p.errf(start, "bad escape \\%c", c)
		}
		n := 4
		if c == 'U' {
			n = 8
		}
		v, err := p.hexEscape(start, c, n)
		return v, catNone, err
	case 'N':
		v, err := p.namedEscape(start)
		return v, catNone, err
	}
	if isOct(c) {
		v := c - '0'
		for i := 0; i < 2 && isOct(p.peek()); i++ {
			v = v*8 + (p.src[p.pos] - '0')
			p.pos++
		}
		if v > 0377 {
			return 0, 0, p.errf(start, "octal escape value \\%s outside of range 0-0o377", string(p.src[start+1:p.pos]))
		}
		return v, catNone, nil
	}
	if isDigit(c) || isASCIILetter(c) {
		return 0, 0, p.errf(start, "bad escape \\%c", c)
	}
	return c, catNone, nil
}

func (p *parser) parseClass(start int) (*charSet, error) {
	set := &charSet{ascii: p.asciiMode(), fold: p.ignoreCase()}
	if p.match('^') {
		set.negate = true
	}
	first := true
	for {
		if !p.more() {
			return nil, p.errf(start, "unterminated character set")
		}
		itemStart := p.pos
		c := p.src[p.pos]
		p.pos++
		if c == ']' && !first {
			break
		}
		first = false
		var lo rune
		var cat catKind
		if c == '\\' {
			var err error
			lo, cat, err = p.classEscape(itemStart)
			if err != nil {
				return nil, err
			}
		} else {
			lo = c
		}
		if p.peek() == '-' && p.peekAt(1) != ']' && p.peekAt(1) >= 0 {
			// range
			p.pos++
			hiStart := p.pos
			h := p.src[p.pos]
			p.pos++
			var hi rune
			var cat2 catKind
			if h == '\\' {
				var err error
				hi, cat2, err = p.classEscape(hiStart)
				if err != nil {
					return nil, err
				}
			} else {
				hi = h
			}
			if cat != catNone || cat2 != catNone {
				return nil, p.errf(itemStart, "bad character range %s-%s", string(p.src[itemStart:hiStart-1]), string(p.src[hiStart:p.pos]))
			}
			if hi < lo {
				return nil, p.errf(itemStart, "bad character range %s-%s", string(p.src[itemStart:hiStart-1]), string(p.src[hiStart:p.pos]))
			}
			set.items = append(set.items, csItem{lo: lo, hi: hi})
			continue
		}
		if cat != catNone {
			set.items = append(set.items, csItem{cat: cat})
		} else {
			set.items = append(set.items, csItem{lo: lo, hi: lo})
		}
	}
	return set, nil
}

// namedEscape parses the {NAME} part of a \N{NAME} escape (p.pos is just past
// the 'N') and resolves it through the unicodedata tables.
func (p *parser) namedEscape(start int) (rune, error) {
	if p.isBytes {
		return 0, p.errf(start, "bad escape \\N")
	}
	if p.peek() != '{' {
		return 0, p.errf(start, "missing {")
	}
	p.pos++
	nameStart := p.pos
	for p.more() && p.src[p.pos] != '}' {
		p.pos++
	}
	if !p.more() {
		return 0, p.errf(start, "missing }, unterminated name")
	}
	name := string(p.src[nameStart:p.pos])
	p.pos++
	if name == "" {
		return 0, p.errf(start, "missing character name")
	}
	r, ok := unicodedata.Lookup(name)
	if !ok {
		return 0, p.errf(start, "undefined character name '%s'", name)
	}
	return r, nil
}
