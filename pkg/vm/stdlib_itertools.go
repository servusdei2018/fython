package vm

import (
	"github.com/servusdei2018/fython/pkg/val"
)

func (e *Engine) createItertoolsModule() val.Value {
	modVal := val.NewModuleVal("itertools", "")
	mod := modVal.AsModule()

	// 1. count(start=0, step=1)
	mod.Dict.Set(val.InternString("count"), val.NewBuiltinFuncVal("count", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		start := int64(0)
		step := int64(1)
		if len(args) > 0 && args[0].IsInt64() {
			start = args[0].AsInt64()
		}
		if len(args) > 1 && args[1].IsInt64() {
			step = args[1].AsInt64()
		}
		curr := start
		return val.NewIterValWithNext(func() (val.Value, bool) {
			v := curr
			curr += step
			return val.Int64Val(v), true
		})
	}))

	// 2. repeat(object[, times])
	mod.Dict.Set(val.InternString("repeat"), val.NewBuiltinFuncVal("repeat", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: repeat() takes at least 1 argument")
		}
		obj := args[0]
		hasTimes := len(args) > 1 && args[1].IsInt64()
		times := int64(0)
		if hasTimes {
			times = args[1].AsInt64()
		}
		count := int64(0)
		return val.NewIterValWithNext(func() (val.Value, bool) {
			if hasTimes && count >= times {
				return val.NoneVal(), false
			}
			count++
			return obj, true
		})
	}))

	// 3. cycle(iterable)
	mod.Dict.Set(val.InternString("cycle"), val.NewBuiltinFuncVal("cycle", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: cycle() takes exactly one argument")
		}
		it := iterFor(ctx, args[0])
		var saved []val.Value
		exhausted := false
		savedIdx := 0

		return val.NewIterValWithNext(func() (val.Value, bool) {
			if !exhausted {
				v, ok := it.Next()
				if ok {
					saved = append(saved, v)
					return v, true
				}
				exhausted = true
				if len(saved) == 0 {
					return val.NoneVal(), false
				}
			}
			v := saved[savedIdx]
			savedIdx = (savedIdx + 1) % len(saved)
			return v, true
		})
	}))

	// 4. chain(*iterables) & chain.from_iterable(iterable)
	chainType := val.MustNewType("chain", []*val.PyType{val.ObjectType}, nil)
	chainType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		iters := make([]*val.PyIter, len(args))
		for i, a := range args {
			iters[i] = iterFor(ctx, a)
		}
		curIdx := 0
		return val.NewIterValWithNext(func() (val.Value, bool) {
			for curIdx < len(iters) {
				v, ok := iters[curIdx].Next()
				if ok {
					return v, true
				}
				curIdx++
			}
			return val.NoneVal(), false
		})
	}
	chainType.Dict.Set(val.InternString("from_iterable"), val.NewBuiltinFuncVal("from_iterable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: from_iterable() takes exactly one argument")
		}
		outerIt := iterFor(ctx, args[0])
		var curSubIt *val.PyIter
		return val.NewIterValWithNext(func() (val.Value, bool) {
			for {
				if curSubIt != nil {
					v, ok := curSubIt.Next()
					if ok {
						return v, true
					}
					curSubIt = nil
				}
				subIterable, ok := outerIt.Next()
				if !ok {
					return val.NoneVal(), false
				}
				curSubIt = iterFor(ctx, subIterable)
			}
		})
	}))
	mod.Dict.Set(val.InternString("chain"), val.TypeVal(chainType))

	// 5. islice(iterable, [start,] stop [, step])
	mod.Dict.Set(val.InternString("islice"), val.NewBuiltinFuncVal("islice", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: islice expected at least 2 arguments")
		}
		it := iterFor(ctx, args[0])
		start := int64(0)
		stop := int64(-1) // -1 means until end / infinite
		step := int64(1)

		if len(args) == 2 {
			if !args[1].IsNone() && args[1].IsInt64() {
				stop = args[1].AsInt64()
			}
		} else {
			if !args[1].IsNone() && args[1].IsInt64() {
				start = args[1].AsInt64()
			}
			if !args[2].IsNone() && args[2].IsInt64() {
				stop = args[2].AsInt64()
			}
			if len(args) > 3 && !args[3].IsNone() && args[3].IsInt64() {
				step = args[3].AsInt64()
				if step <= 0 {
					panic("ValueError: Step for islice() must be a positive integer or None")
				}
			}
		}

		curIdx := int64(0)
		// Advance to start
		for curIdx < start {
			_, ok := it.Next()
			if !ok {
				return val.NewIterValWithNext(func() (val.Value, bool) { return val.NoneVal(), false })
			}
			curIdx++
		}

		return val.NewIterValWithNext(func() (val.Value, bool) {
			if stop >= 0 && curIdx >= stop {
				return val.NoneVal(), false
			}
			v, ok := it.Next()
			if !ok {
				return val.NoneVal(), false
			}
			curIdx++
			// Skip step - 1 items
			for skip := int64(1); skip < step; skip++ {
				if stop >= 0 && curIdx >= stop {
					break
				}
				_, okSkip := it.Next()
				if !okSkip {
					break
				}
				curIdx++
			}
			return v, true
		})
	}))

	// 6. product(*iterables, repeat=1)
	mod.Dict.Set(val.InternString("product"), val.NewBuiltinFuncVal("product", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		repeat := 1
		if len(kwnames) > 0 {
			kwStart := len(args) - len(kwnames)
			for i, kn := range kwnames {
				if kn.AsString() == "repeat" && args[kwStart+i].IsInt64() {
					repeat = int(args[kwStart+i].AsInt64())
				}
			}
			args = args[:kwStart]
		}
		pools := make([][]val.Value, 0, len(args)*repeat)
		for r := 0; r < repeat; r++ {
			for _, arg := range args {
				it := iterFor(ctx, arg)
				var pool []val.Value
				for {
					v, ok := it.Next()
					if !ok {
						break
					}
					pool = append(pool, v)
				}
				if len(pool) == 0 {
					return val.NewIterValWithNext(func() (val.Value, bool) { return val.NoneVal(), false })
				}
				pools = append(pools, pool)
			}
		}
		if len(pools) == 0 {
			once := false
			return val.NewIterValWithNext(func() (val.Value, bool) {
				if !once {
					once = true
					return val.NewTupleVal(), true
				}
				return val.NoneVal(), false
			})
		}

		indices := make([]int, len(pools))
		done := false

		return val.NewIterValWithNext(func() (val.Value, bool) {
			if done {
				return val.NoneVal(), false
			}
			res := make([]val.Value, len(pools))
			for i, p := range pools {
				res[i] = p[indices[i]]
			}
			// Advance indices from right
			for i := len(pools) - 1; i >= 0; i-- {
				indices[i]++
				if indices[i] == len(pools[i]) {
					indices[i] = 0
					if i == 0 {
						done = true
					}
				} else {
					break
				}
			}
			return val.NewTupleVal(res...), true
		})
	}))

	// 7. permutations(iterable, r=None)
	mod.Dict.Set(val.InternString("permutations"), val.NewBuiltinFuncVal("permutations", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: permutations() takes at least 1 argument")
		}
		it := iterFor(ctx, args[0])
		var pool []val.Value
		for {
			v, ok := it.Next()
			if !ok {
				break
			}
			pool = append(pool, v)
		}
		n := len(pool)
		r := n
		if len(args) > 1 && !args[1].IsNone() && args[1].IsInt64() {
			r = int(args[1].AsInt64())
		}
		if r > n || r < 0 {
			return val.NewIterValWithNext(func() (val.Value, bool) { return val.NoneVal(), false })
		}

		indices := make([]int, n)
		for i := range indices {
			indices[i] = i
		}
		cycles := make([]int, r)
		for i := range cycles {
			cycles[i] = n - i
		}
		first := true

		return val.NewIterValWithNext(func() (val.Value, bool) {
			if first {
				first = false
				res := make([]val.Value, r)
				for i := 0; i < r; i++ {
					res[i] = pool[indices[i]]
				}
				return val.NewTupleVal(res...), true
			}
			for i := r - 1; i >= 0; i-- {
				cycles[i]--
				if cycles[i] == 0 {
					// Rotate indices[i:] left by 1
					tmp := indices[i]
					copy(indices[i:n-1], indices[i+1:n])
					indices[n-1] = tmp
					cycles[i] = n - i
				} else {
					j := cycles[i]
					indices[i], indices[n-j] = indices[n-j], indices[i]
					res := make([]val.Value, r)
					for k := 0; k < r; k++ {
						res[k] = pool[indices[k]]
					}
					return val.NewTupleVal(res...), true
				}
			}
			return val.NoneVal(), false
		})
	}))

	// 8. combinations(iterable, r)
	mod.Dict.Set(val.InternString("combinations"), val.NewBuiltinFuncVal("combinations", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: combinations() takes exactly 2 arguments")
		}
		it := iterFor(ctx, args[0])
		var pool []val.Value
		for {
			v, ok := it.Next()
			if !ok {
				break
			}
			pool = append(pool, v)
		}
		n := len(pool)
		r := int(args[1].AsInt64())
		if r > n || r < 0 {
			return val.NewIterValWithNext(func() (val.Value, bool) { return val.NoneVal(), false })
		}

		indices := make([]int, r)
		for i := range indices {
			indices[i] = i
		}
		first := true

		return val.NewIterValWithNext(func() (val.Value, bool) {
			if first {
				first = false
				res := make([]val.Value, r)
				for i := 0; i < r; i++ {
					res[i] = pool[indices[i]]
				}
				return val.NewTupleVal(res...), true
			}
			i := r - 1
			for ; i >= 0; i-- {
				if indices[i] != i+n-r {
					break
				}
			}
			if i < 0 {
				return val.NoneVal(), false
			}
			indices[i]++
			for j := i + 1; j < r; j++ {
				indices[j] = indices[j-1] + 1
			}
			res := make([]val.Value, r)
			for k := 0; k < r; k++ {
				res[k] = pool[indices[k]]
			}
			return val.NewTupleVal(res...), true
		})
	}))

	// 9. groupby(iterable, key=None)
	mod.Dict.Set(val.InternString("groupby"), val.NewBuiltinFuncVal("groupby", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: groupby() takes at least 1 argument")
		}
		it := iterFor(ctx, args[0])
		keyFn := val.NoneVal()
		if len(args) > 1 && !args[1].IsNone() {
			keyFn = args[1]
		}
		var currItem val.Value
		var currKey val.Value
		hasItem := false
		done := false

		// Fetch first item
		if v, ok := it.Next(); ok {
			currItem = v
			hasItem = true
			if !keyFn.IsNone() {
				currKey = ctx.Invoke1(keyFn, currItem)
			} else {
				currKey = currItem
			}
		} else {
			done = true
		}

		return val.NewIterValWithNext(func() (val.Value, bool) {
			if done || !hasItem {
				return val.NoneVal(), false
			}
			targetKey := currKey
			var groupItems []val.Value
			groupItems = append(groupItems, currItem)

			for {
				v, ok := it.Next()
				if !ok {
					done = true
					hasItem = false
					break
				}
				var k val.Value
				if !keyFn.IsNone() {
					k = ctx.Invoke1(keyFn, v)
				} else {
					k = v
				}
				if val.RichCompare(ctx, k, targetKey, val.CmpEQ).IsTruthy() {
					groupItems = append(groupItems, v)
				} else {
					currItem = v
					currKey = k
					hasItem = true
					break
				}
			}
			return val.NewTupleVal(targetKey, val.NewIterVal(val.NewListVal(groupItems...))), true
		})
	}))

	// 10. accumulate, compress, dropwhile, takewhile, filterfalse, starmap, pairwise, zip_longest
	mod.Dict.Set(val.InternString("accumulate"), val.NewBuiltinFuncVal("accumulate", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: accumulate() takes at least 1 argument")
		}
		it := iterFor(ctx, args[0])
		fn := val.NoneVal()
		if len(args) > 1 && !args[1].IsNone() {
			fn = args[1]
		}
		var total val.Value
		first := true
		return val.NewIterValWithNext(func() (val.Value, bool) {
			v, ok := it.Next()
			if !ok {
				return val.NoneVal(), false
			}
			if first {
				first = false
				total = v
				return total, true
			}
			if fn.IsNone() {
				total = val.AddCtx(ctx, total, v)
			} else {
				total = ctx.Invoke2(fn, total, v)
			}
			return total, true
		})
	}))

	mod.Dict.Set(val.InternString("compress"), val.NewBuiltinFuncVal("compress", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: compress() takes exactly 2 arguments")
		}
		dataIt := iterFor(ctx, args[0])
		selIt := iterFor(ctx, args[1])
		return val.NewIterValWithNext(func() (val.Value, bool) {
			for {
				d, okD := dataIt.Next()
				s, okS := selIt.Next()
				if !okD || !okS {
					return val.NoneVal(), false
				}
				if s.IsTruthy() {
					return d, true
				}
			}
		})
	}))

	mod.Dict.Set(val.InternString("dropwhile"), val.NewBuiltinFuncVal("dropwhile", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: dropwhile() takes exactly 2 arguments")
		}
		pred := args[0]
		it := iterFor(ctx, args[1])
		dropping := true
		return val.NewIterValWithNext(func() (val.Value, bool) {
			for {
				v, ok := it.Next()
				if !ok {
					return val.NoneVal(), false
				}
				if dropping {
					if ctx.Invoke1(pred, v).IsTruthy() {
						continue
					}
					dropping = false
					return v, true
				}
				return v, true
			}
		})
	}))

	mod.Dict.Set(val.InternString("takewhile"), val.NewBuiltinFuncVal("takewhile", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: takewhile() takes exactly 2 arguments")
		}
		pred := args[0]
		it := iterFor(ctx, args[1])
		done := false
		return val.NewIterValWithNext(func() (val.Value, bool) {
			if done {
				return val.NoneVal(), false
			}
			v, ok := it.Next()
			if !ok {
				return val.NoneVal(), false
			}
			if !ctx.Invoke1(pred, v).IsTruthy() {
				done = true
				return val.NoneVal(), false
			}
			return v, true
		})
	}))

	mod.Dict.Set(val.InternString("filterfalse"), val.NewBuiltinFuncVal("filterfalse", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: filterfalse() takes exactly 2 arguments")
		}
		pred := args[0]
		it := iterFor(ctx, args[1])
		return val.NewIterValWithNext(func() (val.Value, bool) {
			for {
				v, ok := it.Next()
				if !ok {
					return val.NoneVal(), false
				}
				if pred.IsNone() {
					if !v.IsTruthy() {
						return v, true
					}
				} else {
					if !ctx.Invoke1(pred, v).IsTruthy() {
						return v, true
					}
				}
			}
		})
	}))

	mod.Dict.Set(val.InternString("starmap"), val.NewBuiltinFuncVal("starmap", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: starmap() takes exactly 2 arguments")
		}
		fn := args[0]
		it := iterFor(ctx, args[1])
		return val.NewIterValWithNext(func() (val.Value, bool) {
			v, ok := it.Next()
			if !ok {
				return val.NoneVal(), false
			}
			var subArgs []val.Value
			if v.IsTuple() {
				subArgs = v.AsTuple().Items
			} else if v.IsList() {
				subArgs = v.AsList().Items
			} else {
				subIt := iterFor(ctx, v)
				for {
					item, okItem := subIt.Next()
					if !okItem {
						break
					}
					subArgs = append(subArgs, item)
				}
			}
			return ctx.Call(fn, subArgs, nil), true
		})
	}))

	mod.Dict.Set(val.InternString("pairwise"), val.NewBuiltinFuncVal("pairwise", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: pairwise() takes exactly 1 argument")
		}
		it := iterFor(ctx, args[0])
		prev, ok := it.Next()
		if !ok {
			return val.NewIterValWithNext(func() (val.Value, bool) { return val.NoneVal(), false })
		}
		return val.NewIterValWithNext(func() (val.Value, bool) {
			curr, ok := it.Next()
			if !ok {
				return val.NoneVal(), false
			}
			p := prev
			prev = curr
			return val.NewTupleVal(p, curr), true
		})
	}))

	// batched(iterable, n, *, strict=False)
	mod.Dict.Set(val.InternString("batched"), val.NewBuiltinFuncVal("batched", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: batched() missing required arguments")
		}
		it := iterFor(ctx, args[0])
		n := int(args[1].AsInt64())
		if n < 1 {
			panic("ValueError: n must be at least one")
		}
		strict := false
		if v, ok := kwArg(args, kwnames, "strict"); ok {
			strict = v.IsTruthy()
		}
		return val.NewIterValWithNext(func() (val.Value, bool) {
			batch := make([]val.Value, 0, n)
			for i := 0; i < n; i++ {
				item, ok := it.Next()
				if !ok {
					break
				}
				batch = append(batch, item)
			}
			if len(batch) == 0 {
				return val.NoneVal(), false
			}
			if strict && len(batch) < n {
				panic("ValueError: batched(): incomplete batch")
			}
			return val.NewTupleVal(batch...), true
		})
	}))

	// zip_longest(*iterables, fillvalue=None)
	mod.Dict.Set(val.InternString("zip_longest"), val.NewBuiltinFuncVal("zip_longest", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		fillvalue := val.NoneVal()
		if len(kwnames) > 0 {
			kwStart := len(args) - len(kwnames)
			for i, kn := range kwnames {
				if kn.AsString() == "fillvalue" {
					fillvalue = args[kwStart+i]
				}
			}
			args = args[:kwStart]
		}
		if len(args) == 0 {
			return val.NewIterValWithNext(func() (val.Value, bool) { return val.NoneVal(), false })
		}
		iters := make([]*val.PyIter, len(args))
		for i, a := range args {
			iters[i] = iterFor(ctx, a)
		}
		exhausted := make([]bool, len(args))
		activeCount := len(args)

		return val.NewIterValWithNext(func() (val.Value, bool) {
			if activeCount == 0 {
				return val.NoneVal(), false
			}
			res := make([]val.Value, len(iters))
			produced := false
			for i, it := range iters {
				if exhausted[i] {
					res[i] = fillvalue
				} else {
					v, ok := it.Next()
					if ok {
						res[i] = v
						produced = true
					} else {
						exhausted[i] = true
						activeCount--
						res[i] = fillvalue
					}
				}
			}
			if !produced {
				return val.NoneVal(), false
			}
			return val.NewTupleVal(res...), true
		})
	}))

	return modVal
}
