//go:build goexperiment.simd && amd64

package val

import (
	"math/bits"
	"simd/archsimd"
	"unsafe"
)

func isASCIIBytes(b []byte) bool {
	n := len(b)
	if n >= 32 && archsimd.X86.AVX2() {
		p := unsafe.Pointer(unsafe.SliceData(b))
		zero := archsimd.BroadcastInt8x32(0)
		i := 0
		for ; i+32 <= n; i += 32 {
			v := archsimd.LoadUint8x32Array((*[32]uint8)(unsafe.Add(p, i)))
			if v.AsInt8x32().Less(zero).ToBits() != 0 {
				return false
			}
		}
		return isASCIIBytesSWAR(b[i:])
	}
	return isASCIIBytesSWAR(b)
}

func findJSONEscape(b []byte) int {
	n := len(b)
	if n >= 32 && archsimd.X86.AVX2() {
		p := unsafe.Pointer(unsafe.SliceData(b))
		vSpace := archsimd.BroadcastUint8x32(0x20)
		vQuote := archsimd.BroadcastUint8x32('"')
		vSlash := archsimd.BroadcastUint8x32('\\')
		i := 0
		for ; i+32 <= n; i += 32 {
			v := archsimd.LoadUint8x32Array((*[32]uint8)(unsafe.Add(p, i)))
			mask := v.Less(vSpace).Or(v.Equal(vQuote)).Or(v.Equal(vSlash)).ToBits()
			if mask != 0 {
				return i + bits.TrailingZeros32(mask)
			}
		}
		return i + findJSONEscapeSWAR(b[i:])
	}
	return findJSONEscapeSWAR(b)
}

func findJSONEscapeASCII(b []byte) int {
	n := len(b)
	if n >= 32 && archsimd.X86.AVX2() {
		p := unsafe.Pointer(unsafe.SliceData(b))
		zero := archsimd.BroadcastInt8x32(0)
		vSpace := archsimd.BroadcastUint8x32(0x20)
		vQuote := archsimd.BroadcastUint8x32('"')
		vSlash := archsimd.BroadcastUint8x32('\\')
		i := 0
		for ; i+32 <= n; i += 32 {
			v := archsimd.LoadUint8x32Array((*[32]uint8)(unsafe.Add(p, i)))
			mask := v.Less(vSpace).Or(v.Equal(vQuote)).Or(v.Equal(vSlash)).Or(v.AsInt8x32().Less(zero)).ToBits()
			if mask != 0 {
				return i + bits.TrailingZeros32(mask)
			}
		}
		return i + findJSONEscapeASCIISWAR(b[i:])
	}
	return findJSONEscapeASCIISWAR(b)
}
