package vm

import (
	"bufio"
	"io"
	"os"
	"strings"
	"sync"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	BytesIOType  *val.PyType
	StringIOType *val.PyType
	FileType     *val.PyType
	ioInitOnce   sync.Once
)

// Compile-time layout assertions: Header must be at offset 0 so that the
// val.Value -> *PyInstance downcast is valid for these native objects.
var (
	_ [0 - unsafe.Offsetof(pyBytesIO{}.inst)]byte
	_ [0 - unsafe.Offsetof(pyStringIO{}.inst)]byte
	_ [0 - unsafe.Offsetof(pyFile{}.inst)]byte
)

type pyBytesIO struct {
	inst val.PyInstance // must be first: Header lives at offset 0
	buf  []byte
	pos  int
	mu   sync.RWMutex
}

type pyStringIO struct {
	inst val.PyInstance // must be first: Header lives at offset 0
	buf  []byte
	pos  int
	mu   sync.RWMutex
}

type pyFile struct {
	inst     val.PyInstance // must be first: Header lives at offset 0
	f        *os.File
	reader   *bufio.Reader
	writeBuf []byte
	binary   bool
	isClosed bool
	// unbuffered files (sys.stdout, sys.stderr) flush after every write.
	unbuffered bool
	name       string
	mu         sync.Mutex
}

func (pf *pyFile) flushLocked() {
	if len(pf.writeBuf) > 0 && pf.f != nil {
		_, err := pf.f.Write(pf.writeBuf)
		pf.writeBuf = pf.writeBuf[:0]
		if err != nil {
			panic("OSError: " + err.Error())
		}
	}
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
			return val.NewStringVal(string(res))
		}))

		StringIOType.Dict.Set(val.InternString("write"), val.NewBuiltinFuncVal("write", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: write() takes exactly one argument")
			}
			sio := (*pyStringIO)(val.AsPointer(args[0]))
			sio.mu.Lock()
			v := args[1]
			oldLen := len(sio.buf)
			if sio.pos == oldLen {
				var ok bool
				sio.buf, ok = val.AppendValueBytes(sio.buf, v)
				if !ok {
					sio.buf = append(sio.buf, v.AsString()...)
				}
				written := len(sio.buf) - oldLen
				sio.pos = len(sio.buf)
				sio.mu.Unlock()
				return val.Int64Val(int64(written))
			} else {
				var tmp []byte
				var ok bool
				tmp, ok = val.AppendValueBytes(tmp, v)
				if !ok {
					tmp = append(tmp, v.AsString()...)
				}
				if sio.pos+len(tmp) <= len(sio.buf) {
					copy(sio.buf[sio.pos:], tmp)
				} else {
					sio.buf = append(sio.buf[:sio.pos], tmp...)
				}
				sio.pos += len(tmp)
				sio.mu.Unlock()
				return val.Int64Val(int64(len(tmp)))
			}
		}))

		StringIOType.Dict.Set(val.InternString("getvalue"), val.NewBuiltinFuncVal("getvalue", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			sio := (*pyStringIO)(val.AsPointer(args[0]))
			sio.mu.RLock()
			n := len(sio.buf)
			if n == 0 {
				sio.mu.RUnlock()
				return val.NewStringVal("")
			}
			if n <= 7 {
				res := val.SmallStringFromBytes(sio.buf)
				sio.mu.RUnlock()
				return res
			}
			s := unsafe.String(unsafe.SliceData(sio.buf), n)
			ps := &val.PyString{
				Header: val.ObjectHeader{
					Kind: val.KindString,
					Hash: val.HashString(s),
					Type: val.StrType,
				},
				Data: s,
			}
			sio.mu.RUnlock()
			return val.ObjectVal(&ps.Header)
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

		for _, t := range []*val.PyType{StringIOType, BytesIOType} {
			noop := val.NewBuiltinFuncVal("flush", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				return val.NoneVal()
			})
			no := val.NewBuiltinFuncVal("isatty", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				return val.BoolVal(false)
			})
			yes := val.NewBuiltinFuncVal("seekable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				return val.BoolVal(true)
			})
			t.Dict.Set(val.InternString("flush"), noop)
			t.Dict.Set(val.InternString("isatty"), no)
			t.Dict.Set(val.InternString("readable"), yes)
			t.Dict.Set(val.InternString("writable"), yes)
			t.Dict.Set(val.InternString("seekable"), yes)
			t.InvalidateVersion()
		}
		StringIOType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			sio := (*pyStringIO)(val.AsPointer(args[0]))
			sio.mu.Lock()
			sio.buf = nil
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
				pf.flushLocked()
				_ = pf.f.Close()
				pf.isClosed = true
			}
			pf.mu.Unlock()
			return val.NoneVal()
		}))

		FileType.Dict.Set(val.InternString("read"), val.NewBuiltinFuncVal("read", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			pf := (*pyFile)(val.AsPointer(args[0]))
			pf.mu.Lock()
			if pf.isClosed {
				pf.mu.Unlock()
				panic("ValueError: I/O operation on closed file.")
			}
			n := -1
			if len(args) >= 2 && args[1].IsInt64() {
				n = int(args[1].AsInt64())
			}
			if n < 0 {
				pf.flushLocked()
				stat, err := pf.f.Stat()
				if err == nil && stat.Size() > 0 {
					size := stat.Size()
					buf := make([]byte, size)
					readN, _ := io.ReadFull(pf.f, buf)
					if readN > 0 {
						buf = buf[:readN]
						if pf.binary {
							pf.mu.Unlock()
							return val.NewBytesVal(buf)
						}
						s := unsafe.String(unsafe.SliceData(buf), readN)
						ps := &val.PyString{
							Header: val.ObjectHeader{
								Kind: val.KindString,
								Hash: val.HashString(s),
								Type: val.StrType,
							},
							Data: s,
						}
						pf.mu.Unlock()
						return val.ObjectVal(&ps.Header)
					}
				}
				content, err := io.ReadAll(pf.reader)
				if err != nil && len(content) == 0 {
					pf.mu.Unlock()
					if pf.binary {
						return val.NewBytesVal(nil)
					}
					return val.NewStringVal("")
				}
				pf.mu.Unlock()
				if pf.binary {
					return val.NewBytesVal(content)
				}
				s := unsafe.String(unsafe.SliceData(content), len(content))
				ps := &val.PyString{
					Header: val.ObjectHeader{
						Kind: val.KindString,
						Hash: val.HashString(s),
						Type: val.StrType,
					},
					Data: s,
				}
				return val.ObjectVal(&ps.Header)
			}
			buf := make([]byte, n)
			readN, _ := io.ReadFull(pf.reader, buf)
			pf.mu.Unlock()
			if readN <= 0 {
				if pf.binary {
					return val.NewBytesVal(nil)
				}
				return val.NewStringVal("")
			}
			if pf.binary {
				return val.NewBytesVal(buf[:readN])
			}
			s := unsafe.String(unsafe.SliceData(buf), readN)
			ps := &val.PyString{
				Header: val.ObjectHeader{
					Kind: val.KindString,
					Hash: val.HashString(s),
					Type: val.StrType,
				},
				Data: s,
			}
			return val.ObjectVal(&ps.Header)
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
			if pf.isClosed {
				pf.mu.Unlock()
				panic("ValueError: I/O operation on closed file.")
			}
			oldLen := len(pf.writeBuf)
			if pf.binary {
				if args[1].IsBytes() {
					pf.writeBuf = append(pf.writeBuf, args[1].AsBytes().Data...)
				} else if args[1].IsByteArray() {
					pf.writeBuf = append(pf.writeBuf, args[1].AsByteArray().Data...)
				} else if args[1].IsMemoryView() {
					pf.writeBuf = append(pf.writeBuf, args[1].AsMemoryView().Buf...)
				} else {
					pf.mu.Unlock()
					panic("TypeError: a bytes-like object is required, not 'str'")
				}
			} else {
				var ok bool
				pf.writeBuf, ok = val.AppendValueBytes(pf.writeBuf, args[1])
				if !ok {
					if !args[1].IsAnyString() {
						pf.mu.Unlock()
						panic("TypeError: write() argument must be str, not " + args[1].Type().Name)
					}
					pf.writeBuf = append(pf.writeBuf, args[1].AsString()...)
				}
			}
			written := len(pf.writeBuf) - oldLen
			if pf.unbuffered || len(pf.writeBuf) >= 65536 {
				pf.flushLocked()
			}
			pf.mu.Unlock()
			return val.Int64Val(int64(written))
		}))

		FileType.Dict.Set(val.InternString("flush"), val.NewBuiltinFuncVal("flush", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			pf := (*pyFile)(val.AsPointer(args[0]))
			pf.mu.Lock()
			defer pf.mu.Unlock()
			if !pf.isClosed && pf.f != nil {
				pf.flushLocked()
				_ = pf.f.Sync()
			}
			return val.NoneVal()
		}))

		FileType.Dict.Set(val.InternString("isatty"), val.NewBuiltinFuncVal("isatty", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			pf := (*pyFile)(val.AsPointer(args[0]))
			if pf.f == nil {
				return val.BoolVal(false)
			}
			info, err := pf.f.Stat()
			return val.BoolVal(err == nil && info.Mode()&os.ModeCharDevice != 0)
		}))
		FileType.Dict.Set(val.InternString("fileno"), val.NewBuiltinFuncVal("fileno", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			pf := (*pyFile)(val.AsPointer(args[0]))
			if pf.f == nil {
				panic("OSError: file has no descriptor")
			}
			return val.Int64Val(int64(pf.f.Fd()))
		}))
		FileType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			pf := (*pyFile)(val.AsPointer(args[0]))
			pf.mu.Lock()
			defer pf.mu.Unlock()
			if !pf.isClosed && pf.f != nil {
				pf.flushLocked()
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

	var writeBuf []byte
	if strings.Contains(mode, "w") || strings.Contains(mode, "a") || strings.Contains(mode, "+") {
		writeBuf = make([]byte, 0, 65536)
	}

	pf := &pyFile{
		inst:     val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: FileType}, Shape: FileType.RootShape},
		f:        f,
		reader:   bufio.NewReader(f),
		writeBuf: writeBuf,
		binary:   binary,
	}

	return val.ObjectVal(&pf.inst.Header)
}

// newStdStream wraps one of the process's standard streams as a text file.
func newStdStream(f *os.File, name string, writable bool) val.Value {
	initIOTypes()
	pf := &pyFile{
		inst:       val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: FileType}, Shape: FileType.RootShape},
		f:          f,
		reader:     bufio.NewReader(f),
		unbuffered: writable,
		name:       name,
	}
	return val.ObjectVal(&pf.inst.Header)
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
			inst: val.PyInstance{
				Header: val.ObjectHeader{Kind: val.KindInstance, Type: BytesIOType},
				Shape:  BytesIOType.RootShape,
			},
			buf: initBytes,
		}
		return val.ObjectVal(&bio.inst.Header)
	}))

	// StringIO constructor
	ioMod.Dict.Set(val.InternString("StringIO"), val.NewBuiltinFuncVal("StringIO", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		var initBytes []byte
		if len(args) > 0 && args[0].IsAnyString() {
			initBytes, _ = val.AppendValueBytes(nil, args[0])
		} else {
			initBytes = make([]byte, 0, 8192)
		}
		sio := &pyStringIO{
			inst: val.PyInstance{
				Header: val.ObjectHeader{Kind: val.KindInstance, Type: StringIOType},
				Shape:  StringIOType.RootShape,
			},
			buf: initBytes,
		}
		return val.ObjectVal(&sio.inst.Header)
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
