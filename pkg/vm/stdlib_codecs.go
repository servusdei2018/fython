package vm

import (
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	codecInfoType *val.PyType
	codecInfoOnce sync.Once
)

func initCodecInfoType() {
	codecInfoOnce.Do(func() {
		t := val.MustNewType("CodecInfo", []*val.PyType{val.ObjectType}, nil)
		t.Dict.Set(val.InternString("__module__"), val.InternString("codecs"))
		seq := func(self val.Value) []val.Value {
			inst := self.AsInstance()
			encode, _ := inst.GetAttr(val.InternString("encode"))
			decode, _ := inst.GetAttr(val.InternString("decode"))
			sr, _ := inst.GetAttr(val.InternString("streamreader"))
			sw, _ := inst.GetAttr(val.InternString("streamwriter"))
			return []val.Value{encode, decode, sr, sw}
		}
		t.Dict.Set(val.InternString("__getitem__"), val.NewBuiltinFuncVal("__getitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			items := seq(args[0])
			i := int(args[1].AsInt64())
			if i < 0 {
				i += len(items)
			}
			if i < 0 || i >= len(items) {
				panic("IndexError: tuple index out of range")
			}
			return items[i]
		}))
		t.Dict.Set(val.InternString("__len__"), val.NewBuiltinFuncVal("__len__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.Int64Val(4)
		}))
		t.Dict.Set(val.InternString("__iter__"), val.NewBuiltinFuncVal("__iter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NewIterVal(val.NewTupleVal(seq(args[0])...))
		}))
		t.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			name, _ := args[0].AsInstance().GetAttr(val.InternString("name"))
			return val.NewStringVal("<codecs.CodecInfo object for encoding " + name.AsString() + ">")
		}))
		t.InvalidateVersion()
		codecInfoType = t
	})
}

func makeCodecInfo(name string, encFn, decFn val.Value) val.Value {
	initCodecInfoType()
	v := val.NewInstanceOfType(codecInfoType)
	inst := v.AsInstance()
	inst.SetAttr(val.InternString("name"), val.NewStringVal(name))
	inst.SetAttr(val.InternString("encode"), encFn)
	inst.SetAttr(val.InternString("decode"), decFn)
	inst.SetAttr(val.InternString("streamreader"), val.NoneVal())
	inst.SetAttr(val.InternString("streamwriter"), val.NoneVal())
	inst.SetAttr(val.InternString("incrementalencoder"), val.NoneVal())
	inst.SetAttr(val.InternString("incrementaldecoder"), val.NoneVal())
	return v
}

func (e *Engine) createCodecsModule() val.Value {
	modVal := val.NewModuleVal("_codecs", "Native codec registry and encoders/decoders")
	mod := modVal.AsModule()

	var searchFuncs []val.Value
	var searchMu sync.RWMutex
	errorHandlers := make(map[string]val.Value)
	var errorMu sync.RWMutex

	normalizeEncoding := func(encoding string) string {
		encoding = strings.ToLower(encoding)
		encoding = strings.ReplaceAll(encoding, "-", "_")
		encoding = strings.ReplaceAll(encoding, " ", "_")
		return encoding
	}

	mod.Dict.Set(val.InternString("register"), val.NewModuleFuncVal("register", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: register() takes 1 argument")
		}
		searchMu.Lock()
		searchFuncs = append(searchFuncs, args[0])
		searchMu.Unlock()
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("lookup"), val.NewModuleFuncVal("lookup", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: lookup() takes 1 argument")
		}
		enc := args[0].AsString()
		norm := normalizeEncoding(enc)

		searchMu.RLock()
		funcs := make([]val.Value, len(searchFuncs))
		copy(funcs, searchFuncs)
		searchMu.RUnlock()

		for _, fn := range funcs {
			res := ctx.Invoke1(fn, val.NewStringVal(norm))
			if !res.IsNone() {
				return res
			}
		}

		switch norm {
		case "utf_8", "utf8", "u8":
			utf8Enc, _ := mod.Dict.Get(val.InternString("utf_8_encode"))
			utf8Dec, _ := mod.Dict.Get(val.InternString("utf_8_decode"))
			return makeCodecInfo("utf-8", utf8Enc, utf8Dec)
		case "ascii", "us_ascii", "646":
			asciiEnc, _ := mod.Dict.Get(val.InternString("ascii_encode"))
			asciiDec, _ := mod.Dict.Get(val.InternString("ascii_decode"))
			return makeCodecInfo("ascii", asciiEnc, asciiDec)
		case "latin_1", "latin1", "iso_8859_1", "8859", "cp819":
			latinEnc, _ := mod.Dict.Get(val.InternString("latin_1_encode"))
			latinDec, _ := mod.Dict.Get(val.InternString("latin_1_decode"))
			return makeCodecInfo("latin-1", latinEnc, latinDec)
		case "raw_unicode_escape":
			rawEnc, _ := mod.Dict.Get(val.InternString("raw_unicode_escape_encode"))
			rawDec, _ := mod.Dict.Get(val.InternString("raw_unicode_escape_decode"))
			return makeCodecInfo("raw-unicode-escape", rawEnc, rawDec)
		}

		panic("LookupError: unknown encoding: " + enc)
	}))

	mod.Dict.Set(val.InternString("register_error"), val.NewModuleFuncVal("register_error", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: register_error() takes 2 arguments")
		}
		name := args[0].AsString()
		errorMu.Lock()
		errorHandlers[name] = args[1]
		errorMu.Unlock()
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("lookup_error"), val.NewModuleFuncVal("lookup_error", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: lookup_error() takes 1 argument")
		}
		name := args[0].AsString()
		errorMu.RLock()
		h, ok := errorHandlers[name]
		errorMu.RUnlock()
		if ok {
			return h
		}
		// Return standard error handler
		return val.NewModuleFuncVal(name, func(c *val.ExecContext, call val.Value, a []val.Value, kw []val.Value) val.Value {
			switch name {
			case "ignore":
				return val.NewTupleVal(val.NewStringVal(""), val.Int64Val(1))
			case "replace":
				return val.NewTupleVal(val.NewStringVal("\ufffd"), val.Int64Val(1))
			case "surrogateescape", "backslashreplace", "xmlcharrefreplace":
				return val.NewTupleVal(val.NewStringVal(""), val.Int64Val(1))
			case "strict":
				if len(a) > 0 {
					panic(a[0])
				}
				panic("UnicodeError: 'strict' codec error handler")
			default:
				if len(a) > 0 {
					panic(a[0])
				}
				panic("LookupError: unknown error handler name '" + name + "'")
			}
		})
	}))

	// UTF-8
	mod.Dict.Set(val.InternString("utf_8_encode"), val.NewModuleFuncVal("utf_8_encode", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: utf_8_encode() takes at least 1 argument")
		}
		s := args[0].AsString()
		b := []byte(s)
		return val.NewTupleVal(val.NewBytesVal(b), val.Int64Val(int64(utf8.RuneCountInString(s))))
	}))

	mod.Dict.Set(val.InternString("utf_8_decode"), val.NewModuleFuncVal("utf_8_decode", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: utf_8_decode() takes at least 1 argument")
		}
		var b []byte
		if args[0].IsBytes() {
			b = args[0].AsBytes().Data
		} else if args[0].IsByteArray() {
			b = args[0].AsByteArray().Data
		} else if args[0].IsMemoryView() {
			b = args[0].AsMemoryView().Buf
		} else {
			b = []byte(args[0].AsString())
		}
		return val.NewTupleVal(val.NewStringVal(string(b)), val.Int64Val(int64(len(b))))
	}))

	// Latin-1 (ISO-8859-1)
	mod.Dict.Set(val.InternString("latin_1_encode"), val.NewModuleFuncVal("latin_1_encode", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: latin_1_encode() takes at least 1 argument")
		}
		s := args[0].AsString()
		b := make([]byte, len(s))
		for i, r := range s {
			if r > 0xFF {
				panic("UnicodeEncodeError: 'latin-1' codec can't encode character")
			}
			b[i] = byte(r)
		}
		return val.NewTupleVal(val.NewBytesVal(b), val.Int64Val(int64(len(s))))
	}))

	mod.Dict.Set(val.InternString("latin_1_decode"), val.NewModuleFuncVal("latin_1_decode", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: latin_1_decode() takes at least 1 argument")
		}
		var b []byte
		if args[0].IsBytes() {
			b = args[0].AsBytes().Data
		} else if args[0].IsByteArray() {
			b = args[0].AsByteArray().Data
		} else if args[0].IsMemoryView() {
			b = args[0].AsMemoryView().Buf
		} else {
			b = []byte(args[0].AsString())
		}
		runes := make([]rune, len(b))
		for i, v := range b {
			runes[i] = rune(v)
		}
		return val.NewTupleVal(val.NewStringVal(string(runes)), val.Int64Val(int64(len(b))))
	}))

	// ASCII
	mod.Dict.Set(val.InternString("ascii_encode"), val.NewModuleFuncVal("ascii_encode", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: ascii_encode() takes at least 1 argument")
		}
		s := args[0].AsString()
		b := make([]byte, len(s))
		for i, r := range s {
			if r > 0x7F {
				panic("UnicodeEncodeError: 'ascii' codec can't encode character")
			}
			b[i] = byte(r)
		}
		return val.NewTupleVal(val.NewBytesVal(b), val.Int64Val(int64(len(s))))
	}))

	mod.Dict.Set(val.InternString("ascii_decode"), val.NewModuleFuncVal("ascii_decode", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: ascii_decode() takes at least 1 argument")
		}
		var b []byte
		if args[0].IsBytes() {
			b = args[0].AsBytes().Data
		} else if args[0].IsByteArray() {
			b = args[0].AsByteArray().Data
		} else if args[0].IsMemoryView() {
			b = args[0].AsMemoryView().Buf
		} else {
			b = []byte(args[0].AsString())
		}
		return val.NewTupleVal(val.NewStringVal(string(b)), val.Int64Val(int64(len(b))))
	}))

	// Raw unicode escape & unicode escape
	mod.Dict.Set(val.InternString("raw_unicode_escape_encode"), val.NewModuleFuncVal("raw_unicode_escape_encode", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: raw_unicode_escape_encode() takes at least 1 argument")
		}
		s := args[0].AsString()
		return val.NewTupleVal(val.NewBytesVal([]byte(s)), val.Int64Val(int64(len(s))))
	}))

	mod.Dict.Set(val.InternString("raw_unicode_escape_decode"), val.NewModuleFuncVal("raw_unicode_escape_decode", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: raw_unicode_escape_decode() takes at least 1 argument")
		}
		var b []byte
		if args[0].IsBytes() {
			b = args[0].AsBytes().Data
		} else if args[0].IsByteArray() {
			b = args[0].AsByteArray().Data
		} else {
			b = []byte(args[0].AsString())
		}
		return val.NewTupleVal(val.NewStringVal(string(b)), val.Int64Val(int64(len(b))))
	}))

	mod.Dict.Set(val.InternString("encode"), val.NewModuleFuncVal("encode", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: encode() takes at least 1 argument")
		}
		enc := "utf-8"
		if len(args) > 1 && !args[1].IsNone() {
			enc = args[1].AsString()
		}
		s := args[0].AsString()
		switch strings.ToLower(enc) {
		case "utf-8", "utf8":
			return val.NewBytesVal([]byte(s))
		case "latin-1", "latin1", "iso-8859-1":
			b := make([]byte, len(s))
			for i, r := range s {
				b[i] = byte(r)
			}
			return val.NewBytesVal(b)
		case "ascii":
			return val.NewBytesVal([]byte(s))
		default:
			return val.NewBytesVal([]byte(s))
		}
	}))

	mod.Dict.Set(val.InternString("decode"), val.NewModuleFuncVal("decode", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: decode() takes at least 1 argument")
		}
		var b []byte
		if args[0].IsBytes() {
			b = args[0].AsBytes().Data
		} else if args[0].IsByteArray() {
			b = args[0].AsByteArray().Data
		} else if args[0].IsMemoryView() {
			b = args[0].AsMemoryView().Buf
		} else {
			b = []byte(args[0].AsString())
		}
		return val.NewStringVal(string(b))
	}))

	return modVal
}
