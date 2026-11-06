package vm

import (
	"strings"
	"sync"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	typingOnce sync.Once

	UnionType           *val.PyType
	GenericType         *val.PyType
	GenericAliasType    *val.PyType
	ParamSpecArgsType   *val.PyType
	ParamSpecKwargsType *val.PyType
	NoDefaultType       *val.PyType
	NoDefaultVal        val.Value
)

type pyUnion struct {
	Header val.ObjectHeader
	Args   []val.Value
}

type pyGenericAlias struct {
	Header val.ObjectHeader
	Origin val.Value
	Args   []val.Value
}

type pyParamSpecArgs struct {
	Header val.ObjectHeader
	Origin val.Value
}

type pyParamSpecKwargs struct {
	Header val.ObjectHeader
	Origin val.Value
}

type pyNoDefault struct {
	Header val.ObjectHeader
}

func asTypeVar(v val.Value) *val.PyTypeVar {
	return (*val.PyTypeVar)(unsafe.Pointer(v.HeaderPtr()))
}

func asParamSpec(v val.Value) *val.PyParamSpec {
	return (*val.PyParamSpec)(unsafe.Pointer(v.HeaderPtr()))
}

func asTypeVarTuple(v val.Value) *val.PyTypeVarTuple {
	return (*val.PyTypeVarTuple)(unsafe.Pointer(v.HeaderPtr()))
}

func asTypeAliasType(v val.Value) *val.PyTypeAliasType {
	return (*val.PyTypeAliasType)(unsafe.Pointer(v.HeaderPtr()))
}

func asUnion(v val.Value) *pyUnion {
	return (*pyUnion)(unsafe.Pointer(v.HeaderPtr()))
}

func asGenericAlias(v val.Value) *pyGenericAlias {
	return (*pyGenericAlias)(unsafe.Pointer(v.HeaderPtr()))
}

func asParamSpecArgs(v val.Value) *pyParamSpecArgs {
	return (*pyParamSpecArgs)(unsafe.Pointer(v.HeaderPtr()))
}

func asParamSpecKwargs(v val.Value) *pyParamSpecKwargs {
	return (*pyParamSpecKwargs)(unsafe.Pointer(v.HeaderPtr()))
}

func initTypingTypes() {
	typingOnce.Do(func() {
		initTypingTypesBody()
	})
}

func initTypingTypesBody() {
	NoDefaultType = val.MustNewType("NoDefault", []*val.PyType{val.ObjectType}, nil)
	nd := &pyNoDefault{Header: val.ObjectHeader{Kind: val.KindNative, Type: NoDefaultType}}
	NoDefaultVal = val.ObjectVal(&nd.Header)
	NoDefaultType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("NoDefault.__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.InternString("typing.NoDefault")
	}))
	NoDefaultType.Dict.Set(val.InternString("__module__"), val.InternString("typing"))
	NoDefaultType.InvalidateVersion()

	UnionType = val.MustNewType("UnionType", []*val.PyType{val.ObjectType}, nil)
	GenericAliasType = val.MustNewType("GenericAlias", []*val.PyType{val.ObjectType}, nil)
	ParamSpecArgsType = val.MustNewType("ParamSpecArgs", []*val.PyType{val.ObjectType}, nil)
	ParamSpecKwargsType = val.MustNewType("ParamSpecKwargs", []*val.PyType{val.ObjectType}, nil)
	GenericType = val.MustNewType("Generic", []*val.PyType{val.ObjectType}, nil)

	for _, t := range []*val.PyType{UnionType, GenericAliasType, ParamSpecArgsType, ParamSpecKwargsType, GenericType, val.TypeVarType, val.ParamSpecType, val.TypeVarTupleType, val.TypeAliasTypeType} {
		t.Dict.Set(val.InternString("__module__"), val.InternString("typing"))
	}

	installTypeVarMethods()
	installParamSpecMethods()
	installTypeVarTupleMethods()
	installTypeAliasMethods()
	installUnionMethods()
	installGenericAliasMethods()
	installParamSpecArgsMethods()
	installGenericMethods()
	installBuiltinClassGetItem()

	val.UnionOrHook = func(ctx *val.ExecContext, a, b val.Value) (val.Value, bool) {
		if !isUnionable(a) || !isUnionable(b) {
			return val.NoneVal(), false
		}
		return makeUnion([]val.Value{a, b}), true
	}
}

func installBuiltinClassGetItem() {
	getter := val.NewClassMethodVal(val.TypeVal(GenericAliasType))
	name := val.InternString("__class_getitem__")
	for _, t := range []*val.PyType{val.ListType, val.DictType, val.TupleType, val.SetType, val.TypeType} {
		if t == nil {
			continue
		}
		// Dict lookup finds __class_getitem__. Do not InvalidateVersion:
		// re-syncing set/list slots wraps TpIter around __iter__, which calls TpIter.
		t.Dict.Set(name, getter)
	}
}

func (e *Engine) createTypingModule() val.Value {
	initTypingTypes()
	modVal := val.NewModuleVal("_typing", "")
	mod := modVal.AsModule()
	d := mod.Dict
	d.Set(val.InternString("_idfunc"), val.NewBuiltinFuncVal("_idfunc", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: _idfunc() missing 1 required positional argument")
		}
		return args[0]
	}))
	d.Set(val.InternString("TypeVar"), val.TypeVal(val.TypeVarType))
	d.Set(val.InternString("ParamSpec"), val.TypeVal(val.ParamSpecType))
	d.Set(val.InternString("TypeVarTuple"), val.TypeVal(val.TypeVarTupleType))
	d.Set(val.InternString("ParamSpecArgs"), val.TypeVal(ParamSpecArgsType))
	d.Set(val.InternString("ParamSpecKwargs"), val.TypeVal(ParamSpecKwargsType))
	d.Set(val.InternString("TypeAliasType"), val.TypeVal(val.TypeAliasTypeType))
	d.Set(val.InternString("Generic"), val.TypeVal(GenericType))
	d.Set(val.InternString("Union"), val.TypeVal(UnionType))
	d.Set(val.InternString("NoDefault"), NoDefaultVal)
	return modVal
}

func splitKeywords(args, kwnames []val.Value) (pos []val.Value, kw map[string]val.Value) {
	nkw := len(kwnames)
	if nkw == 0 {
		return args, nil
	}
	if nkw > len(args) {
		panic("TypeError: too many keyword arguments")
	}
	pos = args[:len(args)-nkw]
	kw = make(map[string]val.Value, nkw)
	for i, name := range kwnames {
		kw[name.AsString()] = args[len(pos)+i]
	}
	return pos, kw
}

func kwOr(kw map[string]val.Value, name string, fallback val.Value) val.Value {
	if kw == nil {
		return fallback
	}
	if v, ok := kw[name]; ok {
		return v
	}
	return fallback
}

func valueOrNone(v val.Value) val.Value {
	if v.HeaderPtr() == nil || v.IsUnbound() {
		return val.NoneVal()
	}
	return v
}

func hasTypingDefault(v val.Value) bool {
	if v.HeaderPtr() == nil || v.IsUnbound() {
		return false
	}
	return v.HeaderPtr() != NoDefaultVal.HeaderPtr()
}

func callerModuleName(ctx *val.ExecContext) string {
	if ctx == nil || ctx.VM == nil {
		return "typing"
	}
	vm, ok := ctx.VM.(*VM)
	if !ok || vm.activeFrameIdx < 0 || vm.activeFrameIdx >= len(vm.frames) {
		return "typing"
	}
	g := vm.frames[vm.activeFrameIdx].globals
	if g == nil {
		return "typing"
	}
	if n, ok := g.Get(val.InternString("__name__")); ok && n.IsAnyString() {
		name := n.AsString()
		if name != "" {
			return name
		}
	}
	return "typing"
}

func typingDict(ctx *val.ExecContext) *val.PyDict {
	if ctx == nil || ctx.VM == nil {
		return nil
	}
	vm, ok := ctx.VM.(*VM)
	if !ok || vm.modules == nil {
		return nil
	}
	mod, ok := vm.modules.Get(val.InternString("typing"))
	if !ok || !mod.IsModule() {
		return nil
	}
	return mod.AsModule().Dict
}

func callTyping(ctx *val.ExecContext, name string, args []val.Value, kwnames []val.Value) (val.Value, bool) {
	d := typingDict(ctx)
	if d == nil || ctx == nil {
		return val.NoneVal(), false
	}
	fn, ok := d.Get(val.InternString(name))
	if !ok {
		return val.NoneVal(), false
	}
	return ctx.Call(fn, args, kwnames), true
}

func tryGetAttr(ctx *val.ExecContext, obj val.Value, name string) (val.Value, bool) {
	if obj.HeaderPtr() == nil {
		return val.NoneVal(), false
	}
	interned := val.InternString(name)
	if obj.IsType() {
		if v, ok := obj.AsType().Dict.Get(interned); ok {
			return v, true
		}
	}
	if obj.IsInstance() {
		if v, ok := obj.AsInstance().GetAttr(interned); ok {
			return v, true
		}
	}
	if _, _, ok := obj.Type().LookupMRO(interned); ok {
		return val.GetAttrCtx(ctx, obj, interned), true
	}
	return val.NoneVal(), false
}

func installTypeVarMethods() {
	val.TypeVarType.Slots.TpCall = typeVarTpCall
	val.TypeVarType.Slots.TpGetAttr = typeVarGetAttr
	val.TypeVarType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("TypeVar.__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__repr__' requires an argument")
		}
		return val.InternString(typeVarRepr(asTypeVar(args[0])))
	}))
	val.TypeVarType.Dict.Set(val.InternString("has_default"), val.NewBuiltinFuncVal("TypeVar.has_default", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: has_default() missing self")
		}
		return val.BoolVal(hasTypingDefault(asTypeVar(args[0]).Default))
	}))
	val.TypeVarType.Dict.Set(val.InternString("__typing_subst__"), val.NewBuiltinFuncVal("TypeVar.__typing_subst__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: __typing_subst__() missing argument")
		}
		if v, ok := callTyping(ctx, "_typevar_subst", args[:2], nil); ok {
			return v
		}
		return args[1]
	}))
	val.TypeVarType.Dict.Set(val.InternString("__mro_entries__"), val.NewBuiltinFuncVal("TypeVar.__mro_entries__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		panic("TypeError: type variables cannot be used as a base class")
	}))
	val.TypeVarType.InvalidateVersion()
}

func typeVarTpCall(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	pos, kw := splitKeywords(args, kwnames)
	if len(pos) < 1 || !pos[0].IsAnyString() {
		panic("TypeError: TypeVar() requires a string name")
	}
	name := pos[0].AsString()
	constraints := append([]val.Value(nil), pos[1:]...)
	bound := valueOrNone(kwOr(kw, "bound", val.NoneVal()))
	if len(constraints) > 0 && bound.HeaderPtr() != nil && !bound.IsNone() {
		panic("TypeError: TypeVar cannot have both constraints and a bound")
	}
	covariant := kwOr(kw, "covariant", val.BoolVal(false)).IsTruthy()
	contravariant := kwOr(kw, "contravariant", val.BoolVal(false)).IsTruthy()
	infer := kwOr(kw, "infer_variance", val.BoolVal(false)).IsTruthy()
	if covariant && contravariant {
		panic("TypeError: TypeVar cannot be both covariant and contravariant")
	}
	if infer && (covariant || contravariant) {
		panic("TypeError: TypeVar cannot combine infer_variance with explicit variance")
	}
	def := kwOr(kw, "default", NoDefaultVal)
	tv := &val.PyTypeVar{
		Header:              val.ObjectHeader{Kind: val.KindNative, Type: val.TypeVarType},
		Name:                name,
		Bound:               bound,
		Constraints:         constraints,
		Default:             def,
		Covariant:           covariant,
		Contravariant:       contravariant,
		InferVariance:       infer,
		Module:              callerModuleName(ctx),
		EvaluateBound:       kwOr(kw, "evaluate_bound", val.NoneVal()),
		EvaluateConstraints: kwOr(kw, "evaluate_constraints", val.NoneVal()),
		EvaluateDefault:     kwOr(kw, "evaluate_default", val.NoneVal()),
	}
	return val.ObjectVal(&tv.Header)
}

func typeVarRepr(tv *val.PyTypeVar) string {
	if tv.InferVariance {
		return "~" + tv.Name
	}
	if tv.Covariant {
		return "+" + tv.Name
	}
	if tv.Contravariant {
		return "-" + tv.Name
	}
	return tv.Name
}

func typeVarGetAttr(ctx *val.ExecContext, obj, name val.Value) val.Value {
	tv := asTypeVar(obj)
	switch name.AsString() {
	case "__name__":
		return val.InternString(tv.Name)
	case "__bound__":
		return valueOrNone(tv.Bound)
	case "__constraints__":
		return val.NewTupleVal(tv.Constraints...)
	case "__covariant__":
		return val.BoolVal(tv.Covariant)
	case "__contravariant__":
		return val.BoolVal(tv.Contravariant)
	case "__infer_variance__":
		return val.BoolVal(tv.InferVariance)
	case "__default__":
		if hasTypingDefault(tv.Default) {
			return tv.Default
		}
		return NoDefaultVal
	case "__module__":
		if tv.Module == "" {
			return val.InternString("typing")
		}
		return val.InternString(tv.Module)
	case "evaluate_bound":
		return valueOrNone(tv.EvaluateBound)
	case "evaluate_constraints":
		return valueOrNone(tv.EvaluateConstraints)
	case "evaluate_default":
		return valueOrNone(tv.EvaluateDefault)
	default:
		return val.ObjectGetAttr(ctx, obj, name)
	}
}

func installParamSpecMethods() {
	val.ParamSpecType.Slots.TpCall = paramSpecTpCall
	val.ParamSpecType.Slots.TpGetAttr = paramSpecGetAttr
	val.ParamSpecType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("ParamSpec.__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__repr__' requires an argument")
		}
		ps := asParamSpec(args[0])
		prefix := ""
		if ps.InferVariance {
			prefix = "~"
		} else if ps.Covariant {
			prefix = "+"
		} else if ps.Contravariant {
			prefix = "-"
		}
		return val.InternString(prefix + ps.Name)
	}))
	val.ParamSpecType.Dict.Set(val.InternString("has_default"), val.NewBuiltinFuncVal("ParamSpec.has_default", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: has_default() missing self")
		}
		return val.BoolVal(hasTypingDefault(asParamSpec(args[0]).Default))
	}))
	val.ParamSpecType.Dict.Set(val.InternString("__typing_subst__"), val.NewBuiltinFuncVal("ParamSpec.__typing_subst__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: __typing_subst__() missing argument")
		}
		if v, ok := callTyping(ctx, "_paramspec_subst", args[:2], nil); ok {
			return v
		}
		return args[1]
	}))
	val.ParamSpecType.Dict.Set(val.InternString("__typing_prepare_subst__"), val.NewBuiltinFuncVal("ParamSpec.__typing_prepare_subst__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			panic("TypeError: __typing_prepare_subst__() missing arguments")
		}
		if v, ok := callTyping(ctx, "_paramspec_prepare_subst", args[:3], nil); ok {
			return v
		}
		return args[2]
	}))
	val.ParamSpecType.Dict.Set(val.InternString("__mro_entries__"), val.NewBuiltinFuncVal("ParamSpec.__mro_entries__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		panic("TypeError: parameter specifications cannot be used as a base class")
	}))
	val.ParamSpecType.InvalidateVersion()
}

func paramSpecTpCall(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	pos, kw := splitKeywords(args, kwnames)
	if len(pos) < 1 || !pos[0].IsAnyString() {
		panic("TypeError: ParamSpec() requires a string name")
	}
	if len(pos) > 1 {
		panic("TypeError: ParamSpec() takes exactly 1 positional argument")
	}
	covariant := kwOr(kw, "covariant", val.BoolVal(false)).IsTruthy()
	contravariant := kwOr(kw, "contravariant", val.BoolVal(false)).IsTruthy()
	infer := kwOr(kw, "infer_variance", val.BoolVal(false)).IsTruthy()
	if covariant && contravariant {
		panic("TypeError: ParamSpec cannot be both covariant and contravariant")
	}
	ps := &val.PyParamSpec{
		Header:        val.ObjectHeader{Kind: val.KindNative, Type: val.ParamSpecType},
		Name:          pos[0].AsString(),
		Bound:         valueOrNone(kwOr(kw, "bound", val.NoneVal())),
		Default:       kwOr(kw, "default", NoDefaultVal),
		Covariant:     covariant,
		Contravariant: contravariant,
		InferVariance: infer,
		Module:        callerModuleName(ctx),
	}
	return val.ObjectVal(&ps.Header)
}

func ensureParamSpecEnds(ps *val.PyParamSpec, self val.Value) {
	if ps.Args.HeaderPtr() == nil {
		a := &pyParamSpecArgs{Header: val.ObjectHeader{Kind: val.KindNative, Type: ParamSpecArgsType}, Origin: self}
		ps.Args = val.ObjectVal(&a.Header)
	}
	if ps.Kwargs.HeaderPtr() == nil {
		k := &pyParamSpecKwargs{Header: val.ObjectHeader{Kind: val.KindNative, Type: ParamSpecKwargsType}, Origin: self}
		ps.Kwargs = val.ObjectVal(&k.Header)
	}
}

func paramSpecGetAttr(ctx *val.ExecContext, obj, name val.Value) val.Value {
	ps := asParamSpec(obj)
	switch name.AsString() {
	case "__name__":
		return val.InternString(ps.Name)
	case "__bound__":
		return valueOrNone(ps.Bound)
	case "__covariant__":
		return val.BoolVal(ps.Covariant)
	case "__contravariant__":
		return val.BoolVal(ps.Contravariant)
	case "__infer_variance__":
		return val.BoolVal(ps.InferVariance)
	case "__default__":
		if hasTypingDefault(ps.Default) {
			return ps.Default
		}
		return NoDefaultVal
	case "__module__":
		if ps.Module == "" {
			return val.InternString("typing")
		}
		return val.InternString(ps.Module)
	case "args":
		ensureParamSpecEnds(ps, obj)
		return ps.Args
	case "kwargs":
		ensureParamSpecEnds(ps, obj)
		return ps.Kwargs
	default:
		return val.ObjectGetAttr(ctx, obj, name)
	}
}

func installTypeVarTupleMethods() {
	val.TypeVarTupleType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pos, kw := splitKeywords(args, kwnames)
		if len(pos) < 1 || !pos[0].IsAnyString() {
			panic("TypeError: TypeVarTuple() requires a string name")
		}
		tvt := &val.PyTypeVarTuple{
			Header:  val.ObjectHeader{Kind: val.KindNative, Type: val.TypeVarTupleType},
			Name:    pos[0].AsString(),
			Default: kwOr(kw, "default", NoDefaultVal),
			Module:  callerModuleName(ctx),
		}
		return val.ObjectVal(&tvt.Header)
	}
	val.TypeVarTupleType.Slots.TpGetAttr = func(ctx *val.ExecContext, obj, name val.Value) val.Value {
		tvt := asTypeVarTuple(obj)
		switch name.AsString() {
		case "__name__":
			return val.InternString(tvt.Name)
		case "__default__":
			if hasTypingDefault(tvt.Default) {
				return tvt.Default
			}
			return NoDefaultVal
		case "__module__":
			if tvt.Module == "" {
				return val.InternString("typing")
			}
			return val.InternString(tvt.Module)
		default:
			return val.ObjectGetAttr(ctx, obj, name)
		}
	}
	val.TypeVarTupleType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("TypeVarTuple.__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__repr__' requires an argument")
		}
		return val.InternString("*" + asTypeVarTuple(args[0]).Name)
	}))
	val.TypeVarTupleType.Dict.Set(val.InternString("has_default"), val.NewBuiltinFuncVal("TypeVarTuple.has_default", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: has_default() missing self")
		}
		return val.BoolVal(hasTypingDefault(asTypeVarTuple(args[0]).Default))
	}))
	val.TypeVarTupleType.Dict.Set(val.InternString("__typing_prepare_subst__"), val.NewBuiltinFuncVal("TypeVarTuple.__typing_prepare_subst__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			panic("TypeError: __typing_prepare_subst__() missing arguments")
		}
		if v, ok := callTyping(ctx, "_typevartuple_prepare_subst", args[:3], nil); ok {
			return v
		}
		return args[2]
	}))
	val.TypeVarTupleType.Dict.Set(val.InternString("__typing_subst__"), val.NewBuiltinFuncVal("TypeVarTuple.__typing_subst__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		panic("TypeError: substitution of bare TypeVarTuple is not supported")
	}))
	val.TypeVarTupleType.InvalidateVersion()
}

func installTypeAliasMethods() {
	val.TypeAliasTypeType.Slots.TpGetAttr = func(ctx *val.ExecContext, obj, name val.Value) val.Value {
		tat := asTypeAliasType(obj)
		switch name.AsString() {
		case "__name__":
			return val.InternString(tat.Name)
		case "__value__":
			return valueOrNone(tat.Value)
		case "__type_params__":
			return val.NewTupleVal(tat.TypeParams...)
		case "__module__":
			return val.InternString("typing")
		default:
			return val.ObjectGetAttr(ctx, obj, name)
		}
	}
	val.TypeAliasTypeType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("TypeAliasType.__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__repr__' requires an argument")
		}
		return val.InternString(asTypeAliasType(args[0]).Name)
	}))
	val.TypeAliasTypeType.InvalidateVersion()
}

func isUnionable(v val.Value) bool {
	if v.HeaderPtr() == nil {
		return false
	}
	if v.IsNone() || v.IsType() {
		return true
	}
	switch v.Type() {
	case UnionType, GenericAliasType, val.TypeVarType, val.ParamSpecType, val.TypeVarTupleType, val.TypeAliasTypeType:
		return true
	default:
		return false
	}
}

func normalizeUnionArg(v val.Value) val.Value {
	if v.IsType() && v.AsType() == val.NoneType {
		return val.NoneVal()
	}
	return v
}

func makeUnion(parts []val.Value) val.Value {
	var flat []val.Value
	for _, p := range parts {
		p = normalizeUnionArg(p)
		if p.HeaderPtr() != nil && p.Type() == UnionType {
			flat = append(flat, asUnion(p).Args...)
			continue
		}
		flat = append(flat, p)
	}
	deduped := make([]val.Value, 0, len(flat))
	for _, a := range flat {
		seen := false
		for _, b := range deduped {
			if val.Equals(a, b) {
				seen = true
				break
			}
		}
		if !seen {
			deduped = append(deduped, a)
		}
	}
	if len(deduped) == 0 {
		panic("TypeError: Cannot take a union of no types")
	}
	if len(deduped) == 1 {
		return deduped[0]
	}
	u := &pyUnion{
		Header: val.ObjectHeader{Kind: val.KindNative, Type: UnionType},
		Args:   deduped,
	}
	return val.ObjectVal(&u.Header)
}

func unionRepr(ctx *val.ExecContext, args []val.Value) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = unionArgRepr(ctx, a)
	}
	return strings.Join(parts, " | ")
}

func unionArgRepr(ctx *val.ExecContext, v val.Value) string {
	if v.IsNone() {
		return "None"
	}
	if v.IsType() {
		if v.AsType() == val.NoneType {
			return "None"
		}
		return val.TypeQualName(v.AsType())
	}
	if v.Type() == val.TypeVarType {
		return typeVarRepr(asTypeVar(v))
	}
	if v.Type() == val.ParamSpecType {
		return asParamSpec(v).Name
	}
	if v.Type() == GenericAliasType {
		return genericAliasRepr(ctx, asGenericAlias(v))
	}
	return val.PyRepr(ctx, v)
}

func installUnionMethods() {
	UnionType.Slots.TpGetAttr = func(ctx *val.ExecContext, obj, name val.Value) val.Value {
		u := asUnion(obj)
		switch name.AsString() {
		case "__args__":
			return val.NewTupleVal(u.Args...)
		case "__origin__":
			return val.TypeVal(UnionType)
		case "__parameters__":
			return val.NewTupleVal(collectTypeParams(ctx, u.Args)...)
		default:
			return val.ObjectGetAttr(ctx, obj, name)
		}
	}
	UnionType.Dict.Set(val.InternString("__class_getitem__"), val.NewBuiltinFuncVal("Union.__class_getitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: Union[...] expected a type argument")
		}
		item := args[1]
		var parts []val.Value
		if item.IsTuple() {
			parts = item.AsTuple().Items
		} else {
			parts = []val.Value{item}
		}
		if len(parts) == 0 {
			panic("TypeError: Cannot take a union of no types")
		}
		return makeUnion(parts)
	}))
	UnionType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("UnionType.__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__repr__' requires an argument")
		}
		return val.InternString(unionRepr(ctx, asUnion(args[0]).Args))
	}))
	UnionType.Dict.Set(val.InternString("__eq__"), val.NewBuiltinFuncVal("UnionType.__eq__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 || args[1].Type() != UnionType {
			return val.NotImplementedVal()
		}
		return val.BoolVal(sameMembers(asUnion(args[0]).Args, asUnion(args[1]).Args))
	}))
	UnionType.Dict.Set(val.InternString("__hash__"), val.NewBuiltinFuncVal("UnionType.__hash__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: __hash__() missing self")
		}
		var acc uint64 = 0x9e3779b9
		for _, a := range asUnion(args[0]).Args {
			acc ^= uint64(val.HashValue(a)) + 0x9e3779b9 + (acc << 6) + (acc >> 2)
		}
		return val.Int64Val(int64(acc))
	}))
	UnionType.Dict.Set(val.InternString("__instancecheck__"), val.NewBuiltinFuncVal("UnionType.__instancecheck__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: __instancecheck__() missing arguments")
		}
		obj := args[1]
		for _, a := range asUnion(args[0]).Args {
			if unionInstanceMatch(obj, a) {
				return val.BoolVal(true)
			}
		}
		return val.BoolVal(false)
	}))
	UnionType.Dict.Set(val.InternString("__subclasscheck__"), val.NewBuiltinFuncVal("UnionType.__subclasscheck__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: __subclasscheck__() missing arguments")
		}
		cls := args[1]
		if !cls.IsType() {
			panic("TypeError: issubclass() arg 1 must be a class")
		}
		for _, a := range asUnion(args[0]).Args {
			if unionSubclassMatch(cls, a) {
				return val.BoolVal(true)
			}
		}
		return val.BoolVal(false)
	}))
	UnionType.Dict.Set(val.InternString("__mro_entries__"), val.NewBuiltinFuncVal("UnionType.__mro_entries__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		panic("TypeError: Cannot subclass a union")
	}))
	UnionType.InvalidateVersion()
}

func unionInstanceMatch(obj, arg val.Value) bool {
	if arg.IsNone() {
		return obj.IsNone()
	}
	if arg.IsType() {
		return val.IsSubclass(obj.Type(), arg.AsType())
	}
	return false
}

func unionSubclassMatch(cls, arg val.Value) bool {
	if arg.IsNone() {
		return cls.IsType() && cls.AsType() == val.NoneType
	}
	if arg.IsType() && cls.IsType() {
		return val.IsSubclass(cls.AsType(), arg.AsType())
	}
	return false
}

func sameMembers(a, b []val.Value) bool {
	if len(a) != len(b) {
		return false
	}
	used := make([]bool, len(b))
	for _, x := range a {
		found := false
		for j, y := range b {
			if used[j] {
				continue
			}
			if val.Equals(x, y) {
				used[j] = true
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func collectTypeParams(ctx *val.ExecContext, args []val.Value) []val.Value {
	var out []val.Value
	add := func(v val.Value) {
		for _, existing := range out {
			if val.Equals(existing, v) {
				return
			}
		}
		out = append(out, v)
	}
	for _, a := range args {
		if a.HeaderPtr() == nil {
			continue
		}
		switch a.Type() {
		case val.TypeVarType, val.ParamSpecType, val.TypeVarTupleType:
			add(a)
			continue
		}
		if params, ok := tryGetAttr(ctx, a, "__parameters__"); ok && params.IsTuple() {
			for _, p := range params.AsTuple().Items {
				add(p)
			}
		}
	}
	return out
}

func newGenericAlias(origin val.Value, args []val.Value) val.Value {
	ga := &pyGenericAlias{
		Header: val.ObjectHeader{Kind: val.KindNative, Type: GenericAliasType},
		Origin: origin,
		Args:   append([]val.Value(nil), args...),
	}
	return val.ObjectVal(&ga.Header)
}

func genericAliasRepr(ctx *val.ExecContext, ga *pyGenericAlias) string {
	origin := unionArgRepr(ctx, ga.Origin)
	if ga.Origin.IsType() {
		origin = val.TypeQualName(ga.Origin.AsType())
	}
	parts := make([]string, len(ga.Args))
	for i, a := range ga.Args {
		parts[i] = unionArgRepr(ctx, a)
	}
	return origin + "[" + strings.Join(parts, ", ") + "]"
}

func installGenericAliasMethods() {
	GenericAliasType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pos, _ := splitKeywords(args, kwnames)
		if len(pos) < 2 {
			panic("TypeError: GenericAlias expected 2 arguments")
		}
		var items []val.Value
		if pos[1].IsTuple() {
			items = pos[1].AsTuple().Items
		} else {
			items = []val.Value{pos[1]}
		}
		return newGenericAlias(pos[0], items)
	}
	GenericAliasType.Slots.TpGetAttr = func(ctx *val.ExecContext, obj, name val.Value) val.Value {
		ga := asGenericAlias(obj)
		switch name.AsString() {
		case "__origin__":
			return ga.Origin
		case "__args__":
			return val.NewTupleVal(ga.Args...)
		case "__parameters__":
			return val.NewTupleVal(collectTypeParams(ctx, ga.Args)...)
		case "__unpacked__":
			return val.BoolVal(false)
		default:
			return val.ObjectGetAttr(ctx, obj, name)
		}
	}
	GenericAliasType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("GenericAlias.__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: descriptor '__repr__' requires an argument")
		}
		return val.InternString(genericAliasRepr(ctx, asGenericAlias(args[0])))
	}))
	GenericAliasType.Dict.Set(val.InternString("__mro_entries__"), val.NewBuiltinFuncVal("GenericAlias.__mro_entries__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: __mro_entries__() missing self")
		}
		origin := asGenericAlias(args[0]).Origin
		if origin.IsType() {
			return val.NewTupleVal(origin)
		}
		return val.NewTupleVal()
	}))
	GenericAliasType.Dict.Set(val.InternString("__eq__"), val.NewBuiltinFuncVal("GenericAlias.__eq__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 || args[1].Type() != GenericAliasType {
			return val.NotImplementedVal()
		}
		a := asGenericAlias(args[0])
		b := asGenericAlias(args[1])
		if !val.Equals(a.Origin, b.Origin) || len(a.Args) != len(b.Args) {
			return val.BoolVal(false)
		}
		for i := range a.Args {
			if !val.Equals(a.Args[i], b.Args[i]) {
				return val.BoolVal(false)
			}
		}
		return val.BoolVal(true)
	}))
	GenericAliasType.Dict.Set(val.InternString("__hash__"), val.NewBuiltinFuncVal("GenericAlias.__hash__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: __hash__() missing self")
		}
		ga := asGenericAlias(args[0])
		acc := uint64(val.HashValue(ga.Origin))
		for _, a := range ga.Args {
			acc = acc*1000003 ^ uint64(val.HashValue(a))
		}
		return val.Int64Val(int64(acc))
	}))
	GenericAliasType.InvalidateVersion()
}

func installParamSpecArgsMethods() {
	newEnd := func(tp *val.PyType, kwargs bool) func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			pos, _ := splitKeywords(args, kwnames)
			if len(pos) < 1 {
				panic("TypeError: missing origin argument")
			}
			if kwargs {
				k := &pyParamSpecKwargs{Header: val.ObjectHeader{Kind: val.KindNative, Type: tp}, Origin: pos[0]}
				return val.ObjectVal(&k.Header)
			}
			a := &pyParamSpecArgs{Header: val.ObjectHeader{Kind: val.KindNative, Type: tp}, Origin: pos[0]}
			return val.ObjectVal(&a.Header)
		}
	}
	ParamSpecArgsType.Slots.TpCall = newEnd(ParamSpecArgsType, false)
	ParamSpecKwargsType.Slots.TpCall = newEnd(ParamSpecKwargsType, true)
	getOrigin := func(obj val.Value) val.Value {
		if obj.Type() == ParamSpecKwargsType {
			return asParamSpecKwargs(obj).Origin
		}
		return asParamSpecArgs(obj).Origin
	}
	attr := func(ctx *val.ExecContext, obj, name val.Value) val.Value {
		if name.AsString() == "__origin__" {
			return getOrigin(obj)
		}
		return val.ObjectGetAttr(ctx, obj, name)
	}
	ParamSpecArgsType.Slots.TpGetAttr = attr
	ParamSpecKwargsType.Slots.TpGetAttr = attr
	reprFn := func(suffix string) val.Value {
		return val.NewBuiltinFuncVal("ParamSpecEnd.__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: descriptor '__repr__' requires an argument")
			}
			origin := getOrigin(args[0])
			name := "?"
			if origin.Type() == val.ParamSpecType {
				name = asParamSpec(origin).Name
			}
			return val.InternString(name + suffix)
		})
	}
	ParamSpecArgsType.Dict.Set(val.InternString("__repr__"), reprFn(".args"))
	ParamSpecKwargsType.Dict.Set(val.InternString("__repr__"), reprFn(".kwargs"))
	refuse := val.NewBuiltinFuncVal("ParamSpecEnd.__mro_entries__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		panic("TypeError: ParamSpec args and kwargs cannot be used as a base class")
	})
	ParamSpecArgsType.Dict.Set(val.InternString("__mro_entries__"), refuse)
	ParamSpecKwargsType.Dict.Set(val.InternString("__mro_entries__"), refuse)
	ParamSpecArgsType.InvalidateVersion()
	ParamSpecKwargsType.InvalidateVersion()
}

func installGenericMethods() {
	GenericType.Dict.Set(val.InternString("__class_getitem__"), val.NewClassMethodVal(val.NewBuiltinFuncVal("Generic.__class_getitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: __class_getitem__() missing arguments")
		}
		if v, ok := callTyping(ctx, "_generic_class_getitem", args[:2], nil); ok {
			return v
		}
		item := args[1]
		var parts []val.Value
		if item.IsTuple() {
			parts = item.AsTuple().Items
		} else {
			parts = []val.Value{item}
		}
		return newGenericAlias(args[0], parts)
	})))
	GenericType.Dict.Set(val.InternString("__init_subclass__"), val.NewClassMethodVal(val.NewBuiltinFuncVal("Generic.__init_subclass__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if v, ok := callTyping(ctx, "_generic_init_subclass", args, kwnames); ok {
			return v
		}
		if len(args) > 0 && args[0].IsType() {
			args[0].AsType().Dict.Set(val.InternString("__parameters__"), val.NewTupleVal())
		}
		return val.NoneVal()
	})))
	GenericType.Dict.Set(val.InternString("__parameters__"), val.NewTupleVal())
	GenericType.InvalidateVersion()
}
