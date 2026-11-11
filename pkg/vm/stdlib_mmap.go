package vm

import (
	"os"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/servusdei2018/fython/pkg/val"
)

// mmapType is the native type of the mmap.mmap class.
var mmapType *val.PyType

// pyMmap is the native payload of an mmap.mmap object.
type pyMmap struct {
	inst     val.PyInstance // must be first: Header lives at offset 0
	file     *os.File
	data     []byte
	pos      int
	readOnly bool
	closed   bool
	mu       sync.RWMutex
}

// initMmapType creates the native mmap type.
func initMmapType() {
	if mmapType != nil {
		return
	}
	mmapType = val.MustNewType("mmap", []*val.PyType{val.ObjectType}, nil)
	mmapType.Dict.Set(val.InternString("__module__"), val.InternString("mmap"))

	mmapType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: mmap() takes at least 2 arguments")
		}
		fileno := pyInt(args, 0, "mmap")
		length := pyInt(args, 1, "mmap")
		if length < 0 {
			panic("ValueError: mmap() length must be non-negative")
		}
		access := val.ACCESS_WRITE
		for i, k := range kwnames {
			if k.AsString() == "access" && i+1 < len(args) {
				access = int(args[i+1].AsInt64())
			}
		}
		if access == val.ACCESS_DEFAULT {
			access = val.ACCESS_WRITE
		}
		prot := unix.PROT_READ
		if access != val.ACCESS_READ {
			prot |= unix.PROT_WRITE
		}
		flags := unix.MAP_SHARED
		if access == val.ACCESS_COPY {
			prot = unix.PROT_READ | unix.PROT_WRITE
			flags = unix.MAP_PRIVATE
		}
		data, err := unix.Mmap(fileno, 0, length, prot, flags)
		if err != nil {
			panic("OSError: " + err.Error())
		}
		m := &pyMmap{
			inst:     val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: mmapType}, Shape: mmapType.RootShape},
			file:     os.NewFile(uintptr(fileno), "mmap"),
			data:     data,
			readOnly: access == val.ACCESS_READ,
		}
		return val.ObjectVal(&m.inst.Header)
	}

	mmapType.Slots.TpAsBuffer = func(ctx *val.ExecContext, obj val.Value, view *val.PyBuffer, flags int) error {
		m := (*pyMmap)(val.AsPointer(obj))
		view.Buf = m.data
		view.ReadOnly = m.readOnly
		view.ItemSize = 1
		view.Format = "B"
		view.NDim = 1
		view.Shape = []int{len(m.data)}
		view.Strides = []int{1}
		return nil
	}

	setMethod := func(name string, fn func(ctx *val.ExecContext, m *pyMmap, args []val.Value) val.Value) {
		mmapType.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal("mmap."+name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			m := (*pyMmap)(val.AsPointer(args[0]))
			m.mu.Lock()
			defer m.mu.Unlock()
			return fn(ctx, m, args)
		}))
	}

	setMethod("read", func(ctx *val.ExecContext, m *pyMmap, args []val.Value) val.Value {
		if m.closed {
			panic("ValueError: mmap closed or invalid")
		}
		n := len(m.data) - m.pos
		if len(args) > 1 && args[1].IsInt64() {
			n = int(args[1].AsInt64())
		}
		if n < 0 {
			n = 0
		}
		if m.pos+n > len(m.data) {
			n = len(m.data) - m.pos
		}
		out := make([]byte, n)
		copy(out, m.data[m.pos:m.pos+n])
		m.pos += n
		return val.NewBytesVal(out)
	})

	setMethod("write", func(ctx *val.ExecContext, m *pyMmap, args []val.Value) val.Value {
		if m.closed {
			panic("ValueError: mmap closed or invalid")
		}
		if m.readOnly {
			panic("TypeError: mmap object is read-only")
		}
		if len(args) < 2 || !args[1].IsBytes() {
			panic("TypeError: write() argument must be bytes-like")
		}
		data := args[1].AsBytes().Data
		if m.pos+len(data) > len(m.data) {
			panic("ValueError: data out of range")
		}
		copy(m.data[m.pos:], data)
		m.pos += len(data)
		return val.Int64Val(int64(len(data)))
	})

	setMethod("seek", func(ctx *val.ExecContext, m *pyMmap, args []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: seek() takes at least 2 arguments")
		}
		pos := pyInt(args, 1, "seek")
		whence := 0
		if len(args) > 2 {
			whence = pyInt(args, 2, "seek")
		}
		switch whence {
		case 0:
			m.pos = pos
		case 1:
			m.pos += pos
		case 2:
			m.pos = len(m.data) + pos
		default:
			panic("ValueError: invalid whence")
		}
		return val.Int64Val(int64(m.pos))
	})

	setMethod("tell", func(ctx *val.ExecContext, m *pyMmap, args []val.Value) val.Value {
		return val.Int64Val(int64(m.pos))
	})

	setMethod("flush", func(ctx *val.ExecContext, m *pyMmap, args []val.Value) val.Value {
		return val.NoneVal()
	})

	setMethod("close", func(ctx *val.ExecContext, m *pyMmap, args []val.Value) val.Value {
		if !m.closed {
			m.closed = true
			if len(m.data) > 0 {
				_ = unix.Munmap(m.data)
				m.data = nil
			}
		}
		return val.NoneVal()
	})

	setMethod("size", func(ctx *val.ExecContext, m *pyMmap, args []val.Value) val.Value {
		return val.Int64Val(int64(len(m.data)))
	})

	setMethod("readline", func(ctx *val.ExecContext, m *pyMmap, args []val.Value) val.Value {
		for i := m.pos; i < len(m.data); i++ {
			if m.data[i] == '\n' {
				out := make([]byte, i-m.pos+1)
				copy(out, m.data[m.pos:i+1])
				m.pos = i + 1
				return val.NewBytesVal(out)
			}
		}
		out := make([]byte, len(m.data)-m.pos)
		copy(out, m.data[m.pos:])
		m.pos = len(m.data)
		return val.NewBytesVal(out)
	})

	mmapType.Dict.Set(val.InternString("__len__"), val.NewBuiltinFuncVal("mmap.__len__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		m := (*pyMmap)(val.AsPointer(args[0]))
		return val.Int64Val(int64(len(m.data)))
	}))

	mmapType.Dict.Set(val.InternString("__getitem__"), val.NewBuiltinFuncVal("mmap.__getitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		m := (*pyMmap)(val.AsPointer(args[0]))
		if len(args) < 2 {
			panic("TypeError: mmap indices must be integers")
		}
		if args[1].IsSlice() {
			sl := args[1].AsSlice()
			start, _, _, length, err := val.NormalizeSliceIndices(sl.Start, sl.Stop, sl.Step, len(m.data))
			if err != nil {
				panic("ValueError: " + err.Error())
			}
			out := make([]byte, length)
			for i := range length {
				out[i] = m.data[start+i]
			}
			return val.NewBytesVal(out)
		}
		if !args[1].IsInt64() {
			panic("TypeError: mmap indices must be integers")
		}
		idx := int(args[1].AsInt64())
		if idx < 0 {
			idx += len(m.data)
		}
		if idx < 0 || idx >= len(m.data) {
			panic("IndexError: mmap index out of range")
		}
		return val.Int64Val(int64(m.data[idx]))
	}))

	mmapType.Dict.Set(val.InternString("__setitem__"), val.NewBuiltinFuncVal("mmap.__setitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		m := (*pyMmap)(val.AsPointer(args[0]))
		if len(args) < 3 || !args[1].IsInt64() {
			panic("TypeError: mmap indices must be integers")
		}
		idx := int(args[1].AsInt64())
		if idx < 0 {
			idx += len(m.data)
		}
		if idx < 0 || idx >= len(m.data) {
			panic("IndexError: mmap index out of range")
		}
		m.data[idx] = byte(args[2].AsInt64())
		return val.NoneVal()
	}))

	mmapType.Dict.Set(val.InternString("__enter__"), val.NewBuiltinFuncVal("mmap.__enter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return args[0]
	}))

	mmapType.Dict.Set(val.InternString("__exit__"), val.NewBuiltinFuncVal("mmap.__exit__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		m := (*pyMmap)(val.AsPointer(args[0]))
		if !m.closed {
			m.closed = true
			if len(m.data) > 0 {
				_ = unix.Munmap(m.data)
				m.data = nil
			}
		}
		return val.NoneVal()
	}))

	mmapType.InvalidateVersion()
}

// createMmapModule builds the mmap module.
func (e *Engine) createMmapModule() val.Value {
	initMmapType()
	modVal := val.NewModuleVal("mmap", "memory mapped files")
	mod := modVal.AsModule()
	mod.Dict.Set(val.InternString("mmap"), val.TypeVal(mmapType))
	mod.Dict.Set(val.InternString("error"), val.TypeVal(val.OSErrorType))
	mod.Dict.Set(val.InternString("ACCESS_READ"), val.Int64Val(val.ACCESS_READ))
	mod.Dict.Set(val.InternString("ACCESS_WRITE"), val.Int64Val(val.ACCESS_WRITE))
	mod.Dict.Set(val.InternString("ACCESS_COPY"), val.Int64Val(val.ACCESS_COPY))
	mod.Dict.Set(val.InternString("ACCESS_DEFAULT"), val.Int64Val(val.ACCESS_DEFAULT))
	mod.Dict.Set(val.InternString("MAP_SHARED"), val.Int64Val(val.MAP_SHARED))
	mod.Dict.Set(val.InternString("MAP_PRIVATE"), val.Int64Val(val.MAP_PRIVATE))
	mod.Dict.Set(val.InternString("PROT_READ"), val.Int64Val(val.PROT_READ))
	mod.Dict.Set(val.InternString("PROT_WRITE"), val.Int64Val(val.PROT_WRITE))
	mod.Dict.Set(val.InternString("PROT_EXEC"), val.Int64Val(val.PROT_EXEC))
	return modVal
}
