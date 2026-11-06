// Package pyast is a CPython 3.15 shaped Python parser, generic AST and
// unparser. It has no dependency on the VM: the vm package bridges Nodes to
// `_ast` objects.
package pyast

import (
	"fmt"
	"math/big"
)

// Field describes one ASDL field.
type Field struct {
	Name string
	Type string // ASDL type: "expr", "stmt", "str", "int", "object", ...
	List bool
	Opt  bool
}

// Spec describes one ASDL node class.
type Spec struct {
	Name   string
	Base   string
	Fields []Field
	Attrs  []string
	ID     int
	// Concrete is true for node classes that can be instantiated by the parser
	// (everything with a non-abstract role).
	Concrete bool

	fieldIdx map[string]int
}

var (
	specByName = map[string]*Spec{}
	// Specs lists every node class, bases first.
	Specs []*Spec
)

func init() {
	for i := range specTable {
		s := &specTable[i]
		s.ID = i
		s.fieldIdx = make(map[string]int, len(s.Fields))
		for j, f := range s.Fields {
			s.fieldIdx[f.Name] = j
		}
		specByName[s.Name] = s
		Specs = append(Specs, s)
	}
	for _, s := range Specs {
		s.Concrete = s.Base != "" || len(s.Fields) > 0
	}
	for _, n := range []string{"expr_context", "boolop", "operator", "unaryop", "cmpop"} {
		_ = n
	}
}

// Lookup returns the spec for a class name, or nil.
func Lookup(name string) *Spec { return specByName[name] }

// FieldIndex returns the index of the named field or -1.
func (s *Spec) FieldIndex(name string) int {
	if i, ok := s.fieldIdx[name]; ok {
		return i
	}
	return -1
}

// IsA reports whether s is the class base or derives from it.
func (s *Spec) IsA(base string) bool {
	for c := s; c != nil; c = specByName[c.Base] {
		if c.Name == base {
			return true
		}
		if c.Base == "" {
			break
		}
	}
	return false
}

// Constant value types (Node.F[i] for Constant.value).
type (
	// NoneT is Python None.
	NoneT struct{}
	// EllipsisT is Python Ellipsis.
	EllipsisT struct{}
	// TupleT is a constant tuple.
	TupleT []any
	// FrozenSetT is a constant frozenset.
	FrozenSetT []any
)

// Position attribute indexes.
const (
	PLineno = iota
	PCol
	PEndLineno
	PEndCol
)

// Node is a generic AST node. Field values are one of:
//
//	*Node            node field
//	[]*Node          list-of-node field (elements may be nil, e.g. Dict.keys)
//	[]string         list of identifiers
//	string           identifier / str field
//	int              int field
//	nil              missing optional field
//	Constant values  (Constant.value only)
type Node struct {
	Spec   *Spec
	F      []any
	Pos    [4]int
	HasPos uint8 // bit i set when Pos[i] is valid
}

// New creates a node of the named class with the given positional field values.
func New(name string, fields ...any) *Node {
	s := specByName[name]
	if s == nil {
		panic("pyast: unknown node " + name)
	}
	if len(fields) != len(s.Fields) {
		panic(fmt.Sprintf("pyast: %s expects %d fields, got %d", name, len(s.Fields), len(fields)))
	}
	n := &Node{Spec: s, F: make([]any, len(fields))}
	copy(n.F, fields)
	for i, f := range s.Fields {
		if f.List && n.F[i] == nil {
			if f.Type == "str" {
				n.F[i] = []string(nil)
			} else {
				n.F[i] = []*Node(nil)
			}
		}
	}
	return n
}

// NewEmpty creates a node with all fields unset.
func NewEmpty(s *Spec) *Node {
	n := &Node{Spec: s, F: make([]any, len(s.Fields))}
	for i, f := range s.Fields {
		if f.List {
			if f.Type == "str" {
				n.F[i] = []string(nil)
			} else {
				n.F[i] = []*Node(nil)
			}
		}
	}
	return n
}

// Name returns the class name.
func (n *Node) Name() string { return n.Spec.Name }

// Get returns a field by name (nil if absent).
func (n *Node) Get(name string) any {
	if i := n.Spec.FieldIndex(name); i >= 0 {
		return n.F[i]
	}
	return nil
}

// Set assigns a field by name.
func (n *Node) Set(name string, v any) {
	i := n.Spec.FieldIndex(name)
	if i < 0 {
		panic("pyast: no field " + name + " on " + n.Spec.Name)
	}
	n.F[i] = v
}

// Node returns a node field.
func (n *Node) Node(name string) *Node {
	v, _ := n.Get(name).(*Node)
	return v
}

// List returns a list-of-nodes field.
func (n *Node) List(name string) []*Node {
	v, _ := n.Get(name).([]*Node)
	return v
}

// Str returns a string field.
func (n *Node) Str(name string) string {
	v, _ := n.Get(name).(string)
	return v
}

// Int returns an int field (0 if unset).
func (n *Node) Int(name string) int {
	v, _ := n.Get(name).(int)
	return v
}

// SetPos sets all four position attributes.
func (n *Node) SetPos(l, c, el, ec int) *Node {
	n.Pos = [4]int{l, c, el, ec}
	n.HasPos = 0xF
	return n
}

// Has reports whether position attribute i is set.
func (n *Node) Has(i int) bool { return n.HasPos&(1<<uint(i)) != 0 }

// Lineno etc. are convenience accessors (0 when unset).
func (n *Node) Lineno() int    { return n.Pos[PLineno] }
func (n *Node) ColOffset() int { return n.Pos[PCol] }
func (n *Node) EndLineno() int { return n.Pos[PEndLineno] }
func (n *Node) EndCol() int    { return n.Pos[PEndCol] }

// BigOrInt normalizes an integer constant.
func BigOrInt(b *big.Int) any {
	if b.IsInt64() {
		return b.Int64()
	}
	return b
}

// Walk calls fn for n and every descendant node in field order. If fn returns
// false the node's children are skipped.
func Walk(n *Node, fn func(*Node) bool) {
	if n == nil || !fn(n) {
		return
	}
	for _, f := range n.F {
		switch v := f.(type) {
		case *Node:
			Walk(v, fn)
		case []*Node:
			for _, c := range v {
				Walk(c, fn)
			}
		}
	}
}
