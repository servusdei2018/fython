package pyast

func (p *Parser) tryMatchStmt() *Node {
	start := p.cur().Start
	var subject *Node
	ok := p.try(func() {
		p.advance() // match
		if !p.startsExpr(p.cur()) {
			p.failSyntax()
		}
		subject = p.parseTupleOf(p.parseStarNamed)
		p.expectOp(":")
		if p.cur().Kind != TNewline {
			p.failSyntax()
		}
		p.advance()
		if p.cur().Kind != TIndent || !p.isKw(p.peek(1), "case") {
			p.failSyntax()
		}
	})
	if !ok {
		return nil
	}
	p.advance() // INDENT
	var cases []*Node
	for p.atKw("case") {
		p.advance()
		pat := p.parsePatterns()
		var guard any
		if p.acceptKw("if") {
			guard = p.parseNamedExpr()
		}
		body := p.parseBlock()
		cases = append(cases, New("match_case", pat, guard, body))
	}
	if p.cur().Kind == TDedent {
		p.advance()
	} else if p.cur().Kind != TEOF {
		p.failSyntax()
	}
	return p.done(New("Match", subject, cases), start)
}

func (p *Parser) parsePatterns() *Node {
	start := p.cur().Start
	first := p.parseMaybeStarPattern()
	if !p.atOp(",") {
		if first.Spec.Name == "MatchStar" {
			p.failAtNode("star pattern can only be used inside a sequence pattern", first)
		}
		return first
	}
	pats := []*Node{first}
	for p.acceptOp(",") {
		if p.atOp(":") || p.atKw("if") {
			break
		}
		pats = append(pats, p.parseMaybeStarPattern())
	}
	return p.done(New("MatchSequence", pats), start)
}

func (p *Parser) parseMaybeStarPattern() *Node {
	if p.atOp("*") {
		start := p.cur().Start
		p.advance()
		name := p.parseName()
		var nm any
		if name != "_" {
			nm = name
		}
		return p.done(New("MatchStar", nm), start)
	}
	return p.parsePattern()
}

func (p *Parser) parsePattern() *Node {
	start := p.cur().Start
	pat := p.parseOrPattern()
	if p.acceptKw("as") {
		name := p.parseName()
		if name == "_" {
			p.failAt("SyntaxError", "cannot use '_' as a target", p.toks[p.i-1].Start)
		}
		return p.done(New("MatchAs", pat, name), start)
	}
	return pat
}

func (p *Parser) parseOrPattern() *Node {
	start := p.cur().Start
	first := p.parseClosedPattern()
	if !p.atOp("|") {
		return first
	}
	pats := []*Node{first}
	for p.acceptOp("|") {
		pats = append(pats, p.parseClosedPattern())
	}
	return p.done(New("MatchOr", pats), start)
}

func (p *Parser) parseSignedNumber() *Node {
	start := p.cur().Start
	neg := false
	if p.atOp("-") {
		neg = true
		p.advance()
	}
	t := p.cur()
	if t.Kind != TNumber {
		p.failSyntax()
	}
	p.advance()
	n := p.done(New("Constant", p.parseNumber(t), nil), t.Start)
	if neg {
		n = p.done(New("UnaryOp", New("USub"), n), start)
	}
	return n
}

func (p *Parser) parseLiteralExpr() *Node {
	start := p.cur().Start
	left := p.parseSignedNumber()
	if p.atOp("+") || p.atOp("-") {
		op := "Add"
		if p.atOp("-") {
			op = "Sub"
		}
		p.advance()
		t := p.cur()
		if t.Kind != TNumber {
			p.failSyntax()
		}
		p.advance()
		right := p.done(New("Constant", p.parseNumber(t), nil), t.Start)
		return p.done(New("BinOp", left, New(op), right), start)
	}
	return left
}

func (p *Parser) parseClosedPattern() *Node {
	t := p.cur()
	start := t.Start
	switch t.Kind {
	case TNumber:
		return p.done(New("MatchValue", p.parseLiteralExpr()), start)
	case TString:
		s := p.parseStrings()
		return p.done(New("MatchValue", s), start)
	case TOp:
		switch p.txt(t) {
		case "-":
			return p.done(New("MatchValue", p.parseLiteralExpr()), start)
		case "(":
			p.advance()
			if p.atOp(")") {
				p.advance()
				return p.done(New("MatchSequence", []*Node(nil)), start)
			}
			first := p.parseMaybeStarPattern()
			if p.atOp(",") {
				pats := []*Node{first}
				for p.acceptOp(",") {
					if p.atOp(")") {
						break
					}
					pats = append(pats, p.parseMaybeStarPattern())
				}
				p.expectOp(")")
				return p.done(New("MatchSequence", pats), start)
			}
			p.expectOp(")")
			if first.Spec.Name == "MatchStar" {
				return p.done(New("MatchSequence", []*Node{first}), start)
			}
			return first
		case "[":
			p.advance()
			var pats []*Node
			for !p.atOp("]") {
				pats = append(pats, p.parseMaybeStarPattern())
				if !p.acceptOp(",") {
					break
				}
			}
			p.expectOp("]")
			return p.done(New("MatchSequence", pats), start)
		case "{":
			return p.parseMappingPattern()
		}
	case TName:
		switch s := p.txt(t); s {
		case "None":
			p.advance()
			return p.done(New("MatchSingleton", NoneT{}), start)
		case "True":
			p.advance()
			return p.done(New("MatchSingleton", true), start)
		case "False":
			p.advance()
			return p.done(New("MatchSingleton", false), start)
		}
		name := p.parseName()
		expr := p.done(New("Name", name, load()), start)
		dotted := false
		for p.atOp(".") {
			p.advance()
			attr := p.parseName()
			expr = p.done(New("Attribute", expr, attr, load()), start)
			dotted = true
		}
		if p.atOp("(") {
			return p.parseClassPattern(expr, start)
		}
		if dotted {
			return p.done(New("MatchValue", expr), start)
		}
		if name == "_" {
			return p.done(New("MatchAs", nil, nil), start)
		}
		return p.done(New("MatchAs", nil, name), start)
	}
	p.failSyntax()
	return nil
}

func (p *Parser) parseClassPattern(cls *Node, start int) *Node {
	p.expectOp("(")
	var pats, kwPats []*Node
	var kwAttrs []string
	for !p.atOp(")") {
		t := p.cur()
		if t.Kind == TName && !p.isReserved(t) && p.isOp(p.peek(1), "=") {
			name := p.txt(t)
			p.advance()
			p.advance()
			kwAttrs = append(kwAttrs, name)
			kwPats = append(kwPats, p.parsePattern())
		} else {
			if len(kwAttrs) > 0 {
				p.failAt("SyntaxError", "positional patterns follow keyword patterns", t.Start)
			}
			pats = append(pats, p.parsePattern())
		}
		if !p.acceptOp(",") {
			break
		}
	}
	p.expectOp(")")
	return p.done(New("MatchClass", cls, pats, kwAttrs, kwPats), start)
}

func (p *Parser) parseMappingPattern() *Node {
	start := p.cur().Start
	p.expectOp("{")
	var keys, pats []*Node
	var rest any
	for !p.atOp("}") {
		if p.atOp("**") {
			p.advance()
			rest = p.parseName()
			p.acceptOp(",")
			break
		}
		keys = append(keys, p.parseMappingKey())
		p.expectOp(":")
		pats = append(pats, p.parsePattern())
		if !p.acceptOp(",") {
			break
		}
	}
	p.expectOp("}")
	return p.done(New("MatchMapping", keys, pats, rest), start)
}

func (p *Parser) parseMappingKey() *Node {
	t := p.cur()
	start := t.Start
	switch {
	case t.Kind == TNumber || p.isOp(t, "-"):
		return p.parseLiteralExpr()
	case t.Kind == TString:
		return p.parseStrings()
	case t.Kind == TName:
		switch p.txt(t) {
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
		name := p.parseName()
		var expr = p.done(New("Name", name, load()), start)
		if !p.atOp(".") {
			p.failSyntax()
		}
		for p.atOp(".") {
			p.advance()
			attr := p.parseName()
			expr = p.done(New("Attribute", expr, attr, load()), start)
		}
		return expr
	}
	p.failSyntax()
	return nil
}
