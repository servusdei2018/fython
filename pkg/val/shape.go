package val

import (
	"sync"
	"sync/atomic"
	"unsafe"
)

var (
	nextShapeID     uint32
	isMultiThreaded atomic.Bool
)

// SetMultiThreaded enables or disables multi-threaded mode for instance slots.
func SetMultiThreaded(v bool) {
	isMultiThreaded.Store(v)
}

// IsMultiThreaded reports whether concurrent execution is active.
func IsMultiThreaded() bool {
	return isMultiThreaded.Load()
}

func allocShapeID() uint32 {
	return atomic.AddUint32(&nextShapeID, 1)
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

// Lookup resolves an attribute name to its flat slot index in O(N_attrs) without heap allocations.
// For interned strings and inline strings, comparison is a single pointer+num check.
func (s *Shape) Lookup(name Value) (int, bool) {
	attrs := s.Attrs
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

// PyInstance represents a Python object instance backed by a Hidden Class Shape
// and a flat contiguous slice of attribute Values (never backed by a PyDict or Go map).
// Header MUST be at offset 0.
type PyInstance struct {
	Header ObjectHeader
	Shape  *Shape
	Slots  []Value
	mu     sync.RWMutex
}

// Compile-time assertion that PyInstance.Header is at byte offset 0.
var _ [0 - unsafe.Offsetof(PyInstance{}.Header)]byte

// NewInstanceVal constructs a new heap-allocated KindInstance Value with the given initial Shape.
// If shape is nil, DefaultRootShape() is used.
func NewInstanceVal(shape *Shape) Value {
	if shape == nil {
		shape = defaultRootShape
	}
	n := len(shape.Attrs)
	slotCap := 8
	if n > slotCap {
		slotCap = n
	}
	obj := &PyInstance{
		Header: ObjectHeader{Kind: KindInstance, Type: ObjectType},
		Shape:  shape,
		Slots:  make([]Value, n, slotCap),
	}
	return Value{num: 0, ptr: &obj.Header}
}

// NewInstanceOfType constructs a new heap-allocated KindInstance Value belonging to cls.
func NewInstanceOfType(cls *PyType) Value {
	if cls == nil {
		cls = ObjectType
	}
	if ModuleType != nil && IsSubclass(cls, ModuleType) {
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
	slotCap := 8
	if n > slotCap {
		slotCap = n
	}
	obj := &PyInstance{
		Header: ObjectHeader{Kind: KindInstance, Type: cls},
		Shape:  shape,
		Slots:  make([]Value, n, slotCap),
	}
	return Value{num: 0, ptr: &obj.Header}
}

// AsInstance performs a zero-cost offset-0 downcast from Value to *PyInstance.
func (v Value) AsInstance() *PyInstance {
	return (*PyInstance)(unsafe.Pointer(v.ptr))
}

// GetSlot returns the Value at slot index idx.
func (inst *PyInstance) GetSlot(idx int) (Value, bool) {
	if !isMultiThreaded.Load() {
		if uint(idx) < uint(len(inst.Slots)) {
			return inst.Slots[idx], true
		}
		return NoneVal(), false
	}
	inst.mu.RLock()
	if idx < 0 || idx >= len(inst.Slots) {
		inst.mu.RUnlock()
		return NoneVal(), false
	}
	val := inst.Slots[idx]
	inst.mu.RUnlock()
	return val, true
}

// SetSlot sets the Value at slot index idx.
func (inst *PyInstance) SetSlot(idx int, val Value) bool {
	if !isMultiThreaded.Load() {
		if uint(idx) < uint(len(inst.Slots)) {
			inst.Slots[idx] = val
			return true
		}
		return false
	}
	inst.mu.Lock()
	if idx < 0 || idx >= len(inst.Slots) {
		inst.mu.Unlock()
		return false
	}
	inst.Slots[idx] = val
	inst.mu.Unlock()
	return true
}

// GetAttr looks up an attribute by name on the instance using its Shape.
func (inst *PyInstance) GetAttr(name Value) (Value, bool) {
	if !isMultiThreaded.Load() {
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
	inst.mu.RLock()
	idx, ok := inst.Shape.Lookup(name)
	if !ok || idx >= len(inst.Slots) {
		inst.mu.RUnlock()
		return NoneVal(), false
	}
	val := inst.Slots[idx]
	inst.mu.RUnlock()
	if val.IsUnbound() {
		return NoneVal(), false
	}
	return val, true
}

// DeleteAttr removes an attribute from the instance by marking its slot unbound.
func (inst *PyInstance) DeleteAttr(name Value) bool {
	if !isMultiThreaded.Load() {
		idx, ok := inst.Shape.Lookup(name)
		if !ok || uint(idx) >= uint(len(inst.Slots)) || inst.Slots[idx].IsUnbound() {
			return false
		}
		inst.Slots[idx] = UnboundVal()
		return true
	}
	inst.mu.Lock()
	defer inst.mu.Unlock()
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
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if idx, ok := inst.Shape.Lookup(name); ok {
		if idx < len(inst.Slots) {
			inst.Slots[idx] = value
		} else {
			inst.Slots = append(inst.Slots, value)
		}
		return idx
	}
	if inst.Header.Type != nil && (inst.Header.Type.Flags&uint32(FlagForbidDynamicAttrs)) != 0 {
		if BaseExceptionType != nil && IsSubclass(inst.Header.Type, BaseExceptionType) {
			switch name.AsString() {
			case "__traceback__", "__cause__", "__context__", "__suppress_context__":
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
		inst.Slots = append(inst.Slots, value)
	}
	inst.Shape = newShape
	return slotIdx
}
