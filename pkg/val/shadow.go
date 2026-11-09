package val

import (
	"sync"
	"unsafe"
)

// Shadow PyObject arena — off-heap 32-byte stubs bridging C PyObject* layout
// to fython's 16-byte Value representation.
//
// Layout (spec §2.2):
//
//	struct PyObjectStub {
//	  ssize_t       ob_refcnt;   // 0
//	  PyTypeObject* ob_type;     // 8
//	  uint64_t      handle_idx;  // 16
//	  uint64_t      flags_data;  // 24
//	}
const (
	StubSize       = 32
	StubOffRefcnt  = 0
	StubOffType    = 8
	StubOffHandle  = 16
	StubOffFlags   = 24
	ImmortalRefcnt = int64(1) << 30
	TypeBlockSize  = 512
	// CustomPayloadPrefixSize is the off-heap prefix before a C instance's PyObject*.
	// Layout: [handle uint64][magic uint64][PyObject basicsize bytes].
	CustomPayloadPrefixSize = 16
	CustomPayloadMagic      = uint64(0x46595453313233) // "FyTS123"
)

type arenaPage struct {
	data []byte // keep mmap alive
	next int
}

type typePage struct {
	data []byte
	next int
}

// CDeallocHook is invoked when a C instance's off-heap stub is deallocated
// (refcount 0 from C) or when Go GC collects it (AddCleanup). capi sets this
// to bridge to the C tp_dealloc slot. val never imports capi (avoids cycle).
var CDeallocHook func(v Value)

// ShadowArena holds handle table + reverse identity maps + freelist.
type ShadowArena struct {
	mu            sync.Mutex
	pages         []arenaPage
	freeStubs     []unsafe.Pointer
	handleTable   []Value
	freeHandles   []uint64
	reverse       map[Value]unsafe.Pointer
	typeMap       map[*PyType]unsafe.Pointer
	typeReverse   map[unsafe.Pointer]*PyType
	typeNames     map[unsafe.Pointer][]byte
	strCache      map[unsafe.Pointer][]byte
	typePages     []typePage
	freeTypeBlk   []unsafe.Pointer
	immortalDone  bool
	immortalStubs map[Value]unsafe.Pointer
	// Custom (tp_basicsize > 16) allocation: side maps keyed by returned PyObject* base.
	customPages   []arenaPage
	customAlloc   map[unsafe.Pointer]int // base -> total size incl. prefix
	customHandles map[unsafe.Pointer]uint64
}

var (
	defaultArena     *ShadowArena
	defaultArenaOnce sync.Once
)

func GetDefaultArena() *ShadowArena {
	defaultArenaOnce.Do(func() {
		defaultArena = NewShadowArena()
	})
	return defaultArena
}

func NewShadowArena() *ShadowArena {
	a := &ShadowArena{
		handleTable:   make([]Value, 0, 1024),
		reverse:       make(map[Value]unsafe.Pointer),
		typeMap:       make(map[*PyType]unsafe.Pointer),
		typeReverse:   make(map[unsafe.Pointer]*PyType),
		typeNames:     make(map[unsafe.Pointer][]byte),
		strCache:      make(map[unsafe.Pointer][]byte),
		customAlloc:   make(map[unsafe.Pointer]int),
		customHandles: make(map[unsafe.Pointer]uint64),
	}
	return a
}

func stubRefcnt(p unsafe.Pointer) *int64 {
	return (*int64)(p)
}
func stubTypePtr(p unsafe.Pointer) *unsafe.Pointer {
	return (*unsafe.Pointer)(unsafe.Add(p, StubOffType))
}
func stubHandle(p unsafe.Pointer) *uint64 {
	return (*uint64)(unsafe.Add(p, StubOffHandle))
}
func stubFlags(p unsafe.Pointer) *uint64 {
	return (*uint64)(unsafe.Add(p, StubOffFlags))
}

// allocStubLocked pops or mmaps a new stub. Caller holds mu.
func (a *ShadowArena) allocStubLocked() unsafe.Pointer {
	if len(a.freeStubs) > 0 {
		p := a.freeStubs[len(a.freeStubs)-1]
		a.freeStubs = a.freeStubs[:len(a.freeStubs)-1]
		*stubRefcnt(p) = 1
		*stubTypePtr(p) = nil
		*stubHandle(p) = 0
		*stubFlags(p) = 0
		return p
	}
	if len(a.pages) == 0 || a.pages[len(a.pages)-1].next+StubSize > len(a.pages[len(a.pages)-1].data) {
		data, base, err := arenaMmap(64 * 1024)
		if err != nil {
			panic("val: shadow arena mmap failed: " + err.Error())
		}
		_ = base
		a.pages = append(a.pages, arenaPage{data: data, next: 0})
	}
	pg := &a.pages[len(a.pages)-1]
	p := unsafe.Pointer(&pg.data[pg.next])
	pg.next += StubSize
	*stubRefcnt(p) = 1
	*stubTypePtr(p) = nil
	*stubHandle(p) = 0
	*stubFlags(p) = 0
	return p
}

// needsCustomLocked reports whether v's type requires a tp_basicsize payload stub (prefix).
func needsCustomLocked(v Value) bool {
	tp := v.Type()
	if tp == nil {
		return false
	}
	return effectiveBasicsize(tp) > 16
}

// allocCustomLocked allocates a variable-size payload stub for tp_basicsize > 16.
// Layout: [16B prefix: handle u64][magic u64][PyObject basicsize bytes].
// Caller holds mu. Returns the PyObject* base (after prefix). Handle stored in map.
func effectiveBasicsize(tp *PyType) int {
	if tp == nil {
		return 16
	}
	best := tp.Basicsize
	for _, b := range tp.MRO {
		if b != nil && b.Basicsize > best {
			best = b.Basicsize
		}
	}
	if best < 16 {
		best = 16
	}
	return best
}

func (a *ShadowArena) allocCustomLocked(v Value) unsafe.Pointer {
	tp := v.Type()
	basicsize := max(effectiveBasicsize(tp), 16)
	// Align total to 8 for Go/C ABI.
	total := basicsize + CustomPayloadPrefixSize
	if total%8 != 0 {
		total += 8 - (total % 8)
	}
	// Allocate from customPages (append-only; freed blocks leak as holes — acceptable for MVP,
	// sizes are small and process lifetime is bounded; alternatively reuse via free list).
	if len(a.customPages) == 0 || a.customPages[len(a.customPages)-1].next+total > len(a.customPages[len(a.customPages)-1].data) {
		data, _, err := arenaMmap(256 * 1024)
		if err != nil {
			panic("val: shadow custom mmap failed: " + err.Error())
		}
		a.customPages = append(a.customPages, arenaPage{data: data, next: 0})
	}
	pg := &a.customPages[len(a.customPages)-1]
	base := unsafe.Pointer(&pg.data[pg.next])
	pg.next += total
	// Zero the whole block incl. payload (C expects zeroed tp_alloc memory).
	for i := range total {
		*(*byte)(unsafe.Add(base, i)) = 0
	}
	stub := unsafe.Add(base, CustomPayloadPrefixSize)
	*stubRefcnt(stub) = 1
	*stubTypePtr(stub) = a.shadowTypeLocked(v.Type())
	idx := a.allocHandleLocked(v)
	*(*uint64)(base) = idx
	*(*uint64)(unsafe.Add(base, 8)) = CustomPayloadMagic
	// Keep size + handle in side maps for FromStub/Dealloc (C may overwrite bytes 16..).
	a.customAlloc[stub] = total
	a.customHandles[stub] = idx
	if v.ptr != nil && v.ptr.Kind == KindNative {
		// Remember arena base on the Go object for tp_dealloc access.
		v.AsCInstance().ArenaPtr = stub
	}
	return stub
}

func (a *ShadowArena) allocHandleLocked(v Value) uint64 {
	if len(a.freeHandles) > 0 {
		idx := a.freeHandles[len(a.freeHandles)-1]
		a.freeHandles = a.freeHandles[:len(a.freeHandles)-1]
		a.handleTable[idx] = v
		return idx
	}
	idx := uint64(len(a.handleTable))
	a.handleTable = append(a.handleTable, v)
	return idx
}

// ensureImmortalsLocked creates immortal singletons. Caller holds mu.
func (a *ShadowArena) ensureImmortalsLocked() {
	if a.immortalDone {
		return
	}
	a.immortalDone = true
	a.immortalStubs = make(map[Value]unsafe.Pointer)
	immortals := []Value{
		NoneVal(),
		BoolVal(true),
		BoolVal(false),
		NotImplementedVal(),
		EllipsisVal(),
	}
	for _, v := range immortals {
		stub := a.allocStubLocked()
		*stubRefcnt(stub) = ImmortalRefcnt
		idx := a.allocHandleLocked(v)
		*stubHandle(stub) = idx
		// type pointer set below after type map ready
		a.reverse[v] = stub
		a.immortalStubs[v] = stub
	}
}

func (a *ShadowArena) shadowTypeLocked(t *PyType) unsafe.Pointer {
	if t == nil {
		return nil
	}
	if p, ok := a.typeMap[t]; ok {
		return p
	}
	// allocate block
	var blk unsafe.Pointer
	if len(a.freeTypeBlk) > 0 {
		blk = a.freeTypeBlk[len(a.freeTypeBlk)-1]
		a.freeTypeBlk = a.freeTypeBlk[:len(a.freeTypeBlk)-1]
	} else {
		if len(a.typePages) == 0 || a.typePages[len(a.typePages)-1].next+TypeBlockSize > len(a.typePages[len(a.typePages)-1].data) {
			data, _, err := arenaMmap(64 * 1024)
			if err != nil {
				panic("val: shadow type mmap failed: " + err.Error())
			}
			a.typePages = append(a.typePages, typePage{data: data, next: 0})
		}
		pg := &a.typePages[len(a.typePages)-1]
		blk = unsafe.Pointer(&pg.data[pg.next])
		pg.next += TypeBlockSize
		clear(pg.data[pg.next-TypeBlockSize : pg.next])
	}
	// refcnt immortal
	*(*int64)(blk) = ImmortalRefcnt
	// ob_type: for TypeType itself, point to self; otherwise to TypeType's type
	// TypeType may not be in map yet; handle recursion guard.
	switch {
	case t == TypeType:
		*(*unsafe.Pointer)(unsafe.Add(blk, 8)) = blk
	case TypeType != nil && TypeType != t:
		if mt, ok := a.typeMap[TypeType]; ok {
			*(*unsafe.Pointer)(unsafe.Add(blk, 8)) = mt
		} else if TypeType != nil {
			// temporarily register current t to avoid cycle, then create TypeType
			a.typeMap[t] = blk
			a.typeReverse[blk] = t
			_ = a.shadowTypeLocked(TypeType)
		}
	default:
		*(*unsafe.Pointer)(unsafe.Add(blk, 8)) = blk
	}
	// tp_name at offset 24 (after PyVarObject: refcnt8+type8+size8)
	nameBytes := append([]byte(t.Name), 0)
	a.typeNames[blk] = nameBytes
	*(*unsafe.Pointer)(unsafe.Add(blk, 24)) = unsafe.Pointer(&nameBytes[0])
	// tp_basicsize at 32: plausible size per kind
	bs := int64(32)
	switch t.Name {
	case "str", "bytes", "bytearray":
		bs = 64
	case "int", "float", "complex":
		bs = 32
	case "list", "dict", "tuple", "set":
		bs = 64
	case "module":
		bs = 64
	}
	*(*int64)(unsafe.Add(blk, 32)) = bs
	// tp_flags @168 for FastSubclass checks; propagate from MRO bases so int subclasses pass PyLong_Check.
	var cflags uint64
	for _, b := range t.MRO {
		switch b {
		case IntType, BoolType:
			cflags |= 1 << 24
		case ListType:
			cflags |= 1 << 25
		case TupleType:
			cflags |= 1 << 26
		case BytesType, ByteArrayType:
			cflags |= 1 << 27
		case StrType:
			cflags |= 1 << 28
		case DictType:
			cflags |= 1 << 29
		case TypeType:
			cflags |= 1 << 31
		}
	}
	// Self case when MRO not yet built (early init: t itself is builtin).
	switch t {
	case IntType, BoolType:
		cflags |= 1 << 24
	case ListType:
		cflags |= 1 << 25
	case TupleType:
		cflags |= 1 << 26
	case BytesType, ByteArrayType:
		cflags |= 1 << 27
	case StrType:
		cflags |= 1 << 28
	case DictType:
		cflags |= 1 << 29
	case TypeType:
		cflags |= 1 << 31
	}
	cflags |= 1 << 10
	*(*uint64)(unsafe.Add(blk, 168)) = cflags
	// tp_itemsize at 40
	*(*int64)(unsafe.Add(blk, 40)) = 0
	a.typeMap[t] = blk
	a.typeReverse[blk] = t
	return blk
}

// ToStub returns PyObject* for v with new reference (refcnt inc).
func (a *ShadowArena) ToStub(v Value) unsafe.Pointer {
	if v.ptr == nil {
		return nil
	}
	a.mu.Lock()
	a.ensureImmortalsLocked()
	if stub, ok := a.reverse[v]; ok {
		rc := *stubRefcnt(stub)
		if rc < ImmortalRefcnt {
			*stubRefcnt(stub) = rc + 1
		}
		a.mu.Unlock()
		return stub
	}
	if needsCustomLocked(v) {
		stub := a.allocCustomLocked(v)
		a.reverse[v] = stub
		a.mu.Unlock()
		return stub
	}
	stub := a.allocStubLocked()
	*stubRefcnt(stub) = 1
	*stubTypePtr(stub) = a.shadowTypeLocked(v.Type())
	idx := a.allocHandleLocked(v)
	*stubHandle(stub) = idx
	a.reverse[v] = stub
	a.mu.Unlock()
	return stub
}

// BorrowStub returns PyObject* without inc ref (borrowed).
func (a *ShadowArena) BorrowStub(v Value) unsafe.Pointer {
	if v.ptr == nil {
		return nil
	}
	a.mu.Lock()
	a.ensureImmortalsLocked()
	if stub, ok := a.reverse[v]; ok {
		a.mu.Unlock()
		return stub
	}
	if needsCustomLocked(v) {
		stub := a.allocCustomLocked(v)
		a.reverse[v] = stub
		a.mu.Unlock()
		return stub
	}
	stub := a.allocStubLocked()
	*stubRefcnt(stub) = 1
	*stubTypePtr(stub) = a.shadowTypeLocked(v.Type())
	idx := a.allocHandleLocked(v)
	*stubHandle(stub) = idx
	a.reverse[v] = stub
	a.mu.Unlock()
	return stub
}

// FromStub returns Value for PyObject*.
func (a *ShadowArena) FromStub(p unsafe.Pointer) Value {
	if p == nil {
		return NoneVal()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if idx, ok := a.customHandles[p]; ok {
		if idx >= uint64(len(a.handleTable)) {
			return NoneVal()
		}
		return a.handleTable[idx]
	}
	idx := *stubHandle(p)
	if idx >= uint64(len(a.handleTable)) {
		return NoneVal()
	}
	return a.handleTable[idx]
}

// IsImmortal reports whether stub is immortal.
func (a *ShadowArena) IsImmortal(p unsafe.Pointer) bool {
	if p == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return *stubRefcnt(p) >= ImmortalRefcnt
}

// DeallocStub is called by _Py_Dealloc when refcnt hits 0.
func (a *ShadowArena) DeallocStub(p unsafe.Pointer) {
	if p == nil {
		return
	}
	a.mu.Lock()
	rc := *stubRefcnt(p)
	if rc >= ImmortalRefcnt {
		a.mu.Unlock()
		return
	}
	if rc != 0 {
		a.mu.Unlock()
		return
	}
	// Resolve handle (custom side map first; C may have overwritten offset 16).
	var idx uint64
	isCustom := false
	if h, ok := a.customHandles[p]; ok {
		idx = h
		isCustom = true
	} else {
		idx = *stubHandle(p)
	}
	var v Value
	if idx < uint64(len(a.handleTable)) {
		v = a.handleTable[idx]
		a.handleTable[idx] = NoneVal()
		a.freeHandles = append(a.freeHandles, idx)
	}
	if isCustom {
		delete(a.customHandles, p)
		delete(a.customAlloc, p)
		delete(a.reverse, v)
		if buf, ok := a.strCache[p]; ok {
			_ = buf
			delete(a.strCache, p)
		}
		// Mark payload zeroed; block is leaked (append-only arena) to avoid use-after-free
		// with C code retaining the pointer during tp_dealloc. Call tp_dealloc hook, then release.
		if v.ptr != nil && v.ptr.Kind == KindNative {
			ci := v.AsCInstance()
			if ci != nil && !ci.DeallocCalled {
				ci.DeallocCalled = true
			}
		}
		hook := CDeallocHook
		a.mu.Unlock()
		if hook != nil && v.ptr != nil {
			hook(v)
		}
		return
	}
	// Non-custom path: lock still held from top (no re-acquire). Release at end.
	// delete reverse entry (find by value)
	if v.ptr != nil || v.num != 0 {
		delete(a.reverse, v)
	}
	// delete string cache
	if buf, ok := a.strCache[p]; ok {
		_ = buf
		delete(a.strCache, p)
	}
	// zero stub for reuse
	*stubRefcnt(p) = 0
	*stubTypePtr(p) = nil
	*stubHandle(p) = 0
	*stubFlags(p) = 0
	a.freeStubs = append(a.freeStubs, p)
	// decref children to avoid leaks (tuple/list/dict/set)
	if v.ptr != nil && v.Kind() == KindTuple {
		for _, it := range v.AsTuple().Items {
			if it.ptr != nil {
				if stub, ok := a.reverse[it]; ok {
					rc := *stubRefcnt(stub)
					if rc < ImmortalRefcnt {
						if rc-1 == 0 {
							// recursive dealloc (unlock would deadlock if calling DeallocStub directly;
							// inline free here)
							*stubRefcnt(stub) = 0
							idx2 := *stubHandle(stub)
							if idx2 < uint64(len(a.handleTable)) {
								a.handleTable[idx2] = NoneVal()
								a.freeHandles = append(a.freeHandles, idx2)
							}
							delete(a.reverse, it)
							delete(a.strCache, stub)
							*stubTypePtr(stub) = nil
							*stubHandle(stub) = 0
							*stubFlags(stub) = 0
							a.freeStubs = append(a.freeStubs, stub)
						} else {
							*stubRefcnt(stub) = rc - 1
						}
					}
				}
			}
		}
	}
	a.mu.Unlock()
}

// ShadowType returns PyTypeObject* for t.
func (a *ShadowArena) ShadowType(t *PyType) unsafe.Pointer {
	if t == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.shadowTypeLocked(t)
}

// TypeFromShadow returns *PyType for shadow type ptr.
func (a *ShadowArena) TypeFromShadow(p unsafe.Pointer) *PyType {
	if p == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.typeReverse[p]
}

// StubUTF8 returns cached NUL-terminated pointer for string/bytes Value behind stub.
// Allocates on first use, keeps Go slice alive in strCache.
func (a *ShadowArena) StubUTF8(p unsafe.Pointer) unsafe.Pointer {
	if p == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if b, ok := a.strCache[p]; ok {
		if len(b) == 0 {
			return nil
		}
		return unsafe.Pointer(&b[0])
	}
	idx := *stubHandle(p)
	if idx >= uint64(len(a.handleTable)) {
		return nil
	}
	v := a.handleTable[idx]
	var b []byte
	switch {
	case v.IsAnyString():
		s := v.AsString()
		b = append([]byte(s), 0)
	case v.IsBytes():
		b = append([]byte{}, v.AsBytes().Data...)
		b = append(b, 0)
	case v.IsByteArray():
		ba := v.AsByteArray()
		// lock to copy
		ba.mu.RLock()
		b = append([]byte{}, ba.Data...)
		ba.mu.RUnlock()
		b = append(b, 0)
	default:
		return nil
	}
	if len(b) == 0 {
		b = []byte{0}
	}
	a.strCache[p] = b
	// Keep flags_data as size for debugging (not pointer bits).
	*stubFlags(p) = uint64(len(b))
	return unsafe.Pointer(&b[0])
}

// StubSetImmortal forces immortal (for internal use).
func (a *ShadowArena) StubSetImmortal(p unsafe.Pointer) {
	if p == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	*stubRefcnt(p) = ImmortalRefcnt
}

// Package-level helpers via default arena.
func ArenaToStub(v Value) unsafe.Pointer       { return GetDefaultArena().ToStub(v) }
func ArenaBorrowStub(v Value) unsafe.Pointer   { return GetDefaultArena().BorrowStub(v) }
func ArenaFromStub(p unsafe.Pointer) Value     { return GetDefaultArena().FromStub(p) }
func ArenaDealloc(p unsafe.Pointer)            { GetDefaultArena().DeallocStub(p) }
func ArenaShadowType(t *PyType) unsafe.Pointer { return GetDefaultArena().ShadowType(t) }
func ArenaTypeFromShadow(p unsafe.Pointer) *PyType {
	return GetDefaultArena().TypeFromShadow(p)
}
func ArenaStubUTF8(p unsafe.Pointer) unsafe.Pointer {
	return GetDefaultArena().StubUTF8(p)
}

// AliasShadowType registers an external type block (shim data symbol) as the shadow for t.
func AliasShadowType(a *ShadowArena, t *PyType, blk unsafe.Pointer) {
	if a == nil || t == nil || blk == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.typeMap[t] = blk
	a.typeReverse[blk] = t
}

// AliasShadowStub registers an external object block (shim _Py_*Struct) as the stub for v.
// The block must be at least 32 bytes; it is initialized as an immortal stub
// with refcnt=Immortal, type=shadow of v.Type(), handle pointing at v.
func AliasShadowStub(a *ShadowArena, v Value, blk unsafe.Pointer) {
	if a == nil || blk == nil || v.HeaderPtr() == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ensureImmortalsLocked()
	*stubRefcnt(blk) = ImmortalRefcnt
	*stubTypePtr(blk) = a.shadowTypeLocked(v.Type())
	idx := a.allocHandleLocked(v)
	*stubHandle(blk) = idx
	*stubFlags(blk) = 0
	a.reverse[v] = blk
	if v.IsNone() || v.IsBool() || v.IsNotImplemented() || v.IsEllipsis() {
		a.immortalStubs[v] = blk
	}
}

// ForgetStubForValue drops reverse/handle entries when Go GC collects a C instance
// whose C refcount never hit zero (orphaned). Safe to call multiple times.
func (a *ShadowArena) ForgetStubForValue(v Value) {
	if v.HeaderPtr() == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// If the stub still exists and refcount is 1 (Go-owned), free it.
	// If refcount >1 (C holds refs), leave handle so C can still FromStub, but clear reverse
	// so GC'd Value is not returned for a stale pointer. For MVP, only free when refcount==1.
	if stub, ok := a.reverse[v]; ok {
		rc := *stubRefcnt(stub)
		if rc < ImmortalRefcnt && rc <= 1 {
			if h, isCustom := a.customHandles[stub]; isCustom {
				_ = h
				delete(a.customHandles, stub)
				delete(a.customAlloc, stub)
				// Free handle.
				if h < uint64(len(a.handleTable)) {
					a.handleTable[h] = NoneVal()
					a.freeHandles = append(a.freeHandles, h)
				}
			} else if idx := *stubHandle(stub); idx < uint64(len(a.handleTable)) {
				a.handleTable[idx] = NoneVal()
				a.freeHandles = append(a.freeHandles, idx)
			}
			*stubRefcnt(stub) = 0
			delete(a.reverse, v)
			if _, isCustom := a.customHandles[stub]; !isCustom {
				a.freeStubs = append(a.freeStubs, stub)
			}
		}
	}
}

// ArenaStats exposes counts for GC/leak tests.
func (a *ShadowArena) ArenaStats() (handles int, freeStubs int, reverse int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.handleTable), len(a.freeStubs), len(a.reverse)
}
