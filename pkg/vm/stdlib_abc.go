package vm

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	abcCacheToken uint64 = 1
	abcMu         sync.RWMutex
	abcData       = make(map[*val.PyType]*abcMeta)
)

type abcMeta struct {
	registry             *val.PySet
	cache                *val.PySet
	negativeCache        *val.PySet
	negativeCacheVersion uint64
}

func getABCMeta(cls *val.PyType) *abcMeta {
	abcMu.RLock()
	meta := abcData[cls]
	abcMu.RUnlock()
	if meta != nil {
		return meta
	}
	abcMu.Lock()
	defer abcMu.Unlock()
	if meta = abcData[cls]; meta != nil {
		return meta
	}
	meta = &abcMeta{
		registry:             val.NewSetVal().AsSet(),
		cache:                val.NewSetVal().AsSet(),
		negativeCache:        val.NewSetVal().AsSet(),
		negativeCacheVersion: atomic.LoadUint64(&abcCacheToken),
	}
	abcData[cls] = meta
	return meta
}

func abcSubclassCheck(ctx *val.ExecContext, cls, sub *val.PyType) bool {
	if sub == cls || val.IsSubclass(sub, cls) {
		return true
	}
	meta := getABCMeta(cls)
	subVal := val.TypeVal(sub)
	if meta.cache.Contains(subVal) {
		return true
	}

	// 1. Check __subclasshook__ on cls
	if hook, _, ok := cls.LookupMRO(val.InternString("__subclasshook__")); ok {
		if ctx != nil {
			fn := hook
			if fn.Kind() == val.KindClassMethod {
				fn = fn.AsClassMethod().Callable
			}
			res := ctx.Invoke2(fn, val.TypeVal(cls), subVal)
			if !res.IsNotImplemented() {
				if res.IsTruthy() {
					meta.cache.Add(subVal)
					return true
				}
				return false
			}
		}
	} else if cls.Name == "Sequence" && sub.Name == "list" {
		fmt.Printf("DEBUG Sequence has NO __subclasshook__\n")
	}

	// 2. Check direct registry against sub MRO
	for _, base := range sub.MRO {
		bVal := val.TypeVal(base)
		if meta.registry.Contains(bVal) {
			meta.cache.Add(subVal)
			return true
		}
	}

	// 3. Check registered classes in registry (recursive)
	for _, regVal := range meta.registry.Items() {
		if regVal.IsType() && (val.IsSubclass(sub, regVal.AsType()) || (ctx != nil && abcSubclassCheck(ctx, regVal.AsType(), sub))) {
			meta.cache.Add(subVal)
			return true
		}
	}

	// 4. Check cls.Subclasses recursively
	for _, scls := range cls.Subclasses {
		if scls != cls && abcSubclassCheck(ctx, scls, sub) {
			meta.cache.Add(subVal)
			return true
		}
	}

	return false
}

func (e *Engine) createABCModule() val.Value {
	modVal := val.NewModuleVal("_abc", "")
	mod := modVal.AsModule()

	// get_cache_token() -> int
	mod.Dict.Set(val.InternString("get_cache_token"), val.NewBuiltinFuncVal("get_cache_token", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		token := atomic.LoadUint64(&abcCacheToken)
		return val.Int64Val(int64(token))
	}))

	// _abc_init(cls)
	mod.Dict.Set(val.InternString("_abc_init"), val.NewBuiltinFuncVal("_abc_init", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 || !args[0].IsType() {
			panic("TypeError: _abc_init() expected a class")
		}
		cls := args[0].AsType()
		meta := getABCMeta(cls)
		meta.negativeCacheVersion = atomic.LoadUint64(&abcCacheToken)
		return val.NoneVal()
	}))

	// _abc_register(cls, subclass) -> subclass
	mod.Dict.Set(val.InternString("_abc_register"), val.NewBuiltinFuncVal("_abc_register", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 || !args[0].IsType() || !args[1].IsType() {
			panic(fmt.Sprintf("TypeError: _abc_register() expected 2 classes, got len=%d args[0]=%v (isType=%v) args[1]=%v (isType=%v)", len(args), args[0], args[0].IsType(), args[1], args[1].IsType()))
		}
		cls := args[0].AsType()
		sub := args[1].AsType()

		if val.IsSubclass(sub, cls) {
			return args[1]
		}
		if val.IsSubclass(cls, sub) {
			panic("RuntimeError: Refusing to create an ABC cycle")
		}

		meta := getABCMeta(cls)
		meta.registry.Add(args[1])
		meta.cache = val.NewSetVal().AsSet()
		meta.negativeCache = val.NewSetVal().AsSet()
		atomic.AddUint64(&abcCacheToken, 1)
		meta.negativeCacheVersion = atomic.LoadUint64(&abcCacheToken)

		return args[1]
	}))

	// _abc_instancecheck(cls, instance) -> bool
	mod.Dict.Set(val.InternString("_abc_instancecheck"), val.NewBuiltinFuncVal("_abc_instancecheck", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 || !args[0].IsType() {
			panic("TypeError: _abc_instancecheck() arg 1 must be a class")
		}
		cls := args[0].AsType()
		inst := args[1]
		instType := inst.Type()
		if inst.IsInstance() {
			if clsVal, ok := inst.AsInstance().GetAttr(val.InternString("__class__")); ok && clsVal.IsType() {
				instType = clsVal.AsType()
			}
		}
		return val.BoolVal(abcSubclassCheck(ctx, cls, instType))
	}))

	// _abc_subclasscheck(cls, subclass) -> bool
	mod.Dict.Set(val.InternString("_abc_subclasscheck"), val.NewBuiltinFuncVal("_abc_subclasscheck", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 || !args[0].IsType() || !args[1].IsType() {
			panic("TypeError: _abc_subclasscheck() expected 2 classes")
		}
		cls := args[0].AsType()
		sub := args[1].AsType()
		return val.BoolVal(abcSubclassCheck(ctx, cls, sub))
	}))

	// _get_dump(cls) -> (registry, cache, negative_cache, negative_cache_version)
	mod.Dict.Set(val.InternString("_get_dump"), val.NewBuiltinFuncVal("_get_dump", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 || !args[0].IsType() {
			panic("TypeError: _get_dump() expected a class")
		}
		cls := args[0].AsType()
		meta := getABCMeta(cls)
		return val.NewTupleVal(
			val.SetVal(meta.registry),
			val.SetVal(meta.cache),
			val.SetVal(meta.negativeCache),
			val.Int64Val(int64(meta.negativeCacheVersion)),
		)
	}))

	// _reset_registry(cls)
	mod.Dict.Set(val.InternString("_reset_registry"), val.NewBuiltinFuncVal("_reset_registry", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 || !args[0].IsType() {
			panic("TypeError: _reset_registry() expected a class")
		}
		cls := args[0].AsType()
		meta := getABCMeta(cls)
		meta.registry = val.NewSetVal().AsSet()
		return val.NoneVal()
	}))

	// _reset_caches(cls)
	mod.Dict.Set(val.InternString("_reset_caches"), val.NewBuiltinFuncVal("_reset_caches", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 || !args[0].IsType() {
			panic("TypeError: _reset_caches() expected a class")
		}
		cls := args[0].AsType()
		meta := getABCMeta(cls)
		meta.cache = val.NewSetVal().AsSet()
		meta.negativeCache = val.NewSetVal().AsSet()
		return val.NoneVal()
	}))

	return modVal
}
