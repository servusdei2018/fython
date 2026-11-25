package sre

import "bytes"

type asciiMatcher struct {
	re      *Regexp
	prog    []inst
	in      []byte
	end     int
	caps    []int
	regs    []int
	last    int
	stack   []entry
	full    int
	mustAdv int
}

func (cs *charSet) hasByte(b byte) bool {
	if b < 128 {
		return cs.table[b]
	}
	return (cs.latinBits[b>>3]>>(uint(b)&7))&1 != 0
}

// ExecASCIIInto runs the regexp directly against an ASCII or single-byte subject
// slice without allocating or decoding a []rune buffer, copying capture bounds into dst.
func (re *Regexp) ExecASCIIInto(input []byte, pos, endpos int, mode Mode, mustAdvance bool, dst []int32) ([]int32, int, bool) {
	return re.ExecASCIIIntoWithState(nil, input, pos, endpos, mode, mustAdvance, dst)
}

// ExecASCIIIntoWithState is ExecASCIIInto using an optional caller-owned ExecState
// to avoid per-call sync.Pool.Get/Put and defer overhead on single-threaded VM hot paths.
func (re *Regexp) ExecASCIIIntoWithState(st *ExecState, input []byte, pos, endpos int, mode Mode, mustAdvance bool, dst []int32) ([]int32, int, bool) {
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
		return nil, -1, false
	}
	if re.nonASCIIPrefix && !re.IsBytes {
		return nil, -1, false
	}
	if (mode == ModeMatch || mode == ModeFullMatch) && pos < endpos {
		b := input[pos]
		if re.hasFirst {
			ascii := re.Flags&FlagASCII != 0
			if re.firstFold {
				if canon(rune(b), ascii) != canon(re.firstLit, ascii) {
					return nil, -1, false
				}
			} else {
				if uint32(re.firstLit) >= 256 || b != byte(re.firstLit) {
					return nil, -1, false
				}
				if len(re.asciiPrefix) > 1 {
					if pos+len(re.asciiPrefix) > endpos || !bytes.Equal(input[pos:pos+len(re.asciiPrefix)], re.asciiPrefix) {
						return nil, -1, false
					}
				} else if len(re.prefix) > 1 {
					if pos+len(re.prefix) > endpos {
						return nil, -1, false
					}
					for i := 1; i < len(re.prefix); i++ {
						if rune(input[pos+i]) != re.prefix[i] {
							return nil, -1, false
						}
					}
				}
			}
		} else if re.hasFirstMask {
			if (re.firstMask[b>>6]>>(uint(b)&63))&1 == 0 {
				return nil, -1, false
			}
		}
	}

	var m *asciiMatcher
	fromPool := false
	if st != nil {
		m = st.ascii
		if m == nil {
			m = &asciiMatcher{
				caps: make([]int, 2*(re.NumGroups+1)),
				regs: make([]int, re.nregs),
			}
			st.ascii = m
		}
	} else {
		fromPool = true
		mm, _ := re.asciiMatcherPool.Get().(*asciiMatcher)
		m = mm
		if m == nil {
			m = &asciiMatcher{
				re:   re,
				prog: re.prog,
				caps: make([]int, 2*(re.NumGroups+1)),
				regs: make([]int, re.nregs),
			}
		}
	}
	m.re = re
	m.prog = re.prog
	m.in = input
	m.end = endpos
	m.last = -1
	m.full = -1
	m.mustAdv = -1
	needCaps := 2 * (re.NumGroups + 1)
	if cap(m.caps) < needCaps {
		m.caps = make([]int, needCaps)
	} else {
		m.caps = m.caps[:needCaps]
	}
	for i := range m.caps {
		m.caps[i] = -1
	}
	if cap(m.regs) < re.nregs {
		m.regs = make([]int, re.nregs)
	} else {
		m.regs = m.regs[:re.nregs]
	}
	for i := range m.regs {
		m.regs[i] = 0
	}
	m.stack = m.stack[:0]
	if mode == ModeFullMatch {
		m.full = endpos
	}
	if mustAdvance {
		m.mustAdv = pos
	}

	release := func() {
		m.in = nil
		m.re = nil
		m.prog = nil
		if fromPool {
			re.asciiMatcherPool.Put(m)
		}
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
			release()
			return nil, -1, false
		}
	default:
		if re.bosOnly {
			if pos != 0 || !try(0) {
				release()
				return nil, -1, false
			}
			break
		}
		found := false
		for s := pos; s <= endpos; s++ {
			if re.hasFirst {
				ascii := re.Flags&FlagASCII != 0
				switch {
				case re.firstFold:
					want := canon(re.firstLit, ascii)
					for s < endpos {
						if canon(rune(input[s]), ascii) == want {
							break
						}
						s++
					}
				case len(re.asciiPrefix) > 1:
					if s+len(re.asciiPrefix) > endpos {
						s = endpos
						break
					}
					if idx := bytes.Index(input[s:endpos], re.asciiPrefix); idx >= 0 {
						s += idx
					} else {
						s = endpos
					}
				case len(re.prefix) > 1:
					pref := re.prefix
					plen := len(pref)
					if uint32(pref[0]) >= 256 {
						s = endpos
						break
					}
					first := byte(pref[0])
				scanPref:
					for s+plen <= endpos {
						if idx := bytes.IndexByte(input[s:endpos-plen+1], first); idx >= 0 {
							s += idx
							for i := 1; i < plen; i++ {
								if rune(input[s+i]) != pref[i] {
									s++
									continue scanPref
								}
							}
							break
						}
						s = endpos
						break
					}
					if s+plen > endpos {
						s = endpos
					}
				default:
					if uint32(re.firstLit) >= 256 {
						s = endpos
						break
					}
					if s < endpos {
						if idx := bytes.IndexByte(input[s:endpos], byte(re.firstLit)); idx >= 0 {
							s += idx
						} else {
							s = endpos
						}
					}
				}
				if s >= endpos {
					break
				}
			} else if re.hasFirstMask {
				for s < endpos {
					b := input[s]
					if (re.firstMask[b>>6]>>(uint(b)&63))&1 != 0 {
						break
					}
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
			release()
			return nil, -1, false
		}
	}

	n := len(m.caps)
	var out []int32
	if cap(dst) >= n {
		out = dst[:n]
	} else {
		out = make([]int32, n)
	}
	for i, c := range m.caps {
		out[i] = int32(c)
	}
	last := m.last
	release()
	return out, last, true
}

func (m *asciiMatcher) setReg(i, v int) {
	m.stack = append(m.stack, entry{kind: ekUndoReg, a: i, b: m.regs[i]})
	m.regs[i] = v
}

func (m *asciiMatcher) single(in *inst, b byte) bool {
	switch in.sub {
	case opChar:
		return rune(b) == in.r
	case opCharFold:
		return canon(rune(b), in.flags&flAscii != 0) == in.r
	case opAny:
		return b != '\n'
	case opAnyNL:
		return true
	case opSet:
		return in.set.hasByte(b)
	}
	return false
}

func (m *asciiMatcher) unwind(top int) {
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

func (m *asciiMatcher) compact(top int) {
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

func (m *asciiMatcher) backtrack(base int) (pc, pos int, ok bool) {
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
				if next.op == opChar && (cur >= m.end || rune(m.in[cur]) != next.r) {
					cur--
					continue
				}
				if next.op == opSet && cur < m.end && !next.set.hasByte(m.in[cur]) {
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

func (m *asciiMatcher) atMatch(in *inst, pos int) bool {
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
		before := pos > 0 && isWordRune(rune(m.in[pos-1]), ascii)
		after := pos < m.end && isWordRune(rune(m.in[pos]), ascii)
		if atKind(in.x) == atWordB {
			return before != after
		}
		return before == after
	}
	return false
}

func (m *asciiMatcher) run(pc, pos int) (int, bool) {
	base := len(m.stack)
	for {
		in := &m.prog[pc]
		ok := true
		switch in.op {
		case opChar:
			if pos < m.end && rune(m.in[pos]) == in.r {
				pos++
				pc++
			} else {
				ok = false
			}
		case opCharFold:
			if pos < m.end && canon(rune(m.in[pos]), in.flags&flAscii != 0) == in.r {
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
			if pos < m.end && in.set.hasByte(m.in[pos]) {
				pos++
				pc++
			} else {
				ok = false
			}
		case opSplit:
			if pos < m.end {
				r := m.in[pos]
				if in.xHasMask && (in.xMask[r>>6]>>(uint(r)&63))&1 == 0 {
					pc = in.y
					break
				}
				if in.yHasMask && (in.yMask[r>>6]>>(uint(r)&63))&1 == 0 {
					pc = in.x
					break
				}
			}
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
				for i := range e - s {
					if canon(rune(m.in[s+i]), ascii) != canon(rune(m.in[pos+i]), ascii) {
						ok = false
						break
					}
				}
			} else if !bytes.Equal(m.in[s:e], m.in[pos:pos+e-s]) {
				ok = false
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
			switch {
			case neg:
				if sok {
					m.unwind(top)
					ok = false
				} else {
					pc = in.x
				}
			case !sok:
				ok = false
			default:
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
			subSlice := m.in[pos : pos+limit]
			switch in.sub {
			case opChar:
				if uint32(in.r) < 256 {
					n = scanByteEq(subSlice, byte(in.r))
				}
			case opCharFold:
				ascii := in.flags&flAscii != 0
				want := in.r
				for n < len(subSlice) && canon(rune(subSlice[n]), ascii) == want {
					n++
				}
			case opAny:
				if idx := bytes.IndexByte(subSlice, '\n'); idx >= 0 {
					n = idx
				} else {
					n = len(subSlice)
				}
			case opAnyNL:
				n = len(subSlice)
			case opSet:
				n = scanCharSetBytes(subSlice, in.set)
				if m.re.IsBytes && n < len(subSlice) && subSlice[n] >= 0x80 {
					for n < len(subSlice) && in.set.hasByte(subSlice[n]) {
						n++
					}
				}
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
