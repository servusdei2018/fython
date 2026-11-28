package vm

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"hash/crc32"
	"sync"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	binasciiOnce      sync.Once
	binasciiErrorType *val.PyType
	binasciiIncomp    *val.PyType

	base64StdAlphabet = []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/")
	base64UrlAlphabet = []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_")
	base32StdAlphabet = []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567")
	base32HexAlphabet = []byte("0123456789ABCDEFGHIJKLMNOPQRSTUV")
)

func initBinasciiTypes() {
	binasciiOnce.Do(func() {
		binasciiErrorType = val.MustNewType("Error", []*val.PyType{val.ValueErrorType}, nil)
		binasciiErrorType.Dict.Set(val.InternString("__module__"), val.InternString("binascii"))
		binasciiIncomp = val.MustNewType("Incomplete", []*val.PyType{binasciiErrorType}, nil)
		binasciiIncomp.Dict.Set(val.InternString("__module__"), val.InternString("binascii"))
	})
}

func getBytesFromValue(v val.Value) ([]byte, bool) {
	if v.IsBytes() {
		return v.AsBytes().Data, true
	}
	if v.IsByteArray() {
		return v.AsByteArray().Data, true
	}
	if v.IsAnyString() {
		return []byte(v.AsString()), true
	}
	return nil, false
}

func (e *Engine) createBinasciiModule() val.Value {
	initBinasciiTypes()
	modVal := val.NewModuleVal("binascii", "Accelerated binascii module")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("Error"), val.TypeVal(binasciiErrorType))
	mod.Dict.Set(val.InternString("Incomplete"), val.TypeVal(binasciiIncomp))
	mod.Dict.Set(val.InternString("BASE64_ALPHABET"), val.NewBytesVal(base64StdAlphabet))
	mod.Dict.Set(val.InternString("URLSAFE_BASE64_ALPHABET"), val.NewBytesVal(base64UrlAlphabet))
	mod.Dict.Set(val.InternString("BASE32_ALPHABET"), val.NewBytesVal(base32StdAlphabet))
	mod.Dict.Set(val.InternString("BASE32HEX_ALPHABET"), val.NewBytesVal(base32HexAlphabet))

	set := func(name string, fn val.VectorcallFunc) {
		mod.Dict.Set(val.InternString(name), val.NewModuleFuncVal(name, fn))
	}

	// b2a_base64(data, *, newline=True, padded=True, wrapcol=0, alphabet=None)
	set("b2a_base64", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			ctx.Raise(val.NewException(val.TypeErrorType, "b2a_base64() takes at least 1 argument"))
			return val.NoneVal()
		}
		data, ok := getBytesFromValue(pa[0])
		if !ok {
			ctx.Raise(val.NewException(val.TypeErrorType, "a bytes-like object is required"))
			return val.NoneVal()
		}
		newline := true
		padded := true
		wrapcol := 0
		enc := base64.StdEncoding
		if len(kwnames) > 0 {
			kwStart := len(args) - len(kwnames)
			for i, kn := range kwnames {
				v := args[kwStart+i]
				switch {
				case val.StringEqualsASCII(kn, "newline"):
					newline = v.IsTruthy()
				case val.StringEqualsASCII(kn, "padded"):
					padded = v.IsTruthy()
				case val.StringEqualsASCII(kn, "wrapcol"):
					if v.IsInt64() {
						wrapcol = int(v.AsInt64())
					}
				case val.StringEqualsASCII(kn, "alphabet"):
					if !v.IsNone() {
						if b, ok := getBytesFromValue(v); ok && len(b) == 64 {
							enc = base64.NewEncoding(string(b))
						}
					}
				}
			}
		}
		if !padded {
			enc = enc.WithPadding(base64.NoPadding)
		}
		encLen := enc.EncodedLen(len(data))
		if wrapcol > 0 && encLen > wrapcol {
			raw := make([]byte, encLen)
			enc.Encode(raw, data)
			extraNL := (encLen - 1) / wrapcol
			total := encLen + extraNL
			if newline {
				total++
			}
			wrapped := make([]byte, 0, total)
			for len(raw) > wrapcol {
				wrapped = append(wrapped, raw[:wrapcol]...)
				wrapped = append(wrapped, '\n')
				raw = raw[wrapcol:]
			}
			wrapped = append(wrapped, raw...)
			if newline {
				wrapped = append(wrapped, '\n')
			}
			return val.NewBytesVal(wrapped)
		}
		if newline {
			dst := make([]byte, encLen+1)
			enc.Encode(dst[:encLen], data)
			dst[encLen] = '\n'
			return val.NewBytesVal(dst)
		}
		dst := make([]byte, encLen)
		enc.Encode(dst, data)
		return val.NewBytesVal(dst)
	})

	// a2b_base64(data, *, strict_mode=False, ignorechars=b'', padded=True, alphabet=None)
	set("a2b_base64", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			ctx.Raise(val.NewException(val.TypeErrorType, "a2b_base64() takes at least 1 argument"))
			return val.NoneVal()
		}
		data, ok := getBytesFromValue(pa[0])
		if !ok {
			ctx.Raise(val.NewException(val.TypeErrorType, "a bytes-like object is required"))
			return val.NoneVal()
		}
		strictMode := false
		padded := true
		var ignoreChars []byte
		alphabet := base64StdAlphabet
		enc := base64.StdEncoding
		if len(kwnames) > 0 {
			kwStart := len(args) - len(kwnames)
			for i, kn := range kwnames {
				v := args[kwStart+i]
				switch {
				case val.StringEqualsASCII(kn, "strict_mode"):
					strictMode = v.IsTruthy()
				case val.StringEqualsASCII(kn, "padded"):
					padded = v.IsTruthy()
				case val.StringEqualsASCII(kn, "ignorechars"):
					if b, ok := getBytesFromValue(v); ok {
						ignoreChars = b
					}
				case val.StringEqualsASCII(kn, "alphabet"):
					if !v.IsNone() {
						if b, ok := getBytesFromValue(v); ok && len(b) == 64 {
							alphabet = b
							enc = base64.NewEncoding(string(b))
						}
					}
				}
			}
		}
		if !padded {
			enc = enc.WithPadding(base64.NoPadding)
		}

		// Zero-copy fast path: Go's base64.Encoding.Decode natively ignores \r and \n.
		// In strict_mode=True, reject \r/\n unless explicitly in ignorechars.
		if len(ignoreChars) == 0 && (!strictMode || (bytes.IndexByte(data, '\n') < 0 && bytes.IndexByte(data, '\r') < 0)) {
			dst := make([]byte, enc.DecodedLen(len(data)))
			n, err := enc.Decode(dst, data)
			if err == nil {
				return val.NewBytesVal(dst[:n])
			}
			if strictMode {
				ctx.Raise(val.NewException(binasciiErrorType, "Incorrect padding or invalid base64 characters"))
				return val.NoneVal()
			}
		}

		// Fallback path: filter ignored/non-alphabet bytes according to CPython semantics.
		var clean []byte
		if strictMode {
			var ignoreMap [256]bool
			for _, b := range ignoreChars {
				ignoreMap[b] = true
			}
			clean = make([]byte, 0, len(data))
			for _, b := range data {
				if ignoreMap[b] {
					continue
				}
				if b == '\r' || b == '\n' {
					ctx.Raise(val.NewException(binasciiErrorType, "Only base64 data is allowed"))
					return val.NoneVal()
				}
				clean = append(clean, b)
			}
		} else {
			var validMap [256]bool
			for _, b := range alphabet {
				validMap[b] = true
			}
			clean = make([]byte, 0, len(data))
			padSeen := 0
			for _, b := range data {
				if validMap[b] {
					if padSeen > 0 {
						// In CPython strict_mode=False, trailing data after valid '=' padding is ignored.
						break
					}
					clean = append(clean, b)
				} else if b == '=' && padded {
					if len(clean)%4 == 0 {
						break
					}
					clean = append(clean, b)
					padSeen++
					if len(clean)%4 == 0 {
						break
					}
				}
			}
		}

		dst := make([]byte, enc.DecodedLen(len(clean)))
		n, err := enc.Decode(dst, clean)
		if err != nil {
			if !strictMode && enc == base64.StdEncoding {
				uenc := base64.URLEncoding
				if !padded {
					uenc = uenc.WithPadding(base64.NoPadding)
				}
				// Re-filter against URL alphabet if std failed.
				uClean := make([]byte, 0, len(data))
				for _, b := range data {
					if b == ' ' || b == '\t' || b == '\r' || b == '\n' {
						continue
					}
					uClean = append(uClean, b)
				}
				uDst := make([]byte, uenc.DecodedLen(len(uClean)))
				if n2, err2 := uenc.Decode(uDst, uClean); err2 == nil {
					return val.NewBytesVal(uDst[:n2])
				}
			}
			ctx.Raise(val.NewException(binasciiErrorType, "Incorrect padding or invalid base64 characters"))
			return val.NoneVal()
		}
		return val.NewBytesVal(dst[:n])
	})

	// hexlify / b2a_hex
	hexlifyFn := func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			ctx.Raise(val.NewException(val.TypeErrorType, "hexlify() takes at least 1 argument"))
			return val.NoneVal()
		}
		data, ok := getBytesFromValue(pa[0])
		if !ok {
			ctx.Raise(val.NewException(val.TypeErrorType, "a bytes-like object is required"))
			return val.NoneVal()
		}
		encoded := make([]byte, hex.EncodedLen(len(data)))
		hex.Encode(encoded, data)
		return val.NewBytesVal(encoded)
	}
	set("hexlify", hexlifyFn)
	set("b2a_hex", hexlifyFn)

	// unhexlify / a2b_hex
	unhexlifyFn := func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			ctx.Raise(val.NewException(val.TypeErrorType, "unhexlify() takes at least 1 argument"))
			return val.NoneVal()
		}
		data, ok := getBytesFromValue(pa[0])
		if !ok {
			ctx.Raise(val.NewException(val.TypeErrorType, "a bytes-like object is required"))
			return val.NoneVal()
		}
		trimmed := bytes.TrimSpace(data)
		if len(trimmed)%2 != 0 {
			ctx.Raise(val.NewException(binasciiErrorType, "Odd-length string"))
			return val.NoneVal()
		}
		dst := make([]byte, hex.DecodedLen(len(trimmed)))
		n, err := hex.Decode(dst, trimmed)
		if err != nil {
			ctx.Raise(val.NewException(binasciiErrorType, "Non-hexadecimal digit found"))
			return val.NoneVal()
		}
		return val.NewBytesVal(dst[:n])
	}
	set("unhexlify", unhexlifyFn)
	set("a2b_hex", unhexlifyFn)

	// crc32(data, value=0)
	set("crc32", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			ctx.Raise(val.NewException(val.TypeErrorType, "crc32() takes at least 1 argument"))
			return val.NoneVal()
		}
		data, ok := getBytesFromValue(pa[0])
		if !ok {
			ctx.Raise(val.NewException(val.TypeErrorType, "a bytes-like object is required"))
			return val.NoneVal()
		}
		var crc uint32 = 0
		if len(pa) > 1 && pa[1].IsInt64() {
			crc = uint32(pa[1].AsInt64())
		}
		if crc == 0 {
			crc = crc32.ChecksumIEEE(data)
		} else {
			crc = crc32.Update(crc, crc32.IEEETable, data)
		}
		return val.Int64Val(int64(crc))
	})

	return modVal
}
