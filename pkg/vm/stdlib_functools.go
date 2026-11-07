package vm

import (
	"strings"
	"sync"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	PartialType   *val.PyType
	CacheWrapper  *val.PyType
	functoolsOnce sync.Once
)

type pyPartial struct {
	Header   val.ObjectHeader
	Target   val.Value
	Args     []val.Value
	Keywords *val.PyDict
}

type lruEntry struct {
	key   string
	value val.Value
}

type pyLRUCache struct {
	Header       val.ObjectHeader
	UserFunction val.Value
	Maxsize      int // -1 for unbounded
	Hits         int64
	Misses       int64
	CacheInfoCls val.Value
	Cache        map[string]*lruEntry
	Order        []string
	Attrs        *val.PyDict
	Mu           sync.Mutex
}

func initFunctoolsTypes() {
	functoolsOnce.Do(func() {
		// 1. PartialType
		PartialType = val.MustNewType("partial", []*val.PyType{val.ObjectType}, nil)
		PartialType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: partial expected at least 1 argument, got 0")
			}
			fn := args[0]
			kwStart := len(args) - len(kwnames)
			var boundArgs []val.Value
			if kwStart > 1 {
				boundArgs = append([]val.Value(nil), args[1:kwStart]...)
			}
			var kwDict *val.PyDict
			if len(kwnames) > 0 {
				kwDict = val.NewDictVal(len(kwnames)).AsDict()
				for i, kn := range kwnames {
					kwDict.Set(kn, args[kwStart+i])
				}
			}
			if fn.Type() == PartialType {
				inner := (*pyPartial)(unsafe.Pointer(fn.HeaderPtr()))
				fn = inner.Target
				var allArgs []val.Value
				allArgs = append(allArgs, inner.Args...)
				allArgs = append(allArgs, boundArgs...)
				boundArgs = allArgs
				allKw := val.NewDictVal(16).AsDict()
				if inner.Keywords != nil {
					for _, entry := range inner.Keywords.Entries {
						if entry.Active {
							allKw.Set(entry.Key, entry.Value)
						}
					}
				}
				if kwDict != nil {
					for _, entry := range kwDict.Entries {
						if entry.Active {
							allKw.Set(entry.Key, entry.Value)
						}
					}
				}
				kwDict = allKw
			}
			part := &pyPartial{
				Header: val.ObjectHeader{
					Kind: val.KindNative,
					Type: PartialType,
				},
				Target:   fn,
				Args:     boundArgs,
				Keywords: kwDict,
			}
			return val.ObjectVal((*val.ObjectHeader)(unsafe.Pointer(&part.Header)))
		}

		PartialType.Dict.Set(val.DunderCall, val.NewBuiltinFuncVal("__call__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) == 0 {
				panic("TypeError: descriptor '__call__' of 'partial' object needs an argument")
			}
			part := (*pyPartial)(unsafe.Pointer(args[0].HeaderPtr()))
			kwStart := len(args) - len(kwnames)
			posCall := args[1:kwStart]
			kwCallVals := args[kwStart:]

			var mergedArgs []val.Value
			mergedArgs = append(mergedArgs, part.Args...)
			mergedArgs = append(mergedArgs, posCall...)

			mergedKwMap := make(map[string]val.Value)
			var kwOrder []string
			if part.Keywords != nil {
				for _, entry := range part.Keywords.Entries {
					if entry.Active {
						k := entry.Key.AsString()
						if _, exists := mergedKwMap[k]; !exists {
							kwOrder = append(kwOrder, k)
						}
						mergedKwMap[k] = entry.Value
					}
				}
			}
			for i, kn := range kwnames {
				k := kn.AsString()
				if _, exists := mergedKwMap[k]; !exists {
					kwOrder = append(kwOrder, k)
				}
				mergedKwMap[k] = kwCallVals[i]
			}

			var mergedKwNames []val.Value
			for _, k := range kwOrder {
				mergedArgs = append(mergedArgs, mergedKwMap[k])
				mergedKwNames = append(mergedKwNames, val.InternString(k))
			}

			return ctx.Call(part.Target, mergedArgs, mergedKwNames)
		}))

		// 2. CacheWrapper. Instances are KindNative so attribute writes do not
		// go through PyInstance's shape lock (this struct is not a PyInstance).
		CacheWrapper = val.MustNewType("_lru_cache_wrapper", []*val.PyType{val.ObjectType}, nil)
		cacheCall := func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: _lru_cache_wrapper expected an instance")
			}
			cache := (*pyLRUCache)(unsafe.Pointer(args[0].HeaderPtr()))
			args = args[1:]
			cache.Mu.Lock()

			// Build cache key
			var keyParts []string
			for _, a := range args {
				keyParts = append(keyParts, a.String())
			}
			for _, kn := range kwnames {
				keyParts = append(keyParts, kn.AsString())
			}
			cacheKey := strings.Join(keyParts, ":")

			if entry, ok := cache.Cache[cacheKey]; ok {
				cache.Hits++
				// Move to end of order (most recently used)
				for i, k := range cache.Order {
					if k == cacheKey {
						copy(cache.Order[i:], cache.Order[i+1:])
						cache.Order[len(cache.Order)-1] = cacheKey
						break
					}
				}
				cache.Mu.Unlock()
				return entry.value
			}

			cache.Misses++
			cache.Mu.Unlock()

			// Compute result
			res := ctx.Call(cache.UserFunction, args, kwnames)

			cache.Mu.Lock()
			defer cache.Mu.Unlock()

			if cache.Maxsize > 0 && len(cache.Cache) >= cache.Maxsize {
				// Evict oldest
				oldestKey := cache.Order[0]
				cache.Order = cache.Order[1:]
				delete(cache.Cache, oldestKey)
			}
			cache.Cache[cacheKey] = &lruEntry{key: cacheKey, value: res}
			cache.Order = append(cache.Order, cacheKey)

			return res
		}
		CacheWrapper.Dict.Set(val.DunderCall, val.NewBuiltinFuncVal("__call__", cacheCall))
		cacheGet := func(ctx *val.ExecContext, descr, obj val.Value, owner *val.PyType) val.Value {
			if obj.IsNone() || obj.IsUnbound() {
				return descr
			}
			return val.NewBoundMethodVal(obj, descr)
		}
		CacheWrapper.Slots.TpDescrGet = cacheGet
		CacheWrapper.Dict.Set(val.DunderGet, val.NewBuiltinFuncVal("__get__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: descriptor '__get__' of '_lru_cache_wrapper' object needs an argument")
			}
			obj := val.NoneVal()
			if len(args) > 1 {
				obj = args[1]
			}
			return cacheGet(ctx, args[0], obj, nil)
		}))
		CacheWrapper.InvalidateVersion()
		CacheWrapper.Slots.TpGetAttr = func(ctx *val.ExecContext, obj, name val.Value) val.Value {
			cache := (*pyLRUCache)(unsafe.Pointer(obj.HeaderPtr()))
			if name.AsString() == "__dict__" {
				if cache.Attrs == nil {
					cache.Attrs = val.NewDictVal(4).AsDict()
				}
				return val.ObjectVal(&cache.Attrs.Header)
			}
			if name.AsString() == "__wrapped__" {
				return cache.UserFunction
			}
			if cache.Attrs != nil {
				if v, ok := cache.Attrs.Get(val.InternValue(name)); ok {
					return v
				}
			}
			return val.ObjectGetAttr(ctx, obj, name)
		}
		CacheWrapper.Slots.TpSetAttr = func(ctx *val.ExecContext, obj, name, value val.Value) val.Value {
			cache := (*pyLRUCache)(unsafe.Pointer(obj.HeaderPtr()))
			if cache.Attrs == nil {
				cache.Attrs = val.NewDictVal(4).AsDict()
			}
			cache.Attrs.Set(val.InternValue(name), value)
			return val.NoneVal()
		}

		CacheWrapper.Dict.Set(val.InternString("cache_info"), val.NewBuiltinFuncVal("cache_info", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) == 0 {
				panic("TypeError: cache_info() requires 1 argument")
			}
			cache := (*pyLRUCache)(unsafe.Pointer(args[0].HeaderPtr()))
			cache.Mu.Lock()
			hits := cache.Hits
			misses := cache.Misses
			maxsize := cache.Maxsize
			currsize := len(cache.Cache)
			cache.Mu.Unlock()

			maxVal := val.NoneVal()
			if maxsize >= 0 {
				maxVal = val.Int64Val(int64(maxsize))
			}
			if !cache.CacheInfoCls.IsNone() {
				return ctx.Call(cache.CacheInfoCls, []val.Value{val.Int64Val(hits), val.Int64Val(misses), maxVal, val.Int64Val(int64(currsize))}, nil)
			}
			return val.NewTupleVal(val.Int64Val(hits), val.Int64Val(misses), maxVal, val.Int64Val(int64(currsize)))
		}))

		CacheWrapper.Dict.Set(val.InternString("cache_clear"), val.NewBuiltinFuncVal("cache_clear", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) == 0 {
				panic("TypeError: cache_clear() requires 1 argument")
			}
			cache := (*pyLRUCache)(unsafe.Pointer(args[0].HeaderPtr()))
			cache.Mu.Lock()
			cache.Hits = 0
			cache.Misses = 0
			cache.Cache = make(map[string]*lruEntry)
			cache.Order = nil
			cache.Mu.Unlock()
			return val.NoneVal()
		}))
		CacheWrapper.InvalidateVersion()
	})
}

func (e *Engine) createFunctoolsModule() val.Value {
	initFunctoolsTypes()

	modVal := val.NewModuleVal("_functools", "")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("partial"), val.TypeVal(PartialType))

	// reduce(function, iterable[, initializer])
	mod.Dict.Set(val.InternString("reduce"), val.NewBuiltinFuncVal("reduce", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: reduce() takes at least 2 arguments")
		}
		fn := args[0]
		it := iterFor(ctx, args[1])
		hasInit := len(args) > 2
		var acc val.Value

		if hasInit {
			acc = args[2]
		} else {
			first, ok := it.Next()
			if !ok {
				panic("TypeError: reduce() of empty iterable with no initial value")
			}
			acc = first
		}

		for {
			item, ok := it.Next()
			if !ok {
				break
			}
			acc = ctx.Invoke2(fn, acc, item)
		}

		return acc
	}))

	// _lru_cache_wrapper(user_function, maxsize, typed, _CacheInfo)
	mod.Dict.Set(val.InternString("_lru_cache_wrapper"), val.NewBuiltinFuncVal("_lru_cache_wrapper", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 4 {
			panic("TypeError: _lru_cache_wrapper expected 4 arguments")
		}
		userFn := args[0]
		maxsize := -1
		if args[1].IsInt64() {
			maxsize = int(args[1].AsInt64())
		}
		cacheInfoCls := args[3]

		cache := &pyLRUCache{
			Header: val.ObjectHeader{
				Kind: val.KindNative,
				Type: CacheWrapper,
			},
			UserFunction: userFn,
			Maxsize:      maxsize,
			CacheInfoCls: cacheInfoCls,
			Cache:        make(map[string]*lruEntry),
		}
		return val.ObjectVal((*val.ObjectHeader)(unsafe.Pointer(&cache.Header)))
	}))

	// cmp_to_key(mycmp)
	mod.Dict.Set(val.InternString("cmp_to_key"), val.NewBuiltinFuncVal("cmp_to_key", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: cmp_to_key() takes exactly 1 argument")
		}
		mycmp := args[0]
		KeyCls := val.MustNewType("K", []*val.PyType{val.ObjectType}, nil)
		KeyCls.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, cArgs []val.Value, kwnames []val.Value) val.Value {
			inst := val.NewInstanceOfType(KeyCls)
			if len(cArgs) > 0 {
				inst.AsInstance().SetAttr(val.InternString("obj"), cArgs[0])
			}
			return inst
		}
		for _, cmpDef := range []struct {
			name string
			test func(int64) bool
		}{
			{"__lt__", func(r int64) bool { return r < 0 }},
			{"__le__", func(r int64) bool { return r <= 0 }},
			{"__gt__", func(r int64) bool { return r > 0 }},
			{"__ge__", func(r int64) bool { return r >= 0 }},
			{"__eq__", func(r int64) bool { return r == 0 }},
			{"__ne__", func(r int64) bool { return r != 0 }},
		} {
			test := cmpDef.test
			KeyCls.Dict.Set(val.InternString(cmpDef.name), val.NewBuiltinFuncVal(cmpDef.name, func(ctx *val.ExecContext, callable val.Value, cArgs []val.Value, kwnames []val.Value) val.Value {
				if len(cArgs) < 2 || !cArgs[0].IsInstance() || !cArgs[1].IsInstance() {
					return val.NotImplementedVal()
				}
				selfObj, _ := cArgs[0].AsInstance().GetAttr(val.InternString("obj"))
				otherObj, ok := cArgs[1].AsInstance().GetAttr(val.InternString("obj"))
				if !ok {
					return val.NotImplementedVal()
				}
				res := ctx.Invoke2(mycmp, selfObj, otherObj)
				return val.BoolVal(test(res.AsInt64()))
			}))
		}
		KeyCls.InvalidateVersion()
		return val.TypeVal(KeyCls)
	}))

	return modVal
}
