package val

import (
	"bytes"
	"encoding/hex"
	"strconv"
)

func bytesMethodArg(v Value) ([]byte, bool) {
	if v.IsBytes() {
		return v.AsBytes().Data, true
	}
	if v.IsByteArray() {
		return v.AsByteArray().Data, true
	}
	if v.IsMemoryView() {
		return v.AsMemoryView().Buf, true
	}
	return nil, false
}

func bytesSelf(name string, args []Value) []byte {
	if len(args) < 1 {
		panic("TypeError: descriptor '" + name + "' of 'bytes' object needs an argument")
	}
	b, ok := bytesMethodArg(args[0])
	if !ok {
		panic("TypeError: descriptor '" + name + "' requires a bytes-like object but received a '" + args[0].Type().Name + "'")
	}
	return b
}

func initBytesMethods() {
	maketransFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		pa := args
		if len(pa) > 0 && pa[0].IsType() {
			pa = pa[1:]
		}
		if len(pa) != 2 {
			panic("TypeError: maketrans() takes exactly 2 arguments")
		}
		f, ok1 := bytesMethodArg(pa[0])
		t, ok2 := bytesMethodArg(pa[1])
		if !ok1 || !ok2 {
			panic("TypeError: maketrans() arguments must be bytes-like objects")
		}
		if len(f) != len(t) {
			panic("ValueError: maketrans arguments must have same length")
		}
		table := make([]byte, 256)
		for i := 0; i < 256; i++ {
			table[i] = byte(i)
		}
		for i := 0; i < len(f); i++ {
			table[f[i]] = t[i]
		}
		return NewBytesVal(table)
	}

	translateFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("translate", args)
		pa := args[1:]
		if len(pa) < 1 {
			panic("TypeError: translate() takes at least 1 argument")
		}
		var delMap [256]bool
		if len(pa) > 1 && !pa[1].IsNone() {
			delBytes, ok := bytesMethodArg(pa[1])
			if !ok {
				panic("TypeError: deletechars argument must be bytes-like")
			}
			for _, b := range delBytes {
				delMap[b] = true
			}
		}
		var tableBytes []byte
		if !pa[0].IsNone() {
			tb, ok := bytesMethodArg(pa[0])
			if !ok || len(tb) != 256 {
				panic("ValueError: translation table must be 256 characters long")
			}
			tableBytes = tb
		}
		out := make([]byte, 0, len(self))
		for _, b := range self {
			if delMap[b] {
				continue
			}
			if tableBytes != nil {
				out = append(out, tableBytes[b])
			} else {
				out = append(out, b)
			}
		}
		if args[0].IsByteArray() {
			return NewByteArrayVal(out)
		}
		return NewBytesVal(out)
	}

	ljustFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("ljust", args)
		if len(args) < 2 {
			panic("TypeError: ljust() takes at least 1 argument")
		}
		w := int(args[1].AsInt64())
		fill := byte(' ')
		if len(args) > 2 {
			fb, ok := bytesMethodArg(args[2])
			if !ok || len(fb) != 1 {
				panic("TypeError: fillchar must be a bytes object of length 1")
			}
			fill = fb[0]
		}
		if len(self) >= w {
			cp := make([]byte, len(self))
			copy(cp, self)
			if args[0].IsByteArray() {
				return NewByteArrayVal(cp)
			}
			return NewBytesVal(cp)
		}
		out := make([]byte, w)
		copy(out, self)
		for i := len(self); i < w; i++ {
			out[i] = fill
		}
		if args[0].IsByteArray() {
			return NewByteArrayVal(out)
		}
		return NewBytesVal(out)
	}

	rjustFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("rjust", args)
		if len(args) < 2 {
			panic("TypeError: rjust() takes at least 1 argument")
		}
		w := int(args[1].AsInt64())
		fill := byte(' ')
		if len(args) > 2 {
			fb, ok := bytesMethodArg(args[2])
			if !ok || len(fb) != 1 {
				panic("TypeError: fillchar must be a bytes object of length 1")
			}
			fill = fb[0]
		}
		if len(self) >= w {
			cp := make([]byte, len(self))
			copy(cp, self)
			if args[0].IsByteArray() {
				return NewByteArrayVal(cp)
			}
			return NewBytesVal(cp)
		}
		out := make([]byte, w)
		pad := w - len(self)
		for i := 0; i < pad; i++ {
			out[i] = fill
		}
		copy(out[pad:], self)
		if args[0].IsByteArray() {
			return NewByteArrayVal(out)
		}
		return NewBytesVal(out)
	}

	joinFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("join", args)
		if len(args) < 2 {
			panic("TypeError: join() takes 1 argument")
		}
		items := ExtractIterableItems(ctx, args[1])
		var totalLen int
		rawItems := make([][]byte, len(items))
		for i, item := range items {
			b, ok := bytesMethodArg(item)
			if !ok {
				panic("TypeError: sequence item " + strconv.Itoa(i) + ": expected a bytes-like object")
			}
			rawItems[i] = b
			totalLen += len(b)
		}
		if len(rawItems) > 1 {
			totalLen += (len(rawItems) - 1) * len(self)
		}
		out := make([]byte, 0, totalLen)
		for i, raw := range rawItems {
			if i > 0 && len(self) > 0 {
				out = append(out, self...)
			}
			out = append(out, raw...)
		}
		if args[0].IsByteArray() {
			return NewByteArrayVal(out)
		}
		return NewBytesVal(out)
	}

	replaceFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("replace", args)
		if len(args) < 3 {
			panic("TypeError: replace() takes at least 2 arguments")
		}
		oldBytes, ok1 := bytesMethodArg(args[1])
		newBytes, ok2 := bytesMethodArg(args[2])
		if !ok1 || !ok2 {
			panic("TypeError: replace arguments must be bytes-like")
		}
		cnt := -1
		if len(args) > 3 {
			cnt = int(args[3].AsInt64())
		}
		out := bytes.Replace(self, oldBytes, newBytes, cnt)
		if args[0].IsByteArray() {
			return NewByteArrayVal(out)
		}
		return NewBytesVal(out)
	}

	splitFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("split", args)
		maxsplit := -1
		if len(args) > 2 {
			maxsplit = int(args[2].AsInt64())
		}
		var parts [][]byte
		if len(args) > 1 && !args[1].IsNone() {
			sepBytes, ok := bytesMethodArg(args[1])
			if !ok {
				panic("TypeError: sep must be bytes-like or None")
			}
			if len(sepBytes) == 0 {
				panic("ValueError: empty separator")
			}
			if maxsplit >= 0 {
				parts = bytes.SplitN(self, sepBytes, maxsplit+1)
			} else {
				parts = bytes.Split(self, sepBytes)
			}
		} else {
			if maxsplit >= 0 {
				parts = bytes.Fields(self)
				if len(parts) > maxsplit+1 {
					// Join remainder
					rest := bytes.Join(parts[maxsplit:], []byte(" "))
					parts = append(parts[:maxsplit], rest)
				}
			} else {
				parts = bytes.Fields(self)
			}
		}
		out := make([]Value, len(parts))
		for i, p := range parts {
			if args[0].IsByteArray() {
				out[i] = NewByteArrayVal(p)
			} else {
				out[i] = NewBytesVal(p)
			}
		}
		return NewListVal(out...)
	}

	stripFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("strip", args)
		var cut string = " \t\r\n\x00"
		if len(args) > 1 && !args[1].IsNone() {
			if cb, ok := bytesMethodArg(args[1]); ok {
				cut = string(cb)
			}
		}
		out := bytes.Trim(self, cut)
		if args[0].IsByteArray() {
			return NewByteArrayVal(out)
		}
		return NewBytesVal(out)
	}

	lstripFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("lstrip", args)
		var cut string = " \t\r\n\x00"
		if len(args) > 1 && !args[1].IsNone() {
			if cb, ok := bytesMethodArg(args[1]); ok {
				cut = string(cb)
			}
		}
		out := bytes.TrimLeft(self, cut)
		if args[0].IsByteArray() {
			return NewByteArrayVal(out)
		}
		return NewBytesVal(out)
	}

	rstripFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("rstrip", args)
		var cut string = " \t\r\n\x00"
		if len(args) > 1 && !args[1].IsNone() {
			if cb, ok := bytesMethodArg(args[1]); ok {
				cut = string(cb)
			}
		}
		out := bytes.TrimRight(self, cut)
		if args[0].IsByteArray() {
			return NewByteArrayVal(out)
		}
		return NewBytesVal(out)
	}

	lowerFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("lower", args)
		out := bytes.ToLower(self)
		if args[0].IsByteArray() {
			return NewByteArrayVal(out)
		}
		return NewBytesVal(out)
	}

	upperFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("upper", args)
		out := bytes.ToUpper(self)
		if args[0].IsByteArray() {
			return NewByteArrayVal(out)
		}
		return NewBytesVal(out)
	}

	fromhexFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		pa := args
		isByteArray := false
		if len(pa) > 0 && pa[0].IsType() {
			if pa[0].AsType() == ByteArrayType {
				isByteArray = true
			}
			pa = pa[1:]
		}
		if len(pa) < 1 || !pa[0].IsAnyString() {
			panic("TypeError: fromhex() argument must be str")
		}
		s := pa[0].AsString()
		var hexChars []byte
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
				continue
			}
			hexChars = append(hexChars, c)
		}
		if len(hexChars)%2 != 0 {
			panic("ValueError: fromhex() arg length must be even")
		}
		decoded, err := hex.DecodeString(string(hexChars))
		if err != nil {
			panic("ValueError: non-hexadecimal number found in fromhex() arg")
		}
		if isByteArray {
			return NewByteArrayVal(decoded)
		}
		return NewBytesVal(decoded)
	}

	hexFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("hex", args)
		return NewStringVal(hex.EncodeToString(self))
	}

	for _, t := range []*PyType{BytesType, ByteArrayType} {
		t.Dict.Set(InternString("maketrans"), NewBuiltinFuncVal("maketrans", maketransFn))
		t.Dict.Set(InternString("translate"), NewBuiltinFuncVal("translate", translateFn))
		t.Dict.Set(InternString("ljust"), NewBuiltinFuncVal("ljust", ljustFn))
		t.Dict.Set(InternString("rjust"), NewBuiltinFuncVal("rjust", rjustFn))
		t.Dict.Set(InternString("join"), NewBuiltinFuncVal("join", joinFn))
		t.Dict.Set(InternString("replace"), NewBuiltinFuncVal("replace", replaceFn))
		t.Dict.Set(InternString("split"), NewBuiltinFuncVal("split", splitFn))
		t.Dict.Set(InternString("strip"), NewBuiltinFuncVal("strip", stripFn))
		t.Dict.Set(InternString("lstrip"), NewBuiltinFuncVal("lstrip", lstripFn))
		t.Dict.Set(InternString("rstrip"), NewBuiltinFuncVal("rstrip", rstripFn))
		t.Dict.Set(InternString("lower"), NewBuiltinFuncVal("lower", lowerFn))
		t.Dict.Set(InternString("upper"), NewBuiltinFuncVal("upper", upperFn))
		t.Dict.Set(InternString("fromhex"), NewBuiltinFuncVal("fromhex", fromhexFn))
		t.Dict.Set(InternString("hex"), NewBuiltinFuncVal("hex", hexFn))
	}
}
