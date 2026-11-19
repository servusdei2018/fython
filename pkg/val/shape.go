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
	Header    ObjectHeader
	Shape     *Shape
	Slots     []Value
	bootstrap [6]Value
	dictView  *PyDict
}

// Compile-time assertions that PyInstance.Header is at byte offset 0 and the
// struct stays at 152 bytes (inside Go's 160-byte size class).
var (
	_ [0 - unsafe.Offsetof(PyInstance{}.Header)]byte
	_ [152 - unsafe.Sizeof(PyInstance{})]byte
	_ [unsafe.Sizeof(PyInstance{}) - 152]byte
)

// NewInstanceVal constructs a new heap-allocated KindInstance Value with the given initial Shape.
// If shape is nil, DefaultRootShape() is used.
func NewInstanceVal(shape *Shape) Value {
	if shape == nil {
		shape = defaultRootShape
	}
	n := len(shape.Attrs)
	obj := &PyInstance{
		Header: ObjectHeader{Kind: KindInstance, Type: ObjectType},
		Shape:  shape,
	}
	if n <= len(obj.bootstrap) {
		obj.Slots = obj.bootstrap[:n]
	} else {
		obj.Slots = make([]Value, n, max(8, n))
	}
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
		mod := &PyModule{
			Header: ObjectHeader{Kind: KindModule, Type: cls},
			Dict:   NewDictVal(16).AsDict(),
		}
		return Value{num: 0, ptr: &mod.Header}
	}
	shape := cls.RootShape
	if shape == nil {
		shape = defaultRootShape
	}
	n := len(shape.Attrs)
	obj := &PyInstance{
		Header: ObjectHeader{Kind: KindInstance, Type: cls},
		Shape:  shape,
	}
	if n <= len(obj.bootstrap) {
		obj.Slots = obj.bootstrap[:n]
	} else {
		obj.Slots = make([]Value, n, max(8, n))
	}
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

// ExtendBootstrapSlot appends v into the inline bootstrap array at slotIdx when
// slotIdx is the next contiguous slot and still fits inline.
func (inst *PyInstance) ExtendBootstrapSlot(slotIdx int, v Value) bool {
	if slotIdx == len(inst.Slots) && slotIdx < len(inst.bootstrap) && (slotIdx == 0 || &inst.Slots[0] == &inst.bootstrap[0]) {
		inst.bootstrap[slotIdx] = v
		inst.Slots = inst.bootstrap[:slotIdx+1]
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
		case slotIdx == len(inst.Slots) && slotIdx < len(inst.bootstrap) && (slotIdx == 0 || &inst.Slots[0] == &inst.bootstrap[0]):
			inst.bootstrap[slotIdx] = v
			inst.Slots = inst.bootstrap[:slotIdx+1]
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
	case slotIdx == len(inst.Slots) && slotIdx < len(inst.bootstrap) && (slotIdx == 0 || &inst.Slots[0] == &inst.bootstrap[0]):
		inst.bootstrap[slotIdx] = v
		inst.Slots = inst.bootstrap[:slotIdx+1]
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
	if n < len(inst.bootstrap) && (n == 0 || &inst.Slots[0] == &inst.bootstrap[0]) {
		inst.bootstrap[n] = value
		return inst.bootstrap[:n+1]
	}
	if n == len(inst.bootstrap) && n > 0 && &inst.Slots[0] == &inst.bootstrap[0] {
		newSlots := make([]Value, n+1, n+4)
		copy(newSlots, inst.bootstrap[:])
		newSlots[n] = value
		return newSlots
	}
	return append(inst.Slots, value)
}
