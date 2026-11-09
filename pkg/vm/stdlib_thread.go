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

type lockState struct {
	sem chan struct{}
}

type rlockState struct {
	mu    sync.Mutex
	cond  *sync.Cond
	owner int64
	count int
}

type condState struct {
	lock val.Value
	mu   sync.Mutex
	cond *sync.Cond
}

var (
	threadLockType  *val.PyType
	threadRLockType *val.PyType
	threadCondType  *val.PyType
	threadType      *val.PyType
	threadStateMap  sync.Map // map[*val.PyInstance]*threadState
	lockStateMap    sync.Map // map[*val.PyInstance]*lockState
	rlockStateMap   sync.Map // map[*val.PyInstance]*rlockState
	condStateMap    sync.Map // map[*val.PyInstance]*condState
	threadOnce      sync.Once
	threadCounter   int64 = 1
)

func getCurrentThreadIdent(ctx *val.ExecContext) int64 {
	if ctx != nil && ctx.ThreadID != 0 {
		return ctx.ThreadID
	}
	return 1
}

func initThreadModule() {
	threadOnce.Do(func() {
		getLockState := func(self val.Value) *lockState {
			if !self.IsInstance() {
				panic("TypeError: descriptor requires a 'Lock' object")
			}
			inst := self.AsInstance()
			if v, ok := lockStateMap.Load(inst); ok {
				return v.(*lockState)
			}
			st := &lockState{sem: make(chan struct{}, 1)}
			st.sem <- struct{}{}
			lockStateMap.Store(inst, st)
			return st
		}

		threadLockType = val.MustNewType("LockType", []*val.PyType{val.ObjectType}, nil)
		threadLockType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			inst := val.NewInstanceOfType(threadLockType)
			st := &lockState{sem: make(chan struct{}, 1)}
			st.sem <- struct{}{}
			lockStateMap.Store(inst.AsInstance(), st)
			return inst
		}

		threadLockType.Dict.Set(val.InternString("acquire"), val.NewBuiltinFuncVal("acquire", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: acquire() requires at least 0 arguments")
			}
			st := getLockState(args[0])
			blocking := true
			var timeout time.Duration = -1
			if len(args) > 1 {
				blocking = args[1].IsTruthy()
			}
			if len(args) > 2 && !args[2].IsNone() {
				if args[2].IsFloat64() {
					timeout = time.Duration(args[2].AsFloat64() * float64(time.Second))
				} else if args[2].IsInt64() {
					timeout = time.Duration(args[2].AsInt64()) * time.Second
				}
			}
			for i, kw := range kwnames {
				kwStr := kw.AsString()
				valIdx := len(args) - len(kwnames) + i
				v := args[valIdx]
				switch kwStr {
				case "blocking":
					blocking = v.IsTruthy()
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

			if !blocking {
				select {
				case <-st.sem:
					return val.BoolVal(true)
				default:
					return val.BoolVal(false)
				}
			}
			if timeout < 0 {
				<-st.sem
				return val.BoolVal(true)
			}
			select {
			case <-st.sem:
				return val.BoolVal(true)
			case <-time.After(timeout):
				return val.BoolVal(false)
			}
		}))

		threadLockType.Dict.Set(val.InternString("release"), val.NewBuiltinFuncVal("release", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: release() requires at least 0 arguments")
			}
			st := getLockState(args[0])
			select {
			case st.sem <- struct{}{}:
				return val.NoneVal()
			default:
				panic("RuntimeError: release unlocked lock")
			}
		}))

		threadLockType.Dict.Set(val.InternString("locked"), val.NewBuiltinFuncVal("locked", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: locked() requires at least 0 arguments")
			}
			st := getLockState(args[0])
			return val.BoolVal(len(st.sem) == 0)
		}))

		threadLockType.Dict.Set(val.InternString("__enter__"), val.NewBuiltinFuncVal("__enter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: descriptor '__enter__' requires 1 argument")
			}
			self := args[0]
			st := getLockState(self)
			<-st.sem
			return self
		}))

		threadLockType.Dict.Set(val.InternString("__exit__"), val.NewBuiltinFuncVal("__exit__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: descriptor '__exit__' requires at least 1 argument")
			}
			st := getLockState(args[0])
			select {
			case st.sem <- struct{}{}:
				return val.NoneVal()
			default:
				panic("RuntimeError: release unlocked lock")
			}
		}))

		// RLock
		getRLockState := func(self val.Value) *rlockState {
			if !self.IsInstance() {
				panic("TypeError: descriptor requires an 'RLock' object")
			}
			inst := self.AsInstance()
			if v, ok := rlockStateMap.Load(inst); ok {
				return v.(*rlockState)
			}
			st := &rlockState{}
			st.cond = sync.NewCond(&st.mu)
			rlockStateMap.Store(inst, st)
			return st
		}

		threadRLockType = val.MustNewType("RLock", []*val.PyType{val.ObjectType}, nil)
		threadRLockType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			inst := val.NewInstanceOfType(threadRLockType)
			st := &rlockState{}
			st.cond = sync.NewCond(&st.mu)
			rlockStateMap.Store(inst.AsInstance(), st)
			return inst
		}

		threadRLockType.Dict.Set(val.InternString("acquire"), val.NewBuiltinFuncVal("RLock.acquire", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: acquire() requires at least 0 arguments")
			}
			st := getRLockState(args[0])
			ident := getCurrentThreadIdent(ctx)
			blocking := true
			var timeout time.Duration = -1

			if len(args) > 1 {
				blocking = args[1].IsTruthy()
			}
			if len(args) > 2 && !args[2].IsNone() {
				if args[2].IsFloat64() {
					timeout = time.Duration(args[2].AsFloat64() * float64(time.Second))
				} else if args[2].IsInt64() {
					timeout = time.Duration(args[2].AsInt64()) * time.Second
				}
			}

			st.mu.Lock()
			defer st.mu.Unlock()

			if st.owner == ident {
				st.count++
				return val.BoolVal(true)
			}
			if st.owner == 0 {
				st.owner = ident
				st.count = 1
				return val.BoolVal(true)
			}
			if !blocking {
				return val.BoolVal(false)
			}

			if timeout < 0 {
				for st.owner != 0 {
					st.cond.Wait()
				}
				st.owner = ident
				st.count = 1
				return val.BoolVal(true)
			}

			deadline := time.Now().Add(timeout)
			for st.owner != 0 {
				rem := time.Until(deadline)
				if rem <= 0 {
					return val.BoolVal(false)
				}
				done := make(chan struct{})
				go func() {
					st.mu.Lock()
					defer st.mu.Unlock()
					for st.owner != 0 && time.Now().Before(deadline) {
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
					if st.owner != 0 {
						return val.BoolVal(false)
					}
				}
			}
			st.owner = ident
			st.count = 1
			return val.BoolVal(true)
		}))

		threadRLockType.Dict.Set(val.InternString("release"), val.NewBuiltinFuncVal("RLock.release", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: release() requires at least 0 arguments")
			}
			st := getRLockState(args[0])
			ident := getCurrentThreadIdent(ctx)

			st.mu.Lock()
			defer st.mu.Unlock()

			if st.owner != ident {
				panic("RuntimeError: cannot release un-acquired lock")
			}
			st.count--
			if st.count == 0 {
				st.owner = 0
				st.cond.Signal()
			}
			return val.NoneVal()
		}))

		threadRLockType.Dict.Set(val.InternString("__enter__"), val.NewBuiltinFuncVal("__enter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: descriptor '__enter__' requires 1 argument")
			}
			self := args[0]
			st := getRLockState(self)
			ident := getCurrentThreadIdent(ctx)
			st.mu.Lock()
			if st.owner == ident {
				st.count++
				st.mu.Unlock()
				return self
			}
			for st.owner != 0 {
				st.cond.Wait()
			}
			st.owner = ident
			st.count = 1
			st.mu.Unlock()
			return self
		}))

		threadRLockType.Dict.Set(val.InternString("__exit__"), val.NewBuiltinFuncVal("__exit__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: descriptor '__exit__' requires at least 1 argument")
			}
			st := getRLockState(args[0])
			ident := getCurrentThreadIdent(ctx)
			st.mu.Lock()
			if st.owner != ident {
				st.mu.Unlock()
				panic("RuntimeError: cannot release un-acquired lock")
			}
			st.count--
			if st.count == 0 {
				st.owner = 0
				st.cond.Signal()
			}
			st.mu.Unlock()
			return val.NoneVal()
		}))

		// Condition
		getCondState := func(self val.Value) *condState {
			if !self.IsInstance() {
				panic("TypeError: descriptor requires a 'Condition' object")
			}
			inst := self.AsInstance()
			if v, ok := condStateMap.Load(inst); ok {
				return v.(*condState)
			}
			st := &condState{}
			st.cond = sync.NewCond(&st.mu)
			condStateMap.Store(inst, st)
			return st
		}

		threadCondDict := val.NewDictVal(16).AsDict()
		threadCondType = val.MustNewType("Condition", []*val.PyType{val.ObjectType}, threadCondDict)

		threadCondType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			inst := val.NewInstanceOfType(threadCondType)
			st := &condState{}
			st.cond = sync.NewCond(&st.mu)
			var lockVal val.Value
			if len(args) > 0 && !args[0].IsNone() {
				lockVal = args[0]
			} else {
				lockVal = val.NewInstanceOfType(threadRLockType)
				rst := &rlockState{}
				rst.cond = sync.NewCond(&rst.mu)
				rlockStateMap.Store(lockVal.AsInstance(), rst)
			}
			st.lock = lockVal
			condStateMap.Store(inst.AsInstance(), st)
			return inst
		}

		threadCondDict.Set(val.InternString("acquire"), val.NewBuiltinFuncVal("Condition.acquire", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: acquire() requires self")
			}
			st := getCondState(args[0])
			if fn, _, ok := st.lock.Type().LookupMRO(val.InternString("acquire")); ok {
				passArgs := append([]val.Value{st.lock}, args[1:]...)
				if tpCall := fn.Type().Slots.TpCall; tpCall != nil {
					return tpCall(ctx, fn, passArgs, nil)
				}
			}
			return val.BoolVal(true)
		}))

		threadCondDict.Set(val.InternString("release"), val.NewBuiltinFuncVal("Condition.release", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: release() requires self")
			}
			st := getCondState(args[0])
			if fn, _, ok := st.lock.Type().LookupMRO(val.InternString("release")); ok {
				return ctx.Invoke1(fn, st.lock)
			}
			return val.NoneVal()
		}))

		threadCondDict.Set(val.InternString("wait"), val.NewBuiltinFuncVal("Condition.wait", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: wait() requires self")
			}
			st := getCondState(args[0])
			var timeout time.Duration = -1
			if len(args) > 1 && !args[1].IsNone() {
				if args[1].IsFloat64() {
					timeout = time.Duration(args[1].AsFloat64() * float64(time.Second))
				} else if args[1].IsInt64() {
					timeout = time.Duration(args[1].AsInt64()) * time.Second
				}
			}

			// Release lock
			if relFn, _, ok := st.lock.Type().LookupMRO(val.InternString("release")); ok {
				ctx.Invoke1(relFn, st.lock)
			}
			defer func() {
				// Re-acquire lock
				if acqFn, _, ok := st.lock.Type().LookupMRO(val.InternString("acquire")); ok {
					ctx.Invoke1(acqFn, st.lock)
				}
			}()

			st.mu.Lock()
			if timeout < 0 {
				st.cond.Wait()
				st.mu.Unlock()
				return val.BoolVal(true)
			}

			done := make(chan struct{})
			go func() {
				st.mu.Lock()
				st.cond.Wait()
				st.mu.Unlock()
				close(done)
			}()
			st.mu.Unlock()

			select {
			case <-done:
				return val.BoolVal(true)
			case <-time.After(timeout):
				return val.BoolVal(false)
			}
		}))

		threadCondDict.Set(val.InternString("notify"), val.NewBuiltinFuncVal("Condition.notify", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: notify() requires self")
			}
			st := getCondState(args[0])
			st.mu.Lock()
			st.cond.Broadcast()
			st.mu.Unlock()
			return val.NoneVal()
		}))

		threadCondDict.Set(val.InternString("notify_all"), threadCondDict.Items()[len(threadCondDict.Items())-1].Value)
		threadCondDict.Set(val.InternString("notifyAll"), threadCondDict.Items()[len(threadCondDict.Items())-1].Value)

		threadCondDict.Set(val.InternString("__enter__"), val.NewBuiltinFuncVal("Condition.__enter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: descriptor '__enter__' requires 1 argument")
			}
			self := args[0]
			st := getCondState(self)
			if fn, _, ok := st.lock.Type().LookupMRO(val.InternString("acquire")); ok {
				ctx.Invoke1(fn, st.lock)
			}
			return self
		}))

		threadCondDict.Set(val.InternString("__exit__"), val.NewBuiltinFuncVal("Condition.__exit__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: descriptor '__exit__' requires at least 1 argument")
			}
			st := getCondState(args[0])
			if fn, _, ok := st.lock.Type().LookupMRO(val.InternString("release")); ok {
				ctx.Invoke1(fn, st.lock)
			}
			return val.NoneVal()
		}))

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
			val.SetMultiThreaded(true)
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
			ident := st.ident

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
					threadCtx.ThreadID = ident
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
			setFn := val.NoneVal()
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

		val.SetMultiThreaded(true)
		thID := atomic.AddInt64(&threadCounter, 1)

		go func() {
			threadCtx, release := val.AcquireExecContextHook()
			defer release()
			threadCtx.ThreadID = thID
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

	// Semaphore & BoundedSemaphore
	type semaphoreState struct {
		value     int64
		maxValue  int64
		isBounded bool
		mu        sync.Mutex
		cond      *sync.Cond
	}
	var semStateMap sync.Map

	semTypeDict := val.NewDictVal(8).AsDict()
	semType := val.MustNewType("Semaphore", []*val.PyType{val.ObjectType}, semTypeDict)
	semType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		inst := val.NewInstanceOfType(semType)
		var v int64 = 1
		if len(args) > 0 && args[0].IsInt64() {
			v = args[0].AsInt64()
		}
		if v < 0 {
			panic("ValueError: semaphore initial value must be >= 0")
		}
		st := &semaphoreState{value: v}
		st.cond = sync.NewCond(&st.mu)
		semStateMap.Store(inst.AsInstance(), st)
		return inst
	}

	getSemState := func(self val.Value) *semaphoreState {
		if !self.IsInstance() {
			panic("TypeError: descriptor requires a 'Semaphore' object")
		}
		if v, ok := semStateMap.Load(self.AsInstance()); ok {
			return v.(*semaphoreState)
		}
		st := &semaphoreState{value: 1}
		st.cond = sync.NewCond(&st.mu)
		semStateMap.Store(self.AsInstance(), st)
		return st
	}

	semTypeDict.Set(val.InternString("acquire"), val.NewBuiltinFuncVal("acquire", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getSemState(args[0])
		blocking := true
		var timeout time.Duration = -1
		if len(args) > 1 {
			blocking = args[1].IsTruthy()
		}
		if len(args) > 2 && !args[2].IsNone() {
			if args[2].IsFloat64() {
				timeout = time.Duration(args[2].AsFloat64() * float64(time.Second))
			} else if args[2].IsInt64() {
				timeout = time.Duration(args[2].AsInt64()) * time.Second
			}
		}

		st.mu.Lock()
		defer st.mu.Unlock()
		if !blocking {
			if st.value > 0 {
				st.value--
				return val.BoolVal(true)
			}
			return val.BoolVal(false)
		}
		if timeout < 0 {
			for st.value <= 0 {
				st.cond.Wait()
			}
			st.value--
			return val.BoolVal(true)
		}

		done := make(chan struct{})
		go func() {
			st.mu.Lock()
			for st.value <= 0 {
				st.cond.Wait()
			}
			st.mu.Unlock()
			close(done)
		}()
		select {
		case <-done:
			st.value--
			return val.BoolVal(true)
		case <-time.After(timeout):
			return val.BoolVal(false)
		}
	}))

	semTypeDict.Set(val.InternString("release"), val.NewBuiltinFuncVal("release", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getSemState(args[0])
		var n int64 = 1
		if len(args) > 1 && args[1].IsInt64() {
			n = args[1].AsInt64()
		}
		st.mu.Lock()
		if st.isBounded && st.value+n > st.maxValue {
			st.mu.Unlock()
			panic("ValueError: Semaphore released too many times")
		}
		st.value += n
		for range n {
			st.cond.Signal()
		}
		st.mu.Unlock()
		return val.NoneVal()
	}))

	semTypeDict.Set(val.InternString("__enter__"), val.NewBuiltinFuncVal("__enter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getSemState(args[0])
		st.mu.Lock()
		for st.value <= 0 {
			st.cond.Wait()
		}
		st.value--
		st.mu.Unlock()
		return args[0]
	}))

	semTypeDict.Set(val.InternString("__exit__"), val.NewBuiltinFuncVal("__exit__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getSemState(args[0])
		st.mu.Lock()
		st.value++
		st.cond.Signal()
		st.mu.Unlock()
		return val.NoneVal()
	}))

	bsemTypeDict := val.NewDictVal(8).AsDict()
	bsemType := val.MustNewType("BoundedSemaphore", []*val.PyType{semType}, bsemTypeDict)
	bsemType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		inst := val.NewInstanceOfType(bsemType)
		var v int64 = 1
		if len(args) > 0 && args[0].IsInt64() {
			v = args[0].AsInt64()
		}
		if v < 0 {
			panic("ValueError: semaphore initial value must be >= 0")
		}
		st := &semaphoreState{value: v, maxValue: v, isBounded: true}
		st.cond = sync.NewCond(&st.mu)
		semStateMap.Store(inst.AsInstance(), st)
		return inst
	}

	mod.Dict.Set(val.InternString("Lock"), val.TypeVal(threadLockType))
	mod.Dict.Set(val.InternString("RLock"), val.TypeVal(threadRLockType))
	mod.Dict.Set(val.InternString("Condition"), val.TypeVal(threadCondType))
	mod.Dict.Set(val.InternString("Thread"), val.TypeVal(threadType))
	mod.Dict.Set(val.InternString("Event"), val.TypeVal(eventType))
	mod.Dict.Set(val.InternString("Semaphore"), val.TypeVal(semType))
	mod.Dict.Set(val.InternString("BoundedSemaphore"), val.TypeVal(bsemType))
	mod.Dict.Set(val.InternString("_register_atexit"), val.NewBuiltinFuncVal("_register_atexit", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))
	mod.Dict.Set(val.InternString("get_ident"), val.NewBuiltinFuncVal("get_ident", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		id := getCurrentThreadIdent(ctx)
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

	defaultExcepthook := val.NewBuiltinFuncVal("excepthook", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	})
	mod.Dict.Set(val.InternString("excepthook"), defaultExcepthook)

	exceptHookArgsType := val.MustNewType("ExceptHookArgs", []*val.PyType{val.ObjectType}, nil)
	exceptHookArgsType.Dict.Set(val.InternString("__init__"), val.NewBuiltinFuncVal("__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			return val.NoneVal()
		}
		self := args[0]
		if self.IsInstance() {
			inst := self.AsInstance()
			fieldNames := []string{"exc_type", "exc_value", "exc_traceback", "thread"}
			posArgs := args[1:]
			if len(kwnames) > 0 {
				posArgs = args[1 : len(args)-len(kwnames)]
				kwStart := len(args) - len(kwnames)
				for i, kn := range kwnames {
					inst.SetAttr(kn, args[kwStart+i])
				}
			}
			for i, arg := range posArgs {
				if i < len(fieldNames) {
					inst.SetAttr(val.InternString(fieldNames[i]), arg)
				}
			}
		}
		return val.NoneVal()
	}))
	exceptHookArgsType.SyncDunderSlots()
	mod.Dict.Set(val.InternString("ExceptHookArgs"), val.TypeVal(exceptHookArgsType))

	return modVal
}
