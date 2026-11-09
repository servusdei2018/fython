package vm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"sync"
	"unicode"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	structOnce      sync.Once
	structType      *val.PyType
	structErrorType *val.PyType
)

type structOpCode rune

type structField struct {
	op    structOpCode
	count int // string length for 's', repeat count for primitives
	size  int // size of single element (or total size for 's'/'x')
}

type parsedFormat struct {
	fmtStr   string
	order    binary.ByteOrder
	isNative bool
	fields   []structField
	size     int
	numArgs  int
}

var (
	structCacheMu sync.RWMutex
	structCache   = make(map[string]*parsedFormat)
)

func getFormatString(v val.Value) (string, error) {
	if v.IsAnyString() {
		return v.AsString(), nil
	}
	if v.IsBytes() {
		return string(v.AsBytes().Data), nil
	}
	if v.IsByteArray() {
		return string(v.AsByteArray().Data), nil
	}
	return "", errors.New("struct format requires str or bytes")
}

func parseStructFormat(fmtStr string) (*parsedFormat, error) {
	structCacheMu.RLock()
	if cached, ok := structCache[fmtStr]; ok {
		structCacheMu.RUnlock()
		return cached, nil
	}
	structCacheMu.RUnlock()

	var order binary.ByteOrder = binary.LittleEndian
	isNative := true
	runes := []rune(fmtStr)
	idx := 0

	if len(runes) > 0 {
		switch runes[0] {
		case '@':
			isNative = true
			order = binary.LittleEndian
			idx++
		case '=':
			isNative = false
			order = binary.LittleEndian
			idx++
		case '<':
			isNative = false
			order = binary.LittleEndian
			idx++
		case '>', '!':
			isNative = false
			order = binary.BigEndian
			idx++
		}
	}

	var fields []structField
	totalSize := 0
	numArgs := 0

	for idx < len(runes) {
		r := runes[idx]
		if unicode.IsSpace(r) {
			idx++
			continue
		}

		repeat := 1
		hasRepeat := false
		if unicode.IsDigit(r) {
			hasRepeat = true
			start := idx
			for idx < len(runes) && unicode.IsDigit(runes[idx]) {
				idx++
			}
			repNum, err := strconv.Atoi(string(runes[start:idx]))
			if err != nil {
				return nil, errors.New("bad repeat count")
			}
			repeat = repNum
			if idx >= len(runes) {
				return nil, errors.New("repeat count without format character")
			}
			r = runes[idx]
		}
		idx++

		var elemSize int
		var consumesArg bool

		switch r {
		case 'x': // pad byte
			elemSize = 1
			consumesArg = false
		case 'c', 'b', 'B', '?':
			elemSize = 1
			consumesArg = true
		case 'h', 'H', 'e':
			elemSize = 2
			consumesArg = true
		case 'i', 'I', 'l', 'L':
			elemSize = 4
			consumesArg = true
		case 'q', 'Q', 'n', 'N', 'P':
			elemSize = 8
			consumesArg = true
		case 'f':
			elemSize = 4
			consumesArg = true
		case 'd':
			elemSize = 8
			consumesArg = true
		case 's', 'p': // bytes string
			elemSize = 1
			consumesArg = true
		default:
			return nil, fmt.Errorf("bad char in struct format: %c", r)
		}

		switch r {
		case 's', 'p':
			count := repeat
			if !hasRepeat {
				count = 1
			}
			fieldSize := count * elemSize
			fields = append(fields, structField{op: structOpCode(r), count: count, size: fieldSize})
			totalSize += fieldSize
			numArgs++
		case 'x':
			fieldSize := repeat * elemSize
			fields = append(fields, structField{op: structOpCode(r), count: repeat, size: fieldSize})
			totalSize += fieldSize
		default:
			fields = append(fields, structField{op: structOpCode(r), count: repeat, size: elemSize})
			totalSize += repeat * elemSize
			if consumesArg {
				numArgs += repeat
			}
		}
	}

	pf := &parsedFormat{
		fmtStr:   fmtStr,
		order:    order,
		isNative: isNative,
		fields:   fields,
		size:     totalSize,
		numArgs:  numArgs,
	}

	structCacheMu.Lock()
	structCache[fmtStr] = pf
	structCacheMu.Unlock()
	return pf, nil
}

func packValues(pf *parsedFormat, args []val.Value, buf []byte) error {
	if len(args) != pf.numArgs {
		return fmt.Errorf("pack expected %d items for packing (got %d)", pf.numArgs, len(args))
	}
	if len(buf) < pf.size {
		return fmt.Errorf("buffer too small for packing: got %d bytes, need %d", len(buf), pf.size)
	}

	argIdx := 0
	offset := 0

	for _, field := range pf.fields {
		if field.op == 'x' {
			for i := range field.count {
				buf[offset+i] = 0
			}
			offset += field.size
			continue
		}

		if field.op == 's' {
			v := args[argIdx]
			argIdx++
			b, ok := getBytesFromValue(v)
			if !ok {
				return errors.New("struct: required bytes-like object for 's' format")
			}
			copyLen := min(len(b), field.count)
			copy(buf[offset:offset+copyLen], b[:copyLen])
			for i := copyLen; i < field.count; i++ {
				buf[offset+i] = 0
			}
			offset += field.count
			continue
		}

		if field.op == 'p' {
			v := args[argIdx]
			argIdx++
			b, ok := getBytesFromValue(v)
			if !ok {
				return errors.New("struct: required bytes-like object for 'p' format")
			}
			maxStr := max(field.count-1, 0)
			pLen := min(len(b), maxStr)
			pLen = min(pLen, 255)
			buf[offset] = byte(pLen)
			copy(buf[offset+1:offset+1+pLen], b[:pLen])
			for i := 1 + pLen; i < field.count; i++ {
				buf[offset+i] = 0
			}
			offset += field.count
			continue
		}

		for range field.count {
			v := args[argIdx]
			argIdx++

			switch field.op {
			case 'c':
				b, ok := getBytesFromValue(v)
				if !ok || len(b) != 1 {
					return errors.New("struct: char format requires a bytes object of length 1")
				}
				buf[offset] = b[0]
			case '?':
				if v.IsTruthy() {
					buf[offset] = 1
				} else {
					buf[offset] = 0
				}
			case 'b':
				num := v.AsInt64()
				if num < -128 || num > 127 {
					return errors.New("byte format requires -128 <= number <= 127")
				}
				buf[offset] = byte(int8(num))
			case 'B':
				num := v.AsInt64()
				if num < 0 || num > 255 {
					return errors.New("ubyte format requires 0 <= number <= 255")
				}
				buf[offset] = byte(num)
			case 'h':
				num := v.AsInt64()
				if num < -32768 || num > 32767 {
					return errors.New("short format requires -32768 <= number <= 32767")
				}
				pf.order.PutUint16(buf[offset:], uint16(int16(num)))
			case 'H':
				num := v.AsInt64()
				if num < 0 || num > 65535 {
					return errors.New("ushort format requires 0 <= number <= 65535")
				}
				pf.order.PutUint16(buf[offset:], uint16(num))
			case 'i', 'l':
				num := v.AsInt64()
				if num < -2147483648 || num > 2147483647 {
					return errors.New("int format requires -2147483648 <= number <= 2147483647")
				}
				pf.order.PutUint32(buf[offset:], uint32(int32(num)))
			case 'I', 'L':
				num := v.AsInt64()
				if num < 0 || num > 4294967295 {
					return errors.New("uint format requires 0 <= number <= 4294967295")
				}
				pf.order.PutUint32(buf[offset:], uint32(num))
			case 'q', 'n':
				num := v.AsInt64()
				pf.order.PutUint64(buf[offset:], uint64(num))
			case 'Q', 'N', 'P':
				var u uint64
				switch {
				case v.IsInt64():
					u = uint64(v.AsInt64())
				case v.Kind() == val.KindBigInt:
					u = v.AsBigInt().Val.Uint64()
				default:
					return errors.New("required integer for Q format")
				}
				pf.order.PutUint64(buf[offset:], u)
			case 'f':
				var fl float64
				switch {
				case v.IsFloat64():
					fl = v.AsFloat64()
				case v.IsInt64():
					fl = float64(v.AsInt64())
				default:
					return errors.New("required float for f format")
				}
				pf.order.PutUint32(buf[offset:], math.Float32bits(float32(fl)))
			case 'd':
				var fl float64
				switch {
				case v.IsFloat64():
					fl = v.AsFloat64()
				case v.IsInt64():
					fl = float64(v.AsInt64())
				default:
					return errors.New("required float for d format")
				}
				pf.order.PutUint64(buf[offset:], math.Float64bits(fl))
			}
			offset += field.size
		}
	}
	return nil
}

func unpackValues(pf *parsedFormat, buf []byte) ([]val.Value, error) {
	if len(buf) < pf.size {
		return nil, fmt.Errorf("unpack requires a buffer of at least %d bytes", pf.size)
	}

	results := make([]val.Value, 0, pf.numArgs)
	offset := 0

	for _, field := range pf.fields {
		if field.op == 'x' {
			offset += field.size
			continue
		}

		if field.op == 's' {
			b := make([]byte, field.count)
			copy(b, buf[offset:offset+field.count])
			results = append(results, val.NewBytesVal(b))
			offset += field.count
			continue
		}

		if field.op == 'p' {
			pLen := int(buf[offset])
			maxStr := field.count - 1
			if pLen > maxStr {
				pLen = maxStr
			}
			b := make([]byte, pLen)
			copy(b, buf[offset+1:offset+1+pLen])
			results = append(results, val.NewBytesVal(b))
			offset += field.count
			continue
		}

		for range field.count {
			switch field.op {
			case 'c':
				results = append(results, val.NewBytesVal([]byte{buf[offset]}))
			case '?':
				results = append(results, val.BoolVal(buf[offset] != 0))
			case 'b':
				results = append(results, val.Int64Val(int64(int8(buf[offset]))))
			case 'B':
				results = append(results, val.Int64Val(int64(buf[offset])))
			case 'h':
				u := pf.order.Uint16(buf[offset:])
				results = append(results, val.Int64Val(int64(int16(u))))
			case 'H':
				u := pf.order.Uint16(buf[offset:])
				results = append(results, val.Int64Val(int64(u)))
			case 'i', 'l':
				u := pf.order.Uint32(buf[offset:])
				results = append(results, val.Int64Val(int64(int32(u))))
			case 'I', 'L':
				u := pf.order.Uint32(buf[offset:])
				results = append(results, val.Int64Val(int64(u)))
			case 'q', 'n':
				u := pf.order.Uint64(buf[offset:])
				results = append(results, val.Int64Val(int64(u)))
			case 'Q', 'N', 'P':
				u := pf.order.Uint64(buf[offset:])
				if u <= math.MaxInt64 {
					results = append(results, val.Int64Val(int64(u)))
				} else {
					results = append(results, val.NewBigIntVal(new(big.Int).SetUint64(u)))
				}
			case 'f':
				bits := pf.order.Uint32(buf[offset:])
				results = append(results, val.Float64Val(float64(math.Float32frombits(bits))))
			case 'd':
				bits := pf.order.Uint64(buf[offset:])
				results = append(results, val.Float64Val(math.Float64frombits(bits)))
			}
			offset += field.size
		}
	}
	return results, nil
}

type pyStruct struct {
	inst   val.PyInstance
	format string
	pf     *parsedFormat
}

func initStructTypes() {
	structOnce.Do(func() {
		structErrorType = val.MustNewType("error", []*val.PyType{val.ExceptionType}, nil)
		structErrorType.Dict.Set(val.InternString("__module__"), val.InternString("_struct"))

		structType = val.MustNewType("Struct", []*val.PyType{val.ObjectType}, nil)
		structType.Dict.Set(val.InternString("__module__"), val.InternString("_struct"))

		structType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			pa := posArgs(args, kwnames)
			if len(pa) < 1 {
				exc := val.NewException(structErrorType, "Struct() takes at least 1 argument")
				panic(exc)
			}
			fmtStr, err := getFormatString(pa[0])
			if err != nil {
				exc := val.NewException(structErrorType, err.Error())
				panic(exc)
			}
			pf, err := parseStructFormat(fmtStr)
			if err != nil {
				exc := val.NewException(structErrorType, err.Error())
				panic(exc)
			}
			ps := &pyStruct{
				inst: val.PyInstance{
					Header: val.ObjectHeader{Kind: val.KindInstance, Type: structType},
					Shape:  structType.RootShape,
				},
				format: fmtStr,
				pf:     pf,
			}
			return val.ObjectVal(&ps.inst.Header)
		}

		structType.Slots.TpGetAttr = func(ctx *val.ExecContext, obj, name val.Value) val.Value {
			ps := (*pyStruct)(val.AsPointer(obj))
			switch name.AsString() {
			case "format":
				return val.NewStringVal(ps.format)
			case "size":
				return val.Int64Val(int64(ps.pf.size))
			}
			return val.ObjectGetAttr(ctx, obj, name)
		}

		structType.Dict.Set(val.InternString("pack"), val.NewBuiltinFuncVal("pack", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: pack() missing self")
			}
			ps := (*pyStruct)(val.AsPointer(args[0]))
			buf := make([]byte, ps.pf.size)
			if err := packValues(ps.pf, args[1:], buf); err != nil {
				exc := val.NewException(structErrorType, err.Error())
				panic(exc)
			}
			return val.NewBytesVal(buf)
		}))

		structType.Dict.Set(val.InternString("pack_into"), val.NewBuiltinFuncVal("pack_into", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 3 {
				panic("TypeError: pack_into() takes at least 2 arguments")
			}
			ps := (*pyStruct)(val.AsPointer(args[0]))
			b, ok := getBytesFromValue(args[1])
			if !ok {
				panic("TypeError: pack_into() requires a buffer")
			}
			offset := int(args[2].AsInt64())
			if offset < 0 || offset+ps.pf.size > len(b) {
				exc := val.NewException(structErrorType, "pack_into: buffer too small")
				panic(exc)
			}
			if err := packValues(ps.pf, args[3:], b[offset:]); err != nil {
				exc := val.NewException(structErrorType, err.Error())
				panic(exc)
			}
			return val.NoneVal()
		}))

		structType.Dict.Set(val.InternString("unpack"), val.NewBuiltinFuncVal("unpack", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: unpack() missing buffer")
			}
			ps := (*pyStruct)(val.AsPointer(args[0]))
			b, ok := getBytesFromValue(args[1])
			if !ok {
				panic("TypeError: unpack() requires bytes-like object")
			}
			vals, err := unpackValues(ps.pf, b)
			if err != nil {
				exc := val.NewException(structErrorType, err.Error())
				panic(exc)
			}
			return val.NewTupleVal(vals...)
		}))

		structType.Dict.Set(val.InternString("unpack_from"), val.NewBuiltinFuncVal("unpack_from", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: unpack_from() missing buffer")
			}
			ps := (*pyStruct)(val.AsPointer(args[0]))
			b, ok := getBytesFromValue(args[1])
			if !ok {
				panic("TypeError: unpack_from() requires bytes-like object")
			}
			offset := 0
			if len(args) > 2 {
				offset = int(args[2].AsInt64())
			}
			if offset < 0 || offset > len(b) {
				exc := val.NewException(structErrorType, "unpack_from requires buffer of at least required size")
				panic(exc)
			}
			vals, err := unpackValues(ps.pf, b[offset:])
			if err != nil {
				exc := val.NewException(structErrorType, err.Error())
				panic(exc)
			}
			return val.NewTupleVal(vals...)
		}))

		structType.Dict.Set(val.InternString("iter_unpack"), val.NewBuiltinFuncVal("iter_unpack", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: iter_unpack() missing buffer")
			}
			ps := (*pyStruct)(val.AsPointer(args[0]))
			b, ok := getBytesFromValue(args[1])
			if !ok {
				panic("TypeError: iter_unpack() requires bytes-like object")
			}
			if ps.pf.size == 0 {
				exc := val.NewException(structErrorType, "cannot iter_unpack with zero size")
				panic(exc)
			}
			if len(b)%ps.pf.size != 0 {
				exc := val.NewException(structErrorType, "iter_unpack requires a buffer of a multiple of size")
				panic(exc)
			}
			curOffset := 0
			return val.NewIterValWithNext(func() (val.Value, bool) {
				if curOffset >= len(b) {
					return val.NoneVal(), false
				}
				vals, _ := unpackValues(ps.pf, b[curOffset:])
				curOffset += ps.pf.size
				return val.NewTupleVal(vals...), true
			})
		}))
	})
}

func (e *Engine) createStructModule() val.Value {
	initStructTypes()
	modVal := val.NewModuleVal("_struct", "Accelerated binary packing/unpacking module")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("Struct"), val.TypeVal(structType))
	mod.Dict.Set(val.InternString("error"), val.TypeVal(structErrorType))
	mod.Dict.Set(val.InternString("_clearcache"), val.NewModuleFuncVal("_clearcache", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		structCacheMu.Lock()
		structCache = make(map[string]*parsedFormat)
		structCacheMu.Unlock()
		return val.NoneVal()
	}))

	set := func(name string, fn val.VectorcallFunc) {
		mod.Dict.Set(val.InternString(name), val.NewModuleFuncVal(name, fn))
	}

	// calcsize(format)
	set("calcsize", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			exc := val.NewException(structErrorType, "calcsize() takes at least 1 argument")
			panic(exc)
		}
		fmtStr, err := getFormatString(pa[0])
		if err != nil {
			exc := val.NewException(structErrorType, err.Error())
			panic(exc)
		}
		pf, err := parseStructFormat(fmtStr)
		if err != nil {
			exc := val.NewException(structErrorType, err.Error())
			panic(exc)
		}
		return val.Int64Val(int64(pf.size))
	})

	// pack(format, *v)
	set("pack", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			exc := val.NewException(structErrorType, "pack() takes at least 1 argument")
			panic(exc)
		}
		fmtStr, err := getFormatString(pa[0])
		if err != nil {
			exc := val.NewException(structErrorType, err.Error())
			panic(exc)
		}
		pf, err := parseStructFormat(fmtStr)
		if err != nil {
			exc := val.NewException(structErrorType, err.Error())
			panic(exc)
		}
		buf := make([]byte, pf.size)
		if err := packValues(pf, pa[1:], buf); err != nil {
			exc := val.NewException(structErrorType, err.Error())
			panic(exc)
		}
		return val.NewBytesVal(buf)
	})

	// pack_into(format, buffer, offset, *v)
	set("pack_into", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 3 {
			exc := val.NewException(structErrorType, "pack_into() takes at least 3 arguments")
			panic(exc)
		}
		fmtStr, err := getFormatString(pa[0])
		if err != nil {
			exc := val.NewException(structErrorType, err.Error())
			panic(exc)
		}
		pf, err := parseStructFormat(fmtStr)
		if err != nil {
			exc := val.NewException(structErrorType, err.Error())
			panic(exc)
		}
		b, ok := getBytesFromValue(pa[1])
		if !ok {
			panic("TypeError: pack_into() requires a buffer")
		}
		offset := int(pa[2].AsInt64())
		if offset < 0 || offset+pf.size > len(b) {
			exc := val.NewException(structErrorType, "pack_into: buffer too small")
			panic(exc)
		}
		if err := packValues(pf, pa[3:], b[offset:]); err != nil {
			exc := val.NewException(structErrorType, err.Error())
			panic(exc)
		}
		return val.NoneVal()
	})

	// unpack(format, buffer)
	set("unpack", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 2 {
			exc := val.NewException(structErrorType, "unpack() takes at least 2 arguments")
			panic(exc)
		}
		fmtStr, err := getFormatString(pa[0])
		if err != nil {
			exc := val.NewException(structErrorType, err.Error())
			panic(exc)
		}
		pf, err := parseStructFormat(fmtStr)
		if err != nil {
			exc := val.NewException(structErrorType, err.Error())
			panic(exc)
		}
		b, ok := getBytesFromValue(pa[1])
		if !ok {
			panic("TypeError: unpack() requires bytes-like object")
		}
		vals, err := unpackValues(pf, b)
		if err != nil {
			exc := val.NewException(structErrorType, err.Error())
			panic(exc)
		}
		return val.NewTupleVal(vals...)
	})

	// unpack_from(format, buffer, offset=0)
	set("unpack_from", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 2 {
			exc := val.NewException(structErrorType, "unpack_from() takes at least 2 arguments")
			panic(exc)
		}
		fmtStr, err := getFormatString(pa[0])
		if err != nil {
			exc := val.NewException(structErrorType, err.Error())
			panic(exc)
		}
		pf, err := parseStructFormat(fmtStr)
		if err != nil {
			exc := val.NewException(structErrorType, err.Error())
			panic(exc)
		}
		b, ok := getBytesFromValue(pa[1])
		if !ok {
			panic("TypeError: unpack_from() requires bytes-like object")
		}
		offset := 0
		if len(pa) > 2 {
			offset = int(pa[2].AsInt64())
		}
		if offset < 0 || offset > len(b) {
			exc := val.NewException(structErrorType, "unpack_from requires buffer of at least required size")
			panic(exc)
		}
		vals, err := unpackValues(pf, b[offset:])
		if err != nil {
			exc := val.NewException(structErrorType, err.Error())
			panic(exc)
		}
		return val.NewTupleVal(vals...)
	})

	// iter_unpack(format, buffer)
	set("iter_unpack", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 2 {
			exc := val.NewException(structErrorType, "iter_unpack() takes at least 2 arguments")
			panic(exc)
		}
		fmtStr, err := getFormatString(pa[0])
		if err != nil {
			exc := val.NewException(structErrorType, err.Error())
			panic(exc)
		}
		pf, err := parseStructFormat(fmtStr)
		if err != nil {
			exc := val.NewException(structErrorType, err.Error())
			panic(exc)
		}
		b, ok := getBytesFromValue(pa[1])
		if !ok {
			panic("TypeError: iter_unpack() requires bytes-like object")
		}
		if pf.size == 0 {
			exc := val.NewException(structErrorType, "cannot iter_unpack with zero size")
			panic(exc)
		}
		if len(b)%pf.size != 0 {
			exc := val.NewException(structErrorType, "iter_unpack requires a buffer of a multiple of size")
			panic(exc)
		}
		curOffset := 0
		return val.NewIterValWithNext(func() (val.Value, bool) {
			if curOffset >= len(b) {
				return val.NoneVal(), false
			}
			vals, _ := unpackValues(pf, b[curOffset:])
			curOffset += pf.size
			return val.NewTupleVal(vals...), true
		})
	})

	return modVal
}
