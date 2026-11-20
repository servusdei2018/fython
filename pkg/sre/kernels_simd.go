//go:build goexperiment.simd && amd64

package sre

import (
	"math/bits"
	"simd/archsimd"
	"unsafe"
)

var sreBitLUT32 = [32]uint8{
	0x01, 0x02, 0x04, 0x08, 0x10, 0x20, 0x40, 0x80,
	0, 0, 0, 0, 0, 0, 0, 0,
	0x01, 0x02, 0x04, 0x08, 0x10, 0x20, 0x40, 0x80,
	0, 0, 0, 0, 0, 0, 0, 0,
}

func scanCharSetBytes(b []byte, cs *charSet) int {
	n := len(b)
	if n >= 32 && archsimd.X86.AVX2() {
		var lut32 [32]uint8
		copy(lut32[:16], cs.latinBits[:16])
		copy(lut32[16:], cs.latinBits[:16])
		lutVec := archsimd.LoadUint8x32Array(&lut32)
		bitLutVec := archsimd.LoadUint8x32Array(&sreBitLUT32)
		b0x07 := archsimd.BroadcastUint8x32(0x07)
		b0x0F := archsimd.BroadcastUint8x32(0x0F)
		b0x80 := archsimd.BroadcastUint8x32(0x80)
		zero := archsimd.BroadcastUint8x32(0)
		p := unsafe.Pointer(unsafe.SliceData(b))
		i := 0
		for ; i+32 <= n; i += 32 {
			v := archsimd.LoadUint8x32Array((*[32]uint8)(unsafe.Add(p, i)))
			// For bytes < 128, (v >> 3) & 0x0F indexes the 16-byte ASCII bitmap;
			// bytes >= 128 set bit 7 via (v & 0x80), making PermuteOrZeroGrouped yield 0.
			rowIdx := v.AsUint16x16().ShiftAllRight(3).AsUint8x32().And(b0x0F).Or(v.And(b0x80)).AsInt8x32()
			row := lutVec.PermuteOrZeroGrouped(rowIdx)
			bit := bitLutVec.PermuteOrZeroGrouped(v.And(b0x07).AsInt8x32())
			m := row.And(bit).NotEqual(zero).ToBits()
			if m != 0xFFFFFFFF {
				return i + bits.TrailingZeros32(^m)
			}
		}
		return i + scanCharSetBytesSWAR(b[i:], cs)
	}
	return scanCharSetBytesSWAR(b, cs)
}

func scanByteEq(b []byte, want byte) int {
	n := len(b)
	if n >= 32 && archsimd.X86.AVX2() {
		p := unsafe.Pointer(unsafe.SliceData(b))
		vWant := archsimd.BroadcastUint8x32(want)
		i := 0
		for ; i+32 <= n; i += 32 {
			v := archsimd.LoadUint8x32Array((*[32]uint8)(unsafe.Add(p, i)))
			m := v.Equal(vWant).ToBits()
			if m != 0xFFFFFFFF {
				return i + bits.TrailingZeros32(^m)
			}
		}
		return i + scanByteEqSWAR(b[i:], want)
	}
	return scanByteEqSWAR(b, want)
}

func scanCharSetRunes(rs []rune, cs *charSet) int {
	n := len(rs)
	if n >= 8 && archsimd.X86.AVX2() {
		tbl := &cs.table
		p := unsafe.Pointer(unsafe.SliceData(rs))
		vMinusOne := archsimd.BroadcastInt32x8(-1)
		v128 := archsimd.BroadcastInt32x8(128)
		i := 0
		for ; i+8 <= n; i += 8 {
			v := archsimd.LoadInt32x8Array((*[8]int32)(unsafe.Add(p, i*4)))
			if v.Greater(vMinusOne).And(v.Less(v128)).ToBits() != 0xFF {
				return i + scanCharSetRunesScalar(rs[i:], cs)
			}
			if !tbl[rs[i]] {
				return i
			}
			if !tbl[rs[i+1]] {
				return i + 1
			}
			if !tbl[rs[i+2]] {
				return i + 2
			}
			if !tbl[rs[i+3]] {
				return i + 3
			}
			if !tbl[rs[i+4]] {
				return i + 4
			}
			if !tbl[rs[i+5]] {
				return i + 5
			}
			if !tbl[rs[i+6]] {
				return i + 6
			}
			if !tbl[rs[i+7]] {
				return i + 7
			}
		}
		return i + scanCharSetRunesSWAR(rs[i:], cs)
	}
	return scanCharSetRunesSWAR(rs, cs)
}

func indexRuneFast(rs []rune, want rune) int {
	n := len(rs)
	if n >= 8 && archsimd.X86.AVX2() {
		p := unsafe.Pointer(unsafe.SliceData(rs))
		vWant := archsimd.BroadcastInt32x8(want)
		i := 0
		for ; i+8 <= n; i += 8 {
			v := archsimd.LoadInt32x8Array((*[8]int32)(unsafe.Add(p, i*4)))
			if m := v.Equal(vWant).ToBits(); m != 0 {
				return i + bits.TrailingZeros8(m)
			}
		}
		return i + indexRuneSWAR(rs[i:], want)
	}
	return indexRuneSWAR(rs, want)
}
