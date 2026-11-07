package vm

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"hash/crc32"
	"io"
	"sync"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	zlibOnce           sync.Once
	zlibErrorType      *val.PyType
	zlibCompressType   *val.PyType
	zlibDecompressType *val.PyType
)

type pyZlibCompress struct {
	inst  val.PyInstance
	level int
	wbits int
	buf   bytes.Buffer
	fw    *flate.Writer
	zw    *zlib.Writer
	gw    *gzip.Writer
}

type pyZlibDecompress struct {
	inst              val.PyInstance
	wbits             int
	inBuf             []byte
	outPending        []byte
	unusedData        []byte
	unconsumedTail    []byte
	savedUnusedData   []byte
	streamFinished    bool
	totalDecodedSoFar int
	eof               bool
	needsInput        bool
}

func computeAdler32(data []byte, initial uint32) uint32 {
	if len(data) == 0 {
		return initial
	}
	s1 := initial & 0xffff
	s2 := (initial >> 16) & 0xffff
	for _, b := range data {
		s1 = (s1 + uint32(b)) % 65521
		s2 = (s2 + s1) % 65521
	}
	return (s2 << 16) | s1
}

func getBytesFromVal(v val.Value) []byte {
	if v.IsBytes() {
		return v.AsBytes().Data
	}
	if v.IsByteArray() {
		return v.AsByteArray().Data
	}
	if v.IsAnyString() {
		return []byte(v.AsString())
	}
	panic(val.NewException(val.TypeErrorType, "a bytes-like object is required"))
}

func initZlibTypes() {
	zlibOnce.Do(func() {
		zlibErrorType = val.MustNewType("error", []*val.PyType{val.ExceptionType}, nil)
		zlibErrorType.Dict.Set(val.InternString("__module__"), val.InternString("zlib"))

		// Compress object
		zlibCompressType = val.MustNewType("Compress", []*val.PyType{val.ObjectType}, nil)
		zlibCompressType.Dict.Set(val.InternString("__module__"), val.InternString("zlib"))

		zlibCompressType.Dict.Set(val.InternString("compress"), val.NewBuiltinFuncVal("compress", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic(val.NewException(val.TypeErrorType, "compress() requires data argument"))
			}
			c := (*pyZlibCompress)(val.AsPointer(args[0]))
			data := getBytesFromVal(args[1])
			out := c.compress(data)
			return val.NewBytesVal(out)
		}))

		zlibCompressType.Dict.Set(val.InternString("flush"), val.NewBuiltinFuncVal("flush", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic(val.NewException(val.TypeErrorType, "flush() requires self"))
			}
			c := (*pyZlibCompress)(val.AsPointer(args[0]))
			mode := 4 // Z_FINISH
			if len(args) > 1 && args[1].IsInt64() {
				mode = int(args[1].AsInt64())
			}
			out := c.flush(mode)
			return val.NewBytesVal(out)
		}))

		zlibCompressType.Dict.Set(val.InternString("copy"), val.NewBuiltinFuncVal("copy", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic(val.NewException(val.TypeErrorType, "copy() requires self"))
			}
			c := (*pyZlibCompress)(val.AsPointer(args[0]))
			c2 := &pyZlibCompress{
				inst: val.PyInstance{
					Header: val.ObjectHeader{Kind: val.KindInstance, Type: zlibCompressType},
					Shape:  zlibCompressType.RootShape,
				},
				level: c.level,
				wbits: c.wbits,
			}
			c2.buf.Write(c.buf.Bytes())
			return val.ObjectVal(&c2.inst.Header)
		}))

		// Decompress object
		zlibDecompressType = val.MustNewType("Decompress", []*val.PyType{val.ObjectType}, nil)
		zlibDecompressType.Dict.Set(val.InternString("__module__"), val.InternString("zlib"))

		zlibDecompressType.Slots.TpGetAttr = func(ctx *val.ExecContext, obj, name val.Value) val.Value {
			d := (*pyZlibDecompress)(val.AsPointer(obj))
			switch name.AsString() {
			case "unused_data":
				return val.NewBytesVal(d.unusedData)
			case "unconsumed_tail":
				return val.NewBytesVal(d.unconsumedTail)
			case "eof":
				return val.BoolVal(d.eof)
			case "needs_input":
				return val.BoolVal(d.needsInput)
			}
			return val.ObjectGetAttr(ctx, obj, name)
		}

		zlibDecompressType.Dict.Set(val.InternString("decompress"), val.NewBuiltinFuncVal("decompress", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic(val.NewException(val.TypeErrorType, "decompress() requires data argument"))
			}
			d := (*pyZlibDecompress)(val.AsPointer(args[0]))
			data := getBytesFromVal(args[1])
			maxLen := 0
			if len(args) > 2 && args[2].IsInt64() {
				maxLen = int(args[2].AsInt64())
			}
			out := d.decompress(data, maxLen)
			return val.NewBytesVal(out)
		}))

		zlibDecompressType.Dict.Set(val.InternString("flush"), val.NewBuiltinFuncVal("flush", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic(val.NewException(val.TypeErrorType, "flush() requires self"))
			}
			d := (*pyZlibDecompress)(val.AsPointer(args[0]))
			maxLen := 0
			if len(args) > 1 && args[1].IsInt64() {
				maxLen = int(args[1].AsInt64())
			}
			out := d.flush(maxLen)
			return val.NewBytesVal(out)
		}))

		zlibDecompressType.Dict.Set(val.InternString("copy"), val.NewBuiltinFuncVal("copy", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic(val.NewException(val.TypeErrorType, "copy() requires self"))
			}
			d := (*pyZlibDecompress)(val.AsPointer(args[0]))
			d2 := &pyZlibDecompress{
				inst: val.PyInstance{
					Header: val.ObjectHeader{Kind: val.KindInstance, Type: zlibDecompressType},
					Shape:  zlibDecompressType.RootShape,
				},
				wbits:             d.wbits,
				inBuf:             append([]byte(nil), d.inBuf...),
				outPending:        append([]byte(nil), d.outPending...),
				unusedData:        append([]byte(nil), d.unusedData...),
				unconsumedTail:    append([]byte(nil), d.unconsumedTail...),
				totalDecodedSoFar: d.totalDecodedSoFar,
				savedUnusedData:   append([]byte(nil), d.savedUnusedData...),
				streamFinished:    d.streamFinished,
				eof:               d.eof,
				needsInput:        d.needsInput,
			}
			return val.ObjectVal(&d2.inst.Header)
		}))
	})
}

func (c *pyZlibCompress) compress(data []byte) []byte {
	if len(data) == 0 {
		return []byte{}
	}
	lvl := c.level
	if lvl < -1 || lvl > 9 {
		lvl = flate.DefaultCompression
	}
	if c.wbits < 0 {
		if c.fw == nil {
			fw, err := flate.NewWriter(&c.buf, lvl)
			if err != nil {
				panic(val.NewException(zlibErrorType, err.Error()))
			}
			c.fw = fw
		}
		c.fw.Write(data)
		c.fw.Flush()
	} else if c.wbits > 15 {
		if c.gw == nil {
			gw, err := gzip.NewWriterLevel(&c.buf, lvl)
			if err != nil {
				panic(val.NewException(zlibErrorType, err.Error()))
			}
			c.gw = gw
		}
		c.gw.Write(data)
		c.gw.Flush()
	} else {
		if c.zw == nil {
			zw, err := zlib.NewWriterLevel(&c.buf, lvl)
			if err != nil {
				panic(val.NewException(zlibErrorType, err.Error()))
			}
			c.zw = zw
		}
		c.zw.Write(data)
		c.zw.Flush()
	}
	out := append([]byte(nil), c.buf.Bytes()...)
	c.buf.Reset()
	return out
}

func (c *pyZlibCompress) flush(mode int) []byte {
	if mode == 4 /* Z_FINISH */ {
		if c.fw != nil {
			c.fw.Close()
			c.fw = nil
		}
		if c.gw != nil {
			c.gw.Close()
			c.gw = nil
		}
		if c.zw != nil {
			c.zw.Close()
			c.zw = nil
		}
	} else {
		if c.fw != nil {
			c.fw.Flush()
		}
		if c.gw != nil {
			c.gw.Flush()
		}
		if c.zw != nil {
			c.zw.Flush()
		}
	}
	out := append([]byte(nil), c.buf.Bytes()...)
	c.buf.Reset()
	return out
}

func (d *pyZlibDecompress) decompress(data []byte, maxLen int) []byte {
	if len(data) > 0 {
		d.inBuf = append(d.inBuf, data...)
	}
	if !d.streamFinished && len(d.inBuf) > 0 {
		br := bytes.NewReader(d.inBuf)
		var r io.Reader = br
		var closeReader io.Closer
		if d.wbits < 0 {
			fr := flate.NewReader(br)
			r = fr
			closeReader = fr
		} else if d.wbits > 15 && d.wbits <= 31 {
			gzr, err := gzip.NewReader(br)
			if err == nil {
				r = gzr
				closeReader = gzr
			}
		} else if d.wbits > 31 {
			if len(d.inBuf) >= 2 && d.inBuf[0] == 0x1f && d.inBuf[1] == 0x8b {
				gzr, err := gzip.NewReader(br)
				if err == nil {
					r = gzr
					closeReader = gzr
				}
			} else if len(d.inBuf) >= 1 && (d.inBuf[0] == 0x78 || d.inBuf[0] == 0x58) {
				zr, err := zlib.NewReader(br)
				if err == nil {
					r = zr
					closeReader = zr
				}
			} else {
				fr := flate.NewReader(br)
				r = fr
				closeReader = fr
			}
		} else {
			zr, err := zlib.NewReader(br)
			if err == nil {
				r = zr
				closeReader = zr
			}
		}

		decoded, err := io.ReadAll(r)
		if closeReader != nil {
			closeReader.Close()
		}

		if err == nil || err == io.EOF {
			d.streamFinished = true
			unread := br.Len()
			d.savedUnusedData = append([]byte(nil), d.inBuf[len(d.inBuf)-unread:]...)
			d.inBuf = nil
			if len(decoded) > d.totalDecodedSoFar {
				d.outPending = append(d.outPending, decoded[d.totalDecodedSoFar:]...)
				d.totalDecodedSoFar = len(decoded)
			}
		} else if err == io.ErrUnexpectedEOF {
			d.streamFinished = false
			if len(decoded) > d.totalDecodedSoFar {
				d.outPending = append(d.outPending, decoded[d.totalDecodedSoFar:]...)
				d.totalDecodedSoFar = len(decoded)
			}
		} else {
			panic(val.NewException(zlibErrorType, err.Error()))
		}
	}

	var res []byte
	if maxLen > 0 && maxLen < len(d.outPending) {
		res = append([]byte(nil), d.outPending[:maxLen]...)
		d.outPending = d.outPending[maxLen:]
		d.unconsumedTail = append([]byte(nil), d.inBuf...)
		d.eof = false
		d.needsInput = false
		d.unusedData = []byte{}
	} else {
		res = d.outPending
		d.outPending = nil
		d.unconsumedTail = nil
		if d.streamFinished {
			d.eof = true
			d.needsInput = false
			d.unusedData = d.savedUnusedData
		} else {
			d.eof = false
			d.needsInput = true
			d.unusedData = []byte{}
		}
	}
	if res == nil {
		res = []byte{}
	}
	return res
}

func (d *pyZlibDecompress) flush(length int) []byte {
	var res []byte
	if length > 0 && length < len(d.outPending) {
		res = append([]byte(nil), d.outPending[:length]...)
		d.outPending = d.outPending[length:]
		d.eof = false
		d.needsInput = false
	} else {
		res = d.outPending
		d.outPending = nil
		if d.streamFinished {
			d.eof = true
			d.needsInput = false
			d.unusedData = d.savedUnusedData
		}
	}
	if res == nil {
		res = []byte{}
	}
	return res
}

func makeDecompressObj(args []val.Value, kwnames []val.Value) val.Value {
	initZlibTypes()
	wbits := 15
	pa := posArgs(args, kwnames)
	kw := extractKwArgs(args, kwnames)
	if len(pa) > 0 && pa[0].IsInt64() {
		wbits = int(pa[0].AsInt64())
	} else if v, ok := kw["wbits"]; ok && v.IsInt64() {
		wbits = int(v.AsInt64())
	}
	d := &pyZlibDecompress{
		inst: val.PyInstance{
			Header: val.ObjectHeader{Kind: val.KindInstance, Type: zlibDecompressType},
			Shape:  zlibDecompressType.RootShape,
		},
		wbits:      wbits,
		unusedData: []byte{},
		needsInput: true,
	}
	return val.ObjectVal(&d.inst.Header)
}

func makeCompressObj(args []val.Value, kwnames []val.Value) val.Value {
	initZlibTypes()
	level := -1
	wbits := 15
	pa := posArgs(args, kwnames)
	kw := extractKwArgs(args, kwnames)
	if len(pa) > 0 && pa[0].IsInt64() {
		level = int(pa[0].AsInt64())
	} else if v, ok := kw["level"]; ok && v.IsInt64() {
		level = int(v.AsInt64())
	}
	if len(pa) > 2 && pa[2].IsInt64() {
		wbits = int(pa[2].AsInt64())
	} else if v, ok := kw["wbits"]; ok && v.IsInt64() {
		wbits = int(v.AsInt64())
	}
	c := &pyZlibCompress{
		inst: val.PyInstance{
			Header: val.ObjectHeader{Kind: val.KindInstance, Type: zlibCompressType},
			Shape:  zlibCompressType.RootShape,
		},
		level: level,
		wbits: wbits,
	}
	return val.ObjectVal(&c.inst.Header)
}

func (e *Engine) createZlibModule() val.Value {
	initZlibTypes()
	modVal := val.NewModuleVal("zlib", "zlib compression accelerator")
	d := modVal.AsModule().Dict

	set := func(name string, v val.Value) {
		d.Set(val.InternString(name), v)
	}

	set("__name__", val.NewStringVal("zlib"))
	set("__doc__", val.NewStringVal("zlib compression accelerator"))
	set("error", val.TypeVal(zlibErrorType))

	// Constants
	set("DEFLATED", val.Int64Val(8))
	set("DEF_MEM_LEVEL", val.Int64Val(8))
	set("MAX_WBITS", val.Int64Val(15))
	set("DEF_BUF_SIZE", val.Int64Val(16384))
	set("Z_NO_COMPRESSION", val.Int64Val(0))
	set("Z_BEST_SPEED", val.Int64Val(1))
	set("Z_BEST_COMPRESSION", val.Int64Val(9))
	set("Z_DEFAULT_COMPRESSION", val.Int64Val(-1))
	set("Z_FILTERED", val.Int64Val(1))
	set("Z_HUFFMAN_ONLY", val.Int64Val(2))
	set("Z_RLE", val.Int64Val(3))
	set("Z_FIXED", val.Int64Val(4))
	set("Z_DEFAULT_STRATEGY", val.Int64Val(0))
	set("Z_NO_FLUSH", val.Int64Val(0))
	set("Z_PARTIAL_FLUSH", val.Int64Val(1))
	set("Z_SYNC_FLUSH", val.Int64Val(2))
	set("Z_FULL_FLUSH", val.Int64Val(3))
	set("Z_FINISH", val.Int64Val(4))
	set("Z_BLOCK", val.Int64Val(5))
	set("Z_TREES", val.Int64Val(6))

	// crc32(data, [value=0])
	set("crc32", val.NewBuiltinFuncVal("crc32", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			panic(val.NewException(val.TypeErrorType, "crc32() requires data argument"))
		}
		data := getBytesFromVal(pa[0])
		initial := uint32(0)
		if len(pa) > 1 && pa[1].IsInt64() {
			initial = uint32(pa[1].AsInt64())
		}
		res := crc32.Update(initial, crc32.IEEETable, data)
		return val.Int64Val(int64(res))
	}))

	// adler32(data, [value=1])
	set("adler32", val.NewBuiltinFuncVal("adler32", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			panic(val.NewException(val.TypeErrorType, "adler32() requires data argument"))
		}
		data := getBytesFromVal(pa[0])
		initial := uint32(1)
		if len(pa) > 1 && pa[1].IsInt64() {
			initial = uint32(pa[1].AsInt64())
		}
		res := computeAdler32(data, initial)
		return val.Int64Val(int64(res))
	}))

	// compress(data, level=Z_DEFAULT_COMPRESSION, wbits=MAX_WBITS)
	set("compress", val.NewBuiltinFuncVal("compress", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		kw := extractKwArgs(args, kwnames)
		if len(pa) < 1 {
			panic(val.NewException(val.TypeErrorType, "compress() requires data argument"))
		}
		b := getBytesFromVal(pa[0])
		level := -1
		if len(pa) > 1 && pa[1].IsInt64() {
			level = int(pa[1].AsInt64())
		} else if v, ok := kw["level"]; ok && v.IsInt64() {
			level = int(v.AsInt64())
		}
		wbits := 15
		if len(pa) > 2 && pa[2].IsInt64() {
			wbits = int(pa[2].AsInt64())
		} else if v, ok := kw["wbits"]; ok && v.IsInt64() {
			wbits = int(v.AsInt64())
		}

		lvl := level
		if lvl < -1 || lvl > 9 {
			lvl = flate.DefaultCompression
		}
		var buf bytes.Buffer
		if wbits < 0 {
			fw, err := flate.NewWriter(&buf, lvl)
			if err != nil {
				panic(val.NewException(zlibErrorType, err.Error()))
			}
			fw.Write(b)
			fw.Close()
		} else if wbits > 15 {
			gw, err := gzip.NewWriterLevel(&buf, lvl)
			if err != nil {
				panic(val.NewException(zlibErrorType, err.Error()))
			}
			gw.Write(b)
			gw.Close()
		} else {
			zw, err := zlib.NewWriterLevel(&buf, lvl)
			if err != nil {
				panic(val.NewException(zlibErrorType, err.Error()))
			}
			zw.Write(b)
			zw.Close()
		}
		return val.NewBytesVal(buf.Bytes())
	}))

	// decompress(data, wbits=MAX_WBITS, bufsize=DEF_BUF_SIZE)
	set("decompress", val.NewBuiltinFuncVal("decompress", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		kw := extractKwArgs(args, kwnames)
		if len(pa) < 1 {
			panic(val.NewException(val.TypeErrorType, "decompress() requires data argument"))
		}
		b := getBytesFromVal(pa[0])
		wbits := 15
		if len(pa) > 1 && pa[1].IsInt64() {
			wbits = int(pa[1].AsInt64())
		} else if v, ok := kw["wbits"]; ok && v.IsInt64() {
			wbits = int(v.AsInt64())
		}

		br := bytes.NewReader(b)
		var r io.Reader = br
		var closeReader io.Closer
		if wbits < 0 {
			fr := flate.NewReader(br)
			r = fr
			closeReader = fr
		} else if wbits > 15 && wbits <= 31 {
			gzr, err := gzip.NewReader(br)
			if err != nil {
				panic(val.NewException(zlibErrorType, err.Error()))
			}
			r = gzr
			closeReader = gzr
		} else if wbits > 31 {
			if len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b {
				gzr, err := gzip.NewReader(br)
				if err != nil {
					panic(val.NewException(zlibErrorType, err.Error()))
				}
				r = gzr
				closeReader = gzr
			} else if len(b) >= 1 && (b[0] == 0x78 || b[0] == 0x58) {
				zr, err := zlib.NewReader(br)
				if err != nil {
					panic(val.NewException(zlibErrorType, err.Error()))
				}
				r = zr
				closeReader = zr
			} else {
				fr := flate.NewReader(br)
				r = fr
				closeReader = fr
			}
		} else {
			zr, err := zlib.NewReader(br)
			if err != nil {
				panic(val.NewException(zlibErrorType, err.Error()))
			}
			r = zr
			closeReader = zr
		}

		out, err := io.ReadAll(r)
		if closeReader != nil {
			closeReader.Close()
		}
		if err != nil && err != io.EOF {
			panic(val.NewException(zlibErrorType, err.Error()))
		}
		return val.NewBytesVal(out)
	}))

	// compressobj
	set("compressobj", val.NewBuiltinFuncVal("compressobj", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return makeCompressObj(args, kwnames)
	}))

	// decompressobj
	set("decompressobj", val.NewBuiltinFuncVal("decompressobj", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return makeDecompressObj(args, kwnames)
	}))

	// _ZlibDecompressor
	set("_ZlibDecompressor", val.NewBuiltinFuncVal("_ZlibDecompressor", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return makeDecompressObj(args, kwnames)
	}))

	return modVal
}
