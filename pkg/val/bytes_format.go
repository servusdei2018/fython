package val

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

func bytesFormatKeyError(keyBytes []byte) Value {
	return NewKeyError(NewBytesVal(append([]byte(nil), keyBytes...)))
}

func bytesAsBufferLike(ctx *ExecContext, v Value, conv byte) ([]byte, bool) {
	switch {
	case v.IsBytes():
		return v.AsBytes().Data, true
	case v.IsByteArray():
		return v.AsByteArray().Data, true
	case v.IsMemoryView():
		mv := v.AsMemoryView()
		return mv.Buf, true
	}
	tryBytesDunder := func(useCtx *ExecContext) (Value, bool) {
		fn, _, ok := v.Type().LookupMRO(InternString("__bytes__"))
		if !ok || fn.IsNone() || fn.HeaderPtr() == nil {
			return NoneVal(), false
		}
		var res Value
		if descrGet := fn.Type().Slots.TpDescrGet; descrGet != nil {
			bound := descrGet(useCtx, fn, v, v.Type())
			if useCtx != nil && useCtx.HasException() {
				return NoneVal(), true
			}
			res = useCtx.Invoke0(bound)
		} else {
			res = useCtx.Invoke1(fn, v)
		}
		return res, true
	}
	switch {
	case ctx != nil && ctx.CallHook != nil:
		if res, found := tryBytesDunder(ctx); found {
			if ctx.HasException() {
				return nil, false
			}
			if res.IsBytes() {
				return res.AsBytes().Data, true
			}
			if res.IsByteArray() {
				return res.AsByteArray().Data, true
			}
		}
	case AcquireExecContextHook != nil:
		tmpCtx, release := AcquireExecContextHook()
		defer release()
		if res, found := tryBytesDunder(tmpCtx); found {
			if tmpCtx.HasException() {
				if ctx != nil {
					ctx.Raise(tmpCtx.ActiveException)
					tmpCtx.ClearException()
				}
				return nil, false
			}
			if res.IsBytes() {
				return res.AsBytes().Data, true
			}
			if res.IsByteArray() {
				return res.AsByteArray().Data, true
			}
		}
	default:
		if ctx != nil {
			if res, found := tryBytesDunder(ctx); found {
				if ctx.HasException() {
					return nil, false
				}
				if res.IsBytes() {
					return res.AsBytes().Data, true
				}
				if res.IsByteArray() {
					return res.AsByteArray().Data, true
				}
			}
		}
	}
	return nil, false
}

func bytesFormatTypeName(v Value) string {
	tp := v.Type()
	if tp == nil {
		return "object"
	}
	return tp.Name
}

func formatBytesModCtx(ctx *ExecContext, fmtBytes []byte, arg Value, wantByteArray bool) Value {
	var items []Value
	var singleItem [1]Value
	var argDict *PyDict
	var argDictIsBytes bool

	hasPositional := false
	for j := 0; j < len(fmtBytes); j++ {
		if fmtBytes[j] == '%' {
			if j+1 < len(fmtBytes) && fmtBytes[j+1] == '%' {
				j++
				continue
			}
			if j+1 < len(fmtBytes) && fmtBytes[j+1] == '(' {
			} else {
				hasPositional = true
			}
		}
	}
	switch {
	case arg.IsTuple():
		items = arg.AsTuple().Items
	case arg.IsDict() && !hasPositional:
		argDict = arg.AsDict()
		argDictIsBytes = true
	default:
		singleItem[0] = arg
		items = singleItem[:]
	}

	var buf []byte
	if len(fmtBytes) <= 128 {
		var stackBuf [128]byte
		buf = stackBuf[:0]
		buf = append(buf, fmtBytes[:0]...)
	} else {
		buf = make([]byte, 0, len(fmtBytes)+32)
	}
	_ = buf
	buf = buf[:0]
	itemIdx := 0
	i := 0
	n := len(fmtBytes)
	hasAnyMapKey := false

	for i < n {
		litStart := i
		for i < n && fmtBytes[i] != '%' {
			i++
		}
		if i > litStart {
			buf = append(buf, fmtBytes[litStart:i]...)
		}
		if i >= n {
			break
		}
		if i+1 < n && fmtBytes[i+1] == '%' {
			buf = append(buf, '%')
			i += 2
			continue
		}
		i++
		var mapKey []byte
		hasMapKey := false
		if i < n && fmtBytes[i] == '(' {
			startKey := i + 1
			rel := -1
			for k := startKey; k < n; k++ {
				if fmtBytes[k] == ')' {
					rel = k - startKey
					break
				}
			}
			if rel >= 0 {
				mapKey = fmtBytes[startKey : startKey+rel]
				hasMapKey = true
				hasAnyMapKey = true
				i = startKey + rel + 1
			}
		}
		startSpec := i
		hasStar := false
		for i < n && (fmtBytes[i] == '-' || fmtBytes[i] == '+' || fmtBytes[i] == '0' || fmtBytes[i] == ' ' || fmtBytes[i] == '#' || (fmtBytes[i] >= '0' && fmtBytes[i] <= '9') || fmtBytes[i] == '.' || fmtBytes[i] == '*') {
			if fmtBytes[i] == '*' {
				hasStar = true
			}
			i++
		}
		if i >= n {
			return raiseValueError(ctx, "incomplete format")
		}
		specBytes := fmtBytes[startSpec:i]
		specifier := string(specBytes)
		convType := fmtBytes[i]
		i++

		if hasStar {
			for strings.Contains(specifier, "*") {
				if itemIdx >= len(items) {
					return raiseTypeError(ctx, "not enough arguments for format string (got "+strconv.Itoa(len(items))+")")
				}
				starVal := items[itemIdx]
				itemIdx++
				if !starVal.IsInt64() && !starVal.IsBool() {
					return raiseTypeError(ctx, "* wants int")
				}
				starInt := starVal.AsInt64()
				specifier = strings.Replace(specifier, "*", strconv.FormatInt(starInt, 10), 1)
			}
		}

		var curVal Value
		if hasMapKey {
			if argDict == nil {
				return raiseTypeError(ctx, "format requires a mapping")
			}
			var ok bool
			if argDictIsBytes {
				kb := NewBytesVal(append([]byte(nil), mapKey...))
				curVal, ok = argDict.Get(kb)
				if !ok {
					if ctx != nil {
						ctx.Raise(NewKeyError(kb))
						return NoneVal()
					}
					return NewKeyError(kb)
				}
			} else {
				curVal, ok = argDict.Get(InternString(string(mapKey)))
				if !ok {
					return bytesFormatKeyError(mapKey)
				}
			}
		} else {
			if itemIdx >= len(items) {
				return raiseTypeError(ctx, "not enough arguments for format string (got "+strconv.Itoa(len(items))+")")
			}
			curVal = items[itemIdx]
			itemIdx++
		}

		switch convType {
		case 'b', 's':
			b, ok := bytesAsBufferLike(ctx, curVal, convType)
			if ctx != nil && ctx.HasException() {
				return NoneVal()
			}
			if !ok {
				return raiseTypeError(ctx, "format argument: %b requires a bytes-like object, or an object that implements __bytes__, not "+bytesFormatTypeName(curVal))
			}
			if specifier == "" {
				buf = append(buf, b...)
			} else {
				s := string(b)
				buf = append(buf, fmt.Sprintf("%"+specifier+"s", s)...)
			}
			continue
		case 'a', 'r':
			rendered := asciiEscape(modConvert(ctx, curVal, PyRepr))
			if specifier != "" {
				rendered = fmt.Sprintf("%"+specifier+"s", rendered)
			}
			buf = append(buf, rendered...)
			continue
		case 'c':
			switch {
			case curVal.IsInt64() || curVal.IsBool():
				c := curVal.AsInt64()
				if c < 0 || c > 255 {
					if ctx != nil {
						ctx.Raise(NewOverflowError("format argument: %c argument not in range(256)"))
						return NoneVal()
					}
					return NewOverflowError("format argument: %c argument not in range(256)")
				}
				buf = append(buf, byte(c))
			case curVal.IsBytes():
				d := curVal.AsBytes().Data
				if len(d) != 1 {
					return raiseTypeError(ctx, "format argument: %c requires an integer in range(256) or a single byte, not a bytes object of length "+strconv.Itoa(len(d)))
				}
				buf = append(buf, d[0])
			case curVal.IsByteArray():
				d := curVal.AsByteArray().Data
				if len(d) != 1 {
					return raiseTypeError(ctx, "format argument: %c requires an integer in range(256) or a single byte, not a bytearray object of length "+strconv.Itoa(len(d)))
				}
				buf = append(buf, d[0])
			case isAnyInt(curVal):
				bi := toBigInt(curVal)
				if bi.Sign() < 0 || bi.BitLen() > 8 {
					if ctx != nil {
						ctx.Raise(NewOverflowError("format argument: %c argument not in range(256)"))
						return NoneVal()
					}
					return NewOverflowError("format argument: %c argument not in range(256)")
				}
				buf = append(buf, byte(bi.Int64()))
			default:
				return raiseTypeError(ctx, "format argument: %c requires an integer in range(256) or a single byte, not "+bytesFormatTypeName(curVal))
			}
			continue
		case 'd', 'i', 'u':
			verb := "d"
			if curVal.IsInt64() || curVal.IsBool() {
				if ps, ok := parsePctSpec(specifier); ok {
					buf = appendPctInt64(buf, curVal.AsInt64(), 'd', ps)
					continue
				}
				buf = append(buf, fmt.Sprintf("%"+specifier+"d", curVal.AsInt64())...)
				continue
			}
			if curVal.IsFloat64() {
				f := curVal.AsFloat64()
				if math.IsNaN(f) {
					return raiseValueError(ctx, "cannot convert float NaN to integer")
				}
				if math.IsInf(f, 0) {
					return raiseOverflowError(ctx, "cannot convert float infinity to integer")
				}
				bi, _ := new(big.Float).SetFloat64(f).Int(nil)
				if ps, ok := parsePctSpec(specifier); ok {
					if bi.IsInt64() {
						buf = appendPctInt64(buf, bi.Int64(), 'd', ps)
					} else {
						buf = append(buf, fmt.Sprintf("%"+specifier+"d", bi)...)
					}
					continue
				}
				buf = append(buf, fmt.Sprintf("%"+specifier+"d", bi)...)
				continue
			}
			if isAnyInt(curVal) {
				bi := toBigInt(curVal)
				buf = append(buf, fmt.Sprintf("%"+specifier+verb, bi)...)
				continue
			}
			return raiseTypeError(ctx, "format argument: %"+string([]byte{convType})+" requires a real number, not "+bytesFormatTypeName(curVal))
		case 'o', 'x', 'X':
			if curVal.IsInt64() || curVal.IsBool() {
				if ps, ok := parsePctSpec(specifier); ok {
					buf = appendPctInt64(buf, curVal.AsInt64(), convType, ps)
					continue
				}
				buf = append(buf, fmt.Sprintf("%"+specifier+string([]byte{convType}), curVal.AsInt64())...)
				continue
			}
			if isAnyInt(curVal) {
				bi := toBigInt(curVal)
				buf = append(buf, fmt.Sprintf("%"+specifier+string([]byte{convType}), bi)...)
				continue
			}
			return raiseTypeError(ctx, "format argument: %"+string([]byte{convType})+" requires an integer, not "+bytesFormatTypeName(curVal))
		case 'e', 'E', 'f', 'F', 'g', 'G':
			var fVal float64
			switch {
			case curVal.IsFloat64():
				fVal = curVal.AsFloat64()
			case curVal.IsInt64() || curVal.IsBool():
				fVal = float64(curVal.AsInt64())
			case isAnyInt(curVal):
				bi := toBigInt(curVal)
				fVal, _ = new(big.Float).SetInt(bi).Float64()
			default:
				return raiseTypeError(ctx, "format argument: %"+string([]byte{convType})+" requires a real number, not "+bytesFormatTypeName(curVal))
			}
			if ps, ok := parsePctSpec(specifier); ok {
				buf = appendPctFloat64(buf, fVal, convType, ps)
				continue
			}
			buf = append(buf, fmt.Sprintf("%"+specifier+string([]byte{convType}), fVal)...)
			continue
		default:
			return raiseValueError(ctx, "unsupported format character '"+string([]byte{convType})+"'")
		}
	}

	if !hasAnyMapKey && itemIdx < len(items) {
		if len(items)-itemIdx == 1 && items[itemIdx].IsTuple() {
		} else {
			return raiseTypeError(ctx, "not all arguments converted during bytes formatting (required "+strconv.Itoa(itemIdx)+", got "+strconv.Itoa(len(items))+")")
		}
	}

	out := append([]byte(nil), buf...)
	_ = utf8.Valid(out)
	if wantByteArray {
		return NewByteArrayVal(out)
	}
	return NewBytesVal(out)
}
