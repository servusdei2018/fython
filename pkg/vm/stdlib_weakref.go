package vm

import (
	"runtime"
	"sync"
	"unsafe"
	"weak"

	"github.com/servusdei2018/fython/pkg/val"
)

type weakCleanupData struct {
	engine   *Engine
	addr     uintptr
	ref      *val.PyWeakRef
	refVal   val.Value
	callback val.Value
}

type dictCleanupArg struct {
	dict *val.PyDict
	key  val.Value
}

type finalizeCleanupArg struct {
	engine   *Engine
	selfVal  val.Value
	aliveKey val.Value
	fnVal    val.Value
}

func onWeakRefCleanup(d *weakCleanupData) {
	d.ref.IsDead.Store(true)
	unregisterWeakRef(d.addr, d.ref)
	if !d.callback.IsNone() {
		go func() {
			vm := d.engine.AcquireVM()
			defer d.engine.ReleaseVM(vm)
			vm.Vectorcall(d.callback, []val.Value{d.refVal}, nil)
		}()
	}
}

func onWeakProxyCleanup(d *weakCleanupData) {
	if !d.callback.IsNone() {
		go func() {
			vm := d.engine.AcquireVM()
			defer d.engine.ReleaseVM(vm)
			vm.Vectorcall(d.callback, []val.Value{d.refVal}, nil)
		}()
	}
}

func onDictCleanup(arg dictCleanupArg) {
	arg.dict.Delete(arg.key)
}

func onFinalizeCleanup(arg finalizeCleanupArg) {
	vm := arg.engine.AcquireVM()
	defer arg.engine.ReleaseVM(vm)
	isAlive := val.GetAttrCtx(&vm.execCtx, arg.selfVal, arg.aliveKey)
	if isAlive.IsBool() && isAlive.AsBool() {
		val.SetAttrCtx(&vm.execCtx, arg.selfVal, arg.aliveKey, val.BoolVal(false))
		vm.Vectorcall(arg.fnVal, nil, nil)
	}
}

var (
	weakrefRegistryMu sync.RWMutex
	weakrefRegistry   = make(map[uintptr][]*val.PyWeakRef)

	weakrefTypesOnce        sync.Once
	WeakKeyDictionaryType   *val.PyType
	WeakValueDictionaryType *val.PyType
	WeakSetType             *val.PyType
	FinalizeType            *val.PyType
)

func registerWeakRef(addr uintptr, ref *val.PyWeakRef) {
	if addr == 0 {
		return
	}
	weakrefRegistryMu.Lock()
	weakrefRegistry[addr] = append(weakrefRegistry[addr], ref)
	weakrefRegistryMu.Unlock()
}

func unregisterWeakRef(addr uintptr, ref *val.PyWeakRef) {
	if addr == 0 {
		return
	}
	weakrefRegistryMu.Lock()
	defer weakrefRegistryMu.Unlock()
	refs := weakrefRegistry[addr]
	n := 0
	for _, r := range refs {
		if r != ref && !r.IsDead.Load() && r.WP.Value() != nil {
			refs[n] = r
			n++
		}
	}
	if n == 0 {
		delete(weakrefRegistry, addr)
	} else {
		weakrefRegistry[addr] = refs[:n]
	}
}

func getActiveWeakRefs(hdr *val.ObjectHeader) []*val.PyWeakRef {
	if hdr == nil {
		return nil
	}
	addr := uintptr(unsafe.Pointer(hdr))
	weakrefRegistryMu.RLock()
	defer weakrefRegistryMu.RUnlock()
	refs := weakrefRegistry[addr]
	var active []*val.PyWeakRef
	for _, r := range refs {
		if !r.IsDead.Load() && r.WP.Value() != nil {
			active = append(active, r)
		}
	}
	return active
}

func (e *Engine) createWeakrefCModule() val.Value {
	modVal := val.NewModuleVal("_weakref", "")
	mod := modVal.AsModule()

	// _weakref.ref(ob, callback=None)
	refFn := val.NewBuiltinFuncVal("ref", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: ref() takes at least 1 argument")
		}
		ob := args[0]
		hdr := ob.HeaderPtr()
		if hdr == nil {
			panic("TypeError: cannot create weak reference to '" + ob.Type().Name + "' object")
		}
		callback := val.NoneVal()
		if len(args) > 1 {
			callback = args[1]
		}

		addr := uintptr(unsafe.Pointer(hdr))
		pyRef := &val.PyWeakRef{
			WP:       weak.Make(hdr),
			Callback: callback,
		}
		refVal := val.NewWeakRefVal(pyRef)
		registerWeakRef(addr, pyRef)

		cleanupObj := &weakCleanupData{
			engine:   e,
			addr:     addr,
			ref:      pyRef,
			refVal:   refVal,
			callback: callback,
		}

		runtime.AddCleanup(hdr, onWeakRefCleanup, cleanupObj)

		return refVal
	})
	mod.Dict.Set(val.InternString("ref"), refFn)
	mod.Dict.Set(val.InternString("ReferenceType"), val.TypeVal(val.WeakRefType))

	// _weakref.proxy(ob, callback=None)
	proxyFn := val.NewBuiltinFuncVal("proxy", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: proxy() takes at least 1 argument")
		}
		ob := args[0]
		hdr := ob.HeaderPtr()
		if hdr == nil {
			panic("TypeError: cannot create weak reference to '" + ob.Type().Name + "' object")
		}
		callback := val.NoneVal()
		if len(args) > 1 {
			callback = args[1]
		}

		isCallable := val.IsCallable(ob)
		pyProxy := &val.PyWeakProxy{
			WP:         weak.Make(hdr),
			Callback:   callback,
			IsCallable: isCallable,
		}
		proxyVal := val.NewWeakProxyVal(pyProxy)

		if !callback.IsNone() {
			addr := uintptr(unsafe.Pointer(hdr))
			runtime.AddCleanup(hdr, onWeakProxyCleanup, &weakCleanupData{
				engine:   e,
				addr:     addr,
				refVal:   proxyVal,
				callback: callback,
			})
		}

		return proxyVal
	})
	mod.Dict.Set(val.InternString("proxy"), proxyFn)
	mod.Dict.Set(val.InternString("ProxyType"), val.TypeVal(val.WeakProxyType))
	mod.Dict.Set(val.InternString("CallableProxyType"), val.TypeVal(val.CallableWeakProxyType))

	// getweakrefcount(ob)
	mod.Dict.Set(val.InternString("getweakrefcount"), val.NewBuiltinFuncVal("getweakrefcount", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: getweakrefcount() takes exactly 1 argument")
		}
		hdr := args[0].HeaderPtr()
		if hdr == nil {
			return val.Int64Val(0)
		}
		active := getActiveWeakRefs(hdr)
		return val.Int64Val(int64(len(active)))
	}))

	// getweakrefs(ob)
	mod.Dict.Set(val.InternString("getweakrefs"), val.NewBuiltinFuncVal("getweakrefs", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: getweakrefs() takes exactly 1 argument")
		}
		hdr := args[0].HeaderPtr()
		if hdr == nil {
			return val.NewListVal()
		}
		active := getActiveWeakRefs(hdr)
		res := make([]val.Value, len(active))
		for i, r := range active {
			res[i] = val.NewWeakRefVal(r)
		}
		return val.NewListVal(res...)
	}))

	// _remove_dead_weakref(dict, key)
	mod.Dict.Set(val.InternString("_remove_dead_weakref"), val.NewBuiltinFuncVal("_remove_dead_weakref", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: _remove_dead_weakref() takes exactly 2 arguments")
		}
		d := args[0]
		key := args[1]
		if d.IsDict() {
			d.AsDict().Delete(key)
		}
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("ReferenceError"), val.TypeVal(val.ReferenceErrorType))
	return modVal
}

func initWeakrefTypes(e *Engine) {
	weakrefTypesOnce.Do(func() {
		// WeakKeyDictionary
		WeakKeyDictionaryType = val.MustNewType("WeakKeyDictionary", []*val.PyType{val.ObjectType}, nil)

		dataKey := val.InternString("_data")

		WeakKeyDictionaryType.Dict.Set(val.InternString("__init__"), val.NewBuiltinFuncVal("__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: __init__() missing 1 required positional argument: 'self'")
			}
			self := args[0]
			dataDict := val.NewDictVal(16)
			val.SetAttrCtx(ctx, self, dataKey, dataDict)
			if len(args) > 1 && args[1].IsDict() {
				srcDict := args[1].AsDict()
				setitem, _, _ := self.Type().LookupMRO(val.DunderSetItem)
				for _, entry := range srcDict.Items() {
					ctx.Invoke3(setitem, self, entry.Key, entry.Value)
				}
			}
			return val.NoneVal()
		}))

		WeakKeyDictionaryType.Dict.Set(val.InternString("__getitem__"), val.NewBuiltinFuncVal("__getitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: __getitem__() requires self and key")
			}
			self := args[0]
			key := args[1]
			data := val.GetAttrCtx(ctx, self, dataKey).AsDict()

			hdr := key.HeaderPtr()
			if hdr == nil {
				panic("KeyError: " + key.String())
			}
			addr := uintptr(unsafe.Pointer(hdr))
			valKey := val.Int64Val(int64(addr))
			res, ok := data.Get(valKey)
			if !ok {
				panic("KeyError: " + key.String())
			}
			return res
		}))

		WeakKeyDictionaryType.Dict.Set(val.InternString("__setitem__"), val.NewBuiltinFuncVal("__setitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 3 {
				panic("TypeError: __setitem__() requires self, key, and value")
			}
			self := args[0]
			key := args[1]
			valToSet := args[2]
			hdr := key.HeaderPtr()
			if hdr == nil {
				panic("TypeError: cannot create weak reference to '" + key.Type().Name + "' object")
			}
			data := val.GetAttrCtx(ctx, self, dataKey).AsDict()
			addr := uintptr(unsafe.Pointer(hdr))
			valKey := val.Int64Val(int64(addr))
			data.Set(valKey, valToSet)

			runtime.AddCleanup(hdr, onDictCleanup, dictCleanupArg{dict: data, key: valKey})

			return val.NoneVal()
		}))

		WeakKeyDictionaryType.Dict.Set(val.InternString("__delitem__"), val.NewBuiltinFuncVal("__delitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: __delitem__() requires self and key")
			}
			self := args[0]
			key := args[1]
			hdr := key.HeaderPtr()
			if hdr == nil {
				panic("KeyError: " + key.String())
			}
			data := val.GetAttrCtx(ctx, self, dataKey).AsDict()
			addr := uintptr(unsafe.Pointer(hdr))
			valKey := val.Int64Val(int64(addr))
			if !data.Delete(valKey) {
				panic("KeyError: " + key.String())
			}
			return val.NoneVal()
		}))

		WeakKeyDictionaryType.Dict.Set(val.InternString("__contains__"), val.NewBuiltinFuncVal("__contains__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				return val.BoolVal(false)
			}
			self := args[0]
			key := args[1]
			hdr := key.HeaderPtr()
			if hdr == nil {
				return val.BoolVal(false)
			}
			data := val.GetAttrCtx(ctx, self, dataKey).AsDict()
			addr := uintptr(unsafe.Pointer(hdr))
			_, ok := data.Get(val.Int64Val(int64(addr)))
			return val.BoolVal(ok)
		}))

		WeakKeyDictionaryType.Dict.Set(val.InternString("__len__"), val.NewBuiltinFuncVal("__len__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				return val.Int64Val(0)
			}
			data := val.GetAttrCtx(ctx, args[0], dataKey).AsDict()
			return val.Int64Val(int64(data.Len()))
		}))

		WeakKeyDictionaryType.Dict.Set(val.InternString("get"), val.NewBuiltinFuncVal("get", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: get() requires at least key argument")
			}
			self := args[0]
			key := args[1]
			dflt := val.NoneVal()
			if len(args) > 2 {
				dflt = args[2]
			}
			hdr := key.HeaderPtr()
			if hdr == nil {
				return dflt
			}
			data := val.GetAttrCtx(ctx, self, dataKey).AsDict()
			addr := uintptr(unsafe.Pointer(hdr))
			v, ok := data.Get(val.Int64Val(int64(addr)))
			if !ok {
				return dflt
			}
			return v
		}))
		WeakKeyDictionaryType.Dict.Set(val.InternString("clear"), val.NewBuiltinFuncVal("clear", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				return val.NoneVal()
			}
			data := val.GetAttrCtx(ctx, args[0], dataKey)
			if data.IsDict() {
				data.AsDict().Clear()
			}
			return val.NoneVal()
		}))
		WeakKeyDictionaryType.InvalidateVersion()

		// WeakValueDictionary
		WeakValueDictionaryType = val.MustNewType("WeakValueDictionary", []*val.PyType{val.ObjectType}, nil)

		WeakValueDictionaryType.Dict.Set(val.InternString("__init__"), val.NewBuiltinFuncVal("__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: __init__() missing 1 required positional argument: 'self'")
			}
			self := args[0]
			dataDict := val.NewDictVal(16)
			val.SetAttrCtx(ctx, self, dataKey, dataDict)
			if len(args) > 1 && args[1].IsDict() {
				srcDict := args[1].AsDict()
				setitem, _, _ := self.Type().LookupMRO(val.DunderSetItem)
				for _, entry := range srcDict.Items() {
					ctx.Invoke3(setitem, self, entry.Key, entry.Value)
				}
			}
			return val.NoneVal()
		}))

		WeakValueDictionaryType.Dict.Set(val.InternString("__getitem__"), val.NewBuiltinFuncVal("__getitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: __getitem__() requires self and key")
			}
			self := args[0]
			key := args[1]
			data := val.GetAttrCtx(ctx, self, dataKey).AsDict()
			wrVal, ok := data.Get(key)
			if !ok {
				panic("KeyError: " + key.String())
			}
			if !wrVal.IsWeakRef() {
				return wrVal
			}
			res := wrVal.AsWeakRef().WP.Value()
			if res == nil {
				data.Delete(key)
				panic("KeyError: " + key.String())
			}
			return val.ObjectVal(res)
		}))

		WeakValueDictionaryType.Dict.Set(val.InternString("clear"), val.NewBuiltinFuncVal("clear", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				return val.NoneVal()
			}
			data := val.GetAttrCtx(ctx, args[0], dataKey)
			if data.IsDict() {
				data.AsDict().Clear()
			}
			return val.NoneVal()
		}))

		WeakValueDictionaryType.Dict.Set(val.InternString("__setitem__"), val.NewBuiltinFuncVal("__setitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 3 {
				panic("TypeError: __setitem__() requires self, key, and value")
			}
			self := args[0]
			key := args[1]
			targetVal := args[2]
			hdr := targetVal.HeaderPtr()
			if hdr == nil {
				panic("TypeError: cannot create weak reference to '" + targetVal.Type().Name + "' object")
			}
			data := val.GetAttrCtx(ctx, self, dataKey).AsDict()
			pyRef := &val.PyWeakRef{
				WP: weak.Make(hdr),
			}
			refVal := val.NewWeakRefVal(pyRef)
			data.Set(key, refVal)

			runtime.AddCleanup(hdr, onDictCleanup, dictCleanupArg{dict: data, key: key})

			return val.NoneVal()
		}))

		WeakValueDictionaryType.Dict.Set(val.InternString("__delitem__"), val.NewBuiltinFuncVal("__delitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: __delitem__() requires self and key")
			}
			self := args[0]
			key := args[1]
			data := val.GetAttrCtx(ctx, self, dataKey).AsDict()
			if !data.Delete(key) {
				panic("KeyError: " + key.String())
			}
			return val.NoneVal()
		}))

		WeakValueDictionaryType.Dict.Set(val.InternString("__contains__"), val.NewBuiltinFuncVal("__contains__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				return val.BoolVal(false)
			}
			self := args[0]
			key := args[1]
			data := val.GetAttrCtx(ctx, self, dataKey).AsDict()
			wrVal, ok := data.Get(key)
			if !ok {
				return val.BoolVal(false)
			}
			if !wrVal.IsWeakRef() {
				return val.BoolVal(true)
			}
			return val.BoolVal(wrVal.AsWeakRef().WP.Value() != nil)
		}))

		WeakValueDictionaryType.Dict.Set(val.InternString("__len__"), val.NewBuiltinFuncVal("__len__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				return val.Int64Val(0)
			}
			data := val.GetAttrCtx(ctx, args[0], dataKey).AsDict()
			return val.Int64Val(int64(data.Len()))
		}))

		WeakValueDictionaryType.Dict.Set(val.InternString("get"), val.NewBuiltinFuncVal("get", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: get() requires at least key argument")
			}
			self := args[0]
			key := args[1]
			dflt := val.NoneVal()
			if len(args) > 2 {
				dflt = args[2]
			}
			data := val.GetAttrCtx(ctx, self, dataKey).AsDict()
			wrVal, ok := data.Get(key)
			if !ok {
				return dflt
			}
			if !wrVal.IsWeakRef() {
				return wrVal
			}
			res := wrVal.AsWeakRef().WP.Value()
			if res == nil {
				data.Delete(key)
				return dflt
			}
			return val.ObjectVal(res)
		}))

		WeakValueDictionaryType.Dict.Set(val.InternString("clear"), val.NewBuiltinFuncVal("clear", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				return val.NoneVal()
			}
			data := val.GetAttrCtx(ctx, args[0], dataKey)
			if data.IsDict() {
				data.AsDict().Clear()
			}
			return val.NoneVal()
		}))
		WeakValueDictionaryType.InvalidateVersion()

		// WeakSet
		WeakSetType = val.MustNewType("WeakSet", []*val.PyType{val.ObjectType}, nil)

		WeakSetType.Dict.Set(val.InternString("__init__"), val.NewBuiltinFuncVal("__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: __init__() missing 1 required positional argument: 'self'")
			}
			self := args[0]
			dataDict := val.NewDictVal(16)
			val.SetAttrCtx(ctx, self, dataKey, dataDict)
			if len(args) > 1 {
				addFn, _, _ := self.Type().LookupMRO(val.InternString("add"))
				if args[1].IsList() {
					for _, item := range args[1].AsList().Items {
						ctx.Invoke2(addFn, self, item)
					}
				}
			}
			return val.NoneVal()
		}))

		WeakSetType.Dict.Set(val.InternString("add"), val.NewBuiltinFuncVal("add", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: add() requires self and item")
			}
			self := args[0]
			item := args[1]
			hdr := item.HeaderPtr()
			if hdr == nil {
				panic("TypeError: cannot create weak reference to '" + item.Type().Name + "' object")
			}
			data := val.GetAttrCtx(ctx, self, dataKey).AsDict()
			addr := uintptr(unsafe.Pointer(hdr))
			valKey := val.Int64Val(int64(addr))
			data.Set(valKey, item)

			runtime.AddCleanup(hdr, onDictCleanup, dictCleanupArg{dict: data, key: valKey})

			return val.NoneVal()
		}))

		WeakSetType.Dict.Set(val.InternString("discard"), val.NewBuiltinFuncVal("discard", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				return val.NoneVal()
			}
			self := args[0]
			item := args[1]
			hdr := item.HeaderPtr()
			if hdr == nil {
				return val.NoneVal()
			}
			data := val.GetAttrCtx(ctx, self, dataKey).AsDict()
			addr := uintptr(unsafe.Pointer(hdr))
			data.Delete(val.Int64Val(int64(addr)))
			return val.NoneVal()
		}))

		WeakSetType.Dict.Set(val.InternString("__contains__"), val.NewBuiltinFuncVal("__contains__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				return val.BoolVal(false)
			}
			self := args[0]
			item := args[1]
			hdr := item.HeaderPtr()
			if hdr == nil {
				return val.BoolVal(false)
			}
			data := val.GetAttrCtx(ctx, self, dataKey).AsDict()
			addr := uintptr(unsafe.Pointer(hdr))
			_, ok := data.Get(val.Int64Val(int64(addr)))
			return val.BoolVal(ok)
		}))

		WeakSetType.Dict.Set(val.InternString("__len__"), val.NewBuiltinFuncVal("__len__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				return val.Int64Val(0)
			}
			data := val.GetAttrCtx(ctx, args[0], dataKey).AsDict()
			return val.Int64Val(int64(data.Len()))
		}))
		WeakSetType.InvalidateVersion()

		// finalize
		FinalizeType = val.MustNewType("finalize", []*val.PyType{val.ObjectType}, nil)

		aliveKey := val.InternString("alive")
		fnKey := val.InternString("_fn")

		FinalizeType.Dict.Set(val.InternString("__init__"), val.NewBuiltinFuncVal("__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 3 {
				panic("TypeError: finalize() requires self, obj, and func")
			}
			self := args[0]
			obj := args[1]
			fn := args[2]
			hdr := obj.HeaderPtr()
			if hdr == nil {
				panic("TypeError: cannot create weak reference to '" + obj.Type().Name + "' object")
			}

			val.SetAttrCtx(ctx, self, aliveKey, val.BoolVal(true))
			val.SetAttrCtx(ctx, self, fnKey, fn)

			runtime.AddCleanup(hdr, onFinalizeCleanup, finalizeCleanupArg{
				engine:   e,
				selfVal:  self,
				aliveKey: aliveKey,
				fnVal:    fn,
			})

			return val.NoneVal()
		}))

		FinalizeType.Dict.Set(val.InternString("__call__"), val.NewBuiltinFuncVal("__call__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				return val.NoneVal()
			}
			self := args[0]
			isAlive := val.GetAttrCtx(ctx, self, aliveKey)
			if isAlive.IsBool() && isAlive.AsBool() {
				val.SetAttrCtx(ctx, self, aliveKey, val.BoolVal(false))
				fn := val.GetAttrCtx(ctx, self, fnKey)
				if !fn.IsNone() {
					vmInstance := ctx.VM.(*VM)
					return vmInstance.Vectorcall(fn, nil, nil)
				}
			}
			return val.NoneVal()
		}))

		FinalizeType.Dict.Set(val.InternString("detach"), val.NewBuiltinFuncVal("detach", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				return val.NoneVal()
			}
			self := args[0]
			val.SetAttrCtx(ctx, self, aliveKey, val.BoolVal(false))
			return val.NoneVal()
		}))
		FinalizeType.InvalidateVersion()
	})
}

func (e *Engine) createWeakrefModule() val.Value {
	cModVal := e.createWeakrefCModule()
	cMod := cModVal.AsModule()

	modVal := val.NewModuleVal("weakref", "")
	mod := modVal.AsModule()

	// Copy all symbols from _weakref
	for _, entry := range cMod.Dict.Items() {
		mod.Dict.Set(entry.Key, entry.Value)
	}

	initWeakrefTypes(e)

	mod.Dict.Set(val.InternString("WeakKeyDictionary"), val.TypeVal(WeakKeyDictionaryType))
	mod.Dict.Set(val.InternString("WeakValueDictionary"), val.TypeVal(WeakValueDictionaryType))
	mod.Dict.Set(val.InternString("WeakSet"), val.TypeVal(WeakSetType))
	mod.Dict.Set(val.InternString("finalize"), val.TypeVal(FinalizeType))

	mod.Dict.Set(val.InternString("ReferenceError"), val.TypeVal(val.ReferenceErrorType))
	return modVal
}
