package val

import (
	"slices"
	"unsafe"
)

// DictViewKind classifies which projection of a PyDict a PyDictView exposes.
type DictViewKind uint8

const (
	DictViewKeys DictViewKind = iota
	DictViewValues
	DictViewItems
)

// PyDictView is a zero-snapshot lazy view over a PyDict (dict_keys, dict_values, dict_items).
// Header MUST remain at byte offset 0.
type PyDictView struct {
	Header   ObjectHeader
	Dict     *PyDict
	ViewKind DictViewKind
}

// NewDictViewVal constructs a lazy PyDictView Value over d.
func NewDictViewVal(d *PyDict, kind DictViewKind) Value {
	var tp *PyType
	switch kind {
	case DictViewKeys:
		tp = DictKeysType
	case DictViewValues:
		tp = DictValuesType
	default:
		tp = DictItemsType
	}
	dv := &PyDictView{
		Header:   ObjectHeader{Kind: KindDictView, Type: tp},
		Dict:     d,
		ViewKind: kind,
	}
	return Value{ptr: &dv.Header}
}

// NewDictKeysVal constructs a dict_keys view over d.
func NewDictKeysVal(d *PyDict) Value {
	return NewDictViewVal(d, DictViewKeys)
}

// NewDictValuesVal constructs a dict_values view over d.
func NewDictValuesVal(d *PyDict) Value {
	return NewDictViewVal(d, DictViewValues)
}

// NewDictItemsVal constructs a dict_items view over d.
func NewDictItemsVal(d *PyDict) Value {
	return NewDictViewVal(d, DictViewItems)
}

// IsDictView reports whether v is a lazy PyDictView.
func (v Value) IsDictView() bool {
	return v.ptr != nil && v.ptr.Kind == KindDictView
}

// AsDictView performs a zero-cost offset-0 downcast from Value to *PyDictView.
func (v Value) AsDictView() *PyDictView {
	return (*PyDictView)(unsafe.Pointer(v.ptr))
}

// dictViewElemEqual compares two values using CPython's identity-first equality
// semantics (matching PyObject_RichCompareBool: identical values including NaN are equal).
func dictViewElemEqual(ctx *ExecContext, a, b Value) bool {
	if a == b {
		return true
	}
	if ctx != nil {
		res := RichCompare(ctx, a, b, CmpEQ)
		if ctx.HasException() {
			return false
		}
		return res.IsTruthyCtx(ctx)
	}
	return Equals(a, b)
}

// dictViewContains tests `item in view` with CPython 3.15 semantics.
func dictViewContains(ctx *ExecContext, dv *PyDictView, item Value) bool {
	d := dv.Dict
	switch dv.ViewKind {
	case DictViewKeys:
		if _, bad := IsUnhashableValue(item); bad {
			return false
		}
		_, found := d.Get(item)
		return found
	case DictViewItems:
		if !item.IsTuple() {
			return false
		}
		pair := item.AsTuple().Items
		if len(pair) != 2 {
			return false
		}
		if _, bad := IsUnhashableValue(pair[0]); bad {
			return false
		}
		v, found := d.Get(pair[0])
		if !found {
			return false
		}
		return dictViewElemEqual(ctx, v, pair[1])
	default: // DictViewValues
		// Snapshot values under lock only when concurrent; otherwise scan live entries
		// without holding locks across Python __eq__ callbacks.
		if d.Live != nil || d.Shared || MultiThreadedFlag.Load() {
			for _, v := range d.Values() {
				if dictViewElemEqual(ctx, v, item) {
					return true
				}
				if ctx != nil && ctx.HasException() {
					return false
				}
			}
			return false
		}
		if sp := d.split; sp != nil {
			for i := range sp.keys.Count {
				if dictViewElemEqual(ctx, sp.vals[i], item) {
					return true
				}
				if ctx != nil && ctx.HasException() {
					return false
				}
			}
			return false
		}
		for i := range d.Entries {
			e := &d.Entries[i]
			if !e.Active {
				continue
			}
			if dictViewElemEqual(ctx, e.Value, item) {
				return true
			}
			if ctx != nil && ctx.HasException() {
				return false
			}
		}
		return false
	}
}

// isSetLikeViewOperand reports whether v is a set, frozenset, dict_keys, or dict_items,
// matching CPython's dictview_richcompare operand filter.
func isSetLikeViewOperand(v Value) bool {
	if v.IsSet() {
		return true
	}
	if v.IsDictView() {
		k := v.AsDictView().ViewKind
		return k == DictViewKeys || k == DictViewItems
	}
	return false
}

// dictViewAllIn checks whether every element of view a is contained in set-like operand b.
// Works even when dict_items holds unhashable values (no hashing of values).
func dictViewAllIn(ctx *ExecContext, a *PyDictView, b Value) bool {
	if b.IsDictView() {
		bdv := b.AsDictView()
		if a.ViewKind == DictViewKeys {
			if bdv.ViewKind == DictViewKeys {
				// Both are dict_keys: O(1) key probes.
				for _, k := range a.Dict.Keys() {
					if _, ok := bdv.Dict.Get(k); !ok {
						return false
					}
				}
				return true
			}
			// dict_keys <= dict_items: every key in a must be a 2-tuple matching (k, v) in b.
			for _, k := range a.Dict.Keys() {
				if !dictViewContains(ctx, bdv, k) {
					return false
				}
				if ctx != nil && ctx.HasException() {
					return false
				}
			}
			return true
		}
		// a is dict_items
		if bdv.ViewKind == DictViewItems {
			// Both are dict_items: O(1) key lookup + identity-first value equality (supports unhashable values!).
			for _, it := range a.Dict.Items() {
				bv, ok := bdv.Dict.Get(it.Key)
				if !ok || !dictViewElemEqual(ctx, it.Value, bv) {
					return false
				}
				if ctx != nil && ctx.HasException() {
					return false
				}
			}
			return true
		}
		// dict_items <= dict_keys: each (k, v) tuple must be a key in b.
		for _, it := range a.Dict.Items() {
			if _, bad := IsUnhashableValue(it.Value); bad {
				return false
			}
			pair := NewTupleVal(it.Key, it.Value)
			if _, ok := bdv.Dict.Get(pair); !ok {
				return false
			}
		}
		return true
	}
	// b is a PySet (set or frozenset)
	bs := b.AsSet()
	if a.ViewKind == DictViewKeys {
		for _, k := range a.Dict.Keys() {
			if !bs.Contains(k) {
				return false
			}
		}
		return true
	}
	// a is dict_items, b is set/frozenset: unhashable values cannot be in a set.
	for _, it := range a.Dict.Items() {
		if _, bad := IsUnhashableValue(it.Value); bad {
			return false
		}
		if !bs.Contains(NewTupleVal(it.Key, it.Value)) {
			return false
		}
	}
	return true
}

func setLikeLen(v Value) int {
	if v.IsSet() {
		return v.AsSet().Len()
	}
	return v.AsDictView().Dict.Len()
}

func setAllInDictView(ctx *ExecContext, s *PySet, dv *PyDictView) bool {
	for _, elem := range s.Items() {
		if !dictViewContains(ctx, dv, elem) {
			return false
		}
		if ctx != nil && ctx.HasException() {
			return false
		}
	}
	return true
}

// dictViewRichCompare implements ==, !=, <, <=, >, >= for dict_keys and dict_items.
func dictViewRichCompare(ctx *ExecContext, a, b Value, op uint8) Value {
	if !a.IsDictView() || !isSetLikeViewOperand(b) {
		return NotImplementedVal()
	}
	adv := a.AsDictView()
	if adv.ViewKind == DictViewValues {
		return NotImplementedVal()
	}
	lenA := adv.Dict.Len()
	lenB := setLikeLen(b)
	switch op {
	case CmpEQ:
		if lenA != lenB {
			return BoolVal(false)
		}
		res := dictViewAllIn(ctx, adv, b)
		if ctx != nil && ctx.HasException() {
			return NoneVal()
		}
		return BoolVal(res)
	case CmpNEQ:
		if lenA != lenB {
			return BoolVal(true)
		}
		res := dictViewAllIn(ctx, adv, b)
		if ctx != nil && ctx.HasException() {
			return NoneVal()
		}
		return BoolVal(!res)
	case CmpLT:
		if lenA >= lenB {
			return BoolVal(false)
		}
		res := dictViewAllIn(ctx, adv, b)
		if ctx != nil && ctx.HasException() {
			return NoneVal()
		}
		return BoolVal(res)
	case CmpLTE:
		if lenA > lenB {
			return BoolVal(false)
		}
		res := dictViewAllIn(ctx, adv, b)
		if ctx != nil && ctx.HasException() {
			return NoneVal()
		}
		return BoolVal(res)
	case CmpGT:
		if lenA <= lenB {
			return BoolVal(false)
		}
		var res bool
		if b.IsDictView() {
			res = dictViewAllIn(ctx, b.AsDictView(), a)
		} else {
			res = setAllInDictView(ctx, b.AsSet(), adv)
		}
		if ctx != nil && ctx.HasException() {
			return NoneVal()
		}
		return BoolVal(res)
	case CmpGTE:
		if lenA < lenB {
			return BoolVal(false)
		}
		var res bool
		if b.IsDictView() {
			res = dictViewAllIn(ctx, b.AsDictView(), a)
		} else {
			res = setAllInDictView(ctx, b.AsSet(), adv)
		}
		if ctx != nil && ctx.HasException() {
			return NoneVal()
		}
		return BoolVal(res)
	default:
		return NotImplementedVal()
	}
}

// buildSetCheckedHashable builds a PySet from any iterable, raising TypeError on unhashable items.
func buildSetCheckedHashable(ctx *ExecContext, v Value) (*PySet, bool) {
	if v.IsSet() {
		return v.AsSet().Copy(), true
	}
	if v.IsDictView() {
		dv := v.AsDictView()
		if dv.ViewKind == DictViewKeys {
			return NewSetFromDict(dv.Dict).AsSet(), true
		}
		if dv.ViewKind == DictViewItems {
			raw := dv.Dict.Items()
			s := newSetWithCap(SetType, len(raw))
			for _, it := range raw {
				if badType, bad := IsUnhashableValue(it.Value); bad {
					raiseTypeError(ctx, "unhashable type: '"+badType+"'")
					return nil, false
				}
				pair := NewTupleVal(it.Key, it.Value)
				s.Add(pair)
			}
			return s, true
		}
	}
	items := ExtractIterableItems(ctx, v)
	if ctx != nil && ctx.HasException() {
		return nil, false
	}
	s := newSetWithCap(SetType, len(items))
	for _, it := range items {
		if badType, bad := IsUnhashableValue(it); bad {
			raiseTypeError(ctx, "unhashable type: '"+badType+"'")
			return nil, false
		}
		s.Add(it)
	}
	return s, true
}

func dictViewSetOr(ctx *ExecContext, a, b Value) Value {
	s1, ok := buildSetCheckedHashable(ctx, a)
	if !ok {
		return NoneVal()
	}
	if b.IsSet() {
		return s1.Union(b.AsSet())
	}
	if b.IsDictView() && b.AsDictView().ViewKind == DictViewKeys {
		b.AsDictView().Dict.ForEachActive(func(k, _ Value) bool {
			s1.Add(k)
			return true
		})
		return Value{ptr: &s1.Header}
	}
	items := ExtractIterableItems(ctx, b)
	if ctx != nil && ctx.HasException() {
		return NoneVal()
	}
	for _, it := range items {
		if badType, bad := IsUnhashableValue(it); bad {
			return raiseTypeError(ctx, "unhashable type: '"+badType+"'")
		}
		s1.Add(it)
	}
	return Value{ptr: &s1.Header}
}

func dictViewSetAnd(ctx *ExecContext, a, b Value) Value {
	// Fast path: dict_keys & dict_keys without allocating two intermediate sets.
	if a.IsDictView() && a.AsDictView().ViewKind == DictViewKeys &&
		b.IsDictView() && b.AsDictView().ViewKind == DictViewKeys {
		da, db := a.AsDictView().Dict, b.AsDictView().Dict
		if da.Len() > db.Len() {
			da, db = db, da
		}
		res := newEmptySetFor(SetType)
		da.ForEachActive(func(k, _ Value) bool {
			if _, ok := db.Get(k); ok {
				res.Add(k)
			}
			return true
		})
		return Value{ptr: &res.Header}
	}
	s1, ok := buildSetCheckedHashable(ctx, a)
	if !ok {
		return NoneVal()
	}
	s2, ok := buildSetCheckedHashable(ctx, b)
	if !ok {
		return NoneVal()
	}
	return s1.Intersection(s2)
}

func dictViewSetSub(ctx *ExecContext, a, b Value) Value {
	if a.IsDictView() && a.AsDictView().ViewKind == DictViewKeys &&
		b.IsDictView() && b.AsDictView().ViewKind == DictViewKeys {
		da, db := a.AsDictView().Dict, b.AsDictView().Dict
		res := newEmptySetFor(SetType)
		da.ForEachActive(func(k, _ Value) bool {
			if _, ok := db.Get(k); !ok {
				res.Add(k)
			}
			return true
		})
		return Value{ptr: &res.Header}
	}
	s1, ok := buildSetCheckedHashable(ctx, a)
	if !ok {
		return NoneVal()
	}
	s2, ok := buildSetCheckedHashable(ctx, b)
	if !ok {
		return NoneVal()
	}
	return s1.Difference(s2)
}

func dictViewSetXor(ctx *ExecContext, a, b Value) Value {
	s1, ok := buildSetCheckedHashable(ctx, a)
	if !ok {
		return NoneVal()
	}
	s2, ok := buildSetCheckedHashable(ctx, b)
	if !ok {
		return NoneVal()
	}
	return s1.SymmetricDifference(s2)
}

func dictViewIsDisjoint(ctx *ExecContext, self Value, other Value) Value {
	dv := self.AsDictView()
	if dv.Dict.Len() == 0 {
		// Still validate that other is iterable.
		if !other.IsDictView() && !other.IsSet() && !other.IsList() && !other.IsTuple() && !other.IsDict() {
			ExtractIterableItems(ctx, other)
			if ctx != nil && ctx.HasException() {
				return NoneVal()
			}
		}
		return BoolVal(true)
	}
	if other.IsSet() {
		os := other.AsSet()
		if os.Len() == 0 {
			return BoolVal(true)
		}
		if dv.ViewKind == DictViewKeys && dv.Dict.Len() <= os.Len() {
			return BoolVal(!slices.ContainsFunc(dv.Dict.Keys(), os.Contains))
		}
		for _, elem := range os.Items() {
			if dictViewContains(ctx, dv, elem) {
				return BoolVal(false)
			}
			if ctx != nil && ctx.HasException() {
				return NoneVal()
			}
		}
		return BoolVal(true)
	}
	if other.IsDictView() {
		odv := other.AsDictView()
		if odv.Dict.Len() == 0 {
			return BoolVal(true)
		}
		if dv.ViewKind == DictViewKeys && odv.ViewKind == DictViewKeys {
			da, db := dv.Dict, odv.Dict
			if da.Len() > db.Len() {
				da, db = db, da
			}
			for _, k := range da.Keys() {
				if _, ok := db.Get(k); ok {
					return BoolVal(false)
				}
			}
			return BoolVal(true)
		}
	}
	items := ExtractIterableItems(ctx, other)
	if ctx != nil && ctx.HasException() {
		return NoneVal()
	}
	for _, elem := range items {
		if dictViewContains(ctx, dv, elem) {
			return BoolVal(false)
		}
		if ctx != nil && ctx.HasException() {
			return NoneVal()
		}
	}
	return BoolVal(true)
}

func installDictViewCommon(tp *PyType, fwdKind, revKind IterKind) {
	tp.Slots.SqLen = func(ctx *ExecContext, self Value) int64 {
		return int64(self.AsDictView().Dict.Len())
	}
	tp.Slots.TpIter = func(ctx *ExecContext, self Value) Value {
		return NewDictViewIterVal(self.AsDictView().Dict, fwdKind)
	}
	tp.Dict.Set(DunderLen, NewBuiltinFuncVal("__len__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			return raiseTypeError(ctx, "__len__() missing self")
		}
		return Int64Val(int64(args[0].AsDictView().Dict.Len()))
	}))
	tp.Dict.Set(DunderIter, NewBuiltinFuncVal("__iter__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			return raiseTypeError(ctx, "__iter__() missing self")
		}
		return NewDictViewIterVal(args[0].AsDictView().Dict, fwdKind)
	}))
	tp.Dict.Set(InternString("__reversed__"), NewBuiltinFuncVal("__reversed__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			return raiseTypeError(ctx, "__reversed__() missing self")
		}
		return NewDictViewIterVal(args[0].AsDictView().Dict, revKind)
	}))
	tp.Dict.Set(DunderContains, NewBuiltinFuncVal("__contains__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return raiseTypeError(ctx, "__contains__() takes 1 argument")
		}
		res := dictViewContains(ctx, args[0].AsDictView(), args[1])
		if ctx != nil && ctx.HasException() {
			return NoneVal()
		}
		return BoolVal(res)
	}))
	tp.Dict.Set(InternString("__hash__"), NoneVal())
	// Read-only .mapping property returning mappingproxy(d)
	mappingGetter := NewBuiltinFuncVal("mapping", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 1 {
			return raiseTypeError(ctx, "descriptor 'mapping' needs an argument")
		}
		return NewMappingProxyVal(DictVal(args[0].AsDictView().Dict))
	})
	tp.Dict.Set(InternString("mapping"), NewPropertyVal(mappingGetter, NoneVal(), NoneVal()))
	tp.InvalidateVersion()
}

func installDictViewSetOps(tp *PyType) {
	tp.Slots.NbSub = dictViewSetSub
	tp.Dict.Set(DunderSub, NewBuiltinFuncVal("__sub__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return raiseTypeError(ctx, "__sub__() takes 1 argument")
		}
		return dictViewSetSub(ctx, args[0], args[1])
	}))
	tp.Dict.Set(DunderRSub, NewBuiltinFuncVal("__rsub__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return raiseTypeError(ctx, "__rsub__() takes 1 argument")
		}
		return dictViewSetSub(ctx, args[1], args[0])
	}))
	tp.Dict.Set(strOr, NewBuiltinFuncVal("__or__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return raiseTypeError(ctx, "__or__() takes 1 argument")
		}
		return dictViewSetOr(ctx, args[0], args[1])
	}))
	tp.Dict.Set(DunderROr, NewBuiltinFuncVal("__ror__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return raiseTypeError(ctx, "__ror__() takes 1 argument")
		}
		return dictViewSetOr(ctx, args[1], args[0])
	}))
	tp.Dict.Set(strAnd, NewBuiltinFuncVal("__and__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return raiseTypeError(ctx, "__and__() takes 1 argument")
		}
		return dictViewSetAnd(ctx, args[0], args[1])
	}))
	tp.Dict.Set(DunderRAnd, NewBuiltinFuncVal("__rand__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return raiseTypeError(ctx, "__rand__() takes 1 argument")
		}
		return dictViewSetAnd(ctx, args[1], args[0])
	}))
	tp.Dict.Set(strXor, NewBuiltinFuncVal("__xor__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return raiseTypeError(ctx, "__xor__() takes 1 argument")
		}
		return dictViewSetXor(ctx, args[0], args[1])
	}))
	tp.Dict.Set(DunderRXor, NewBuiltinFuncVal("__rxor__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return raiseTypeError(ctx, "__rxor__() takes 1 argument")
		}
		return dictViewSetXor(ctx, args[1], args[0])
	}))
	tp.Dict.Set(InternString("isdisjoint"), NewBuiltinFuncVal("isdisjoint", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) != 2 {
			return raiseTypeError(ctx, "isdisjoint() takes exactly one argument")
		}
		return dictViewIsDisjoint(ctx, args[0], args[1])
	}))
	tp.Dict.Set(DunderEq, NewBuiltinFuncVal("__eq__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		return dictViewRichCompare(ctx, args[0], args[1], CmpEQ)
	}))
	tp.Dict.Set(DunderNe, NewBuiltinFuncVal("__ne__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		return dictViewRichCompare(ctx, args[0], args[1], CmpNEQ)
	}))
	tp.Dict.Set(DunderLt, NewBuiltinFuncVal("__lt__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		return dictViewRichCompare(ctx, args[0], args[1], CmpLT)
	}))
	tp.Dict.Set(DunderLe, NewBuiltinFuncVal("__le__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		return dictViewRichCompare(ctx, args[0], args[1], CmpLTE)
	}))
	tp.Dict.Set(DunderGt, NewBuiltinFuncVal("__gt__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		return dictViewRichCompare(ctx, args[0], args[1], CmpGT)
	}))
	tp.Dict.Set(DunderGe, NewBuiltinFuncVal("__ge__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		return dictViewRichCompare(ctx, args[0], args[1], CmpGTE)
	}))
	tp.InvalidateVersion()
}

func initDictViewTypes() {
	DictKeysType = MustNewType("dict_keys", []*PyType{ObjectType}, nil)
	installDictViewCommon(DictKeysType, IterKindDict, IterKindDictRevKeys)
	installDictViewSetOps(DictKeysType)

	DictValuesType = MustNewType("dict_values", []*PyType{ObjectType}, nil)
	installDictViewCommon(DictValuesType, IterKindDictValues, IterKindDictRevValues)

	DictItemsType = MustNewType("dict_items", []*PyType{ObjectType}, nil)
	installDictViewCommon(DictItemsType, IterKindDictItems, IterKindDictRevItems)
	installDictViewSetOps(DictItemsType)
}
