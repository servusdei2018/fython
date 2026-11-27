package vm

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"
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
	inst     val.PyInstance // must be first: Header lives at offset 0
	buf      []byte
	pos      int
	isClosed bool
	mu       sync.RWMutex
}

const (
	sioNewlineLF    uint8 = 0 // default "\n": no translation
	sioNewlineEmpty uint8 = 1 // "": no translation
	sioNewlineNone  uint8 = 2 // None: universal newlines (\r\n, \r -> \n)
	sioNewlineCR    uint8 = 3 // "\r": translate \n -> \r
	sioNewlineCRLF  uint8 = 4 // "\r\n": translate \n -> \r\n
)

type strIOBufBox struct {
	b []byte
}

type pyStringIO struct {
	inst        val.PyInstance // must be first: Header lives at offset 0
	buf         []byte
	pos         int // Unicode code-point offset (== bytePos when asciiOnly)
	bytePos     int // cached byte offset into buf (-1 if unresolved)
	asciiOnly   bool
	newlineMode uint8
	isClosed    bool
	mu          sync.RWMutex
	box         *strIOBufBox
	cleanup     runtime.Cleanup
}

func isASCIIBytes(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 {
			return false
		}
	}
	return true
}

func isASCIIString(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func translateSIONewlines(b []byte, mode uint8) []byte {
	switch mode {
	case sioNewlineLF, sioNewlineEmpty:
		return b
	case sioNewlineNone:
		return normalizeUniversalNewlines(b)
	case sioNewlineCR:
		if bytes.IndexByte(b, '\n') < 0 {
			return b
		}
		out := make([]byte, len(b))
		for i, c := range b {
			if c == '\n' {
				out[i] = '\r'
			} else {
				out[i] = c
			}
		}
		return out
	case sioNewlineCRLF:
		if bytes.IndexByte(b, '\n') < 0 {
			return b
		}
		out := make([]byte, 0, len(b)+8)
		for _, c := range b {
			if c == '\n' {
				out = append(out, '\r', '\n')
			} else {
				out = append(out, c)
			}
		}
		return out
	}
	return b
}

func runeIndexToByteOffset(b []byte, runeIdx int) int {
	if runeIdx <= 0 {
		return 0
	}
	off := 0
	for r := 0; r < runeIdx && off < len(b); r++ {
		if b[off] < 0x80 {
			off++
		} else {
			_, size := utf8.DecodeRune(b[off:])
			off += size
		}
	}
	return off
}

func (sio *pyStringIO) runeLenLocked() int {
	if sio.asciiOnly {
		return len(sio.buf)
	}
	return utf8.RuneCount(sio.buf)
}

func (sio *pyStringIO) resolveBytePosLocked() int {
	if sio.asciiOnly {
		if sio.pos > len(sio.buf) {
			return len(sio.buf)
		}
		sio.bytePos = sio.pos
		return sio.pos
	}
	if sio.bytePos >= 0 && sio.bytePos <= len(sio.buf) {
		return sio.bytePos
	}
	bOff := runeIndexToByteOffset(sio.buf, sio.pos)
	sio.bytePos = bOff
	return bOff
}

var stringIOBufPool = sync.Pool{
	New: func() any {
		return &strIOBufBox{b: make([]byte, 0, 131072)}
	},
}

func releaseStrIOBox(box *strIOBufBox) {
	if box == nil || box.b == nil {
		return
	}
	if cap(box.b) >= 131072 && cap(box.b) <= 4<<20 {
		box.b = box.b[:0]
		stringIOBufPool.Put(box)
		return
	}
	box.b = nil
}

func newStringIO(cls *val.PyType) *pyStringIO {
	box := stringIOBufPool.Get().(*strIOBufBox)
	sio := &pyStringIO{
		inst: val.PyInstance{
			Header: val.ObjectHeader{Kind: val.KindInstance, Type: cls},
			Shape:  cls.RootShape,
		},
		buf:         box.b[:0],
		asciiOnly:   true,
		newlineMode: sioNewlineLF,
		box:         box,
	}
	sio.cleanup = runtime.AddCleanup(sio, releaseStrIOBox, box)
	return sio
}

func appendStringIOWrite(sio *pyStringIO, arg val.Value) int {
	oldLen := len(sio.buf)
	var ok bool
	sio.buf, ok = val.AppendValueBytes(sio.buf, arg)
	if !ok {
		s := arg.AsString()
		view := unsafe.Slice(unsafe.StringData(s), len(s))
		sio.buf = append(sio.buf, view...)
	}
	if sio.box != nil && cap(sio.buf) != cap(sio.box.b) {
		sio.box.b = sio.buf[:0]
	}
	added := sio.buf[oldLen:]
	if isASCIIBytes(added) {
		sio.pos = len(sio.buf)
		sio.bytePos = len(sio.buf)
		return len(added)
	}
	sio.asciiOnly = false
	runes := utf8.RuneCount(added)
	sio.pos += runes
	sio.bytePos = len(sio.buf)
	return runes
}

func releaseStringIOBuf(sio *pyStringIO) {
	sio.cleanup.Stop()
	if sio.box != nil {
		box := sio.box
		sio.box = nil
		if sio.buf != nil && cap(sio.buf) > cap(box.b) {
			box.b = sio.buf[:0]
		} else {
			box.b = box.b[:0]
		}
		releaseStrIOBox(box)
	}
	sio.buf = nil
	sio.pos = 0
	sio.bytePos = 0
	sio.isClosed = true
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
	pooledBuf  *[]byte
}

var fileBufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, 32768)
		return &b
	},
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

func (pf *pyFile) ensureReader() *bufio.Reader {
	if pf.reader == nil && pf.f != nil {
		pf.reader = bufio.NewReader(pf.f)
	}
	return pf.reader
}

func normalizeUniversalNewlines(b []byte) []byte {
	if bytes.IndexByte(b, '\r') < 0 {
		return b
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] == '\r' {
			if i+1 < len(b) && b[i+1] == '\n' {
				i++
			}
			out = append(out, '\n')
		} else {
			out = append(out, b[i])
		}
	}
	return out
}

func (pf *pyFile) readResultVal(data []byte) val.Value {
	if pf.binary {
		if len(data) == 0 {
			return val.NewBytesVal(nil)
		}
		return val.NewBytesVal(data)
	}
	data = normalizeUniversalNewlines(data)
	if len(data) == 0 {
		return val.NewStringVal("")
	}
	s := unsafe.String(unsafe.SliceData(data), len(data))
	ps := &val.PyString{
		Header: val.ObjectHeader{
			Kind: val.KindString,
			Type: val.StrType,
		},
		Data: s,
	}
	return val.ObjectVal(&ps.Header)
}

func (pf *pyFile) readAllBytes(isMT bool) (data []byte, closed bool, err error) {
	if isMT {
		pf.mu.Lock()
		defer pf.mu.Unlock()
	}
	if pf.isClosed {
		return nil, true, nil
	}
	pf.flushLocked()
	if pf.f == nil {
		return nil, false, nil
	}
	if pf.reader != nil && pf.reader.Buffered() > 0 {
		data, err = io.ReadAll(pf.reader)
		return data, false, err
	}
	if stat, statErr := pf.f.Stat(); statErr == nil && stat.Mode().IsRegular() {
		if curPos, seekErr := pf.f.Seek(0, io.SeekCurrent); seekErr == nil {
			rem := max(stat.Size()-curPos, 0)
			initCap := max(int(rem)+1, 512)
			buf := make([]byte, 0, initCap)
			for {
				if len(buf) == cap(buf) {
					newCap := max(cap(buf)*2, 512)
					grown := make([]byte, len(buf), newCap)
					copy(grown, buf)
					buf = grown
				}
				n, rerr := pf.f.Read(buf[len(buf):cap(buf)])
				buf = buf[:len(buf)+n]
				if rerr != nil {
					if rerr == io.EOF {
						return buf, false, nil
					}
					return buf, false, rerr
				}
			}
		}
	}
	data, err = io.ReadAll(pf.ensureReader())
	return data, false, err
}

func readFileTextRunes(rdr *bufio.Reader, n int) []byte {
	var out []byte
	for count := 0; count < n; {
		// Fast path: inspect buffered bytes for a pure-ASCII, CR-free prefix.
		if avail := rdr.Buffered(); avail > 0 {
			need := n - count
			if avail > need {
				avail = need
			}
			peeked, _ := rdr.Peek(avail)
			asciiEnd := 0
			for asciiEnd < len(peeked) {
				c := peeked[asciiEnd]
				if c >= 0x80 || c == '\r' {
					break
				}
				asciiEnd++
			}
			if asciiEnd > 0 {
				if out == nil {
					out = make([]byte, 0, n)
				}
				out = append(out, peeked[:asciiEnd]...)
				_, _ = rdr.Discard(asciiEnd)
				count += asciiEnd
				continue
			}
		}
		r, _, err := rdr.ReadRune()
		if err != nil {
			break
		}
		if r == '\r' {
			if b, perr := rdr.Peek(1); perr == nil && len(b) == 1 && b[0] == '\n' {
				_, _ = rdr.Discard(1)
			}
			out = append(out, '\n')
			count++
			continue
		}
		out = utf8.AppendRune(out, r)
		count++
	}
	return out
}

// EnsureIOTypes initializes BytesIOType, StringIOType, and FileType.
func EnsureIOTypes() {
	initIOTypes()
}

func initIOTypes() {
	ioInitOnce.Do(func() {
		// BytesIO
		BytesIOType = val.MustNewType("BytesIO", []*val.PyType{val.ObjectType}, nil)
		BytesIOType.Dict.Set(val.InternString("read"), val.NewBuiltinFuncVal("read", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewException(val.TypeErrorType, "read() takes at least 0 arguments"))
				return val.NoneVal()
			}
			bio := (*pyBytesIO)(val.AsPointer(args[0]))
			isMT := val.IsMultiThreaded()
			if isMT {
				bio.mu.Lock()
				defer bio.mu.Unlock()
			}
			if bio.isClosed {
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
			n := -1
			if len(args) >= 2 && args[1].IsInt64() {
				n = int(args[1].AsInt64())
			}
			if bio.pos >= len(bio.buf) {
				return val.NewBytesVal(nil)
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
				ctx.Raise(val.NewException(val.TypeErrorType, "write() takes exactly one argument"))
				return val.NoneVal()
			}
			bio := (*pyBytesIO)(val.AsPointer(args[0]))
			isMT := val.IsMultiThreaded()
			if isMT {
				bio.mu.Lock()
				defer bio.mu.Unlock()
			}
			if bio.isClosed {
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
			var data []byte
			switch {
			case args[1].IsBytes():
				data = args[1].AsBytes().Data
			case args[1].IsByteArray():
				data = args[1].AsByteArray().Data
			case args[1].IsMemoryView():
				data = args[1].AsMemoryView().Buf
			default:
				ctx.Raise(val.NewException(val.TypeErrorType, "a bytes-like object is required"))
				return val.NoneVal()
			}
			n := len(data)
			if n == 0 {
				return val.Int64Val(0)
			}
			end := bio.pos + n
			switch {
			case bio.pos == len(bio.buf):
				bio.buf = append(bio.buf, data...)
			case end <= len(bio.buf):
				copy(bio.buf[bio.pos:], data)
			default:
				oldLen := len(bio.buf)
				if end > cap(bio.buf) {
					newCap := max(cap(bio.buf)*2, end, 64)
					newBuf := make([]byte, end, newCap)
					copy(newBuf, bio.buf)
					bio.buf = newBuf
				} else {
					bio.buf = bio.buf[:end]
					if bio.pos > oldLen {
						clear(bio.buf[oldLen:bio.pos])
					}
				}
				copy(bio.buf[bio.pos:], data)
			}
			bio.pos = end
			return val.Int64Val(int64(n))
		}))

		BytesIOType.Dict.Set(val.InternString("getvalue"), val.NewBuiltinFuncVal("getvalue", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			bio := (*pyBytesIO)(val.AsPointer(args[0]))
			isMT := val.IsMultiThreaded()
			if isMT {
				bio.mu.RLock()
				defer bio.mu.RUnlock()
			}
			if bio.isClosed {
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
			if len(bio.buf) == 0 {
				return val.NewBytesVal(nil)
			}
			res := make([]byte, len(bio.buf))
			copy(res, bio.buf)
			return val.NewBytesVal(res)
		}))

		BytesIOType.Dict.Set(val.InternString("seek"), val.NewBuiltinFuncVal("seek", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				ctx.Raise(val.NewException(val.TypeErrorType, "seek() takes at least 1 argument"))
				return val.NoneVal()
			}
			bio := (*pyBytesIO)(val.AsPointer(args[0]))
			bio.mu.Lock()
			defer bio.mu.Unlock()
			if bio.isClosed {
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
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
			if bio.isClosed {
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
			return val.Int64Val(int64(bio.pos))
		}))

		BytesIOType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			bio := (*pyBytesIO)(val.AsPointer(args[0]))
			bio.mu.Lock()
			bio.buf = nil
			bio.pos = 0
			bio.isClosed = true
			bio.mu.Unlock()
			return val.NoneVal()
		}))

		BytesIOType.Dict.Set(val.InternString("readline"), val.NewBuiltinFuncVal("readline", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			bio := (*pyBytesIO)(val.AsPointer(args[0]))
			bio.mu.Lock()
			defer bio.mu.Unlock()
			if bio.isClosed {
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
			if bio.pos >= len(bio.buf) {
				return val.NewBytesVal(nil)
			}
			start := bio.pos
			nl := bytes.IndexByte(bio.buf[start:], '\n')
			if nl >= 0 {
				end := start + nl + 1
				bio.pos = end
				res := make([]byte, end-start)
				copy(res, bio.buf[start:end])
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
			if bio.isClosed {
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
			var lines []val.Value
			for bio.pos < len(bio.buf) {
				start := bio.pos
				nl := bytes.IndexByte(bio.buf[start:], '\n')
				var end int
				if nl >= 0 {
					end = start + nl + 1
				} else {
					end = len(bio.buf)
				}
				bio.pos = end
				res := make([]byte, end-start)
				copy(res, bio.buf[start:end])
				lines = append(lines, val.NewBytesVal(res))
			}
			return val.NewListVal(lines...)
		}))

		BytesIOType.Slots.TpIter = func(ctx *val.ExecContext, o val.Value) val.Value {
			return val.NewIterValWithNext(func() (val.Value, bool) {
				bio := (*pyBytesIO)(val.AsPointer(o))
				bio.mu.Lock()
				defer bio.mu.Unlock()
				if bio.isClosed || bio.pos >= len(bio.buf) {
					return val.NoneVal(), false
				}
				start := bio.pos
				nl := bytes.IndexByte(bio.buf[start:], '\n')
				var end int
				if nl >= 0 {
					end = start + nl + 1
				} else {
					end = len(bio.buf)
				}
				bio.pos = end
				res := make([]byte, end-start)
				copy(res, bio.buf[start:end])
				return val.NewBytesVal(res), true
			})
		}
		BytesIOType.Dict.Set(val.InternString("__iter__"), val.NewBuiltinFuncVal("__iter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewException(val.TypeErrorType, "__iter__() missing self"))
				return val.NoneVal()
			}
			return BytesIOType.Slots.TpIter(ctx, args[0])
		}))

		BytesIOType.Dict.Set(val.InternString("truncate"), val.NewBuiltinFuncVal("truncate", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			bio := (*pyBytesIO)(val.AsPointer(args[0]))
			bio.mu.Lock()
			defer bio.mu.Unlock()
			if bio.isClosed {
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
			size := bio.pos
			if len(args) > 1 && !args[1].IsNone() && args[1].IsInt64() {
				size = int(args[1].AsInt64())
			}
			if size < 0 {
				ctx.Raise(val.NewException(val.ValueErrorType, "Negative size not allowed"))
				return val.NoneVal()
			}
			// CPython semantics: truncate does not move pos, and size > len(buf)
			// leaves the buffer unchanged while returning size.
			if size < len(bio.buf) {
				bio.buf = bio.buf[:size]
			}
			return val.Int64Val(int64(size))
		}))

		BytesIOType.Dict.Set(val.DunderNew, val.NewBuiltinFuncVal("__new__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			cls := BytesIOType
			if len(args) > 0 && args[0].IsType() {
				cls = args[0].AsType()
			}
			bio := &pyBytesIO{
				inst: val.PyInstance{
					Header: val.ObjectHeader{Kind: val.KindInstance, Type: cls},
					Shape:  cls.RootShape,
				},
				buf: nil,
			}
			return val.ObjectVal(&bio.inst.Header)
		}))
		BytesIOType.Dict.Set(val.DunderInit, val.NewBuiltinFuncVal("__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				return val.NoneVal()
			}
			bio := (*pyBytesIO)(val.AsPointer(args[0]))
			bio.mu.Lock()
			defer bio.mu.Unlock()
			pa := posArgs(args[1:], kwnames)
			var initBytes []byte
			var src val.Value
			if len(pa) > 0 {
				src = pa[0]
			} else if v, ok := kwArg(args[1:], kwnames, "initial_bytes"); ok {
				src = v
			}
			if !src.IsNone() && src.HeaderPtr() != nil {
				switch {
				case src.IsBytes():
					initBytes = append([]byte(nil), src.AsBytes().Data...)
				case src.IsByteArray():
					initBytes = append([]byte(nil), src.AsByteArray().Data...)
				case src.IsMemoryView():
					initBytes = append([]byte(nil), src.AsMemoryView().Buf...)
				}
			}
			bio.buf = initBytes
			bio.pos = 0
			return val.NoneVal()
		}))
		BytesIOType.InvalidateVersion()

		// StringIO
		StringIOType = val.MustNewType("StringIO", []*val.PyType{val.ObjectType}, nil)
		StringIOType.Dict.Set(val.InternString("read"), val.NewBuiltinFuncVal("read", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			sio := (*pyStringIO)(val.AsPointer(args[0]))
			isMT := val.IsMultiThreaded()
			if isMT {
				sio.mu.Lock()
				defer sio.mu.Unlock()
			}
			if sio.isClosed {
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
			n := -1
			if len(args) >= 2 && args[1].IsInt64() {
				n = int(args[1].AsInt64())
			}
			if sio.asciiOnly {
				if sio.pos >= len(sio.buf) {
					return val.NewStringVal("")
				}
				if n < 0 || sio.pos+n > len(sio.buf) {
					n = len(sio.buf) - sio.pos
				}
				if n <= 0 {
					return val.NewStringVal("")
				}
				res := sio.buf[sio.pos : sio.pos+n]
				sio.pos += n
				sio.bytePos = sio.pos
				return val.NewStringVal(string(res))
			}
			bStart := sio.resolveBytePosLocked()
			if bStart >= len(sio.buf) {
				return val.NewStringVal("")
			}
			if n == 0 {
				return val.NewStringVal("")
			}
			if n < 0 {
				res := sio.buf[bStart:]
				sio.pos += utf8.RuneCount(res)
				sio.bytePos = len(sio.buf)
				return val.NewStringVal(string(res))
			}
			bEnd := bStart
			readRunes := 0
			for readRunes < n && bEnd < len(sio.buf) {
				if sio.buf[bEnd] < 0x80 {
					bEnd++
				} else {
					_, sz := utf8.DecodeRune(sio.buf[bEnd:])
					bEnd += sz
				}
				readRunes++
			}
			sio.pos += readRunes
			sio.bytePos = bEnd
			return val.NewStringVal(string(sio.buf[bStart:bEnd]))
		}))

		StringIOType.Dict.Set(val.InternString("write"), val.NewBuiltinFuncVal("write", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				ctx.Raise(val.NewException(val.TypeErrorType, "write() takes exactly one argument"))
				return val.NoneVal()
			}
			sio := (*pyStringIO)(val.AsPointer(args[0]))
			isMT := val.IsMultiThreaded()
			if isMT {
				sio.mu.Lock()
				defer sio.mu.Unlock()
			}
			if sio.isClosed {
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
			v := args[1]
			if !v.IsAnyString() {
				ctx.Raise(val.NewException(val.TypeErrorType, "string argument expected, got '"+v.Type().Name+"'"))
				return val.NoneVal()
			}
			if sio.asciiOnly && sio.newlineMode == sioNewlineLF && sio.pos == len(sio.buf) {
				written := appendStringIOWrite(sio, v)
				return val.Int64Val(int64(written))
			}
			var stackTmp [64]byte
			raw, ok := val.AppendValueBytes(stackTmp[:0], v)
			if !ok {
				s := v.AsString()
				view := unsafe.Slice(unsafe.StringData(s), len(s))
				raw = append(stackTmp[:0], view...)
			}
			data := translateSIONewlines(raw, sio.newlineMode)
			origRunes := len(raw)
			if !isASCIIBytes(raw) {
				sio.asciiOnly = false
				origRunes = utf8.RuneCount(raw)
			}
			dataRunes := len(data)
			if !isASCIIBytes(data) {
				sio.asciiOnly = false
				dataRunes = utf8.RuneCount(data)
			}
			bufRunes := sio.runeLenLocked()
			switch {
			case sio.pos == bufRunes:
				sio.buf = append(sio.buf, data...)
				sio.pos += dataRunes
				sio.bytePos = len(sio.buf)
			case sio.pos > bufRunes:
				pad := sio.pos - bufRunes
				need := len(sio.buf) + pad + len(data)
				if need > cap(sio.buf) {
					newCap := max(cap(sio.buf)*2, need)
					newBuf := make([]byte, len(sio.buf)+pad, newCap)
					copy(newBuf, sio.buf)
					newBuf = append(newBuf, data...)
					sio.buf = newBuf
				} else {
					oldLen := len(sio.buf)
					sio.buf = sio.buf[:oldLen+pad]
					clear(sio.buf[oldLen:])
					sio.buf = append(sio.buf, data...)
				}
				sio.pos += dataRunes
				sio.bytePos = len(sio.buf)
			default:
				bStart := sio.resolveBytePosLocked()
				if sio.asciiOnly {
					end := bStart + len(data)
					if end <= len(sio.buf) {
						copy(sio.buf[bStart:], data)
					} else {
						sio.buf = append(sio.buf[:bStart], data...)
					}
					sio.pos = end
					sio.bytePos = end
				} else {
					bEnd := bStart
					for r := 0; r < dataRunes && bEnd < len(sio.buf); r++ {
						if sio.buf[bEnd] < 0x80 {
							bEnd++
						} else {
							_, sz := utf8.DecodeRune(sio.buf[bEnd:])
							bEnd += sz
						}
					}
					oldSpan := bEnd - bStart
					newSpan := len(data)
					if oldSpan == newSpan {
						copy(sio.buf[bStart:bEnd], data)
					} else {
						tailLen := len(sio.buf) - bEnd
						newLen := bStart + newSpan + tailLen
						if newLen > cap(sio.buf) {
							newCap := max(cap(sio.buf)*2, newLen)
							newBuf := make([]byte, newLen, newCap)
							copy(newBuf, sio.buf[:bStart])
							copy(newBuf[bStart:], data)
							copy(newBuf[bStart+newSpan:], sio.buf[bEnd:])
							sio.buf = newBuf
						} else {
							sio.buf = sio.buf[:newLen]
							copy(sio.buf[bStart+newSpan:], sio.buf[bEnd:bEnd+tailLen])
							copy(sio.buf[bStart:], data)
						}
					}
					sio.pos += dataRunes
					sio.bytePos = bStart + newSpan
				}
			}
			if sio.box != nil && cap(sio.buf) != cap(sio.box.b) {
				sio.box.b = sio.buf[:0]
			}
			return val.Int64Val(int64(origRunes))
		}))

		StringIOType.Dict.Set(val.InternString("getvalue"), val.NewBuiltinFuncVal("getvalue", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			sio := (*pyStringIO)(val.AsPointer(args[0]))
			isMT := val.IsMultiThreaded()
			if isMT {
				sio.mu.RLock()
				defer sio.mu.RUnlock()
			}
			if sio.isClosed {
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
			n := len(sio.buf)
			if n == 0 {
				return val.NewStringVal("")
			}
			if n <= 7 {
				return val.SmallStringFromBytes(sio.buf)
			}
			// Copy the buffer: the returned string must not alias the live
			// StringIO buffer, or a later write would mutate it in place.
			copied := string(sio.buf)
			ps := &val.PyString{
				Header: val.ObjectHeader{
					Kind: val.KindString,
					Type: val.StrType,
				},
				Data: copied,
			}
			return val.ObjectVal(&ps.Header)
		}))

		StringIOType.Dict.Set(val.InternString("seek"), val.NewBuiltinFuncVal("seek", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				ctx.Raise(val.NewException(val.TypeErrorType, "seek() takes at least 1 argument"))
				return val.NoneVal()
			}
			sio := (*pyStringIO)(val.AsPointer(args[0]))
			sio.mu.Lock()
			defer sio.mu.Unlock()
			if sio.isClosed {
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
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
				sio.pos = sio.runeLenLocked() + offset
			}
			if sio.pos < 0 {
				sio.pos = 0
			}
			switch {
			case sio.asciiOnly:
				sio.bytePos = sio.pos
			case sio.pos == 0:
				sio.bytePos = 0
			default:
				sio.bytePos = -1
			}
			return val.Int64Val(int64(sio.pos))
		}))

		StringIOType.Dict.Set(val.InternString("tell"), val.NewBuiltinFuncVal("tell", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			sio := (*pyStringIO)(val.AsPointer(args[0]))
			sio.mu.RLock()
			defer sio.mu.RUnlock()
			if sio.isClosed {
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
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
			enter := val.NewBuiltinFuncVal("__enter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				if len(args) < 1 {
					ctx.Raise(val.NewException(val.TypeErrorType, "__enter__() missing self"))
					return val.NoneVal()
				}
				return args[0]
			})
			exit := val.NewBuiltinFuncVal("__exit__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				if t == StringIOType && len(args) > 0 && args[0].IsInstance() {
					sio := (*pyStringIO)(val.AsPointer(args[0]))
					sio.mu.Lock()
					releaseStringIOBuf(sio)
					sio.mu.Unlock()
				}
				return val.NoneVal()
			})
			t.Dict.Set(val.InternString("flush"), noop)
			t.Dict.Set(val.InternString("isatty"), no)
			t.Dict.Set(val.InternString("readable"), yes)
			t.Dict.Set(val.InternString("writable"), yes)
			t.Dict.Set(val.InternString("seekable"), yes)
			t.Dict.Set(val.InternString("__enter__"), enter)
			t.Dict.Set(val.InternString("__exit__"), exit)
			t.InvalidateVersion()
		}
		StringIOType.Dict.Set(val.InternString("encoding"), val.NoneVal())
		StringIOType.Dict.Set(val.InternString("errors"), val.NoneVal())
		StringIOType.Dict.Set(val.InternString("readline"), val.NewBuiltinFuncVal("readline", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			sio := (*pyStringIO)(val.AsPointer(args[0]))
			sio.mu.Lock()
			defer sio.mu.Unlock()
			if sio.isClosed {
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
			bStart := sio.resolveBytePosLocked()
			if bStart >= len(sio.buf) {
				return val.NewStringVal("")
			}
			delim := byte('\n')
			if sio.newlineMode == sioNewlineCR {
				delim = '\r'
			}
			nl := bytes.IndexByte(sio.buf[bStart:], delim)
			var bEnd int
			if nl >= 0 {
				bEnd = bStart + nl + 1
			} else {
				bEnd = len(sio.buf)
			}
			line := sio.buf[bStart:bEnd]
			if sio.asciiOnly {
				sio.pos = bEnd
			} else {
				sio.pos += utf8.RuneCount(line)
			}
			sio.bytePos = bEnd
			return val.NewStringVal(string(line))
		}))

		StringIOType.Dict.Set(val.InternString("readlines"), val.NewBuiltinFuncVal("readlines", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			sio := (*pyStringIO)(val.AsPointer(args[0]))
			sio.mu.Lock()
			defer sio.mu.Unlock()
			if sio.isClosed {
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
			delim := byte('\n')
			if sio.newlineMode == sioNewlineCR {
				delim = '\r'
			}
			var lines []val.Value
			bStart := sio.resolveBytePosLocked()
			for bStart < len(sio.buf) {
				nl := bytes.IndexByte(sio.buf[bStart:], delim)
				var bEnd int
				if nl >= 0 {
					bEnd = bStart + nl + 1
				} else {
					bEnd = len(sio.buf)
				}
				line := sio.buf[bStart:bEnd]
				if sio.asciiOnly {
					sio.pos = bEnd
				} else {
					sio.pos += utf8.RuneCount(line)
				}
				sio.bytePos = bEnd
				lines = append(lines, val.NewStringVal(string(line)))
				bStart = bEnd
			}
			return val.NewListVal(lines...)
		}))

		StringIOType.Dict.Set(val.InternString("truncate"), val.NewBuiltinFuncVal("truncate", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			sio := (*pyStringIO)(val.AsPointer(args[0]))
			sio.mu.Lock()
			defer sio.mu.Unlock()
			if sio.isClosed {
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
			size := sio.pos
			if len(args) > 1 && !args[1].IsNone() && args[1].IsInt64() {
				size = int(args[1].AsInt64())
			}
			if size < 0 {
				ctx.Raise(val.NewException(val.ValueErrorType, "Negative size not allowed"))
				return val.NoneVal()
			}
			// CPython semantics: truncate does not move pos, and size > len
			// leaves the buffer unchanged while returning size.
			if sio.asciiOnly {
				if size < len(sio.buf) {
					sio.buf = sio.buf[:size]
				}
			} else {
				bCut := runeIndexToByteOffset(sio.buf, size)
				if bCut < len(sio.buf) {
					sio.buf = sio.buf[:bCut]
					if sio.bytePos > bCut {
						sio.bytePos = -1
					}
				}
			}
			return val.Int64Val(int64(size))
		}))

		StringIOType.Slots.TpIter = func(ctx *val.ExecContext, o val.Value) val.Value {
			return val.NewIterValWithNext(func() (val.Value, bool) {
				sio := (*pyStringIO)(val.AsPointer(o))
				isMT := val.IsMultiThreaded()
				if isMT {
					sio.mu.Lock()
					defer sio.mu.Unlock()
				}
				if sio.isClosed {
					return val.NoneVal(), false
				}
				bStart := sio.resolveBytePosLocked()
				if bStart >= len(sio.buf) {
					return val.NoneVal(), false
				}
				delim := byte('\n')
				if sio.newlineMode == sioNewlineCR {
					delim = '\r'
				}
				nl := bytes.IndexByte(sio.buf[bStart:], delim)
				var bEnd int
				if nl >= 0 {
					bEnd = bStart + nl + 1
				} else {
					bEnd = len(sio.buf)
				}
				line := sio.buf[bStart:bEnd]
				if sio.asciiOnly {
					sio.pos = bEnd
				} else {
					sio.pos += utf8.RuneCount(line)
				}
				sio.bytePos = bEnd
				return val.NewStringVal(string(line)), true
			})
		}
		StringIOType.Dict.Set(val.InternString("__iter__"), val.NewBuiltinFuncVal("__iter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewException(val.TypeErrorType, "__iter__() missing self"))
				return val.NoneVal()
			}
			return StringIOType.Slots.TpIter(ctx, args[0])
		}))

		StringIOType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			sio := (*pyStringIO)(val.AsPointer(args[0]))
			sio.mu.Lock()
			releaseStringIOBuf(sio)
			sio.mu.Unlock()
			return val.NoneVal()
		}))

		StringIOType.Dict.Set(val.DunderNew, val.NewBuiltinFuncVal("__new__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			cls := StringIOType
			if len(args) > 0 && args[0].IsType() {
				cls = args[0].AsType()
			}
			sio := newStringIO(cls)
			return val.ObjectVal(&sio.inst.Header)
		}))
		StringIOType.Dict.Set(val.DunderInit, val.NewBuiltinFuncVal("__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				return val.NoneVal()
			}
			sio := (*pyStringIO)(val.AsPointer(args[0]))
			sio.mu.Lock()
			defer sio.mu.Unlock()
			pa := posArgs(args[1:], kwnames)
			var src val.Value
			if len(pa) > 0 {
				src = pa[0]
			} else if v, ok := kwArg(args[1:], kwnames, "initial_value"); ok {
				src = v
			}
			nlMode := sioNewlineLF
			var nlVal val.Value
			hasNL := false
			if len(pa) > 1 {
				nlVal = pa[1]
				hasNL = true
			} else if v, ok := kwArg(args[1:], kwnames, "newline"); ok {
				nlVal = v
				hasNL = true
			}
			if hasNL {
				if nlVal.IsNone() {
					nlMode = sioNewlineNone
				} else if nlVal.IsAnyString() {
					switch nlVal.AsString() {
					case "\n":
						nlMode = sioNewlineLF
					case "":
						nlMode = sioNewlineEmpty
					case "\r":
						nlMode = sioNewlineCR
					case "\r\n":
						nlMode = sioNewlineCRLF
					default:
						ctx.Raise(val.NewException(val.ValueErrorType, "illegal newline value: "+nlVal.AsString()))
						return val.NoneVal()
					}
				}
			}
			sio.newlineMode = nlMode
			var initBytes []byte
			if !src.IsNone() && src.IsAnyString() {
				initBytes, _ = val.AppendValueBytes(sio.buf[:0], src)
				if nlMode != sioNewlineLF && nlMode != sioNewlineEmpty {
					initBytes = translateSIONewlines(initBytes, nlMode)
				}
			} else {
				if sio.buf == nil {
					initBytes = make([]byte, 0, 131072)
				} else {
					initBytes = sio.buf[:0]
				}
			}
			sio.buf = initBytes
			sio.asciiOnly = isASCIIBytes(initBytes)
			sio.pos = 0
			sio.bytePos = 0
			sio.isClosed = false
			return val.NoneVal()
		}))
		StringIOType.InvalidateVersion()

		// FileType
		FileType = val.MustNewType("_io.TextIOWrapper", []*val.PyType{val.ObjectType}, nil)

		// Context Manager protocol: __enter__, __exit__
		FileType.Dict.Set(val.InternString("__enter__"), val.NewBuiltinFuncVal("__enter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewException(val.TypeErrorType, "__enter__() takes 0 arguments"))
				return val.NoneVal()
			}
			return args[0]
		}))

		FileType.Dict.Set(val.InternString("__exit__"), val.NewBuiltinFuncVal("__exit__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewException(val.TypeErrorType, "__exit__() takes arguments"))
				return val.NoneVal()
			}
			pf := (*pyFile)(val.AsPointer(args[0]))
			pf.mu.Lock()
			if !pf.isClosed && pf.f != nil {
				pf.flushLocked()
				if pf.pooledBuf != nil {
					fileBufferPool.Put(pf.pooledBuf)
					pf.pooledBuf = nil
					pf.writeBuf = nil
				}
				if pf.f != os.Stdin && pf.f != os.Stdout && pf.f != os.Stderr {
					_ = pf.f.Close()
					pf.isClosed = true
				}
			}
			pf.mu.Unlock()
			return val.NoneVal()
		}))

		FileType.Dict.Set(val.InternString("read"), val.NewBuiltinFuncVal("read", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			pf := (*pyFile)(val.AsPointer(args[0]))
			isMT := val.IsMultiThreaded()
			n := -1
			if len(args) >= 2 && args[1].IsInt64() {
				n = int(args[1].AsInt64())
			}
			if n < 0 {
				content, closed, err := pf.readAllBytes(isMT)
				if closed {
					ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
					return val.NoneVal()
				}
				if err != nil && len(content) == 0 {
					if pf.binary {
						return val.NewBytesVal(nil)
					}
					return val.NewStringVal("")
				}
				return pf.readResultVal(content)
			}
			if isMT {
				pf.mu.Lock()
			}
			if pf.isClosed {
				if isMT {
					pf.mu.Unlock()
				}
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
			if n == 0 {
				if isMT {
					pf.mu.Unlock()
				}
				if pf.binary {
					return val.NewBytesVal(nil)
				}
				return val.NewStringVal("")
			}
			pf.flushLocked()
			if pf.binary {
				buf := make([]byte, n)
				readN, _ := io.ReadFull(pf.ensureReader(), buf)
				if isMT {
					pf.mu.Unlock()
				}
				if readN <= 0 {
					return val.NewBytesVal(nil)
				}
				return val.NewBytesVal(buf[:readN])
			}
			out := readFileTextRunes(pf.ensureReader(), n)
			if isMT {
				pf.mu.Unlock()
			}
			if len(out) == 0 {
				return val.NewStringVal("")
			}
			s := unsafe.String(unsafe.SliceData(out), len(out))
			ps := &val.PyString{
				Header: val.ObjectHeader{
					Kind: val.KindString,
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
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
			pf.flushLocked()
			line, err := pf.ensureReader().ReadBytes('\n')
			if err != nil && len(line) == 0 {
				if pf.binary {
					return val.NewBytesVal(nil)
				}
				return val.NewStringVal("")
			}
			return pf.readResultVal(line)
		}))

		FileType.Dict.Set(val.InternString("readlines"), val.NewBuiltinFuncVal("readlines", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			pf := (*pyFile)(val.AsPointer(args[0]))
			pf.mu.Lock()
			defer pf.mu.Unlock()
			if pf.isClosed {
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
			pf.flushLocked()
			rdr := pf.ensureReader()
			var lines []val.Value
			for {
				line, err := rdr.ReadBytes('\n')
				if len(line) > 0 {
					lines = append(lines, pf.readResultVal(line))
				}
				if err != nil {
					break
				}
			}
			return val.NewListVal(lines...)
		}))

		FileType.Dict.Set(val.InternString("seek"), val.NewBuiltinFuncVal("seek", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				ctx.Raise(val.NewException(val.TypeErrorType, "seek() takes at least 1 argument"))
				return val.NoneVal()
			}
			pf := (*pyFile)(val.AsPointer(args[0]))
			pf.mu.Lock()
			defer pf.mu.Unlock()
			if pf.isClosed {
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
			pf.flushLocked()
			offset := args[1].AsInt64()
			whence := io.SeekStart
			if len(args) >= 3 && args[2].IsInt64() {
				whence = int(args[2].AsInt64())
			}
			if whence == io.SeekCurrent && pf.reader != nil && pf.reader.Buffered() > 0 {
				offset -= int64(pf.reader.Buffered())
			}
			pos, err := pf.f.Seek(offset, whence)
			if err != nil {
				ctx.Raise(val.NewException(val.OSErrorType, err.Error()))
				return val.NoneVal()
			}
			if pf.reader != nil {
				pf.reader.Reset(pf.f)
			}
			return val.Int64Val(pos)
		}))

		FileType.Dict.Set(val.InternString("tell"), val.NewBuiltinFuncVal("tell", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			pf := (*pyFile)(val.AsPointer(args[0]))
			pf.mu.Lock()
			defer pf.mu.Unlock()
			if pf.isClosed {
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
			pf.flushLocked()
			pos, err := pf.f.Seek(0, io.SeekCurrent)
			if err != nil {
				ctx.Raise(val.NewException(val.OSErrorType, err.Error()))
				return val.NoneVal()
			}
			if pf.reader != nil && pf.reader.Buffered() > 0 {
				pos -= int64(pf.reader.Buffered())
			}
			return val.Int64Val(pos)
		}))

		FileType.Dict.Set(val.InternString("seekable"), val.NewBuiltinFuncVal("seekable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.BoolVal(true)
		}))
		FileType.Dict.Set(val.InternString("readable"), val.NewBuiltinFuncVal("readable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.BoolVal(true)
		}))
		FileType.Dict.Set(val.InternString("writable"), val.NewBuiltinFuncVal("writable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.BoolVal(true)
		}))

		FileType.Dict.Set(val.InternString("write"), val.NewBuiltinFuncVal("write", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				ctx.Raise(val.NewException(val.TypeErrorType, "write() takes exactly one argument"))
				return val.NoneVal()
			}
			pf := (*pyFile)(val.AsPointer(args[0]))
			isMT := val.IsMultiThreaded()
			if isMT {
				pf.mu.Lock()
			}
			if pf.isClosed {
				if isMT {
					pf.mu.Unlock()
				}
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
			oldLen := len(pf.writeBuf)
			if pf.binary {
				switch {
				case args[1].IsBytes():
					pf.writeBuf = append(pf.writeBuf, args[1].AsBytes().Data...)
				case args[1].IsByteArray():
					pf.writeBuf = append(pf.writeBuf, args[1].AsByteArray().Data...)
				case args[1].IsMemoryView():
					pf.writeBuf = append(pf.writeBuf, args[1].AsMemoryView().Buf...)
				default:
					if isMT {
						pf.mu.Unlock()
					}
					ctx.Raise(val.NewException(val.TypeErrorType, "a bytes-like object is required, not 'str'"))
					return val.NoneVal()
				}
			} else {
				v := args[1]
				var ok bool
				pf.writeBuf, ok = val.AppendValueBytes(pf.writeBuf, v)
				if !ok {
					if !v.IsAnyString() {
						if isMT {
							pf.mu.Unlock()
						}
						ctx.Raise(val.NewException(val.TypeErrorType, "write() argument must be str, not"+v.Type().Name))
						return val.NoneVal()
					}
					s := v.AsString()
					view := unsafe.Slice(unsafe.StringData(s), len(s))
					pf.writeBuf = append(pf.writeBuf, view...)
				}
			}
			written := len(pf.writeBuf) - oldLen
			if pf.unbuffered || len(pf.writeBuf) >= 32768 {
				pf.flushLocked()
			}
			if isMT {
				pf.mu.Unlock()
			}
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
		FileType.Dict.Set(val.InternString("reconfigure"), val.NewBuiltinFuncVal("reconfigure", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewException(val.TypeErrorType, "reconfigure() missing self"))
				return val.NoneVal()
			}
			inst := args[0].AsInstance()
			if v, ok := kwArg(args, kwnames, "encoding"); ok && !v.IsNone() {
				inst.SetAttr(nameEncoding, v)
			}
			if v, ok := kwArg(args, kwnames, "errors"); ok && !v.IsNone() {
				inst.SetAttr(nameErrors, v)
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
				ctx.Raise(val.NewException(val.OSErrorType, "file has no descriptor"))
				return val.NoneVal()
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
				line, err := pf.ensureReader().ReadBytes('\n')
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
				ctx.Raise(val.NewException(val.TypeErrorType, "__iter__() missing self"))
				return val.NoneVal()
			}
			return FileType.Slots.TpIter(ctx, args[0])
		}))
		FileType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			pf := (*pyFile)(val.AsPointer(args[0]))
			pf.mu.Lock()
			defer pf.mu.Unlock()
			if !pf.isClosed && pf.f != nil {
				pf.flushLocked()
				if pf.pooledBuf != nil {
					fileBufferPool.Put(pf.pooledBuf)
					pf.pooledBuf = nil
					pf.writeBuf = nil
				}
				if pf.f != os.Stdin && pf.f != os.Stdout && pf.f != os.Stderr {
					_ = pf.f.Close()
					pf.isClosed = true
				}
			}
			return val.NoneVal()
		}))

		FileType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) == 0 {
				ctx.Raise(val.NewException(val.TypeErrorType, "TextIOWrapper() missing required argument 'buffer'"))
				return val.NoneVal()
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

func getAttrOrNone(ctx *val.ExecContext, obj, name val.Value) (v val.Value, ok bool) {
	if obj.HeaderPtr() == nil {
		return val.NoneVal(), false
	}
	defer func() {
		if r := recover(); r != nil {
			v = val.NoneVal()
			ok = false
		}
	}()
	v = val.GetAttrCtx(ctx, obj, name)
	return v, true
}

// openFileBuiltinCtx opens a file and returns a Python File Value.
func openFileBuiltinCtx(ctx *val.ExecContext, file string, mode string) val.Value {
	return openFileBuiltinValCtx(ctx, val.NewStringVal(file), mode)
}

func openFileBuiltinValCtx(ctx *val.ExecContext, fileVal val.Value, mode string) val.Value {
	initIOTypes()
	if mode == "" {
		mode = "r"
	}
	var flag int
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
		// The mode is parsed the way CPython parses it: the first letter
		// selects the access, and a '+' anywhere turns the file into one
		// that can be read and written.
		switch {
		case strings.Contains(mode, "w"):
			flag = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		case strings.Contains(mode, "a"):
			flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
		case strings.Contains(mode, "x"):
			flag = os.O_CREATE | os.O_EXCL | os.O_WRONLY
		default:
			flag = os.O_RDONLY
		}
		if strings.Contains(mode, "+") {
			flag &^= os.O_WRONLY | os.O_RDONLY
			flag |= os.O_RDWR
		}
		pathStr := fileVal.AsString()
		f, err = os.OpenFile(pathStr, flag, 0666)
		if err != nil {
			ctx.Raise(val.NewException(val.FileNotFoundErrorType, err.Error()))
			return val.NoneVal()
		}
	}

	var writeBuf []byte
	var pooledBuf *[]byte
	if strings.Contains(mode, "w") || strings.Contains(mode, "a") || strings.Contains(mode, "+") {
		pooledBuf = fileBufferPool.Get().(*[]byte)
		writeBuf = (*pooledBuf)[:0]
	}

	pf := &pyFile{
		inst:      val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: FileType}, Shape: FileType.RootShape},
		f:         f,
		writeBuf:  writeBuf,
		binary:    binary,
		pooledBuf: pooledBuf,
	}
	pf.inst.SetAttr(nameMode, val.NewStringVal(mode))
	if fileVal.IsAnyString() {
		pf.inst.SetAttr(nameAttrName, fileVal)
	}
	if !binary && f != nil {
		bufPf := &pyFile{
			inst:     val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: FileType}, Shape: FileType.RootShape},
			f:        f,
			reader:   pf.reader,
			writeBuf: writeBuf,
			binary:   true,
		}
		bufPf.inst.SetAttr(nameMode, val.NewStringVal(mode+"b"))
		pf.inst.SetAttr(nameBuffer, val.ObjectVal(&bufPf.inst.Header))
		pf.inst.SetAttr(nameEncoding, val.NewStringVal("utf-8"))
		pf.inst.SetAttr(nameErrors, val.NewStringVal("strict"))
	}

	return val.ObjectVal(&pf.inst.Header)
}

// newStdStream wraps one of the process's standard streams as a text file.
func newStdStream(f *os.File, name string, writable bool) val.Value {
	initIOTypes()
	bufMode := "rb"
	mode := "r"
	if writable {
		bufMode = "wb"
		mode = "w"
	}
	bufPf := &pyFile{
		inst:       val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: FileType}, Shape: FileType.RootShape},
		f:          f,
		binary:     true,
		unbuffered: writable,
		name:       name,
	}
	bufVal := val.ObjectVal(&bufPf.inst.Header)
	bufPf.inst.SetAttr(nameAttrName, val.NewStringVal(name))
	bufPf.inst.SetAttr(nameMode, val.NewStringVal(bufMode))
	bufPf.inst.SetAttr(nameRaw, bufVal)

	pf := &pyFile{
		inst:       val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: FileType}, Shape: FileType.RootShape},
		f:          f,
		unbuffered: writable,
		name:       name,
	}
	pf.inst.SetAttr(nameAttrName, val.NewStringVal(name))
	pf.inst.SetAttr(nameMode, val.NewStringVal(mode))
	pf.inst.SetAttr(nameEncoding, val.NewStringVal("utf-8"))
	pf.inst.SetAttr(nameErrors, val.NewStringVal("surrogateescape"))
	pf.inst.SetAttr(nameBuffer, bufVal)
	return val.ObjectVal(&pf.inst.Header)
}

func (e *Engine) createIOModule() val.Value {
	initIOTypes()
	ioModVal := val.NewModuleVal("io", "")
	ioMod := ioModVal.AsModule()

	ioMod.Dict.Set(val.InternString("BytesIO"), val.TypeVal(BytesIOType))
	ioMod.Dict.Set(val.InternString("StringIO"), val.TypeVal(StringIOType))

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
			if closeFn, _, ok := args[0].Type().LookupMRO(nameClose); ok {
				return ctx.Invoke1(closeFn, args[0])
			}
		}
		return val.NoneVal()
	}))
	ioBaseType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 && args[0].IsInstance() {
			args[0].AsInstance().SetAttr(nameClosed, val.BoolVal(true))
		}
		return val.NoneVal()
	}))
	ioBaseType.Dict.Set(val.InternString("flush"), val.NewBuiltinFuncVal("flush", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))
	ioBaseType.Dict.Set(val.InternString("seek"), val.NewBuiltinFuncVal("seek", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		panic("io.UnsupportedOperation: seek")
	}))
	ioBaseType.Dict.Set(val.InternString("tell"), val.NewBuiltinFuncVal("tell", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			ctx.Raise(val.NewException(val.TypeErrorType, "descriptor 'tell' of 'IOBase' object needs an argument"))
			return val.NoneVal()
		}
		if seekFn, _, ok := args[0].Type().LookupMRO(nameSeek); ok {
			return ctx.Invoke3(seekFn, args[0], val.Int64Val(0), val.Int64Val(1))
		}
		panic("io.UnsupportedOperation: seek")
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
			clVal := val.GetAttrCtx(ctx, args[0], nameClosed)
			if clVal.IsTruthy() {
				ctx.Raise(val.NewException(val.ValueErrorType, "I/O operation on closed file."))
				return val.NoneVal()
			}
		}
		return val.NoneVal()
	}))
	ioBaseType.Dict.Set(val.InternString("_checkReadable"), val.NewBuiltinFuncVal("_checkReadable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if fn, _, ok := args[0].Type().LookupMRO(nameReadable); ok {
				res := ctx.Invoke1(fn, args[0])
				if !res.IsTruthy() {
					ctx.Raise(val.NewException(val.OSErrorType, "File or stream is not readable."))
					return val.NoneVal()
				}
			}
		}
		return val.NoneVal()
	}))
	ioBaseType.Dict.Set(val.InternString("_checkWritable"), val.NewBuiltinFuncVal("_checkWritable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if fn, _, ok := args[0].Type().LookupMRO(nameWritable); ok {
				res := ctx.Invoke1(fn, args[0])
				if !res.IsTruthy() {
					ctx.Raise(val.NewException(val.OSErrorType, "File or stream is not writable."))
					return val.NoneVal()
				}
			}
		}
		return val.NoneVal()
	}))
	ioBaseType.Dict.Set(val.InternString("_checkSeekable"), val.NewBuiltinFuncVal("_checkSeekable", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if fn, _, ok := args[0].Type().LookupMRO(nameSeekable); ok {
				res := ctx.Invoke1(fn, args[0])
				if !res.IsTruthy() {
					ctx.Raise(val.NewException(val.OSErrorType, "File or stream is not seekable."))
					return val.NoneVal()
				}
			}
		}
		return val.NoneVal()
	}))

	invokeIOSeek := func(ctx *val.ExecContext, rawVal val.Value, args []val.Value) val.Value {
		seekFn, _, ok := rawVal.Type().LookupMRO(nameSeek)
		if !ok {
			panic("io.UnsupportedOperation: seek")
		}
		var callArgs []val.Value
		if len(args) > 1 {
			callArgs = args[1:]
		}
		if descrGet := seekFn.Type().Slots.TpDescrGet; descrGet != nil {
			bound := descrGet(ctx, seekFn, rawVal, rawVal.Type())
			switch len(callArgs) {
			case 0:
				return ctx.Invoke0(bound)
			case 1:
				return ctx.Invoke1(bound, callArgs[0])
			default:
				return ctx.Invoke2(bound, callArgs[0], callArgs[1])
			}
		}
		switch len(callArgs) {
		case 0:
			return ctx.Invoke1(seekFn, rawVal)
		case 1:
			return ctx.Invoke2(seekFn, rawVal, callArgs[0])
		default:
			return ctx.Invoke3(seekFn, rawVal, callArgs[0], callArgs[1])
		}
	}

	invokeIOTell := func(ctx *val.ExecContext, rawVal val.Value) val.Value {
		tellFn, _, ok := rawVal.Type().LookupMRO(nameTell)
		if !ok {
			panic("io.UnsupportedOperation: tell")
		}
		if descrGet := tellFn.Type().Slots.TpDescrGet; descrGet != nil {
			bound := descrGet(ctx, tellFn, rawVal, rawVal.Type())
			return ctx.Invoke0(bound)
		}
		return ctx.Invoke1(tellFn, rawVal)
	}

	rawIOBaseType := val.MustNewType("RawIOBase", []*val.PyType{ioBaseType}, nil)
	bufferedIOBaseType := val.MustNewType("BufferedIOBase", []*val.PyType{ioBaseType}, nil)
	textIOBaseType := val.MustNewType("TextIOBase", []*val.PyType{ioBaseType}, nil)

	// BufferedReader
	bufferedReaderType := val.MustNewType("BufferedReader", []*val.PyType{bufferedIOBaseType}, nil)
	bufferedReaderType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			ctx.Raise(val.NewException(val.TypeErrorType, "BufferedReader() missing required argument 'raw'"))
			return val.NoneVal()
		}
		inst := val.NewInstanceOfType(bufferedReaderType)
		inst.AsInstance().SetAttr(nameRaw, args[0])
		return inst
	}
	bufferedReaderType.Dict.Set(val.InternString("read"), val.NewBuiltinFuncVal("read", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			ctx.Raise(val.NewException(val.TypeErrorType, "read() missing self"))
			return val.NoneVal()
		}
		rawVal, _ := args[0].AsInstance().GetAttr(nameRaw)
		size := -1
		if len(args) > 1 && !args[1].IsNone() {
			size = int(args[1].AsInt64())
		}
		readintoFn, _, hasReadinto := rawVal.Type().LookupMRO(nameReadinto)
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
		if readFn, _, hasRead := rawVal.Type().LookupMRO(nameRead); hasRead {
			if size >= 0 {
				return ctx.Invoke2(readFn, rawVal, val.Int64Val(int64(size)))
			}
			return ctx.Invoke1(readFn, rawVal)
		}
		ctx.Raise(val.NewException(val.AttributeErrorType, "raw object has no read or readinto"))
		return val.NoneVal()
	}))
	bufferedReaderType.Dict.Set(val.InternString("readline"), val.NewBuiltinFuncVal("readline", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			ctx.Raise(val.NewException(val.TypeErrorType, "readline() missing self"))
			return val.NoneVal()
		}
		rawVal, _ := args[0].AsInstance().GetAttr(nameRaw)
		readintoFn, _, hasReadinto := rawVal.Type().LookupMRO(nameReadinto)
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
			ctx.Raise(val.NewException(val.TypeErrorType, "readinto() takes 1 argument"))
			return val.NoneVal()
		}
		rawVal, _ := args[0].AsInstance().GetAttr(nameRaw)
		if fn, _, ok := rawVal.Type().LookupMRO(nameReadinto); ok {
			return ctx.Invoke2(fn, rawVal, args[1])
		}
		ctx.Raise(val.NewException(val.AttributeErrorType, "raw object has no readinto"))
		return val.NoneVal()
	}))
	bufferedReaderType.Dict.Set(val.InternString("flush"), val.NewBuiltinFuncVal("flush", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if rawVal, ok := args[0].AsInstance().GetAttr(nameRaw); ok {
				if fn, _, has := rawVal.Type().LookupMRO(nameFlush); has {
					return ctx.Invoke1(fn, rawVal)
				}
			}
		}
		return val.NoneVal()
	}))
	bufferedReaderType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if rawVal, ok := args[0].AsInstance().GetAttr(nameRaw); ok {
				if fn, _, has := rawVal.Type().LookupMRO(nameClose); has {
					return ctx.Invoke1(fn, rawVal)
				}
			}
		}
		return val.NoneVal()
	}))
	bufferedReaderType.Dict.Set(val.InternString("fileno"), val.NewBuiltinFuncVal("fileno", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if rawVal, ok := args[0].AsInstance().GetAttr(nameRaw); ok {
				if fn, _, has := rawVal.Type().LookupMRO(nameFileno); has {
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
				if rawVal, ok := args[0].AsInstance().GetAttr(nameRaw); ok {
					return val.GetAttrCtx(ctx, rawVal, nameClosed)
				}
			}
			return val.BoolVal(false)
		}),
		val.NoneVal(),
		val.NoneVal(),
	))
	bufferedReaderType.Dict.Set(val.InternString("seek"), val.NewBuiltinFuncVal("seek", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			ctx.Raise(val.NewException(val.TypeErrorType, "seek() missing self"))
			return val.NoneVal()
		}
		rawVal, _ := args[0].AsInstance().GetAttr(nameRaw)
		return invokeIOSeek(ctx, rawVal, args)
	}))
	bufferedReaderType.Dict.Set(val.InternString("tell"), val.NewBuiltinFuncVal("tell", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			ctx.Raise(val.NewException(val.TypeErrorType, "tell() missing self"))
			return val.NoneVal()
		}
		rawVal, _ := args[0].AsInstance().GetAttr(nameRaw)
		return invokeIOTell(ctx, rawVal)
	}))

	// BufferedWriter
	bufferedWriterType := val.MustNewType("BufferedWriter", []*val.PyType{bufferedIOBaseType}, nil)
	bufferedWriterType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			ctx.Raise(val.NewException(val.TypeErrorType, "BufferedWriter() missing required argument 'raw'"))
			return val.NoneVal()
		}
		inst := val.NewInstanceOfType(bufferedWriterType)
		inst.AsInstance().SetAttr(nameRaw, args[0])
		return inst
	}
	bufferedWriterType.Dict.Set(val.InternString("write"), val.NewBuiltinFuncVal("write", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			ctx.Raise(val.NewException(val.TypeErrorType, "write() takes 1 argument"))
			return val.NoneVal()
		}
		rawVal, _ := args[0].AsInstance().GetAttr(nameRaw)
		if fn, _, ok := rawVal.Type().LookupMRO(nameWrite); ok {
			return ctx.Invoke2(fn, rawVal, args[1])
		}
		ctx.Raise(val.NewException(val.AttributeErrorType, "raw object has no write"))
		return val.NoneVal()
	}))
	bufferedWriterType.Dict.Set(val.InternString("flush"), val.NewBuiltinFuncVal("flush", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if rawVal, ok := args[0].AsInstance().GetAttr(nameRaw); ok {
				if fn, _, has := rawVal.Type().LookupMRO(nameFlush); has {
					return ctx.Invoke1(fn, rawVal)
				}
			}
		}
		return val.NoneVal()
	}))
	bufferedWriterType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if rawVal, ok := args[0].AsInstance().GetAttr(nameRaw); ok {
				if fn, _, has := rawVal.Type().LookupMRO(nameClose); has {
					return ctx.Invoke1(fn, rawVal)
				}
			}
		}
		return val.NoneVal()
	}))
	bufferedWriterType.Dict.Set(val.InternString("fileno"), val.NewBuiltinFuncVal("fileno", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if rawVal, ok := args[0].AsInstance().GetAttr(nameRaw); ok {
				if fn, _, has := rawVal.Type().LookupMRO(nameFileno); has {
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
				if rawVal, ok := args[0].AsInstance().GetAttr(nameRaw); ok {
					return val.GetAttrCtx(ctx, rawVal, nameClosed)
				}
			}
			return val.BoolVal(false)
		}),
		val.NoneVal(),
		val.NoneVal(),
	))
	bufferedWriterType.Dict.Set(val.InternString("seek"), val.NewBuiltinFuncVal("seek", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			ctx.Raise(val.NewException(val.TypeErrorType, "seek() missing self"))
			return val.NoneVal()
		}
		rawVal, _ := args[0].AsInstance().GetAttr(nameRaw)
		return invokeIOSeek(ctx, rawVal, args)
	}))
	bufferedWriterType.Dict.Set(val.InternString("tell"), val.NewBuiltinFuncVal("tell", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			ctx.Raise(val.NewException(val.TypeErrorType, "tell() missing self"))
			return val.NoneVal()
		}
		rawVal, _ := args[0].AsInstance().GetAttr(nameRaw)
		return invokeIOTell(ctx, rawVal)
	}))

	// BufferedRWPair
	bufferedRWPairType := val.MustNewType("BufferedRWPair", []*val.PyType{bufferedIOBaseType}, nil)
	bufferedRWPairType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			ctx.Raise(val.NewException(val.TypeErrorType, "BufferedRWPair() takes at least 2 arguments"))
			return val.NoneVal()
		}
		inst := val.NewInstanceOfType(bufferedRWPairType)
		inst.AsInstance().SetAttr(nameReader, args[0])
		inst.AsInstance().SetAttr(nameWriter, args[1])
		return inst
	}
	bufferedRWPairType.Dict.Set(val.InternString("read"), val.NewBuiltinFuncVal("read", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		rVal, _ := args[0].AsInstance().GetAttr(nameReader)
		if fn, _, ok := rVal.Type().LookupMRO(nameRead); ok {
			if len(args) > 1 {
				return ctx.Invoke2(fn, rVal, args[1])
			}
			return ctx.Invoke1(fn, rVal)
		}
		ctx.Raise(val.NewException(val.AttributeErrorType, "reader has no read"))
		return val.NoneVal()
	}))
	bufferedRWPairType.Dict.Set(val.InternString("readline"), val.NewBuiltinFuncVal("readline", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		rVal, _ := args[0].AsInstance().GetAttr(nameReader)
		if fn, _, ok := rVal.Type().LookupMRO(nameReadline); ok {
			if len(args) > 1 {
				return ctx.Invoke2(fn, rVal, args[1])
			}
			return ctx.Invoke1(fn, rVal)
		}
		ctx.Raise(val.NewException(val.AttributeErrorType, "reader has no readline"))
		return val.NoneVal()
	}))
	bufferedRWPairType.Dict.Set(val.InternString("write"), val.NewBuiltinFuncVal("write", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		wVal, _ := args[0].AsInstance().GetAttr(nameWriter)
		if fn, _, ok := wVal.Type().LookupMRO(nameWrite); ok {
			return ctx.Invoke2(fn, wVal, args[1])
		}
		ctx.Raise(val.NewException(val.AttributeErrorType, "writer has no write"))
		return val.NoneVal()
	}))
	bufferedRWPairType.Dict.Set(val.InternString("flush"), val.NewBuiltinFuncVal("flush", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		wVal, _ := args[0].AsInstance().GetAttr(nameWriter)
		if fn, _, ok := wVal.Type().LookupMRO(nameFlush); ok {
			return ctx.Invoke1(fn, wVal)
		}
		return val.NoneVal()
	}))
	bufferedRWPairType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		rVal, _ := args[0].AsInstance().GetAttr(nameReader)
		wVal, _ := args[0].AsInstance().GetAttr(nameWriter)
		if fn, _, ok := rVal.Type().LookupMRO(nameClose); ok {
			ctx.Invoke1(fn, rVal)
		}
		if fn, _, ok := wVal.Type().LookupMRO(nameClose); ok {
			ctx.Invoke1(fn, wVal)
		}
		return val.NoneVal()
	}))
	bufferedRWPairType.Dict.Set(val.InternString("fileno"), val.NewBuiltinFuncVal("fileno", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		wVal, _ := args[0].AsInstance().GetAttr(nameWriter)
		if fn, _, ok := wVal.Type().LookupMRO(nameFileno); ok {
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
				if wVal, ok := args[0].AsInstance().GetAttr(nameWriter); ok {
					return val.GetAttrCtx(ctx, wVal, nameClosed)
				}
			}
			return val.BoolVal(false)
		}),
		val.NoneVal(),
		val.NoneVal(),
	))

	// TextIOWrapper
	textIOWrapperType := val.MustNewType("TextIOWrapper", []*val.PyType{textIOBaseType}, nil)
	textIOWrapperType.Dict.Set(val.DunderNew, val.NewBuiltinFuncVal("__new__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		cls := textIOWrapperType
		if len(args) > 0 && args[0].IsType() {
			cls = args[0].AsType()
		}
		if cls == textIOWrapperType && len(args) > 1 && args[1].Type() == FileType {
			pf := (*pyFile)(val.AsPointer(args[1]))
			pf.binary = false
			return args[1]
		}
		return val.NewInstanceOfType(cls)
	}))
	textIOWrapperType.Dict.Set(val.DunderInit, val.NewBuiltinFuncVal("__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			ctx.Raise(val.NewException(val.TypeErrorType, "TextIOWrapper.__init__() missing required argument 'buffer'"))
			return val.NoneVal()
		}
		self := args[0].AsInstance()
		bufVal := args[1]
		self.SetAttr(nameBuffer, bufVal)
		enc := "utf-8"
		if v, ok := kwArg(args, kwnames, "encoding"); ok && !v.IsNone() {
			enc = v.AsString()
		} else if len(args) > 2 && !args[2].IsNone() {
			enc = args[2].AsString()
		}
		self.SetAttr(nameEncoding, val.NewStringVal(enc))
		errs := "strict"
		if v, ok := kwArg(args, kwnames, "errors"); ok && !v.IsNone() {
			errs = v.AsString()
		} else if len(args) > 3 && !args[3].IsNone() {
			errs = args[3].AsString()
		}
		self.SetAttr(nameErrors, val.NewStringVal(errs))
		return val.NoneVal()
	}))
	textIOWrapperType.Dict.Set(val.InternString("encoding"), val.NewStringVal("utf-8"))
	textIOWrapperType.Dict.Set(val.InternString("errors"), val.NewStringVal("strict"))
	textIOWrapperType.Dict.Set(val.InternString("name"), val.NewStringVal(""))
	textIOWrapperType.Dict.Set(val.InternString("mode"), val.NewStringVal("r"))
	textIOWrapperType.Dict.Set(val.InternString("isatty"), val.NewBuiltinFuncVal("isatty", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if bufVal, ok := getAttrOrNone(ctx, args[0], nameBuffer); ok && !bufVal.IsNone() {
				if meth, ok := getAttrOrNone(ctx, bufVal, nameIsatty); ok {
					return ctx.Invoke0(meth)
				}
			}
		}
		return val.BoolVal(false)
	}))
	textIOWrapperType.Dict.Set(val.InternString("reconfigure"), val.NewBuiltinFuncVal("reconfigure", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			ctx.Raise(val.NewException(val.TypeErrorType, "reconfigure() missing self"))
			return val.NoneVal()
		}
		inst := args[0].AsInstance()
		if v, ok := kwArg(args, kwnames, "encoding"); ok && !v.IsNone() {
			inst.SetAttr(nameEncoding, v)
		}
		if v, ok := kwArg(args, kwnames, "errors"); ok && !v.IsNone() {
			inst.SetAttr(nameErrors, v)
		}
		return val.NoneVal()
	}))
	textIOWrapperType.InvalidateVersion()
	textIOWrapperType.Dict.Set(val.InternString("read"), val.NewBuiltinFuncVal("read", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			ctx.Raise(val.NewException(val.TypeErrorType, "read() missing self"))
			return val.NoneVal()
		}
		bufVal, ok := getAttrOrNone(ctx, args[0], nameBuffer)
		var res val.Value
		if ok && !bufVal.IsNone() {
			if meth, ok := getAttrOrNone(ctx, bufVal, nameRead); ok {
				if len(args) > 1 {
					res = ctx.Invoke1(meth, args[1])
				} else {
					res = ctx.Invoke0(meth)
				}
			}
		}
		if res.IsBytes() {
			return val.NewStringVal(string(res.AsBytes().Data))
		}
		return res
	}))
	textIOWrapperType.Dict.Set(val.InternString("readline"), val.NewBuiltinFuncVal("readline", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			ctx.Raise(val.NewException(val.TypeErrorType, "readline() missing self"))
			return val.NoneVal()
		}
		bufVal, ok := getAttrOrNone(ctx, args[0], nameBuffer)
		var res val.Value
		if ok && !bufVal.IsNone() {
			if meth, ok := getAttrOrNone(ctx, bufVal, nameReadline); ok {
				if len(args) > 1 {
					res = ctx.Invoke1(meth, args[1])
				} else {
					res = ctx.Invoke0(meth)
				}
			}
		}
		if res.IsBytes() {
			return val.NewStringVal(string(res.AsBytes().Data))
		}
		return res
	}))
	textIOWrapperType.Dict.Set(val.InternString("write"), val.NewBuiltinFuncVal("write", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			ctx.Raise(val.NewException(val.TypeErrorType, "write() takes 1 argument"))
			return val.NoneVal()
		}
		if !args[1].IsAnyString() {
			ctx.Raise(val.NewException(val.TypeErrorType, "write() argument must be str, not"+args[1].Type().Name))
			return val.NoneVal()
		}
		bufVal, ok := getAttrOrNone(ctx, args[0], nameBuffer)
		strData := args[1].AsString()
		bytesVal := val.NewBytesVal([]byte(strData))
		if ok && !bufVal.IsNone() {
			if meth, ok := getAttrOrNone(ctx, bufVal, nameWrite); ok {
				ctx.Invoke1(meth, bytesVal)
			}
		}
		return val.Int64Val(int64(len([]rune(strData))))
	}))
	textIOWrapperType.Dict.Set(val.InternString("flush"), val.NewBuiltinFuncVal("flush", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if bufVal, ok := getAttrOrNone(ctx, args[0], nameBuffer); ok && !bufVal.IsNone() {
				if meth, ok := getAttrOrNone(ctx, bufVal, nameFlush); ok {
					return ctx.Invoke0(meth)
				}
			}
		}
		return val.NoneVal()
	}))
	textIOWrapperType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if bufVal, ok := getAttrOrNone(ctx, args[0], nameBuffer); ok && !bufVal.IsNone() {
				if meth, ok := getAttrOrNone(ctx, bufVal, nameClose); ok {
					return ctx.Invoke0(meth)
				}
			}
		}
		return val.NoneVal()
	}))
	textIOWrapperType.Dict.Set(val.InternString("fileno"), val.NewBuiltinFuncVal("fileno", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) > 0 {
			if bufVal, ok := getAttrOrNone(ctx, args[0], nameBuffer); ok && !bufVal.IsNone() {
				if meth, ok := getAttrOrNone(ctx, bufVal, nameFileno); ok {
					return ctx.Invoke0(meth)
				}
			}
		}
		panic("UnsupportedOperation: fileno")
	}))
	textIOWrapperType.Dict.Set(val.InternString("closed"), val.NewPropertyVal(
		val.NewBuiltinFuncVal("closed", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) > 0 {
				if bufVal, ok := getAttrOrNone(ctx, args[0], nameBuffer); ok && !bufVal.IsNone() {
					if c, ok := getAttrOrNone(ctx, bufVal, nameClosed); ok {
						return c
					}
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
				ctx.Raise(val.NewException(val.TypeErrorType, "open() missing required argument 'file' (pos 1)"))
				return val.NoneVal()
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
		return openFileBuiltinValCtx(ctx, targetVal, mode)
	})

	ioMod.Dict.Set(val.InternString("open"), openFn)
	ioMod.Dict.Set(val.InternString("open_code"), val.NewBuiltinFuncVal("open_code", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			ctx.Raise(val.NewException(val.TypeErrorType, "open_code() missing required argument 'path'"))
			return val.NoneVal()
		}
		return openFileBuiltinValCtx(ctx, val.NewStringVal(pathArg(ctx, args[0])), "rb")
	}))
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
