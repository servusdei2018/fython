package pyast

import (
	"strings"
)

// Mode selects the grammar start symbol, mirroring compile()'s mode argument.
type Mode int

const (
	ModeExec Mode = iota
	ModeEval
	ModeSingle
	ModeFuncType
)

// Parser is a recursive-descent parser producing CPython-shaped trees.
type Parser struct {
	S       *Source
	lx      *Lexer
	toks    []Token
	i       int
	lastEnd int // end offset of the last consumed non-layout token
	// featureVersion is accepted for API compatibility; all 3.15 syntax parses.
}

// Parse parses src in the given mode. Errors are *SyntaxError.
func Parse(src []byte, mode Mode) (n *Node, err error) {
	s := NewSource(src)
	p := &Parser{S: s, lx: NewLexer(s)}
	defer func() {
		if r := recover(); r != nil {
			if se, ok := r.(*SyntaxError); ok {
				n, err = nil, se
				return
			}
			panic(r)
		}
	}()
	switch mode {
	case ModeEval:
		return p.parseEvalMode(), nil
	case ModeSingle:
		return p.parseSingleMode(), nil
	case ModeFuncType:
		return p.parseFuncTypeMode(), nil
	}
	return p.parseModule(), nil
}

// ---- token plumbing -------------------------------------------------------

func (p *Parser) fill(k int) {
	for len(p.toks) <= p.i+k {
		p.toks = append(p.toks, p.lx.Next())
	}
}

func (p *Parser) cur() Token {
	p.fill(0)
	return p.toks[p.i]
}

func (p *Parser) peek(k int) Token {
	p.fill(k)
	return p.toks[p.i+k]
}

func (p *Parser) advance() Token {
	p.fill(0)
	t := p.toks[p.i]
	if t.Kind != TEOF {
		p.i++
	}
	switch t.Kind {
	case TNewline, TIndent, TDedent, TEOF:
	default:
		p.lastEnd = t.End
	}
	return t
}

type mark struct{ i, lastEnd int }

func (p *Parser) mark() mark   { return mark{p.i, p.lastEnd} }
func (p *Parser) reset(m mark) { p.i, p.lastEnd = m.i, m.lastEnd }

func (p *Parser) txt(t Token) string { return string(p.S.Src[t.Start:t.End]) }

func (p *Parser) isOp(t Token, s string) bool {
	return t.Kind == TOp && t.End-t.Start == len(s) && string(p.S.Src[t.Start:t.End]) == s
}

func (p *Parser) isKw(t Token, s string) bool {
	return t.Kind == TName && t.End-t.Start == len(s) && string(p.S.Src[t.Start:t.End]) == s
}

func (p *Parser) atOp(s string) bool { return p.isOp(p.cur(), s) }
func (p *Parser) atKw(s string) bool { return p.isKw(p.cur(), s) }

func (p *Parser) acceptOp(s string) bool {
	if p.atOp(s) {
		p.advance()
		return true
	}
	return false
}

func (p *Parser) acceptKw(s string) bool {
	if p.atKw(s) {
		p.advance()
		return true
	}
	return false
}

func (p *Parser) expectOp(s string) Token {
	if !p.atOp(s) {
		p.failExpected("'" + s + "'")
	}
	return p.advance()
}

func (p *Parser) expectKw(s string) Token {
	if !p.atKw(s) {
		p.failExpected("'" + s + "'")
	}
	return p.advance()
}

func (p *Parser) failExpected(what string) {
	t := p.cur()
	if t.Kind == TIndent {
		p.failAt("IndentationError", "unexpected indent", t.End)
	}
	p.failAt("SyntaxError", "expected "+what, t.Start)
}

func (p *Parser) failAt(kind, msg string, off int) {
	panic(p.S.syntaxErrAt(kind, msg, off))
}

func (p *Parser) fail(msg string) {
	t := p.cur()
	if t.Kind == TIndent {
		p.failAt("IndentationError", "unexpected indent", t.End)
	}
	p.failAt("SyntaxError", msg, t.Start)
}

func (p *Parser) failSyntax() { p.fail("invalid syntax") }

// try runs fn, returning false (with the parser state restored) if it raised
// a SyntaxError.
func (p *Parser) try(fn func()) (ok bool) {
	m := p.mark()
	defer func() {
		if r := recover(); r != nil {
			if _, is := r.(*SyntaxError); is {
				p.reset(m)
				ok = false
				return
			}
			panic(r)
		}
	}()
	fn()
	return true
}

var reserved = map[string]bool{
	"False": true, "None": true, "True": true, "and": true, "as": true, "assert": true, "async": true,
	"await": true, "break": true, "class": true, "continue": true, "def": true, "del": true, "elif": true,
	"else": true, "except": true, "finally": true, "for": true, "from": true, "global": true, "if": true,
	"import": true, "in": true, "is": true, "lambda": true, "nonlocal": true, "not": true, "or": true,
	"pass": true, "raise": true, "return": true, "try": true, "while": true, "with": true, "yield": true,
}

func (p *Parser) isReserved(t Token) bool {
	return t.Kind == TName && reserved[string(p.S.Src[t.Start:t.End])]
}

func (p *Parser) parseName() string {
	t := p.cur()
	if t.Kind != TName || p.isReserved(t) {
		p.failSyntax()
	}
	p.advance()
	return p.txt(t)
}

// ---- node helpers ---------------------------------------------------------

// done stamps n with the span [start, lastEnd).
func (p *Parser) done(n *Node, start int) *Node {
	l, c := p.S.Pos(start)
	el, ec := p.S.Pos(p.lastEnd)
	return n.SetPos(l, c, el, ec)
}

func (p *Parser) doneRange(n *Node, start, end int) *Node {
	l, c := p.S.Pos(start)
	el, ec := p.S.Pos(end)
	return n.SetPos(l, c, el, ec)
}

func load() *Node  { return New("Load") }
func store() *Node { return New("Store") }

// ---- module-level ---------------------------------------------------------

func (p *Parser) parseModule() *Node {
	var body []*Node
	for p.cur().Kind != TEOF {
		if p.cur().Kind == TNewline {
			p.advance()
			continue
		}
		body = append(body, p.parseStatement()...)
	}
	return New("Module", body, []*Node(nil))
}

func (p *Parser) parseEvalMode() *Node {
	for p.cur().Kind == TNewline {
		p.advance()
	}
	e := p.parseStarExpressions()
	for p.cur().Kind == TNewline {
		p.advance()
	}
	if p.cur().Kind != TEOF {
		p.failSyntax()
	}
	return New("Expression", e)
}

func (p *Parser) parseSingleMode() *Node {
	for p.cur().Kind == TNewline {
		p.advance()
	}
	var body []*Node
	if p.cur().Kind != TEOF {
		body = p.parseStatement()
	}
	for p.cur().Kind == TNewline {
		p.advance()
	}
	if p.cur().Kind != TEOF {
		p.failSyntax()
	}
	return New("Interactive", body)
}

func (p *Parser) parseFuncTypeMode() *Node {
	p.expectOp("(")
	var args []*Node
	for !p.atOp(")") {
		if p.atOp("*") || p.atOp("**") {
			st := p.cur().Start
			p.advance()
			v := p.parseExpression()
			args = append(args, p.done(New("Starred", v, load()), st))
		} else {
			args = append(args, p.parseExpression())
		}
		if !p.acceptOp(",") {
			break
		}
	}
	p.expectOp(")")
	p.expectOp("->")
	ret := p.parseExpression()
	for p.cur().Kind == TNewline {
		p.advance()
	}
	if p.cur().Kind != TEOF {
		p.failSyntax()
	}
	return New("FunctionType", args, ret)
}

// ---- statements -----------------------------------------------------------

// parseStatement parses one statement line (possibly several simple
// statements separated by ';').
func (p *Parser) parseStatement() []*Node {
	t := p.cur()
	switch t.Kind {
	case TIndent:
		p.failAt("IndentationError", "unexpected indent", t.End)
	case TName:
		switch p.txt(t) {
		case "if":
			return []*Node{p.parseIf()}
		case "while":
			return []*Node{p.parseWhile()}
		case "for":
			return []*Node{p.parseFor(false, t.Start)}
		case "try":
			return []*Node{p.parseTry()}
		case "with":
			return []*Node{p.parseWith(false, t.Start)}
		case "def":
			return []*Node{p.parseFuncDef(nil, false, t.Start)}
		case "class":
			return []*Node{p.parseClassDef(nil)}
		case "async":
			return []*Node{p.parseAsync(nil)}
		case "match":
			if n := p.tryMatchStmt(); n != nil {
				return []*Node{n}
			}
		}
	case TOp:
		if p.isOp(t, "@") {
			return []*Node{p.parseDecorated()}
		}
	}
	return p.parseSimpleStmts()
}

func (p *Parser) parseSimpleStmts() []*Node {
	var out []*Node
	for {
		out = append(out, p.parseSimpleStmt())
		if p.acceptOp(";") {
			if p.cur().Kind == TNewline || p.cur().Kind == TEOF {
				break
			}
			continue
		}
		break
	}
	switch p.cur().Kind {
	case TNewline:
		p.advance()
	case TEOF:
	default:
		p.failSyntax()
	}
	return out
}

func (p *Parser) parseBlock() []*Node {
	p.expectOp(":")
	if p.cur().Kind != TNewline {
		return p.parseSimpleStmts()
	}
	p.advance()
	if p.cur().Kind != TIndent {
		p.failAt("IndentationError", "expected an indented block", p.cur().Start)
	}
	p.advance()
	var body []*Node
	for p.cur().Kind != TDedent && p.cur().Kind != TEOF {
		if p.cur().Kind == TNewline {
			p.advance()
			continue
		}
		body = append(body, p.parseStatement()...)
	}
	if p.cur().Kind == TDedent {
		p.advance()
	}
	return body
}

func (p *Parser) parseIf() *Node {
	start := p.cur().Start
	p.advance() // if / elif
	test := p.parseNamedExpr()
	body := p.parseBlock()
	var orelse []*Node
	switch {
	case p.atKw("elif"):
		orelse = []*Node{p.parseIf()}
	case p.atKw("else"):
		p.advance()
		orelse = p.parseBlock()
	}
	return p.done(New("If", test, body, orelse), start)
}

func (p *Parser) parseWhile() *Node {
	start := p.cur().Start
	p.advance()
	test := p.parseNamedExpr()
	body := p.parseBlock()
	var orelse []*Node
	if p.acceptKw("else") {
		orelse = p.parseBlock()
	}
	return p.done(New("While", test, body, orelse), start)
}

func (p *Parser) parseFor(async bool, start int) *Node {
	p.expectKw("for")
	target := p.parseStarTargets()
	p.setContext(target, store())
	p.expectKw("in")
	iter := p.parseStarExpressions()
	body := p.parseBlock()
	var orelse []*Node
	if p.acceptKw("else") {
		orelse = p.parseBlock()
	}
	name := "For"
	if async {
		name = "AsyncFor"
	}
	return p.done(New(name, target, iter, body, orelse, nil), start)
}

func (p *Parser) parseTry() *Node {
	start := p.cur().Start
	p.advance()
	body := p.parseBlock()
	var handlers []*Node
	star := false
	for p.atKw("except") {
		hs := p.cur().Start
		p.advance()
		isStar := p.acceptOp("*")
		if len(handlers) > 0 && isStar != star {
			p.failAt("SyntaxError", "cannot have both 'except' and 'except*' on the same 'try'", hs)
		}
		star = isStar
		var typ *Node
		var name any
		if !p.atOp(":") {
			ts := p.cur().Start
			typ = p.parseExpression()
			if p.atOp(",") { // PEP 758: except A, B:
				elts := []*Node{typ}
				for p.acceptOp(",") {
					if p.atOp(":") || p.atKw("as") {
						break
					}
					elts = append(elts, p.parseExpression())
				}
				typ = p.done(New("Tuple", elts, load()), ts)
			}
			if p.acceptKw("as") {
				name = p.parseName()
			}
		} else if isStar {
			p.failAt("SyntaxError", "expected one or more exception types", p.cur().Start)
		}
		hb := p.parseBlock()
		var tn any
		if typ != nil {
			tn = typ
		}
		handlers = append(handlers, p.done(New("ExceptHandler", tn, name, hb), hs))
	}
	var orelse, final []*Node
	if len(handlers) > 0 && p.acceptKw("else") {
		orelse = p.parseBlock()
	}
	if p.acceptKw("finally") {
		final = p.parseBlock()
	}
	if len(handlers) == 0 && final == nil {
		p.failAt("SyntaxError", "expected 'except' or 'finally' block", p.cur().Start)
	}
	nm := "Try"
	if star {
		nm = "TryStar"
	}
	return p.done(New(nm, body, handlers, orelse, final), start)
}

func (p *Parser) parseWith(async bool, start int) *Node {
	p.expectKw("with")
	var items []*Node
	parsed := false
	if p.atOp("(") {
		parsed = p.try(func() {
			p.advance()
			var its []*Node
			for {
				its = append(its, p.parseWithItem())
				if !p.acceptOp(",") || p.atOp(")") {
					break
				}
			}
			p.expectOp(")")
			if !p.atOp(":") {
				p.failSyntax()
			}
			items = its
		})
	}
	if !parsed {
		for {
			items = append(items, p.parseWithItem())
			if !p.acceptOp(",") {
				break
			}
		}
	}
	body := p.parseBlock()
	name := "With"
	if async {
		name = "AsyncWith"
	}
	return p.done(New(name, items, body, nil), start)
}

func (p *Parser) parseWithItem() *Node {
	e := p.parseExpression()
	var vars any
	if p.acceptKw("as") {
		t := p.parseStarTarget()
		p.setContext(t, store())
		vars = t
	}
	return New("withitem", e, vars)
}

func (p *Parser) parseAsync(decorators []*Node) *Node {
	start := p.cur().Start
	p.advance()
	switch {
	case p.atKw("def"):
		return p.parseFuncDef(decorators, true, start)
	case p.atKw("for") && decorators == nil:
		return p.parseFor(true, start)
	case p.atKw("with") && decorators == nil:
		return p.parseWith(true, start)
	}
	p.failSyntax()
	return nil
}

func (p *Parser) parseDecorated() *Node {
	var decos []*Node
	for p.atOp("@") {
		p.advance()
		decos = append(decos, p.parseNamedExpr())
		if p.cur().Kind != TNewline {
			p.failSyntax()
		}
		p.advance()
	}
	switch {
	case p.atKw("def"):
		return p.parseFuncDef(decos, false, p.cur().Start)
	case p.atKw("class"):
		return p.parseClassDef(decos)
	case p.atKw("async"):
		return p.parseAsync(decos)
	}
	p.failSyntax()
	return nil
}

func (p *Parser) parseFuncDef(decos []*Node, async bool, start int) *Node {
	p.expectKw("def")
	name := p.parseName()
	var tparams []*Node
	if p.atOp("[") {
		tparams = p.parseTypeParams()
	}
	p.expectOp("(")
	args := p.parseParams(false)
	p.expectOp(")")
	var returns any
	if p.acceptOp("->") {
		returns = p.parseExpression()
	}
	body := p.parseBlock()
	nm := "FunctionDef"
	if async {
		nm = "AsyncFunctionDef"
	}
	return p.done(New(nm, name, args, body, decos, returns, nil, tparams), start)
}

func (p *Parser) parseClassDef(decos []*Node) *Node {
	start := p.cur().Start
	p.expectKw("class")
	name := p.parseName()
	var tparams []*Node
	if p.atOp("[") {
		tparams = p.parseTypeParams()
	}
	var bases, kws []*Node
	if p.atOp("(") {
		lp := p.advance()
		bases, kws = p.parseCallArgs(lp.Start)
		p.expectOp(")")
	}
	body := p.parseBlock()
	return p.done(New("ClassDef", name, bases, kws, body, decos, tparams), start)
}

func (p *Parser) parseTypeParams() []*Node {
	p.expectOp("[")
	var out []*Node
	for !p.atOp("]") {
		start := p.cur().Start
		switch {
		case p.atOp("*"):
			p.advance()
			name := p.parseName()
			var def any
			if p.acceptOp("=") {
				if p.atOp("*") {
					ss := p.cur().Start
					p.advance()
					v := p.parseExpression()
					def = p.done(New("Starred", v, load()), ss)
				} else {
					def = p.parseExpression()
				}
			}
			out = append(out, p.done(New("TypeVarTuple", name, def), start))
		case p.atOp("**"):
			p.advance()
			name := p.parseName()
			var def any
			if p.acceptOp("=") {
				def = p.parseExpression()
			}
			out = append(out, p.done(New("ParamSpec", name, def), start))
		default:
			name := p.parseName()
			var bound, def any
			if p.acceptOp(":") {
				bound = p.parseExpression()
			}
			if p.acceptOp("=") {
				def = p.parseExpression()
			}
			out = append(out, p.done(New("TypeVar", name, bound, def), start))
		}
		if !p.acceptOp(",") {
			break
		}
	}
	p.expectOp("]")
	return out
}

// parseParams parses a parameter list up to (not including) ')' or ':'.
func (p *Parser) parseParams(lambda bool) *Node {
	var posonly, args, kwonly, kwdefaults, defaults []*Node
	var vararg, kwarg *Node
	seenStar, seenDefault := false, false
	end := func() bool {
		if lambda {
			return p.atOp(":")
		}
		return p.atOp(")")
	}
	parseArg := func(allowAnn bool) *Node {
		start := p.cur().Start
		name := p.parseName()
		var ann any
		if allowAnn && !lambda && p.acceptOp(":") {
			ann = p.parseExpression()
		}
		return p.done(New("arg", name, ann, nil), start)
	}
	for !end() {
		switch {
		case p.atOp("/"):
			if seenStar || len(posonly) > 0 || len(args) == 0 {
				p.failSyntax()
			}
			p.advance()
			posonly, args = args, nil
		case p.atOp("**"):
			p.advance()
			kwarg = parseArg(true)
			p.acceptOp(",")
			if !end() {
				p.failSyntax()
			}
			continue
		case p.atOp("*"):
			if seenStar {
				p.failSyntax()
			}
			p.advance()
			seenStar = true
			if p.atOp(",") {
				// bare '*'
				if t := p.peek(1); p.isOp(t, ")") || p.isOp(t, ":") {
					p.failAt("SyntaxError", "named arguments must follow bare *", p.cur().Start)
				}
			} else {
				start := p.cur().Start
				name := p.parseName()
				var ann any
				if !lambda && p.acceptOp(":") {
					if p.atOp("*") {
						ss := p.cur().Start
						p.advance()
						v := p.parseBitOr()
						ann = p.done(New("Starred", v, load()), ss)
					} else {
						ann = p.parseExpression()
					}
				}
				vararg = p.done(New("arg", name, ann, nil), start)
			}
		default:
			a := parseArg(true)
			var def *Node
			if p.acceptOp("=") {
				def = p.parseExpression()
			}
			if seenStar {
				kwonly = append(kwonly, a)
				if def != nil {
					kwdefaults = append(kwdefaults, def)
				} else {
					kwdefaults = append(kwdefaults, nil)
				}
			} else {
				if def != nil {
					seenDefault = true
					defaults = append(defaults, def)
				} else if seenDefault {
					p.failAt("SyntaxError", "parameter without a default follows parameter with a default", a.Pos[PCol])
				}
				args = append(args, a)
			}
		}
		if !p.acceptOp(",") {
			break
		}
	}
	var va, ka any
	if vararg != nil {
		va = vararg
	}
	if kwarg != nil {
		ka = kwarg
	}
	return New("arguments", posonly, args, va, kwonly, kwdefaults, ka, defaults)
}

// ---- simple statements ----------------------------------------------------

var augOps = map[string]string{
	"+=": "Add", "-=": "Sub", "*=": "Mult", "@=": "MatMult", "/=": "Div", "%=": "Mod",
	"&=": "BitAnd", "|=": "BitOr", "^=": "BitXor", "<<=": "LShift", ">>=": "RShift", "**=": "Pow", "//=": "FloorDiv",
}

func (p *Parser) parseSimpleStmt() *Node {
	t := p.cur()
	start := t.Start
	if t.Kind == TName {
		switch p.txt(t) {
		case "pass":
			p.advance()
			return p.done(New("Pass"), start)
		case "break":
			p.advance()
			return p.done(New("Break"), start)
		case "continue":
			p.advance()
			return p.done(New("Continue"), start)
		case "return":
			p.advance()
			var v any
			if p.startsExpr(p.cur()) {
				v = p.parseStarExpressions()
			}
			return p.done(New("Return", v), start)
		case "raise":
			p.advance()
			var exc, cause any
			if p.startsExpr(p.cur()) {
				exc = p.parseExpression()
				if p.acceptKw("from") {
					cause = p.parseExpression()
				}
			}
			return p.done(New("Raise", exc, cause), start)
		case "global", "nonlocal":
			kw := p.txt(t)
			p.advance()
			names := []string{p.parseName()}
			for p.acceptOp(",") {
				names = append(names, p.parseName())
			}
			if kw == "global" {
				return p.done(New("Global", names), start)
			}
			return p.done(New("Nonlocal", names), start)
		case "del":
			p.advance()
			var targets []*Node
			for {
				e := p.parseBitOr()
				if e.Spec.Name == "Starred" {
					p.failAt("SyntaxError", "cannot delete starred", e.Pos[PCol])
				}
				p.setContext(e, New("Del"))
				targets = append(targets, e)
				if !p.acceptOp(",") || !p.startsExpr(p.cur()) {
					break
				}
			}
			return p.done(New("Delete", targets), start)
		case "assert":
			p.advance()
			test := p.parseExpression()
			var msg any
			if p.acceptOp(",") {
				msg = p.parseExpression()
			}
			return p.done(New("Assert", test, msg), start)
		case "import":
			return p.parseImport(start, false)
		case "from":
			return p.parseFromImport(start, false)
		case "lazy":
			if n := p.peek(1); p.isKw(n, "import") || p.isKw(n, "from") {
				p.advance()
				if p.atKw("import") {
					return p.parseImport(start, true)
				}
				return p.parseFromImport(start, true)
			}
		case "type":
			if n := p.peek(1); n.Kind == TName && !p.isReserved(n) {
				var res *Node
				if p.try(func() { res = p.parseTypeAlias(start) }) {
					return res
				}
			}
		}
	}
	return p.parseExprStmt(start)
}

func (p *Parser) parseTypeAlias(start int) *Node {
	p.advance() // type
	ns := p.cur().Start
	name := p.parseName()
	nameNode := p.done(New("Name", name, store()), ns)
	var tparams []*Node
	if p.atOp("[") {
		tparams = p.parseTypeParams()
	}
	p.expectOp("=")
	value := p.parseExpression()
	return p.done(New("TypeAlias", nameNode, tparams, value), start)
}

func lazyVal(lazy bool) any {
	if lazy {
		return 1
	}
	return 0
}

func (p *Parser) parseDottedName() string {
	var b strings.Builder
	b.WriteString(p.parseName())
	for p.atOp(".") {
		p.advance()
		b.WriteByte('.')
		b.WriteString(p.parseName())
	}
	return b.String()
}

func (p *Parser) parseImport(start int, lazy bool) *Node {
	p.expectKw("import")
	var names []*Node
	for {
		as := p.cur().Start
		name := p.parseDottedName()
		var asname any
		if p.acceptKw("as") {
			asname = p.parseName()
		}
		names = append(names, p.done(New("alias", name, asname), as))
		if !p.acceptOp(",") {
			break
		}
	}
	return p.done(New("Import", names, lazyVal(lazy)), start)
}

func (p *Parser) parseFromImport(start int, lazy bool) *Node {
	p.expectKw("from")
	level := 0
	for {
		switch {
		case p.atOp("."):
			level++
			p.advance()
		case p.atOp("..."):
			level += 3
			p.advance()
		default:
			goto doneDots
		}
	}
doneDots:
	var module any
	if !p.atKw("import") {
		module = p.parseDottedName()
	} else if level == 0 {
		p.failSyntax()
	}
	p.expectKw("import")
	var names []*Node
	if p.atOp("*") {
		as := p.cur().Start
		p.advance()
		names = []*Node{p.done(New("alias", "*", nil), as)}
	} else {
		paren := p.acceptOp("(")
		for {
			as := p.cur().Start
			name := p.parseName()
			var asname any
			if p.acceptKw("as") {
				asname = p.parseName()
			}
			names = append(names, p.done(New("alias", name, asname), as))
			if !p.acceptOp(",") {
				break
			}
			if paren && p.atOp(")") {
				break
			}
			if !paren && p.cur().Kind != TName {
				p.failAt("SyntaxError", "trailing comma not allowed without surrounding parentheses", p.cur().Start)
			}
		}
		if paren {
			p.expectOp(")")
		}
	}
	return p.done(New("ImportFrom", module, names, level, lazyVal(lazy)), start)
}

func (p *Parser) parseExprStmt(start int) *Node {
	firstTok := p.cur()
	var e *Node
	if p.atKw("yield") {
		e = p.parseYield()
	} else {
		e = p.parseStarExpressions()
	}
	switch {
	case p.atOp(":") && e.Spec.Name != "Yield":
		// annotated assignment
		switch e.Spec.Name {
		case "Name", "Attribute", "Subscript":
		case "Tuple", "List":
			p.failAt("SyntaxError", "only single target (not "+lowerName(e)+") can be annotated", e.Pos[PCol])
		default:
			p.failAt("SyntaxError", "illegal target for annotation", e.Pos[PCol])
		}
		p.advance()
		ann := p.parseExpression()
		p.setContext(e, store())
		var value any
		if p.acceptOp("=") {
			if p.atKw("yield") {
				value = p.parseYield()
			} else {
				value = p.parseStarExpressions()
			}
		}
		simple := 0
		if e.Spec.Name == "Name" && firstTok.Kind == TName {
			simple = 1
		}
		return p.done(New("AnnAssign", e, ann, value, simple), start)
	case p.atOp("="):
		targets := []*Node{e}
		var value *Node
		for p.acceptOp("=") {
			var v *Node
			if p.atKw("yield") {
				v = p.parseYield()
			} else {
				v = p.parseStarExpressions()
			}
			if p.atOp("=") {
				targets = append(targets, v)
			} else {
				value = v
			}
		}
		for _, t := range targets {
			p.setContext(t, store())
		}
		return p.done(New("Assign", targets, value, nil), start)
	case p.cur().Kind == TOp:
		if op, ok := augOps[p.txt(p.cur())]; ok {
			switch e.Spec.Name {
			case "Name", "Attribute", "Subscript":
			default:
				p.failAt("SyntaxError", "'"+describe(e)+"' is an illegal expression for augmented assignment", e.Pos[PCol])
			}
			p.advance()
			p.setContext(e, store())
			var v *Node
			if p.atKw("yield") {
				v = p.parseYield()
			} else {
				v = p.parseStarExpressions()
			}
			return p.done(New("AugAssign", e, New(op), v), start)
		}
	}
	return p.done(New("Expr", e), start)
}

func lowerName(n *Node) string {
	if n.Spec.Name == "Tuple" {
		return "tuple"
	}
	return "list"
}

// setContext assigns a Store/Del context to an assignment target, validating
// its shape.
func (p *Parser) setContext(n *Node, ctx *Node) {
	switch n.Spec.Name {
	case "Name":
		n.F[1] = New(ctx.Spec.Name)
	case "Attribute":
		n.F[2] = New(ctx.Spec.Name)
	case "Subscript":
		n.F[2] = New(ctx.Spec.Name)
	case "Starred":
		if ctx.Spec.Name == "Del" {
			p.failAt("SyntaxError", "cannot delete starred", n.Pos[PCol])
		}
		n.F[1] = New(ctx.Spec.Name)
		p.setContext(n.F[0].(*Node), ctx)
	case "Tuple", "List":
		n.F[1] = New(ctx.Spec.Name)
		for _, e := range n.F[0].([]*Node) {
			p.setContext(e, ctx)
		}
	default:
		verb := "assign to"
		if ctx.Spec.Name == "Del" {
			verb = "delete"
		}
		p.failAtNode("cannot "+verb+" "+describe(n), n)
	}
}

func (p *Parser) failAtNode(msg string, n *Node) {
	line, col := n.Pos[PLineno], n.Pos[PCol]
	text := p.S.LineText(line)
	if col > len(text) {
		col = len(text)
	}
	el, ec := n.Pos[PEndLineno], n.Pos[PEndCol]
	et := p.S.LineText(el)
	if ec > len(et) {
		ec = len(et)
	}
	panic(&SyntaxError{Kind: "SyntaxError", Msg: msg, Lineno: line, Offset: runeCount(text[:col]) + 1,
		EndLineno: el, EndOffset: runeCount(et[:ec]) + 1, Text: text})
}

func runeCount(s string) int { return len([]rune(s)) }

func describe(n *Node) string {
	switch n.Spec.Name {
	case "Call":
		return "function call"
	case "BinOp", "UnaryOp", "BoolOp":
		return "expression"
	case "Constant":
		switch v := n.F[0].(type) {
		case NoneT:
			return "None"
		case bool:
			if v {
				return "True"
			}
			return "False"
		case EllipsisT:
			return "ellipsis"
		}
		return "literal"
	case "Compare":
		return "comparison"
	case "Lambda":
		return "lambda"
	case "IfExp":
		return "conditional expression"
	case "Dict":
		return "dict literal"
	case "Set":
		return "set display"
	case "JoinedStr", "FormattedValue":
		return "f-string expression"
	case "TemplateStr", "Interpolation":
		return "t-string expression"
	case "ListComp":
		return "list comprehension"
	case "SetComp":
		return "set comprehension"
	case "DictComp":
		return "dict comprehension"
	case "GeneratorExp":
		return "generator expression"
	case "Await":
		return "await expression"
	case "Yield", "YieldFrom":
		return "yield expression"
	case "NamedExpr":
		return "named expression"
	case "Tuple":
		return "tuple"
	case "List":
		return "list"
	case "Attribute":
		return "attribute"
	case "Subscript":
		return "subscript"
	case "Starred":
		return "starred"
	case "Name":
		return "name"
	}
	return n.Spec.Name
}
