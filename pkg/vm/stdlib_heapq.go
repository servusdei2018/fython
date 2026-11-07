package vm

import (
	"github.com/servusdei2018/fython/pkg/val"
)

func getHeapList(args []val.Value, name string) *val.PyList {
	if len(args) == 0 {
		panic("TypeError: " + name + "() takes at least 1 argument (0 given)")
	}
	v := args[0]
	if !v.IsList() {
		panic("TypeError: heap argument must be a list")
	}
	return v.AsList()
}

func heapqSiftdown(heap []val.Value, startpos, pos int) {
	newitem := heap[pos]
	for pos > startpos {
		parentpos := (pos - 1) >> 1
		parent := heap[parentpos]
		if val.LessThan(newitem, parent).IsTruthy() {
			heap[pos] = parent
			pos = parentpos
			continue
		}
		break
	}
	heap[pos] = newitem
}

func heapqSiftup(heap []val.Value, pos int) {
	endpos := len(heap)
	startpos := pos
	newitem := heap[pos]
	childpos := 2*pos + 1
	for childpos < endpos {
		rightpos := childpos + 1
		if rightpos < endpos && !val.LessThan(heap[childpos], heap[rightpos]).IsTruthy() {
			childpos = rightpos
		}
		heap[pos] = heap[childpos]
		pos = childpos
		childpos = 2*pos + 1
	}
	heap[pos] = newitem
	heapqSiftdown(heap, startpos, pos)
}

func heapqSiftdownMax(heap []val.Value, startpos, pos int) {
	newitem := heap[pos]
	for pos > startpos {
		parentpos := (pos - 1) >> 1
		parent := heap[parentpos]
		if val.GreaterThan(newitem, parent).IsTruthy() {
			heap[pos] = parent
			pos = parentpos
			continue
		}
		break
	}
	heap[pos] = newitem
}

func heapqSiftupMax(heap []val.Value, pos int) {
	endpos := len(heap)
	startpos := pos
	newitem := heap[pos]
	childpos := 2*pos + 1
	for childpos < endpos {
		rightpos := childpos + 1
		if rightpos < endpos && !val.GreaterThan(heap[childpos], heap[rightpos]).IsTruthy() {
			childpos = rightpos
		}
		heap[pos] = heap[childpos]
		pos = childpos
		childpos = 2*pos + 1
	}
	heap[pos] = newitem
	heapqSiftdownMax(heap, startpos, pos)
}

func (e *Engine) createHeapQModule() val.Value {
	modVal := val.NewModuleVal("_heapq", "Native heap queue algorithm")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("heappush"), val.NewBuiltinFuncVal("heappush", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: heappush() takes at least 2 arguments")
		}
		lst := getHeapList(args, "heappush")
		item := args[1]
		lst.Items = append(lst.Items, item)
		heapqSiftdown(lst.Items, 0, len(lst.Items)-1)
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("heappop"), val.NewBuiltinFuncVal("heappop", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		lst := getHeapList(args, "heappop")
		if len(lst.Items) == 0 {
			panic(val.NewException(val.IndexErrorType, "index out of range"))
		}
		lastelt := lst.Items[len(lst.Items)-1]
		lst.Items = lst.Items[:len(lst.Items)-1]
		if len(lst.Items) > 0 {
			returnitem := lst.Items[0]
			lst.Items[0] = lastelt
			heapqSiftup(lst.Items, 0)
			return returnitem
		}
		return lastelt
	}))

	mod.Dict.Set(val.InternString("heapreplace"), val.NewBuiltinFuncVal("heapreplace", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: heapreplace() takes at least 2 arguments")
		}
		lst := getHeapList(args, "heapreplace")
		if len(lst.Items) == 0 {
			panic(val.NewException(val.IndexErrorType, "index out of range"))
		}
		item := args[1]
		returnitem := lst.Items[0]
		lst.Items[0] = item
		heapqSiftup(lst.Items, 0)
		return returnitem
	}))

	mod.Dict.Set(val.InternString("heappushpop"), val.NewBuiltinFuncVal("heappushpop", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: heappushpop() takes at least 2 arguments")
		}
		lst := getHeapList(args, "heappushpop")
		item := args[1]
		if len(lst.Items) > 0 && val.LessThan(lst.Items[0], item).IsTruthy() {
			item, lst.Items[0] = lst.Items[0], item
			heapqSiftup(lst.Items, 0)
		}
		return item
	}))

	mod.Dict.Set(val.InternString("heapify"), val.NewBuiltinFuncVal("heapify", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		lst := getHeapList(args, "heapify")
		n := len(lst.Items)
		for i := (n >> 1) - 1; i >= 0; i-- {
			heapqSiftup(lst.Items, i)
		}
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("_heapify_max"), val.NewBuiltinFuncVal("_heapify_max", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		lst := getHeapList(args, "_heapify_max")
		n := len(lst.Items)
		for i := (n >> 1) - 1; i >= 0; i-- {
			heapqSiftupMax(lst.Items, i)
		}
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("_heappop_max"), val.NewBuiltinFuncVal("_heappop_max", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		lst := getHeapList(args, "_heappop_max")
		if len(lst.Items) == 0 {
			panic(val.NewException(val.IndexErrorType, "index out of range"))
		}
		lastelt := lst.Items[len(lst.Items)-1]
		lst.Items = lst.Items[:len(lst.Items)-1]
		if len(lst.Items) > 0 {
			returnitem := lst.Items[0]
			lst.Items[0] = lastelt
			heapqSiftupMax(lst.Items, 0)
			return returnitem
		}
		return lastelt
	}))

	mod.Dict.Set(val.InternString("_heapreplace_max"), val.NewBuiltinFuncVal("_heapreplace_max", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: _heapreplace_max() takes at least 2 arguments")
		}
		lst := getHeapList(args, "_heapreplace_max")
		if len(lst.Items) == 0 {
			panic(val.NewException(val.IndexErrorType, "index out of range"))
		}
		item := args[1]
		returnitem := lst.Items[0]
		lst.Items[0] = item
		heapqSiftupMax(lst.Items, 0)
		return returnitem
	}))

	return modVal
}
