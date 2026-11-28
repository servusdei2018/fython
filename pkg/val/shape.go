package val

import (
	"sync"
	"sync/atomic"
	"unsafe"
)

var (
	nextShapeID atomic.Uint32
)

// SetMultiThreaded enables or disables multi-threaded mode for instance slots.
func SetMultiThreaded(v bool) {
	MultiThreadedFlag.Store(v)
}

// MultiThreadedFlag holds the state of the multi-threaded flag. It is
// exported so that the dispatch loop of the interpreter can read it without
// a function call: the interpreter runs in a function that is too large for
// the Go inliner, so every call there is a real call.
var MultiThreadedFlag atomic.Bool

// IsMultiThreaded reports whether concurrent execution is active.
func IsMultiThreaded() bool {
	return MultiThreadedFlag.Load()
}

func allocShapeID() uint32 {
	return nextShapeID.Add(1)
}

// ShapeAttr maps an interned attribute name Value to its flat slot index in PyInstance.Slots.
type ShapeAttr struct {
	Name  Value
	Index int
}

// ShapeTransition records a parent-to-child Shape transition when a new attribute is added.
type ShapeTransition struct {
	Name   Value
	Target *Shape
}

// Shape represents a V8/PyPy-style Hidden Class descriptor.
// It maps interned attribute names to integer slot indices in PyInstance.Slots
// and tracks parent-to-child transitions when new attributes are added.
type Shape struct {
	ID          uint32
	Parent      *Shape
	Type        *PyType
	AttrName    Value
	SlotIndex   int
	Attrs       []ShapeAttr
	index       atomic.Pointer[map[Value]int]
	mu          sync.Mutex
	transitions atomic.Pointer[[]ShapeTransition]
}

// NewRootShapeForType allocates a new empty root Shape tied to a specific concrete PyType.
func NewRootShapeForType(cls *PyType) *Shape {
	s := &Shape{
		ID:        allocShapeID(),
		Type:      cls,
		SlotIndex: -1,
	}
	empty := make([]ShapeTransition, 0, 4)
	s.transitions.Store(&empty)
	return s
}

// NewRootShape allocates a new empty root Shape with a unique non-zero ID.
func NewRootShape() *Shape {
	return NewRootShapeForType(nil)
}

var defaultRootShape = NewRootShape()

// DefaultRootShape returns the shared root Shape for standard object instances.
func DefaultRootShape() *Shape {
	return defaultRootShape
}

// Lookup resolves an attribute name to its flat slot index without heap allocations.
// For shapes with >8 attributes, a lazily built map index avoids O(N) scans.
func (s *Shape) Lookup(name Value) (int, bool) {
	attrs := s.Attrs
	if len(attrs) > 8 {
		m := s.index.Load()
		if m == nil {
			built := make(map[Value]int, len(attrs))
			for i := range attrs {
				built[attrs[i].Name] = attrs[i].Index
			}
			s.index.Store(&built)
			m = &built
		}
		if idx, ok := (*m)[name]; ok {
			return idx, true
		}
		if name.ptr != nil && name.ptr.Kind == KindString && (name.ptr.Flags&FlagInterned) == 0 {
			for i := range attrs {
				if StringEquals(attrs[i].Name, name) {
					return attrs[i].Index, true
				}
			}
		}
		return -1, false
	}
	for i := range attrs {
		a := &attrs[i]
		if a.Name.ptr == name.ptr && a.Name.num == name.num {
			return a.Index, true
		}
	}
	// Fallback if caller passed an un-interned heap string.
	if name.ptr != nil && name.ptr.Kind == KindString && (name.ptr.Flags&FlagInterned) == 0 {
		for i := range attrs {
			if StringEquals(attrs[i].Name, name) {
				return attrs[i].Index, true
			}
		}
	}
	return -1, false
}

// Transition returns the child Shape resulting from adding attribute name to s.
// It uses a lock-free atomic load on the fast path and copy-on-write transitions on new attributes.
func (s *Shape) Transition(name Value) *Shape {
	interned := InternValue(name)

	// Lock-free fast path
	tPtr := s.transitions.Load()
	if tPtr != nil {
		transitions := *tPtr
		for i := range transitions {
			t := &transitions[i]
			if t.Name.ptr == interned.ptr && t.Name.num == interned.num {
				return t.Target
			}
		}
	}

	// Slow path: acquire mutex and double-check
	s.mu.Lock()
	defer s.mu.Unlock()

	tPtr = s.transitions.Load()
	var transitions []ShapeTransition
	if tPtr != nil {
		transitions = *tPtr
		for i := range transitions {
			t := &transitions[i]
			if t.Name.ptr == interned.ptr && t.Name.num == interned.num {
				return t.Target
			}
		}
	}

	newSlotIdx := len(s.Attrs)
	newAttrs := make([]ShapeAttr, newSlotIdx+1)
	copy(newAttrs, s.Attrs)
	newAttrs[newSlotIdx] = ShapeAttr{
		Name:  interned,
		Index: newSlotIdx,
	}

	child := &Shape{
		ID:        allocShapeID(),
		Parent:    s,
		Type:      s.Type,
		AttrName:  interned,
		SlotIndex: newSlotIdx,
		Attrs:     newAttrs,
	}
	empty := make([]ShapeTransition, 0, 4)
	child.transitions.Store(&empty)

	newTransitions := make([]ShapeTransition, len(transitions)+1)
	copy(newTransitions, transitions)
	newTransitions[len(transitions)] = ShapeTransition{
		Name:   interned,
		Target: child,
	}
	s.transitions.Store(&newTransitions)

	return child
}

// instanceLocks is a striped RWMutex table used to synchronize PyInstance slot
// access only when MultiThreadedFlag is set, keeping PyInstance free of an
// embedded 24-byte sync.RWMutex.
type paddedRWMutex struct {
	sync.RWMutex
	_ [64 - unsafe.Sizeof(sync.RWMutex{})]byte
}

var instanceLocks [64]paddedRWMutex

func (inst *PyInstance) lock() *sync.RWMutex {
	return &instanceLocks[(uintptr(unsafe.Pointer(inst))>>5)&63].RWMutex
}

// PyInstance represents a Python object instance backed by a Hidden Class Shape
// and a flat contiguous slice of attribute Values (never backed by a PyDict or Go map).
// Header MUST be at offset 0.
type PyInstance struct {
	Header   ObjectHeader
	Shape    *Shape
	Slots    []Value
	dictView *PyDict
}

// Fused size-class instance bundles place PyInstance at offset 0 (preserving
// Header at offset 0) followed by an inline fixed-size Value array so instances
// with 1..12 attributes allocate in a single heap object sized to their slot count.
type pyInstanceBundle1 struct {
	inst  PyInstance
	slots [1]Value
}

type pyInstanceBundle2 struct {
	inst  PyInstance
	slots [2]Value
}

type pyInstanceBundle3 struct {
	inst  PyInstance
	slots [3]Value
}

type pyInstanceBundle4 struct {
	inst  PyInstance
	slots [4]Value
}

type pyInstanceBundle6 struct {
	inst  PyInstance
	slots [6]Value
}

type pyInstanceBundle8 struct {
	inst  PyInstance
	slots [8]Value
}

type pyInstanceBundle12 struct {
	inst  PyInstance
	slots [12]Value
}

// Compile-time assertions that PyInstance.Header is at byte offset 0, the
// decoupled struct stays at 56 bytes (Go's 64-byte size class), and every
// fused bundle places inst at offset 0.
var (
	_ [0 - unsafe.Offsetof(PyInstance{}.Header)]byte
	_ [56 - unsafe.Sizeof(PyInstance{})]byte
	_ [unsafe.Sizeof(PyInstance{}) - 56]byte
	_ [0 - unsafe.Offsetof(pyInstanceBundle1{}.inst)]byte
	_ [0 - unsafe.Offsetof(pyInstanceBundle2{}.inst)]byte
	_ [0 - unsafe.Offsetof(pyInstanceBundle3{}.inst)]byte
	_ [0 - unsafe.Offsetof(pyInstanceBundle4{}.inst)]byte
	_ [0 - unsafe.Offsetof(pyInstanceBundle6{}.inst)]byte
	_ [0 - unsafe.Offsetof(pyInstanceBundle8{}.inst)]byte
	_ [0 - unsafe.Offsetof(pyInstanceBundle12{}.inst)]byte
)

// allocInstanceBundle allocates a PyInstance with len(Slots) == initLen and
// cap(Slots) >= max(initLen, capHint), using a single fused bundle for 0..12
// slots and a decoupled backing slice above 12 slots.
func allocInstanceBundle(cls *PyType, shape *Shape, initLen, capHint int) *PyInstance {
	slotCap := max(initLen, capHint)
	switch {
	case slotCap <= 0:
		return &PyInstance{Header: ObjectHeader{Kind: KindInstance, Type: cls}, Shape: shape}
	case slotCap == 1:
		b := &pyInstanceBundle1{inst: PyInstance{Header: ObjectHeader{Kind: KindInstance, Type: cls}, Shape: shape}}
		b.inst.Slots = b.slots[:initLen]
		return &b.inst
	case slotCap == 2:
		b := &pyInstanceBundle2{inst: PyInstance{Header: ObjectHeader{Kind: KindInstance, Type: cls}, Shape: shape}}
		b.inst.Slots = b.slots[:initLen]
		return &b.inst
	case slotCap == 3:
		b := &pyInstanceBundle3{inst: PyInstance{Header: ObjectHeader{Kind: KindInstance, Type: cls}, Shape: shape}}
		b.inst.Slots = b.slots[:initLen]
		return &b.inst
	case slotCap == 4:
		b := &pyInstanceBundle4{inst: PyInstance{Header: ObjectHeader{Kind: KindInstance, Type: cls}, Shape: shape}}
		b.inst.Slots = b.slots[:initLen]
		return &b.inst
	case slotCap <= 6:
		b := &pyInstanceBundle6{inst: PyInstance{Header: ObjectHeader{Kind: KindInstance, Type: cls}, Shape: shape}}
		b.inst.Slots = b.slots[:initLen]
		return &b.inst
	case slotCap <= 8:
		b := &pyInstanceBundle8{inst: PyInstance{Header: ObjectHeader{Kind: KindInstance, Type: cls}, Shape: shape}}
		b.inst.Slots = b.slots[:initLen]
		return &b.inst
	case slotCap <= 12:
		b := &pyInstanceBundle12{inst: PyInstance{Header: ObjectHeader{Kind: KindInstance, Type: cls}, Shape: shape}}
		b.inst.Slots = b.slots[:initLen]
		return &b.inst
	default:
		return &PyInstance{
			Header: ObjectHeader{Kind: KindInstance, Type: cls},
			Shape:  shape,
			Slots:  make([]Value, initLen, slotCap),
		}
	}
}

// NewInstanceVal constructs a new heap-allocated KindInstance Value with the given initial Shape.
// If shape is nil, DefaultRootShape() is used.
func NewInstanceVal(shape *Shape) Value {
	if shape == nil {
		shape = defaultRootShape
	}
	n := len(shape.Attrs)
	obj := allocInstanceBundle(ObjectType, shape, n, n)
	return Value{num: 0, ptr: &obj.Header}
}

// NewCInstanceVal constructs a new C-extension instance (KindNative) belonging to cls.
// The off-heap payload is allocated lazily on first ToStub/BorrowStub conversion;
// AllocCInstanceStub forces allocation (used by capi tp_new/tp_init bridging).
func NewCInstanceVal(cls *PyType) Value {
	if cls == nil {
		cls = ObjectType
	}
	obj := &PyCInstance{
		Header: ObjectHeader{Kind: KindNative, Type: cls},
	}
	return Value{num: 0, ptr: &obj.Header}
}

// AllocCInstanceStub ensures v has an off-heap PyObject* (allocates custom payload if needed)
// and returns it. Used by capi when bridging tp_new/tp_init.
func AllocCInstanceStub(v Value) unsafe.Pointer {
	return GetDefaultArena().ToStub(v)
}

// NewInstanceWithShape constructs a new heap-allocated PyInstance belonging to cls
// with a pre-seeded Shape whose slot count matches len(shape.Attrs). Callers must
// populate every slot in [0, len(shape.Attrs)) before exposing the instance.
func NewInstanceWithShape(cls *PyType, shape *Shape) *PyInstance {
	n := len(shape.Attrs)
	return allocInstanceBundle(cls, shape, n, n)
}

// NewInstanceOfType constructs a new heap-allocated KindInstance Value belonging to cls.
func NewInstanceOfType(cls *PyType) Value {
	if cls == nil {
		cls = ObjectType
	}
	if (cls.Flags & (FlagHasCExtBase | FlagIsModuleSub)) != 0 {
		// C extension types (tp_basicsize > 16) use PyCInstance even for Python subclasses,
		// so C private fields at offsets 16.. survive Python __dict__ additions.
		if (cls.Flags & FlagHasCExtBase) != 0 {
			return NewCInstanceVal(cls)
		}
		dict := NewDictVal(16).AsDict()
		dict.VersionTag = atomic.AddUint64(&nextModuleDictVer, 1<<32)
		mod := &PyModule{
			Header: ObjectHeader{Kind: KindModule, Type: cls},
			Dict:   dict,
		}
		return Value{num: 0, ptr: &mod.Header}
	}
	shape := cls.RootShape
	if shape == nil {
		shape = defaultRootShape
	}
	n := len(shape.Attrs)
	capHint := n
	if (cls.Flags & FlagForbidDynamicAttrs) == 0 {
		if pred := int(cls.likelySlotCap.Load()); pred > capHint {
			capHint = pred
		}
	}
	obj := allocInstanceBundle(cls, shape, n, capHint)
	return Value{num: 0, ptr: &obj.Header}
}

// AsInstanceChecked returns the instance of v, or nil when v does not hold an
// instance. It is kept small (inlinable cost <= 20) so that the interpreter
// loop can use it without growing its own body.
func (v Value) AsInstanceChecked() *PyInstance {
	if v.ptr == nil || v.ptr.Kind != KindInstance {
		return nil
	}
	return (*PyInstance)(unsafe.Pointer(v.ptr))
}

// AsInstance performs a zero-cost offset-0 downcast from Value to *PyInstance.
func (v Value) AsInstance() *PyInstance {
	return (*PyInstance)(unsafe.Pointer(v.ptr))
}

// GetSlot returns the Value at slot index idx.
func (inst *PyInstance) GetSlot(idx int) (Value, bool) {
	if !MultiThreadedFlag.Load() {
		if uint(idx) < uint(len(inst.Slots)) {
			return inst.Slots[idx], true
		}
		return NoneVal(), false
	}
	mu := inst.lock()
	mu.RLock()
	if idx < 0 || idx >= len(inst.Slots) {
		mu.RUnlock()
		return NoneVal(), false
	}
	val := inst.Slots[idx]
	mu.RUnlock()
	return val, true
}

// SetSlot sets the Value at slot index idx.
func (inst *PyInstance) SetSlot(idx int, val Value) bool {
	if !MultiThreadedFlag.Load() {
		if uint(idx) < uint(len(inst.Slots)) {
			inst.Slots[idx] = val
			return true
		}
		return false
	}
	mu := inst.lock()
	mu.Lock()
	if idx < 0 || idx >= len(inst.Slots) {
		mu.Unlock()
		return false
	}
	inst.Slots[idx] = val
	mu.Unlock()
	return true
}

// noteSlotCap monotonically bumps cls.likelySlotCap up to min(c, 64) so future
// instances of dynamic-attribute classes allocate the matching fused slot bundle
// in one allocation without overflowing mid-__init__.
func (t *PyType) noteSlotCap(c int) {
	if t == nil || c <= 0 {
		return
	}
	if c > 64 {
		c = 64
	}
	want := uint32(c)
	for {
		cur := t.likelySlotCap.Load()
		if cur >= want {
			return
		}
		if t.likelySlotCap.CompareAndSwap(cur, want) {
			return
		}
	}
}

// ExtendBootstrapSlot appends v into inst.Slots at slotIdx when slotIdx is the
// next contiguous slot and fits within existing capacity (fused bundle or
// pre-allocated Slots slice).
func (inst *PyInstance) ExtendBootstrapSlot(slotIdx int, v Value) bool {
	if slotIdx == len(inst.Slots) && slotIdx < cap(inst.Slots) {
		inst.Slots = inst.Slots[:slotIdx+1]
		inst.Slots[slotIdx] = v
		if tp := inst.Header.Type; tp != nil && slotIdx+1 > int(tp.likelySlotCap.Load()) {
			tp.noteSlotCap(slotIdx + 1)
		}
		return true
	}
	return false
}

// ApplyShapeTransition writes v at slotIdx (extending Slots if needed) and
// advances inst.Shape to newShape.
func (inst *PyInstance) ApplyShapeTransition(newShape *Shape, slotIdx int, v Value) {
	if !MultiThreadedFlag.Load() {
		switch {
		case slotIdx < len(inst.Slots):
			inst.Slots[slotIdx] = v
		case slotIdx == len(inst.Slots) && slotIdx < cap(inst.Slots):
			inst.Slots = inst.Slots[:slotIdx+1]
			inst.Slots[slotIdx] = v
			if tp := inst.Header.Type; tp != nil && slotIdx+1 > int(tp.likelySlotCap.Load()) {
				tp.noteSlotCap(slotIdx + 1)
			}
		default:
			inst.Slots = inst.appendSlot(v)
		}
		inst.Shape = newShape
		return
	}
	mu := inst.lock()
	mu.Lock()
	switch {
	case slotIdx < len(inst.Slots):
		inst.Slots[slotIdx] = v
	case slotIdx == len(inst.Slots) && slotIdx < cap(inst.Slots):
		inst.Slots = inst.Slots[:slotIdx+1]
		inst.Slots[slotIdx] = v
		if tp := inst.Header.Type; tp != nil && slotIdx+1 > int(tp.likelySlotCap.Load()) {
			tp.noteSlotCap(slotIdx + 1)
		}
	default:
		inst.Slots = inst.appendSlot(v)
	}
	inst.Shape = newShape
	mu.Unlock()
}

// GetAttr looks up an attribute by name on the instance using its Shape.
func (inst *PyInstance) GetAttr(name Value) (Value, bool) {
	if !MultiThreadedFlag.Load() {
		idx, ok := inst.Shape.Lookup(name)
		if !ok || uint(idx) >= uint(len(inst.Slots)) {
			return NoneVal(), false
		}
		val := inst.Slots[idx]
		if val.IsUnbound() {
			return NoneVal(), false
		}
		return val, true
	}
	mu := inst.lock()
	mu.RLock()
	idx, ok := inst.Shape.Lookup(name)
	if !ok || idx >= len(inst.Slots) {
		mu.RUnlock()
		return NoneVal(), false
	}
	val := inst.Slots[idx]
	mu.RUnlock()
	if val.IsUnbound() {
		return NoneVal(), false
	}
	return val, true
}

// DeleteAttr removes an attribute from the instance by marking its slot unbound.
func (inst *PyInstance) DeleteAttr(name Value) bool {
	if !MultiThreadedFlag.Load() {
		idx, ok := inst.Shape.Lookup(name)
		if !ok || uint(idx) >= uint(len(inst.Slots)) || inst.Slots[idx].IsUnbound() {
			return false
		}
		inst.Slots[idx] = UnboundVal()
		return true
	}
	mu := inst.lock()
	mu.Lock()
	defer mu.Unlock()
	idx, ok := inst.Shape.Lookup(name)
	if !ok || idx >= len(inst.Slots) || inst.Slots[idx].IsUnbound() {
		return false
	}
	inst.Slots[idx] = UnboundVal()
	return true
}

// SetAttr sets or adds an attribute on the instance, transitioning its Shape if the attribute is new in a thread-safe manner.
// Returns the resolved slot index.
func (inst *PyInstance) SetAttr(name, value Value) int {
	if !MultiThreadedFlag.Load() {
		return inst.setAttrUnlocked(name, value)
	}
	mu := inst.lock()
	mu.Lock()
	defer mu.Unlock()
	return inst.setAttrUnlocked(name, value)
}

func (inst *PyInstance) setAttrUnlocked(name, value Value) int {
	if idx, ok := inst.Shape.Lookup(name); ok {
		if idx < len(inst.Slots) {
			inst.Slots[idx] = value
		} else {
			inst.Slots = inst.appendSlot(value)
		}
		return idx
	}
	if inst.Header.Type != nil && (inst.Header.Type.Flags&FlagForbidDynamicAttrs) != 0 {
		if BaseExceptionType != nil && IsSubclass(inst.Header.Type, BaseExceptionType) {
			if name.StringEquals("__traceback__") || name.StringEquals("__cause__") ||
				name.StringEquals("__context__") || name.StringEquals("__suppress_context__") {
				goto allowDynamic
			}
		}
		panic("val: AttributeError: '" + inst.Header.Type.Name + "' object has no attribute '" + name.AsString() + "'")
	}
allowDynamic:
	newShape := inst.Shape.Transition(name)
	slotIdx := newShape.SlotIndex
	if slotIdx < len(inst.Slots) {
		inst.Slots[slotIdx] = value
	} else {
		inst.Slots = inst.appendSlot(value)
	}
	inst.Shape = newShape
	return slotIdx
}

func (inst *PyInstance) appendSlot(value Value) []Value {
	n := len(inst.Slots)
	if n < cap(inst.Slots) {
		s := inst.Slots[:n+1]
		s[n] = value
		if tp := inst.Header.Type; tp != nil && len(s) > int(tp.likelySlotCap.Load()) {
			tp.noteSlotCap(len(s))
		}
		return s
	}
	inst.Slots = append(inst.Slots, value)
	if tp := inst.Header.Type; tp != nil && len(inst.Slots) > int(tp.likelySlotCap.Load()) {
		tp.noteSlotCap(len(inst.Slots))
	}
	return inst.Slots
}
