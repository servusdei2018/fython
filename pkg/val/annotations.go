package val

// PEP 649 / 749 runtime support: lazily evaluated, cached `__annotations__`
// for classes and modules, driven by the compiler-synthesized
// `__annotate__(format)` function.

var (
	keyAnnotations     = InternString("__annotations__")
	keyAnnotationCache = InternString("__annotations_cache__")
	keyAnnotateFunc    = InternString("__annotate_func__")
	keyAnnotate        = InternString("__annotate__")
)

// callAnnotate invokes an __annotate__ callable with the VALUE format and
// validates that it returned a dict.
func callAnnotate(ctx *ExecContext, fn Value) Value {
	var res Value
	switch {
	case ctx != nil:
		res = ctx.Invoke1(fn, Int64Val(1))
	default:
		panic("RuntimeError: cannot evaluate annotations without an execution context")
	}
	if !res.IsDict() {
		panic("TypeError: __annotate__ returned non-dict of type '" + res.Type().Name + "'")
	}
	return res
}

// annotateFuncOf returns the annotate callable stored in a class or module
// namespace, or ok=false when none (or None) is present.
func annotateFuncOf(d *PyDict, moduleStyle bool) (Value, bool) {
	if d == nil {
		return NoneVal(), false
	}
	keys := []Value{keyAnnotate, keyAnnotateFunc}
	if moduleStyle {
		keys = keys[:1]
	}
	for _, k := range keys {
		if v, ok := d.Get(k); ok && !v.IsNone() && v.ptr != nil {
			return v, true
		}
	}
	return NoneVal(), false
}

// TypeAnnotations implements `type.__annotations__`: the explicit
// `__annotations__` entry, else the cached dict, else the result of calling the
// class's __annotate__(1) (cached), else a fresh empty dict (cached).
func TypeAnnotations(ctx *ExecContext, cls *PyType) Value {
	if cls.Dict == nil {
		cls.Dict = NewDictVal(8).AsDict()
	}
	if (cls.Flags & FlagHeapType) == 0 {
		if ann, ok := cls.Dict.Get(keyAnnotations); ok && ann.IsDict() {
			return ann
		}
		panic("val: AttributeError: type object '" + cls.Name + "' has no attribute '__annotations__'")
	}
	if ann, ok := cls.Dict.Get(keyAnnotations); ok {
		return ann
	}
	if ann, ok := cls.Dict.Get(keyAnnotationCache); ok {
		return ann
	}
	var ann Value
	if fn, ok := annotateFuncOf(cls.Dict, false); ok {
		ann = callAnnotate(ctx, fn)
	} else {
		ann = NewDictVal(4)
	}
	cls.Dict.Set(keyAnnotationCache, ann)
	return ann
}

// TypeSetAnnotations implements `cls.__annotations__ = value`.
func TypeSetAnnotations(cls *PyType, value Value) {
	if (cls.Flags & FlagHeapType) == 0 {
		panic("TypeError: cannot set '__annotations__' attribute of immutable type '" + cls.Name + "'")
	}
	if cls.Dict == nil {
		cls.Dict = NewDictVal(8).AsDict()
	}
	cls.Dict.Delete(keyAnnotations)
	cls.Dict.Set(keyAnnotationCache, value)
	cls.Dict.Delete(keyAnnotateFunc)
	cls.Dict.Delete(keyAnnotate)
}

// TypeDelAnnotations implements `del cls.__annotations__`.
func TypeDelAnnotations(cls *PyType) {
	if (cls.Flags & FlagHeapType) == 0 {
		panic("TypeError: cannot delete '__annotations__' attribute of immutable type '" + cls.Name + "'")
	}
	if cls.Dict == nil {
		return
	}
	cls.Dict.Delete(keyAnnotations)
	cls.Dict.Delete(keyAnnotationCache)
	cls.Dict.Delete(keyAnnotateFunc)
	cls.Dict.Delete(keyAnnotate)
}

// TypeAnnotate implements `type.__annotate__`.
func TypeAnnotate(cls *PyType) Value {
	if (cls.Flags & FlagHeapType) == 0 {
		panic("val: AttributeError: type object '" + cls.Name + "' has no attribute '__annotate__'")
	}
	if fn, ok := annotateFuncOf(cls.Dict, false); ok {
		return fn
	}
	return NoneVal()
}

// TypeSetAnnotate implements `cls.__annotate__ = value`.
func TypeSetAnnotate(cls *PyType, value Value) {
	if (cls.Flags & FlagHeapType) == 0 {
		panic("TypeError: cannot set '__annotate__' attribute of immutable type '" + cls.Name + "'")
	}
	if !value.IsNone() && !isCallableValue(value) {
		panic("TypeError: __annotate__ must be callable or None")
	}
	if cls.Dict == nil {
		cls.Dict = NewDictVal(8).AsDict()
	}
	cls.Dict.Delete(keyAnnotate)
	cls.Dict.Set(keyAnnotateFunc, value)
	if !value.IsNone() {
		cls.Dict.Delete(keyAnnotationCache)
	}
}

// ModuleAnnotations implements `module.__annotations__`: the cached entry in the
// module dict, else the result of the module's __annotate__(1), else `{}`.
func ModuleAnnotations(ctx *ExecContext, mod *PyModule) Value {
	if ann, ok := mod.Dict.Get(keyAnnotations); ok {
		return ann
	}
	var ann Value
	if fn, ok := annotateFuncOf(mod.Dict, true); ok {
		ann = callAnnotate(ctx, fn)
	} else {
		ann = NewDictVal(4)
	}
	mod.Dict.Set(keyAnnotations, ann)
	return ann
}

func isCallableValue(v Value) bool {
	tp := v.Type()
	if tp.Slots.TpCall != nil {
		return true
	}
	_, _, ok := tp.LookupMRO(DunderCall)
	return ok
}
