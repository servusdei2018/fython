package vm

import (
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	DequeType       *val.PyType
	DefaultDictType *val.PyType
	TupleGetterType *val.PyType
	collectionsOnce sync.Once
)

type pyDeque struct {
	Header val.ObjectHeader
	Items  []val.Value
	MaxLen int // -1 for unbounded
	Mu     sync.RWMutex
}

func (d *pyDeque) appendRight(v val.Value) {
	d.Mu.Lock()
	defer d.Mu.Unlock()
	if d.MaxLen >= 0 && len(d.Items) >= d.MaxLen {
		if d.MaxLen == 0 {
			return
		}
		copy(d.Items, d.Items[1:])
		d.Items[len(d.Items)-1] = v
		return
	}
	d.Items = append(d.Items, v)
}

func (d *pyDeque) appendLeft(v val.Value) {
	d.Mu.Lock()
	defer d.Mu.Unlock()
	if d.MaxLen >= 0 && len(d.Items) >= d.MaxLen {
		if d.MaxLen == 0 {
			return
		}
		copy(d.Items[1:], d.Items[:len(d.Items)-1])
		d.Items[0] = v
		return
	}
	newItems := make([]val.Value, len(d.Items)+1)
	newItems[0] = v
	copy(newItems[1:], d.Items)
	d.Items = newItems
}

func (d *pyDeque) popRight() val.Value {
	d.Mu.Lock()
	defer d.Mu.Unlock()
	if len(d.Items) == 0 {
		panic("IndexError: pop from an empty deque")
	}
	idx := len(d.Items) - 1
	v := d.Items[idx]
	d.Items = d.Items[:idx]
	return v
}

func (d *pyDeque) popLeft() val.Value {
	d.Mu.Lock()
	defer d.Mu.Unlock()
	if len(d.Items) == 0 {
		panic("IndexError: pop from an empty deque")
	}
	v := d.Items[0]
	d.Items = d.Items[1:]
	return v
}

func (d *pyDeque) rotate(n int) {
	d.Mu.Lock()
	defer d.Mu.Unlock()
	l := len(d.Items)
	if l <= 1 {
		return
	}
	n = n % l
	if n < 0 {
		n += l
	}
	if n == 0 {
		return
	}
	// rotate right by n: last n elements move to front
	split := l - n
	rotated := make([]val.Value, l)
	copy(rotated, d.Items[split:])
	copy(rotated[n:], d.Items[:split])
	d.Items = rotated
}

type pyTupleGetter struct {
	Header val.ObjectHeader
	Index  int
	Doc    val.Value
}

func initCollectionsTypes() {
	collectionsOnce.Do(func() {
		// 1. DequeType
		DequeType = val.MustNewType("deque", []*val.PyType{val.ObjectType}, nil)

		DequeType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			maxLen := -1
			posCount := len(args) - len(kwnames)
			if posCount > 1 && !args[1].IsNone() && args[1].IsInt64() {
				maxLen = int(args[1].AsInt64())
			}
			if len(kwnames) > 0 {
				kwStart := len(args) - len(kwnames)
				for i, kn := range kwnames {
					if kn.AsString() == "maxlen" {
						v := args[kwStart+i]
						if v.IsInt64() {
							maxLen = int(v.AsInt64())
						}
					}
				}
			}
			if maxLen < -1 {
				panic("ValueError: maxlen must be non-negative")
			}
			deq := &pyDeque{
				Header: val.ObjectHeader{Kind: val.KindNative, Type: DequeType},
				MaxLen: maxLen,
			}
			if posCount > 0 && !args[0].IsNone() {
				items := val.ExtractIterableItems(ctx, args[0])
				for _, v := range items {
					deq.appendRight(v)
				}
			}
			return val.ObjectVal(&deq.Header)
		}

		DequeType.Slots.SqLen = func(ctx *val.ExecContext, o val.Value) int64 {
			deq := (*pyDeque)(unsafe.Pointer(o.HeaderPtr()))
			deq.Mu.RLock()
			defer deq.Mu.RUnlock()
			return int64(len(deq.Items))
		}

		DequeType.Slots.TpIter = func(ctx *val.ExecContext, o val.Value) val.Value {
			deq := (*pyDeque)(unsafe.Pointer(o.HeaderPtr()))
			deq.Mu.RLock()
			defer deq.Mu.RUnlock()
			itemsCopy := make([]val.Value, len(deq.Items))
			copy(itemsCopy, deq.Items)
			return val.NewIterVal(val.NewListVal(itemsCopy...))
		}

		DequeType.Slots.MpSubscript = func(ctx *val.ExecContext, o, key val.Value) val.Value {
			if !key.IsInt64() {
				panic("TypeError: deque indices must be integers")
			}
			deq := (*pyDeque)(unsafe.Pointer(o.HeaderPtr()))
			deq.Mu.RLock()
			defer deq.Mu.RUnlock()
			idx := int(key.AsInt64())
			if idx < 0 {
				idx += len(deq.Items)
			}
			if idx < 0 || idx >= len(deq.Items) {
				panic("IndexError: deque index out of range")
			}
			return deq.Items[idx]
		}

		DequeType.Slots.MpAssSubscript = func(ctx *val.ExecContext, o, key, value val.Value) val.Value {
			if !key.IsInt64() {
				panic("TypeError: deque indices must be integers")
			}
			deq := (*pyDeque)(unsafe.Pointer(o.HeaderPtr()))
			deq.Mu.Lock()
			defer deq.Mu.Unlock()
			idx := int(key.AsInt64())
			if idx < 0 {
				idx += len(deq.Items)
			}
			if idx < 0 || idx >= len(deq.Items) {
				panic("IndexError: deque index out of range")
			}
			deq.Items[idx] = value
			return val.NoneVal()
		}

		// Deque methods
		registerDequeMethod := func(name string, fn func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value) {
			DequeType.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				if len(args) == 0 {
					panic("TypeError: descriptor '" + name + "' of 'deque' object needs an argument")
				}
				deq := (*pyDeque)(unsafe.Pointer(args[0].HeaderPtr()))
				return fn(ctx, deq, args[1:])
			}))
		}

		registerDequeMethod("append", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: append() takes exactly one argument")
			}
			deq.appendRight(args[0])
			return val.NoneVal()
		})

		registerDequeMethod("appendleft", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: appendleft() takes exactly one argument")
			}
			deq.appendLeft(args[0])
			return val.NoneVal()
		})

		registerDequeMethod("pop", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			return deq.popRight()
		})

		registerDequeMethod("popleft", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			return deq.popLeft()
		})

		registerDequeMethod("rotate", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			n := 1
			if len(args) > 0 && args[0].IsInt64() {
				n = int(args[0].AsInt64())
			}
			deq.rotate(n)
			return val.NoneVal()
		})

		registerDequeMethod("clear", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			deq.Mu.Lock()
			deq.Items = nil
			deq.Mu.Unlock()
			return val.NoneVal()
		})

		registerDequeMethod("extend", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: extend() takes exactly one argument")
			}
			for _, v := range val.ExtractIterableItems(ctx, args[0]) {
				deq.appendRight(v)
			}
			return val.NoneVal()
		})

		registerDequeMethod("extendleft", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: extendleft() takes exactly one argument")
			}
			for _, v := range val.ExtractIterableItems(ctx, args[0]) {
				deq.appendLeft(v)
			}
			return val.NoneVal()
		})

		registerDequeMethod("copy", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			deq.Mu.RLock()
			defer deq.Mu.RUnlock()
			newDeq := &pyDeque{
				Header: val.ObjectHeader{Kind: val.KindNative, Type: DequeType},
				Items:  append([]val.Value(nil), deq.Items...),
				MaxLen: deq.MaxLen,
			}
			return val.ObjectVal(&newDeq.Header)
		})

		// 2. TupleGetterType
		TupleGetterType = val.MustNewType("_tuplegetter", []*val.PyType{val.ObjectType}, nil)
		TupleGetterType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 || !args[0].IsInt64() {
				panic("TypeError: _tuplegetter index must be an integer")
			}
			idx := int(args[0].AsInt64())
			doc := val.NoneVal()
			if len(args) > 1 {
				doc = args[1]
			}
			getter := &pyTupleGetter{
				Header: val.ObjectHeader{
					Kind:  val.KindNative,
					Flags: val.FlagDataDescriptor,
					Type:  TupleGetterType,
				},
				Index: idx,
				Doc:   doc,
			}
			return val.ObjectVal((*val.ObjectHeader)(unsafe.Pointer(&getter.Header)))
		}

		TupleGetterType.Slots.TpDescrGet = func(ctx *val.ExecContext, descr, obj val.Value, owner *val.PyType) val.Value {
			if obj.IsNone() || obj.IsUnbound() {
				return descr
			}
			getter := (*pyTupleGetter)(unsafe.Pointer(descr.HeaderPtr()))
			if obj.IsTuple() {
				t := obj.AsTuple()
				if getter.Index < 0 || getter.Index >= len(t.Items) {
					panic("IndexError: tuple index out of range")
				}
				return t.Items[getter.Index]
			}
			if sqItem := obj.Type().Slots.SqItem; sqItem != nil {
				return sqItem(ctx, obj, val.Int64Val(int64(getter.Index)))
			}
			panic("TypeError: tuplegetter applied to non-tuple")
		}

		TupleGetterType.Slots.TpGetAttr = func(ctx *val.ExecContext, obj, name val.Value) val.Value {
			getter := (*pyTupleGetter)(unsafe.Pointer(obj.HeaderPtr()))
			if name.AsString() == "__doc__" {
				return getter.Doc
			}
			return val.ObjectGetAttr(ctx, obj, name)
		}

		TupleGetterType.Slots.TpSetAttr = func(ctx *val.ExecContext, obj, name, value val.Value) val.Value {
			getter := (*pyTupleGetter)(unsafe.Pointer(obj.HeaderPtr()))
			if name.AsString() == "__doc__" {
				getter.Doc = value
				return val.NoneVal()
			}
			panic(fmt.Sprintf("AttributeError: '_tuplegetter' object has no attribute '%s'", name.AsString()))
		}

		DequeType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("deque.__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			deq := (*pyDeque)(val.AsPointer(args[0]))
			deq.Mu.RLock()
			items := append([]val.Value(nil), deq.Items...)
			maxLen := deq.MaxLen
			deq.Mu.RUnlock()
			var sb strings.Builder
			sb.WriteString("deque([")
			for i, it := range items {
				if i > 0 {
					sb.WriteString(", ")
				}
				sb.WriteString(val.PyRepr(ctx, it))
			}
			sb.WriteString("]")
			if maxLen >= 0 {
				sb.WriteString(", maxlen=" + itoa(maxLen))
			}
			sb.WriteString(")")
			return val.NewStringVal(sb.String())
		}))
		DequeType.InvalidateVersion()

		// 3. DefaultDictType (subclass of dict)
		DefaultDictType = val.MustNewType("defaultdict", []*val.PyType{val.DictType}, nil)
		DefaultDictType.Dict.Set(val.InternString("default_factory"), val.NoneVal())
		getDefaultFactory := func(d *val.PyDict) (val.Value, bool) {
			key := val.InternString("default_factory")
			if d.Attrs != nil {
				if f, ok := d.Attrs.Get(key); ok {
					return f, true
				}
			}
			return d.Get(key)
		}
		defaultDictInit := func(ctx *val.ExecContext, dVal val.Value, args []val.Value, kwnames []val.Value) {
			d := dVal.AsDict()
			defaultFactory := val.NoneVal()
			posCount := len(args) - len(kwnames)
			var dictArgs []val.Value
			if posCount > 0 {
				defaultFactory = args[0]
				dictArgs = append(dictArgs, args[1:]...)
			} else if len(kwnames) > 0 {
				dictArgs = append(dictArgs, args...)
			}
			if d.Attrs == nil {
				d.Attrs = val.NewDictVal(4).AsDict()
			}
			d.Attrs.Set(val.InternString("default_factory"), defaultFactory)
			if len(dictArgs) > 0 {
				initFn, _, ok := val.DictType.LookupMRO(val.DunderInit)
				if ok && initFn.IsBuiltinFunc() {
					callArgs := make([]val.Value, 1+len(dictArgs))
					callArgs[0] = dVal
					copy(callArgs[1:], dictArgs)
					initFn.AsBuiltinFunc().Fn(ctx, initFn, callArgs, kwnames)
				}
			}
		}
		DefaultDictType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			dVal := val.NewDictVal(16)
			d := dVal.AsDict()
			if callable.IsType() {
				d.Header.Type = callable.AsType()
			} else {
				d.Header.Type = DefaultDictType
			}
			defaultDictInit(ctx, dVal, args, kwnames)
			return dVal
		}
		DefaultDictType.Dict.Set(val.DunderInit, val.NewBuiltinFuncVal("__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: descriptor '__init__' of 'defaultdict' object needs an argument")
			}
			defaultDictInit(ctx, args[0], args[1:], kwnames)
			return val.NoneVal()
		}))

		DefaultDictType.Slots.MpSubscript = func(ctx *val.ExecContext, o, key val.Value) val.Value {
			d := o.AsDict()
			if v, ok := d.Get(key); ok {
				return v
			}
			if missingFn, _, ok := o.Type().LookupMRO(val.InternString("__missing__")); ok {
				return ctx.Invoke2(missingFn, o, key)
			}
			panic("KeyError: " + key.String())
		}

		DefaultDictType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("defaultdict.__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			d := args[0].AsDict()
			factory, _ := getDefaultFactory(d)
			var sb strings.Builder
			sb.WriteString("defaultdict(" + val.PyRepr(ctx, factory) + ", {")
			first := true
			for _, e := range d.ActiveEntries() {
				if !e.Active || (e.Key.IsAnyString() && e.Key.AsString() == "default_factory") {
					continue
				}
				if !first {
					sb.WriteString(", ")
				}
				first = false
				sb.WriteString(val.PyRepr(ctx, e.Key) + ": " + val.PyRepr(ctx, e.Value))
			}
			sb.WriteString("})")
			return val.NewStringVal(sb.String())
		}))
		DefaultDictType.Dict.Set(val.InternString("__missing__"), val.NewBuiltinFuncVal("__missing__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: __missing__() takes 1 argument")
			}
			self := args[0]
			key := args[1]
			d := self.AsDict()
			factory, ok := getDefaultFactory(d)
			if !ok || factory.IsNone() {
				panic("KeyError: " + key.String())
			}
			newVal := ctx.Invoke0(factory)
			d.Set(key, newVal)
			return newVal
		}))
		DefaultDictType.InvalidateVersion()
	})
}

func (e *Engine) createCollectionsModule() val.Value {
	initCollectionsTypes()

	modVal := val.NewModuleVal("_collections", "")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("deque"), val.TypeVal(DequeType))
	mod.Dict.Set(val.InternString("defaultdict"), val.TypeVal(DefaultDictType))
	mod.Dict.Set(val.InternString("_tuplegetter"), val.TypeVal(TupleGetterType))

	// _count_elements(mapping, iterable)
	mod.Dict.Set(val.InternString("_count_elements"), val.NewBuiltinFuncVal("_count_elements", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: _count_elements() takes 2 arguments")
		}
		mapping := args[0]
		iterable := args[1]
		elems := val.ExtractIterableItems(ctx, iterable)
		if ctx != nil && ctx.HasException() {
			return val.NoneVal()
		}

		if mapping.IsDict() {
			d := mapping.AsDict()
			for _, elem := range elems {
				curCount, found := d.Get(elem)
				if !found || !curCount.IsInt64() {
					d.Set(elem, val.Int64Val(1))
				} else {
					d.Set(elem, val.Int64Val(curCount.AsInt64()+1))
				}
			}
			return val.NoneVal()
		}

		for _, elem := range elems {
			curVal := val.Int64Val(0)
			getFn, _, okGet := mapping.Type().LookupMRO(val.InternString("get"))
			if okGet {
				curVal = ctx.Invoke3(getFn, mapping, elem, val.Int64Val(0))
			}
			newVal := val.Int64Val(curVal.AsInt64() + 1)
			val.SetItemCtx(ctx, mapping, elem, newVal)
		}

		return val.NoneVal()
	}))

	return modVal
}
