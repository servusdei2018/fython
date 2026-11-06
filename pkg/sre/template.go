package sre

import (
	"strconv"
	"strings"
)

// TemplatePart is either a literal run or a group reference.
type TemplatePart struct {
	Lit   []rune
	Group int // -1 for a literal part
}

// Template is a parsed replacement template.
type Template struct {
	Parts []TemplatePart
}

// ParseTemplate parses a re.sub replacement string for use with re.
func (re *Regexp) ParseTemplate(repl []rune) (*Template, error) {
	t := &Template{}
	var lit []rune
	flush := func() {
		if len(lit) > 0 {
			t.Parts = append(t.Parts, TemplatePart{Lit: lit, Group: -1})
			lit = nil
		}
	}
	addGroup := func(g, pos int) error {
		if g < 0 || g > re.NumGroups {
			return &Error{Msg: "invalid group reference " + strconv.Itoa(g), Pos: pos}
		}
		flush()
		t.Parts = append(t.Parts, TemplatePart{Group: g})
		return nil
	}
	i := 0
	for i < len(repl) {
		c := repl[i]
		if c != '\\' {
			lit = append(lit, c)
			i++
			continue
		}
		start := i
		i++
		if i >= len(repl) {
			return nil, &Error{Msg: "bad escape (end of pattern)", Pos: start}
		}
		c = repl[i]
		i++
		switch {
		case c == 'g':
			if i >= len(repl) || repl[i] != '<' {
				return nil, &Error{Msg: "missing <", Pos: i}
			}
			i++
			j := i
			for j < len(repl) && repl[j] != '>' {
				j++
			}
			if j >= len(repl) {
				return nil, &Error{Msg: "missing >, unterminated name", Pos: i}
			}
			name := string(repl[i:j])
			i = j + 1
			if name == "" {
				return nil, &Error{Msg: "missing group name", Pos: j}
			}
			if validIdent(name) {
				g, ok := re.GroupIndex[name]
				if !ok {
					return nil, &Error{Msg: "unknown group name " + pyQuote(name), Pos: j - len(name)}
				}
				if err := addGroup(g, j-len(name)); err != nil {
					return nil, err
				}
			} else {
				n, err := strconv.Atoi(strings.TrimSpace(name))
				if err != nil || strings.ContainsAny(name, "+-_") && !strings.HasPrefix(strings.TrimSpace(name), "+") {
					return nil, &Error{Msg: "bad character in group name " + pyQuote(name), Pos: j - len(name)}
				}
				if err := addGroup(n, j-len(name)); err != nil {
					return nil, err
				}
			}
		case c == '0':
			v := rune(0)
			for k := 0; k < 2 && i < len(repl) && isOct(repl[i]); k++ {
				v = v*8 + (repl[i] - '0')
				i++
			}
			lit = append(lit, v&0xff)
		case c >= '1' && c <= '9':
			if i < len(repl) && isDigit(repl[i]) {
				d := repl[i]
				i++
				if isOct(c) && isOct(d) && i < len(repl) && isOct(repl[i]) {
					v := (c-'0')*64 + (d-'0')*8 + (repl[i] - '0')
					i++
					if v > 0377 {
						return nil, &Error{Msg: "octal escape value " + string(repl[start:i]) + " outside of range 0-0o377", Pos: start}
					}
					lit = append(lit, v)
					continue
				}
				if err := addGroup(int(c-'0')*10+int(d-'0'), start); err != nil {
					return nil, err
				}
			} else if err := addGroup(int(c-'0'), start); err != nil {
				return nil, err
			}
		default:
			switch c {
			case 'a':
				lit = append(lit, 7)
			case 'b':
				lit = append(lit, 8)
			case 'f':
				lit = append(lit, 12)
			case 'n':
				lit = append(lit, 10)
			case 'r':
				lit = append(lit, 13)
			case 't':
				lit = append(lit, 9)
			case 'v':
				lit = append(lit, 11)
			case '\\':
				lit = append(lit, '\\')
			default:
				if isASCIILetter(c) {
					return nil, &Error{Msg: "bad escape \\" + string(c), Pos: start}
				}
				lit = append(lit, '\\', c)
			}
		}
	}
	flush()
	return t, nil
}

// Expand appends the expansion of the template for a match.
func (t *Template) Expand(dst []rune, input []rune, caps []int) []rune {
	for _, p := range t.Parts {
		if p.Group < 0 {
			dst = append(dst, p.Lit...)
			continue
		}
		s, e := caps[2*p.Group], caps[2*p.Group+1]
		if s >= 0 && e >= 0 {
			dst = append(dst, input[s:e]...)
		}
	}
	return dst
}
