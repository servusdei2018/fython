package vm

import (
	"os"
	"runtime"
	"sync"

	"github.com/servusdei2018/fython/pkg/val"
)

// The native types of the multiprocessing module.
var (
	mpProcessType *val.PyType
	mpQueueType   *val.PyType
	mpLockType    *val.PyType
	mpEventType   *val.PyType
	mpValueType   *val.PyType
	mpArrayType   *val.PyType
	mpPoolType    *val.PyType
	mpContextType *val.PyType
)

// pyProcess is the native payload of a multiprocessing.Process object.
type pyProcess struct {
	inst    val.PyInstance // must be first: Header lives at offset 0
	target  val.Value
	args    []val.Value
	kwargs  *val.PyDict
	done    chan struct{}
	mu      sync.Mutex
	started bool
	closed  bool
	exit    int64
	pid     int64
	name    string
}

// pyQueue is the native payload of a multiprocessing.Queue object.
type pyQueue struct {
	inst val.PyInstance // must be first: Header lives at offset 0
	ch   chan val.Value
	mu   sync.Mutex
}

// pyLock is the native payload of a multiprocessing.Lock object.
type pyLock struct {
	inst val.PyInstance // must be first: Header lives at offset 0
	mu   sync.Mutex
}

// pyEvent is the native payload of a multiprocessing.Event object.
type pyEvent struct {
	inst val.PyInstance // must be first: Header lives at offset 0
	ch   chan struct{}
	once sync.Once
}

// pyShared holds a value that is shared between the processes.
type pyShared struct {
	inst val.PyInstance // must be first: Header lives at offset 0
	val  val.Value
	mu   sync.Mutex
}

// pyPool is the native payload of a multiprocessing.Pool object.
type pyPool struct {
	inst val.PyInstance // must be first: Header lives at offset 0
}

// mpContext is the native payload of a multiprocessing context object.
type mpContext struct {
	inst val.PyInstance // must be first: Header lives at offset 0
}

// initMultiprocessingTypes creates the native types of the module.
func initMultiprocessingTypes() {
	if mpProcessType != nil {
		return
	}

	mpProcessType = val.MustNewType("Process", []*val.PyType{val.ObjectType}, nil)
	mpProcessType.Dict.Set(val.InternString("__module__"), val.InternString("multiprocessing"))
	mpQueueType = val.MustNewType("Queue", []*val.PyType{val.ObjectType}, nil)
	mpQueueType.Dict.Set(val.InternString("__module__"), val.InternString("multiprocessing"))
	mpLockType = val.MustNewType("Lock", []*val.PyType{val.ObjectType}, nil)
	mpLockType.Dict.Set(val.InternString("__module__"), val.InternString("multiprocessing"))
	mpEventType = val.MustNewType("Event", []*val.PyType{val.ObjectType}, nil)
	mpEventType.Dict.Set(val.InternString("__module__"), val.InternString("multiprocessing"))
	mpValueType = val.MustNewType("Value", []*val.PyType{val.ObjectType}, nil)
	mpValueType.Dict.Set(val.InternString("__module__"), val.InternString("multiprocessing"))
	mpArrayType = val.MustNewType("Array", []*val.PyType{val.ObjectType}, nil)
	mpArrayType.Dict.Set(val.InternString("__module__"), val.InternString("multiprocessing"))
	mpPoolType = val.MustNewType("Pool", []*val.PyType{val.ObjectType}, nil)
	mpPoolType.Dict.Set(val.InternString("__module__"), val.InternString("multiprocessing"))
	mpContextType = val.MustNewType("DefaultContext", []*val.PyType{val.ObjectType}, nil)
	mpContextType.Dict.Set(val.InternString("__module__"), val.InternString("multiprocessing"))

	mpProcessType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		p := &pyProcess{inst: val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: mpProcessType}, Shape: mpProcessType.RootShape}}
		p.name = "Process-" + itoa(mpProcessCounter())
		if len(args) > 0 && !args[0].IsNone() {
			p.target = args[0]
		}
		if len(args) > 1 {
			p.args = args[1:]
		}
		for i, k := range kwnames {
			if k.AsString() == "target" && i+1 < len(args) {
				p.target = args[i+1]
			}
			if k.AsString() == "args" && i+1 < len(args) {
				p.args = pySeqItems(args[i+1])
			}
			if k.AsString() == "kwargs" && i+1 < len(args) && args[i+1].IsDict() {
				p.kwargs = args[i+1].AsDict()
			}
			if k.AsString() == "name" && i+1 < len(args) {
				p.name = args[i+1].AsString()
			}
		}
		return val.ObjectVal(&p.inst.Header)
	}

	mpQueueType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		q := &pyQueue{inst: val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: mpQueueType}, Shape: mpQueueType.RootShape}, ch: make(chan val.Value, 1024)}
		return val.ObjectVal(&q.inst.Header)
	}

	mpLockType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		l := &pyLock{inst: val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: mpLockType}, Shape: mpLockType.RootShape}}
		return val.ObjectVal(&l.inst.Header)
	}

	mpEventType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		e := &pyEvent{inst: val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: mpEventType}, Shape: mpEventType.RootShape}, ch: make(chan struct{}, 1)}
		return val.ObjectVal(&e.inst.Header)
	}

	mpValueType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		v := val.Int64Val(0)
		if len(args) > 1 {
			v = args[1]
		}
		s := &pyShared{inst: val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: mpValueType}, Shape: mpValueType.RootShape}, val: v}
		return val.ObjectVal(&s.inst.Header)
	}

	mpArrayType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		s := &pyShared{inst: val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: mpArrayType}, Shape: mpArrayType.RootShape}}
		if len(args) > 1 {
			if items := pySeqItems(args[1]); items != nil {
				s.val = val.NewListVal(items...)
			}
		}
		return val.ObjectVal(&s.inst.Header)
	}

	mpPoolType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		p := &pyPool{inst: val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: mpPoolType}, Shape: mpPoolType.RootShape}}
		return val.ObjectVal(&p.inst.Header)
	}

	mpContextType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		c := &mpContext{inst: val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: mpContextType}, Shape: mpContextType.RootShape}}
		return val.ObjectVal(&c.inst.Header)
	}

	// The process methods.
	mpProcessType.Dict.Set(val.InternString("start"), val.NewBuiltinFuncVal("Process.start", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		p := (*pyProcess)(val.AsPointer(args[0]))
		p.mu.Lock()
		if p.started {
			p.mu.Unlock()
			panic("RuntimeError: process has already started")
		}
		p.started = true
		p.pid = int64(os.Getpid())
		p.done = make(chan struct{})
		target := p.target
		callArgs := p.args
		callKwargs := p.kwargs
		done := p.done
		p.mu.Unlock()
		go func() {
			defer close(done)
			if target.IsNone() {
				return
			}
			kw := make([]val.Value, 0)
			if callKwargs != nil {
				for _, k := range callKwargs.Keys() {
					v, _ := callKwargs.Get(k)
					kw = append(kw, k, v)
				}
			}
			runVM, _ := ctx.VM.(*VM)
			if runVM == nil {
				return
			}
			runVM.Call(target, append(append([]val.Value{}, callArgs...), kw...)...)
		}()
		return val.NoneVal()
	}))

	mpProcessType.Dict.Set(val.InternString("join"), val.NewBuiltinFuncVal("Process.join", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		p := (*pyProcess)(val.AsPointer(args[0]))
		p.mu.Lock()
		done := p.done
		p.mu.Unlock()
		if done != nil {
			<-done
		}
		return val.NoneVal()
	}))

	mpProcessType.Dict.Set(val.InternString("is_alive"), val.NewBuiltinFuncVal("Process.is_alive", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		p := (*pyProcess)(val.AsPointer(args[0]))
		p.mu.Lock()
		defer p.mu.Unlock()
		if !p.started || p.done == nil {
			return val.BoolVal(false)
		}
		select {
		case <-p.done:
			return val.BoolVal(false)
		default:
			return val.BoolVal(true)
		}
	}))

	mpProcessType.Dict.Set(val.InternString("terminate"), val.NewBuiltinFuncVal("Process.terminate", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	mpProcessType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("Process.close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		p := (*pyProcess)(val.AsPointer(args[0]))
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		return val.NoneVal()
	}))

	mpProcessType.Dict.Set(val.InternString("exitcode"), val.NewPropertyVal(
		val.NewBuiltinFuncVal("Process.exitcode", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			p := (*pyProcess)(val.AsPointer(args[0]))
			p.mu.Lock()
			defer p.mu.Unlock()
			if !p.started {
				return val.NoneVal()
			}
			return val.Int64Val(p.exit)
		}),
		val.NoneVal(),
		val.NoneVal(),
	))

	mpProcessType.Dict.Set(val.InternString("pid"), val.NewPropertyVal(
		val.NewBuiltinFuncVal("Process.pid", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			p := (*pyProcess)(val.AsPointer(args[0]))
			p.mu.Lock()
			defer p.mu.Unlock()
			if !p.started {
				return val.NoneVal()
			}
			return val.Int64Val(p.pid)
		}),
		val.NoneVal(),
		val.NoneVal(),
	))

	mpProcessType.Dict.Set(val.InternString("name"), val.NewPropertyVal(
		val.NewBuiltinFuncVal("Process.name", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			p := (*pyProcess)(val.AsPointer(args[0]))
			return val.NewStringVal(p.name)
		}),
		val.NoneVal(),
		val.NoneVal(),
	))

	mpProcessType.Dict.Set(val.InternString("daemon"), val.NewPropertyVal(
		val.NewBuiltinFuncVal("Process.daemon", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.BoolVal(false)
		}),
		val.NoneVal(),
		val.NoneVal(),
	))

	// The queue methods.
	mpQueueType.Dict.Set(val.InternString("put"), val.NewBuiltinFuncVal("Queue.put", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		q := (*pyQueue)(val.AsPointer(args[0]))
		if len(args) < 2 {
			panic("TypeError: put() takes at least 1 argument")
		}
		q.ch <- args[1]
		return val.NoneVal()
	}))

	mpQueueType.Dict.Set(val.InternString("get"), val.NewBuiltinFuncVal("Queue.get", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		q := (*pyQueue)(val.AsPointer(args[0]))
		return <-q.ch
	}))

	mpQueueType.Dict.Set(val.InternString("qsize"), val.NewBuiltinFuncVal("Queue.qsize", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		q := (*pyQueue)(val.AsPointer(args[0]))
		return val.Int64Val(int64(len(q.ch)))
	}))

	mpQueueType.Dict.Set(val.InternString("empty"), val.NewBuiltinFuncVal("Queue.empty", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		q := (*pyQueue)(val.AsPointer(args[0]))
		return val.BoolVal(len(q.ch) == 0)
	}))

	// The lock methods.
	mpLockType.Dict.Set(val.InternString("acquire"), val.NewBuiltinFuncVal("Lock.acquire", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		l := (*pyLock)(val.AsPointer(args[0]))
		l.mu.Lock()
		return val.BoolVal(true)
	}))

	mpLockType.Dict.Set(val.InternString("release"), val.NewBuiltinFuncVal("Lock.release", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		l := (*pyLock)(val.AsPointer(args[0]))
		l.mu.Unlock()
		return val.NoneVal()
	}))

	mpLockType.Dict.Set(val.InternString("locked"), val.NewBuiltinFuncVal("Lock.locked", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(false)
	}))

	mpLockType.Dict.Set(val.InternString("__enter__"), val.NewBuiltinFuncVal("Lock.__enter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		l := (*pyLock)(val.AsPointer(args[0]))
		l.mu.Lock()
		return args[0]
	}))

	mpLockType.Dict.Set(val.InternString("__exit__"), val.NewBuiltinFuncVal("Lock.__exit__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		l := (*pyLock)(val.AsPointer(args[0]))
		l.mu.Unlock()
		return val.NoneVal()
	}))

	// The event methods.
	mpEventType.Dict.Set(val.InternString("set"), val.NewBuiltinFuncVal("Event.set", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		e := (*pyEvent)(val.AsPointer(args[0]))
		e.once.Do(func() { e.ch <- struct{}{} })
		return val.NoneVal()
	}))

	mpEventType.Dict.Set(val.InternString("clear"), val.NewBuiltinFuncVal("Event.clear", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		e := (*pyEvent)(val.AsPointer(args[0]))
		select {
		case <-e.ch:
		default:
		}
		return val.NoneVal()
	}))

	mpEventType.Dict.Set(val.InternString("is_set"), val.NewBuiltinFuncVal("Event.is_set", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		e := (*pyEvent)(val.AsPointer(args[0]))
		select {
		case <-e.ch:
			return val.BoolVal(true)
		default:
			return val.BoolVal(false)
		}
	}))

	mpEventType.Dict.Set(val.InternString("wait"), val.NewBuiltinFuncVal("Event.wait", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		e := (*pyEvent)(val.AsPointer(args[0]))
		select {
		case <-e.ch:
			return val.BoolVal(true)
		default:
			return val.BoolVal(false)
		}
	}))

	// The shared value and array.
	for _, tp := range []*val.PyType{mpValueType, mpArrayType} {
		tp.Dict.Set(val.InternString("value"), val.NewPropertyVal(
			val.NewBuiltinFuncVal("value", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				s := (*pyShared)(val.AsPointer(args[0]))
				s.mu.Lock()
				defer s.mu.Unlock()
				return s.val
			}),
			val.NoneVal(),
			val.NoneVal(),
		))
		tp.Dict.Set(val.InternString("get"), val.NewBuiltinFuncVal("get", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			s := (*pyShared)(val.AsPointer(args[0]))
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.val
		}))
		tp.Dict.Set(val.InternString("set"), val.NewBuiltinFuncVal("set", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			s := (*pyShared)(val.AsPointer(args[0]))
			s.mu.Lock()
			defer s.mu.Unlock()
			s.val = args[1]
			return val.NoneVal()
		}))
	}
	mpArrayType.Dict.Set(val.InternString("__len__"), val.NewBuiltinFuncVal("Array.__len__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		s := (*pyShared)(val.AsPointer(args[0]))
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.val.IsList() {
			return val.Int64Val(int64(len(s.val.AsList().Items)))
		}
		return val.Int64Val(0)
	}))

	// The pool methods.
	mpPoolType.Dict.Set(val.InternString("map"), val.NewBuiltinFuncVal("Pool.map", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		// The pool itself is the first argument of the method.
		if len(args) < 3 {
			panic("TypeError: map() takes at least 2 arguments")
		}
		fn := args[1]
		runVM, _ := ctx.VM.(*VM)
		it := iterFor(ctx, args[2])
		results := make([]val.Value, 0, 8)
		for {
			item, ok := it.Next()
			if !ok {
				break
			}
			results = append(results, runVM.Call(fn, item))
		}
		return val.NewListVal(results...)
	}))

	mpPoolType.Dict.Set(val.InternString("apply"), val.NewBuiltinFuncVal("Pool.apply", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		// The pool itself is the first argument of the method.
		if len(args) < 2 {
			panic("TypeError: apply() takes at least 1 argument")
		}
		runVM, _ := ctx.VM.(*VM)
		if runVM == nil {
			return val.NoneVal()
		}
		// The arguments of the call are passed as a tuple.
		callArgs := pySeqItems(args[2])
		if callArgs == nil {
			callArgs = []val.Value{args[2]}
		}
		return runVM.Call(args[1], callArgs...)
	}))

	mpPoolType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("Pool.close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	mpPoolType.Dict.Set(val.InternString("join"), val.NewBuiltinFuncVal("Pool.join", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	mpPoolType.Dict.Set(val.InternString("terminate"), val.NewBuiltinFuncVal("Pool.terminate", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	for _, tp := range []*val.PyType{mpProcessType, mpQueueType, mpLockType, mpEventType, mpValueType, mpArrayType, mpPoolType, mpContextType} {
		tp.InvalidateVersion()
	}
}

// mpProcessCounter returns a number that makes the process names unique.
func mpProcessCounter() int {
	mpCounterMu.Lock()
	defer mpCounterMu.Unlock()
	mpCounter++
	return mpCounter
}

var (
	mpCounter   int
	mpCounterMu sync.Mutex
)

// createMultiprocessingModule builds the multiprocessing module.
func (e *Engine) createMultiprocessingModule() val.Value {
	initMultiprocessingTypes()
	modVal := val.NewModuleVal("multiprocessing", "process creation and communication")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("Process"), val.TypeVal(mpProcessType))
	mod.Dict.Set(val.InternString("Queue"), val.TypeVal(mpQueueType))
	mod.Dict.Set(val.InternString("SimpleQueue"), val.TypeVal(mpQueueType))
	mod.Dict.Set(val.InternString("Lock"), val.TypeVal(mpLockType))
	mod.Dict.Set(val.InternString("RLock"), val.TypeVal(mpLockType))
	mod.Dict.Set(val.InternString("Event"), val.TypeVal(mpEventType))
	mod.Dict.Set(val.InternString("Value"), val.TypeVal(mpValueType))
	mod.Dict.Set(val.InternString("Array"), val.TypeVal(mpArrayType))
	mod.Dict.Set(val.InternString("Pool"), val.TypeVal(mpPoolType))
	mod.Dict.Set(val.InternString("ProcessError"), val.TypeVal(val.RuntimeErrorType))
	mod.Dict.Set(val.InternString("TimeoutError"), val.TypeVal(val.TimeoutErrorType))

	mod.Dict.Set(val.InternString("cpu_count"), val.NewBuiltinFuncVal("cpu_count", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Int64Val(int64(runtime.NumCPU()))
	}))

	mod.Dict.Set(val.InternString("freeze_support"), val.NewBuiltinFuncVal("freeze_support", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("current_process"), val.NewBuiltinFuncVal("current_process", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		p := &pyProcess{inst: val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: mpProcessType}, Shape: mpProcessType.RootShape}}
		p.name = "MainProcess"
		p.pid = int64(os.Getpid())
		return val.ObjectVal(&p.inst.Header)
	}))

	mod.Dict.Set(val.InternString("active_children"), val.NewBuiltinFuncVal("active_children", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewListVal()
	}))

	mod.Dict.Set(val.InternString("get_start_method"), val.NewBuiltinFuncVal("get_start_method", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewStringVal("spawn")
	}))

	mod.Dict.Set(val.InternString("set_start_method"), val.NewBuiltinFuncVal("set_start_method", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("Pipe"), val.NewBuiltinFuncVal("Pipe", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		q1 := &pyQueue{inst: val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: mpQueueType}, Shape: mpQueueType.RootShape}, ch: make(chan val.Value, 16)}
		q2 := &pyQueue{inst: val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: mpQueueType}, Shape: mpQueueType.RootShape}, ch: q1.ch}
		return val.NewTupleVal(val.ObjectVal(&q1.inst.Header), val.ObjectVal(&q2.inst.Header))
	}))

	mod.Dict.Set(val.InternString("get_context"), val.NewBuiltinFuncVal("get_context", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		c := &mpContext{inst: val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: mpContextType}, Shape: mpContextType.RootShape}}
		return val.ObjectVal(&c.inst.Header)
	}))

	return modVal
}

// createMultiprocessingCoreModule builds the _multiprocessing module.
func (e *Engine) createMultiprocessingCoreModule() val.Value {
	initMultiprocessingTypes()
	modVal := val.NewModuleVal("_multiprocessing", "multiprocessing accelerator")
	mod := modVal.AsModule()
	mod.Dict.Set(val.InternString("Process"), val.TypeVal(mpProcessType))
	mod.Dict.Set(val.InternString("Queue"), val.TypeVal(mpQueueType))
	mod.Dict.Set(val.InternString("Lock"), val.TypeVal(mpLockType))
	mod.Dict.Set(val.InternString("Event"), val.TypeVal(mpEventType))
	mod.Dict.Set(val.InternString("Value"), val.TypeVal(mpValueType))
	mod.Dict.Set(val.InternString("Array"), val.TypeVal(mpArrayType))
	mod.Dict.Set(val.InternString("Pool"), val.TypeVal(mpPoolType))
	return modVal
}
