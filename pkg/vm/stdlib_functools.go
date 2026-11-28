package vm

import (
	"slices"
	"strings"
	"sync"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	PartialType   *val.PyType
	CacheWrapper  *val.PyType
	functoolsOnce sync.Once
	lruKwdMarkHdr = val.ObjectHeader{Kind: val.KindNative}
	lruKwdMark    = val.ObjectVal(&lruKwdMarkHdr)
)

type pyPartial struct {
	Header   val.ObjectHeader
	Target   val.Value
	Args     []val.Value
	Keywords *val.PyDict
}

type lruNode struct {
	key  val.Value
	val  val.Value
	prev int32
	next int32
}

type lruKey2 struct {
	a val.Value
	b val.Value
}

type lruNode2 struct {
	key  lruKey2
	val  val.Value
	prev int32
	next int32
}

func isLruKey2Value(v val.Value) bool {
	if v.IsInt64() || v.IsBool() || v.IsNone() || v.IsInlineStr() || v.IsInterned() {
		return true
	}
	if v.IsType() || v.IsFunction() {
		return true
	}
	return false
}

type pyLRUCache struct {
	Header       val.ObjectHeader
	UserFunction val.Value
	Maxsize      int // -1 for unbounded
	Typed        bool
	Hits         int64
	Misses       int64
	CacheInfoCls val.Value
	Cache        *val.PyDict
	nodes        []lruNode // nodes[0] is sentinel root: next=MRU, prev=LRU
	free         []int32
	clearGen     uint64
	Attrs        *val.PyDict
	Mu           sync.Mutex
	cache2       map[lruKey2]int32
	nodes2       []lruNode2
	free2        []int32
}

func isFastHashableValue(v val.Value) bool {
	return v.IsInt64() || v.IsAnyString() || v.IsBool() || v.IsFloat64() || v.IsNone()
}

func makeLRUCacheKey(ctx *val.ExecContext, args []val.Value, kwnames []val.Value, typed bool) (val.Value, bool) {
	if len(kwnames) == 0 && !typed {
		if len(args) == 1 {
			k := args[0]
			if !isFastHashableValue(k) {
				if _, exc, ok := val.TryHashValue(ctx, k); !ok {
					ctx.Raise(exc)
					return val.Value{}, false
				}
			}
			return k, true
		}
		if len(args) == 0 {
			return val.EmptyTupleVal(), true
		}
	}
	posCount := len(args) - len(kwnames)
	extra := 0
	if len(kwnames) > 0 {
		extra = 1 + 2*len(kwnames)
	}
	var stackBuf [8]val.Value
	totalCap := posCount + extra
	if typed {
		totalCap += len(args)
	}
	var keyParts []val.Value
	if totalCap <= len(stackBuf) {
		keyParts = stackBuf[:0]
	} else {
		keyParts = make([]val.Value, 0, totalCap)
	}
	keyParts = append(keyParts, args[:posCount]...)
	if len(kwnames) > 0 {
		keyParts = append(keyParts, lruKwdMark)
		if len(kwnames) == 1 {
			keyParts = append(keyParts, kwnames[0], args[posCount])
		} else {
			type kwPair struct {
				k val.Value
				v val.Value
			}
			pairs := make([]kwPair, len(kwnames))
			for i, kn := range kwnames {
				pairs[i] = kwPair{k: kn, v: args[posCount+i]}
			}
			slices.SortFunc(pairs, func(a, b kwPair) int {
				return strings.Compare(a.k.AsString(), b.k.AsString())
			})
			for _, p := range pairs {
				keyParts = append(keyParts, p.k, p.v)
			}
		}
	}
	if typed {
		for _, a := range args[:posCount] {
			keyParts = append(keyParts, val.TypeVal(a.Type()))
		}
		if len(kwnames) > 0 {
			for _, a := range args[posCount:] {
				keyParts = append(keyParts, val.TypeVal(a.Type()))
			}
		}
	}
	key := val.NewTupleVal(keyParts...)
	if _, exc, ok := val.TryHashValue(ctx, key); !ok {
		ctx.Raise(exc)
		return val.Value{}, false
	}
	return key, true
}

func (c *pyLRUCache) unlinkNode(idx int32) {
	p := c.nodes[idx].prev
	n := c.nodes[idx].next
	c.nodes[p].next = n
	c.nodes[n].prev = p
}

func (c *pyLRUCache) linkMRU(idx int32) {
	first := c.nodes[0].next
	c.nodes[idx].prev = 0
	c.nodes[idx].next = first
	c.nodes[first].prev = idx
	c.nodes[0].next = idx
}

func (c *pyLRUCache) unlinkNode2(idx int32) {
	p := c.nodes2[idx].prev
	n := c.nodes2[idx].next
	c.nodes2[p].next = n
	c.nodes2[n].prev = p
}

func (c *pyLRUCache) linkMRU2(idx int32) {
	first := c.nodes2[0].next
	c.nodes2[idx].prev = 0
	c.nodes2[idx].next = first
	c.nodes2[first].prev = idx
	c.nodes2[0].next = idx
}

func (c *pyLRUCache) probeLocked2(k lruKey2) (val.Value, bool) {
	if c.Maxsize == 0 {
		return val.Value{}, false
	}
	if c.cache2 == nil {
		return val.Value{}, false
	}
	if idx, ok := c.cache2[k]; ok {
		c.Hits++
		if c.nodes2[0].next != idx {
			c.unlinkNode2(idx)
			c.linkMRU2(idx)
		}
		return c.nodes2[idx].val, true
	}
	return val.Value{}, false
}

func (c *pyLRUCache) insertAfterMiss2(k lruKey2, res val.Value, startGen uint64) {
	if c.clearGen != startGen || c.Maxsize == 0 {
		return
	}
	if c.cache2 == nil {
		c.cache2 = make(map[lruKey2]int32)
		c.nodes2 = append(c.nodes2[:0], lruNode2{prev: 0, next: 0})
		c.free2 = c.free2[:0]
	}
	if _, exists := c.cache2[k]; exists {
		return
	}
	var idx int32
	if c.Maxsize > 0 && len(c.cache2) >= c.Maxsize {
		lruIdx := c.nodes2[0].prev
		if lruIdx != 0 {
			delete(c.cache2, c.nodes2[lruIdx].key)
			c.unlinkNode2(lruIdx)
			idx = lruIdx
			c.nodes2[idx].key = k
			c.nodes2[idx].val = res
			c.linkMRU2(idx)
			c.cache2[k] = idx
			return
		}
	}
	if nf := len(c.free2); nf > 0 {
		idx = c.free2[nf-1]
		c.free2 = c.free2[:nf-1]
	} else {
		idx = int32(len(c.nodes2))
		c.nodes2 = append(c.nodes2, lruNode2{})
	}
	c.nodes2[idx].key = k
	c.nodes2[idx].val = res
	c.linkMRU2(idx)
	c.cache2[k] = idx
}

func (c *pyLRUCache) probeLocked(cacheKey val.Value) (val.Value, bool) {
	if c.Maxsize == 0 || c.Cache == nil {
		return val.Value{}, false
	}
	if idxVal, ok := c.Cache.Get(cacheKey); ok {
		c.Hits++
		idx := int32(idxVal.AsInt64())
		if c.nodes[0].next != idx {
			c.unlinkNode(idx)
			c.linkMRU(idx)
		}
		return c.nodes[idx].val, true
	}
	return val.Value{}, false
}

func (c *pyLRUCache) insertAfterMiss(cacheKey, res val.Value, startGen uint64) {
	if c.clearGen != startGen || c.Maxsize == 0 || c.Cache == nil {
		return
	}
	// If a recursive call during the miss already cached this key, keep ring consistent.
	if _, exists := c.Cache.Get(cacheKey); exists {
		return
	}
	var idx int32
	if c.Maxsize > 0 && c.Cache.Len() >= c.Maxsize {
		lruIdx := c.nodes[0].prev
		if lruIdx != 0 {
			c.Cache.Delete(c.nodes[lruIdx].key)
			c.unlinkNode(lruIdx)
			idx = lruIdx
			c.nodes[idx].key = cacheKey
			c.nodes[idx].val = res
			c.linkMRU(idx)
			c.Cache.Set(cacheKey, val.Int64Val(int64(idx)))
			return
		}
	}
	if nf := len(c.free); nf > 0 {
		idx = c.free[nf-1]
		c.free = c.free[:nf-1]
	} else {
		idx = int32(len(c.nodes))
		c.nodes = append(c.nodes, lruNode{})
	}
	c.nodes[idx].key = cacheKey
	c.nodes[idx].val = res
	c.linkMRU(idx)
	c.Cache.Set(cacheKey, val.Int64Val(int64(idx)))
}

func initFunctoolsTypes() {
	functoolsOnce.Do(func() {
		// 1. PartialType
		PartialType = val.MustNewType("partial", []*val.PyType{val.ObjectType}, nil)
		PartialType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewException(val.TypeErrorType, "partial expected at least 1 argument, got 0"))
				return val.NoneVal()
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
					inner.Keywords.EnsureCombined()
					for _, entry := range inner.Keywords.Entries {
						if entry.Active {
							allKw.Set(entry.Key, entry.Value)
						}
					}
				}
				if kwDict != nil {
					kwDict.EnsureCombined()
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
				ctx.Raise(val.NewException(val.TypeErrorType, "descriptor '__call__' of 'partial' object needs an argument"))
				return val.NoneVal()
			}
			part := (*pyPartial)(unsafe.Pointer(args[0].HeaderPtr()))
			if part.Keywords == nil && len(kwnames) == 0 {
				if len(part.Args) == 0 {
					return ctx.Call(part.Target, args[1:], nil)
				}
				var stackBuf [16]val.Value
				total := len(part.Args) + len(args) - 1
				var mergedArgs []val.Value
				if total <= len(stackBuf) {
					mergedArgs = stackBuf[:total]
				} else {
					mergedArgs = make([]val.Value, total)
				}
				copy(mergedArgs, part.Args)
				copy(mergedArgs[len(part.Args):], args[1:])
				return ctx.Call(part.Target, mergedArgs, nil)
			}
			kwStart := len(args) - len(kwnames)
			posCall := args[1:kwStart]
			kwCallVals := args[kwStart:]

			var mergedArgs []val.Value
			mergedArgs = append(mergedArgs, part.Args...)
			mergedArgs = append(mergedArgs, posCall...)

			mergedKwMap := make(map[string]val.Value)
			var kwOrder []string
			if part.Keywords != nil {
				part.Keywords.EnsureCombined()
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
		cacheCallTp := func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			cache := (*pyLRUCache)(unsafe.Pointer(callable.HeaderPtr()))
			if len(kwnames) == 0 && !cache.Typed && len(args) == 2 && isLruKey2Value(args[0]) && isLruKey2Value(args[1]) {
				k := lruKey2{a: args[0], b: args[1]}
				locked := val.MultiThreadedFlag.Load()
				if locked {
					cache.Mu.Lock()
				}
				if res, hit := cache.probeLocked2(k); hit {
					if locked {
						cache.Mu.Unlock()
					}
					return res
				}
				cache.Misses++
				startGen := cache.clearGen
				if locked {
					cache.Mu.Unlock()
				}
				res := ctx.Call(cache.UserFunction, args, kwnames)
				if ctx != nil && ctx.HasException() {
					return val.NoneVal()
				}
				locked = val.MultiThreadedFlag.Load()
				if locked {
					cache.Mu.Lock()
				}
				cache.insertAfterMiss2(k, res, startGen)
				if locked {
					cache.Mu.Unlock()
				}
				return res
			}
			cacheKey, ok := makeLRUCacheKey(ctx, args, kwnames, cache.Typed)
			if !ok {
				return val.NoneVal()
			}
			locked := val.MultiThreadedFlag.Load()
			if locked {
				cache.Mu.Lock()
			}
			if res, hit := cache.probeLocked(cacheKey); hit {
				if locked {
					cache.Mu.Unlock()
				}
				return res
			}
			cache.Misses++
			startGen := cache.clearGen
			if locked {
				cache.Mu.Unlock()
			}

			res := ctx.Call(cache.UserFunction, args, kwnames)
			if ctx != nil && ctx.HasException() {
				return val.NoneVal()
			}

			locked = val.MultiThreadedFlag.Load()
			if locked {
				cache.Mu.Lock()
			}
			cache.insertAfterMiss(cacheKey, res, startGen)
			if locked {
				cache.Mu.Unlock()
			}
			return res
		}
		CacheWrapper.Slots.TpCall = cacheCallTp
		CacheWrapper.Dict.Set(val.DunderCall, val.NewBuiltinFuncVal("__call__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewException(val.TypeErrorType, "_lru_cache_wrapper expected an instance"))
				return val.NoneVal()
			}
			return cacheCallTp(ctx, args[0], args[1:], kwnames)
		}))
		cacheGet := func(ctx *val.ExecContext, descr, obj val.Value, owner *val.PyType) val.Value {
			if obj.IsNone() || obj.IsUnbound() {
				return descr
			}
			return val.NewBoundMethodVal(obj, descr)
		}
		CacheWrapper.Slots.TpDescrGet = cacheGet
		CacheWrapper.Dict.Set(val.DunderGet, val.NewBuiltinFuncVal("__get__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewException(val.TypeErrorType, "descriptor '__get__' of '_lru_cache_wrapper' object needs an argument"))
				return val.NoneVal()
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
				ctx.Raise(val.NewException(val.TypeErrorType, "cache_info() requires 1 argument"))
				return val.NoneVal()
			}
			cache := (*pyLRUCache)(unsafe.Pointer(args[0].HeaderPtr()))
			locked := val.MultiThreadedFlag.Load()
			if locked {
				cache.Mu.Lock()
			}
			hits := cache.Hits
			misses := cache.Misses
			maxsize := cache.Maxsize
			currsize := 0
			if cache.Cache != nil {
				currsize = cache.Cache.Len()
			}
			currsize += len(cache.cache2)
			if locked {
				cache.Mu.Unlock()
			}

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
				ctx.Raise(val.NewException(val.TypeErrorType, "cache_clear() requires 1 argument"))
				return val.NoneVal()
			}
			cache := (*pyLRUCache)(unsafe.Pointer(args[0].HeaderPtr()))
			locked := val.MultiThreadedFlag.Load()
			if locked {
				cache.Mu.Lock()
			}
			cache.Hits = 0
			cache.Misses = 0
			cache.clearGen++
			clear(cache.nodes)
			cache.nodes = append(cache.nodes[:0], lruNode{prev: 0, next: 0})
			cache.free = cache.free[:0]
			cache.Cache = val.NewDictVal(16).AsDict()
			clear(cache.nodes2)
			cache.nodes2 = append(cache.nodes2[:0], lruNode2{prev: 0, next: 0})
			cache.free2 = cache.free2[:0]
			cache.cache2 = nil
			if locked {
				cache.Mu.Unlock()
			}
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
			ctx.Raise(val.NewException(val.TypeErrorType, "reduce() takes at least 2 arguments"))
			return val.NoneVal()
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
				ctx.Raise(val.NewException(val.TypeErrorType, "reduce() of empty iterable with no initial value"))
				return val.NoneVal()
			}
			acc = first
		}

		if vmInst, ok := ctx.VM.(*VM); ok && vmInst != nil {
			if b, ok := vmInst.beginBatchCall(ctx, fn, 2); ok {
				res, _ := foldBatched(vmInst, ctx, b, it, acc)
				vmInst.finishBatch(ctx, b)
				return res
			}
			if ctx.HasException() {
				return val.NoneVal()
			}
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
			ctx.Raise(val.NewException(val.TypeErrorType, "_lru_cache_wrapper expected 4 arguments"))
			return val.NoneVal()
		}
		userFn := args[0]
		maxsize := -1
		if args[1].IsInt64() {
			maxsize = max(int(args[1].AsInt64()), 0)
		}
		typed := args[2].IsTruthy()
		cacheInfoCls := args[3]

		nodesCap := 16
		if maxsize >= 0 && maxsize < 16 {
			nodesCap = maxsize + 1
		}
		nodes := make([]lruNode, 1, nodesCap)
		nodes[0] = lruNode{prev: 0, next: 0}
		cache := &pyLRUCache{
			Header: val.ObjectHeader{
				Kind: val.KindNative,
				Type: CacheWrapper,
			},
			UserFunction: userFn,
			Maxsize:      maxsize,
			Typed:        typed,
			CacheInfoCls: cacheInfoCls,
			Cache:        val.NewDictVal(nodesCap).AsDict(),
			nodes:        nodes,
		}
		return val.ObjectVal((*val.ObjectHeader)(unsafe.Pointer(&cache.Header)))
	}))

	// cmp_to_key(mycmp)
	mod.Dict.Set(val.InternString("cmp_to_key"), val.NewBuiltinFuncVal("cmp_to_key", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			ctx.Raise(val.NewException(val.TypeErrorType, "cmp_to_key() takes exactly 1 argument"))
			return val.NoneVal()
		}
		mycmp := args[0]
		KeyCls := val.MustNewType("K", []*val.PyType{val.ObjectType}, nil)
		KeyCls.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, cArgs []val.Value, kwnames []val.Value) val.Value {
			inst := val.NewInstanceOfType(KeyCls)
			if len(cArgs) > 0 {
				inst.AsInstance().SetAttr(nameObj, cArgs[0])
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
				selfObj, _ := cArgs[0].AsInstance().GetAttr(nameObj)
				otherObj, ok := cArgs[1].AsInstance().GetAttr(nameObj)
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
