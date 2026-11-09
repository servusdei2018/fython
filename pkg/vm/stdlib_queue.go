package vm

import (
	"sync"
	"time"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	queueOnce       sync.Once
	queueEmptyType  *val.PyType
	simpleQueueType *val.PyType
	queueStateMap   sync.Map // map[*val.PyInstance]*simpleQueueState
)

type simpleQueueState struct {
	mu    sync.Mutex
	cond  *sync.Cond
	items []val.Value
}

func initQueueModule() {
	queueOnce.Do(func() {
		queueEmptyType = val.MustNewType("Empty", []*val.PyType{val.ExceptionType}, nil)

		simpleQueueDict := val.NewDictVal(8).AsDict()
		simpleQueueType = val.MustNewType("SimpleQueue", []*val.PyType{val.ObjectType}, simpleQueueDict)

		getQueueState := func(inst *val.PyInstance) *simpleQueueState {
			if v, ok := queueStateMap.Load(inst); ok {
				return v.(*simpleQueueState)
			}
			st := &simpleQueueState{
				items: make([]val.Value, 0, 16),
			}
			st.cond = sync.NewCond(&st.mu)
			queueStateMap.Store(inst, st)
			return st
		}

		simpleQueueType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			inst := val.NewInstanceOfType(simpleQueueType)
			st := &simpleQueueState{
				items: make([]val.Value, 0, 16),
			}
			st.cond = sync.NewCond(&st.mu)
			queueStateMap.Store(inst.AsInstance(), st)
			return inst
		}

		simpleQueueDict.Set(val.InternString("put"), val.NewBuiltinFuncVal("SimpleQueue.put", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: put() takes at least 1 argument")
			}
			inst := args[0]
			item := args[1]
			st := getQueueState(inst.AsInstance())

			st.mu.Lock()
			st.items = append(st.items, item)
			st.cond.Signal()
			st.mu.Unlock()

			return val.NoneVal()
		}))

		simpleQueueDict.Set(val.InternString("put_nowait"), val.NewBuiltinFuncVal("SimpleQueue.put_nowait", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: put_nowait() takes at least 1 argument")
			}
			inst := args[0]
			item := args[1]
			st := getQueueState(inst.AsInstance())

			st.mu.Lock()
			st.items = append(st.items, item)
			st.cond.Signal()
			st.mu.Unlock()

			return val.NoneVal()
		}))

		simpleQueueDict.Set(val.InternString("get"), val.NewBuiltinFuncVal("SimpleQueue.get", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: get() takes at least 0 arguments")
			}
			inst := args[0]
			st := getQueueState(inst.AsInstance())

			block := true
			var timeout time.Duration = -1

			if len(args) > 1 {
				block = args[1].IsTruthy()
			}
			if len(args) > 2 && !args[2].IsNone() {
				if args[2].IsFloat64() {
					timeout = time.Duration(args[2].AsFloat64() * float64(time.Second))
				} else if args[2].IsInt64() {
					timeout = time.Duration(args[2].AsInt64()) * time.Second
				}
			}

			// Parse keyword arguments
			for i, kw := range kwnames {
				kwStr := kw.AsString()
				valIdx := len(args) - len(kwnames) + i
				v := args[valIdx]
				switch kwStr {
				case "block":
					block = v.IsTruthy()
				case "timeout":
					if !v.IsNone() {
						if v.IsFloat64() {
							timeout = time.Duration(v.AsFloat64() * float64(time.Second))
						} else if v.IsInt64() {
							timeout = time.Duration(v.AsInt64()) * time.Second
						}
					}
				}
			}

			st.mu.Lock()
			defer st.mu.Unlock()

			if !block {
				if len(st.items) == 0 {
					panic(val.NewException(queueEmptyType, "Queue is empty"))
				}
				item := st.items[0]
				st.items = st.items[1:]
				return item
			}

			if timeout < 0 {
				for len(st.items) == 0 {
					st.cond.Wait()
				}
				item := st.items[0]
				st.items = st.items[1:]
				return item
			}

			// Wait with timeout
			deadline := time.Now().Add(timeout)
			for len(st.items) == 0 {
				rem := time.Until(deadline)
				if rem <= 0 {
					panic(val.NewException(queueEmptyType, "Queue is empty"))
				}
				done := make(chan struct{})
				go func() {
					st.mu.Lock()
					defer st.mu.Unlock()
					for len(st.items) == 0 && time.Now().Before(deadline) {
						st.cond.Wait()
					}
					close(done)
				}()
				st.mu.Unlock()
				select {
				case <-done:
					st.mu.Lock()
				case <-time.After(rem):
					st.mu.Lock()
					if len(st.items) == 0 {
						panic(val.NewException(queueEmptyType, "Queue is empty"))
					}
				}
			}

			item := st.items[0]
			st.items = st.items[1:]
			return item
		}))

		simpleQueueDict.Set(val.InternString("get_nowait"), val.NewBuiltinFuncVal("SimpleQueue.get_nowait", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: get_nowait() takes at least 0 arguments")
			}
			inst := args[0]
			st := getQueueState(inst.AsInstance())

			st.mu.Lock()
			defer st.mu.Unlock()

			if len(st.items) == 0 {
				panic(val.NewException(queueEmptyType, "Queue is empty"))
			}
			item := st.items[0]
			st.items = st.items[1:]
			return item
		}))

		simpleQueueDict.Set(val.InternString("empty"), val.NewBuiltinFuncVal("SimpleQueue.empty", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: empty() takes at least 0 arguments")
			}
			st := getQueueState(args[0].AsInstance())
			st.mu.Lock()
			isEmpty := len(st.items) == 0
			st.mu.Unlock()
			return val.BoolVal(isEmpty)
		}))

		simpleQueueDict.Set(val.InternString("qsize"), val.NewBuiltinFuncVal("SimpleQueue.qsize", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: qsize() takes at least 0 arguments")
			}
			st := getQueueState(args[0].AsInstance())
			st.mu.Lock()
			size := len(st.items)
			st.mu.Unlock()
			return val.Int64Val(int64(size))
		}))
	})
}

func (e *Engine) createQueueModule() val.Value {
	initQueueModule()

	modVal := val.NewModuleVal("_queue", "Native queue implementation")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("Empty"), val.TypeVal(queueEmptyType))
	mod.Dict.Set(val.InternString("SimpleQueue"), val.TypeVal(simpleQueueType))

	return modVal
}
