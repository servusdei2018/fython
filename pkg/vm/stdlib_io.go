package vm

import (
	"bufio"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	BytesIOType  *val.PyType
	StringIOType *val.PyType
	FileType     *val.PyType
	ioInitOnce   sync.Once
)

type pyBytesIO struct {
	Header val.ObjectHeader
	Shape  *val.Shape
	buf    []byte
	pos    int
	mu     sync.RWMutex
}

type pyStringIO struct {
	Header val.ObjectHeader
	Shape  *val.Shape
	buf    string
	pos    int
	mu     sync.RWMutex
}

type pyFile struct {
	Header   val.ObjectHeader
	Shape    *val.Shape
	f        *os.File
	reader   *bufio.Reader
	binary   bool
	isClosed bool
	mu       sync.Mutex
}

func initIOTypes() {
	ioInitOnce.Do(func() {
		// BytesIO
		BytesIOType = val.MustNewType("BytesIO", []*val.PyType{val.ObjectType}, nil)
		BytesIOType.Dict.Set(val.InternString("read"), val.NewBuiltinFuncVal("read", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: read() takes at least 0 arguments")
			}
			bio := (*pyBytesIO)(val.AsPointer(args[0]))
			bio.mu.Lock()
			defer bio.mu.Unlock()
			n := -1
			if len(args) >= 2 && args[1].IsInt64() {
				n = int(args[1].AsInt64())
			}
			if n < 0 || bio.pos+n > len(bio.buf) {
				n = len(bio.buf) - bio.pos
			}
			if n <= 0 {
				return val.NewBytesVal(nil)
			}
			res := make([]byte, n)
			copy(res, bio.buf[bio.pos:bio.pos+n])
			bio.pos += n
			return val.NewBytesVal(res)
		}))

		BytesIOType.Dict.Set(val.InternString("write"), val.NewBuiltinFuncVal("write", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: write() takes exactly one argument")
			}
			bio := (*pyBytesIO)(val.AsPointer(args[0]))
			bio.mu.Lock()
			defer bio.mu.Unlock()
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
			if bio.pos+len(data) > len(bio.buf) {
				newBuf := make([]byte, bio.pos+len(data))
				copy(newBuf, bio.buf)
				bio.buf = newBuf
			}
			copy(bio.buf[bio.pos:], data)
			bio.pos += len(data)
			return val.Int64Val(int64(len(data)))
		}))

		BytesIOType.Dict.Set(val.InternString("getvalue"), val.NewBuiltinFuncVal("getvalue", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			bio := (*pyBytesIO)(val.AsPointer(args[0]))
			bio.mu.RLock()
			defer bio.mu.RUnlock()
			res := make([]byte, len(bio.buf))
			copy(res, bio.buf)
			return val.NewBytesVal(res)
		}))

		BytesIOType.Dict.Set(val.InternString("seek"), val.NewBuiltinFuncVal("seek", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: seek() takes at least 1 argument")
			}
			bio := (*pyBytesIO)(val.AsPointer(args[0]))
			bio.mu.Lock()
			defer bio.mu.Unlock()
			offset := int(args[1].AsInt64())
			whence := 0
			if len(args) >= 3 && args[2].IsInt64() {
				whence = int(args[2].AsInt64())
			}
			switch whence {
			case 0:
				bio.pos = offset
			case 1:
				bio.pos += offset
			case 2:
				bio.pos = len(bio.buf) + offset
			}
			if bio.pos < 0 {
				bio.pos = 0
			}
			return val.Int64Val(int64(bio.pos))
		}))

		BytesIOType.Dict.Set(val.InternString("tell"), val.NewBuiltinFuncVal("tell", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			bio := (*pyBytesIO)(val.AsPointer(args[0]))
			bio.mu.RLock()
			defer bio.mu.RUnlock()
			return val.Int64Val(int64(bio.pos))
		}))

		BytesIOType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			bio := (*pyBytesIO)(val.AsPointer(args[0]))
			bio.mu.Lock()
			bio.buf = nil
			bio.pos = 0
			bio.mu.Unlock()
			return val.NoneVal()
		}))

		// StringIO
		StringIOType = val.MustNewType("StringIO", []*val.PyType{val.ObjectType}, nil)
		StringIOType.Dict.Set(val.InternString("read"), val.NewBuiltinFuncVal("read", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			sio := (*pyStringIO)(val.AsPointer(args[0]))
			sio.mu.Lock()
			defer sio.mu.Unlock()
			n := -1
			if len(args) >= 2 && args[1].IsInt64() {
				n = int(args[1].AsInt64())
			}
			if n < 0 || sio.pos+n > len(sio.buf) {
				n = len(sio.buf) - sio.pos
			}
			if n <= 0 {
				return val.NewStringVal("")
			}
			res := sio.buf[sio.pos : sio.pos+n]
			sio.pos += n
			return val.NewStringVal(res)
		}))

		StringIOType.Dict.Set(val.InternString("write"), val.NewBuiltinFuncVal("write", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: write() takes exactly one argument")
			}
			sio := (*pyStringIO)(val.AsPointer(args[0]))
			sio.mu.Lock()
			defer sio.mu.Unlock()
			s := args[1].AsString()
			if sio.pos == len(sio.buf) {
				sio.buf += s
			} else {
				prefix := sio.buf[:sio.pos]
				var suffix string
				if sio.pos+len(s) < len(sio.buf) {
					suffix = sio.buf[sio.pos+len(s):]
				}
				sio.buf = prefix + s + suffix
			}
			sio.pos += len(s)
			return val.Int64Val(int64(len(s)))
		}))

		StringIOType.Dict.Set(val.InternString("getvalue"), val.NewBuiltinFuncVal("getvalue", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			sio := (*pyStringIO)(val.AsPointer(args[0]))
			sio.mu.RLock()
			defer sio.mu.RUnlock()
			return val.NewStringVal(sio.buf)
		}))

		StringIOType.Dict.Set(val.InternString("seek"), val.NewBuiltinFuncVal("seek", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: seek() takes at least 1 argument")
			}
			sio := (*pyStringIO)(val.AsPointer(args[0]))
			sio.mu.Lock()
			defer sio.mu.Unlock()
			offset := int(args[1].AsInt64())
			whence := 0
			if len(args) >= 3 && args[2].IsInt64() {
				whence = int(args[2].AsInt64())
			}
			switch whence {
			case 0:
				sio.pos = offset
			case 1:
				sio.pos += offset
			case 2:
				sio.pos = len(sio.buf) + offset
			}
			if sio.pos < 0 {
				sio.pos = 0
			}
			return val.Int64Val(int64(sio.pos))
		}))

		StringIOType.Dict.Set(val.InternString("tell"), val.NewBuiltinFuncVal("tell", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			sio := (*pyStringIO)(val.AsPointer(args[0]))
			sio.mu.RLock()
			defer sio.mu.RUnlock()
			return val.Int64Val(int64(sio.pos))
		}))

		StringIOType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			sio := (*pyStringIO)(val.AsPointer(args[0]))
			sio.mu.Lock()
			sio.buf = ""
			sio.pos = 0
			sio.mu.Unlock()
			return val.NoneVal()
		}))

		// FileType
		FileType = val.MustNewType("_io.TextIOWrapper", []*val.PyType{val.ObjectType}, nil)

		// Context Manager protocol: __enter__, __exit__
		FileType.Dict.Set(val.InternString("__enter__"), val.NewBuiltinFuncVal("__enter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: __enter__() takes 0 arguments")
			}
			return args[0]
		}))

		FileType.Dict.Set(val.InternString("__exit__"), val.NewBuiltinFuncVal("__exit__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: __exit__() takes arguments")
			}
			pf := (*pyFile)(val.AsPointer(args[0]))
			pf.mu.Lock()
			if !pf.isClosed && pf.f != nil {
				_ = pf.f.Close()
				pf.isClosed = true
			}
			pf.mu.Unlock()
			return val.NoneVal()
		}))

		FileType.Dict.Set(val.InternString("read"), val.NewBuiltinFuncVal("read", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			pf := (*pyFile)(val.AsPointer(args[0]))
			pf.mu.Lock()
			defer pf.mu.Unlock()
			if pf.isClosed {
				panic("ValueError: I/O operation on closed file.")
			}
			n := -1
			if len(args) >= 2 && args[1].IsInt64() {
				n = int(args[1].AsInt64())
			}
			if n < 0 {
				content, err := io.ReadAll(pf.reader)
				if err != nil && len(content) == 0 {
					if pf.binary {
						return val.NewBytesVal(nil)
					}
					return val.NewStringVal("")
				}
				if pf.binary {
					return val.NewBytesVal(content)
				}
				return val.NewStringVal(string(content))
			}
			buf := make([]byte, n)
			readN, _ := io.ReadFull(pf.reader, buf)
			if readN <= 0 {
				if pf.binary {
					return val.NewBytesVal(nil)
				}
				return val.NewStringVal("")
			}
			if pf.binary {
				return val.NewBytesVal(buf[:readN])
			}
			return val.NewStringVal(string(buf[:readN]))
		}))

		FileType.Dict.Set(val.InternString("readline"), val.NewBuiltinFuncVal("readline", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			pf := (*pyFile)(val.AsPointer(args[0]))
			pf.mu.Lock()
			defer pf.mu.Unlock()
			if pf.isClosed {
				panic("ValueError: I/O operation on closed file.")
			}
			line, err := pf.reader.ReadBytes('\n')
			if err != nil && len(line) == 0 {
				if pf.binary {
					return val.NewBytesVal(nil)
				}
				return val.NewStringVal("")
			}
			if pf.binary {
				return val.NewBytesVal(line)
			}
			return val.NewStringVal(string(line))
		}))

		FileType.Dict.Set(val.InternString("write"), val.NewBuiltinFuncVal("write", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: write() takes exactly one argument")
			}
			pf := (*pyFile)(val.AsPointer(args[0]))
			pf.mu.Lock()
			defer pf.mu.Unlock()
			if pf.isClosed {
				panic("ValueError: I/O operation on closed file.")
			}
			var data []byte
			if pf.binary {
				if args[1].IsBytes() {
					data = args[1].AsBytes().Data
				} else if args[1].IsByteArray() {
					data = args[1].AsByteArray().Data
				} else {
					panic("TypeError: a bytes-like object is required, not 'str'")
				}
			} else {
				if !args[1].IsAnyString() {
					panic("TypeError: write() argument must be str, not " + args[1].Type().Name)
				}
				data = []byte(args[1].AsString())
			}
			n, err := pf.f.Write(data)
			if err != nil {
				panic("OSError: " + err.Error())
			}
			return val.Int64Val(int64(n))
		}))

		FileType.Dict.Set(val.InternString("flush"), val.NewBuiltinFuncVal("flush", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			pf := (*pyFile)(val.AsPointer(args[0]))
			pf.mu.Lock()
			defer pf.mu.Unlock()
			if !pf.isClosed && pf.f != nil {
				_ = pf.f.Sync()
			}
			return val.NoneVal()
		}))

		FileType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			pf := (*pyFile)(val.AsPointer(args[0]))
			pf.mu.Lock()
			defer pf.mu.Unlock()
			if !pf.isClosed && pf.f != nil {
				_ = pf.f.Close()
				pf.isClosed = true
			}
			return val.NoneVal()
		}))
	})
}

// openFileBuiltin opens a file and returns a Python File Value.
func openFileBuiltin(file string, mode string) val.Value {
	initIOTypes()
	if mode == "" {
		mode = "r"
	}
	flag := os.O_RDONLY
	binary := strings.Contains(mode, "b")

	if strings.Contains(mode, "w") {
		flag = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	} else if strings.Contains(mode, "a") {
		flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	} else if strings.Contains(mode, "+") {
		flag = os.O_RDWR
	}

	f, err := os.OpenFile(file, flag, 0666)
	if err != nil {
		panic("FileNotFoundError: " + err.Error())
	}

	pf := &pyFile{
		Header: val.ObjectHeader{
			Kind: val.KindInstance,
			Type: FileType,
		},
		Shape:  FileType.RootShape,
		f:      f,
		reader: bufio.NewReader(f),
		binary: binary,
	}

	return val.ObjectVal(&pf.Header)
}

func (e *Engine) createIOModule() val.Value {
	initIOTypes()
	ioModVal := val.NewModuleVal("io", "")
	ioMod := ioModVal.AsModule()

	// BytesIO constructor
	ioMod.Dict.Set(val.InternString("BytesIO"), val.NewBuiltinFuncVal("BytesIO", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		var initBytes []byte
		if len(args) > 0 {
			if args[0].IsBytes() {
				initBytes = append([]byte(nil), args[0].AsBytes().Data...)
			} else if args[0].IsByteArray() {
				initBytes = append([]byte(nil), args[0].AsByteArray().Data...)
			} else if args[0].IsMemoryView() {
				initBytes = append([]byte(nil), args[0].AsMemoryView().Buf...)
			}
		}
		bio := &pyBytesIO{
			Header: val.ObjectHeader{
				Kind: val.KindInstance,
				Type: BytesIOType,
			},
			Shape: BytesIOType.RootShape,
			buf:   initBytes,
		}
		return val.ObjectVal(&bio.Header)
	}))

	// StringIO constructor
	ioMod.Dict.Set(val.InternString("StringIO"), val.NewBuiltinFuncVal("StringIO", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		var initStr string
		if len(args) > 0 && args[0].IsAnyString() {
			initStr = args[0].AsString()
		}
		sio := &pyStringIO{
			Header: val.ObjectHeader{
				Kind: val.KindInstance,
				Type: StringIOType,
			},
			Shape: StringIOType.RootShape,
			buf:   initStr,
		}
		return val.ObjectVal(&sio.Header)
	}))

	// open
	openFn := val.NewBuiltinFuncVal("open", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: open() takes at least 1 argument")
		}
		file := args[0].AsString()
		mode := "r"
		if len(args) >= 2 && args[1].IsAnyString() {
			mode = args[1].AsString()
		}
		return openFileBuiltin(file, mode)
	})

	ioMod.Dict.Set(val.InternString("open"), openFn)

	return ioModVal
}
