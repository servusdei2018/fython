package sre

// Mode selects how Exec anchors a match.
type Mode int

const (
	// ModeSearch scans forward from pos for the first match.
	ModeSearch Mode = iota
	// ModeMatch requires the match to start at pos.
	ModeMatch
	// ModeFullMatch requires the match to start at pos and end at endpos.
	ModeFullMatch
)

// Result holds capture positions (rune indices) for a successful match.
type Result struct {
	// Caps holds start/end pairs for group 0..NumGroups, -1 when unset.
	Caps      []int
	LastIndex int // -1 when no group participated
}

const (
	ekChoice uint8 = iota
	ekUndoCap
	ekUndoReg
	ekUndoLast
	ekRepGreedy
	ekRepLazy
	ekLoopEnter
)

type entry struct {
	kind uint8
	pc   int
	pos  int
	a, b int
}

type matcher struct {
	re      *Regexp
	prog    []inst
	in      []rune
	end     int
	caps    []int
	regs    []int
	last    int
	stack   []entry
	full    int
	mustAdv int
}

// Exec runs the regexp against input[:endpos], starting at pos. Positions are
// rune indices. It returns nil when there is no match.
func (re *Regexp) Exec(input []rune, pos, endpos int, mode Mode) *Result {
	return re.ExecFrom(input, pos, endpos, mode, false)
}

// ExecFrom is Exec with CPython's must_advance semantics: when mustAdvance is
// set, an empty match at pos is rejected (used by iteration after an empty match).
func (re *Regexp) ExecFrom(input []rune, pos, endpos int, mode Mode, mustAdvance bool) *Result {
	if pos < 0 {
		pos = 0
	}
	if endpos > len(input) {
		endpos = len(input)
	}
	if endpos < 0 {
		endpos = 0
	}
	if pos > endpos {
		return nil
	}
	m := &matcher{
		re:      re,
		prog:    re.prog,
		in:      input,
		end:     endpos,
		caps:    make([]int, 2*(re.NumGroups+1)),
		regs:    make([]int, re.nregs),
		last:    -1,
		full:    -1,
		mustAdv: -1,
	}
	for i := range m.caps {
		m.caps[i] = -1
	}
	if mode == ModeFullMatch {
		m.full = endpos
	}
	if mustAdvance {
		m.mustAdv = pos
	}
	try := func(start int) bool {
		m.stack = m.stack[:0]
		m.last = -1
		m.caps[0] = start
		e, ok := m.run(0, start)
		if !ok {
			return false
		}
		m.caps[1] = e
		return true
	}
	switch mode {
	case ModeMatch, ModeFullMatch:
		if !try(pos) {
			return nil
		}
	default:
		if re.bosOnly {
			if pos != 0 || !try(0) {
				return nil
			}
			break
		}
		found := false
		for s := pos; s <= endpos; s++ {
			if re.hasFirst {
				for s < endpos && input[s] != re.firstLit {
					s++
				}
				if s >= endpos {
					break
				}
			}
			if try(s) {
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return &Result{Caps: m.caps, LastIndex: m.last}
}

func (m *matcher) setReg(i, v int) {
	m.stack = append(m.stack, entry{kind: ekUndoReg, a: i, b: m.regs[i]})
	m.regs[i] = v
}

func (m *matcher) single(in *inst, r rune) bool {
	switch in.sub {
	case opChar:
		return r == in.r
	case opCharFold:
		return canon(r, in.flags&flAscii != 0) == in.r
	case opAny:
		return r != '\n'
	case opAnyNL:
		return true
	case opSet:
		return in.set.has(r)
	}
	return false
}

// unwind pops entries down to top, applying undo records.
func (m *matcher) unwind(top int) {
	for len(m.stack) > top {
		e := m.stack[len(m.stack)-1]
		m.stack = m.stack[:len(m.stack)-1]
		switch e.kind {
		case ekUndoCap:
			m.caps[e.a] = e.b
		case ekUndoReg:
			m.regs[e.a] = e.b
		case ekUndoLast:
			m.last = e.a
		}
	}
}

// compact drops choice points above top but keeps undo records, so captures
// made inside an atomic region are still rolled back on outer backtracking.
func (m *matcher) compact(top int) {
	j := top
	for i := top; i < len(m.stack); i++ {
		switch m.stack[i].kind {
		case ekUndoCap, ekUndoReg, ekUndoLast:
			m.stack[j] = m.stack[i]
			j++
		}
	}
	m.stack = m.stack[:j]
}

// backtrack pops until a resumable choice is found.
func (m *matcher) backtrack(base int) (pc, pos int, ok bool) {
	for len(m.stack) > base {
		e := m.stack[len(m.stack)-1]
		m.stack = m.stack[:len(m.stack)-1]
		switch e.kind {
		case ekChoice:
			return e.pc, e.pos, true
		case ekUndoCap:
			m.caps[e.a] = e.b
		case ekUndoReg:
			m.regs[e.a] = e.b
		case ekUndoLast:
			m.last = e.a
		case ekRepGreedy:
			cur := e.pos - 1
			floor := e.a
			next := &m.prog[e.pc]
			for cur >= floor {
				if next.op == opChar && !(cur < m.end && m.in[cur] == next.r) {
					cur--
					continue
				}
				break
			}
			if cur < floor {
				continue
			}
			if cur > floor {
				m.stack = append(m.stack, entry{kind: ekRepGreedy, pc: e.pc, pos: cur, a: floor})
			}
			return e.pc, cur, true
		case ekRepLazy:
			in := &m.prog[e.pc]
			cur, count := e.pos, e.a
			if (in.max < 0 || count < in.max) && cur < m.end && m.single(in, m.in[cur]) {
				cur++
				count++
				m.stack = append(m.stack, entry{kind: ekRepLazy, pc: e.pc, pos: cur, a: count})
				return e.pc + 1, cur, true
			}
		case ekLoopEnter:
			in := &m.prog[e.pc]
			c := m.regs[in.x]
			m.setReg(in.x, c+1)
			m.setReg(in.x+1, e.pos)
			return e.pc + 1, e.pos, true
		}
	}
	return 0, 0, false
}

func (m *matcher) atMatch(in *inst, pos int) bool {
	switch atKind(in.x) {
	case atBOL, atBOS:
		return pos == 0
	case atBOLMulti:
		return pos == 0 || m.in[pos-1] == '\n'
	case atEOL:
		return pos == m.end || (pos == m.end-1 && m.in[pos] == '\n')
	case atEOLMulti:
		return pos == m.end || m.in[pos] == '\n'
	case atEOS:
		return pos == m.end
	case atWordB, atNotWordB:
		ascii := in.flags&flAscii != 0
		before := pos > 0 && isWordRune(m.in[pos-1], ascii)
		after := pos < m.end && isWordRune(m.in[pos], ascii)
		if atKind(in.x) == atWordB {
			return before != after
		}
		return before == after
	}
	return false
}

func (m *matcher) run(pc, pos int) (int, bool) {
	base := len(m.stack)
	for {
		in := &m.prog[pc]
		ok := true
		switch in.op {
		case opChar:
			if pos < m.end && m.in[pos] == in.r {
				pos++
				pc++
			} else {
				ok = false
			}
		case opCharFold:
			if pos < m.end && canon(m.in[pos], in.flags&flAscii != 0) == in.r {
				pos++
				pc++
			} else {
				ok = false
			}
		case opAny:
			if pos < m.end && m.in[pos] != '\n' {
				pos++
				pc++
			} else {
				ok = false
			}
		case opAnyNL:
			if pos < m.end {
				pos++
				pc++
			} else {
				ok = false
			}
		case opSet:
			if pos < m.end && in.set.has(m.in[pos]) {
				pos++
				pc++
			} else {
				ok = false
			}
		case opSplit:
			m.stack = append(m.stack, entry{kind: ekChoice, pc: in.y, pos: pos})
			pc = in.x
		case opJmp:
			pc = in.x
		case opSave:
			idx := in.x
			m.stack = append(m.stack, entry{kind: ekUndoCap, a: idx, b: m.caps[idx]})
			m.caps[idx] = pos
			if idx&1 == 1 {
				m.stack = append(m.stack, entry{kind: ekUndoLast, a: m.last})
				m.last = idx / 2
			}
			pc++
		case opAt:
			if m.atMatch(in, pos) {
				pc++
			} else {
				ok = false
			}
		case opBackref:
			s, e := m.caps[2*in.x], m.caps[2*in.x+1]
			if s < 0 || e < 0 || pos+(e-s) > m.end {
				ok = false
				break
			}
			if in.sub == opCharFold {
				ascii := in.flags&flAscii != 0
				for i := 0; i < e-s; i++ {
					if canon(m.in[s+i], ascii) != canon(m.in[pos+i], ascii) {
						ok = false
						break
					}
				}
			} else {
				for i := 0; i < e-s; i++ {
					if m.in[s+i] != m.in[pos+i] {
						ok = false
						break
					}
				}
			}
			if ok {
				pos += e - s
				pc++
			}
		case opAtomic:
			top := len(m.stack)
			end, sok := m.run(pc+1, pos)
			if !sok {
				ok = false
				break
			}
			m.compact(top)
			pos = end
			pc = in.x
		case opLook, opLookBehind:
			start := pos
			if in.op == opLookBehind {
				start = pos - in.y
			}
			neg := in.flags&flNeg != 0
			if start < 0 {
				if neg {
					pc = in.x
				} else {
					ok = false
				}
				break
			}
			top := len(m.stack)
			_, sok := m.run(pc+1, start)
			if neg {
				if sok {
					m.unwind(top)
					ok = false
				} else {
					pc = in.x
				}
			} else if !sok {
				ok = false
			} else {
				m.compact(top)
				pc = in.x
			}
		case opSubEnd:
			return pos, true
		case opCond:
			if m.caps[2*in.x] >= 0 && m.caps[2*in.x+1] >= 0 {
				pc++
			} else {
				pc = in.y
			}
		case opLoopInit:
			m.setReg(in.x, 0)
			m.setReg(in.x+1, -1)
			pc++
		case opLoop:
			c := m.regs[in.x]
			if c > 0 && m.regs[in.x+1] == pos && c >= in.min {
				pc = in.y
				break
			}
			enter := func() {
				m.setReg(in.x, c+1)
				m.setReg(in.x+1, pos)
				pc++
			}
			switch {
			case c < in.min:
				enter()
			case in.max < 0 || c < in.max:
				if in.flags&flGreedy != 0 {
					m.stack = append(m.stack, entry{kind: ekChoice, pc: in.y, pos: pos})
					enter()
				} else {
					m.stack = append(m.stack, entry{kind: ekLoopEnter, pc: pc, pos: pos})
					pc = in.y
				}
			default:
				pc = in.y
			}
		case opRepeat:
			if in.flags&flGreedy == 0 {
				cnt := 0
				for cnt < in.min && pos+cnt < m.end && m.single(in, m.in[pos+cnt]) {
					cnt++
				}
				if cnt < in.min {
					ok = false
					break
				}
				m.stack = append(m.stack, entry{kind: ekRepLazy, pc: pc, pos: pos + cnt, a: cnt})
				pos += cnt
				pc++
				break
			}
			n := 0
			limit := m.end - pos
			if in.max >= 0 && in.max < limit {
				limit = in.max
			}
			for n < limit && m.single(in, m.in[pos+n]) {
				n++
			}
			if n < in.min {
				ok = false
				break
			}
			if n > in.min && in.flags&flPossessive == 0 {
				m.stack = append(m.stack, entry{kind: ekRepGreedy, pc: pc + 1, pos: pos + n, a: pos + in.min})
			}
			pos += n
			pc++
		case opMatch:
			if (m.full >= 0 && pos != m.full) || (m.mustAdv >= 0 && pos == m.mustAdv && m.caps[0] == pos) {
				ok = false
				break
			}
			return pos, true
		}
		if ok {
			continue
		}
		var found bool
		pc, pos, found = m.backtrack(base)
		if !found {
			return 0, false
		}
	}
}
