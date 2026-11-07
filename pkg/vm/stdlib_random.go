package vm

import (
	"math/big"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	randomClassType *val.PyType
	randomClassOnce sync.Once
)

type pyRandom struct {
	inst val.PyInstance // must be first: Header lives at offset 0
	rng  *rand.Rand
	mu   sync.Mutex
}

func initRandomClassType() {
	randomClassOnce.Do(func() {
		t := val.MustNewType("Random", []*val.PyType{val.ObjectType}, nil)
		t.Dict.Set(val.InternString("__module__"), val.InternString("_random"))

		t.Dict.Set(val.InternString("__new__"), val.NewBuiltinFuncVal("__new__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			cls := t
			if len(args) > 0 && args[0].IsType() {
				cls = args[0].AsType()
			}
			pcg := rand.NewPCG(uint64(time.Now().UnixNano()), uint64(time.Now().UnixNano()>>32))
			r := &pyRandom{
				inst: val.PyInstance{
					Header: val.ObjectHeader{Kind: val.KindInstance, Type: cls},
					Shape:  cls.RootShape,
				},
				rng: rand.New(pcg),
			}
			return val.ObjectVal(&r.inst.Header)
		}))

		t.Dict.Set(val.InternString("__init__"), val.NewBuiltinFuncVal("__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) > 1 {
				seedFn, ok := t.Dict.Get(val.InternString("seed"))
				if ok && seedFn.IsBuiltinFunc() {
					seedFn.AsBuiltinFunc().Fn(ctx, seedFn, args, kwnames)
				}
			}
			return val.NoneVal()
		}))

		t.Dict.Set(val.InternString("seed"), val.NewBuiltinFuncVal("seed", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: descriptor 'seed' of 'Random' object needs an argument")
			}
			r := (*pyRandom)(val.AsPointer(args[0]))
			var s1, s2 uint64
			if len(args) > 1 && !args[1].IsNone() {
				if args[1].IsInt64() {
					s1 = uint64(args[1].AsInt64())
					s2 = s1 ^ 0x5555555555555555
				} else if args[1].IsBigInt() {
					bi := args[1].AsBigInt()
					s1 = bi.Val.Uint64()
					s2 = s1 ^ 0x5555555555555555
				} else {
					s1 = uint64(time.Now().UnixNano())
					s2 = uint64(time.Now().UnixNano() >> 32)
				}
			} else {
				s1 = uint64(time.Now().UnixNano())
				s2 = uint64(time.Now().UnixNano() >> 32)
			}
			pcg := rand.NewPCG(s1, s2)
			r.mu.Lock()
			r.rng = rand.New(pcg)
			r.mu.Unlock()
			return val.NoneVal()
		}))

		t.Dict.Set(val.InternString("random"), val.NewBuiltinFuncVal("random", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: descriptor 'random' of 'Random' object needs an argument")
			}
			r := (*pyRandom)(val.AsPointer(args[0]))
			r.mu.Lock()
			if r.rng == nil {
				pcg := rand.NewPCG(uint64(time.Now().UnixNano()), uint64(time.Now().UnixNano()>>32))
				r.rng = rand.New(pcg)
			}
			res := r.rng.Float64()
			r.mu.Unlock()
			return val.Float64Val(res)
		}))

		t.Dict.Set(val.InternString("getrandbits"), val.NewBuiltinFuncVal("getrandbits", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 || !args[1].IsInt64() {
				panic("TypeError: getrandbits() takes an integer argument")
			}
			k := int(args[1].AsInt64())
			if k <= 0 {
				panic("ValueError: number of bits must be greater than zero")
			}
			r := (*pyRandom)(val.AsPointer(args[0]))
			r.mu.Lock()
			if r.rng == nil {
				pcg := rand.NewPCG(uint64(time.Now().UnixNano()), uint64(time.Now().UnixNano()>>32))
				r.rng = rand.New(pcg)
			}
			if k <= 63 {
				mask := (uint64(1) << k) - 1
				valBits := r.rng.Uint64() & mask
				r.mu.Unlock()
				return val.Int64Val(int64(valBits))
			}
			numBytes := (k + 7) / 8
			bytes := make([]byte, numBytes)
			for i := range bytes {
				bytes[i] = byte(r.rng.Uint32())
			}
			r.mu.Unlock()
			excess := numBytes*8 - k
			if excess > 0 {
				bytes[0] &= (0xFF >> excess)
			}
			bi := new(big.Int).SetBytes(bytes)
			return val.NewBigIntVal(bi)
		}))

		t.InvalidateVersion()
		randomClassType = t
	})
}

func (e *Engine) createRandomModule() val.Value {
	initRandomClassType()
	modVal := val.NewModuleVal("_random", "")
	mod := modVal.AsModule()
	mod.Dict.Set(val.InternString("Random"), val.TypeVal(randomClassType))
	return modVal
}
