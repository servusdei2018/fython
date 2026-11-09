package vm

import (
	"encoding/base64"
	"encoding/hex"
	"hash/crc32"
	"strings"
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
			panic("TypeError: b2a_base64() takes at least 1 argument")
		}
		data, ok := getBytesFromValue(pa[0])
		if !ok {
			panic("TypeError: a bytes-like object is required")
		}
		newline := true
		if v, ok := kwArg(args, kwnames, "newline"); ok {
			newline = v.IsTruthy()
		}
		padded := true
		if v, ok := kwArg(args, kwnames, "padded"); ok {
			padded = v.IsTruthy()
		}
		wrapcol := 0
		if v, ok := kwArg(args, kwnames, "wrapcol"); ok && v.IsInt64() {
			wrapcol = int(v.AsInt64())
		}
		enc := base64.StdEncoding
		if v, ok := kwArg(args, kwnames, "alphabet"); ok && !v.IsNone() {
			if b, ok := getBytesFromValue(v); ok && len(b) == 64 {
				enc = base64.NewEncoding(string(b))
			}
		}
		if !padded {
			enc = enc.WithPadding(base64.NoPadding)
		}
		dst := make([]byte, enc.EncodedLen(len(data)))
		enc.Encode(dst, data)
		if wrapcol > 0 && len(dst) > wrapcol {
			var wrapped []byte
			for len(dst) > wrapcol {
				wrapped = append(wrapped, dst[:wrapcol]...)
				wrapped = append(wrapped, '\n')
				dst = dst[wrapcol:]
			}
			wrapped = append(wrapped, dst...)
			dst = wrapped
		}
		if newline {
			dst = append(dst, '\n')
		}
		return val.NewBytesVal(dst)
	})

	// a2b_base64(data, *, strict_mode=False, ignorechars=b'', padded=True, alphabet=None)
	set("a2b_base64", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			panic("TypeError: a2b_base64() takes at least 1 argument")
		}
		data, ok := getBytesFromValue(pa[0])
		if !ok {
			panic("TypeError: a bytes-like object is required")
		}
		padded := true
		if v, ok := kwArg(args, kwnames, "padded"); ok {
			padded = v.IsTruthy()
		}
		enc := base64.StdEncoding
		if v, ok := kwArg(args, kwnames, "alphabet"); ok && !v.IsNone() {
			if b, ok := getBytesFromValue(v); ok && len(b) == 64 {
				enc = base64.NewEncoding(string(b))
			}
		}
		if !padded {
			enc = enc.WithPadding(base64.NoPadding)
		}

		// Filter out ASCII whitespace and newlines if not strict
		clean := make([]byte, 0, len(data))
		for _, b := range data {
			if b == ' ' || b == '\t' || b == '\r' || b == '\n' {
				continue
			}
			clean = append(clean, b)
		}

		dst := make([]byte, enc.DecodedLen(len(clean)))
		n, err := enc.Decode(dst, clean)
		if err != nil {
			// If std failed, attempt URL encoding fallback
			if enc == base64.StdEncoding {
				uenc := base64.URLEncoding
				if !padded {
					uenc = uenc.WithPadding(base64.NoPadding)
				}
				if n2, err2 := uenc.Decode(dst, clean); err2 == nil {
					return val.NewBytesVal(dst[:n2])
				}
			}
			exc := val.NewException(binasciiErrorType, "Incorrect padding or invalid base64 characters")
			panic(exc)
		}
		return val.NewBytesVal(dst[:n])
	})

	// hexlify / b2a_hex
	hexlifyFn := func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			panic("TypeError: hexlify() takes at least 1 argument")
		}
		data, ok := getBytesFromValue(pa[0])
		if !ok {
			panic("TypeError: a bytes-like object is required")
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
			panic("TypeError: unhexlify() takes at least 1 argument")
		}
		data, ok := getBytesFromValue(pa[0])
		if !ok {
			panic("TypeError: a bytes-like object is required")
		}
		s := strings.TrimSpace(string(data))
		if len(s)%2 != 0 {
			exc := val.NewException(binasciiErrorType, "Odd-length string")
			panic(exc)
		}
		dst, err := hex.DecodeString(s)
		if err != nil {
			exc := val.NewException(binasciiErrorType, "Non-hexadecimal digit found")
			panic(exc)
		}
		return val.NewBytesVal(dst)
	}
	set("unhexlify", unhexlifyFn)
	set("a2b_hex", unhexlifyFn)

	// crc32(data, value=0)
	set("crc32", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			panic("TypeError: crc32() takes at least 1 argument")
		}
		data, ok := getBytesFromValue(pa[0])
		if !ok {
			panic("TypeError: a bytes-like object is required")
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
