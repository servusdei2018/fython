package val

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
	"unsafe"
)

func isASCIIBytesSpace(b byte) bool {
	return b == ' ' || (b >= '\t' && b <= '\r')
}

func bytesSplitWhitespace(self []byte, maxsplit int, fromRight bool) [][]byte {
	if maxsplit < 0 {
		return bytes.FieldsFunc(self, func(r rune) bool {
			return r < 128 && isASCIIBytesSpace(byte(r))
		})
	}
	var parts [][]byte
	if !fromRight {
		i, n := 0, len(self)
		for i < n && isASCIIBytesSpace(self[i]) {
			i++
		}
		for i < n {
			if len(parts) == maxsplit {
				parts = append(parts, self[i:])
				return parts
			}
			start := i
			for i < n && !isASCIIBytesSpace(self[i]) {
				i++
			}
			parts = append(parts, self[start:i])
			for i < n && isASCIIBytesSpace(self[i]) {
				i++
			}
		}
		return parts
	}
	end := len(self)
	for end > 0 && isASCIIBytesSpace(self[end-1]) {
		end--
	}
	for end > 0 {
		if len(parts) == maxsplit {
			parts = append(parts, self[:end])
			break
		}
		wordEnd := end
		for end > 0 && !isASCIIBytesSpace(self[end-1]) {
			end--
		}
		parts = append(parts, self[end:wordEnd])
		for end > 0 && isASCIIBytesSpace(self[end-1]) {
			end--
		}
	}
	for l, r := 0, len(parts)-1; l < r; l, r = l+1, r-1 {
		parts[l], parts[r] = parts[r], parts[l]
	}
	return parts
}

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
	if v.IsInstance() && len(v.AsInstance().Slots) > 0 {
		return bytesMethodArg(v.AsInstance().Slots[0])
	}
	return nil, false
}

// DecodeBytesToString decodes binary data into a string using the specified encoding.
func DecodeBytesToString(data []byte, encoding string) (string, error) {
	if encoding == "" || encoding == "utf-8" || encoding == "utf8" || encoding == "UTF-8" {
		return string(data), nil
	}
	norm := strings.ToLower(encoding)
	norm = strings.ReplaceAll(norm, "-", "_")
	norm = strings.ReplaceAll(norm, " ", "_")

	switch norm {
	case "", "utf_8", "utf8", "u8", "utf_8_sig", "utf8_sig":
		return string(data), nil
	case "latin_1", "latin1", "iso_8859_1", "iso8859_1", "8859", "cp819", "raw_unicode_escape":
		if IsASCIIBytes(data) {
			return string(data), nil
		}
		runes := make([]rune, len(data))
		for i, b := range data {
			runes[i] = rune(b)
		}
		return string(runes), nil
	case "ascii", "us_ascii":
		if IsASCIIBytes(data) {
			return string(data), nil
		}
		for i, b := range data {
			if b > 127 {
				return "", fmt.Errorf("UnicodeDecodeError: 'ascii' codec can't decode byte 0x%02x in position %d: ordinal not in range(128)", b, i)
			}
		}
		return string(data), nil
	default:
		_ = utf8.RuneCount(data)
		return string(data), nil
	}
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
			ctx.Raise(NewException(TypeErrorType, "maketrans() takes exactly 2 arguments"))
			return NoneVal()
		}
		f, ok1 := bytesMethodArg(pa[0])
		t, ok2 := bytesMethodArg(pa[1])
		if !ok1 || !ok2 {
			ctx.Raise(NewException(TypeErrorType, "maketrans() arguments must be bytes-like objects"))
			return NoneVal()
		}
		if len(f) != len(t) {
			ctx.Raise(NewException(ValueErrorType, "maketrans arguments must have same length"))
			return NoneVal()
		}
		table := make([]byte, 256)
		for i := range 256 {
			table[i] = byte(i)
		}
		for i := range f {
			table[f[i]] = t[i]
		}
		return NewBytesVal(table)
	}

	translateFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("translate", args)
		pa := args[1:]
		if len(pa) < 1 {
			ctx.Raise(NewException(TypeErrorType, "translate() takes at least 1 argument"))
			return NoneVal()
		}
		var delMap [256]bool
		if len(pa) > 1 && !pa[1].IsNone() {
			delBytes, ok := bytesMethodArg(pa[1])
			if !ok {
				ctx.Raise(NewException(TypeErrorType, "deletechars argument must be bytes-like"))
				return NoneVal()
			}
			for _, b := range delBytes {
				delMap[b] = true
			}
		}
		var tableBytes []byte
		if !pa[0].IsNone() {
			tb, ok := bytesMethodArg(pa[0])
			if !ok || len(tb) != 256 {
				ctx.Raise(NewException(ValueErrorType, "translation table must be 256 characters long"))
				return NoneVal()
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
			ctx.Raise(NewException(TypeErrorType, "ljust() takes at least 1 argument"))
			return NoneVal()
		}
		w := int(args[1].AsInt64())
		fill := byte(' ')
		if len(args) > 2 {
			fb, ok := bytesMethodArg(args[2])
			if !ok || len(fb) != 1 {
				ctx.Raise(NewException(TypeErrorType, "fillchar must be a bytes object of length 1"))
				return NoneVal()
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
			ctx.Raise(NewException(TypeErrorType, "rjust() takes at least 1 argument"))
			return NoneVal()
		}
		w := int(args[1].AsInt64())
		fill := byte(' ')
		if len(args) > 2 {
			fb, ok := bytesMethodArg(args[2])
			if !ok || len(fb) != 1 {
				ctx.Raise(NewException(TypeErrorType, "fillchar must be a bytes object of length 1"))
				return NoneVal()
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
		for i := range pad {
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
			ctx.Raise(NewException(TypeErrorType, "join() takes 1 argument"))
			return NoneVal()
		}
		var items []Value
		switch {
		case args[1].IsList() && args[1].Type() == ListType:
			items = args[1].AsList().Items
		case args[1].IsTuple() && args[1].Type() == TupleType:
			items = args[1].AsTuple().Items
		default:
			items = ExtractIterableItems(ctx, args[1])
		}
		var totalLen int
		for i, item := range items {
			b, ok := bytesMethodArg(item)
			if !ok {
				ctx.Raise(NewException(TypeErrorType, "sequence item"+strconv.Itoa(i)+": expected a bytes-like object"))
				return NoneVal()
			}
			totalLen += len(b)
		}
		if len(items) > 1 {
			totalLen += (len(items) - 1) * len(self)
		}
		out := make([]byte, 0, totalLen)
		for i, item := range items {
			if i > 0 && len(self) > 0 {
				out = append(out, self...)
			}
			b, _ := bytesMethodArg(item)
			out = append(out, b...)
		}
		if args[0].IsByteArray() {
			return NewByteArrayVal(out)
		}
		return NewBytesVal(out)
	}

	replaceFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("replace", args)
		if len(args) < 3 {
			ctx.Raise(NewException(TypeErrorType, "replace() takes at least 2 arguments"))
			return NoneVal()
		}
		oldBytes, ok1 := bytesMethodArg(args[1])
		newBytes, ok2 := bytesMethodArg(args[2])
		if !ok1 || !ok2 {
			ctx.Raise(NewException(TypeErrorType, "replace arguments must be bytes-like"))
			return NoneVal()
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
		isBA := args[0].IsByteArray()
		if len(args) > 1 && !args[1].IsNone() {
			sepBytes, ok := bytesMethodArg(args[1])
			if !ok {
				ctx.Raise(NewException(TypeErrorType, "sep must be bytes-like or None"))
				return NoneVal()
			}
			if len(sepBytes) == 0 {
				ctx.Raise(NewException(ValueErrorType, "empty separator"))
				return NoneVal()
			}
			if maxsplit == 0 {
				out := make([]Value, 1)
				if isBA {
					out[0] = NewByteArrayVal(self)
				} else {
					out[0] = NewBytesVal(self)
				}
				return NewListFromOwnedSlice(out)
			}
			count := bytes.Count(self, sepBytes) + 1
			if maxsplit >= 0 && count > maxsplit+1 {
				count = maxsplit + 1
			}
			out := make([]Value, 0, count)
			rest := self
			if len(sepBytes) == 1 {
				b := sepBytes[0]
				for maxsplit < 0 || len(out) < maxsplit {
					idx := bytes.IndexByte(rest, b)
					if idx < 0 {
						break
					}
					if isBA {
						out = append(out, NewByteArrayVal(rest[:idx]))
					} else {
						out = append(out, NewBytesVal(rest[:idx]))
					}
					rest = rest[idx+1:]
				}
			} else {
				sepLen := len(sepBytes)
				for maxsplit < 0 || len(out) < maxsplit {
					idx := bytes.Index(rest, sepBytes)
					if idx < 0 {
						break
					}
					if isBA {
						out = append(out, NewByteArrayVal(rest[:idx]))
					} else {
						out = append(out, NewBytesVal(rest[:idx]))
					}
					rest = rest[idx+sepLen:]
				}
			}
			if isBA {
				out = append(out, NewByteArrayVal(rest))
			} else {
				out = append(out, NewBytesVal(rest))
			}
			return NewListFromOwnedSlice(out)
		}
		parts := bytesSplitWhitespace(self, maxsplit, false)
		out := make([]Value, len(parts))
		for i, p := range parts {
			if isBA {
				out[i] = NewByteArrayVal(p)
			} else {
				out[i] = NewBytesVal(p)
			}
		}
		return NewListFromOwnedSlice(out)
	}

	rsplitFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("rsplit", args)
		maxsplit := -1
		if len(args) > 2 {
			maxsplit = int(args[2].AsInt64())
		}
		isBA := args[0].IsByteArray()
		if len(args) > 1 && !args[1].IsNone() {
			sepBytes, ok := bytesMethodArg(args[1])
			if !ok {
				ctx.Raise(NewException(TypeErrorType, "sep must be bytes-like or None"))
				return NoneVal()
			}
			if len(sepBytes) == 0 {
				ctx.Raise(NewException(ValueErrorType, "empty separator"))
				return NoneVal()
			}
			if maxsplit < 0 {
				count := bytes.Count(self, sepBytes) + 1
				out := make([]Value, 0, count)
				rest := self
				sepLen := len(sepBytes)
				for {
					var idx int
					if sepLen == 1 {
						idx = bytes.IndexByte(rest, sepBytes[0])
					} else {
						idx = bytes.Index(rest, sepBytes)
					}
					if idx < 0 {
						break
					}
					if isBA {
						out = append(out, NewByteArrayVal(rest[:idx]))
					} else {
						out = append(out, NewBytesVal(rest[:idx]))
					}
					rest = rest[idx+sepLen:]
				}
				if isBA {
					out = append(out, NewByteArrayVal(rest))
				} else {
					out = append(out, NewBytesVal(rest))
				}
				return NewListFromOwnedSlice(out)
			}
			out := make([]Value, 0, min(maxsplit+1, 16))
			rest := self
			sepLen := len(sepBytes)
			for len(out) < maxsplit {
				var idx int
				if sepLen == 1 {
					idx = bytes.LastIndexByte(rest, sepBytes[0])
				} else {
					idx = bytes.LastIndex(rest, sepBytes)
				}
				if idx < 0 {
					break
				}
				if isBA {
					out = append(out, NewByteArrayVal(rest[idx+sepLen:]))
				} else {
					out = append(out, NewBytesVal(rest[idx+sepLen:]))
				}
				rest = rest[:idx]
			}
			if isBA {
				out = append(out, NewByteArrayVal(rest))
			} else {
				out = append(out, NewBytesVal(rest))
			}
			for l, r := 0, len(out)-1; l < r; l, r = l+1, r-1 {
				out[l], out[r] = out[r], out[l]
			}
			return NewListFromOwnedSlice(out)
		}
		parts := bytesSplitWhitespace(self, maxsplit, true)
		out := make([]Value, len(parts))
		for i, p := range parts {
			if isBA {
				out[i] = NewByteArrayVal(p)
			} else {
				out[i] = NewBytesVal(p)
			}
		}
		return NewListFromOwnedSlice(out)
	}

	stripFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("strip", args)
		cut := " \t\r\n\x00"
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
		cut := " \t\r\n\x00"
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
		cut := " \t\r\n\x00"
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
			ctx.Raise(NewException(TypeErrorType, "fromhex() argument must be str"))
			return NoneVal()
		}
		s := pa[0].AsString()
		var hexChars []byte
		for i := range len(s) {
			c := s[i]
			if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
				continue
			}
			hexChars = append(hexChars, c)
		}
		if len(hexChars)%2 != 0 {
			ctx.Raise(NewException(ValueErrorType, "fromhex() arg length must be even"))
			return NoneVal()
		}
		decoded, err := hex.DecodeString(string(hexChars))
		if err != nil {
			ctx.Raise(NewException(ValueErrorType, "non-hexadecimal number found in fromhex() arg"))
			return NoneVal()
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

	bytesArg := func(v Value) []byte {
		if v.IsBytes() {
			return v.AsBytes().Data
		}
		if v.IsByteArray() {
			return v.AsByteArray().Data
		}
		if v.IsMemoryView() {
			return v.AsMemoryView().Buf
		}
		if v.IsInt64() {
			return []byte{byte(v.AsInt64())}
		}
		panic("TypeError: a bytes-like object is required, not '" + v.Type().Name + "'")
	}

	findFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("find", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "find expected at least 1 argument, got 0"))
			return NoneVal()
		}
		sub := bytesArg(args[1])
		start := 0
		end := len(self)
		if len(args) > 2 && args[2].IsInt64() {
			start = int(args[2].AsInt64())
			if start < 0 {
				start += len(self)
				if start < 0 {
					start = 0
				}
			}
			if start > len(self) {
				start = len(self)
			}
		}
		if len(args) > 3 && args[3].IsInt64() {
			end = int(args[3].AsInt64())
			if end < 0 {
				end += len(self)
				if end < 0 {
					end = 0
				}
			}
			if end > len(self) {
				end = len(self)
			}
		}
		if start > end {
			return Int64Val(-1)
		}
		idx := bytes.Index(self[start:end], sub)
		if idx == -1 {
			return Int64Val(-1)
		}
		return Int64Val(int64(start + idx))
	}

	rfindFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("rfind", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "rfind expected at least 1 argument, got 0"))
			return NoneVal()
		}
		sub := bytesArg(args[1])
		start := 0
		end := len(self)
		if len(args) > 2 && args[2].IsInt64() {
			start = int(args[2].AsInt64())
			if start < 0 {
				start += len(self)
				if start < 0 {
					start = 0
				}
			}
			if start > len(self) {
				start = len(self)
			}
		}
		if len(args) > 3 && args[3].IsInt64() {
			end = int(args[3].AsInt64())
			if end < 0 {
				end += len(self)
				if end < 0 {
					end = 0
				}
			}
			if end > len(self) {
				end = len(self)
			}
		}
		if start > end {
			return Int64Val(-1)
		}
		idx := bytes.LastIndex(self[start:end], sub)
		if idx == -1 {
			return Int64Val(-1)
		}
		return Int64Val(int64(start + idx))
	}

	startswithFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("startswith", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "startswith expected at least 1 argument, got 0"))
			return NoneVal()
		}
		sLen := len(self)
		start := 0
		end := sLen
		if len(args) > 2 && !args[2].IsNone() {
			if args[2].IsInt64() {
				start = int(args[2].AsInt64())
			}
			if start < 0 {
				start += sLen
				if start < 0 {
					start = 0
				}
			} else if start > sLen {
				start = sLen
			}
		}
		if len(args) > 3 && !args[3].IsNone() {
			if args[3].IsInt64() {
				end = int(args[3].AsInt64())
			}
			if end < 0 {
				end += sLen
				if end < 0 {
					end = 0
				}
			} else if end > sLen {
				end = sLen
			}
		}
		if start > end {
			start = end
		}
		self = self[start:end]
		if args[1].IsTuple() {
			for _, item := range args[1].AsTuple().Items {
				sub := bytesArg(item)
				if bytes.HasPrefix(self, sub) {
					return BoolVal(true)
				}
			}
			return BoolVal(false)
		}
		sub := bytesArg(args[1])
		return BoolVal(bytes.HasPrefix(self, sub))
	}

	endswithFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("endswith", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "endswith expected at least 1 argument, got 0"))
			return NoneVal()
		}
		sLen := len(self)
		start := 0
		end := sLen
		if len(args) > 2 && !args[2].IsNone() {
			if args[2].IsInt64() {
				start = int(args[2].AsInt64())
			}
			if start < 0 {
				start += sLen
				if start < 0 {
					start = 0
				}
			} else if start > sLen {
				start = sLen
			}
		}
		if len(args) > 3 && !args[3].IsNone() {
			if args[3].IsInt64() {
				end = int(args[3].AsInt64())
			}
			if end < 0 {
				end += sLen
				if end < 0 {
					end = 0
				}
			} else if end > sLen {
				end = sLen
			}
		}
		if start > end {
			start = end
		}
		self = self[start:end]
		if args[1].IsTuple() {
			for _, item := range args[1].AsTuple().Items {
				sub := bytesArg(item)
				if bytes.HasSuffix(self, sub) {
					return BoolVal(true)
				}
			}
			return BoolVal(false)
		}
		sub := bytesArg(args[1])
		return BoolVal(bytes.HasSuffix(self, sub))
	}

	countFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("count", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "count expected at least 1 argument, got 0"))
			return NoneVal()
		}
		sLen := len(self)
		start := 0
		end := sLen
		if len(args) > 2 && !args[2].IsNone() {
			if args[2].IsInt64() {
				start = int(args[2].AsInt64())
			}
			if start < 0 {
				start += sLen
				if start < 0 {
					start = 0
				}
			} else if start > sLen {
				start = sLen
			}
		}
		if len(args) > 3 && !args[3].IsNone() {
			if args[3].IsInt64() {
				end = int(args[3].AsInt64())
			}
			if end < 0 {
				end += sLen
				if end < 0 {
					end = 0
				}
			} else if end > sLen {
				end = sLen
			}
		}
		if start > end {
			start = end
		}
		self = self[start:end]
		var sub []byte
		if args[1].IsInt64() {
			sub = []byte{byte(args[1].AsInt64())}
		} else {
			sub = bytesArg(args[1])
		}
		return Int64Val(int64(bytes.Count(self, sub)))
	}

	indexFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		res := findFn(ctx, callable, args, kwnames)
		if res.IsInt64() && res.AsInt64() < 0 {
			ctx.Raise(NewException(ValueErrorType, "subsection not found"))
			return NoneVal()
		}
		return res
	}

	rindexFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		res := rfindFn(ctx, callable, args, kwnames)
		if res.IsInt64() && res.AsInt64() < 0 {
			ctx.Raise(NewException(ValueErrorType, "subsection not found"))
			return NoneVal()
		}
		return res
	}

	partitionFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("partition", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "partition() takes exactly one argument (0 given)"))
			return NoneVal()
		}
		sep, ok := bytesMethodArg(args[1])
		if !ok {
			ctx.Raise(NewException(TypeErrorType, "a bytes-like object is required"))
			return NoneVal()
		}
		if len(sep) == 0 {
			ctx.Raise(NewException(ValueErrorType, "empty separator"))
			return NoneVal()
		}
		wrap := NewBytesVal
		if args[0].IsByteArray() {
			wrap = NewByteArrayVal
		}
		if before, after, found := bytes.Cut(self, sep); found {
			return NewTupleVal(wrap(before), wrap(sep), wrap(after))
		}
		return NewTupleVal(wrap(self), wrap(nil), wrap(nil))
	}

	rpartitionFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("rpartition", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "rpartition() takes exactly one argument (0 given)"))
			return NoneVal()
		}
		sep, ok := bytesMethodArg(args[1])
		if !ok {
			ctx.Raise(NewException(TypeErrorType, "a bytes-like object is required"))
			return NoneVal()
		}
		if len(sep) == 0 {
			ctx.Raise(NewException(ValueErrorType, "empty separator"))
			return NoneVal()
		}
		wrap := NewBytesVal
		if args[0].IsByteArray() {
			wrap = NewByteArrayVal
		}
		if idx := bytes.LastIndex(self, sep); idx >= 0 {
			return NewTupleVal(wrap(self[:idx]), wrap(sep), wrap(self[idx+len(sep):]))
		}
		return NewTupleVal(wrap(nil), wrap(nil), wrap(self))
	}

	removeprefixFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("removeprefix", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "removeprefix() takes exactly one argument (0 given)"))
			return NoneVal()
		}
		p, ok := bytesMethodArg(args[1])
		if !ok {
			ctx.Raise(NewException(TypeErrorType, "removeprefix argument must be bytes-like"))
			return NoneVal()
		}
		out := bytes.TrimPrefix(self, p)
		if args[0].IsByteArray() {
			cp := make([]byte, len(out))
			copy(cp, out)
			return NewByteArrayVal(cp)
		}
		return NewBytesVal(out)
	}

	removesuffixFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("removesuffix", args)
		if len(args) < 2 {
			ctx.Raise(NewException(TypeErrorType, "removesuffix() takes exactly one argument (0 given)"))
			return NoneVal()
		}
		p, ok := bytesMethodArg(args[1])
		if !ok {
			ctx.Raise(NewException(TypeErrorType, "removesuffix argument must be bytes-like"))
			return NoneVal()
		}
		out := bytes.TrimSuffix(self, p)
		if args[0].IsByteArray() {
			cp := make([]byte, len(out))
			copy(cp, out)
			return NewByteArrayVal(cp)
		}
		return NewBytesVal(out)
	}

	isasciiFn := func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		self := bytesSelf("isascii", args)
		if len(self) == 0 {
			return BoolVal(true)
		}
		return BoolVal(isASCII(unsafe.String(unsafe.SliceData(self), len(self))))
	}

	for _, t := range []*PyType{BytesType, ByteArrayType} {
		t.Dict.Set(InternString("count"), NewBuiltinFuncVal("count", countFn))
		t.Dict.Set(InternString("maketrans"), NewBuiltinFuncVal("maketrans", maketransFn))
		t.Dict.Set(InternString("translate"), NewBuiltinFuncVal("translate", translateFn))
		t.Dict.Set(InternString("ljust"), NewBuiltinFuncVal("ljust", ljustFn))
		t.Dict.Set(InternString("rjust"), NewBuiltinFuncVal("rjust", rjustFn))
		t.Dict.Set(InternString("join"), NewBuiltinFuncVal("join", joinFn))
		t.Dict.Set(InternString("replace"), NewBuiltinFuncVal("replace", replaceFn))
		t.Dict.Set(InternString("split"), NewBuiltinFuncVal("split", splitFn))
		t.Dict.Set(InternString("rsplit"), NewBuiltinFuncVal("rsplit", rsplitFn))
		t.Dict.Set(InternString("strip"), NewBuiltinFuncVal("strip", stripFn))
		t.Dict.Set(InternString("lstrip"), NewBuiltinFuncVal("lstrip", lstripFn))
		t.Dict.Set(InternString("rstrip"), NewBuiltinFuncVal("rstrip", rstripFn))
		t.Dict.Set(InternString("lower"), NewBuiltinFuncVal("lower", lowerFn))
		t.Dict.Set(InternString("upper"), NewBuiltinFuncVal("upper", upperFn))
		t.Dict.Set(InternString("fromhex"), NewBuiltinFuncVal("fromhex", fromhexFn))
		t.Dict.Set(InternString("hex"), NewBuiltinFuncVal("hex", hexFn))
		t.Dict.Set(InternString("find"), NewBuiltinFuncVal("find", findFn))
		t.Dict.Set(InternString("rfind"), NewBuiltinFuncVal("rfind", rfindFn))
		t.Dict.Set(InternString("index"), NewBuiltinFuncVal("index", indexFn))
		t.Dict.Set(InternString("rindex"), NewBuiltinFuncVal("rindex", rindexFn))
		t.Dict.Set(InternString("partition"), NewBuiltinFuncVal("partition", partitionFn))
		t.Dict.Set(InternString("rpartition"), NewBuiltinFuncVal("rpartition", rpartitionFn))
		t.Dict.Set(InternString("removeprefix"), NewBuiltinFuncVal("removeprefix", removeprefixFn))
		t.Dict.Set(InternString("removesuffix"), NewBuiltinFuncVal("removesuffix", removesuffixFn))
		t.Dict.Set(InternString("isascii"), NewBuiltinFuncVal("isascii", isasciiFn))
		t.Dict.Set(InternString("startswith"), NewBuiltinFuncVal("startswith", startswithFn))
		t.Dict.Set(InternString("endswith"), NewBuiltinFuncVal("endswith", endswithFn))
	}
}
