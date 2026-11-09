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
)

type arenaPage struct {
	data []byte // keep mmap alive
	next int
}

type typePage struct {
	data []byte
	next int
}

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
		handleTable: make([]Value, 0, 1024),
		reverse:     make(map[Value]unsafe.Pointer),
		typeMap:     make(map[*PyType]unsafe.Pointer),
		typeReverse: make(map[unsafe.Pointer]*PyType),
		typeNames:   make(map[unsafe.Pointer][]byte),
		strCache:    make(map[unsafe.Pointer][]byte),
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
		// check immortal
		rc := *stubRefcnt(stub)
		if rc < ImmortalRefcnt {
			*stubRefcnt(stub) = rc + 1
		}
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
	// fast immortal check via reverse? need handle lookup
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
	defer a.mu.Unlock()
	rc := *stubRefcnt(p)
	if rc >= ImmortalRefcnt {
		// immortal: ignore (CPython immortal DECREF is no-op)
		return
	}
	if rc != 0 {
		return
	}
	idx := *stubHandle(p)
	var v Value
	if idx < uint64(len(a.handleTable)) {
		v = a.handleTable[idx]
		a.handleTable[idx] = NoneVal()
		a.freeHandles = append(a.freeHandles, idx)
	}
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

// ArenaStats exposes counts for GC/leak tests.
func (a *ShadowArena) ArenaStats() (handles int, freeStubs int, reverse int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.handleTable), len(a.freeStubs), len(a.reverse)
}
