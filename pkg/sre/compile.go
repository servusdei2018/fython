package sre

import "unicode"

type opcode uint8

const (
	opChar opcode = iota
	opCharFold
	opAny
	opAnyNL
	opSet
	opSplit // x = preferred pc, y = alternative pc
	opJmp
	opSave
	opAt
	opBackref
	opLook       // lookahead: x = after, neg
	opLookBehind // x = after, y = width, neg
	opAtomic     // x = after
	opSubEnd
	opCond // x = group, y = else pc
	opLoopInit
	opLoop // x = reg base, y = exit pc
	opRepeat
	opMatch
)

const (
	flGreedy uint8 = 1 << iota
	flAscii
	flNeg
	flPossessive
)

type inst struct {
	op    opcode
	sub   opcode // single-character matcher kind for opRepeat
	flags uint8
	r     rune
	x, y  int
	min   int
	max   int
	set   *charSet
}

// Regexp is a compiled Python regular expression.
type Regexp struct {
	Pattern    []rune
	Flags      int
	IsBytes    bool
	NumGroups  int
	GroupIndex map[string]int
	GroupNames []string // indexed by group number, "" when unnamed

	prog     []inst
	nregs    int
	hasFirst bool
	firstLit rune
	bosOnly  bool
}

// Compile parses and compiles a pattern. For bytes patterns every rune must
// be below 256.
func Compile(pattern []rune, flags int, isBytes bool) (*Regexp, error) {
	pr, err := parse(pattern, flags, isBytes)
	if err != nil {
		return nil, err
	}
	re := &Regexp{
		Pattern:    append([]rune(nil), pattern...),
		IsBytes:    isBytes,
		NumGroups:  pr.ngroups,
		GroupIndex: pr.names,
		GroupNames: make([]string, pr.ngroups+1),
	}
	for gid, name := range pr.groupNm {
		re.GroupNames[gid] = name
	}
	re.Flags = pr.flags
	if !isBytes && re.Flags&FlagASCII == 0 {
		re.Flags |= FlagUnicode
	}
	c := &compiler{re: re}
	if err := c.compile(pr.root); err != nil {
		return nil, err
	}
	c.emit(inst{op: opMatch})
	re.prog = c.prog
	re.nregs = c.nregs
	if len(re.prog) > 0 {
		first := &re.prog[0]
		if first.op == opChar {
			re.hasFirst = true
			re.firstLit = first.r
		}
		if first.op == opAt && (atKind(first.x) == atBOS || atKind(first.x) == atBOL) {
			re.bosOnly = true
		}
	}
	return re, nil
}

type compiler struct {
	re    *Regexp
	prog  []inst
	nregs int
}

func (c *compiler) emit(in inst) int {
	c.prog = append(c.prog, in)
	return len(c.prog) - 1
}

func isSimple(n *node) bool {
	return n.kind == nLit || n.kind == nAny || n.kind == nSet
}

func (c *compiler) singleInst(n *node) inst {
	switch n.kind {
	case nLit:
		if n.fold {
			in := inst{op: opCharFold, r: canon(n.r, n.ascii)}
			if n.ascii {
				in.flags |= flAscii
			}
			return in
		}
		return inst{op: opChar, r: n.r}
	case nAny:
		if n.dotall {
			return inst{op: opAnyNL}
		}
		return inst{op: opAny}
	default:
		n.set.init()
		return inst{op: opSet, set: n.set}
	}
}

func (c *compiler) compile(n *node) error {
	switch n.kind {
	case nEmpty:
	case nLit, nAny, nSet:
		c.emit(c.singleInst(n))
	case nCat:
		for _, k := range n.kids {
			if err := c.compile(k); err != nil {
				return err
			}
		}
	case nAlt:
		var jumps []int
		for i, k := range n.kids {
			if i == len(n.kids)-1 {
				if err := c.compile(k); err != nil {
					return err
				}
				break
			}
			sp := c.emit(inst{op: opSplit})
			c.prog[sp].x = len(c.prog)
			if err := c.compile(k); err != nil {
				return err
			}
			jumps = append(jumps, c.emit(inst{op: opJmp}))
			c.prog[sp].y = len(c.prog)
		}
		for _, j := range jumps {
			c.prog[j].x = len(c.prog)
		}
	case nGroup:
		c.emit(inst{op: opSave, x: 2 * n.idx})
		if err := c.compile(n.kids[0]); err != nil {
			return err
		}
		c.emit(inst{op: opSave, x: 2*n.idx + 1})
	case nBackref:
		in := inst{op: opBackref, x: n.idx}
		if n.fold {
			in.sub = opCharFold
			if n.ascii {
				in.flags |= flAscii
			}
		}
		c.emit(in)
	case nAt:
		in := inst{op: opAt, x: int(n.at)}
		if n.ascii {
			in.flags |= flAscii
		}
		c.emit(in)
	case nAtomic:
		at := c.emit(inst{op: opAtomic})
		if err := c.compile(n.kids[0]); err != nil {
			return err
		}
		c.emit(inst{op: opSubEnd})
		c.prog[at].x = len(c.prog)
	case nLook:
		if n.ahead {
			at := c.emit(inst{op: opLook})
			if n.neg {
				c.prog[at].flags |= flNeg
			}
			if err := c.compile(n.kids[0]); err != nil {
				return err
			}
			c.emit(inst{op: opSubEnd})
			c.prog[at].x = len(c.prog)
		} else {
			lo, hi, ok := width(n.kids[0], c.re.NumGroups)
			if !ok || lo != hi {
				return &Error{Msg: "look-behind requires fixed-width pattern", Pos: -1}
			}
			at := c.emit(inst{op: opLookBehind, y: lo})
			if n.neg {
				c.prog[at].flags |= flNeg
			}
			if err := c.compile(n.kids[0]); err != nil {
				return err
			}
			c.emit(inst{op: opSubEnd})
			c.prog[at].x = len(c.prog)
		}
	case nCond:
		cond := c.emit(inst{op: opCond, x: n.idx})
		if err := c.compile(n.kids[0]); err != nil {
			return err
		}
		jmp := c.emit(inst{op: opJmp})
		c.prog[cond].y = len(c.prog)
		if err := c.compile(n.kids[1]); err != nil {
			return err
		}
		c.prog[jmp].x = len(c.prog)
	case nRepeat:
		return c.compileRepeat(n)
	}
	return nil
}

func (c *compiler) compileRepeat(n *node) error {
	child := n.kids[0]
	if n.max == 0 {
		return nil
	}
	if n.min == 1 && n.max == 1 {
		return c.compile(child)
	}
	if isSimple(child) {
		in := c.singleInst(child)
		rep := inst{op: opRepeat, sub: in.op, r: in.r, set: in.set, flags: in.flags & flAscii, min: n.min, max: n.max}
		if n.greedy {
			rep.flags |= flGreedy
		}
		if n.possessive {
			rep.flags |= flPossessive
			rep.flags |= flGreedy
		}
		c.emit(rep)
		return nil
	}
	if n.possessive {
		inner := *n
		inner.possessive = false
		inner.greedy = true
		return c.compile(&node{kind: nAtomic, kids: []*node{&inner}})
	}
	if n.min == 0 && n.max == 1 {
		sp := c.emit(inst{op: opSplit})
		bodyStart := len(c.prog)
		if err := c.compile(child); err != nil {
			return err
		}
		end := len(c.prog)
		if n.greedy {
			c.prog[sp].x, c.prog[sp].y = bodyStart, end
		} else {
			c.prog[sp].x, c.prog[sp].y = end, bodyStart
		}
		return nil
	}
	reg := c.nregs
	c.nregs += 2
	c.emit(inst{op: opLoopInit, x: reg})
	loop := c.emit(inst{op: opLoop, x: reg, min: n.min, max: n.max})
	if n.greedy {
		c.prog[loop].flags |= flGreedy
	}
	if err := c.compile(child); err != nil {
		return err
	}
	c.emit(inst{op: opJmp, x: loop})
	c.prog[loop].y = len(c.prog)
	return nil
}

// width computes the min and max width in characters of a node; ok is false
// when the width is unbounded or unknowable (e.g. backreferences).
func width(n *node, ngroups int) (lo, hi int, ok bool) {
	switch n.kind {
	case nEmpty, nAt, nLook:
		return 0, 0, true
	case nLit, nAny, nSet:
		return 1, 1, true
	case nCat:
		for _, k := range n.kids {
			l, h, ok := width(k, ngroups)
			if !ok {
				return 0, 0, false
			}
			lo += l
			hi += h
		}
		return lo, hi, true
	case nAlt:
		first := true
		for _, k := range n.kids {
			l, h, ok := width(k, ngroups)
			if !ok {
				return 0, 0, false
			}
			if first {
				lo, hi = l, h
				first = false
			} else {
				if l < lo {
					lo = l
				}
				if h > hi {
					hi = h
				}
			}
		}
		return lo, hi, true
	case nGroup, nAtomic:
		return width(n.kids[0], ngroups)
	case nRepeat:
		l, h, ok := width(n.kids[0], ngroups)
		if !ok {
			return 0, 0, false
		}
		if n.max < 0 {
			if h != 0 {
				return 0, 0, false
			}
			return 0, 0, true
		}
		return l * n.min, h * n.max, true
	case nCond:
		l1, h1, ok1 := width(n.kids[0], ngroups)
		l2, h2, ok2 := width(n.kids[1], ngroups)
		if !ok1 || !ok2 {
			return 0, 0, false
		}
		if l2 < l1 {
			l1 = l2
		}
		if h2 > h1 {
			h1 = h2
		}
		return l1, h1, true
	}
	return 0, 0, false
}

// canon returns a canonical representative of r's case-folding orbit.
func canon(r rune, ascii bool) rune {
	if ascii {
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return r
	}
	if r < 0x80 && r != 'k' && r != 'K' && r != 's' && r != 'S' {
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return r
	}
	m := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		if f < m {
			m = f
		}
	}
	return m
}

func isWordRune(r rune, ascii bool) bool {
	if r < 0x80 {
		return r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
	}
	if ascii {
		return false
	}
	return unicode.IsLetter(r) || unicode.IsNumber(r)
}

func isDigitRune(r rune, ascii bool) bool {
	if r < 0x80 {
		return r >= '0' && r <= '9'
	}
	if ascii {
		return false
	}
	return unicode.Is(unicode.Nd, r)
}

func isSpaceRune(r rune, ascii bool) bool {
	if r < 0x80 {
		return r == ' ' || (r >= 9 && r <= 13) || (r >= 0x1c && r <= 0x1f && !ascii)
	}
	if ascii {
		return false
	}
	return r == 0x85 || unicode.IsSpace(r)
}

func catMatch(cat catKind, r rune, ascii bool) bool {
	switch cat {
	case catDigit:
		return isDigitRune(r, ascii)
	case catNotDigit:
		return !isDigitRune(r, ascii)
	case catSpace:
		return isSpaceRune(r, ascii)
	case catNotSpace:
		return !isSpaceRune(r, ascii)
	case catWord:
		return isWordRune(r, ascii)
	case catNotWord:
		return !isWordRune(r, ascii)
	}
	return false
}

func (cs *charSet) itemsMatch(r rune) bool {
	for i := range cs.items {
		it := &cs.items[i]
		if it.cat != catNone {
			if catMatch(it.cat, r, cs.ascii) {
				return true
			}
		} else if r >= it.lo && r <= it.hi {
			return true
		}
	}
	return false
}

func (cs *charSet) slow(r rune) bool {
	in := cs.itemsMatch(r)
	if !in && cs.fold {
		if cs.ascii {
			if r >= 'a' && r <= 'z' {
				in = cs.itemsMatch(r - 32)
			} else if r >= 'A' && r <= 'Z' {
				in = cs.itemsMatch(r + 32)
			}
		} else {
			for f := unicode.SimpleFold(r); f != r && !in; f = unicode.SimpleFold(f) {
				in = cs.itemsMatch(f)
			}
		}
	}
	return in != cs.negate
}

func (cs *charSet) init() {
	for i := range rune(128) {
		cs.table[i] = cs.slow(i)
	}
}

func (cs *charSet) has(r rune) bool {
	if r < 128 && r >= 0 {
		return cs.table[r]
	}
	return cs.slow(r)
}
