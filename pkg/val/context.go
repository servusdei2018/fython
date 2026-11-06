package val

import "sync/atomic"

// ctxEntry is one ContextVar binding inside an immutable context snapshot.
// Published snapshots are never written, so concurrent Get calls do not allocate
// and do not race with Set.
type ctxEntry struct {
	varObj Value
	val    Value
}

type ctxSnap struct {
	m map[uintptr]ctxEntry
}

// emptyCtxSnap is shared by every fresh context. Set copies it instead of mutating it.
var emptyCtxSnap = &ctxSnap{m: map[uintptr]ctxEntry{}}

// PyContext is the copy-on-write mapping behind ContextVar.
// ShallowCopy shares the current snapshot, so task spawn is O(1).
// Set publishes a new snapshot, so ContextVar.get stays at 0 allocations.
type PyContext struct {
	snap atomic.Pointer[ctxSnap]
}

// NewPyContext returns an empty context that shares the global empty snapshot.
func NewPyContext() *PyContext {
	c := &PyContext{}
	c.snap.Store(emptyCtxSnap)
	return c
}

// ShallowCopy returns a context that shares this context's current snapshot.
func (c *PyContext) ShallowCopy() *PyContext {
	n := &PyContext{}
	if c == nil {
		n.snap.Store(emptyCtxSnap)
		return n
	}
	s := c.snap.Load()
	if s == nil {
		s = emptyCtxSnap
	}
	n.snap.Store(s)
	return n
}

// Get returns the value bound to key. The lookup does not allocate.
func (c *PyContext) Get(key uintptr) (Value, bool) {
	if c == nil {
		return Value{}, false
	}
	s := c.snap.Load()
	if s == nil {
		return Value{}, false
	}
	e, ok := s.m[key]
	if !ok {
		return Value{}, false
	}
	return e.val, true
}

// Set binds key to v and returns the previous value.
// had is false when key was unset.
func (c *PyContext) Set(key uintptr, varObj, v Value) (prev Value, had bool) {
	for {
		old := c.snap.Load()
		if old == nil {
			old = emptyCtxSnap
		}
		if e, ok := old.m[key]; ok {
			prev = e.val
			had = true
		} else {
			prev = NoneVal()
			had = false
		}
		nm := make(map[uintptr]ctxEntry, len(old.m)+1)
		for k, e := range old.m {
			nm[k] = e
		}
		nm[key] = ctxEntry{varObj: varObj, val: v}
		next := &ctxSnap{m: nm}
		if c.snap.CompareAndSwap(old, next) {
			return prev, had
		}
	}
}

// Del removes key and returns the previous value.
func (c *PyContext) Del(key uintptr) (prev Value, had bool) {
	for {
		old := c.snap.Load()
		if old == nil {
			old = emptyCtxSnap
		}
		if e, ok := old.m[key]; ok {
			prev = e.val
			had = true
		} else {
			return NoneVal(), false
		}
		nm := make(map[uintptr]ctxEntry, len(old.m))
		for k, e := range old.m {
			if k != key {
				nm[k] = e
			}
		}
		next := &ctxSnap{m: nm}
		if c.snap.CompareAndSwap(old, next) {
			return prev, had
		}
	}
}

// Len reports how many context variables are bound.
func (c *PyContext) Len() int {
	if c == nil {
		return 0
	}
	s := c.snap.Load()
	if s == nil {
		return 0
	}
	return len(s.m)
}

// Vars returns the bound ContextVar objects. Order is unspecified.
func (c *PyContext) Vars() []Value {
	if c == nil {
		return nil
	}
	s := c.snap.Load()
	if s == nil || len(s.m) == 0 {
		return nil
	}
	out := make([]Value, 0, len(s.m))
	for _, e := range s.m {
		out = append(out, e.varObj)
	}
	return out
}
