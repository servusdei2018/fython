package vm

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/servusdei2018/fython/pkg/val"
)

type threadState struct {
	target  val.Value
	args    []val.Value
	kwnames []val.Value
	ident   int64
	daemon  bool
	name    string
	started bool
	alive   bool
	done    chan struct{}
	mu      sync.Mutex
}

var (
	threadLockType  *val.PyType
	threadRLockType *val.PyType
	threadType      *val.PyType
	threadStateMap  sync.Map // map[*val.PyInstance]*threadState
	threadOnce      sync.Once
	threadCounter   int64 = 1
)

func initThreadModule() {
	threadOnce.Do(func() {
		threadLockType = val.MustNewType("LockType", []*val.PyType{val.ObjectType}, nil)
		threadLockType.Dict.Set(val.InternString("acquire"), val.NewBuiltinFuncVal("acquire", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.BoolVal(true)
		}))
		threadLockType.Dict.Set(val.InternString("release"), val.NewBuiltinFuncVal("release", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NoneVal()
		}))
		threadLockType.Dict.Set(val.InternString("locked"), val.NewBuiltinFuncVal("locked", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.BoolVal(false)
		}))
		threadLockType.Dict.Set(val.InternString("__enter__"), val.NewBuiltinFuncVal("__enter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) > 0 {
				return args[0]
			}
			return val.NoneVal()
		}))
		threadLockType.Dict.Set(val.InternString("__exit__"), val.NewBuiltinFuncVal("__exit__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NoneVal()
		}))

		threadRLockType = val.MustNewType("RLock", []*val.PyType{val.ObjectType}, nil)
		threadRLockType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NewInstanceOfType(threadRLockType)
		}
		for _, it := range threadLockType.Dict.Items() {
			threadRLockType.Dict.Set(it.Key, it.Value)
		}

		threadTypeDict := val.NewDictVal(16).AsDict()
		threadType = val.MustNewType("Thread", []*val.PyType{val.ObjectType}, threadTypeDict)
		threadType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			inst := val.NewInstanceOfType(threadType)
			initArgs := append([]val.Value{inst}, args...)
			if initFn, ok := threadTypeDict.Get(val.InternString("__init__")); ok && initFn.IsBuiltinFunc() {
				initFn.AsBuiltinFunc().Fn(ctx, initFn, initArgs, kwnames)
			}
			return inst
		}

		getThreadState := func(self val.Value) *threadState {
			if !self.IsInstance() {
				panic("TypeError: descriptor requires a 'Thread' object")
			}
			if v, ok := threadStateMap.Load(self.AsInstance()); ok {
				return v.(*threadState)
			}
			panic("RuntimeError: Thread.__init__() not called")
		}

		threadTypeDict.Set(val.InternString("__init__"), val.NewBuiltinFuncVal("Thread.__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: Thread.__init__() takes at least 1 argument")
			}
			self := args[0]
			st := &threadState{
				name:   "Thread",
				daemon: false,
				done:   make(chan struct{}),
			}

			// Parse keyword arguments
			for i, kw := range kwnames {
				kwStr := kw.AsString()
				valIdx := len(args) - len(kwnames) + i
				val := args[valIdx]
				switch kwStr {
				case "target":
					st.target = val
				case "name":
					st.name = val.AsString()
				case "daemon":
					st.daemon = val.IsTruthy()
				case "args":
					if val.IsTuple() {
						st.args = append(st.args, val.AsTuple().Items...)
					} else if val.IsList() {
						st.args = append(st.args, val.AsList().Items...)
					}
				case "kwargs":
					if val.IsDict() {
						for _, entry := range val.AsDict().Entries {
							if entry.Active {
								st.kwnames = append(st.kwnames, entry.Key)
								st.args = append(st.args, entry.Value)
							}
						}
					}
				}
			}

			// Positional arguments (excluding self and keyword args)
			numPos := len(args) - len(kwnames)
			if numPos > 1 && st.target.IsNone() {
				st.target = args[1]
			}
			if numPos > 2 && len(st.args) == 0 {
				if args[2].IsTuple() {
					st.args = append(st.args, args[2].AsTuple().Items...)
				} else if args[2].IsList() {
					st.args = append(st.args, args[2].AsList().Items...)
				}
			}

			threadStateMap.Store(self.AsInstance(), st)
			return val.NoneVal()
		}))

		threadTypeDict.Set(val.InternString("start"), val.NewBuiltinFuncVal("start", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			st := getThreadState(args[0])
			st.mu.Lock()
			if st.started {
				st.mu.Unlock()
				panic("RuntimeError: threads can only be started once")
			}
			st.started = true
			st.alive = true
			st.ident = atomic.AddInt64(&threadCounter, 1)
			st.mu.Unlock()

			target := st.target
			callArgs := st.args
			kwNames := st.kwnames
			doneCh := st.done

			go func() {
				defer close(doneCh)
				defer func() {
					st.mu.Lock()
					st.alive = false
					st.mu.Unlock()
				}()
				defer func() {
					_ = recover()
				}()

				if !target.IsNone() {
					threadCtx, release := val.AcquireExecContextHook()
					defer release()
					if tpCall := target.Type().Slots.TpCall; tpCall != nil {
						tpCall(threadCtx, target, callArgs, kwNames)
					}
				}
			}()

			return val.NoneVal()
		}))

		threadTypeDict.Set(val.InternString("join"), val.NewBuiltinFuncVal("join", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			st := getThreadState(args[0])
			if !st.started {
				panic("RuntimeError: cannot join thread before it is started")
			}
			var timeout time.Duration = -1
			if len(args) > 1 && !args[1].IsNone() {
				if args[1].IsFloat64() {
					timeout = time.Duration(args[1].AsFloat64() * float64(time.Second))
				} else if args[1].IsInt64() {
					timeout = time.Duration(args[1].AsInt64()) * time.Second
				}
			}

			if timeout >= 0 {
				select {
				case <-st.done:
				case <-time.After(timeout):
				}
			} else {
				<-st.done
			}
			return val.NoneVal()
		}))

		threadTypeDict.Set(val.InternString("is_alive"), val.NewBuiltinFuncVal("is_alive", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			st := getThreadState(args[0])
			st.mu.Lock()
			defer st.mu.Unlock()
			return val.BoolVal(st.alive)
		}))

		makeProp := func(name string, getter func(st *threadState) val.Value, setter func(st *threadState, v val.Value)) {
			var setFn val.Value = val.NoneVal()
			if setter != nil {
				setFn = val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
					st := getThreadState(args[0])
					setter(st, args[1])
					return val.NoneVal()
				})
			}
			getFn := val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				st := getThreadState(args[0])
				return getter(st)
			})
			threadTypeDict.Set(val.InternString(name), val.NewPropertyVal(getFn, setFn, val.NoneVal()))
		}

		makeProp("ident", func(st *threadState) val.Value {
			if st.ident == 0 {
				return val.NoneVal()
			}
			return val.Int64Val(st.ident)
		}, nil)

		makeProp("daemon", func(st *threadState) val.Value {
			return val.BoolVal(st.daemon)
		}, func(st *threadState, v val.Value) {
			st.daemon = v.IsTruthy()
		})

		makeProp("name", func(st *threadState) val.Value {
			return val.NewStringVal(st.name)
		}, func(st *threadState, v val.Value) {
			st.name = v.AsString()
		})
	})
}

func (e *Engine) createThreadModule() val.Value {
	initThreadModule()
	modVal := val.NewModuleVal("_thread", "")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("LockType"), val.TypeVal(threadLockType))
	mod.Dict.Set(val.InternString("RLock"), val.TypeVal(threadRLockType))
	mod.Dict.Set(val.InternString("allocate_lock"), val.NewBuiltinFuncVal("allocate_lock", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewInstanceOfType(threadLockType)
	}))
	mod.Dict.Set(val.InternString("get_ident"), val.NewBuiltinFuncVal("get_ident", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		id := atomic.LoadInt64(&threadCounter)
		return val.Int64Val(id)
	}))
	mod.Dict.Set(val.InternString("TIMEOUT_MAX"), val.Int64Val(9223372036))
	mod.Dict.Set(val.InternString("error"), val.TypeVal(val.RuntimeErrorType))

	mod.Dict.Set(val.InternString("start_new_thread"), val.NewBuiltinFuncVal("start_new_thread", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: start_new_thread expected at least 2 arguments")
		}
		fn := args[0]
		var callArgs []val.Value
		if args[1].IsTuple() {
			callArgs = args[1].AsTuple().Items
		} else if args[1].IsList() {
			callArgs = args[1].AsList().Items
		}
		var kwNames []val.Value
		if len(args) > 2 && args[2].IsDict() {
			for _, entry := range args[2].AsDict().Entries {
				if entry.Active {
					kwNames = append(kwNames, entry.Key)
					callArgs = append(callArgs, entry.Value)
				}
			}
		}

		thID := atomic.AddInt64(&threadCounter, 1)

		go func() {
			threadCtx, release := val.AcquireExecContextHook()
			defer release()
			defer func() {
				_ = recover()
			}()
			if tpCall := fn.Type().Slots.TpCall; tpCall != nil {
				tpCall(threadCtx, fn, callArgs, kwNames)
			}
		}()

		return val.Int64Val(thID)
	}))

	return modVal
}

func (e *Engine) createThreadingModule() val.Value {
	initThreadModule()
	modVal := val.NewModuleVal("threading", "")
	mod := modVal.AsModule()

	// Event type
	eventTypeDict := val.NewDictVal(8).AsDict()
	eventType := val.MustNewType("Event", []*val.PyType{val.ObjectType}, eventTypeDict)
	type eventState struct {
		flag bool
		cond *sync.Cond
		mu   sync.Mutex
	}
	var eventStateMap sync.Map

	eventType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		inst := val.NewInstanceOfType(eventType)
		es := &eventState{}
		es.cond = sync.NewCond(&es.mu)
		eventStateMap.Store(inst.AsInstance(), es)
		return inst
	}

	getEventState := func(self val.Value) *eventState {
		if !self.IsInstance() {
			panic("TypeError: descriptor requires an 'Event' object")
		}
		if v, ok := eventStateMap.Load(self.AsInstance()); ok {
			return v.(*eventState)
		}
		es := &eventState{}
		es.cond = sync.NewCond(&es.mu)
		eventStateMap.Store(self.AsInstance(), es)
		return es
	}

	eventTypeDict.Set(val.InternString("is_set"), val.NewBuiltinFuncVal("is_set", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		es := getEventState(args[0])
		es.mu.Lock()
		defer es.mu.Unlock()
		return val.BoolVal(es.flag)
	}))
	eventTypeDict.Set(val.InternString("isSet"), eventTypeDict.Items()[len(eventTypeDict.Items())-1].Value)

	eventTypeDict.Set(val.InternString("set"), val.NewBuiltinFuncVal("set", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		es := getEventState(args[0])
		es.mu.Lock()
		es.flag = true
		es.cond.Broadcast()
		es.mu.Unlock()
		return val.NoneVal()
	}))

	eventTypeDict.Set(val.InternString("clear"), val.NewBuiltinFuncVal("clear", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		es := getEventState(args[0])
		es.mu.Lock()
		es.flag = false
		es.mu.Unlock()
		return val.NoneVal()
	}))

	eventTypeDict.Set(val.InternString("wait"), val.NewBuiltinFuncVal("wait", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		es := getEventState(args[0])
		var timeout time.Duration = -1
		if len(args) > 1 && !args[1].IsNone() {
			if args[1].IsFloat64() {
				timeout = time.Duration(args[1].AsFloat64() * float64(time.Second))
			} else if args[1].IsInt64() {
				timeout = time.Duration(args[1].AsInt64()) * time.Second
			}
		}

		es.mu.Lock()
		defer es.mu.Unlock()
		if es.flag {
			return val.BoolVal(true)
		}
		if timeout < 0 {
			for !es.flag {
				es.cond.Wait()
			}
			return val.BoolVal(es.flag)
		}

		// Wait with timeout
		done := make(chan struct{})
		go func() {
			es.mu.Lock()
			for !es.flag {
				es.cond.Wait()
			}
			es.mu.Unlock()
			close(done)
		}()
		select {
		case <-done:
			return val.BoolVal(true)
		case <-time.After(timeout):
			return val.BoolVal(es.flag)
		}
	}))

	mod.Dict.Set(val.InternString("Lock"), val.TypeVal(threadLockType))
	mod.Dict.Set(val.InternString("RLock"), val.TypeVal(threadRLockType))
	mod.Dict.Set(val.InternString("Thread"), val.TypeVal(threadType))
	mod.Dict.Set(val.InternString("Event"), val.TypeVal(eventType))
	mod.Dict.Set(val.InternString("get_ident"), val.NewBuiltinFuncVal("get_ident", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		id := atomic.LoadInt64(&threadCounter)
		return val.Int64Val(id)
	}))
	mod.Dict.Set(val.InternString("TIMEOUT_MAX"), val.Int64Val(9223372036))

	mod.Dict.Set(val.InternString("current_thread"), val.NewBuiltinFuncVal("current_thread", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		inst := val.NewInstanceOfType(threadType)
		st := &threadState{
			name:   "MainThread",
			ident:  atomic.LoadInt64(&threadCounter),
			daemon: false,
		}
		threadStateMap.Store(inst.AsInstance(), st)
		return inst
	}))

	mod.Dict.Set(val.InternString("main_thread"), val.NewBuiltinFuncVal("main_thread", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		inst := val.NewInstanceOfType(threadType)
		st := &threadState{
			name:   "MainThread",
			ident:  1,
			daemon: false,
		}
		threadStateMap.Store(inst.AsInstance(), st)
		return inst
	}))

	mod.Dict.Set(val.InternString("local"), val.TypeVal(val.ObjectType))

	return modVal
}
