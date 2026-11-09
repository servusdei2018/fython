package vm

import (
	"math/big"
	"strconv"
	"strings"
	"sync"

	"github.com/servusdei2018/fython/pkg/pyast"
	"github.com/servusdei2018/fython/pkg/val"
)

// PyCF_* compile flags (values match CPython 3.15).
const (
	pyCFOnlyAST       = 0x400
	pyCFTypeComments  = 0x1000
	pyCFTopLevelAwait = 0x2000
	pyCFOptimizedAST  = 0x8000 | pyCFOnlyAST
)

type astState struct {
	classes []*val.PyType // indexed by pyast.Spec.ID
	byName  map[string]*val.PyType
	astType *val.PyType
	bySpec  map[*val.PyType]*pyast.Spec
}

var (
	astOnce sync.Once
	astSt   *astState
)

func internS(s string) val.Value { return val.InternString(s) }

func strTuple(items []string) val.Value {
	vs := make([]val.Value, len(items))
	for i, s := range items {
		vs[i] = internS(s)
	}
	return val.NewTupleVal(vs...)
}

func builtinFn(name string, fn func(ctx *val.ExecContext, args []val.Value, kwnames []val.Value) val.Value) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return fn(ctx, args, kwnames)
	})
}

func astInit() *astState {
	astOnce.Do(func() {
		st := &astState{
			classes: make([]*val.PyType, len(pyast.Specs)),
			byName:  map[string]*val.PyType{},
			bySpec:  map[*val.PyType]*pyast.Spec{},
		}
		astSt = st

		d := val.NewDictVal(16).AsDict()
		d.Set(internS("__module__"), internS("ast"))
		d.Set(internS("_fields"), val.NewTupleVal())
		d.Set(internS("_attributes"), val.NewTupleVal())
		d.Set(internS("__match_args__"), val.NewTupleVal())
		d.Set(internS("__doc__"), val.NoneVal())
		d.Set(internS("__init__"), builtinFn("__init__", astInitMethod))
		d.Set(internS("__reduce__"), builtinFn("__reduce__", astReduce))
		d.Set(internS("__replace__"), builtinFn("__replace__", astReplace))
		d.Set(internS("__repr__"), builtinFn("__repr__", func(ctx *val.ExecContext, args []val.Value, kwnames []val.Value) val.Value {
			return val.NewStringVal(astRepr(ctx, args[0], 0))
		}))
		st.astType = val.MustNewType("AST", nil, d)
		st.byName["AST"] = st.astType

		for _, sp := range pyast.Specs {
			base := st.astType
			if sp.Base != "" {
				base = st.byName[sp.Base]
			}
			cd := val.NewDictVal(8).AsDict()
			cd.Set(internS("__module__"), internS("ast"))
			names := make([]string, len(sp.Fields))
			for i, f := range sp.Fields {
				names[i] = f.Name
				if f.Opt && !f.List {
					cd.Set(internS(f.Name), val.NoneVal())
				}
			}
			cd.Set(internS("_fields"), strTuple(names))
			cd.Set(internS("__match_args__"), strTuple(names))
			cd.Set(internS("_attributes"), strTuple(sp.Attrs))
			for _, a := range sp.Attrs {
				if strings.HasPrefix(a, "end_") {
					cd.Set(internS(a), val.NoneVal())
				}
			}
			cd.Set(internS("__doc__"), internS(astDoc(sp)))
			t := val.MustNewType(sp.Name, []*val.PyType{base}, cd)
			st.classes[sp.ID] = t
			st.byName[sp.Name] = t
			st.bySpec[t] = sp
		}
		// _field_types (computed after every class exists)
		for _, sp := range pyast.Specs {
			t := st.classes[sp.ID]
			ftv := val.NewDictVal(len(sp.Fields))
			ft := ftv.AsDict()
			for _, f := range sp.Fields {
				ft.Set(internS(f.Name), st.fieldType(f))
			}
			t.Dict.Set(internS("_field_types"), ftv)
		}
	})
	return astSt
}

func astDoc(sp *pyast.Spec) string {
	parts := make([]string, len(sp.Fields))
	for i, f := range sp.Fields {
		ty := f.Type
		if ty == "str" {
			ty = "identifier"
		}
		if f.List {
			ty += "*"
		} else if f.Opt {
			ty += "?"
		}
		parts[i] = ty + " " + f.Name
	}
	return sp.Name + "(" + strings.Join(parts, ", ") + ")"
}

func (st *astState) fieldType(f pyast.Field) val.Value {
	var base val.Value
	switch f.Type {
	case "str":
		base = val.TypeVal(val.StrType)
	case "int":
		base = val.TypeVal(val.IntType)
	case "object":
		base = val.TypeVal(val.ObjectType)
	default:
		base = val.TypeVal(st.byName[f.Type])
	}
	initTypingTypes()
	if f.List {
		base = newGenericAlias(val.TypeVal(val.ListType), []val.Value{base})
	}
	if f.Opt && !f.List {
		base = makeUnion([]val.Value{base, val.NoneVal()})
	}
	return base
}

// specOf returns the spec for the closest registered class in v's MRO.
func (st *astState) specOf(t *val.PyType) *pyast.Spec {
	for _, c := range t.MRO {
		if sp, ok := st.bySpec[c]; ok {
			return sp
		}
	}
	return nil
}

func isASTType(st *astState, t *val.PyType) bool { return val.IsSubclass(t, st.astType) }

// ---- AST methods ----------------------------------------------------------

func instOf(v val.Value) *val.PyInstance {
	if v.IsInstance() {
		return v.AsInstance()
	}
	return nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func joinQuoted(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = "'" + n + "'"
	}
	switch len(q) {
	case 1:
		return q[0]
	case 2:
		return q[0] + " and " + q[1]
	}
	return strings.Join(q[:len(q)-1], ", ") + ", and " + q[len(q)-1]
}

func astInitMethod(ctx *val.ExecContext, args []val.Value, kwnames []val.Value) val.Value {
	st := astInit()
	self := args[0]
	inst := instOf(self)
	if inst == nil {
		panic("TypeError: AST.__init__ requires an AST instance")
	}
	sp := st.specOf(self.Type())
	npos := len(args) - len(kwnames) - 1
	clsName := self.Type().Name
	if sp == nil {
		if npos > 0 {
			panic("TypeError: " + clsName + " constructor takes 0 positional arguments")
		}
		for i, kn := range kwnames {
			inst.SetAttr(kn, args[len(args)-len(kwnames)+i])
		}
		return val.NoneVal()
	}
	if npos > len(sp.Fields) {
		panic("TypeError: " + clsName + " constructor takes at most " + strconv.Itoa(len(sp.Fields)) + " positional argument" + plural(len(sp.Fields)))
	}
	set := make([]bool, len(sp.Fields))
	for i := range npos {
		inst.SetAttr(internS(sp.Fields[i].Name), args[1+i])
		set[i] = true
	}
	kwStart := len(args) - len(kwnames)
	for i, kn := range kwnames {
		name := kn.AsString()
		idx := sp.FieldIndex(name)
		if idx >= 0 {
			if set[idx] {
				panic("TypeError: ast." + clsName + " got multiple values for argument '" + name + "'")
			}
			set[idx] = true
		} else {
			known := false
			for _, a := range sp.Attrs {
				if a == name {
					known = true
				}
			}
			if !known {
				// CPython also tolerates unknown keywords on subclasses; reject on builtins.
				if self.Type().Name == sp.Name && st.classes[sp.ID] == self.Type() {
					panic("TypeError: ast." + clsName + ".__init__ got an unexpected keyword argument '" + name + "'")
				}
			}
		}
		inst.SetAttr(internS(name), args[kwStart+i])
	}
	var missing []string
	for i, f := range sp.Fields {
		if set[i] {
			continue
		}
		switch {
		case f.List:
			inst.SetAttr(internS(f.Name), val.NewListVal())
		case f.Opt:
			inst.SetAttr(internS(f.Name), val.NoneVal())
		case f.Type == "expr_context":
			inst.SetAttr(internS(f.Name), val.NewInstanceOfType(st.byName["Load"]))
		default:
			missing = append(missing, f.Name)
		}
	}
	if len(missing) > 0 {
		panic("TypeError: ast." + clsName + ".__init__ missing " + strconv.Itoa(len(missing)) + " required positional argument" + plural(len(missing)) + ": " + joinQuoted(missing))
	}
	return val.NoneVal()
}

func astReduce(ctx *val.ExecContext, args []val.Value, kwnames []val.Value) val.Value {
	st := astInit()
	self := args[0]
	sp := st.specOf(self.Type())
	n := 0
	if sp != nil {
		n = len(sp.Fields)
	}
	none := make([]val.Value, n)
	for i := range none {
		none[i] = val.NoneVal()
	}
	state := val.NewDictVal(8)
	if inst := instOf(self); inst != nil {
		for _, e := range val.NewInstanceDictProxy(inst).AsDict().ActiveEntries() {
			state.AsDict().Set(e.Key, e.Value)
		}
	}
	return val.NewTupleVal(val.TypeVal(self.Type()), val.NewTupleVal(none...), state)
}

func astReplace(ctx *val.ExecContext, args []val.Value, kwnames []val.Value) val.Value {
	st := astInit()
	self := args[0]
	if len(args)-len(kwnames) > 1 {
		panic("TypeError: __replace__() takes no positional arguments")
	}
	sp := st.specOf(self.Type())
	if sp == nil {
		panic("TypeError: __replace__ requires an AST node")
	}
	valid := map[string]bool{}
	for _, f := range sp.Fields {
		valid[f.Name] = true
	}
	for _, a := range sp.Attrs {
		valid[a] = true
	}
	kwStart := len(args) - len(kwnames)
	for _, kn := range kwnames {
		if !valid[kn.AsString()] {
			panic("TypeError: " + self.Type().Name + ".__replace__ got an unexpected keyword argument '" + kn.AsString() + "'")
		}
	}
	out := val.NewInstanceOfType(self.Type())
	oi := out.AsInstance()
	if inst := instOf(self); inst != nil {
		for _, e := range val.NewInstanceDictProxy(inst).AsDict().ActiveEntries() {
			oi.SetAttr(e.Key, e.Value)
		}
	}
	var missing []string
	for i, kn := range kwnames {
		oi.SetAttr(internS(kn.AsString()), args[kwStart+i])
	}
	for _, f := range sp.Fields {
		if _, ok := oi.GetAttr(internS(f.Name)); ok {
			continue
		}
		switch {
		case f.List:
			oi.SetAttr(internS(f.Name), val.NewListVal())
		case f.Opt:
			oi.SetAttr(internS(f.Name), val.NoneVal())
		default:
			missing = append(missing, f.Name)
		}
	}
	if len(missing) > 0 {
		panic("TypeError: " + self.Type().Name + ".__replace__ missing " + strconv.Itoa(len(missing)) + " required field" + plural(len(missing)) + ": " + joinQuoted(missing))
	}
	return out
}

const astReprMaxDepth = 3

func astRepr(ctx *val.ExecContext, v val.Value, depth int) string {
	st := astInit()
	sp := st.specOf(v.Type())
	name := v.Type().Name
	if sp == nil {
		return name + "()"
	}
	if depth >= astReprMaxDepth {
		return name + "(...)"
	}
	var parts []string
	inst := instOf(v)
	for _, f := range sp.Fields {
		var fv val.Value
		ok := false
		if inst != nil {
			fv, ok = inst.GetAttr(internS(f.Name))
		}
		if !ok {
			continue
		}
		parts = append(parts, f.Name+"="+astReprValue(ctx, fv, depth+1))
	}
	return name + "(" + strings.Join(parts, ", ") + ")"
}

func astReprValue(ctx *val.ExecContext, v val.Value, depth int) string {
	st := astInit()
	if v.IsInstance() && isASTType(st, v.Type()) {
		return astRepr(ctx, v, depth)
	}
	if v.IsList() {
		items := v.AsList().Items
		show := func(x val.Value) string { return astReprValue(ctx, x, depth) }
		switch {
		case len(items) <= 2:
			ss := make([]string, len(items))
			for i, x := range items {
				ss[i] = show(x)
			}
			return "[" + strings.Join(ss, ", ") + "]"
		default:
			return "[" + show(items[0]) + ", ..., " + show(items[len(items)-1]) + "]"
		}
	}
	return val.PyRepr(ctx, v)
}

// ---- Node -> Value --------------------------------------------------------

func (st *astState) newNode(n *pyast.Node) val.Value {
	if n == nil {
		return val.NoneVal()
	}
	cls := st.classes[n.Spec.ID]
	v := val.NewInstanceOfType(cls)
	inst := v.AsInstance()
	for i, f := range n.Spec.Fields {
		inst.SetAttr(internS(f.Name), st.fieldValue(n, i))
	}
	for i, a := range n.Spec.Attrs {
		if n.Has(i) {
			inst.SetAttr(internS(a), val.Int64Val(int64(n.Pos[i])))
		}
	}
	return v
}

func (st *astState) fieldValue(n *pyast.Node, i int) val.Value {
	f := n.Spec.Fields[i]
	switch x := n.F[i].(type) {
	case nil:
		return val.NoneVal()
	case *pyast.Node:
		return st.newNode(x)
	case []*pyast.Node:
		vs := make([]val.Value, len(x))
		for j, c := range x {
			vs[j] = st.newNode(c)
		}
		return val.NewListVal(vs...)
	case []string:
		vs := make([]val.Value, len(x))
		for j, c := range x {
			vs[j] = val.NewStringVal(c)
		}
		return val.NewListVal(vs...)
	case string:
		if n.Spec.Name == "Constant" && f.Name == "value" {
			return val.NewStringVal(x)
		}
		return val.NewStringVal(x)
	case int:
		return val.Int64Val(int64(x))
	case pyast.NoneT:
		return val.NoneVal()
	case bool:
		return val.BoolVal(x)
	case int64:
		return val.Int64Val(x)
	case *big.Int:
		return val.NewBigIntVal(x)
	case float64:
		return val.Float64Val(x)
	case complex128:
		return newComplexVal(real(x), imag(x))
	case []byte:
		return val.NewBytesVal(x)
	case pyast.EllipsisT:
		return val.EllipsisVal()
	case pyast.TupleT:
		vs := make([]val.Value, len(x))
		for j, c := range x {
			vs[j] = st.constValue(c)
		}
		return val.NewTupleVal(vs...)
	}
	return val.NoneVal()
}

func (st *astState) constValue(c any) val.Value {
	tmp := pyast.New("Constant", c, nil)
	return st.fieldValue(tmp, 0)
}

func newComplexVal(re, im float64) val.Value {
	return val.NewComplexVal(re, im)
}

// ---- Value -> Node --------------------------------------------------------

func astTypeName(v val.Value) string { return typeNameOf(v) }

func (st *astState) getField(v val.Value, name string) (val.Value, bool) {
	if inst := instOf(v); inst != nil {
		if x, ok := inst.GetAttr(internS(name)); ok && !x.IsUnbound() {
			return x, true
		}
	}
	// class-level defaults (e.g. Constant.kind = None)
	if x, _, ok := v.Type().LookupMRO(internS(name)); ok && !x.IsUnbound() {
		return x, true
	}
	return val.NoneVal(), false
}

func astErr(msg string) { panic("TypeError: " + msg) }

// toNode converts an `ast.AST` object into a pyast.Node, validating shapes like
// CPython's obj2ast. expect is the ASDL class the value must be an instance of.
func (st *astState) toNode(ctx *val.ExecContext, v val.Value, expect string) *pyast.Node {
	exp := st.byName[expect]
	if !v.IsInstance() || exp == nil || !val.IsSubclass(v.Type(), exp) {
		astErr("expected some sort of " + expect + ", but got " + val.PyRepr(ctx, v))
	}
	sp := st.specOf(v.Type())
	if sp == nil || !sp.Concrete {
		astErr("expected some sort of " + expect + ", but got " + val.PyRepr(ctx, v))
	}
	n := pyast.NewEmpty(sp)
	for i, f := range sp.Fields {
		fv, ok := st.getField(v, f.Name)
		if !ok || (fv.IsNone() && !f.Opt && !f.List && f.Type != "object") {
			if f.Opt || f.List {
				continue
			}
			if f.Type == "expr_context" {
				n.F[i] = pyast.New("Load")
				continue
			}
			if fv.IsNone() && ok {
				astErr("required field \"" + f.Name + "\" missing from " + sp.Name)
			}
			astErr("required field \"" + f.Name + "\" missing from " + sp.Name)
		}
		n.F[i] = st.convertField(ctx, sp, f, fv)
	}
	for i, a := range sp.Attrs {
		av, ok := st.getField(v, a)
		if !ok || av.IsNone() {
			continue
		}
		if x, isInt := intOf(ctx, av); isInt {
			n.Pos[i] = int(x)
			n.HasPos |= 1 << uint(i)
		} else {
			astErr("field \"" + a + "\" must be an int, not " + astTypeName(av))
		}
	}
	return n
}

func (st *astState) convertField(ctx *val.ExecContext, sp *pyast.Spec, f pyast.Field, v val.Value) any {
	if f.List {
		var items []val.Value
		switch {
		case v.IsList():
			items = v.AsList().Items
		case v.IsTuple():
			items = v.AsTuple().Items
		default:
			astErr(sp.Name + " field \"" + f.Name + "\" must be a list, not a " + astTypeName(v))
		}
		if f.Type == "str" {
			out := make([]string, len(items))
			for i, it := range items {
				if !it.IsAnyString() {
					astErr("AST identifier must be of type str")
				}
				out[i] = it.AsString()
			}
			return out
		}
		out := make([]*pyast.Node, len(items))
		for i, it := range items {
			if it.IsNone() && (sp.Name == "Dict" && f.Name == "keys" || sp.Name == "arguments" && f.Name == "kw_defaults") {
				continue
			}
			out[i] = st.toNode(ctx, it, f.Type)
		}
		return out
	}
	if v.IsNone() {
		if f.Opt {
			return nil
		}
		if sp.Name == "Constant" && f.Name == "value" {
			return pyast.NoneT{}
		}
	}
	switch f.Type {
	case "str":
		if !v.IsAnyString() {
			astErr("AST identifier must be of type str")
		}
		return v.AsString()
	case "int":
		x, ok := intOf(ctx, v)
		if !ok {
			astErr("field \"" + f.Name + "\" must be an int, not " + astTypeName(v))
		}
		return int(x)
	case "object":
		return st.toConst(ctx, sp, v)
	}
	return st.toNode(ctx, v, f.Type)
}

func (st *astState) toConst(ctx *val.ExecContext, sp *pyast.Spec, v val.Value) any {
	switch {
	case v.IsNone():
		return pyast.NoneT{}
	case v.IsBool():
		return v.IsTruthy()
	case v.IsInt64():
		return v.AsInt64()
	case v.IsBigInt():
		b := new(big.Int).Set(&v.AsBigInt().Val)
		return pyast.BigOrInt(b)
	case v.IsFloat64():
		return v.AsFloat64()
	case v.IsAnyString():
		return v.AsString()
	case v.IsBytes():
		return append([]byte(nil), v.AsBytes().Data...)
	case v.Kind() == val.KindEllipsis:
		return pyast.EllipsisT{}
	case v.IsComplex():
		c := v.AsComplex()
		return complex(c.Real, c.Imag)
	case v.IsTuple():
		items := v.AsTuple().Items
		out := make(pyast.TupleT, len(items))
		for i, it := range items {
			out[i] = st.toConst(ctx, sp, it)
		}
		return out
	}
	if sp.Name == "MatchSingleton" {
		astErr("MatchSingleton can only contain True, False and None")
	}
	astErr("got an invalid type in Constant: " + astTypeName(v))
	return nil
}

// ---- module ---------------------------------------------------------------

func (e *Engine) createASTModule() val.Value {
	st := astInit()
	modVal := val.NewModuleVal("_ast", "")
	mod := modVal.AsModule()
	mod.Dict.Set(internS("AST"), val.TypeVal(st.astType))
	for _, sp := range pyast.Specs {
		mod.Dict.Set(internS(sp.Name), val.TypeVal(st.classes[sp.ID]))
	}
	mod.Dict.Set(internS("PyCF_ONLY_AST"), val.Int64Val(pyCFOnlyAST))
	mod.Dict.Set(internS("PyCF_TYPE_COMMENTS"), val.Int64Val(pyCFTypeComments))
	mod.Dict.Set(internS("PyCF_ALLOW_TOP_LEVEL_AWAIT"), val.Int64Val(pyCFTopLevelAwait))
	mod.Dict.Set(internS("PyCF_OPTIMIZED_AST"), val.Int64Val(pyCFOptimizedAST))
	return modVal
}

// parseToAST parses src in the given compile mode and returns the `_ast` tree.
func parseToAST(src []byte, filename, mode string) val.Value {
	st := astInit()
	var m pyast.Mode
	switch mode {
	case "exec":
		m = pyast.ModeExec
	case "eval":
		m = pyast.ModeEval
	case "single":
		m = pyast.ModeSingle
	case "func_type":
		m = pyast.ModeFuncType
	default:
		panic("ValueError: compile() mode must be 'exec', 'eval', 'single' or 'func_type'")
	}
	tree, err := pyast.Parse(src, m)
	if err != nil {
		panic(newSyntaxErrorVal(err.(*pyast.SyntaxError), filename))
	}
	return st.newNode(tree)
}

func newSyntaxErrorVal(se *pyast.SyntaxError, filename string) val.Value {
	cls := val.SyntaxErrorType
	switch se.Kind {
	case "IndentationError":
		cls = val.IndentationErrorType
	case "TabError":
		cls = val.TabErrorType
	}
	exc := val.NewException(cls, se.Msg)
	inst := exc.AsInstance()
	text := val.NewStringVal(se.Text + "\n")
	details := val.NewTupleVal(val.NewStringVal(filename), val.Int64Val(int64(se.Lineno)), val.Int64Val(int64(se.Offset)), text,
		val.Int64Val(int64(se.EndLineno)), val.Int64Val(int64(se.EndOffset)))
	inst.SetAttr(internS("args"), val.NewTupleVal(val.NewStringVal(se.Msg), details))
	inst.SetAttr(internS("msg"), val.NewStringVal(se.Msg))
	inst.SetAttr(internS("filename"), val.NewStringVal(filename))
	inst.SetAttr(internS("lineno"), val.Int64Val(int64(se.Lineno)))
	inst.SetAttr(internS("offset"), val.Int64Val(int64(se.Offset)))
	inst.SetAttr(internS("text"), text)
	inst.SetAttr(internS("end_lineno"), val.Int64Val(int64(se.EndLineno)))
	inst.SetAttr(internS("end_offset"), val.Int64Val(int64(se.EndOffset)))
	inst.SetAttr(internS("print_file_and_line"), val.NoneVal())
	return exc
}

// ---- compile() ------------------------------------------------------------

func astModForMode(mode string) string {
	switch mode {
	case "exec":
		return "Module"
	case "eval":
		return "Expression"
	case "single":
		return "Interactive"
	case "func_type":
		return "FunctionType"
	}
	return ""
}

func sourceBytes(v val.Value) ([]byte, bool) {
	switch {
	case v.IsAnyString():
		return []byte(v.AsString()), true
	case v.IsBytes():
		return v.AsBytes().Data, true
	}
	if v.IsByteArray() {
		return append([]byte(nil), v.AsByteArray().Data...), true
	}
	return nil, false
}

// builtinCompile implements compile(source, filename, mode, flags=0,
// dont_inherit=False, optimize=-1, *, _feature_version=-1, module=None).
func builtinCompile(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	a := bindArgs("compile", args, kwnames, "source", "filename", "mode", "flags", "dont_inherit", "optimize", "_feature_version", "module")
	for i, name := range []string{"source", "filename", "mode"} {
		if a[i].IsUnbound() {
			panic("TypeError: compile() missing required argument '" + name + "' (pos " + strconv.Itoa(i+1) + ")")
		}
	}
	srcVal, fnVal, modeVal := a[0], a[1], a[2]
	flags := int(intArg(ctx, a[3], 0))
	optimize := int(intArg(ctx, a[5], -1))
	if optimize < -1 || optimize > 2 {
		panic("ValueError: compile(): invalid optimize value")
	}
	if !modeVal.IsAnyString() {
		panic("TypeError: compile() argument 'mode' must be str, not " + typeNameOf(modeVal))
	}
	mode := modeVal.AsString()
	if astModForMode(mode) == "" {
		panic("ValueError: compile() mode must be 'exec', 'eval', 'single' or 'func_type'")
	}
	filename := "<string>"
	switch {
	case fnVal.IsAnyString():
		filename = fnVal.AsString()
	case fnVal.IsBytes():
		filename = string(fnVal.AsBytes().Data)
	case fnVal.IsInstance():
		if fn, _, ok := fnVal.Type().LookupMRO(internS("__fspath__")); ok {
			if r := ctx.Invoke1(fn, fnVal); r.IsAnyString() {
				filename = r.AsString()
			}
		}
	}

	st := astInit()
	if srcVal.IsInstance() && isASTType(st, srcVal.Type()) {
		want := astModForMode(mode)
		node := st.toNode(ctx, srcVal, "mod")
		if node.Spec.Name != want {
			panic("TypeError: expected " + want + " node, got " + srcVal.Type().Name)
		}
		if flags&pyCFOnlyAST != 0 {
			return srcVal
		}
		if mode == "func_type" {
			panic("ValueError: compile() mode 'func_type' requires flag PyCF_ONLY_AST")
		}
		text := pyast.Unparse(node, pyast.UnparseOptions{PadLines: true})
		if mode == "single" {
			text += "\n"
		}
		resVal := compileSourceWithOptimize(ctx, []byte(text), filename, mode, optimize)
		if co := AsCodeObject(resVal); co != nil {
			unmangleCodeObject(co)
		}
		return resVal
	}
	if srcVal.Kind() == val.KindCode {
		return srcVal
	}
	src, ok := sourceBytes(srcVal)
	if !ok {
		panic("TypeError: compile() arg 1 must be a string, bytes or AST object")
	}
	for _, c := range src {
		if c == 0 {
			panic("ValueError: source code string cannot contain null bytes")
		}
	}
	if len(src) >= 3 && src[0] == 0xEF && src[1] == 0xBB && src[2] == 0xBF {
		src = src[3:]
	}
	if flags&pyCFOnlyAST != 0 {
		return parseToAST(src, filename, mode)
	}
	if mode == "func_type" {
		panic("ValueError: compile() mode 'func_type' requires flag PyCF_ONLY_AST")
	}
	if flags&0x4000 != 0 { // PyCF_ALLOW_INCOMPLETE_INPUT
		if vmInstance := ctx.VM.(*VM); vmInstance.engine.IncompleteInput(src) {
			panic(val.NewException(val.IncompleteInputErrorType, "incomplete input"))
		}
	}
	return compileSourceWithOptimize(ctx, src, filename, mode, optimize)
}

func compileSource(ctx *val.ExecContext, src []byte, filename, mode string) val.Value {
	return compileSourceWithOptimize(ctx, src, filename, mode, -1)
}

func compileSourceWithOptimize(ctx *val.ExecContext, src []byte, filename, mode string, optimize int) val.Value {
	m := 0
	switch mode {
	case "eval":
		m = 1
	case "single":
		m = 2
	}
	vmInstance := ctx.VM.(*VM)
	co, err := vmInstance.engine.CompileDynamicWithOptimize(src, filename, m, optimize)
	if err != nil {
		msg := err.Error()
		msg = strings.TrimPrefix(msg, "SyntaxError: ")
		panic("SyntaxError: " + msg)
	}
	return NewCodeVal(co)
}

func unmangleCodeObject(co *CodeObject) {
	if co == nil {
		return
	}
	for i, v := range co.VarNames {
		s := v.AsString()
		if strings.HasPrefix(s, pyast.SynthAtPrefix) {
			co.VarNames[i] = val.InternString(pyast.DecodeSynthIdent(s))
		}
	}
	for i, v := range co.ParamNames {
		s := v.AsString()
		if strings.HasPrefix(s, pyast.SynthAtPrefix) {
			co.ParamNames[i] = val.InternString(pyast.DecodeSynthIdent(s))
		}
	}
	for i, v := range co.FreeVars {
		s := v.AsString()
		if strings.HasPrefix(s, pyast.SynthAtPrefix) {
			co.FreeVars[i] = val.InternString(pyast.DecodeSynthIdent(s))
		}
	}
	for i, v := range co.CellVars {
		s := v.AsString()
		if strings.HasPrefix(s, pyast.SynthAtPrefix) {
			co.CellVars[i] = val.InternString(pyast.DecodeSynthIdent(s))
		}
	}
	for i, c := range co.Consts {
		if c.IsAnyString() {
			s := c.AsString()
			if strings.HasPrefix(s, pyast.SynthAtPrefix) {
				co.Consts[i] = val.InternString(pyast.DecodeSynthIdent(s))
			}
		} else if c.Kind() == val.KindCode {
			if childCO := AsCodeObject(c); childCO != nil {
				unmangleCodeObject(childCO)
			}
		}
	}
}
