package vm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"sync"

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
	fmtStr    string
	order     binary.ByteOrder
	isNative  bool
	bigEndian bool
	fields    []structField
	size      int
	numArgs   int
}

var (
	structCacheMu sync.RWMutex
	structCache   = make(map[string]*parsedFormat)
	// Fast identity cache for repeated same format object (common in loops).
	// Same immutable Value implies same format content, so pointer/inline
	// equality is sufficient without converting to Go string.
	structFastMu    sync.RWMutex
	structFastVal   val.Value
	structFastPF    *parsedFormat
	structFastValid bool
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
	// Format strings are ASCII; iterate bytes to avoid []rune allocation.
	// Non-ASCII bytes are rejected as bad chars (parity with CPython).
	idx := 0
	n := len(fmtStr)

	if n > 0 {
		switch fmtStr[0] {
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

	for idx < n {
		r := fmtStr[idx]
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' {
			idx++
			continue
		}
		if r >= 0x80 {
			return nil, fmt.Errorf("bad char in struct format: %c", rune(r))
		}

		repeat := 1
		hasRepeat := false
		if r >= '0' && r <= '9' {
			hasRepeat = true
			start := idx
			for idx < n && fmtStr[idx] >= '0' && fmtStr[idx] <= '9' {
				idx++
			}
			repNum, err := strconv.Atoi(fmtStr[start:idx])
			if err != nil {
				return nil, errors.New("bad repeat count")
			}
			repeat = repNum
			if idx >= n {
				return nil, errors.New("repeat count without format character")
			}
			r = fmtStr[idx]
			if r >= 0x80 {
				return nil, fmt.Errorf("bad char in struct format: %c", rune(r))
			}
		}
		idx++

		var elemSize int
		var consumesArg bool

		// Complex types: Zf (2x float=8), Zd (2x double=16). Zg not supported by struct.
		if r == 'Z' {
			if idx >= n {
				return nil, fmt.Errorf("bad char in struct format: %c", r)
			}
			sub := fmtStr[idx]
			switch sub {
			case 'f':
				elemSize = 8
			case 'd':
				elemSize = 16
			default:
				return nil, fmt.Errorf("bad char in struct format: %c", r)
			}
			idx++ // consume sub char
			fields = append(fields, structField{op: structOpCode('z'), count: repeat, size: elemSize})
			totalSize += repeat * elemSize
			numArgs += repeat
			continue
		}

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
		case 'i', 'I':
			elemSize = 4
			consumesArg = true
		case 'l', 'L':
			// Standard mode ('<','>','=','!'): 4 bytes. Native mode ('@'): 8 bytes on LP64.
			if isNative {
				elemSize = 8
			} else {
				elemSize = 4
			}
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
		fmtStr:    fmtStr,
		order:     order,
		isNative:  isNative,
		bigEndian: order == binary.BigEndian,
		fields:    fields,
		size:      totalSize,
		numArgs:   numArgs,
	}

	structCacheMu.Lock()
	structCache[fmtStr] = pf
	structCacheMu.Unlock()
	return pf, nil
}

// parseStructFormatVal resolves a format Value to a compiled plan, using a
// fast identity cache for the common case of the same format object reused
// in a loop. Same immutable Value implies same content, so Value equality
// avoids Go string conversion and map hashing on hits.
// Only immutable str/bytes formats use the identity cache; mutable bytearray
// formats always revalidate since the same object may hold new content.
func parseStructFormatVal(v val.Value) (*parsedFormat, error) {
	cacheable := v.IsAnyString() || v.IsBytes()
	if cacheable {
		structFastMu.RLock()
		if structFastValid && structFastVal == v && structFastPF != nil {
			pf := structFastPF
			structFastMu.RUnlock()
			return pf, nil
		}
		structFastMu.RUnlock()
	}
	fmtStr, err := getFormatString(v)
	if err != nil {
		return nil, err
	}
	pf, err := parseStructFormat(fmtStr)
	if err != nil {
		return nil, err
	}
	if cacheable {
		structFastMu.Lock()
		structFastVal = v
		structFastPF = pf
		structFastValid = true
		structFastMu.Unlock()
	}
	return pf, nil
}

func putU16(pf *parsedFormat, buf []byte, v uint16) {
	if pf.bigEndian {
		binary.BigEndian.PutUint16(buf, v)
	} else {
		binary.LittleEndian.PutUint16(buf, v)
	}
}

func putU32(pf *parsedFormat, buf []byte, v uint32) {
	if pf.bigEndian {
		binary.BigEndian.PutUint32(buf, v)
	} else {
		binary.LittleEndian.PutUint32(buf, v)
	}
}

func putU64(pf *parsedFormat, buf []byte, v uint64) {
	if pf.bigEndian {
		binary.BigEndian.PutUint64(buf, v)
	} else {
		binary.LittleEndian.PutUint64(buf, v)
	}
}

func getU16(pf *parsedFormat, buf []byte) uint16 {
	if pf.bigEndian {
		return binary.BigEndian.Uint16(buf)
	}
	return binary.LittleEndian.Uint16(buf)
}

func getU32(pf *parsedFormat, buf []byte) uint32 {
	if pf.bigEndian {
		return binary.BigEndian.Uint32(buf)
	}
	return binary.LittleEndian.Uint32(buf)
}

func getU64(pf *parsedFormat, buf []byte) uint64 {
	if pf.bigEndian {
		return binary.BigEndian.Uint64(buf)
	}
	return binary.LittleEndian.Uint64(buf)
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
				putU16(pf, buf[offset:], uint16(int16(num)))
			case 'H':
				num := v.AsInt64()
				if num < 0 || num > 65535 {
					return errors.New("ushort format requires 0 <= number <= 65535")
				}
				putU16(pf, buf[offset:], uint16(num))
			case 'i', 'l':
				num := v.AsInt64()
				if num < -2147483648 || num > 2147483647 {
					return errors.New("int format requires -2147483648 <= number <= 2147483647")
				}
				putU32(pf, buf[offset:], uint32(int32(num)))
			case 'I', 'L':
				num := v.AsInt64()
				if num < 0 || num > 4294967295 {
					return errors.New("uint format requires 0 <= number <= 4294967295")
				}
				putU32(pf, buf[offset:], uint32(num))
			case 'q', 'n':
				num := v.AsInt64()
				putU64(pf, buf[offset:], uint64(num))
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
				putU64(pf, buf[offset:], u)
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
				putU32(pf, buf[offset:], math.Float32bits(float32(fl)))
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
				putU64(pf, buf[offset:], math.Float64bits(fl))
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
				u := getU16(pf, buf[offset:])
				results = append(results, val.Int64Val(int64(int16(u))))
			case 'H':
				u := getU16(pf, buf[offset:])
				results = append(results, val.Int64Val(int64(u)))
			case 'i', 'l':
				u := getU32(pf, buf[offset:])
				results = append(results, val.Int64Val(int64(int32(u))))
			case 'I', 'L':
				u := getU32(pf, buf[offset:])
				results = append(results, val.Int64Val(int64(u)))
			case 'q', 'n':
				u := getU64(pf, buf[offset:])
				results = append(results, val.Int64Val(int64(u)))
			case 'Q', 'N', 'P':
				u := getU64(pf, buf[offset:])
				if u <= math.MaxInt64 {
					results = append(results, val.Int64Val(int64(u)))
				} else {
					results = append(results, val.NewBigIntVal(new(big.Int).SetUint64(u)))
				}
			case 'f':
				bits := getU32(pf, buf[offset:])
				results = append(results, val.Float64Val(float64(math.Float32frombits(bits))))
			case 'd':
				bits := getU64(pf, buf[offset:])
				results = append(results, val.Float64Val(math.Float64frombits(bits)))
			}
			offset += field.size
		}
	}
	return results, nil
}

// unpackValuesInto decodes buf into out (len == pf.numArgs) avoiding the
// intermediate slice allocation in unpackValues. Returns error on short buffer.
func unpackValuesInto(pf *parsedFormat, buf []byte, out []val.Value) error {
	if len(buf) < pf.size {
		return fmt.Errorf("unpack requires a buffer of at least %d bytes", pf.size)
	}
	idx := 0
	offset := 0
	for _, field := range pf.fields {
		if field.op == 'x' {
			offset += field.size
			continue
		}
		if field.op == 's' {
			b := make([]byte, field.count)
			copy(b, buf[offset:offset+field.count])
			out[idx] = val.NewBytesVal(b)
			idx++
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
			out[idx] = val.NewBytesVal(b)
			idx++
			offset += field.count
			continue
		}
		for range field.count {
			switch field.op {
			case 'c':
				out[idx] = val.NewBytesVal([]byte{buf[offset]})
			case '?':
				out[idx] = val.BoolVal(buf[offset] != 0)
			case 'b':
				out[idx] = val.Int64Val(int64(int8(buf[offset])))
			case 'B':
				out[idx] = val.Int64Val(int64(buf[offset]))
			case 'h':
				u := getU16(pf, buf[offset:])
				out[idx] = val.Int64Val(int64(int16(u)))
			case 'H':
				u := getU16(pf, buf[offset:])
				out[idx] = val.Int64Val(int64(u))
			case 'i', 'l':
				u := getU32(pf, buf[offset:])
				out[idx] = val.Int64Val(int64(int32(u)))
			case 'I', 'L':
				u := getU32(pf, buf[offset:])
				out[idx] = val.Int64Val(int64(u))
			case 'q', 'n':
				u := getU64(pf, buf[offset:])
				out[idx] = val.Int64Val(int64(u))
			case 'Q', 'N', 'P':
				u := getU64(pf, buf[offset:])
				if u <= math.MaxInt64 {
					out[idx] = val.Int64Val(int64(u))
				} else {
					out[idx] = val.NewBigIntVal(new(big.Int).SetUint64(u))
				}
			case 'f':
				bits := getU32(pf, buf[offset:])
				out[idx] = val.Float64Val(float64(math.Float32frombits(bits)))
			case 'd':
				bits := getU64(pf, buf[offset:])
				out[idx] = val.Float64Val(math.Float64frombits(bits))
			}
			idx++
			offset += field.size
		}
	}
	return nil
}

// tupleFromUnpack decodes buf into a new Tuple Value with minimal allocation.
// For <=8 fields it decodes into a stack scratch array, saving the intermediate
// slice allocation in unpackValues.
func tupleFromUnpack(pf *parsedFormat, buf []byte) (val.Value, error) {
	if pf.numArgs <= 8 {
		var scratch [8]val.Value
		out := scratch[:pf.numArgs]
		if err := unpackValuesInto(pf, buf, out); err != nil {
			return val.NoneVal(), err
		}
		return val.NewTupleVal(out...), nil
	}
	vals, err := unpackValues(pf, buf)
	if err != nil {
		return val.NoneVal(), err
	}
	return val.NewTupleVal(vals...), nil
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
			pf, err := parseStructFormatVal(pa[0])
			if err != nil {
				exc := val.NewException(structErrorType, err.Error())
				panic(exc)
			}
			ps := &pyStruct{
				inst: val.PyInstance{
					Header: val.ObjectHeader{Kind: val.KindInstance, Type: structType},
					Shape:  structType.RootShape,
				},
				format: pf.fmtStr,
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
				ctx.Raise(val.NewException(val.TypeErrorType, "pack() missing self"))
				return val.NoneVal()
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
				ctx.Raise(val.NewException(val.TypeErrorType, "pack_into() takes at least 2 arguments"))
				return val.NoneVal()
			}
			ps := (*pyStruct)(val.AsPointer(args[0]))
			b, ok := getBytesFromValue(args[1])
			if !ok {
				ctx.Raise(val.NewException(val.TypeErrorType, "pack_into() requires a buffer"))
				return val.NoneVal()
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
				ctx.Raise(val.NewException(val.TypeErrorType, "unpack() missing buffer"))
				return val.NoneVal()
			}
			ps := (*pyStruct)(val.AsPointer(args[0]))
			b, ok := getBytesFromValue(args[1])
			if !ok {
				ctx.Raise(val.NewException(val.TypeErrorType, "unpack() requires bytes-like object"))
				return val.NoneVal()
			}
			tv, err := tupleFromUnpack(ps.pf, b)
			if err != nil {
				exc := val.NewException(structErrorType, err.Error())
				panic(exc)
			}
			return tv
		}))

		structType.Dict.Set(val.InternString("unpack_from"), val.NewBuiltinFuncVal("unpack_from", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				ctx.Raise(val.NewException(val.TypeErrorType, "unpack_from() missing buffer"))
				return val.NoneVal()
			}
			ps := (*pyStruct)(val.AsPointer(args[0]))
			b, ok := getBytesFromValue(args[1])
			if !ok {
				ctx.Raise(val.NewException(val.TypeErrorType, "unpack_from() requires bytes-like object"))
				return val.NoneVal()
			}
			offset := 0
			if len(args) > 2 {
				offset = int(args[2].AsInt64())
			}
			if offset < 0 || offset > len(b) {
				exc := val.NewException(structErrorType, "unpack_from requires buffer of at least required size")
				panic(exc)
			}
			tv, err := tupleFromUnpack(ps.pf, b[offset:])
			if err != nil {
				exc := val.NewException(structErrorType, err.Error())
				panic(exc)
			}
			return tv
		}))

		structType.Dict.Set(val.InternString("iter_unpack"), val.NewBuiltinFuncVal("iter_unpack", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				ctx.Raise(val.NewException(val.TypeErrorType, "iter_unpack() missing buffer"))
				return val.NoneVal()
			}
			ps := (*pyStruct)(val.AsPointer(args[0]))
			b, ok := getBytesFromValue(args[1])
			if !ok {
				ctx.Raise(val.NewException(val.TypeErrorType, "iter_unpack() requires bytes-like object"))
				return val.NoneVal()
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
				tv, _ := tupleFromUnpack(ps.pf, b[curOffset:])
				curOffset += ps.pf.size
				return tv, true
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
		structFastMu.Lock()
		structFastValid = false
		structFastPF = nil
		structFastMu.Unlock()
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
		pf, err := parseStructFormatVal(pa[0])
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
		pf, err := parseStructFormatVal(pa[0])
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
		pf, err := parseStructFormatVal(pa[0])
		if err != nil {
			exc := val.NewException(structErrorType, err.Error())
			panic(exc)
		}
		b, ok := getBytesFromValue(pa[1])
		if !ok {
			ctx.Raise(val.NewException(val.TypeErrorType, "pack_into() requires a buffer"))
			return val.NoneVal()
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
		pf, err := parseStructFormatVal(pa[0])
		if err != nil {
			exc := val.NewException(structErrorType, err.Error())
			panic(exc)
		}
		b, ok := getBytesFromValue(pa[1])
		if !ok {
			ctx.Raise(val.NewException(val.TypeErrorType, "unpack() requires bytes-like object"))
			return val.NoneVal()
		}
		tv, err := tupleFromUnpack(pf, b)
		if err != nil {
			exc := val.NewException(structErrorType, err.Error())
			panic(exc)
		}
		return tv
	})

	// unpack_from(format, buffer, offset=0)
	set("unpack_from", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 2 {
			exc := val.NewException(structErrorType, "unpack_from() takes at least 2 arguments")
			panic(exc)
		}
		pf, err := parseStructFormatVal(pa[0])
		if err != nil {
			exc := val.NewException(structErrorType, err.Error())
			panic(exc)
		}
		b, ok := getBytesFromValue(pa[1])
		if !ok {
			ctx.Raise(val.NewException(val.TypeErrorType, "unpack_from() requires bytes-like object"))
			return val.NoneVal()
		}
		offset := 0
		if len(pa) > 2 {
			offset = int(pa[2].AsInt64())
		}
		if offset < 0 || offset > len(b) {
			exc := val.NewException(structErrorType, "unpack_from requires buffer of at least required size")
			panic(exc)
		}
		tv, err := tupleFromUnpack(pf, b[offset:])
		if err != nil {
			exc := val.NewException(structErrorType, err.Error())
			panic(exc)
		}
		return tv
	})

	// iter_unpack(format, buffer)
	set("iter_unpack", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 2 {
			exc := val.NewException(structErrorType, "iter_unpack() takes at least 2 arguments")
			panic(exc)
		}
		pf, err := parseStructFormatVal(pa[0])
		if err != nil {
			exc := val.NewException(structErrorType, err.Error())
			panic(exc)
		}
		b, ok := getBytesFromValue(pa[1])
		if !ok {
			ctx.Raise(val.NewException(val.TypeErrorType, "iter_unpack() requires bytes-like object"))
			return val.NoneVal()
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
			tv, _ := tupleFromUnpack(pf, b[curOffset:])
			curOffset += pf.size
			return tv, true
		})
	})

	return modVal
}
