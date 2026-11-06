package pyast

// startsExpr reports whether t can begin an expression.
func (p *Parser) startsExpr(t Token) bool {
	switch t.Kind {
	case TNumber, TString:
		return true
	case TName:
		s := string(p.S.Src[t.Start:t.End])
		if !reserved[s] {
			return true
		}
		switch s {
		case "None", "True", "False", "not", "lambda", "await", "yield":
			return true
		}
		return false
	case TOp:
		switch string(p.S.Src[t.Start:t.End]) {
		case "(", "[", "{", "-", "+", "~", "...", "*":
			return true
		}
	}
	return false
}

func (p *Parser) parseStarExpressions() *Node {
	return p.parseTupleOf(p.parseStarExpression)
}

func (p *Parser) parseStarExpression() *Node {
	if p.atOp("*") {
		start := p.cur().Start
		p.advance()
		v := p.parseBitOr()
		return p.done(New("Starred", v, load()), start)
	}
	return p.parseExpression()
}

func (p *Parser) parseStarNamed() *Node {
	if p.atOp("*") {
		start := p.cur().Start
		p.advance()
		v := p.parseBitOr()
		return p.done(New("Starred", v, load()), start)
	}
	return p.parseNamedExpr()
}

func (p *Parser) parseTupleOf(elem func() *Node) *Node {
	start := p.cur().Start
	first := elem()
	if !p.atOp(",") {
		return first
	}
	elts := []*Node{first}
	for p.acceptOp(",") {
		if !p.startsExpr(p.cur()) {
			break
		}
		elts = append(elts, elem())
	}
	return p.done(New("Tuple", elts, load()), start)
}

func (p *Parser) parseStarTargets() *Node { return p.parseTupleOf(p.parseStarTarget) }

func (p *Parser) parseStarTarget() *Node {
	if p.atOp("*") {
		start := p.cur().Start
		p.advance()
		v := p.parseBitOr()
		return p.done(New("Starred", v, load()), start)
	}
	return p.parseBitOr()
}

func (p *Parser) parseYield() *Node {
	start := p.cur().Start
	p.expectKw("yield")
	if p.acceptKw("from") {
		v := p.parseExpression()
		return p.done(New("YieldFrom", v), start)
	}
	var v any
	if p.startsExpr(p.cur()) {
		v = p.parseStarExpressions()
	}
	return p.done(New("Yield", v), start)
}

func (p *Parser) parseNamedExpr() *Node {
	t := p.cur()
	if t.Kind == TName && !p.isReserved(t) && p.isOp(p.peek(1), ":=") {
		name := p.txt(t)
		p.advance()
		p.advance()
		target := p.doneRange(New("Name", name, store()), t.Start, t.End)
		v := p.parseExpression()
		return p.done(New("NamedExpr", target, v), t.Start)
	}
	return p.parseExpression()
}

func (p *Parser) parseExpression() *Node {
	if p.atKw("lambda") {
		return p.parseLambda()
	}
	start := p.cur().Start
	body := p.parseDisjunction()
	if p.atKw("if") {
		p.advance()
		test := p.parseDisjunction()
		p.expectKw("else")
		orelse := p.parseExpression()
		return p.done(New("IfExp", test, body, orelse), start)
	}
	return body
}

func (p *Parser) parseLambda() *Node {
	start := p.cur().Start
	p.advance()
	args := p.parseParams(true)
	p.expectOp(":")
	body := p.parseExpression()
	return p.done(New("Lambda", args, body), start)
}

func (p *Parser) parseDisjunction() *Node {
	start := p.cur().Start
	first := p.parseConjunction()
	if !p.atKw("or") {
		return first
	}
	vals := []*Node{first}
	for p.acceptKw("or") {
		vals = append(vals, p.parseConjunction())
	}
	return p.done(New("BoolOp", New("Or"), vals), start)
}

func (p *Parser) parseConjunction() *Node {
	start := p.cur().Start
	first := p.parseInversion()
	if !p.atKw("and") {
		return first
	}
	vals := []*Node{first}
	for p.acceptKw("and") {
		vals = append(vals, p.parseInversion())
	}
	return p.done(New("BoolOp", New("And"), vals), start)
}

func (p *Parser) parseInversion() *Node {
	if p.atKw("not") {
		start := p.cur().Start
		p.advance()
		v := p.parseInversion()
		return p.done(New("UnaryOp", New("Not"), v), start)
	}
	return p.parseComparison()
}

func (p *Parser) compareOp() (string, bool) {
	t := p.cur()
	switch t.Kind {
	case TOp:
		switch s := p.txt(t); s {
		case "==":
			return "Eq", true
		case "!=":
			return "NotEq", true
		case "<":
			return "Lt", true
		case "<=":
			return "LtE", true
		case ">":
			return "Gt", true
		case ">=":
			return "GtE", true
		}
	case TName:
		switch p.txt(t) {
		case "in":
			return "In", true
		case "is":
			if p.isKw(p.peek(1), "not") {
				return "IsNot", true
			}
			return "Is", true
		case "not":
			if p.isKw(p.peek(1), "in") {
				return "NotIn", true
			}
		}
	}
	return "", false
}

func (p *Parser) parseComparison() *Node {
	start := p.cur().Start
	left := p.parseBitOr()
	op, ok := p.compareOp()
	if !ok {
		return left
	}
	var ops, comps []*Node
	for ok {
		p.advance()
		if op == "IsNot" || op == "NotIn" {
			p.advance()
		}
		ops = append(ops, New(op))
		comps = append(comps, p.parseBitOr())
		op, ok = p.compareOp()
	}
	return p.done(New("Compare", left, ops, comps), start)
}

func (p *Parser) binLevel(next func() *Node, ops map[string]string) *Node {
	start := p.cur().Start
	left := next()
	for p.cur().Kind == TOp {
		op, ok := ops[p.txt(p.cur())]
		if !ok {
			break
		}
		p.advance()
		right := next()
		left = p.done(New("BinOp", left, New(op), right), start)
	}
	return left
}

var (
	opsOr    = map[string]string{"|": "BitOr"}
	opsXor   = map[string]string{"^": "BitXor"}
	opsAnd   = map[string]string{"&": "BitAnd"}
	opsShift = map[string]string{"<<": "LShift", ">>": "RShift"}
	opsSum   = map[string]string{"+": "Add", "-": "Sub"}
	opsTerm  = map[string]string{"*": "Mult", "/": "Div", "//": "FloorDiv", "%": "Mod", "@": "MatMult"}
)

func (p *Parser) parseBitOr() *Node  { return p.binLevel(p.parseBitXor, opsOr) }
func (p *Parser) parseBitXor() *Node { return p.binLevel(p.parseBitAnd, opsXor) }
func (p *Parser) parseBitAnd() *Node { return p.binLevel(p.parseShift, opsAnd) }
func (p *Parser) parseShift() *Node  { return p.binLevel(p.parseSum, opsShift) }
func (p *Parser) parseSum() *Node    { return p.binLevel(p.parseTerm, opsSum) }
func (p *Parser) parseTerm() *Node   { return p.binLevel(p.parseFactor, opsTerm) }

func (p *Parser) parseFactor() *Node {
	t := p.cur()
	if t.Kind == TOp {
		var op string
		switch p.txt(t) {
		case "+":
			op = "UAdd"
		case "-":
			op = "USub"
		case "~":
			op = "Invert"
		}
		if op != "" {
			p.advance()
			v := p.parseFactor()
			return p.done(New("UnaryOp", New(op), v), t.Start)
		}
	}
	return p.parsePower()
}

func (p *Parser) parsePower() *Node {
	start := p.cur().Start
	left := p.parseAwaitPrimary()
	if p.atOp("**") {
		p.advance()
		right := p.parseFactor()
		return p.done(New("BinOp", left, New("Pow"), right), start)
	}
	return left
}

func (p *Parser) parseAwaitPrimary() *Node {
	if p.atKw("await") {
		start := p.cur().Start
		p.advance()
		v := p.parsePrimary()
		return p.done(New("Await", v), start)
	}
	return p.parsePrimary()
}

func (p *Parser) parsePrimary() *Node {
	start := p.cur().Start
	n := p.parseAtom()
	for {
		t := p.cur()
		if t.Kind != TOp {
			return n
		}
		switch p.txt(t) {
		case ".":
			p.advance()
			attr := p.parseName()
			n = p.done(New("Attribute", n, attr, load()), start)
		case "(":
			p.advance()
			args, kws := p.parseCallArgs(t.Start)
			p.expectOp(")")
			n = p.done(New("Call", n, args, kws), start)
		case "[":
			p.advance()
			sl := p.parseSlices()
			p.expectOp("]")
			n = p.done(New("Subscript", n, sl, load()), start)
		default:
			return n
		}
	}
}

func (p *Parser) parseCallArgs(lp int) (args, kws []*Node) {
	for !p.atOp(")") {
		t := p.cur()
		start := t.Start
		switch {
		case p.isOp(t, "*"):
			p.advance()
			v := p.parseExpression()
			args = append(args, p.done(New("Starred", v, load()), start))
		case p.isOp(t, "**"):
			p.advance()
			v := p.parseExpression()
			kws = append(kws, p.done(New("keyword", nil, v), start))
		case t.Kind == TName && !p.isReserved(t) && p.isOp(p.peek(1), "="):
			name := p.txt(t)
			p.advance()
			p.advance()
			v := p.parseExpression()
			kws = append(kws, p.done(New("keyword", name, v), start))
		default:
			e := p.parseNamedExpr()
			if p.atKw("for") || (p.atKw("async") && p.isKw(p.peek(1), "for")) {
				gens := p.parseCompFor()
				if !p.atOp(")") || len(args) > 0 || len(kws) > 0 {
					p.failAt("SyntaxError", "Generator expression must be parenthesized", start)
				}
				g := p.doneRange(New("GeneratorExp", e, gens), lp, p.cur().End)
				return []*Node{g}, nil
			}
			args = append(args, e)
		}
		if !p.acceptOp(",") {
			break
		}
	}
	return
}

func (p *Parser) parseSlices() *Node {
	start := p.cur().Start
	var elts []*Node
	comma := false
	for {
		elts = append(elts, p.parseSliceItem())
		if !p.acceptOp(",") {
			break
		}
		comma = true
		if p.atOp("]") {
			break
		}
	}
	if len(elts) == 1 && !comma {
		return elts[0]
	}
	return p.done(New("Tuple", elts, load()), start)
}

func (p *Parser) parseSliceItem() *Node {
	if p.atOp("*") {
		start := p.cur().Start
		p.advance()
		v := p.parseBitOr()
		return p.done(New("Starred", v, load()), start)
	}
	start := p.cur().Start
	var lower, upper, step any
	if !p.atOp(":") {
		e := p.parseNamedExpr()
		if !p.atOp(":") {
			return e
		}
		lower = e
	}
	p.expectOp(":")
	end := func() bool { return p.atOp(":") || p.atOp(",") || p.atOp("]") }
	if !end() {
		upper = p.parseExpression()
	}
	if p.acceptOp(":") {
		if !p.atOp(",") && !p.atOp("]") {
			step = p.parseExpression()
		}
	}
	return p.done(New("Slice", lower, upper, step), start)
}

func (p *Parser) parseCompFor() []*Node {
	var gens []*Node
	for {
		async := 0
		if p.atKw("async") && p.isKw(p.peek(1), "for") {
			p.advance()
			async = 1
		} else if !p.atKw("for") {
			break
		}
		p.expectKw("for")
		target := p.parseStarTargets()
		p.setContext(target, store())
		p.expectKw("in")
		iter := p.parseDisjunction()
		var ifs []*Node
		for p.acceptKw("if") {
			ifs = append(ifs, p.parseDisjunction())
		}
		gens = append(gens, New("comprehension", target, iter, ifs, async))
	}
	return gens
}

func (p *Parser) atCompFor() bool {
	return p.atKw("for") || (p.atKw("async") && p.isKw(p.peek(1), "for"))
}

func (p *Parser) parseAtom() *Node {
	t := p.cur()
	start := t.Start
	switch t.Kind {
	case TName:
		s := p.txt(t)
		switch s {
		case "None":
			p.advance()
			return p.done(New("Constant", NoneT{}, nil), start)
		case "True":
			p.advance()
			return p.done(New("Constant", true, nil), start)
		case "False":
			p.advance()
			return p.done(New("Constant", false, nil), start)
		}
		if reserved[s] {
			p.failSyntax()
		}
		p.advance()
		return p.done(New("Name", s, load()), start)
	case TNumber:
		p.advance()
		v := p.parseNumber(t)
		return p.done(New("Constant", v, nil), start)
	case TString:
		return p.parseStrings()
	case TOp:
		switch p.txt(t) {
		case "...":
			p.advance()
			return p.done(New("Constant", EllipsisT{}, nil), start)
		case "(":
			return p.parseParenAtom()
		case "[":
			return p.parseListAtom()
		case "{":
			return p.parseBraceAtom()
		}
	}
	p.failSyntax()
	return nil
}

func (p *Parser) parseParenAtom() *Node {
	start := p.cur().Start
	p.advance()
	if p.atOp(")") {
		p.advance()
		return p.done(New("Tuple", []*Node(nil), load()), start)
	}
	if p.atKw("yield") {
		y := p.parseYield()
		p.expectOp(")")
		return y
	}
	first := p.parseStarNamed()
	if p.atCompFor() {
		if first.Spec.Name == "Starred" && false {
			p.failSyntax()
		}
		gens := p.parseCompFor()
		p.expectOp(")")
		return p.done(New("GeneratorExp", first, gens), start)
	}
	if p.atOp(",") {
		elts := []*Node{first}
		for p.acceptOp(",") {
			if p.atOp(")") {
				break
			}
			elts = append(elts, p.parseStarNamed())
		}
		p.expectOp(")")
		return p.done(New("Tuple", elts, load()), start)
	}
	p.expectOp(")")
	if first.Spec.Name == "Starred" {
		p.failAtNode("cannot use starred expression here", first)
	}
	return first
}

func (p *Parser) parseListAtom() *Node {
	start := p.cur().Start
	p.advance()
	if p.atOp("]") {
		p.advance()
		return p.done(New("List", []*Node(nil), load()), start)
	}
	first := p.parseStarNamed()
	if p.atCompFor() {
		gens := p.parseCompFor()
		p.expectOp("]")
		return p.done(New("ListComp", first, gens), start)
	}
	elts := []*Node{first}
	for p.acceptOp(",") {
		if p.atOp("]") {
			break
		}
		elts = append(elts, p.parseStarNamed())
	}
	p.expectOp("]")
	return p.done(New("List", elts, load()), start)
}

func (p *Parser) parseBraceAtom() *Node {
	start := p.cur().Start
	p.advance()
	if p.atOp("}") {
		p.advance()
		return p.done(New("Dict", []*Node(nil), []*Node(nil)), start)
	}
	// dict or set
	if p.atOp("**") {
		return p.parseDictRest(start, nil, nil, true)
	}
	first := p.parseStarNamed()
	if p.atOp(":") && first.Spec.Name != "Starred" {
		p.advance()
		v := p.parseExpression()
		if p.atCompFor() {
			gens := p.parseCompFor()
			p.expectOp("}")
			return p.done(New("DictComp", first, v, gens), start)
		}
		return p.parseDictRest(start, []*Node{first}, []*Node{v}, false)
	}
	if p.atCompFor() {
		gens := p.parseCompFor()
		p.expectOp("}")
		return p.done(New("SetComp", first, gens), start)
	}
	elts := []*Node{first}
	for p.acceptOp(",") {
		if p.atOp("}") {
			break
		}
		elts = append(elts, p.parseStarNamed())
	}
	p.expectOp("}")
	return p.done(New("Set", elts), start)
}

// parseDictRest continues a dict display after the first key/value pair (or at
// a leading '**').
func (p *Parser) parseDictRest(start int, keys, vals []*Node, atStar bool) *Node {
	more := !atStar
	if more {
		if !p.acceptOp(",") {
			p.expectOp("}")
			return p.done(New("Dict", keys, vals), start)
		}
	}
	for !p.atOp("}") {
		if p.atOp("**") {
			p.advance()
			v := p.parseBitOr()
			if len(keys) == 0 && p.atCompFor() {
				gens := p.parseCompFor()
				p.expectOp("}")
				return p.done(New("DictComp", v, nil, gens), start)
			}
			keys = append(keys, nil)
			vals = append(vals, v)
		} else {
			k := p.parseExpression()
			p.expectOp(":")
			v := p.parseExpression()
			keys = append(keys, k)
			vals = append(vals, v)
		}
		if !p.acceptOp(",") {
			break
		}
	}
	p.expectOp("}")
	return p.done(New("Dict", keys, vals), start)
}
