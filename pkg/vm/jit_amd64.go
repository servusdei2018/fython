//go:build amd64 && linux

package vm

import (
	"math/bits"
	"runtime"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

const (
	jitMaxRegs    = 32
	jitMaxPinRegs = 7

	// amd64 GPR encodings
	regRAX uint8 = 0
	regRCX uint8 = 1
	regRDX uint8 = 2
	regRBX uint8 = 3
	regRSP uint8 = 4
	regRBP uint8 = 5
	regRSI uint8 = 6
	regRDI uint8 = 7
	regR8  uint8 = 8
	regR9  uint8 = 9
	regR10 uint8 = 10
	regR11 uint8 = 11
	regR12 uint8 = 12
	regR13 uint8 = 13
	regR14 uint8 = 14
	regR15 uint8 = 15

	// x86-64 condition codes
	ccO  uint8 = 0x0
	ccB  uint8 = 0x2
	ccAE uint8 = 0x3
	ccE  uint8 = 0x4
	ccNE uint8 = 0x5
	ccL  uint8 = 0xC
	ccGE uint8 = 0xD
	ccLE uint8 = 0xE
	ccG  uint8 = 0xF
)

var (
	jitHwRegs = [jitMaxPinRegs]uint8{regRAX, regRCX, regRDX, regRSI, regRDI, regR8, regR9}

	jitTagInt64 = uint64(uintptr(unsafe.Pointer(val.Int64Val(0).HeaderPtr())))
	jitTagBool  = uint64(uintptr(unsafe.Pointer(val.BoolVal(false).HeaderPtr())))
	jitTagNone  = uint64(uintptr(unsafe.Pointer(val.NoneVal().HeaderPtr())))
)

// Compile-time struct layout assertions for inline JIT offsets.
var (
	_ [16 - unsafe.Sizeof(val.Value{})]byte
	_ [unsafe.Sizeof(val.Value{}) - 16]byte
	_ [64 - unsafe.Sizeof(ICSlot{})]byte
	_ [unsafe.Sizeof(ICSlot{}) - 64]byte
)

const (
	offVMPinned  = int32(unsafe.Offsetof(VM{}.jitPinned))
	offVMRetVal  = int32(unsafe.Offsetof(VM{}.jitRetVal))
	offVMDirty   = int32(unsafe.Offsetof(VM{}.jitDirty))
	offVMRetKind = int32(unsafe.Offsetof(VM{}.jitRetKind))

	offCacheIC = int32(unsafe.Offsetof(VMCodeCache{}.ic))

	offICTypeVer = int32(unsafe.Offsetof(ICSlot{}.ExpectedTypeVer))
	offICShapeID = int32(unsafe.Offsetof(ICSlot{}.ExpectedShapeID))
	offICSlotIdx = int32(unsafe.Offsetof(ICSlot{}.SlotIndex))
	offICKind    = int32(unsafe.Offsetof(ICSlot{}.Kind))

	offHdrKind = int32(unsafe.Offsetof(val.ObjectHeader{}.Kind))
	offHdrType = int32(unsafe.Offsetof(val.ObjectHeader{}.Type))

	offInstShape = int32(unsafe.Offsetof(val.PyInstance{}.Shape))
	offInstSlots = int32(unsafe.Offsetof(val.PyInstance{}.Slots))

	offShapeID = int32(unsafe.Offsetof(val.Shape{}.ID))

	offTypeVer   = int32(unsafe.Offsetof(val.PyType{}.VersionTag))
	offTypeFlags = int32(unsafe.Offsetof(val.PyType{}.Flags))

	// Local stack slots below saved registers in JIT frame.
	offFrameGlobals = int32(0)
	offFrameCache   = int32(8)
	offFrameTick    = int32(16)
	offFrameTmp     = int32(24)
	offFrameTmp2    = int32(32)
	jitFrameBytes   = int32(48)
)

type jitBuffer struct {
	mem    []byte
	closed atomic.Bool
}

func (b *jitBuffer) release() {
	if b == nil || !b.closed.CompareAndSwap(false, true) {
		return
	}
	if len(b.mem) > 0 {
		_ = syscall.Munmap(b.mem)
		b.mem = nil
	}
}

type jitFuncVal struct {
	fn uintptr
}

type jitTrampolineFn func(vmPtr, regsPtr, constsPtr, globalsPtr, cachePtr uintptr) (uint64, bool)

type jitCode struct {
	buf        *jitBuffer
	entry      uintptr
	fv         *jitFuncVal
	trampoline jitTrampolineFn
	pinRegs    [jitMaxPinRegs]uint8
	pinCount   uint8
}

func (jc *jitCode) release() {
	if jc == nil || jc.buf == nil {
		return
	}
	jc.buf.release()
}

type asmFixup struct {
	pos      int
	targetPC int
}

type asm64 struct {
	code     []byte
	pcOffs   []int
	fixups   []asmFixup
	bailOffs []int
	bailFix  []asmFixup
}

func (a *asm64) emitByte(b byte) {
	a.code = append(a.code, b)
}

func (a *asm64) emitU32(v uint32) {
	a.code = append(a.code, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}

func (a *asm64) emitU64(v uint64) {
	a.emitU32(uint32(v))
	a.emitU32(uint32(v >> 32))
}

func (a *asm64) emitREX(w, r, x, b bool) {
	v := byte(0x40)
	if w {
		v |= 0x08
	}
	if r {
		v |= 0x04
	}
	if x {
		v |= 0x02
	}
	if b {
		v |= 0x01
	}
	a.emitByte(v)
}

func (a *asm64) emitModRMDisp(reg, base uint8, disp int32) {
	needSIB := (base & 7) == 4
	switch {
	case disp == 0 && (base&7) != 5:
		a.emitByte(((reg & 7) << 3) | (base & 7))
		if needSIB {
			a.emitByte(0x24)
		}
	case disp >= -128 && disp <= 127:
		a.emitByte((1 << 6) | ((reg & 7) << 3) | (base & 7))
		if needSIB {
			a.emitByte(0x24)
		}
		a.emitByte(byte(int8(disp)))
	default:
		a.emitByte((2 << 6) | ((reg & 7) << 3) | (base & 7))
		if needSIB {
			a.emitByte(0x24)
		}
		a.emitU32(uint32(disp))
	}
}

func (a *asm64) pushReg(r uint8) {
	if r >= 8 {
		a.emitByte(0x41)
	}
	a.emitByte(0x50 + (r & 7))
}

func (a *asm64) popReg(r uint8) {
	if r >= 8 {
		a.emitByte(0x41)
	}
	a.emitByte(0x58 + (r & 7))
}

func (a *asm64) movRR(dst, src uint8) {
	if dst == src {
		return
	}
	a.emitREX(true, src >= 8, false, dst >= 8)
	a.emitByte(0x89)
	a.emitByte(0xC0 | ((src & 7) << 3) | (dst & 7))
}

func (a *asm64) movImm64(dst uint8, imm uint64) {
	if imm == 0 {
		a.xorRR(dst, dst)
		return
	}
	if imm <= 0x7FFFFFFF {
		if dst >= 8 {
			a.emitByte(0x41)
		}
		a.emitByte(0xB8 + (dst & 7))
		a.emitU32(uint32(imm))
		return
	}
	a.emitREX(true, false, false, dst >= 8)
	a.emitByte(0xB8 + (dst & 7))
	a.emitU64(imm)
}

func (a *asm64) movLoad64(dst, base uint8, disp int32) {
	a.emitREX(true, dst >= 8, false, base >= 8)
	a.emitByte(0x8B)
	a.emitModRMDisp(dst, base, disp)
}

func (a *asm64) movStore64(base uint8, disp int32, src uint8) {
	a.emitREX(true, src >= 8, false, base >= 8)
	a.emitByte(0x89)
	a.emitModRMDisp(src, base, disp)
}

func (a *asm64) movLoad32Z(dst, base uint8, disp int32) {
	if dst >= 8 || base >= 8 {
		a.emitREX(false, dst >= 8, false, base >= 8)
	}
	a.emitByte(0x8B)
	a.emitModRMDisp(dst, base, disp)
}

func (a *asm64) movLoad32SX(dst, base uint8, disp int32) {
	a.emitREX(true, dst >= 8, false, base >= 8)
	a.emitByte(0x63)
	a.emitModRMDisp(dst, base, disp)
}

func (a *asm64) movLoad8Z(dst, base uint8, disp int32) {
	a.emitREX(true, dst >= 8, false, base >= 8)
	a.emitByte(0x0F)
	a.emitByte(0xB6)
	a.emitModRMDisp(dst, base, disp)
}

func (a *asm64) movStore8(base uint8, disp int32, src uint8) {
	if src >= 4 || base >= 8 {
		a.emitREX(false, src >= 8, false, base >= 8)
	}
	a.emitByte(0x88)
	a.emitModRMDisp(src, base, disp)
}

func (a *asm64) addRR(dst, src uint8) {
	a.emitREX(true, src >= 8, false, dst >= 8)
	a.emitByte(0x01)
	a.emitByte(0xC0 | ((src & 7) << 3) | (dst & 7))
}

func (a *asm64) subRR(dst, src uint8) {
	a.emitREX(true, src >= 8, false, dst >= 8)
	a.emitByte(0x29)
	a.emitByte(0xC0 | ((src & 7) << 3) | (dst & 7))
}

func (a *asm64) imulRR(dst, src uint8) {
	a.emitREX(true, dst >= 8, false, src >= 8)
	a.emitByte(0x0F)
	a.emitByte(0xAF)
	a.emitByte(0xC0 | ((dst & 7) << 3) | (src & 7))
}

func (a *asm64) xorRR(dst, src uint8) {
	if dst == src && dst < 8 {
		a.emitByte(0x31)
		a.emitByte(0xC0 | ((src & 7) << 3) | (dst & 7))
		return
	}
	if dst == src {
		a.emitREX(false, src >= 8, false, dst >= 8)
		a.emitByte(0x31)
		a.emitByte(0xC0 | ((src & 7) << 3) | (dst & 7))
		return
	}
	a.emitREX(true, src >= 8, false, dst >= 8)
	a.emitByte(0x31)
	a.emitByte(0xC0 | ((src & 7) << 3) | (dst & 7))
}

func (a *asm64) addMem32Imm(base uint8, disp int32, imm int8) {
	if base >= 8 {
		a.emitByte(0x41)
	}
	a.emitByte(0x83)
	a.emitModRMDisp(0, base, disp)
	a.emitByte(byte(imm))
}

func (a *asm64) cmpRR(lhs, rhs uint8) {
	a.emitREX(true, rhs >= 8, false, lhs >= 8)
	a.emitByte(0x39)
	a.emitByte(0xC0 | ((rhs & 7) << 3) | (lhs & 7))
}

func (a *asm64) cmpImm8(r uint8, imm int8) {
	a.emitREX(true, false, false, r >= 8)
	a.emitByte(0x83)
	a.emitByte(0xF8 | (r & 7))
	a.emitByte(byte(imm))
}

func (a *asm64) testRR(x, y uint8) {
	a.emitREX(true, y >= 8, false, x >= 8)
	a.emitByte(0x85)
	a.emitByte(0xC0 | ((y & 7) << 3) | (x & 7))
}

func (a *asm64) testImm32(r uint8, imm uint32) {
	a.emitREX(true, false, false, r >= 8)
	a.emitByte(0xF7)
	a.emitByte(0xC0 | (r & 7))
	a.emitU32(imm)
}

func (a *asm64) btImm(r, bit uint8) {
	a.emitREX(true, false, false, r >= 8)
	a.emitByte(0x0F)
	a.emitByte(0xBA)
	a.emitByte(0xE0 | (r & 7))
	a.emitByte(bit)
}

func (a *asm64) btsImm(r, bit uint8) {
	a.emitREX(true, false, false, r >= 8)
	a.emitByte(0x0F)
	a.emitByte(0xBA)
	a.emitByte(0xE8 | (r & 7))
	a.emitByte(bit)
}

func (a *asm64) btrImm(r, bit uint8) {
	a.emitREX(true, false, false, r >= 8)
	a.emitByte(0x0F)
	a.emitByte(0xBA)
	a.emitByte(0xF0 | (r & 7))
	a.emitByte(bit)
}

func (a *asm64) setCC(cc, dst uint8) {
	if dst >= 4 {
		a.emitREX(false, false, false, dst >= 8)
	}
	a.emitByte(0x0F)
	a.emitByte(0x90 | cc)
	a.emitByte(0xC0 | (dst & 7))
	a.emitREX(true, dst >= 8, false, dst >= 8)
	a.emitByte(0x0F)
	a.emitByte(0xB6)
	a.emitByte(0xC0 | ((dst & 7) << 3) | (dst & 7))
}

func (a *asm64) shlImm(r, shift uint8) {
	a.emitREX(true, false, false, r >= 8)
	a.emitByte(0xC1)
	a.emitByte(0xE0 | (r & 7))
	a.emitByte(shift)
}

func (a *asm64) shrImm(r, shift uint8) {
	a.emitREX(true, false, false, r >= 8)
	a.emitByte(0xC1)
	a.emitByte(0xE8 | (r & 7))
	a.emitByte(shift)
}

func (a *asm64) subRSP(n int32) {
	a.emitREX(true, false, false, false)
	a.emitByte(0x83)
	a.emitByte(0xEC)
	a.emitByte(byte(int8(n)))
}

func (a *asm64) addRSP(n int32) {
	a.emitREX(true, false, false, false)
	a.emitByte(0x83)
	a.emitByte(0xC4)
	a.emitByte(byte(int8(n)))
}

func (a *asm64) jmpToPC(targetPC int) {
	a.emitByte(0xE9)
	a.fixups = append(a.fixups, asmFixup{pos: len(a.code), targetPC: targetPC})
	a.emitU32(0)
}

func (a *asm64) jccToPC(cc uint8, targetPC int) {
	a.emitByte(0x0F)
	a.emitByte(0x80 | cc)
	a.fixups = append(a.fixups, asmFixup{pos: len(a.code), targetPC: targetPC})
	a.emitU32(0)
}

func (a *asm64) jccToBail(cc uint8, pc int) {
	a.emitByte(0x0F)
	a.emitByte(0x80 | cc)
	a.bailFix = append(a.bailFix, asmFixup{pos: len(a.code), targetPC: pc})
	a.emitU32(0)
}

func (a *asm64) jmpToBail(pc int) {
	a.emitByte(0xE9)
	a.bailFix = append(a.bailFix, asmFixup{pos: len(a.code), targetPC: pc})
	a.emitU32(0)
}

func (a *asm64) jmpPlaceholder() int {
	a.emitByte(0xE9)
	pos := len(a.code)
	a.emitU32(0)
	return pos
}

func (a *asm64) jccPlaceholder(cc uint8) int {
	a.emitByte(0x0F)
	a.emitByte(0x80 | cc)
	pos := len(a.code)
	a.emitU32(0)
	return pos
}

func (a *asm64) patch32(pos int, target int) {
	rel := int32(target - (pos + 4))
	a.code[pos] = byte(rel)
	a.code[pos+1] = byte(rel >> 8)
	a.code[pos+2] = byte(rel >> 16)
	a.code[pos+3] = byte(rel >> 24)
}

// jitCompiler emits amd64 machine code for a single RegCode function.
type jitCompiler struct {
	rc       *RegCode
	consts   []val.Value
	a        asm64
	pinSlot  [jitMaxRegs]int8 // virtual reg -> [0..6] or -1
	pinRegs  [jitMaxPinRegs]uint8
	pinCount uint8

	bailOutFix []int
	retFix     []int
}

func jitSelectPins(rc *RegCode) ([jitMaxRegs]int8, [jitMaxPinRegs]uint8, uint8, bool) {
	var pinSlot [jitMaxRegs]int8
	for i := range pinSlot {
		pinSlot[i] = -1
	}
	var score [jitMaxRegs]int32
	hasHotOp := false
	for _, inst := range rc.Code {
		op := DecodeRegOp(inst)
		dst := int(DecodeRegDst(inst))
		src1 := int(DecodeRegSrc1(inst))
		src2 := int(DecodeRegSrc2(inst))
		switch op {
		case RegAdd, RegSub, RegMul:
			hasHotOp = true
			if dst < jitMaxRegs {
				score[dst] += 4
			}
			if src1 < jitMaxRegs {
				score[src1] += 3
			}
			if src2 < jitMaxRegs {
				score[src2] += 3
			}
		case RegEq, RegNe, RegLt, RegLe, RegGt, RegGe,
			RegLtJmpIfFalse, RegLtJmpIfTrue, RegEqJmpIfFalse, RegEqJmpIfTrue:
			hasHotOp = true
			if src1 < jitMaxRegs {
				score[src1] += 3
			}
			if src2 < jitMaxRegs {
				score[src2] += 3
			}
		case RegLoadAttr:
			hasHotOp = true
			if dst < jitMaxRegs {
				score[dst]++
			}
		case RegMov, RegStoreFastView:
			if src1 < jitMaxRegs {
				score[src1]++
			}
		}
	}
	if !hasHotOp {
		return pinSlot, [jitMaxPinRegs]uint8{}, 0, false
	}
	var pinRegs [jitMaxPinRegs]uint8
	var count uint8
	n := min(int(rc.NumRegs), jitMaxRegs)
	for count < jitMaxPinRegs {
		bestReg := -1
		bestScore := int32(0)
		for r := range n {
			if pinSlot[r] >= 0 || score[r] <= bestScore {
				continue
			}
			bestScore = score[r]
			bestReg = r
		}
		if bestReg < 0 {
			break
		}
		pinSlot[bestReg] = int8(count)
		pinRegs[count] = uint8(bestReg)
		count++
	}
	return pinSlot, pinRegs, count, true
}

// emitEnsureUnboxedInt64 guarantees the value of virtual register vr is loaded
// into a GPR as an unboxed int64, bailing out at pc if not KindInt64.
// Uses R10 and R11 as scratch; targetGPR must be R10 or R11.
func (c *jitCompiler) emitEnsureUnboxedInt64(vr int, pc int, targetGPR uint8) uint8 {
	a := &c.a
	if slot := c.pinSlot[vr]; slot >= 0 {
		hw := jitHwRegs[slot]
		a.btImm(regR15, uint8(slot))
		skipPos := len(a.code)
		a.emitByte(0x0F)
		a.emitByte(0x80 | ccB) // JC (already unboxed in hw)
		a.emitU32(0)
		a.movLoad64(regR10, regR12, int32(vr*16+8))
		a.movImm64(regR11, jitTagInt64)
		a.cmpRR(regR10, regR11)
		a.jccToBail(ccNE, pc)
		a.movLoad64(hw, regR12, int32(vr*16))
		a.btsImm(regR15, uint8(slot))
		a.patch32(skipPos+2, len(a.code))
		return hw
	}
	scratch := regR11
	if targetGPR == regR11 {
		scratch = regR10
	}
	a.movLoad64(targetGPR, regR12, int32(vr*16+8))
	a.movImm64(scratch, jitTagInt64)
	a.cmpRR(targetGPR, scratch)
	a.jccToBail(ccNE, pc)
	a.movLoad64(targetGPR, regR12, int32(vr*16))
	return targetGPR
}

// emitCheckRegNonHeapPtr checks that regs[vr].ptr is nil or a static non-heap tag
// (jitTagInt64, jitTagBool, jitTagNone) before an unpinned store into regs[vr].
// Clobbers R10 and R11.
func (c *jitCompiler) emitCheckRegNonHeapPtr(vr int, pc int) {
	a := &c.a
	a.movLoad64(regR10, regR12, int32(vr*16+8))
	a.testRR(regR10, regR10)
	ok1 := len(a.code)
	a.emitByte(0x0F)
	a.emitByte(0x80 | ccE)
	a.emitU32(0)
	a.movImm64(regR11, jitTagInt64)
	a.cmpRR(regR10, regR11)
	ok2 := len(a.code)
	a.emitByte(0x0F)
	a.emitByte(0x80 | ccE)
	a.emitU32(0)
	a.movImm64(regR11, jitTagBool)
	a.cmpRR(regR10, regR11)
	ok3 := len(a.code)
	a.emitByte(0x0F)
	a.emitByte(0x80 | ccE)
	a.emitU32(0)
	a.movImm64(regR11, jitTagNone)
	a.cmpRR(regR10, regR11)
	a.jccToBail(ccNE, pc)
	target := len(a.code)
	a.patch32(ok1+2, target)
	a.patch32(ok2+2, target)
	a.patch32(ok3+2, target)
}

// emitStoreInt64 stores an int64 in valGPR (must not be R11; may be R10) into vr.
func (c *jitCompiler) emitStoreInt64(vr int, valGPR uint8, pc int) {
	a := &c.a
	if slot := c.pinSlot[vr]; slot >= 0 {
		hw := jitHwRegs[slot]
		a.movRR(hw, valGPR)
		a.btsImm(regR15, uint8(slot))
		a.btsImm(regR15, uint8(slot+8))
		return
	}
	a.movStore64(regRSP, offFrameTmp, valGPR)
	c.emitCheckRegNonHeapPtr(vr, pc)
	a.movLoad64(regR10, regRSP, offFrameTmp)
	a.movStore64(regR12, int32(vr*16), regR10)
	a.movImm64(regR11, jitTagInt64)
	a.movStore64(regR12, int32(vr*16+8), regR11)
}

// emitStoreStaticVal stores (valNumGPR, tagGPR) into vr after verifying oldPtr is non-heap.
func (c *jitCompiler) emitStoreStaticVal(vr int, valNumGPR, tagGPR uint8, pc int) {
	a := &c.a
	a.movStore64(regRSP, offFrameTmp, valNumGPR)
	a.movStore64(regRSP, offFrameTmp2, tagGPR)
	c.emitCheckRegNonHeapPtr(vr, pc)
	if slot := c.pinSlot[vr]; slot >= 0 {
		a.btrImm(regR15, uint8(slot))
		a.btrImm(regR15, uint8(slot+8))
	}
	a.movLoad64(regR10, regRSP, offFrameTmp)
	a.movLoad64(regR11, regRSP, offFrameTmp2)
	a.movStore64(regR12, int32(vr*16), regR10)
	a.movStore64(regR12, int32(vr*16+8), regR11)
}

// emitCheckTagIsStatic jumps to bail if tagReg is not jitTagInt64, jitTagBool, or jitTagNone.
// Clobbers scratchReg (must differ from tagReg).
func (c *jitCompiler) emitCheckTagIsStatic(tagReg, scratchReg uint8, pc int) {
	a := &c.a
	a.movImm64(scratchReg, jitTagInt64)
	a.cmpRR(tagReg, scratchReg)
	ok1 := len(a.code)
	a.emitByte(0x0F)
	a.emitByte(0x80 | ccE)
	a.emitU32(0)
	a.movImm64(scratchReg, jitTagBool)
	a.cmpRR(tagReg, scratchReg)
	ok2 := len(a.code)
	a.emitByte(0x0F)
	a.emitByte(0x80 | ccE)
	a.emitU32(0)
	a.movImm64(scratchReg, jitTagNone)
	a.cmpRR(tagReg, scratchReg)
	a.jccToBail(ccNE, pc)
	target := len(a.code)
	a.patch32(ok1+2, target)
	a.patch32(ok2+2, target)
}

func (c *jitCompiler) emitBackwardTickCheck(pc int) {
	a := &c.a
	a.addMem32Imm(regRSP, offFrameTick, 1)
	a.movLoad32Z(regR10, regRSP, offFrameTick)
	a.testImm32(regR10, 0xFF)
	a.jccToBail(ccE, pc)
}

func (c *jitCompiler) emitBranch(pc, targetPC int) {
	if targetPC <= pc {
		c.emitBackwardTickCheck(pc)
	}
	c.a.jmpToPC(targetPC)
}

func (c *jitCompiler) emitCondBranch(cc uint8, pc, targetPC int) {
	a := &c.a
	if targetPC > pc {
		a.jccToPC(cc, targetPC)
		return
	}
	invCC := cc ^ 1
	skipPos := len(a.code)
	a.emitByte(0x0F)
	a.emitByte(0x80 | invCC)
	a.emitU32(0)
	c.emitBackwardTickCheck(pc)
	a.jmpToPC(targetPC)
	a.patch32(skipPos+2, len(a.code))
}

func (c *jitCompiler) emitMov(dst, src1, pc int) {
	if dst == src1 {
		return
	}
	a := &c.a
	doneFix := -1
	if slot := c.pinSlot[src1]; slot >= 0 {
		a.btImm(regR15, uint8(slot))
		fromMemFix := a.jccPlaceholder(ccAE) // JNC (not unboxed -> read from memory)
		c.emitStoreInt64(dst, jitHwRegs[slot], pc)
		doneFix = a.jmpPlaceholder()
		a.patch32(fromMemFix, len(a.code))
	}
	a.movLoad64(regR10, regR12, int32(src1*16))
	a.movLoad64(regR11, regR12, int32(src1*16+8))
	if dstSlot := c.pinSlot[dst]; dstSlot >= 0 {
		a.movStore64(regRSP, offFrameTmp, regR10)
		a.movImm64(regR10, jitTagInt64)
		a.cmpRR(regR11, regR10)
		notIntFix := a.jccPlaceholder(ccNE)
		a.movLoad64(jitHwRegs[dstSlot], regRSP, offFrameTmp)
		a.btsImm(regR15, uint8(dstSlot))
		a.btsImm(regR15, uint8(dstSlot+8))
		intDoneFix := a.jmpPlaceholder()
		a.patch32(notIntFix, len(a.code))
		a.movLoad64(regR10, regRSP, offFrameTmp)
		c.emitCheckTagIsStatic(regR11, regR10, pc)
		a.movLoad64(regR10, regRSP, offFrameTmp)
		c.emitStoreStaticVal(dst, regR10, regR11, pc)
		a.patch32(intDoneFix, len(a.code))
	} else {
		a.movStore64(regRSP, offFrameTmp, regR10)
		c.emitCheckTagIsStatic(regR11, regR10, pc)
		a.movLoad64(regR10, regRSP, offFrameTmp)
		c.emitStoreStaticVal(dst, regR10, regR11, pc)
	}
	if doneFix >= 0 {
		a.patch32(doneFix, len(a.code))
	}
}

func (c *jitCompiler) emitLoadConst(dst, idx, pc int) {
	a := &c.a
	if idx < 0 || idx >= len(c.consts) {
		a.jmpToBail(pc)
		return
	}
	cv := c.consts[idx]
	switch {
	case cv.IsInt64():
		a.movImm64(regR10, uint64(cv.AsInt64()))
		c.emitStoreInt64(dst, regR10, pc)
	case cv.IsBool():
		var b uint64
		if cv.AsBool() {
			b = 1
		}
		a.movImm64(regR10, b)
		a.movImm64(regR11, jitTagBool)
		c.emitStoreStaticVal(dst, regR10, regR11, pc)
	case cv.IsNone():
		a.xorRR(regR10, regR10)
		a.movImm64(regR11, jitTagNone)
		c.emitStoreStaticVal(dst, regR10, regR11, pc)
	default:
		a.jmpToBail(pc)
	}
}

func (c *jitCompiler) loadPairInt64(src1, src2, pc int) (uint8, uint8) {
	a := &c.a
	s1 := c.emitEnsureUnboxedInt64(src1, pc, regR10)
	savedS1 := false
	if c.pinSlot[src1] < 0 {
		a.movStore64(regRSP, offFrameTmp, s1)
		savedS1 = true
	}
	s2 := c.emitEnsureUnboxedInt64(src2, pc, regR11)
	if savedS1 {
		a.movLoad64(regR10, regRSP, offFrameTmp)
		s1 = regR10
	}
	return s1, s2
}

func (c *jitCompiler) emitArith(op RegOp, dst, src1, src2, pc int) {
	a := &c.a
	s1, s2 := c.loadPairInt64(src1, src2, pc)
	a.movRR(regR10, s1)
	switch op {
	case RegAdd:
		a.addRR(regR10, s2)
	case RegSub:
		a.subRR(regR10, s2)
	case RegMul:
		a.imulRR(regR10, s2)
	}
	a.jccToBail(ccO, pc)
	c.emitStoreInt64(dst, regR10, pc)
}

func (c *jitCompiler) emitCmpBool(op RegOp, dst, src1, src2, pc int) {
	a := &c.a
	s1, s2 := c.loadPairInt64(src1, src2, pc)
	a.cmpRR(s1, s2)
	var cc uint8
	switch op {
	case RegEq:
		cc = ccE
	case RegNe:
		cc = ccNE
	case RegLt:
		cc = ccL
	case RegLe:
		cc = ccLE
	case RegGt:
		cc = ccG
	case RegGe:
		cc = ccGE
	}
	a.setCC(cc, regR10)
	a.movImm64(regR11, jitTagBool)
	c.emitStoreStaticVal(dst, regR10, regR11, pc)
}

func (c *jitCompiler) emitCmpJmp(op RegOp, src1, src2, pc, targetPC int) {
	a := &c.a
	s1, s2 := c.loadPairInt64(src1, src2, pc)
	a.cmpRR(s1, s2)
	var cc uint8
	switch op {
	case RegLtJmpIfFalse:
		cc = ccGE
	case RegLtJmpIfTrue:
		cc = ccL
	case RegEqJmpIfFalse:
		cc = ccNE
	case RegEqJmpIfTrue:
		cc = ccE
	}
	c.emitCondBranch(cc, pc, targetPC)
}

func (c *jitCompiler) emitJmpIfBool(wantTrue bool, src1, pc, targetPC int) {
	a := &c.a
	if slot := c.pinSlot[src1]; slot >= 0 {
		a.btImm(regR15, uint8(slot))
		memCheckFix := a.jccPlaceholder(ccAE) // JNC -> memory check
		hw := jitHwRegs[slot]
		a.testRR(hw, hw)
		if wantTrue {
			c.emitCondBranch(ccNE, pc, targetPC)
		} else {
			c.emitCondBranch(ccE, pc, targetPC)
		}
		fallFix := a.jmpPlaceholder()
		a.patch32(memCheckFix, len(a.code))
		c.emitJmpIfBoolMem(wantTrue, src1, pc, targetPC)
		a.patch32(fallFix, len(a.code))
		return
	}
	c.emitJmpIfBoolMem(wantTrue, src1, pc, targetPC)
}

func (c *jitCompiler) emitJmpIfBoolMem(wantTrue bool, src1, pc, targetPC int) {
	a := &c.a
	a.movLoad64(regR10, regR12, int32(src1*16+8))
	a.movImm64(regR11, jitTagInt64)
	a.cmpRR(regR10, regR11)
	isValFix := a.jccPlaceholder(ccE)
	a.movImm64(regR11, jitTagBool)
	a.cmpRR(regR10, regR11)
	isBoolFix := a.jccPlaceholder(ccE)
	a.movImm64(regR11, jitTagNone)
	a.cmpRR(regR10, regR11)
	a.jccToBail(ccNE, pc)
	// None is falsy.
	if !wantTrue {
		c.emitBranch(pc, targetPC)
	}
	noneDoneFix := a.jmpPlaceholder()

	valStart := len(a.code)
	a.patch32(isValFix, valStart)
	a.patch32(isBoolFix, valStart)
	a.movLoad64(regR10, regR12, int32(src1*16))
	a.testRR(regR10, regR10)
	if wantTrue {
		c.emitCondBranch(ccNE, pc, targetPC)
	} else {
		c.emitCondBranch(ccE, pc, targetPC)
	}
	a.patch32(noneDoneFix, len(a.code))
}

func (c *jitCompiler) emitNot(dst, src1, pc int) {
	a := &c.a
	if slot := c.pinSlot[src1]; slot >= 0 {
		a.btImm(regR15, uint8(slot))
		memCheckFix := a.jccPlaceholder(ccAE)
		hw := jitHwRegs[slot]
		a.testRR(hw, hw)
		a.setCC(ccE, regR10)
		a.movImm64(regR11, jitTagBool)
		c.emitStoreStaticVal(dst, regR10, regR11, pc)
		doneFix := a.jmpPlaceholder()
		a.patch32(memCheckFix, len(a.code))
		c.emitNotMem(dst, src1, pc)
		a.patch32(doneFix, len(a.code))
		return
	}
	c.emitNotMem(dst, src1, pc)
}

func (c *jitCompiler) emitNotMem(dst, src1, pc int) {
	a := &c.a
	a.movLoad64(regR10, regR12, int32(src1*16+8))
	a.movImm64(regR11, jitTagInt64)
	a.cmpRR(regR10, regR11)
	isValFix := a.jccPlaceholder(ccE)
	a.movImm64(regR11, jitTagBool)
	a.cmpRR(regR10, regR11)
	isBoolFix := a.jccPlaceholder(ccE)
	a.movImm64(regR11, jitTagNone)
	a.cmpRR(regR10, regR11)
	a.jccToBail(ccNE, pc)
	// not None -> True
	a.movImm64(regR10, 1)
	a.movImm64(regR11, jitTagBool)
	c.emitStoreStaticVal(dst, regR10, regR11, pc)
	doneFix := a.jmpPlaceholder()

	valStart := len(a.code)
	a.patch32(isValFix, valStart)
	a.patch32(isBoolFix, valStart)
	a.movLoad64(regR10, regR12, int32(src1*16))
	a.testRR(regR10, regR10)
	a.setCC(ccE, regR10)
	a.movImm64(regR11, jitTagBool)
	c.emitStoreStaticVal(dst, regR10, regR11, pc)
	a.patch32(doneFix, len(a.code))
}

func (c *jitCompiler) emitIsNoneJmp(wantTrue bool, src1, pc, targetPC int) {
	a := &c.a
	if slot := c.pinSlot[src1]; slot >= 0 {
		a.btImm(regR15, uint8(slot))
		memCheckFix := a.jccPlaceholder(ccAE)
		// Unboxed int64 is never None.
		if !wantTrue {
			c.emitBranch(pc, targetPC)
		}
		doneFix := a.jmpPlaceholder()
		a.patch32(memCheckFix, len(a.code))
		a.movLoad64(regR10, regR12, int32(src1*16+8))
		a.movImm64(regR11, jitTagNone)
		a.cmpRR(regR10, regR11)
		if wantTrue {
			c.emitCondBranch(ccE, pc, targetPC)
		} else {
			c.emitCondBranch(ccNE, pc, targetPC)
		}
		a.patch32(doneFix, len(a.code))
		return
	}
	a.movLoad64(regR10, regR12, int32(src1*16+8))
	a.movImm64(regR11, jitTagNone)
	a.cmpRR(regR10, regR11)
	if wantTrue {
		c.emitCondBranch(ccE, pc, targetPC)
	} else {
		c.emitCondBranch(ccNE, pc, targetPC)
	}
}

func (c *jitCompiler) emitLoadAttr(dst, sip, pc int) {
	a := &c.a
	slot := c.pinSlot[dst]
	if slot < 0 || sip < 0 {
		a.jmpToBail(pc)
		return
	}
	// If dst currently holds an unboxed int64, it cannot be an instance.
	a.btImm(regR15, uint8(slot))
	a.jccToBail(ccB, pc)

	// R10 = obj.ptr; must be non-nil KindInstance.
	a.movLoad64(regR10, regR12, int32(dst*16+8))
	a.testRR(regR10, regR10)
	a.jccToBail(ccE, pc)
	a.movLoad8Z(regR11, regR10, offHdrKind)
	a.cmpImm8(regR11, int8(val.KindInstance))
	a.jccToBail(ccNE, pc)

	// Save inst pointer in [RSP+offFrameTmp].
	a.movStore64(regRSP, offFrameTmp, regR10)

	// Check inst.Shape != nil and inst.Header.Type != nil.
	a.movLoad64(regR11, regR10, offInstShape)
	a.testRR(regR11, regR11)
	a.jccToBail(ccE, pc)
	a.movLoad64(regR10, regR10, offHdrType)
	a.testRR(regR10, regR10)
	a.jccToBail(ccE, pc)

	// Check (tp.Flags & FlagCustomGetAttribute) == 0.
	a.movLoad32Z(regR11, regR10, offTypeFlags)
	a.testImm32(regR11, val.FlagCustomGetAttribute)
	a.jccToBail(ccNE, pc)

	// Save tp.VersionTag into [RSP+offFrameTmp2].
	a.movLoad64(regR11, regR10, offTypeVer)
	a.movStore64(regRSP, offFrameTmp2, regR11)

	// Check sip < len(cache.ic).
	a.movLoad64(regR10, regRSP, offFrameCache)
	a.movLoad64(regR11, regR10, offCacheIC+8)
	a.movImm64(regR10, uint64(sip))
	a.cmpRR(regR10, regR11)
	a.jccToBail(ccAE, pc)

	// R10 = &cache.ic[sip] (ICSlot is 64 bytes).
	a.movLoad64(regR10, regRSP, offFrameCache)
	a.movLoad64(regR10, regR10, offCacheIC)
	a.movImm64(regR11, uint64(sip)*64)
	a.addRR(regR10, regR11)

	// Check entry.Kind == ICKindSlotMember.
	a.movLoad8Z(regR11, regR10, offICKind)
	a.cmpImm8(regR11, int8(ICKindSlotMember))
	a.jccToBail(ccNE, pc)

	// Check entry.ExpectedTypeVer == tp.VersionTag.
	a.movLoad64(regR11, regRSP, offFrameTmp2)
	a.pushReg(regR10)
	a.movLoad64(regR10, regR10, offICTypeVer)
	a.cmpRR(regR10, regR11)
	a.popReg(regR10)
	a.jccToBail(ccNE, pc)

	// Check entry.ExpectedShapeID == 0 || shape.ID == entry.ExpectedShapeID.
	a.movLoad32Z(regR11, regR10, offICShapeID)
	a.testRR(regR11, regR11)
	skipShapeFix := len(a.code)
	a.emitByte(0x0F)
	a.emitByte(0x80 | ccE)
	a.emitU32(0)
	a.pushReg(regR10)
	a.movLoad64(regR10, regRSP, offFrameTmp+8) // +8 because we pushed R10!
	a.movLoad64(regR10, regR10, offInstShape)
	a.movLoad32Z(regR10, regR10, offShapeID)
	a.cmpRR(regR10, regR11)
	a.popReg(regR10)
	a.jccToBail(ccNE, pc)
	a.patch32(skipShapeFix+2, len(a.code))

	// Load slotIdx = entry.SlotIndex (int32 sign-extended), reload inst into R10.
	a.movLoad32SX(regR11, regR10, offICSlotIdx)
	a.movLoad64(regR10, regRSP, offFrameTmp)
	// Check uint64(slotIdx) < uint64(len(inst.Slots)).
	a.pushReg(regR10)
	a.movLoad64(regR10, regR10, offInstSlots+8)
	a.cmpRR(regR11, regR10)
	a.popReg(regR10)
	a.jccToBail(ccAE, pc)

	// Load inst.Slots[slotIdx]: R10 = Slots.Data + slotIdx*16.
	a.movLoad64(regR10, regR10, offInstSlots)
	a.shlImm(regR11, 4)
	a.addRR(regR10, regR11)

	// Check v.ptr == jitTagInt64 (also guarantees != 0 and != jitTagUnbound).
	a.movLoad64(regR11, regR10, 8)
	a.pushReg(regR10)
	a.movImm64(regR10, jitTagInt64)
	a.cmpRR(regR11, regR10)
	a.popReg(regR10)
	a.jccToBail(ccNE, pc)

	// Load v.num into hwRegs[slot] and mark unboxed + dirty (regs[dst] untouched until Go flush!).
	a.movLoad64(jitHwRegs[slot], regR10, 0)
	a.btsImm(regR15, uint8(slot))
	a.btsImm(regR15, uint8(slot+8))
}

func (c *jitCompiler) emitReturn(src1, pc int) {
	a := &c.a
	if len(c.rc.RetLive) > 0 {
		a.jmpToBail(pc)
		return
	}
	if slot := c.pinSlot[src1]; slot >= 0 {
		a.btImm(regR15, uint8(slot))
		memFix := len(a.code)
		a.emitByte(0x0F)
		a.emitByte(0x80 | ccAE) // JNC -> memory check
		a.emitU32(0)
		a.movStore64(regR14, offVMRetVal, jitHwRegs[slot])
		a.movImm64(regR10, 1) // kind int64
		a.movStore8(regR14, offVMRetKind, regR10)
		a.emitByte(0xE9)
		c.retFix = append(c.retFix, len(a.code))
		a.emitU32(0)
		a.patch32(memFix+2, len(a.code))
	}
	a.movLoad64(regR10, regR12, int32(src1*16))
	a.movLoad64(regR11, regR12, int32(src1*16+8))

	// Check jitTagInt64
	a.pushReg(regR10)
	a.movImm64(regR10, jitTagInt64)
	a.cmpRR(regR11, regR10)
	a.popReg(regR10)
	notIntFix := len(a.code)
	a.emitByte(0x0F)
	a.emitByte(0x80 | ccNE)
	a.emitU32(0)
	a.movStore64(regR14, offVMRetVal, regR10)
	a.movImm64(regR10, 1)
	a.movStore8(regR14, offVMRetKind, regR10)
	a.emitByte(0xE9)
	c.retFix = append(c.retFix, len(a.code))
	a.emitU32(0)
	a.patch32(notIntFix+2, len(a.code))

	// Check jitTagBool
	a.pushReg(regR10)
	a.movImm64(regR10, jitTagBool)
	a.cmpRR(regR11, regR10)
	a.popReg(regR10)
	notBoolFix := len(a.code)
	a.emitByte(0x0F)
	a.emitByte(0x80 | ccNE)
	a.emitU32(0)
	a.movStore64(regR14, offVMRetVal, regR10)
	a.movImm64(regR10, 2)
	a.movStore8(regR14, offVMRetKind, regR10)
	a.emitByte(0xE9)
	c.retFix = append(c.retFix, len(a.code))
	a.emitU32(0)
	a.patch32(notBoolFix+2, len(a.code))

	// Check jitTagNone
	a.movImm64(regR10, jitTagNone)
	a.cmpRR(regR11, regR10)
	a.jccToBail(ccNE, pc)
	a.xorRR(regR10, regR10)
	a.movStore8(regR14, offVMRetKind, regR10)
	a.emitByte(0xE9)
	c.retFix = append(c.retFix, len(a.code))
	a.emitU32(0)
}

func jitCompileRegCode(e *Engine, rc *RegCode, consts []val.Value) *jitCode {
	if rc == nil || len(rc.Code) == 0 || rc.NumRegs > jitMaxRegs || val.MultiThreadedFlag.Load() {
		return nil
	}
	pinSlot, pinRegs, pinCount, ok := jitSelectPins(rc)
	if !ok {
		return nil
	}
	c := &jitCompiler{
		rc:       rc,
		consts:   consts,
		pinSlot:  pinSlot,
		pinRegs:  pinRegs,
		pinCount: pinCount,
	}
	a := &c.a
	a.pcOffs = make([]int, len(rc.Code))
	a.bailOffs = make([]int, len(rc.Code)+1)

	// Go ABIInternal prologue:
	// args: RAX=vmPtr, RBX=regsPtr, RCX=constsPtr, RDI=globalsPtr, RSI=cachePtr
	a.pushReg(regRBP)
	a.movRR(regRBP, regRSP)
	a.pushReg(regRBX)
	a.pushReg(regR12)
	a.pushReg(regR13)
	a.pushReg(regR14)
	a.pushReg(regR15)
	a.subRSP(jitFrameBytes)

	a.movRR(regR14, regRAX) // vmPtr -> R14
	a.movRR(regR12, regRBX) // regsPtr -> R12
	a.movRR(regR13, regRCX) // constsPtr -> R13
	a.movStore64(regRSP, offFrameGlobals, regRDI)
	a.movStore64(regRSP, offFrameCache, regRSI)
	a.xorRR(regR10, regR10)
	a.movStore64(regRSP, offFrameTick, regR10)
	a.xorRR(regR15, regR15) // unboxedMask = 0, dirtyMask = 0

	for pc, inst := range rc.Code {
		a.pcOffs[pc] = len(a.code)
		op := DecodeRegOp(inst)
		dst := int(DecodeRegDst(inst))
		src1 := int(DecodeRegSrc1(inst))
		src2 := int(DecodeRegSrc2(inst))
		aux := DecodeRegAux(inst)

		switch op {
		case RegMov:
			c.emitMov(dst, src1, pc)

		case RegStoreFastView:
			c.emitMov(int(aux), src1, pc)

		case RegLoadConst:
			c.emitLoadConst(dst, int(aux), pc)

		case RegAdd, RegSub, RegMul:
			c.emitArith(op, dst, src1, src2, pc)

		case RegEq, RegNe, RegLt, RegLe, RegGt, RegGe:
			c.emitCmpBool(op, dst, src1, src2, pc)

		case RegNot:
			c.emitNot(dst, src1, pc)

		case RegJmp:
			c.emitBranch(pc, int(aux))

		case RegJmpIfFalse, RegJmpIfTrue:
			c.emitJmpIfBool(op == RegJmpIfTrue, src1, pc, int(aux))

		case RegLtJmpIfFalse, RegLtJmpIfTrue, RegEqJmpIfFalse, RegEqJmpIfTrue:
			c.emitCmpJmp(op, src1, src2, pc, int(aux))

		case RegIsNoneJmpIfFalse, RegIsNoneJmpIfTrue:
			c.emitIsNoneJmp(op == RegIsNoneJmpIfTrue, src1, pc, int(aux))

		case RegLoadAttr:
			c.emitLoadAttr(dst, int(rc.StackPC[pc]), pc)

		case RegReturn:
			c.emitReturn(src1, pc)

		default:
			a.jmpToBail(pc)
		}
	}

	// Fall-through bailout if execution reaches past the last instruction.
	a.jmpToBail(len(rc.Code))

	// Per-pc bailout stubs: set R10 = pc (without clobbering RAX!) and jump to bailout epilogue.
	for pc := 0; pc <= len(rc.Code); pc++ {
		a.bailOffs[pc] = len(a.code)
		a.movImm64(regR10, uint64(pc))
		a.emitByte(0xE9)
		c.bailOutFix = append(c.bailOutFix, len(a.code))
		a.emitU32(0)
	}

	// Shared bailout epilogue: flush all pinned hwRegs to vm.jitPinned,
	// store dirtyMask (R15[8:15]) to vm.jitDirty, return (RAX=pc, RBX=1).
	bailEpiloguePos := len(a.code)
	for _, pos := range c.bailOutFix {
		a.patch32(pos, bailEpiloguePos)
	}
	for k := range c.pinCount {
		a.movStore64(regR14, offVMPinned+int32(k)*8, jitHwRegs[k])
	}
	a.shrImm(regR15, 8)
	a.movStore8(regR14, offVMDirty, regR15)
	a.addRSP(jitFrameBytes)
	a.popReg(regR15)
	a.popReg(regR14)
	a.popReg(regR13)
	a.popReg(regR12)
	a.popReg(regRBX)
	a.popReg(regRBP)
	a.movRR(regRAX, regR10)
	a.movImm64(regRBX, 1)
	a.emitByte(0xC3) // RET

	// Shared return epilogue: return (RAX=0, RBX=0).
	retEpiloguePos := len(a.code)
	for _, pos := range c.retFix {
		a.patch32(pos, retEpiloguePos)
	}
	a.addRSP(jitFrameBytes)
	a.popReg(regR15)
	a.popReg(regR14)
	a.popReg(regR13)
	a.popReg(regR12)
	a.popReg(regRBX)
	a.popReg(regRBP)
	a.xorRR(regRAX, regRAX)
	a.xorRR(regRBX, regRBX)
	a.emitByte(0xC3) // RET

	// Patch all PC and bailout jumps.
	for _, fx := range a.fixups {
		if fx.targetPC >= 0 && fx.targetPC < len(a.pcOffs) {
			a.patch32(fx.pos, a.pcOffs[fx.targetPC])
		} else {
			a.patch32(fx.pos, a.bailOffs[len(rc.Code)])
		}
	}
	for _, fx := range a.bailFix {
		a.patch32(fx.pos, a.bailOffs[fx.targetPC])
	}

	pageSize := syscall.Getpagesize()
	allocSize := (len(a.code) + pageSize - 1) &^ (pageSize - 1)
	if allocSize == 0 {
		allocSize = pageSize
	}
	mem, err := syscall.Mmap(-1, 0, allocSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		return nil
	}
	copy(mem, a.code)
	if err := syscall.Mprotect(mem, syscall.PROT_READ|syscall.PROT_EXEC); err != nil {
		_ = syscall.Munmap(mem)
		return nil
	}

	entry := uintptr(unsafe.Pointer(unsafe.SliceData(mem)))
	fv := &jitFuncVal{fn: entry}
	trampoline := *(*jitTrampolineFn)(unsafe.Pointer(&fv))
	buf := &jitBuffer{mem: mem}
	jc := &jitCode{
		buf:        buf,
		entry:      entry,
		fv:         fv,
		trampoline: trampoline,
		pinRegs:    pinRegs,
		pinCount:   pinCount,
	}
	runtime.AddCleanup(jc, func(b *jitBuffer) {
		b.release()
	}, buf)
	if e != nil {
		e.jitMu.Lock()
		e.jitEntries = append(e.jitEntries, jitEntry{rc: rc, jc: jc})
		e.jitMu.Unlock()
	}
	return jc
}

func (rf *regFrame) runJIT(jc *jitCode) (val.Value, bool, bool) {
	vm := rf.vm
	vmPtr := uintptr(unsafe.Pointer(vm))
	regsPtr := uintptr(unsafe.Pointer(unsafe.SliceData(rf.regs)))
	var constsPtr uintptr
	if len(rf.consts) > 0 {
		constsPtr = uintptr(unsafe.Pointer(unsafe.SliceData(rf.consts)))
	}
	var globalsPtr uintptr
	if rf.st.globals != nil {
		globalsPtr = uintptr(unsafe.Pointer(rf.st.globals))
	}
	cachePtr := uintptr(unsafe.Pointer(rf.cache))

	pc, bailout := jc.trampoline(vmPtr, regsPtr, constsPtr, globalsPtr, cachePtr)

	if dirty := vm.jitDirty; dirty != 0 {
		vm.jitDirty = 0
		regs := rf.regs
		for dirty != 0 {
			k := bits.TrailingZeros8(dirty)
			dirty &^= 1 << k
			regs[jc.pinRegs[k]] = val.Int64Val(vm.jitPinned[k])
		}
	}

	if bailout {
		rf.pc = int(pc)
		return val.Value{}, false, true
	}

	switch vm.jitRetKind {
	case 1:
		return val.Int64Val(int64(vm.jitRetVal)), true, false
	case 2:
		return val.BoolVal(vm.jitRetVal != 0), true, false
	default:
		return val.NoneVal(), true, false
	}
}
