package vm

import (
	"sync"

	"github.com/servusdei2018/fython/pkg/val"
)

// localState holds the per-thread attribute dictionaries of a threading.local
// object.
//
// CPython stores the attributes of a local object in a dictionary that is
// swapped out whenever the owning thread changes. The same is modelled here
// with one dictionary per thread identifier.
type localState struct {
	mu    sync.Mutex
	store map[int64]*val.PyDict
}

// localStateMap maps a local object to its per-thread attribute store.
var localStateMap sync.Map // map[*val.PyInstance]*localState

// localStateOf returns the per-thread attribute store of a local object,
// creating it on first use.
func localStateOf(inst *val.PyInstance) *localState {
	if v, ok := localStateMap.Load(inst); ok {
		return v.(*localState)
	}
	st := &localState{store: map[int64]*val.PyDict{}}
	actual, _ := localStateMap.LoadOrStore(inst, st)
	return actual.(*localState)
}

// threadAttrs returns the attribute dictionary that belongs to the given
// thread, creating it when the thread touches the object for the first time.
func (st *localState) threadAttrs(threadID int64) *val.PyDict {
	st.mu.Lock()
	defer st.mu.Unlock()
	d, ok := st.store[threadID]
	if !ok {
		d = val.NewDictVal(8).AsDict()
		st.store[threadID] = d
	}
	return d
}

// initLocalType creates the threading.local type.
//
// The attributes of a local object live in a per-thread dictionary, so every
// thread sees its own values. Attribute access is routed through the
// __getattr__ and __setattr__ hooks of the type.
func initLocalType() *val.PyType {
	dict := val.NewDictVal(4).AsDict()
	localType := val.MustNewType("local", []*val.PyType{val.ObjectType}, dict)
	localType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		inst := val.NewInstanceOfType(localType)
		localStateOf(inst.AsInstance())
		return inst
	}
	dict.Set(val.InternString("__getattr__"), val.NewBuiltinFuncVal("__getattr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: __getattr__() missing attribute name")
		}
		self := args[0]
		name := args[1]
		if !self.IsInstance() {
			panic("TypeError: __getattr__() called on a non-local object")
		}
		attrs := localStateOf(self.AsInstance()).threadAttrs(getCurrentThreadIdent(ctx))
		if v, ok := attrs.Get(val.InternValue(name)); ok {
			return v
		}
		panic("AttributeError: '" + self.Type().Name + "' object has no attribute '" + name.AsString() + "'")
	}))
	dict.Set(val.InternString("__setattr__"), val.NewBuiltinFuncVal("__setattr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			panic("TypeError: __setattr__() missing attribute name or value")
		}
		self := args[0]
		name := args[1]
		value := args[2]
		if !self.IsInstance() {
			panic("TypeError: __setattr__() called on a non-local object")
		}
		attrs := localStateOf(self.AsInstance()).threadAttrs(getCurrentThreadIdent(ctx))
		attrs.Set(val.InternValue(name), value)
		return val.NoneVal()
	}))
	localType.InvalidateVersion()
	return localType
}
