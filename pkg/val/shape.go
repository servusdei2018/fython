package val

import (
	"sync"
	"sync/atomic"
	"unsafe"
)

var nextShapeID uint32

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
	AttrName    Value
	SlotIndex   int
	Attrs       []ShapeAttr
	mu          sync.RWMutex
	Transitions []ShapeTransition
}

// NewRootShape allocates a new empty root Shape with a unique non-zero ID.
func NewRootShape() *Shape {
	return &Shape{
		ID:        allocShapeID(),
		SlotIndex: -1,
	}
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
	if name.ptr.Kind == KindString && (name.ptr.Flags&FlagInterned) == 0 {
		for i := range attrs {
			if StringEquals(attrs[i].Name, name) {
				return attrs[i].Index, true
			}
		}
	}
	return -1, false
}

// Transition returns the child Shape resulting from adding attribute name to s.
// If a transition for name already exists, the existing child Shape is returned.
func (s *Shape) Transition(name Value) *Shape {
	interned := InternValue(name)

	s.mu.RLock()
	for i := range s.Transitions {
		t := &s.Transitions[i]
		if t.Name.ptr == interned.ptr && t.Name.num == interned.num {
			target := t.Target
			s.mu.RUnlock()
			return target
		}
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.Transitions {
		t := &s.Transitions[i]
		if t.Name.ptr == interned.ptr && t.Name.num == interned.num {
			return t.Target
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
		AttrName:  interned,
		SlotIndex: newSlotIdx,
		Attrs:     newAttrs,
	}
	s.Transitions = append(s.Transitions, ShapeTransition{
		Name:   interned,
		Target: child,
	})
	return child
}

// PyInstance represents a Python object instance backed by a Hidden Class Shape
// and a flat contiguous slice of attribute Values (never backed by a PyDict or Go map).
// Header MUST be at offset 0.
type PyInstance struct {
	Header ObjectHeader
	Shape  *Shape
	Slots  []Value
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

// GetAttr looks up an attribute by name on the instance using its Shape.
func (inst *PyInstance) GetAttr(name Value) (Value, bool) {
	idx, ok := inst.Shape.Lookup(name)
	if !ok {
		return NoneVal(), false
	}
	return inst.Slots[idx], true
}

// SetAttr sets or adds an attribute on the instance, transitioning its Shape if the attribute is new.
// Returns the resolved slot index.
func (inst *PyInstance) SetAttr(name, value Value) int {
	if idx, ok := inst.Shape.Lookup(name); ok {
		inst.Slots[idx] = value
		return idx
	}
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
