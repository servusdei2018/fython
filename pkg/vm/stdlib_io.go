package vm

import (
	"bufio"
	"fmt"
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

		BytesIOType.Dict.Set(val.InternString("readline"), val.NewBuiltinFuncVal("readline", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			bio := (*pyBytesIO)(val.AsPointer(args[0]))
			bio.mu.Lock()
			defer bio.mu.Unlock()
			if bio.pos >= len(bio.buf) {
				return val.NewBytesVal(nil)
			}
			start := bio.pos
			nl := -1
			for i := start; i < len(bio.buf); i++ {
				if bio.buf[i] == '\n' {
					nl = i
					break
				}
			}
			if nl >= 0 {
				bio.pos = nl + 1
				res := make([]byte, nl+1-start)
				copy(res, bio.buf[start:nl+1])
				return val.NewBytesVal(res)
			}
			bio.pos = len(bio.buf)
			res := make([]byte, len(bio.buf)-start)
			copy(res, bio.buf[start:])
			return val.NewBytesVal(res)
		}))

		BytesIOType.Dict.Set(val.InternString("readlines"), val.NewBuiltinFuncVal("readlines", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			bio := (*pyBytesIO)(val.AsPointer(args[0]))
			bio.mu.Lock()
			defer bio.mu.Unlock()
			var lines []val.Value
			for bio.pos < len(bio.buf) {
				start := bio.pos
				nl := -1
				for i := start; i < len(bio.buf); i++ {
					if bio.buf[i] == '\n' {
						nl = i
						break
					}
				}
				if nl >= 0 {
					bio.pos = nl + 1
					res := make([]byte, nl+1-start)
					copy(res, bio.buf[start:nl+1])
					lines = append(lines, val.NewBytesVal(res))
				} else {
					bio.pos = len(bio.buf)
					res := make([]byte, len(bio.buf)-start)
					copy(res, bio.buf[start:])
					lines = append(lines, val.NewBytesVal(res))
				}
			}
			return val.NewListVal(lines...)
		}))

		BytesIOType.Dict.Set(val.InternString("truncate"), val.NewBuiltinFuncVal("truncate", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			bio := (*pyBytesIO)(val.AsPointer(args[0]))
			bio.mu.Lock()
			defer bio.mu.Unlock()
			size := bio.pos
			if len(args) > 1 && !args[1].IsNone() && args[1].IsInt64() {
				size = int(args[1].AsInt64())
			}
			if size < 0 {
				panic("ValueError: Negative size not allowed")
			}
			if size < len(bio.buf) {
				bio.buf = bio.buf[:size]
			} else if size > len(bio.buf) {
				bio.buf = append(bio.buf, make([]byte, size-len(bio.buf))...)
			}
			return val.Int64Val(int64(len(bio.buf)))
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
		StringIOType.Dict.Set(val.InternString("readline"), val.NewBuiltinFuncVal("readline", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			sio := (*pyStringIO)(val.AsPointer(args[0]))
			sio.mu.Lock()
			defer sio.mu.Unlock()
			if sio.pos >= len(sio.buf) {
				return val.NewStringVal("")
			}
			start := sio.pos
			nl := -1
			for i := start; i < len(sio.buf); i++ {
				if sio.buf[i] == '\n' {
					nl = i
					break
				}
			}
			if nl >= 0 {
				sio.pos = nl + 1
				return val.NewStringVal(string(sio.buf[start : nl+1]))
			}
			sio.pos = len(sio.buf)
			return val.NewStringVal(string(sio.buf[start:]))
		}))

		StringIOType.Dict.Set(val.InternString("readlines"), val.NewBuiltinFuncVal("readlines", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			sio := (*pyStringIO)(val.AsPointer(args[0]))
			sio.mu.Lock()
			defer sio.mu.Unlock()
			var lines []val.Value
			for sio.pos < len(sio.buf) {
				start := sio.pos
				nl := -1
				for i := start; i < len(sio.buf); i++ {
					if sio.buf[i] == '\n' {
						nl = i
						break
					}
				}
				if nl >= 0 {
					sio.pos = nl + 1
					lines = append(lines, val.NewStringVal(string(sio.buf[start:nl+1])))
				} else {
					sio.pos = len(sio.buf)
					lines = append(lines, val.NewStringVal(string(sio.buf[start:])))
				}
			}
			return val.NewListVal(lines...)
		}))

		StringIOType.Dict.Set(val.InternString("truncate"), val.NewBuiltinFuncVal("truncate", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			sio := (*pyStringIO)(val.AsPointer(args[0]))
			sio.mu.Lock()
			defer sio.mu.Unlock()
			size := sio.pos
			if len(args) > 1 && !args[1].IsNone() && args[1].IsInt64() {
				size = int(args[1].AsInt64())
			}
			if size < 0 {
				panic("ValueError: Negative size not allowed")
			}
			if size < len(sio.buf) {
				sio.buf = sio.buf[:size]
			} else if size > len(sio.buf) {
				sio.buf = append(sio.buf, make([]byte, size-len(sio.buf))...)
			}
			return val.Int64Val(int64(len(sio.buf)))
		}))

		StringIOType.Slots.TpIter = func(ctx *val.ExecContext, o val.Value) val.Value {
			return val.NewIterValWithNext(func() (val.Value, bool) {
				sio := (*pyStringIO)(val.AsPointer(o))
				sio.mu.Lock()
				defer sio.mu.Unlock()
				if sio.pos >= len(sio.buf) {
					return val.NoneVal(), false
				}
				start := sio.pos
				nl := -1
				for i := start; i < len(sio.buf); i++ {
					if sio.buf[i] == '\n' {
						nl = i
						break
					}
				}
				if nl >= 0 {
					sio.pos = nl + 1
					return val.NewStringVal(string(sio.buf[start : nl+1])), true
				}
				sio.pos = len(sio.buf)
				return val.NewStringVal(string(sio.buf[start:])), true
			})
		}
		StringIOType.Dict.Set(val.InternString("__iter__"), val.NewBuiltinFuncVal("__iter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: __iter__() missing self")
			}
			return StringIOType.Slots.TpIter(ctx, args[0])
		}))

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

		FileType.Dict.Set(val.InternString("encoding"), val.NewStringVal("utf-8"))
		FileType.Dict.Set(val.InternString("errors"), val.NewStringVal("strict"))
		FileType.Dict.Set(val.InternString("closed"), val.BoolVal(false))
		FileType.Dict.Set(val.InternString("mode"), val.NewStringVal("r"))
		FileType.Dict.Set(val.InternString("name"), val.NewStringVal(""))

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
		FileType.Slots.TpIter = func(ctx *val.ExecContext, o val.Value) val.Value {
			return val.NewIterValWithNext(func() (val.Value, bool) {
				pf := (*pyFile)(val.AsPointer(o))
				pf.mu.Lock()
				defer pf.mu.Unlock()
				if pf.isClosed {
					return val.NoneVal(), false
				}
				line, err := pf.reader.ReadBytes('\n')
				if err != nil && len(line) == 0 {
					return val.NoneVal(), false
				}
				if pf.binary {
					return val.NewBytesVal(line), true
				}
				return val.NewStringVal(string(line)), true
			})
		}
		FileType.Dict.Set(val.InternString("__iter__"), val.NewBuiltinFuncVal("__iter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: __iter__() missing self")
			}
			return FileType.Slots.TpIter(ctx, args[0])
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

		FileType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) == 0 {
				panic("TypeError: TextIOWrapper() missing required argument 'buffer'")
			}
			bufVal := args[0]
			if bufVal.Type() == FileType {
				pf := (*pyFile)(val.AsPointer(bufVal))
				pf.binary = false
				return bufVal
			}
			return bufVal
		}
	})
}

// openFileBuiltin opens a file and returns a Python File Value.
func openFileBuiltin(file string, mode string) val.Value {
	return openFileBuiltinVal(val.NewStringVal(file), mode)
}

func openFileBuiltinVal(fileVal val.Value, mode string) val.Value {
	initIOTypes()
	if mode == "" {
		mode = "r"
	}
	flag := os.O_RDONLY
	binary := strings.Contains(mode, "b")

	var f *os.File
	var err error
	if fileVal.IsInt64() {
		fd := int(fileVal.AsInt64())
		fdMutex.Lock()
		existing, ok := fdMap[fd]
		fdMutex.Unlock()
		if ok {
			f = existing
		} else {
			f = os.NewFile(uintptr(fd), fmt.Sprintf("<fdopen %d>", fd))
		}
	} else {
		if strings.Contains(mode, "w") {
			flag = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		} else if strings.Contains(mode, "a") {
			flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
		} else if strings.Contains(mode, "+") {
			flag = os.O_RDWR
		}
		pathStr := fileVal.AsString()
		f, err = os.OpenFile(pathStr, flag, 0666)
		if err != nil {
			panic("FileNotFoundError: " + err.Error())
		}
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

	initIOTypes()
	ioMod.Dict.Set(val.InternString("TextIOWrapper"), val.TypeVal(FileType))
	ioMod.Dict.Set(val.InternString("BufferedReader"), val.TypeVal(FileType))
	ioMod.Dict.Set(val.InternString("BufferedWriter"), val.TypeVal(FileType))
	ioMod.Dict.Set(val.InternString("BufferedRandom"), val.TypeVal(FileType))
	ioMod.Dict.Set(val.InternString("FileIO"), val.TypeVal(FileType))
	ioBaseType := val.MustNewType("IOBase", []*val.PyType{val.ObjectType}, nil)
	ioBaseType.Dict.Set(val.InternString("closed"), val.BoolVal(false))
	ioBaseType.Dict.Set(val.InternString("__enter__"), val.NewBuiltinFuncVal("__enter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			return args[0]
		}
		return val.NoneVal()
	}))
	ioBaseType.Dict.Set(val.InternString("__exit__"), val.NewBuiltinFuncVal("__exit__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if closeFn, _, ok := args[0].Type().LookupMRO(val.InternString("close")); ok {
				return ctx.Invoke1(closeFn, args[0])
			}
		}
		return val.NoneVal()
	}))
	ioBaseType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 && args[0].IsInstance() {
			args[0].AsInstance().SetAttr(val.InternString("closed"), val.BoolVal(true))
		}
		return val.NoneVal()
	}))
	ioBaseType.Dict.Set(val.InternString("flush"), val.NewBuiltinFuncVal("flush", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))
	ioBaseType.Dict.Set(val.InternString("seekable"), val.NewBuiltinFuncVal("seekable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(false)
	}))
	ioBaseType.Dict.Set(val.InternString("readable"), val.NewBuiltinFuncVal("readable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(false)
	}))
	ioBaseType.Dict.Set(val.InternString("writable"), val.NewBuiltinFuncVal("writable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(false)
	}))
	ioBaseType.Dict.Set(val.InternString("_checkClosed"), val.NewBuiltinFuncVal("_checkClosed", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			clVal := val.GetAttrCtx(ctx, args[0], val.InternString("closed"))
			if clVal.IsTruthy() {
				panic("ValueError: I/O operation on closed file.")
			}
		}
		return val.NoneVal()
	}))
	ioBaseType.Dict.Set(val.InternString("_checkReadable"), val.NewBuiltinFuncVal("_checkReadable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if fn, _, ok := args[0].Type().LookupMRO(val.InternString("readable")); ok {
				res := ctx.Invoke1(fn, args[0])
				if !res.IsTruthy() {
					panic("OSError: File or stream is not readable.")
				}
			}
		}
		return val.NoneVal()
	}))
	ioBaseType.Dict.Set(val.InternString("_checkWritable"), val.NewBuiltinFuncVal("_checkWritable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if fn, _, ok := args[0].Type().LookupMRO(val.InternString("writable")); ok {
				res := ctx.Invoke1(fn, args[0])
				if !res.IsTruthy() {
					panic("OSError: File or stream is not writable.")
				}
			}
		}
		return val.NoneVal()
	}))
	ioBaseType.Dict.Set(val.InternString("_checkSeekable"), val.NewBuiltinFuncVal("_checkSeekable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if fn, _, ok := args[0].Type().LookupMRO(val.InternString("seekable")); ok {
				res := ctx.Invoke1(fn, args[0])
				if !res.IsTruthy() {
					panic("OSError: File or stream is not seekable.")
				}
			}
		}
		return val.NoneVal()
	}))

	rawIOBaseType := val.MustNewType("RawIOBase", []*val.PyType{ioBaseType}, nil)
	bufferedIOBaseType := val.MustNewType("BufferedIOBase", []*val.PyType{ioBaseType}, nil)
	textIOBaseType := val.MustNewType("TextIOBase", []*val.PyType{ioBaseType}, nil)

	// BufferedReader
	bufferedReaderType := val.MustNewType("BufferedReader", []*val.PyType{bufferedIOBaseType}, nil)
	bufferedReaderType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			panic("TypeError: BufferedReader() missing required argument 'raw'")
		}
		inst := val.NewInstanceOfType(bufferedReaderType)
		inst.AsInstance().SetAttr(val.InternString("raw"), args[0])
		return inst
	}
	bufferedReaderType.Dict.Set(val.InternString("read"), val.NewBuiltinFuncVal("read", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			panic("TypeError: read() missing self")
		}
		rawVal, _ := args[0].AsInstance().GetAttr(val.InternString("raw"))
		size := -1
		if len(args) > 1 && !args[1].IsNone() {
			size = int(args[1].AsInt64())
		}
		readintoFn, _, hasReadinto := rawVal.Type().LookupMRO(val.InternString("readinto"))
		if hasReadinto {
			if size >= 0 {
				buf := val.NewByteArrayVal(make([]byte, size))
				nVal := ctx.Invoke2(readintoFn, rawVal, buf)
				n := int(nVal.AsInt64())
				return val.NewBytesVal(buf.AsByteArray().Data[:n])
			}
			var accumulated []byte
			for {
				chunk := val.NewByteArrayVal(make([]byte, 8192))
				nVal := ctx.Invoke2(readintoFn, rawVal, chunk)
				if nVal.IsNone() || nVal.AsInt64() == 0 {
					break
				}
				n := int(nVal.AsInt64())
				accumulated = append(accumulated, chunk.AsByteArray().Data[:n]...)
			}
			return val.NewBytesVal(accumulated)
		}
		if readFn, _, hasRead := rawVal.Type().LookupMRO(val.InternString("read")); hasRead {
			if size >= 0 {
				return ctx.Invoke2(readFn, rawVal, val.Int64Val(int64(size)))
			}
			return ctx.Invoke1(readFn, rawVal)
		}
		panic("AttributeError: raw object has no read or readinto")
	}))
	bufferedReaderType.Dict.Set(val.InternString("readline"), val.NewBuiltinFuncVal("readline", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			panic("TypeError: readline() missing self")
		}
		rawVal, _ := args[0].AsInstance().GetAttr(val.InternString("raw"))
		readintoFn, _, hasReadinto := rawVal.Type().LookupMRO(val.InternString("readinto"))
		var line []byte
		if hasReadinto {
			oneByte := val.NewByteArrayVal(make([]byte, 1))
			for {
				nVal := ctx.Invoke2(readintoFn, rawVal, oneByte)
				if nVal.IsNone() || nVal.AsInt64() == 0 {
					break
				}
				b := oneByte.AsByteArray().Data[0]
				line = append(line, b)
				if b == '\n' {
					break
				}
			}
		}
		return val.NewBytesVal(line)
	}))
	bufferedReaderType.Dict.Set(val.InternString("readinto"), val.NewBuiltinFuncVal("readinto", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: readinto() takes 1 argument")
		}
		rawVal, _ := args[0].AsInstance().GetAttr(val.InternString("raw"))
		if fn, _, ok := rawVal.Type().LookupMRO(val.InternString("readinto")); ok {
			return ctx.Invoke2(fn, rawVal, args[1])
		}
		panic("AttributeError: raw object has no readinto")
	}))
	bufferedReaderType.Dict.Set(val.InternString("flush"), val.NewBuiltinFuncVal("flush", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if rawVal, ok := args[0].AsInstance().GetAttr(val.InternString("raw")); ok {
				if fn, _, has := rawVal.Type().LookupMRO(val.InternString("flush")); has {
					return ctx.Invoke1(fn, rawVal)
				}
			}
		}
		return val.NoneVal()
	}))
	bufferedReaderType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if rawVal, ok := args[0].AsInstance().GetAttr(val.InternString("raw")); ok {
				if fn, _, has := rawVal.Type().LookupMRO(val.InternString("close")); has {
					return ctx.Invoke1(fn, rawVal)
				}
			}
		}
		return val.NoneVal()
	}))
	bufferedReaderType.Dict.Set(val.InternString("fileno"), val.NewBuiltinFuncVal("fileno", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if rawVal, ok := args[0].AsInstance().GetAttr(val.InternString("raw")); ok {
				if fn, _, has := rawVal.Type().LookupMRO(val.InternString("fileno")); has {
					return ctx.Invoke1(fn, rawVal)
				}
			}
		}
		panic("UnsupportedOperation: fileno")
	}))
	bufferedReaderType.Dict.Set(val.InternString("readable"), val.NewBuiltinFuncVal("readable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(true)
	}))
	bufferedReaderType.Dict.Set(val.InternString("writable"), val.NewBuiltinFuncVal("writable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(false)
	}))
	bufferedReaderType.Dict.Set(val.InternString("closed"), val.NewPropertyVal(
		val.NewBuiltinFuncVal("closed", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) > 0 {
				if rawVal, ok := args[0].AsInstance().GetAttr(val.InternString("raw")); ok {
					return val.GetAttrCtx(ctx, rawVal, val.InternString("closed"))
				}
			}
			return val.BoolVal(false)
		}),
		val.NoneVal(),
		val.NoneVal(),
	))

	// BufferedWriter
	bufferedWriterType := val.MustNewType("BufferedWriter", []*val.PyType{bufferedIOBaseType}, nil)
	bufferedWriterType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			panic("TypeError: BufferedWriter() missing required argument 'raw'")
		}
		inst := val.NewInstanceOfType(bufferedWriterType)
		inst.AsInstance().SetAttr(val.InternString("raw"), args[0])
		return inst
	}
	bufferedWriterType.Dict.Set(val.InternString("write"), val.NewBuiltinFuncVal("write", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: write() takes 1 argument")
		}
		rawVal, _ := args[0].AsInstance().GetAttr(val.InternString("raw"))
		if fn, _, ok := rawVal.Type().LookupMRO(val.InternString("write")); ok {
			return ctx.Invoke2(fn, rawVal, args[1])
		}
		panic("AttributeError: raw object has no write")
	}))
	bufferedWriterType.Dict.Set(val.InternString("flush"), val.NewBuiltinFuncVal("flush", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if rawVal, ok := args[0].AsInstance().GetAttr(val.InternString("raw")); ok {
				if fn, _, has := rawVal.Type().LookupMRO(val.InternString("flush")); has {
					return ctx.Invoke1(fn, rawVal)
				}
			}
		}
		return val.NoneVal()
	}))
	bufferedWriterType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if rawVal, ok := args[0].AsInstance().GetAttr(val.InternString("raw")); ok {
				if fn, _, has := rawVal.Type().LookupMRO(val.InternString("close")); has {
					return ctx.Invoke1(fn, rawVal)
				}
			}
		}
		return val.NoneVal()
	}))
	bufferedWriterType.Dict.Set(val.InternString("fileno"), val.NewBuiltinFuncVal("fileno", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if rawVal, ok := args[0].AsInstance().GetAttr(val.InternString("raw")); ok {
				if fn, _, has := rawVal.Type().LookupMRO(val.InternString("fileno")); has {
					return ctx.Invoke1(fn, rawVal)
				}
			}
		}
		panic("UnsupportedOperation: fileno")
	}))
	bufferedWriterType.Dict.Set(val.InternString("readable"), val.NewBuiltinFuncVal("readable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(false)
	}))
	bufferedWriterType.Dict.Set(val.InternString("writable"), val.NewBuiltinFuncVal("writable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(true)
	}))
	bufferedWriterType.Dict.Set(val.InternString("closed"), val.NewPropertyVal(
		val.NewBuiltinFuncVal("closed", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) > 0 {
				if rawVal, ok := args[0].AsInstance().GetAttr(val.InternString("raw")); ok {
					return val.GetAttrCtx(ctx, rawVal, val.InternString("closed"))
				}
			}
			return val.BoolVal(false)
		}),
		val.NoneVal(),
		val.NoneVal(),
	))

	// BufferedRWPair
	bufferedRWPairType := val.MustNewType("BufferedRWPair", []*val.PyType{bufferedIOBaseType}, nil)
	bufferedRWPairType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: BufferedRWPair() takes at least 2 arguments")
		}
		inst := val.NewInstanceOfType(bufferedRWPairType)
		inst.AsInstance().SetAttr(val.InternString("reader"), args[0])
		inst.AsInstance().SetAttr(val.InternString("writer"), args[1])
		return inst
	}
	bufferedRWPairType.Dict.Set(val.InternString("read"), val.NewBuiltinFuncVal("read", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		rVal, _ := args[0].AsInstance().GetAttr(val.InternString("reader"))
		if fn, _, ok := rVal.Type().LookupMRO(val.InternString("read")); ok {
			if len(args) > 1 {
				return ctx.Invoke2(fn, rVal, args[1])
			}
			return ctx.Invoke1(fn, rVal)
		}
		panic("AttributeError: reader has no read")
	}))
	bufferedRWPairType.Dict.Set(val.InternString("readline"), val.NewBuiltinFuncVal("readline", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		rVal, _ := args[0].AsInstance().GetAttr(val.InternString("reader"))
		if fn, _, ok := rVal.Type().LookupMRO(val.InternString("readline")); ok {
			if len(args) > 1 {
				return ctx.Invoke2(fn, rVal, args[1])
			}
			return ctx.Invoke1(fn, rVal)
		}
		panic("AttributeError: reader has no readline")
	}))
	bufferedRWPairType.Dict.Set(val.InternString("write"), val.NewBuiltinFuncVal("write", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		wVal, _ := args[0].AsInstance().GetAttr(val.InternString("writer"))
		if fn, _, ok := wVal.Type().LookupMRO(val.InternString("write")); ok {
			return ctx.Invoke2(fn, wVal, args[1])
		}
		panic("AttributeError: writer has no write")
	}))
	bufferedRWPairType.Dict.Set(val.InternString("flush"), val.NewBuiltinFuncVal("flush", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		wVal, _ := args[0].AsInstance().GetAttr(val.InternString("writer"))
		if fn, _, ok := wVal.Type().LookupMRO(val.InternString("flush")); ok {
			return ctx.Invoke1(fn, wVal)
		}
		return val.NoneVal()
	}))
	bufferedRWPairType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		rVal, _ := args[0].AsInstance().GetAttr(val.InternString("reader"))
		wVal, _ := args[0].AsInstance().GetAttr(val.InternString("writer"))
		if fn, _, ok := rVal.Type().LookupMRO(val.InternString("close")); ok {
			ctx.Invoke1(fn, rVal)
		}
		if fn, _, ok := wVal.Type().LookupMRO(val.InternString("close")); ok {
			ctx.Invoke1(fn, wVal)
		}
		return val.NoneVal()
	}))
	bufferedRWPairType.Dict.Set(val.InternString("fileno"), val.NewBuiltinFuncVal("fileno", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		wVal, _ := args[0].AsInstance().GetAttr(val.InternString("writer"))
		if fn, _, ok := wVal.Type().LookupMRO(val.InternString("fileno")); ok {
			return ctx.Invoke1(fn, wVal)
		}
		panic("UnsupportedOperation: fileno")
	}))
	bufferedRWPairType.Dict.Set(val.InternString("readable"), val.NewBuiltinFuncVal("readable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(true)
	}))
	bufferedRWPairType.Dict.Set(val.InternString("writable"), val.NewBuiltinFuncVal("writable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(true)
	}))
	bufferedRWPairType.Dict.Set(val.InternString("closed"), val.NewPropertyVal(
		val.NewBuiltinFuncVal("closed", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) > 0 {
				if wVal, ok := args[0].AsInstance().GetAttr(val.InternString("writer")); ok {
					return val.GetAttrCtx(ctx, wVal, val.InternString("closed"))
				}
			}
			return val.BoolVal(false)
		}),
		val.NoneVal(),
		val.NoneVal(),
	))

	// TextIOWrapper
	textIOWrapperType := val.MustNewType("TextIOWrapper", []*val.PyType{textIOBaseType}, nil)
	textIOWrapperType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			panic("TypeError: TextIOWrapper() missing required argument 'buffer'")
		}
		bufVal := args[0]
		if bufVal.Type() == FileType {
			pf := (*pyFile)(val.AsPointer(bufVal))
			pf.binary = false
			return bufVal
		}
		enc := "utf-8"
		if len(args) > 1 && !args[1].IsNone() {
			enc = args[1].AsString()
		}
		inst := val.NewInstanceOfType(textIOWrapperType)
		inst.AsInstance().SetAttr(val.InternString("buffer"), bufVal)
		inst.AsInstance().SetAttr(val.InternString("encoding"), val.NewStringVal(enc))
		return inst
	}
	textIOWrapperType.Dict.Set(val.InternString("read"), val.NewBuiltinFuncVal("read", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		bufVal, _ := args[0].AsInstance().GetAttr(val.InternString("buffer"))
		var res val.Value
		if fn, _, ok := bufVal.Type().LookupMRO(val.InternString("read")); ok {
			if len(args) > 1 {
				res = ctx.Invoke2(fn, bufVal, args[1])
			} else {
				res = ctx.Invoke1(fn, bufVal)
			}
		}
		if res.IsBytes() {
			return val.NewStringVal(string(res.AsBytes().Data))
		}
		return res
	}))
	textIOWrapperType.Dict.Set(val.InternString("readline"), val.NewBuiltinFuncVal("readline", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		bufVal, _ := args[0].AsInstance().GetAttr(val.InternString("buffer"))
		var res val.Value
		if fn, _, ok := bufVal.Type().LookupMRO(val.InternString("readline")); ok {
			if len(args) > 1 {
				res = ctx.Invoke2(fn, bufVal, args[1])
			} else {
				res = ctx.Invoke1(fn, bufVal)
			}
		}
		if res.IsBytes() {
			return val.NewStringVal(string(res.AsBytes().Data))
		}
		return res
	}))
	textIOWrapperType.Dict.Set(val.InternString("write"), val.NewBuiltinFuncVal("write", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: write() takes 1 argument")
		}
		bufVal, _ := args[0].AsInstance().GetAttr(val.InternString("buffer"))
		strData := args[1].AsString()
		bytesVal := val.NewBytesVal([]byte(strData))
		if fn, _, ok := bufVal.Type().LookupMRO(val.InternString("write")); ok {
			ctx.Invoke2(fn, bufVal, bytesVal)
		}
		return val.Int64Val(int64(len(strData)))
	}))
	textIOWrapperType.Dict.Set(val.InternString("flush"), val.NewBuiltinFuncVal("flush", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		bufVal, _ := args[0].AsInstance().GetAttr(val.InternString("buffer"))
		if fn, _, ok := bufVal.Type().LookupMRO(val.InternString("flush")); ok {
			return ctx.Invoke1(fn, bufVal)
		}
		return val.NoneVal()
	}))
	textIOWrapperType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		bufVal, _ := args[0].AsInstance().GetAttr(val.InternString("buffer"))
		if fn, _, ok := bufVal.Type().LookupMRO(val.InternString("close")); ok {
			return ctx.Invoke1(fn, bufVal)
		}
		return val.NoneVal()
	}))
	textIOWrapperType.Dict.Set(val.InternString("fileno"), val.NewBuiltinFuncVal("fileno", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		bufVal, _ := args[0].AsInstance().GetAttr(val.InternString("buffer"))
		if fn, _, ok := bufVal.Type().LookupMRO(val.InternString("fileno")); ok {
			return ctx.Invoke1(fn, bufVal)
		}
		panic("UnsupportedOperation: fileno")
	}))
	textIOWrapperType.Dict.Set(val.InternString("closed"), val.NewPropertyVal(
		val.NewBuiltinFuncVal("closed", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) > 0 {
				if bufVal, ok := args[0].AsInstance().GetAttr(val.InternString("buffer")); ok {
					return val.GetAttrCtx(ctx, bufVal, val.InternString("closed"))
				}
			}
			return val.BoolVal(false)
		}),
		val.NoneVal(),
		val.NoneVal(),
	))

	ioMod.Dict.Set(val.InternString("IOBase"), val.TypeVal(ioBaseType))
	ioMod.Dict.Set(val.InternString("RawIOBase"), val.TypeVal(rawIOBaseType))
	ioMod.Dict.Set(val.InternString("BufferedIOBase"), val.TypeVal(bufferedIOBaseType))
	ioMod.Dict.Set(val.InternString("TextIOBase"), val.TypeVal(textIOBaseType))
	ioMod.Dict.Set(val.InternString("BufferedReader"), val.TypeVal(bufferedReaderType))
	ioMod.Dict.Set(val.InternString("BufferedWriter"), val.TypeVal(bufferedWriterType))
	ioMod.Dict.Set(val.InternString("BufferedRWPair"), val.TypeVal(bufferedRWPairType))
	ioMod.Dict.Set(val.InternString("TextIOWrapper"), val.TypeVal(textIOWrapperType))
	ioMod.Dict.Set(val.InternString("UnsupportedOperation"), val.TypeVal(val.OSErrorType))

	// open
	openFn := val.NewBuiltinFuncVal("open", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			if v, ok := kwArg(args, kwnames, "file"); ok {
				pa = []val.Value{v}
			} else {
				panic("TypeError: open() missing required argument 'file' (pos 1)")
			}
		}
		var targetVal val.Value
		if pa[0].IsInt64() {
			targetVal = pa[0]
		} else {
			targetVal = val.NewStringVal(pathArg(ctx, pa[0]))
		}
		mode := "r"
		if len(pa) >= 2 && pa[1].IsAnyString() {
			mode = pa[1].AsString()
		} else if v, ok := kwArg(args, kwnames, "mode"); ok && v.IsAnyString() {
			mode = v.AsString()
		}
		return openFileBuiltinVal(targetVal, mode)
	})

	ioMod.Dict.Set(val.InternString("open"), openFn)
	ioMod.Dict.Set(val.InternString("DEFAULT_BUFFER_SIZE"), val.Int64Val(8192))
	ioMod.Dict.Set(val.InternString("SEEK_SET"), val.Int64Val(0))
	ioMod.Dict.Set(val.InternString("SEEK_CUR"), val.Int64Val(1))
	ioMod.Dict.Set(val.InternString("SEEK_END"), val.Int64Val(2))
	ioMod.Dict.Set(val.InternString("text_encoding"), val.NewBuiltinFuncVal("text_encoding", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) >= 1 && !args[0].IsNone() {
			return args[0]
		}
		return val.NewStringVal("locale")
	}))

	return ioModVal
}
