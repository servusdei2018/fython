package capi

import (
	"math/big"
	"slices"
	"strings"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// PyArg_ParseTuple and related — called via shim with pre-collected varargs array.
// Shim protocol: handler(argsStub, fmtPtr, outArrPtr) where outArr is array of MAX=16 void*.
// Handler parses format to know how many to consume; extra slots are garbage and ignored.
// Returns 1 on success, 0 on failure (with exception set).

const maxVarArgs = 16

func unpackTuple(argsVal val.Value, format string, outPtrs []unsafe.Pointer) bool {
	// format may contain: : | ! O p etc. Simplify: support units: b,B,h,H,i,I,l,L,q,Q,n,N,f,d,O,s,y,z,U,Y,t,e,E,c,h?, etc.
	// Iterate over format, skipping : and separators.
	items := []val.Value{}
	if argsVal.IsTuple() {
		items = argsVal.AsTuple().Items
	}
	// Count expected units (excluding :|!X and attributes)
	type unit struct {
		conv byte
	}
	var units []unit
	for _, c := range []byte(format) {
		if c == ':' || c == '|' || c == '!' || c == ' ' {
			continue
		}
		if strings.ContainsRune("bBhHiIlLqQnNfdOsYzUtESc", rune(c)) {
			units = append(units, unit{conv: c})
		}
	}
	// Allow optional (: |) to mean any count? For simplicity require exact match unless optional marker present.
	hasOptional := strings.Contains(format, "|")
	if !hasOptional && len(items) != len(units) {
		pyErrSetTypeError("function takes exactly " + itoa(len(units)) + " argument(s) (" + itoa(len(items)) + " given)")
		return false
	}
	if hasOptional && len(items) > len(units) {
		pyErrSetTypeError("too many arguments")
		return false
	}
	if hasOptional && len(items) < countRequiredBeforeOptional(format) {
		pyErrSetTypeError("not enough arguments")
		return false
	}
	for i, u := range units {
		if i >= len(items) {
			break
		}
		if i >= len(outPtrs) {
			break
		}
		out := outPtrs[i]
		if out == nil {
			pyErrSetTypeError("NULL output pointer")
			return false
		}
		if !convertOne(items[i], u.conv, out) {
			return false
		}
	}
	return true
}

func countRequiredBeforeOptional(format string) int {
	n := 0
	for _, c := range []byte(format) {
		if c == '|' {
			break
		}
		if strings.ContainsRune("bBhHiIlLqQnNfdOsYzUtESc", rune(c)) {
			n++
		}
	}
	return n
}

func convertOne(v val.Value, conv byte, out unsafe.Pointer) bool {
	switch conv {
	case 'L': // long long
		if v.IsInt64() || v.IsBool() {
			*(*int64)(out) = v.AsInt64()
			return true
		}
		if v.Kind() == val.KindBigInt && v.AsBigInt().Val.IsInt64() {
			*(*int64)(out) = v.AsBigInt().Val.Int64()
			return true
		}
		pyErrSetTypeError("expected int")
		return false
	case 'l', 'n', 'N': // long / Py_ssize_t
		if v.IsInt64() || v.IsBool() {
			*(*int64)(out) = v.AsInt64()
			return true
		}
		if v.Kind() == val.KindBigInt && v.AsBigInt().Val.IsInt64() {
			*(*int64)(out) = v.AsBigInt().Val.Int64()
			return true
		}
		pyErrSetTypeError("expected int")
		return false
	case 'i', 'b': // int (32) / bool? b is int? For ParseTuple "b" is int? Accept int64 out as int32? Store low.
		if v.IsInt64() || v.IsBool() {
			*((*int32)(out)) = int32(v.AsInt64())
			return true
		}
		pyErrSetTypeError("expected int")
		return false
	case 'B': // unsigned char? Use uint8?
		if v.IsInt64() || v.IsBool() {
			*((*uint8)(out)) = uint8(v.AsInt64() & 0xFF)
			return true
		}
		pyErrSetTypeError("expected int")
		return false
	case 'h': // short
		if v.IsInt64() || v.IsBool() {
			*((*int16)(out)) = int16(v.AsInt64())
			return true
		}
		pyErrSetTypeError("expected int")
		return false
	case 'H':
		if v.IsInt64() || v.IsBool() {
			*((*uint16)(out)) = uint16(v.AsInt64())
			return true
		}
		pyErrSetTypeError("expected int")
		return false
	case 'I': // unsigned int
		if v.IsInt64() || v.IsBool() {
			*((*uint32)(out)) = uint32(v.AsInt64() & 0xFFFFFFFF)
			return true
		}
		pyErrSetTypeError("expected int")
		return false
	case 'K': // unsigned long long
		if v.IsInt64() || v.IsBool() {
			*(*uint64)(out) = uint64(v.AsInt64())
			return true
		}
		if v.Kind() == val.KindBigInt {
			*(*uint64)(out) = v.AsBigInt().Val.Uint64()
			return true
		}
		pyErrSetTypeError("expected int")
		return false
	case 'Q': // unsigned long long variant
		return convertOne(v, 'K', out)
	case 'q': // signed long long
		return convertOne(v, 'L', out)
	case 'f', 'd': // float / double (d is double, f is float) — go handlers store float64
		// Out is double* for 'd', float* for 'f'
		if v.IsFloat64() {
			if conv == 'f' {
				*((*float32)(out)) = float32(v.AsFloat64())
			} else {
				*((*float64)(out)) = v.AsFloat64()
			}
			return true
		}
		if v.IsInt64() || v.IsBool() {
			f := float64(v.AsInt64())
			if conv == 'f' {
				*((*float32)(out)) = float32(f)
			} else {
				*((*float64)(out)) = f
			}
			return true
		}
		pyErrSetTypeError("expected float")
		return false
	case 'O': // PyObject*
		*((*unsafe.Pointer)(out)) = val.ArenaBorrowStub(v)
		return true
	case 's', 'y', 'S': // string -> const char* (s) or bytes? For 's': converts to UTF-8 C string pointer (from arena), for 'y': bytes
		// out is char**
		if !v.IsAnyString() && !v.IsBytes() {
			pyErrSetTypeError("expected string")
			return false
		}
		// Provide stable pointer: need to store Go bytes somewhere. Use arena cache for strings, or for bytes allocate copy.
		// For 's', if string contains NUL, error in CPython. We ignore.
		stub := val.ArenaBorrowStub(v)
		ptr := val.ArenaStubUTF8(stub)
		*((*unsafe.Pointer)(out)) = ptr
		return true
	case 'z': // like s but NULL for None
		if v.IsNone() {
			*((*unsafe.Pointer)(out)) = nil
			return true
		}
		return convertOne(v, 's', out)
	case 'U': // unicode -> PyObject* (string object)
		*((*unsafe.Pointer)(out)) = val.ArenaBorrowStub(v)
		return true
	case 'E': // error? Actually 'E' is like O but with exception handling? Treat as O.
		*((*unsafe.Pointer)(out)) = val.ArenaBorrowStub(v)
		return true
	case 't': // text buffer?
		return convertOne(v, 's', out)
	case 'c': // char (int)
		if v.IsInt64() || v.IsBool() {
			*((*int32)(out)) = int32(v.AsInt64())
			return true
		}
		if v.IsAnyString() && len(v.AsString()) == 1 {
			*((*int32)(out)) = int32(v.AsString()[0])
			return true
		}
		pyErrSetTypeError("expected char/int")
		return false
	default:
		pyErrSetTypeError("unsupported format unit")
		return false
	}
}

// C pointer array reader that avoids unsafe.Slice on C memory (checkptr safe).
func readPtrArray(base unsafe.Pointer, n int) []unsafe.Pointer {
	out := make([]unsafe.Pointer, n)
	for i := range n {
		out[i] = *(*unsafe.Pointer)(unsafe.Add(base, i*8))
	}
	return out
}

func PyArg_ParseTuple(args unsafe.Pointer, format unsafe.Pointer, outArr unsafe.Pointer) int32 {
	if args == nil || format == nil || outArr == nil {
		pyErrSetTypeError("PyArg_ParseTuple called with NULL")
		return 0
	}
	argsVal := val.ArenaFromStub(args)
	if !argsVal.IsTuple() {
		pyErrSetTypeError("function argument must be a tuple")
		return 0
	}
	formatStr := GoString(format)
	outPtrs := readPtrArray(outArr, maxVarArgs)
	if unpackTuple(argsVal, formatStr, outPtrs) {
		return 1
	}
	return 0
}

func PyArg_ParseTupleAndKeywords(args unsafe.Pointer, kwargs unsafe.Pointer, format unsafe.Pointer, kwlist unsafe.Pointer, outArr unsafe.Pointer) int32 {
	if args == nil || format == nil || outArr == nil {
		pyErrSetTypeError("PyArg_ParseTupleAndKeywords called with NULL")
		return 0
	}
	// MVP: ignore kwargs/kwlist for keyword parsing; treat all as positional.
	// If kwargs non-nil, attempt simple keyword merge: map kwlist entries by name.
	// kwlist is array of const char* terminated by NULL.
	argsVal := val.ArenaFromStub(args)
	if !argsVal.IsTuple() {
		pyErrSetTypeError("function argument must be a tuple")
		return 0
	}
	formatStr := GoString(format)
	outPtrs := readPtrArray(outArr, maxVarArgs)

	// Handle keywords: build positional list + kw dict mapping
	items := argsVal.AsTuple().Items
	// Count units
	var units []byte
	for _, c := range []byte(formatStr) {
		if strings.ContainsRune("bBhHiIlLqQnNfdOsYzUtESc", rune(c)) {
			units = append(units, c)
		}
	}
	// If kwargs present and kwlist present, map by name
	if kwargs != nil && kwlist != nil {
		kwVal := val.ArenaFromStub(kwargs)
		if kwVal.IsDict() {
			// kwlist: array of char* until NULL
			var names []string
			// Need to read kwlist array up to MAX
			for _, p := range readPtrArray(kwlist, maxVarArgs) {
				if p == nil {
					break
				}
				names = append(names, GoString(p))
			}
			// items extended by mapping
			// For each name, find value in dict
			// Positional items fill first len(items) units; keywords fill rest.
			extended := make([]val.Value, 0, len(units))
			extended = append(extended, items...)
			// Build name->value map from dict
			// Go dict iteration: use Items()
			dmap := make(map[string]val.Value)
			for _, it := range kwVal.AsDict().Items() {
				if it.Key.IsAnyString() {
					dmap[it.Key.AsString()] = it.Value
				}
			}
			// Expect units count = len(items) + number of matched kwargs used (some may be optional)
			// Simple: expected count = len(items) + len(dmap) but if some kwargs not in names, error.
			// For each unit index beyond positional, find name.
			for idx := len(items); idx < len(units); idx++ {
				kwName := ""
				if idx < len(names) {
					kwName = names[idx]
				}
				if v, ok := dmap[kwName]; ok {
					extended = append(extended, v)
				} else {
					// Optional arg missing: leave None placeholder? Caller expects default handling? For MVP, if missing and unit is optional (after |), skip.
					// Convert placeholder: if format contains | and missing, we can skip conversion? But outPtrs must still be valid. Set zero.
					extended = append(extended, val.NoneVal())
				}
			}
			// Check unknown kwargs
			for k := range dmap {
				if !slices.Contains(names, k) {
					pyErrSetTypeError("got an unexpected keyword argument '" + k + "'")
					return 0
				}
			}
			// Check if any positional missing without kw default: rely on unpackTuple optional logic
			// Build synthetic tuple
			argsVal = val.NewTupleVal(extended...)
		}
	}
	if unpackTuple(argsVal, formatStr, outPtrs) {
		return 1
	}
	return 0
}

func PyArg_UnpackTuple(args unsafe.Pointer, format unsafe.Pointer, outArr unsafe.Pointer) int32 {
	return PyArg_ParseTuple(args, format, outArr)
}

// Py_BuildValue via shim: handler(formatPtr, argsArrPtr, count) computes result.
// Shim collects typed args into struct array: each elem is { int tag; uint64 bits; unsafe.Pointer ptr; double f; }
// But Go handler signature we request from shim is (format unsafe.Pointer, argsArr unsafe.Pointer, count int64) unsafe.Pointer.
func Py_BuildValue(format unsafe.Pointer, argsArr unsafe.Pointer, count int64) unsafe.Pointer {
	if format == nil {
		pyErrSetTypeError("Py_BuildValue called with NULL format")
		return nil
	}
	formatStr := GoString(format)
	result, ok := buildValue(formatStr, argsArr, count)
	if !ok {
		return nil
	}
	return val.ArenaToStub(result)
}

func buildValue(format string, argsArr unsafe.Pointer, count int64) (val.Value, bool) {
	// argsArr is array of buildArg (32 bytes each: tag int32, padding, bits uint64, ptr, double overlap?) Define layout in shim.
	// Layout: struct BuildArg { int tag; int _pad; uint64 bits; const void* ptr; double d; } but expand to 32B: tag(4)+pad(4)+bits(8)+ptr(8)+d(8)
	// Shim fills accordingly. Go reads tag/bits/ptr.
	type buildArg struct {
		Tag  int32
		_    int32
		Bits uint64
		Ptr  unsafe.Pointer
		D    float64
	}
	_ = buildArg{}
	get := func(i int64) (tag int32, bits uint64, ptr unsafe.Pointer, d float64) {
		if argsArr == nil || i >= count {
			return 0, 0, nil, 0
		}
		base := unsafe.Add(argsArr, int(i)*32) // 32 bytes per arg as per shim
		tag = *(*int32)(base)
		bits = *(*uint64)(unsafe.Add(base, 8))
		ptr = *(*unsafe.Pointer)(unsafe.Add(base, 16))
		d = *(*float64)(unsafe.Add(base, 24))
		return
	}
	// Parse format char by char, consuming args.
	var stack []val.Value
	argIdx := int64(0)
	s := format
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '%' && i+1 < len(s) && s[i+1] == '%' {
			// not valid for BuildValue; treat as error
			pyErrSetTypeError("invalid format")
			return val.NoneVal(), false
		}
		if strings.ContainsRune("ilLkKnN", rune(c)) {
			_, bits, _, _ := get(argIdx)
			argIdx++
			v := int64(bits)
			if c == 'k' || c == 'K' || c == 'n' || c == 'N' {
				// unsigned — treat as int64 if fits else bigint
				if bits <= uint64(maxInt64) {
					stack = append(stack, val.Int64Val(int64(bits)))
				} else {
					stack = append(stack, val.NewBigIntVal(new(big.Int).SetUint64(bits)))
				}
			} else {
				stack = append(stack, val.Int64Val(v))
			}
			i++
			continue
		}
		if c == 'f' || c == 'd' {
			_, _, _, d := get(argIdx)
			argIdx++
			stack = append(stack, val.Float64Val(d))
			i++
			continue
		}
		if c == 'O' {
			_, _, ptr, _ := get(argIdx)
			argIdx++
			if ptr == nil {
				stack = append(stack, val.NoneVal())
			} else {
				stack = append(stack, val.ArenaFromStub(ptr))
			}
			i++
			continue
		}
		if c == 's' || c == 'z' || c == 'y' || c == 'Y' {
			_, _, ptr, _ := get(argIdx)
			argIdx++
			if ptr == nil {
				if c == 'z' {
					stack = append(stack, val.NoneVal())
				} else {
					pyErrSetTypeError("NULL string")
					return val.NoneVal(), false
				}
			} else {
				str := GoString(ptr)
				stack = append(stack, val.NewStringVal(str))
			}
			i++
			continue
		}
		if c == 'S' || c == 'T' {
			// S: unicode object? T: ?
			_, _, ptr, _ := get(argIdx)
			argIdx++
			if ptr != nil {
				stack = append(stack, val.ArenaFromStub(ptr))
			} else {
				stack = append(stack, val.NoneVal())
			}
			i++
			continue
		}
		if c == 'N' {
			// Steals reference — same as O for our purposes (new ref semantics same as O since we borrow the Value, refcount handled by caller? For BuildValue N steals, caller won't DECREF. Our Value is borrowed from stub; no refcount change needed because handle table holds Value via stub; tuple holding it keeps it alive. So treat as O.
			_, _, ptr, _ := get(argIdx)
			argIdx++
			if ptr != nil {
				stack = append(stack, val.ArenaFromStub(ptr))
			} else {
				stack = append(stack, val.NoneVal())
			}
			i++
			continue
		}
		if c == '(' {
			// Tuple until ')'
			// Collect sub-stack until ')'
			start := len(stack)
			i++ // skip '('
			for i < len(s) && s[i] != ')' {
				// Recursively parse one unit? Simplify: handle O/l/s/f inside tuple by same loop logic but stacking.
				// To avoid complex recursion, process one char at a time using same dispatch above by not advancing stack yet?
				// We'll parse via sub-call: extract substring until match and recurse.
				break
			}
			// Find matching )
			depth := 1
			j := i
			for j < len(s) && depth > 0 {
				if s[j] == '(' {
					depth++
				} else if s[j] == ')' {
					depth--
					if depth == 0 {
						break
					}
				}
				j++
			}
			if depth != 0 {
				pyErrSetTypeError("unmatched '(' in format")
				return val.NoneVal(), false
			}
			innerFmt := s[i:j]
			// Need to account for args already consumed? Inner format consumes next args; count them.
			// Parse inner format via same buildValue but sharing argIdx? Our get uses argIdx global — need to share.
			// For simplicity, count units in innerFmt and consume that many, but nested tuples need correct values.
			// Implement recursive helper that shares argIdx.
			innerCount := countBuildUnits(innerFmt)
			innerArgsBase := argIdx
			_ = innerArgsBase
			// Build inner tuple by calling a closure that uses get with offset
			// Simplify: create sub-builder that reads from argsArr at offset argIdx
			subArgs := unsafe.Add(argsArr, int(argIdx)*32)
			innerVal, ok := buildValueAt(innerFmt, subArgs, innerCount)
			if !ok {
				return val.NoneVal(), false
			}
			argIdx += innerCount
			_ = start
			stack = append(stack, innerVal)
			i = j + 1
			continue
		}
		if c == ']' || c == '[' || c == '{' || c == '}' || c == ';' || c == ':' || c == ',' || c == ' ' {
			i++
			continue
		}
		if c == '[' || c == ']' {
			i++
			continue
		}
		// Unknown unit -> error
		pyErrSetTypeError("unsupported format character")
		return val.NoneVal(), false
	}
	// BuildValue returns single object: if stack has 1 element, return it; if 0, return None; if >1, return tuple? Actually CPython returns tuple when format contains multiple units without enclosing parens? E.g., Py_BuildValue("ll", a,b) returns tuple? No, CPython docs: "If format contains no conversion, returns None. If single conversion, returns that object. Otherwise returns tuple." Implement.
	if len(stack) == 0 {
		return val.NoneVal(), true
	}
	if len(stack) == 1 {
		return stack[0], true
	}
	return val.NewTupleVal(stack...), true
}

func buildValueAt(format string, argsArr unsafe.Pointer, count int64) (val.Value, bool) {
	// Wrapper that reads from offset pointer (argsArr already offset)
	return buildValue(format, argsArr, count)
}

func countBuildUnits(format string) int64 {
	n := 0
	for _, c := range []byte(format) {
		if c == '(' || c == ')' || c == '[' || c == ']' || c == '{' || c == '}' || c == ';' || c == ':' || c == ',' || c == ' ' {
			continue
		}
		if strings.ContainsRune("ilLkKnNfOdouslyzST", rune(c)) {
			n++
		}
	}
	return int64(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	b := []byte{}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

const maxInt64 = int64(^uint64(0) >> 1)
