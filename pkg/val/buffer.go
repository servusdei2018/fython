package val

import (
	"errors"
	"math"
	"runtime"
	"strings"
	"unsafe"
)

// PinnerState wraps runtime.Pinner for buffer lifetime pinning.
type PinnerState struct {
	Pinner *runtime.Pinner
	Buf    []byte
}

func (p *PinnerState) Pin(buf []byte) {
	if p == nil {
		return
	}
	if len(buf) == 0 {
		return
	}
	if p.Pinner == nil {
		p.Pinner = &runtime.Pinner{}
	}
	p.Pinner.Pin(&buf[0])
	p.Buf = buf
}

func (p *PinnerState) Unpin() {
	if p == nil {
		return
	}
	if p.Pinner != nil {
		p.Pinner.Unpin()
		p.Pinner = nil
	}
	p.Buf = nil
}

// Format parsing (PEP 3118 subset).
// Supported: B b h i l q f d c P n N H I L Q s x, with @ < = > ! prefix (accepted, ignored for size).
// "B" is unsigned byte (1). "c" is char (1). "s" variable -> size -1 (treated as 1 for itemsize purposes unless count given).
func FormatSize(format string) (int, error) {
	if format == "" {
		return 1, nil
	}
	s := format
	// Strip endian / native prefix.
	if len(s) > 0 && (s[0] == '@' || s[0] == '<' || s[0] == '=' || s[0] == '>' || s[0] == '!') {
		s = s[1:]
	}
	if s == "" {
		return 1, nil
	}
	// Count prefix like "4B" or "16s" — return element size, caller multiplies by count when needed.
	// For simplicity, if numeric prefix present, just return size of last char.
	// Also struct "(...)" — sum member sizes, minimal support.
	if s[0] == '(' {
		if !strings.HasSuffix(s, ")") {
			return 0, errors.New("invalid format: missing close paren")
		}
		inner := s[1 : len(s)-1]
		total := 0
		i := 0
		for i < len(inner) {
			// skip spaces and count digits
			if inner[i] == 'x' || inner[i] == ' ' {
				i++
				continue
			}
			if inner[i] >= '0' && inner[i] <= '9' {
				for i < len(inner) && inner[i] >= '0' && inner[i] <= '9' {
					i++
				}
				if i < len(inner) {
					sz, err := FormatSize(string(inner[i]))
					if err != nil {
						return 0, err
					}
					// repeat count
					if sz > 0 {
						total += sz // count ignored for struct packing simplicity (each once)
					}
					i++
				}
				continue
			}
			sz, err := FormatSize(string(inner[i]))
			if err != nil {
				return 0, err
			}
			total += sz
			i++
		}
		return total, nil
	}
	// strip leading count digits.
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i >= len(s) {
		return 0, errors.New("invalid format: missing code")
	}
	c := s[i]
	switch c {
	case 'B', 'b', 'c':
		return 1, nil
	case 'H', 'h':
		return 2, nil
	case 'I', 'i', 'L', 'l', 'f':
		return 4, nil
	case 'Q', 'q', 'd':
		return 8, nil
	case 'P', 'n', 'N':
		return 8, nil // pointer size on 64-bit (LP64 / LLM still 8 for pointers)
	case 's':
		return 1, nil // treated as bytes; count gives total len
	case 'x':
		return 1, nil
	default:
		return 0, errors.New("unsupported format code: " + string(c))
	}
}

func CContiguousStrides(shape []int, itemSize int) []int {
	if len(shape) == 0 {
		return nil
	}
	strides := make([]int, len(shape))
	acc := itemSize
	for i := len(shape) - 1; i >= 0; i-- {
		strides[i] = acc
		acc *= shape[i]
	}
	return strides
}

func FContiguousStrides(shape []int, itemSize int) []int {
	if len(shape) == 0 {
		return nil
	}
	strides := make([]int, len(shape))
	acc := itemSize
	for i := range shape {
		strides[i] = acc
		acc *= shape[i]
	}
	return strides
}

// total elements in shape (product); 1 for scalar shape [].
func ShapeNumElements(shape []int) int {
	if len(shape) == 0 {
		return 1
	}
	n := 1
	for _, d := range shape {
		n *= d
	}
	return n
}

// Validate strides match shape (roughly).
func IsCContiguous(shape, strides []int, itemSize int) bool {
	if len(shape) == 0 {
		return true
	}
	if len(strides) != len(shape) {
		return false
	}
	want := CContiguousStrides(shape, itemSize)
	for i := range want {
		if strides[i] != want[i] {
			return false
		}
	}
	return true
}

func IsFContiguous(shape, strides []int, itemSize int) bool {
	if len(shape) == 0 {
		return true
	}
	if len(strides) != len(shape) {
		return false
	}
	want := FContiguousStrides(shape, itemSize)
	for i := range want {
		if strides[i] != want[i] {
			return false
		}
	}
	return true
}

// decode helpers for tolist
func decodeValue(buf []byte, offset int, format string, itemSize int) Value {
	if offset < 0 || offset+itemSize > len(buf) {
		return NoneVal()
	}
	raw := buf[offset : offset+itemSize]
	switch format {
	case "B":
		return Int64Val(int64(raw[0]))
	case "b":
		return Int64Val(int64(int8(raw[0])))
	case "c":
		return NewBytesVal([]byte{raw[0]})
	case "h", "H", "i", "I", "l", "L", "q", "Q", "n", "N", "P":
		// little-endian decode, extend to int64
		var v uint64
		for i := range raw {
			v |= uint64(raw[i]) << (8 * i)
		}
		// sign-extend for signed
		switch format {
		case "h":
			return Int64Val(int64(int16(v)))
		case "i", "l":
			return Int64Val(int64(int32(v)))
		case "H", "I", "L":
			return Int64Val(int64(v))
		default:
			return Int64Val(int64(v))
		}
	case "f":
		bits := uint32(raw[0]) | uint32(raw[1])<<8 | uint32(raw[2])<<16 | uint32(raw[3])<<24
		return Float64Val(float64(float32frombits(bits)))
	case "d":
		var v uint64
		for i := range 8 {
			v |= uint64(raw[i]) << (8 * i)
		}
		return Float64Val(math.Float64frombits(v))
	default:
		return Int64Val(int64(raw[0]))
	}
}

func float32frombits(b uint32) float32 {
	return *(*float32)(unsafe.Pointer(&b))
}

// mvEffectiveShape returns shape, defaulting to 1D when nil.
func mvEffectiveShape(mv *PyMemoryView) []int {
	if len(mv.Shape) > 0 {
		return mv.Shape
	}
	is := mv.ItemSize
	if is <= 0 {
		is = 1
	}
	return []int{len(mv.Buf) / is}
}

// mvEffectiveStrides returns strides, defaulting to C-contiguous.
func mvEffectiveStrides(mv *PyMemoryView) []int {
	if len(mv.Strides) > 0 && len(mv.Strides) == len(mv.Shape) {
		return mv.Strides
	}
	return CContiguousStrides(mvEffectiveShape(mv), max(mv.ItemSize, 1))
}

// encodeValue writes val into buf at offset per PEP 3118 format.
func encodeValue(buf []byte, offset int, v Value, format string, itemSize int) {
	if offset < 0 || offset+itemSize > len(buf) {
		panic("IndexError: index out of bounds")
	}
	switch format {
	case "B":
		b := v.AsInt64()
		if b < 0 || b > 255 {
			panic("ValueError: byte must be in range(0, 256)")
		}
		buf[offset] = byte(b)
	case "b":
		b := v.AsInt64()
		if b < -128 || b > 127 {
			panic("ValueError: byte must be in range(-128, 127)")
		}
		buf[offset] = byte(int8(b))
	case "c":
		if v.IsBytes() && len(v.AsBytes().Data) == 1 {
			buf[offset] = v.AsBytes().Data[0]
			return
		}
		if v.IsAnyString() || v.IsInt64() {
			// accept int 0..255
			buf[offset] = byte(v.AsInt64())
			return
		}
		panic("TypeError: cannot assign to c format")
	case "h", "H", "i", "I", "l", "L", "q", "Q", "n", "N", "P":
		var uv uint64
		if v.IsFloat64() {
			uv = uint64(v.AsFloat64())
		} else {
			uv = uint64(v.AsInt64())
		}
		for i := 0; i < itemSize && i < 8; i++ {
			buf[offset+i] = byte(uv >> (8 * i))
		}
	case "f":
		f := float32(v.AsFloat64())
		bits := math.Float32bits(f)
		for i := range 4 {
			buf[offset+i] = byte(bits >> (8 * i))
		}
	case "d":
		bits := math.Float64bits(v.AsFloat64())
		for i := range 8 {
			buf[offset+i] = byte(bits >> (8 * i))
		}
	default:
		// generic: treat as byte
		if v.IsInt64() {
			buf[offset] = byte(v.AsInt64())
		} else if v.IsFloat64() {
			buf[offset] = byte(v.AsFloat64())
		}
	}
}

// mvSubview builds a zero-copy subview sharing Buf backing array with a byte offset.
func mvSubview(mv *PyMemoryView, byteOffset int, shape, strides []int) Value {
	buf := mv.Buf
	if byteOffset > 0 {
		if byteOffset >= len(buf) {
			buf = buf[len(buf):len(buf):len(buf)]
		} else {
			buf = buf[byteOffset:]
		}
	}
	format := mv.Format
	if format == "" {
		format = "B"
	}
	obj := &PyMemoryView{
		Header:   ObjectHeader{Kind: KindMemoryView, Type: MemoryViewType},
		Obj:      mv.Obj,
		Buf:      buf,
		ReadOnly: mv.ReadOnly,
		ItemSize: mv.ItemSize,
		Format:   format,
		NDim:     len(shape),
		Shape:    append([]int(nil), shape...),
		Strides:  append([]int(nil), strides...),
	}
	if obj.ItemSize == 0 {
		if sz, err := FormatSize(obj.Format); err == nil && sz > 0 {
			obj.ItemSize = sz
		} else {
			obj.ItemSize = 1
		}
	}
	return Value{num: 0, ptr: &obj.Header}
}

// Ensure imports used (float64frombits is in value.go via math)
