package pyast

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode"
)

// Operator precedences (mirrors CPython's _ast_unparse._Precedence).
const (
	pNamedExpr = iota + 1
	pTuple
	pYield
	pTest
	pOr
	pAnd
	pNot
	pCmp
	pBOr
	pBXor
	pBAnd
	pShift
	pArith
	pTerm
	pFactor
	pPower
	pAwait
	pAtom
)

// UnparseOptions control source regeneration.
type UnparseOptions struct {
	// PadLines inserts blank lines so each statement starts at (or after) its
	// recorded lineno, keeping tracebacks aligned with the original positions.
	PadLines bool
}

type unparser struct {
	sb       strings.Builder
	indent   int
	line     int // 1-based line of the write cursor
	opts     UnparseOptions
	atLine0  bool
	srcEmpty bool
}

// Unparse renders a Module/Expression/Interactive/FunctionType (or any
// stmt/expr) node as Python source.
func Unparse(n *Node, opts UnparseOptions) string {
	u := &unparser{opts: opts, line: 1, srcEmpty: true}
	switch n.Spec.Name {
	case "Module", "Interactive":
		u.stmts(n.List("body"))
	case "Expression":
		u.expr(n.Node("body"), pTest)
	case "FunctionType":
		u.write("(")
		for i, a := range n.List("argtypes") {
			if i > 0 {
				u.write(", ")
			}
			u.expr(a, pTest)
		}
		u.write(") -> ")
		u.expr(n.Node("returns"), pTest)
	default:
		if n.Spec.IsA("stmt") {
			u.stmt(n)
		} else {
			u.expr(n, pTest)
		}
	}
	return u.sb.String()
}

func (u *unparser) write(s string) {
	if s == "" {
		return
	}
	u.sb.WriteString(s)
	u.srcEmpty = false
	u.line += strings.Count(s, "\n")
}

func (u *unparser) fill(text string) {
	if !u.srcEmpty {
		u.write("\n")
	}
	u.write(strings.Repeat("    ", u.indent) + text)
}

// pad inserts newlines so that the next fill lands on line target.
func (u *unparser) pad(n *Node) {
	if !u.opts.PadLines || n == nil || !n.Has(PLineno) {
		return
	}
	target := n.Pos[PLineno]
	// fill() will emit one newline before the statement unless output is empty.
	cur := u.line
	if u.srcEmpty {
		for ; cur < target; cur++ {
			u.sb.WriteByte('\n')
			u.line++
		}
		return
	}
	for ; cur+1 < target; cur++ {
		u.sb.WriteByte('\n')
		u.line++
	}
}

func (u *unparser) stmts(body []*Node) {
	for _, s := range body {
		u.stmt(s)
	}
}

func (u *unparser) block(body []*Node) {
	u.write(":")
	u.indent++
	if len(body) == 0 {
		u.fill("pass")
	} else {
		u.stmts(body)
	}
	u.indent--
}

func (u *unparser) optExpr(n *Node, prec int) {
	if n != nil {
		u.expr(n, prec)
	}
}

func (u *unparser) stmt(n *Node) {
	switch n.Spec.Name {
	case "FunctionDef", "AsyncFunctionDef", "ClassDef":
		decos := n.List("decorator_list")
		if len(decos) > 0 && n.Has(PLineno) {
			first := &Node{HasPos: 1}
			first.Pos[PLineno] = n.Pos[PLineno] - len(decos)
			if d := decos[0]; d != nil && d.Has(PLineno) && d.Pos[PLineno] < n.Pos[PLineno] {
				first.Pos[PLineno] = d.Pos[PLineno]
			}
			u.pad(first)
		} else {
			u.pad(n)
		}
	default:
		u.pad(n)
	}
	switch n.Spec.Name {
	case "Expr":
		u.fill("")
		u.expr(n.Node("value"), pYield)
	case "Assign":
		u.fill("")
		for _, t := range n.List("targets") {
			u.expr(t, pTuple)
			u.write(" = ")
		}
		u.expr(n.Node("value"), pTest)
	case "AugAssign":
		u.fill("")
		u.expr(n.Node("target"), pTest)
		u.write(" " + binOpText(n.Node("op").Spec.Name) + "= ")
		u.expr(n.Node("value"), pTest)
	case "AnnAssign":
		u.fill("")
		t := n.Node("target")
		paren := n.Int("simple") == 0 && t.Spec.Name == "Name"
		if paren {
			u.write("(")
		}
		u.expr(t, pTest)
		if paren {
			u.write(")")
		}
		u.write(": ")
		u.expr(n.Node("annotation"), pTest)
		if v := n.Node("value"); v != nil {
			u.write(" = ")
			u.expr(v, pTest)
		}
	case "Return":
		u.fill("return")
		if v := n.Node("value"); v != nil {
			u.write(" ")
			u.expr(v, pTest)
		}
	case "Pass":
		u.fill("pass")
	case "Break":
		u.fill("break")
	case "Continue":
		u.fill("continue")
	case "Delete":
		u.fill("del ")
		for i, t := range n.List("targets") {
			if i > 0 {
				u.write(", ")
			}
			u.expr(t, pTest)
		}
	case "Assert":
		u.fill("assert ")
		u.expr(n.Node("test"), pTest)
		if m := n.Node("msg"); m != nil {
			u.write(", ")
			u.expr(m, pTest)
		}
	case "Global", "Nonlocal":
		kw := "global "
		if n.Spec.Name == "Nonlocal" {
			kw = "nonlocal "
		}
		names, _ := n.Get("names").([]string)
		u.fill(kw + strings.Join(names, ", "))
	case "Raise":
		u.fill("raise")
		if e := n.Node("exc"); e != nil {
			u.write(" ")
			u.expr(e, pTest)
			if c := n.Node("cause"); c != nil {
				u.write(" from ")
				u.expr(c, pTest)
			}
		}
	case "Import":
		kw := "import "
		if n.Int("is_lazy") != 0 {
			kw = "lazy import "
		}
		u.fill(kw)
		u.aliases(n.List("names"))
	case "ImportFrom":
		kw := "from "
		if n.Int("is_lazy") != 0 {
			kw = "lazy from "
		}
		mod, _ := n.Get("module").(string)
		u.fill(kw + strings.Repeat(".", n.Int("level")) + mod + " import ")
		u.aliases(n.List("names"))
	case "If":
		u.fill("if ")
		u.expr(n.Node("test"), pTest)
		u.block(n.List("body"))
		orelse := n.List("orelse")
		for len(orelse) == 1 && orelse[0].Spec.Name == "If" {
			e := orelse[0]
			u.fill("elif ")
			u.expr(e.Node("test"), pTest)
			u.block(e.List("body"))
			orelse = e.List("orelse")
		}
		if len(orelse) > 0 {
			u.fill("else")
			u.block(orelse)
		}
	case "While":
		u.fill("while ")
		u.expr(n.Node("test"), pTest)
		u.block(n.List("body"))
		if o := n.List("orelse"); len(o) > 0 {
			u.fill("else")
			u.block(o)
		}
	case "For", "AsyncFor":
		if n.Spec.Name == "AsyncFor" {
			u.fill("async for ")
		} else {
			u.fill("for ")
		}
		u.expr(n.Node("target"), pTuple)
		u.write(" in ")
		u.expr(n.Node("iter"), pTest)
		u.block(n.List("body"))
		if o := n.List("orelse"); len(o) > 0 {
			u.fill("else")
			u.block(o)
		}
	case "Try", "TryStar":
		u.fill("try")
		u.block(n.List("body"))
		for _, h := range n.List("handlers") {
			u.pad(h)
			if n.Spec.Name == "TryStar" {
				u.fill("except*")
			} else {
				u.fill("except")
			}
			if t := h.Node("type"); t != nil {
				u.write(" ")
				u.expr(t, pTest)
				if nm, ok := h.Get("name").(string); ok && nm != "" {
					u.write(" as " + nm)
				}
			}
			u.block(h.List("body"))
		}
		if o := n.List("orelse"); len(o) > 0 {
			u.fill("else")
			u.block(o)
		}
		if f := n.List("finalbody"); len(f) > 0 {
			u.fill("finally")
			u.block(f)
		}
	case "With", "AsyncWith":
		if n.Spec.Name == "AsyncWith" {
			u.fill("async with ")
		} else {
			u.fill("with ")
		}
		for i, it := range n.List("items") {
			if i > 0 {
				u.write(", ")
			}
			u.expr(it.Node("context_expr"), pTest)
			if v := it.Node("optional_vars"); v != nil {
				u.write(" as ")
				u.expr(v, pTest)
			}
		}
		u.block(n.List("body"))
	case "FunctionDef", "AsyncFunctionDef":
		for _, d := range n.List("decorator_list") {
			u.fill("@")
			u.expr(d, pTest)
		}
		if n.Spec.Name == "AsyncFunctionDef" {
			u.fill("async def " + n.Str("name"))
		} else {
			u.fill("def " + n.Str("name"))
		}
		u.typeParams(n.List("type_params"))
		u.write("(")
		u.arguments(n.Node("args"))
		u.write(")")
		if r := n.Node("returns"); r != nil {
			u.write(" -> ")
			u.expr(r, pTest)
		}
		u.block(n.List("body"))
	case "ClassDef":
		for _, d := range n.List("decorator_list") {
			u.fill("@")
			u.expr(d, pTest)
		}
		u.fill("class " + n.Str("name"))
		u.typeParams(n.List("type_params"))
		bases, kws := n.List("bases"), n.List("keywords")
		if len(bases) > 0 || len(kws) > 0 {
			u.write("(")
			first := true
			for _, b := range bases {
				if !first {
					u.write(", ")
				}
				first = false
				u.expr(b, pTest)
			}
			for _, k := range kws {
				if !first {
					u.write(", ")
				}
				first = false
				u.keyword(k)
			}
			u.write(")")
		}
		u.block(n.List("body"))
	case "TypeAlias":
		u.fill("type ")
		u.expr(n.Node("name"), pTest)
		u.typeParams(n.List("type_params"))
		u.write(" = ")
		u.expr(n.Node("value"), pTest)
	case "Match":
		u.fill("match ")
		u.expr(n.Node("subject"), pTest)
		u.write(":")
		u.indent++
		for _, c := range n.List("cases") {
			u.fill("case ")
			u.pattern(c.Node("pattern"))
			if g := c.Node("guard"); g != nil {
				u.write(" if ")
				u.expr(g, pTest)
			}
			u.block(c.List("body"))
		}
		u.indent--
	default:
		u.fill("pass")
	}
}

func (u *unparser) aliases(as []*Node) {
	for i, a := range as {
		if i > 0 {
			u.write(", ")
		}
		u.write(a.Str("name"))
		if as, ok := a.Get("asname").(string); ok && as != "" {
			u.write(" as " + as)
		}
	}
}

func (u *unparser) typeParams(tps []*Node) {
	if len(tps) == 0 {
		return
	}
	u.write("[")
	for i, tp := range tps {
		if i > 0 {
			u.write(", ")
		}
		switch tp.Spec.Name {
		case "TypeVar":
			u.write(tp.Str("name"))
			if b := tp.Node("bound"); b != nil {
				u.write(": ")
				u.expr(b, pTest)
			}
		case "TypeVarTuple":
			u.write("*" + tp.Str("name"))
		case "ParamSpec":
			u.write("**" + tp.Str("name"))
		}
		if d := tp.Node("default_value"); d != nil {
			u.write(" = ")
			u.expr(d, pTest)
		}
	}
	u.write("]")
}

func (u *unparser) arg(a *Node) {
	u.write(a.Str("arg"))
	if ann := a.Node("annotation"); ann != nil {
		u.write(": ")
		u.expr(ann, pTest)
	}
}

func (u *unparser) arguments(a *Node) {
	if a == nil {
		return
	}
	posonly, args := a.List("posonlyargs"), a.List("args")
	defaults := a.List("defaults")
	all := append(append([]*Node{}, posonly...), args...)
	first := true
	comma := func() {
		if !first {
			u.write(", ")
		}
		first = false
	}
	off := len(all) - len(defaults)
	for i, ar := range all {
		comma()
		u.arg(ar)
		if j := i - off; j >= 0 && j < len(defaults) && defaults[j] != nil {
			u.write("=")
			u.expr(defaults[j], pTest)
		}
		if i == len(posonly)-1 {
			comma()
			u.write("/")
		}
	}
	va, _ := a.Get("vararg").(*Node)
	kwonly := a.List("kwonlyargs")
	if va != nil || len(kwonly) > 0 {
		comma()
		u.write("*")
		if va != nil {
			u.write(va.Str("arg"))
			if ann := va.Node("annotation"); ann != nil {
				u.write(": ")
				u.expr(ann, pTest)
			}
		}
	}
	kwd := a.List("kw_defaults")
	for i, k := range kwonly {
		comma()
		u.arg(k)
		if i < len(kwd) && kwd[i] != nil {
			u.write("=")
			u.expr(kwd[i], pTest)
		}
	}
	if ka, _ := a.Get("kwarg").(*Node); ka != nil {
		comma()
		u.write("**")
		u.arg(ka)
	}
}

func (u *unparser) keyword(k *Node) {
	if arg, ok := k.Get("arg").(string); ok && arg != "" {
		u.write(arg + "=")
	} else {
		u.write("**")
	}
	u.expr(k.Node("value"), pTest)
}

// ---- patterns -------------------------------------------------------------

func (u *unparser) pattern(n *Node) {
	switch n.Spec.Name {
	case "MatchValue":
		u.expr(n.Node("value"), pTest)
	case "MatchSingleton":
		switch v := n.F[0].(type) {
		case NoneT:
			u.write("None")
		case bool:
			if v {
				u.write("True")
			} else {
				u.write("False")
			}
		}
	case "MatchSequence":
		u.write("[")
		for i, p := range n.List("patterns") {
			if i > 0 {
				u.write(", ")
			}
			u.pattern(p)
		}
		u.write("]")
	case "MatchMapping":
		u.write("{")
		keys, pats := n.List("keys"), n.List("patterns")
		first := true
		for i, k := range keys {
			if !first {
				u.write(", ")
			}
			first = false
			u.expr(k, pTest)
			u.write(": ")
			u.pattern(pats[i])
		}
		if r, ok := n.Get("rest").(string); ok && r != "" {
			if !first {
				u.write(", ")
			}
			u.write("**" + r)
		}
		u.write("}")
	case "MatchClass":
		u.expr(n.Node("cls"), pAtom)
		u.write("(")
		first := true
		for _, p := range n.List("patterns") {
			if !first {
				u.write(", ")
			}
			first = false
			u.pattern(p)
		}
		attrs, _ := n.Get("kwd_attrs").([]string)
		kp := n.List("kwd_patterns")
		for i, a := range attrs {
			if !first {
				u.write(", ")
			}
			first = false
			u.write(a + "=")
			u.pattern(kp[i])
		}
		u.write(")")
	case "MatchStar":
		name, _ := n.Get("name").(string)
		if name == "" {
			name = "_"
		}
		u.write("*" + name)
	case "MatchAs":
		pat := n.Node("pattern")
		name, _ := n.Get("name").(string)
		switch {
		case pat == nil && name == "":
			u.write("_")
		case pat == nil:
			u.write(name)
		default:
			// parenthesize or-patterns' alternatives that are themselves 'as'
			u.write("(")
			u.pattern(pat)
			u.write(" as " + name + ")")
		}
	case "MatchOr":
		for i, p := range n.List("patterns") {
			if i > 0 {
				u.write(" | ")
			}
			if p.Spec.Name == "MatchOr" {
				u.write("(")
				u.pattern(p)
				u.write(")")
			} else {
				u.pattern(p)
			}
		}
	}
}

// ---- expressions ----------------------------------------------------------

var binOps = map[string]string{
	"Add": "+", "Sub": "-", "Mult": "*", "MatMult": "@", "Div": "/", "Mod": "%",
	"LShift": "<<", "RShift": ">>", "BitOr": "|", "BitXor": "^", "BitAnd": "&",
	"FloorDiv": "//", "Pow": "**",
}

var binPrec = map[string]int{
	"Add": pArith, "Sub": pArith, "Mult": pTerm, "MatMult": pTerm, "Div": pTerm, "Mod": pTerm,
	"LShift": pShift, "RShift": pShift, "BitOr": pBOr, "BitXor": pBXor, "BitAnd": pBAnd,
	"FloorDiv": pTerm, "Pow": pPower,
}

func binOpText(name string) string { return binOps[name] }

var cmpOps = map[string]string{
	"Eq": "==", "NotEq": "!=", "Lt": "<", "LtE": "<=", "Gt": ">", "GtE": ">=",
	"Is": "is", "IsNot": "is not", "In": "in", "NotIn": "not in",
}

// expr writes n, parenthesizing when its precedence is below need.
func (u *unparser) expr(n *Node, need int) {
	if n == nil {
		return
	}
	paren := func(own int) bool {
		if need > own {
			u.write("(")
			return true
		}
		return false
	}
	closeP := func(p bool) {
		if p {
			u.write(")")
		}
	}
	switch n.Spec.Name {
	case "Name":
		u.write(n.Str("id"))
	case "Constant":
		u.constant(n, need)
	case "Attribute":
		v := n.Node("value")
		u.expr(v, pAtom)
		if v.Spec.Name == "Constant" {
			if _, isInt := intConst(v); isInt {
				u.write(" ")
			}
		}
		u.write("." + n.Str("attr"))
	case "Call":
		u.expr(n.Node("func"), pAtom)
		u.write("(")
		first := true
		for _, a := range n.List("args") {
			if !first {
				u.write(", ")
			}
			first = false
			u.expr(a, pTest)
		}
		for _, k := range n.List("keywords") {
			if !first {
				u.write(", ")
			}
			first = false
			u.keyword(k)
		}
		u.write(")")
	case "Subscript":
		u.expr(n.Node("value"), pAtom)
		u.write("[")
		sl := n.Node("slice")
		if sl.Spec.Name == "Tuple" && len(sl.List("elts")) > 0 {
			u.itemsView(sl.List("elts"))
		} else {
			u.expr(sl, pTest)
		}
		u.write("]")
	case "Slice":
		u.optExpr(n.Node("lower"), pTest)
		u.write(":")
		u.optExpr(n.Node("upper"), pTest)
		if s := n.Node("step"); s != nil {
			u.write(":")
			u.expr(s, pTest)
		}
	case "Starred":
		u.write("*")
		u.expr(n.Node("value"), pBOr)
	case "BinOp":
		op := n.Node("op").Spec.Name
		prec := binPrec[op]
		p := paren(prec)
		lp, rp := prec, prec+1
		if op == "Pow" {
			lp, rp = prec+1, prec
		}
		u.expr(n.Node("left"), lp)
		u.write(" " + binOps[op] + " ")
		u.expr(n.Node("right"), rp)
		closeP(p)
	case "UnaryOp":
		op := n.Node("op").Spec.Name
		switch op {
		case "Not":
			p := paren(pNot)
			u.write("not ")
			u.expr(n.Node("operand"), pNot)
			closeP(p)
		default:
			p := paren(pFactor)
			u.write(map[string]string{"UAdd": "+", "USub": "-", "Invert": "~"}[op])
			u.expr(n.Node("operand"), pFactor)
			closeP(p)
		}
	case "BoolOp":
		name, prec := " and ", pAnd
		if n.Node("op").Spec.Name == "Or" {
			name, prec = " or ", pOr
		}
		p := paren(prec)
		cur := prec
		for i, v := range n.List("values") {
			if i > 0 {
				u.write(name)
			}
			if cur < pAtom {
				cur++
			}
			u.expr(v, cur)
		}
		closeP(p)
	case "Compare":
		p := paren(pCmp)
		u.expr(n.Node("left"), pCmp+1)
		ops, comps := n.List("ops"), n.List("comparators")
		for i, o := range ops {
			u.write(" " + cmpOps[o.Spec.Name] + " ")
			u.expr(comps[i], pCmp+1)
		}
		closeP(p)
	case "IfExp":
		p := paren(pTest)
		u.expr(n.Node("body"), pTest+1)
		u.write(" if ")
		u.expr(n.Node("test"), pTest+1)
		u.write(" else ")
		u.expr(n.Node("orelse"), pTest)
		closeP(p)
	case "Lambda":
		p := paren(pTest)
		u.write("lambda")
		var sub unparser
		sub.opts = UnparseOptions{}
		sub.line = 1
		sub.srcEmpty = true
		sub.arguments(n.Node("args"))
		if s := sub.sb.String(); s != "" {
			u.write(" " + s)
		}
		u.write(": ")
		u.expr(n.Node("body"), pTest)
		closeP(p)
	case "NamedExpr":
		p := paren(pNamedExpr)
		u.expr(n.Node("target"), pAtom)
		u.write(" := ")
		u.expr(n.Node("value"), pAtom)
		closeP(p)
	case "Await":
		p := paren(pAwait)
		u.write("await ")
		u.expr(n.Node("value"), pAtom)
		closeP(p)
	case "Yield":
		p := paren(pYield)
		u.write("yield")
		if v := n.Node("value"); v != nil {
			u.write(" ")
			u.expr(v, pTest)
		}
		closeP(p)
	case "YieldFrom":
		p := paren(pYield)
		u.write("yield from ")
		u.expr(n.Node("value"), pAtom)
		closeP(p)
	case "Tuple":
		elts := n.List("elts")
		p := len(elts) == 0 || need > pTuple
		if p {
			u.write("(")
		}
		u.itemsView(elts)
		closeP(p)
	case "List":
		u.write("[")
		for i, e := range n.List("elts") {
			if i > 0 {
				u.write(", ")
			}
			u.expr(e, pTest)
		}
		u.write("]")
	case "Set":
		elts := n.List("elts")
		if len(elts) == 0 {
			u.write("{*()}")
			break
		}
		u.write("{")
		for i, e := range elts {
			if i > 0 {
				u.write(", ")
			}
			u.expr(e, pTest)
		}
		u.write("}")
	case "Dict":
		u.write("{")
		keys, vals := n.List("keys"), n.List("values")
		for i, v := range vals {
			if i > 0 {
				u.write(", ")
			}
			if i < len(keys) && keys[i] != nil {
				u.expr(keys[i], pTest)
				u.write(": ")
				u.expr(v, pTest)
			} else {
				u.write("**")
				u.expr(v, pBOr)
			}
		}
		u.write("}")
	case "ListComp":
		u.write("[")
		u.expr(n.Node("elt"), pTest)
		u.comprehensions(n.List("generators"))
		u.write("]")
	case "SetComp":
		u.write("{")
		u.expr(n.Node("elt"), pTest)
		u.comprehensions(n.List("generators"))
		u.write("}")
	case "GeneratorExp":
		u.write("(")
		u.expr(n.Node("elt"), pTest)
		u.comprehensions(n.List("generators"))
		u.write(")")
	case "DictComp":
		u.write("{")
		if v := n.Node("value"); v != nil {
			u.expr(n.Node("key"), pTest)
			u.write(": ")
			u.expr(v, pTest)
		} else {
			u.write("**")
			u.expr(n.Node("key"), pBOr)
		}
		u.comprehensions(n.List("generators"))
		u.write("}")
	case "JoinedStr":
		u.write(u.interpolated("f", n.List("values")))
	case "TemplateStr":
		u.write(u.interpolated("t", n.List("values")))
	case "FormattedValue", "Interpolation":
		// bare field outside a JoinedStr: wrap
		u.write(u.interpolated("f", []*Node{n}))
	default:
		u.write("None")
	}
}

func (u *unparser) itemsView(elts []*Node) {
	if len(elts) == 1 {
		u.expr(elts[0], pTest)
		u.write(",")
		return
	}
	for i, e := range elts {
		if i > 0 {
			u.write(", ")
		}
		u.expr(e, pTest)
	}
}

func (u *unparser) comprehensions(gens []*Node) {
	for _, g := range gens {
		if g.Int("is_async") != 0 {
			u.write(" async for ")
		} else {
			u.write(" for ")
		}
		u.expr(g.Node("target"), pTuple)
		u.write(" in ")
		u.expr(g.Node("iter"), pTest+1)
		for _, c := range g.List("ifs") {
			u.write(" if ")
			u.expr(c, pTest+1)
		}
	}
}

// ---- constants ------------------------------------------------------------

func intConst(n *Node) (any, bool) {
	switch v := n.F[0].(type) {
	case int64, *big.Int, int:
		return v, true
	}
	return nil, false
}

func (u *unparser) constant(n *Node, need int) {
	v := n.F[0]
	switch x := v.(type) {
	case NoneT:
		u.write("None")
	case bool:
		if x {
			u.write("True")
		} else {
			u.write("False")
		}
	case EllipsisT:
		u.write("...")
	case string:
		if k, _ := n.F[1].(string); k == "u" {
			u.write("u")
		}
		u.write(ReprString(x))
	case []byte:
		u.write(ReprBytes(x))
	case int64, int, *big.Int:
		s := fmt.Sprint(x)
		if strings.HasPrefix(s, "-") && need >= pFactor {
			s = "(" + s + ")"
		}
		u.write(s)
	case float64:
		s := ReprFloat(x)
		if (strings.HasPrefix(s, "-")) && need >= pFactor {
			s = "(" + s + ")"
		}
		u.write(s)
	case complex128:
		u.write(reprComplex(x))
	case TupleT:
		u.write("(")
		items := make([]*Node, len(x))
		for i, c := range x {
			items[i] = New("Constant", c, nil)
		}
		u.itemsView(items)
		u.write(")")
	case FrozenSetT:
		u.write("frozenset({")
		for i, c := range x {
			if i > 0 {
				u.write(", ")
			}
			u.expr(New("Constant", c, nil), pTest)
		}
		u.write("})")
	default:
		u.write("None")
	}
}

// ReprFloat renders f like Python's repr(float), with huge values as 1e309.
func ReprFloat(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "1e309"
	case math.IsInf(f, -1):
		return "-1e309"
	case math.IsNaN(f):
		return "(1e309-1e309)"
	}
	if f == 0 {
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64)
	idx := strings.IndexByte(e, 'e')
	exp, _ := strconv.Atoi(e[idx+1:])
	if exp >= -4 && exp < 16 {
		s := strconv.FormatFloat(f, 'f', -1, 64)
		if !strings.ContainsAny(s, ".") {
			s += ".0"
		}
		return s
	}
	return e
}

func reprComplex(c complex128) string {
	im := ReprFloat(imag(c))
	im = strings.TrimSuffix(im, ".0")
	if real(c) == 0 && !math.Signbit(real(c)) {
		return im + "j"
	}
	re := strings.TrimSuffix(ReprFloat(real(c)), ".0")
	sign := "+"
	if strings.HasPrefix(im, "-") {
		sign = ""
	}
	return "(" + re + sign + im + "j)"
}

func isPrintable(r rune) bool {
	if r == ' ' {
		return true
	}
	return unicode.IsPrint(r)
}

// escapeBody escapes s for inclusion between quote characters.
func escapeBody(s string, quote byte, bytesMode bool) string {
	var sb strings.Builder
	if bytesMode {
		for i := 0; i < len(s); i++ {
			c := s[i]
			switch {
			case c == '\\':
				sb.WriteString(`\\`)
			case c == quote:
				sb.WriteByte('\\')
				sb.WriteByte(c)
			case c == '\n':
				sb.WriteString(`\n`)
			case c == '\r':
				sb.WriteString(`\r`)
			case c == '\t':
				sb.WriteString(`\t`)
			case c < 0x20 || c >= 0x7f:
				fmt.Fprintf(&sb, `\x%02x`, c)
			default:
				sb.WriteByte(c)
			}
		}
		return sb.String()
	}
	for _, r := range s {
		switch {
		case r == '\\':
			sb.WriteString(`\\`)
		case r == rune(quote):
			sb.WriteByte('\\')
			sb.WriteRune(r)
		case r == '\n':
			sb.WriteString(`\n`)
		case r == '\r':
			sb.WriteString(`\r`)
		case r == '\t':
			sb.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&sb, `\x%02x`, r)
		case r < 0x7f:
			sb.WriteRune(r)
		case isPrintable(r):
			sb.WriteRune(r)
		case r <= 0xff:
			fmt.Fprintf(&sb, `\x%02x`, r)
		case r <= 0xffff:
			fmt.Fprintf(&sb, `\u%04x`, r)
		default:
			fmt.Fprintf(&sb, `\U%08x`, r)
		}
	}
	return sb.String()
}

// ReprString renders s like Python's repr(str).
func ReprString(s string) string {
	q := byte('\'')
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		q = '"'
	}
	return string(q) + escapeBody(s, q, false) + string(q)
}

// ReprBytes renders b like Python's repr(bytes).
func ReprBytes(b []byte) string {
	q := byte('\'')
	s := string(b)
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		q = '"'
	}
	return "b" + string(q) + escapeBody(s, q, true) + string(q)
}

// ---- f-strings ------------------------------------------------------------

func (u *unparser) sub() *unparser { return &unparser{line: 1, srcEmpty: true} }

func (u *unparser) exprText(n *Node, need int) string {
	s := u.sub()
	s.expr(n, need)
	return s.sb.String()
}

// fieldBody builds the contents (between quotes) of an f/t-string.
func (u *unparser) fieldBody(values []*Node, quote string, spec bool) (body string, exprs []string) {
	var sb strings.Builder
	for _, v := range values {
		switch v.Spec.Name {
		case "Constant":
			s, _ := v.F[0].(string)
			esc := escapeBody(s, quote[0], false)
			esc = strings.ReplaceAll(esc, "{", "{{")
			esc = strings.ReplaceAll(esc, "}", "}}")
			if spec {
				// inside a format spec, only escape what must be escaped
				esc = strings.ReplaceAll(strings.ReplaceAll(esc, "{{", "{"), "}}", "}")
			}
			sb.WriteString(esc)
		case "FormattedValue", "Interpolation":
			text := u.exprText(v.Node("value"), pTest+1)
			if strings.HasPrefix(text, "{") {
				text = " " + text
			}
			exprs = append(exprs, text)
			sb.WriteString("{" + text)
			if c := v.Int("conversion"); c > 0 {
				sb.WriteString("!" + string(rune(c)))
			}
			if fs := v.Node("format_spec"); fs != nil {
				b, ex := u.fieldBody(fs.List("values"), quote, true)
				exprs = append(exprs, ex...)
				sb.WriteString(":" + b)
			}
			sb.WriteString("}")
		case "JoinedStr":
			b, ex := u.fieldBody(v.List("values"), quote, spec)
			exprs = append(exprs, ex...)
			sb.WriteString(b)
		}
	}
	return sb.String(), exprs
}

func (u *unparser) interpolated(prefix string, values []*Node) string {
	for _, q := range []string{"'", "\"", "'''", "\"\"\""} {
		body, exprs := u.fieldBody(values, q, false)
		ok := true
		for _, e := range exprs {
			if strings.Contains(e, q[:1]) || strings.Contains(e, "\\") || strings.Contains(e, "\n") {
				ok = false
				break
			}
		}
		if ok || q == "\"\"\"" {
			return prefix + q + body + q
		}
	}
	return prefix + "''"
}
