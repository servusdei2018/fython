package vm

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	StreamReaderType      *val.PyType
	StreamWriterType      *val.PyType
	ServerType            *val.PyType
	CancelledErrorType    *val.PyType
	InvalidStateErrorType *val.PyType
	TimeoutErrorType      *val.PyType
	asyncioInitOnce       sync.Once
)

type pyStreamReader struct {
	inst   val.PyInstance
	conn   net.Conn
	reader *bufio.Reader
	mu     sync.Mutex
	closed bool
}

type pyStreamWriter struct {
	inst   val.PyInstance
	conn   net.Conn
	buf    []byte
	mu     sync.Mutex
	closed bool
}

type pyServer struct {
	inst     val.PyInstance
	ln       net.Listener
	sockets  val.Value
	closed   bool
	mu       sync.Mutex
	doneChan chan struct{}
}

func initAsyncIOTypes() {
	asyncioInitOnce.Do(func() {
		StreamReaderType = val.MustNewType("StreamReader", []*val.PyType{val.ObjectType}, nil)

		// StreamReader.read(n=-1) -> returns awaitable Future
		StreamReaderType.Dict.Set(val.InternString("read"), val.NewBuiltinFuncVal("read", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: read() takes at least 1 argument")
			}
			sr := (*pyStreamReader)(val.AsPointer(args[0]))
			n := -1
			if len(args) >= 2 && args[1].IsInt64() {
				n = int(args[1].AsInt64())
			}

			futVal := val.NewFutureVal()
			fut := futVal.AsFuture()

			go func() {
				sr.mu.Lock()
				defer sr.mu.Unlock()

				if sr.closed {
					fut.SetResult(val.NewBytesVal(nil))
					return
				}

				if n > 0 {
					buf := make([]byte, n)
					nr, err := io.ReadAtLeast(sr.reader, buf, 1)
					if err != nil && err != io.EOF {
						fut.SetException(val.NewException(val.RuntimeErrorType, err.Error()))
					} else {
						fut.SetResult(val.NewBytesVal(buf[:nr]))
					}
				} else {
					buf := make([]byte, 4096)
					nr, err := sr.reader.Read(buf)
					if err != nil && err != io.EOF {
						fut.SetException(val.NewException(val.RuntimeErrorType, err.Error()))
					} else {
						fut.SetResult(val.NewBytesVal(buf[:nr]))
					}
				}
			}()

			return futVal
		}))

		// StreamReader.readline() -> returns awaitable Future
		StreamReaderType.Dict.Set(val.InternString("readline"), val.NewBuiltinFuncVal("readline", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: readline() takes at least 1 argument")
			}
			sr := (*pyStreamReader)(val.AsPointer(args[0]))
			futVal := val.NewFutureVal()
			fut := futVal.AsFuture()

			go func() {
				sr.mu.Lock()
				defer sr.mu.Unlock()

				line, err := sr.reader.ReadBytes('\n')
				if err != nil && err != io.EOF {
					fut.SetException(val.NewException(val.RuntimeErrorType, err.Error()))
				} else {
					fut.SetResult(val.NewBytesVal(line))
				}
			}()

			return futVal
		}))

		// StreamReader.readexactly(n) -> returns awaitable Future
		StreamReaderType.Dict.Set(val.InternString("readexactly"), val.NewBuiltinFuncVal("readexactly", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: readexactly() takes at least 1 argument")
			}
			sr := (*pyStreamReader)(val.AsPointer(args[0]))
			n := int(args[1].AsInt64())
			futVal := val.NewFutureVal()
			fut := futVal.AsFuture()

			go func() {
				sr.mu.Lock()
				defer sr.mu.Unlock()

				buf := make([]byte, n)
				_, err := io.ReadFull(sr.reader, buf)
				if err != nil {
					fut.SetException(val.NewException(val.RuntimeErrorType, err.Error()))
				} else {
					fut.SetResult(val.NewBytesVal(buf))
				}
			}()

			return futVal
		}))

		StreamWriterType = val.MustNewType("StreamWriter", []*val.PyType{val.ObjectType}, nil)

		// StreamWriter.write(data)
		StreamWriterType.Dict.Set(val.InternString("write"), val.NewBuiltinFuncVal("write", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: write() takes exactly 1 argument")
			}
			sw := (*pyStreamWriter)(val.AsPointer(args[0]))
			sw.mu.Lock()
			defer sw.mu.Unlock()

			var data []byte
			if args[1].IsBytes() {
				data = args[1].AsBytes().Data
			} else if args[1].IsByteArray() {
				data = args[1].AsByteArray().Data
			} else if args[1].IsMemoryView() {
				data = args[1].AsMemoryView().Buf
			} else {
				panic("TypeError: a bytes-like object is required")
			}

			sw.buf = append(sw.buf, data...)
			return val.NoneVal()
		}))

		// StreamWriter.drain() -> flushes buffer to net.Conn
		StreamWriterType.Dict.Set(val.InternString("drain"), val.NewBuiltinFuncVal("drain", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: drain() takes at least 1 argument")
			}
			sw := (*pyStreamWriter)(val.AsPointer(args[0]))
			sw.mu.Lock()

			if len(sw.buf) == 0 {
				sw.mu.Unlock()
				// Fast-path: already drained!
				futVal := val.NewFutureVal()
				futVal.AsFuture().SetResult(val.NoneVal())
				return futVal
			}

			toWrite := sw.buf
			sw.buf = nil
			sw.mu.Unlock()

			futVal := val.NewFutureVal()
			fut := futVal.AsFuture()

			go func() {
				_, err := sw.conn.Write(toWrite)
				if err != nil {
					fut.SetException(val.NewException(val.RuntimeErrorType, err.Error()))
				} else {
					fut.SetResult(val.NoneVal())
				}
			}()

			return futVal
		}))

		// StreamWriter.close()
		StreamWriterType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: close() takes at least 1 argument")
			}
			sw := (*pyStreamWriter)(val.AsPointer(args[0]))
			sw.mu.Lock()
			defer sw.mu.Unlock()
			if !sw.closed {
				sw.closed = true
				if sw.conn != nil {
					_ = sw.conn.Close()
				}
			}
			return val.NoneVal()
		}))

		// StreamWriter.wait_closed()
		StreamWriterType.Dict.Set(val.InternString("wait_closed"), val.NewBuiltinFuncVal("wait_closed", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			futVal := val.NewFutureVal()
			futVal.AsFuture().SetResult(val.NoneVal())
			return futVal
		}))

		// StreamWriter.get_extra_info(name)
		StreamWriterType.Dict.Set(val.InternString("get_extra_info"), val.NewBuiltinFuncVal("get_extra_info", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				return val.NoneVal()
			}
			sw := (*pyStreamWriter)(val.AsPointer(args[0]))
			key := args[1].AsString()
			if key == "peername" && sw.conn != nil {
				if rAddr, ok := sw.conn.RemoteAddr().(*net.TCPAddr); ok {
					return val.NewTupleVal(val.NewStringVal(rAddr.IP.String()), val.Int64Val(int64(rAddr.Port)))
				}
			}
			if key == "sockname" && sw.conn != nil {
				if lAddr, ok := sw.conn.LocalAddr().(*net.TCPAddr); ok {
					return val.NewTupleVal(val.NewStringVal(lAddr.IP.String()), val.Int64Val(int64(lAddr.Port)))
				}
			}
			return val.NoneVal()
		}))

		// Server type
		ServerType = val.MustNewType("Server", []*val.PyType{val.ObjectType}, nil)

		ServerType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: close() takes at least 1 argument")
			}
			srv := (*pyServer)(val.AsPointer(args[0]))
			srv.mu.Lock()
			defer srv.mu.Unlock()
			if !srv.closed {
				srv.closed = true
				if srv.ln != nil {
					_ = srv.ln.Close()
				}
				close(srv.doneChan)
			}
			return val.NoneVal()
		}))

		ServerType.Dict.Set(val.InternString("wait_closed"), val.NewBuiltinFuncVal("wait_closed", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: wait_closed() takes at least 1 argument")
			}
			srv := (*pyServer)(val.AsPointer(args[0]))
			futVal := val.NewFutureVal()
			fut := futVal.AsFuture()
			go func() {
				<-srv.doneChan
				fut.SetResult(val.NoneVal())
			}()
			return futVal
		}))

		ServerType.Dict.Set(val.InternString("serve_forever"), val.NewBuiltinFuncVal("serve_forever", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: serve_forever() takes at least 1 argument")
			}
			srv := (*pyServer)(val.AsPointer(args[0]))
			futVal := val.NewFutureVal()
			fut := futVal.AsFuture()
			go func() {
				<-srv.doneChan
				fut.SetResult(val.NoneVal())
			}()
			return futVal
		}))

		// Exceptions
		CancelledErrorType = val.MustNewType("CancelledError", []*val.PyType{val.BaseExceptionType}, nil)
		InvalidStateErrorType = val.MustNewType("InvalidStateError", []*val.PyType{val.ExceptionType}, nil)
		TimeoutErrorType = val.MustNewType("TimeoutError", []*val.PyType{val.ExceptionType}, nil)
	})
}

func newPyStreamReader(conn net.Conn) val.Value {
	sr := &pyStreamReader{
		inst: val.PyInstance{
			Header: val.ObjectHeader{Kind: val.KindInstance, Type: StreamReaderType},
			Shape:  StreamReaderType.RootShape,
		},
		conn:   conn,
		reader: bufio.NewReader(conn),
	}
	return val.ObjectVal(&sr.inst.Header)
}

func newPyStreamWriter(conn net.Conn) val.Value {
	sw := &pyStreamWriter{
		inst: val.PyInstance{
			Header: val.ObjectHeader{Kind: val.KindInstance, Type: StreamWriterType},
			Shape:  StreamWriterType.RootShape,
		},
		conn: conn,
		buf:  make([]byte, 0, 1024),
	}
	return val.ObjectVal(&sw.inst.Header)
}

func (e *Engine) createAsyncioModule() val.Value {
	initAsyncIOTypes()

	modVal := val.NewModuleVal("asyncio", "")
	mod := modVal.AsModule()

	// Classes
	mod.Dict.Set(val.InternString("Future"), val.TypeVal(val.FutureType))
	mod.Dict.Set(val.InternString("Task"), val.TypeVal(val.TaskType))
	mod.Dict.Set(val.InternString("StreamReader"), val.TypeVal(StreamReaderType))
	mod.Dict.Set(val.InternString("StreamWriter"), val.TypeVal(StreamWriterType))
	mod.Dict.Set(val.InternString("Server"), val.TypeVal(ServerType))

	// Exceptions
	mod.Dict.Set(val.InternString("CancelledError"), val.TypeVal(CancelledErrorType))
	mod.Dict.Set(val.InternString("InvalidStateError"), val.TypeVal(InvalidStateErrorType))
	mod.Dict.Set(val.InternString("TimeoutError"), val.TypeVal(TimeoutErrorType))

	// coroutines submodule
	coroSubmodVal := val.NewModuleVal("asyncio.coroutines", "")
	isCoroSentinel := val.NewInstanceOfType(val.ObjectType)
	coroSubmodVal.AsModule().Dict.Set(val.InternString("_is_coroutine"), isCoroSentinel)
	mod.Dict.Set(val.InternString("coroutines"), coroSubmodVal)
	if e != nil && e.sharedModules != nil {
		e.sharedModules.Set(val.InternString("asyncio.coroutines"), coroSubmodVal)
	}

	mod.Dict.Set(val.InternString("iscoroutinefunction"), val.NewBuiltinFuncVal("iscoroutinefunction", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.BoolVal(false)
		}
		target := args[0]
		if target.IsInstance() {
			if v, ok := target.AsInstance().GetAttr(val.InternString("_is_coroutine")); ok && !v.IsNone() {
				return val.BoolVal(true)
			}
			if v, ok := target.AsInstance().GetAttr(val.InternString("_is_coroutine_marker")); ok && !v.IsNone() {
				return val.BoolVal(true)
			}
		}
		co := callableCode(args)
		return val.BoolVal(co != nil && co.IsCoroutine && !co.IsGenerator)
	}))

	// asyncio.run(coro)
	mod.Dict.Set(val.InternString("run"), val.NewBuiltinFuncVal("run", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: run() takes at least 1 argument")
		}
		target := args[0]
		var parent *val.PyContext
		if ctx != nil && ctx.VM != nil {
			parent = ctx.VM.(*VM).ActiveContext()
		}
		loop := NewEventLoop(e, e.asyncMode)
		res, err := loop.RunUntilCompleteIn(target, parent)
		if err != nil {
			panic(err.Error())
		}
		return res
	}))

	// asyncio.sleep(delay, result=None)
	mod.Dict.Set(val.InternString("sleep"), val.NewBuiltinFuncVal("sleep", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: sleep() takes at least 1 argument")
		}
		var secs float64
		if args[0].IsFloat64() {
			secs = args[0].AsFloat64()
		} else if args[0].IsInt64() {
			secs = float64(args[0].AsInt64())
		}
		result := val.NoneVal()
		if len(args) >= 2 {
			result = args[1]
		}

		futVal := val.NewFutureVal()
		fut := futVal.AsFuture()

		if secs <= 0 {
			fut.SetResult(result)
			return futVal
		}

		time.AfterFunc(time.Duration(secs*float64(time.Second)), func() {
			fut.SetResult(result)
		})
		return futVal
	}))

	// asyncio.create_task(coro)
	mod.Dict.Set(val.InternString("create_task"), val.NewBuiltinFuncVal("create_task", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			if ctx != nil {
				ctx.Raise(val.NewTypeError("create_task() takes at least 1 argument"))
				return val.NoneVal()
			}
			panic("TypeError: create_task() takes at least 1 argument")
		}
		loop := GetCurrentEventLoop()
		if loop == nil {
			loop = NewEventLoop(e, e.asyncMode)
			SetCurrentEventLoop(loop)
		}
		var parent *val.PyContext
		if ctx != nil && ctx.VM != nil {
			parent = ctx.VM.(*VM).ActiveContext()
		}
		task := loop.CreateTaskIn(args[0], parent)
		return val.ObjectVal(&task.Header)
	}))

	// asyncio.ensure_future(coro_or_future, *, loop=None)
	mod.Dict.Set(val.InternString("ensure_future"), val.NewBuiltinFuncVal("ensure_future", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			if ctx != nil {
				ctx.Raise(val.NewTypeError("ensure_future() takes at least 1 argument"))
				return val.NoneVal()
			}
			panic("TypeError: ensure_future() takes at least 1 argument")
		}
		target := args[0]
		if target.IsFuture() || target.IsTask() {
			return target
		}
		if target.IsCoroutine() {
			loop := GetCurrentEventLoop()
			if loop == nil {
				loop = NewEventLoop(e, e.asyncMode)
				SetCurrentEventLoop(loop)
			}
			var parent *val.PyContext
			if ctx != nil && ctx.VM != nil {
				parent = ctx.VM.(*VM).ActiveContext()
			}
			task := loop.CreateTaskIn(target, parent)
			return val.ObjectVal(&task.Header)
		}
		awaitMethod := val.GetAttrCtx(ctx, target, val.InternString("__await__"))
		if ctx != nil && ctx.HasException() {
			ctx.ClearException()
		} else if !awaitMethod.IsUnbound() && !awaitMethod.IsNone() {
			var coro val.Value
			if ctx != nil && ctx.VM != nil {
				vm := ctx.VM.(*VM)
				coro = vm.Vectorcall(awaitMethod, nil, nil)
			}
			if ctx != nil && ctx.HasException() {
				return val.NoneVal()
			}
			if coro.IsFuture() || coro.IsTask() {
				return coro
			}
			loop := GetCurrentEventLoop()
			if loop == nil {
				loop = NewEventLoop(e, e.asyncMode)
				SetCurrentEventLoop(loop)
			}
			var parent *val.PyContext
			if ctx != nil && ctx.VM != nil {
				parent = ctx.VM.(*VM).ActiveContext()
			}
			task := loop.CreateTaskIn(coro, parent)
			return val.ObjectVal(&task.Header)
		}
		if ctx != nil {
			ctx.Raise(val.NewTypeError("An asyncio.Future, a coroutine, or an awaitable is required"))
			return val.NoneVal()
		}
		panic("TypeError: An asyncio.Future, a coroutine, or an awaitable is required")
	}))

	// asyncio.gather(*aws, return_exceptions=False)
	mod.Dict.Set(val.InternString("gather"), val.NewBuiltinFuncVal("gather", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		returnExceptions := false
		posArgs := args
		if len(kwnames) > 0 {
			// check for return_exceptions
			for i, kw := range kwnames {
				if kw.AsString() == "return_exceptions" {
					returnExceptions = args[len(args)-len(kwnames)+i].AsBool()
				}
			}
			posArgs = args[:len(args)-len(kwnames)]
		}

		futVal := val.NewFutureVal()
		fut := futVal.AsFuture()

		if len(posArgs) == 0 {
			fut.SetResult(val.NewListVal())
			return futVal
		}

		loop := GetCurrentEventLoop()
		if loop == nil {
			loop = NewEventLoop(e, e.asyncMode)
			SetCurrentEventLoop(loop)
		}

		n := len(posArgs)
		results := make([]val.Value, n)
		var mu sync.Mutex
		remaining := n
		hasFailed := false

		for i, aw := range posArgs {
			idx := i
			var childFut *val.PyFuture

			if aw.IsCoroutine() {
				var parent *val.PyContext
				if ctx != nil && ctx.VM != nil {
					parent = ctx.VM.(*VM).ActiveContext()
				}
				task := loop.CreateTaskIn(aw, parent)
				childFut = &task.Future
			} else if aw.IsTask() {
				childFut = &aw.AsTask().Future
			} else if aw.IsFuture() {
				childFut = aw.AsFuture()
			} else {
				results[idx] = aw
				remaining--
				if remaining == 0 {
					fut.SetResult(val.NewListVal(results...))
				}
				continue
			}

			childFut.AddDoneCallback(func(v val.Value) {
				mu.Lock()
				defer mu.Unlock()

				if hasFailed && !returnExceptions {
					return
				}

				res, exc := childFut.Result()
				if !exc.IsNone() && exc.HeaderPtr() != nil {
					if returnExceptions {
						results[idx] = exc
					} else {
						hasFailed = true
						fut.SetException(exc)
						return
					}
				} else {
					results[idx] = res
				}

				remaining--
				if remaining == 0 {
					fut.SetResult(val.NewListVal(results...))
				}
			})
		}

		return futVal
	}))

	// asyncio.open_connection(host, port)
	mod.Dict.Set(val.InternString("open_connection"), val.NewBuiltinFuncVal("open_connection", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: open_connection() takes at least 2 arguments")
		}
		host := args[0].AsString()
		port := int(args[1].AsInt64())

		futVal := val.NewFutureVal()
		fut := futVal.AsFuture()

		go func() {
			addr := net.JoinHostPort(host, strconv.Itoa(port))
			conn, err := net.Dial("tcp", addr)
			if err != nil {
				fut.SetException(val.NewException(val.RuntimeErrorType, err.Error()))
				return
			}
			readerVal := newPyStreamReader(conn)
			writerVal := newPyStreamWriter(conn)
			fut.SetResult(val.NewTupleVal(readerVal, writerVal))
		}()

		return futVal
	}))

	// asyncio.start_server(client_connected_cb, host, port)
	mod.Dict.Set(val.InternString("start_server"), val.NewBuiltinFuncVal("start_server", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			panic("TypeError: start_server() takes at least 3 arguments")
		}
		cb := args[0]
		host := args[1].AsString()
		port := int(args[2].AsInt64())

		futVal := val.NewFutureVal()
		fut := futVal.AsFuture()

		loop := GetCurrentEventLoop()
		if loop == nil {
			loop = NewEventLoop(e, e.asyncMode)
			SetCurrentEventLoop(loop)
		}

		go func() {
			addr := net.JoinHostPort(host, strconv.Itoa(port))
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				fut.SetException(val.NewException(val.RuntimeErrorType, err.Error()))
				return
			}

			// Format socket info
			var sockTuples []val.Value
			if tcpAddr, ok := ln.Addr().(*net.TCPAddr); ok {
				sockTuples = append(sockTuples, val.NewTupleVal(val.NewStringVal(tcpAddr.IP.String()), val.Int64Val(int64(tcpAddr.Port))))
			}

			srv := &pyServer{
				inst: val.PyInstance{
					Header: val.ObjectHeader{Kind: val.KindInstance, Type: ServerType},
					Shape:  ServerType.RootShape,
				},
				ln:       ln,
				sockets:  val.NewListVal(sockTuples...),
				doneChan: make(chan struct{}),
			}
			srvVal := val.ObjectVal(&srv.inst.Header)

			// Accept loop on Go netpoll
			go func() {
				for {
					conn, err := ln.Accept()
					if err != nil {
						break
					}
					rVal := newPyStreamReader(conn)
					wVal := newPyStreamWriter(conn)
					loop.ScheduleCallback(cb, rVal, wVal)
				}
			}()

			fut.SetResult(srvVal)
		}()

		return futVal
	}))

	// Event loop management
	mod.Dict.Set(val.InternString("get_event_loop"), val.NewBuiltinFuncVal("get_event_loop", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		loop := GetCurrentEventLoop()
		if loop == nil {
			loop = NewEventLoop(e, e.asyncMode)
			SetCurrentEventLoop(loop)
		}
		return val.NewStringVal("<_UnixSelectorEventLoop running=True>")
	}))

	// Helper to set execution mode from Python
	mod.Dict.Set(val.InternString("set_mode"), val.NewBuiltinFuncVal("set_mode", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: set_mode() takes exactly 1 argument")
		}
		modeStr := args[0].AsString()
		if modeStr == "multicore" {
			e.SetAsyncMode(AsyncModeMultiCore)
		} else {
			e.SetAsyncMode(AsyncModeSingleThread)
		}
		return val.NoneVal()
	}))

	return modVal
}

// bytes.Buffer helper
var _ = bytes.Buffer{}
