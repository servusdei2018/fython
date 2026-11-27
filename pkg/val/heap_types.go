package val

import (
	"errors"
	"math/big"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"unicode/utf8"
	"unsafe"
	"weak"
)

// PyBigInt wraps an arbitrary-precision math/big.Int on the heap.
// Header MUST be at offset 0.
type PyBigInt struct {
	Header ObjectHeader
	Val    big.Int
}

// PyString wraps a heap-allocated string (> 7 bytes).
// Header MUST be at offset 0.
type PyString struct {
	Header ObjectHeader
	Data   string
	// RuneCache holds a lazily computed []rune view of Data. It is immutable
	// once published, so concurrent readers may use it without locking.
	RuneCache atomic.Pointer[[]rune]
	Buf       []byte
	// BufClaimed records that the spare capacity of Buf has been handed over
	// to a derived string. It is manipulated with sync/atomic only, so that
	// two VMs concatenating the very same string can never both mutate the
	// shared backing bytes.
	BufClaimed uint32
	// asciiState caches pure-ASCII classification (0=unknown, 1=ASCII, 2=non-ASCII)
	// in the 4-byte tail padding after BufClaimed (0 extra struct bytes).
	asciiState atomic.Uint32
}

// PyList represents a dynamic array of Value on the heap.
// Header MUST be at offset 0.
type PyList struct {
	Header ObjectHeader
	Items  []Value
	Attrs  *PyDict
	mu     sync.RWMutex
}

// DictEntry represents a key-value pair stored in the dense, insertion-ordered PyDict array.
type DictEntry struct {
	Hash   uint32
	Active bool
	Key    Value
	Value  Value
}

const (
	dictIndexEmpty  = 0
	dictIndexDummy  = 1
	dictIndexOffset = 2
)

// SplitDictKeys holds an immutable shared key/hash/index table for PEP 412
// split-table constant-key dicts (2..16 interned-string or primitive keys).
type SplitDictKeys struct {
	Keys         [16]Value
	Hashes       [16]uint32
	Indices      [32]byte
	Count        int
	Mask         uint32
	Promotions   atomic.Uint32
	DisableSplit atomic.Bool
}

// splitDictData links a split-table PyDict to its immutable shared keys and
// compact value array (allocated inline inside splitDictBundle4/8/16).
type splitDictData struct {
	keys *SplitDictKeys
	vals []Value
}

// splitDictBundle4/8/16 fuse a PyDict, its splitDictData header, and its
// compact value array into a single heap allocation with PyDict.Header at
// byte offset 0.
type splitDictBundle4 struct {
	dict PyDict
	data splitDictData
	vals [4]Value
}

type splitDictBundle8 struct {
	dict PyDict
	data splitDictData
	vals [8]Value
}

type splitDictBundle16 struct {
	dict PyDict
	data splitDictData
	vals [16]Value
}

// PyDict represents a Hettinger-style compact open-addressed dictionary with offset-0 ObjectHeader.
// It separates the sparse hash index table (indices, dynamically sized as uint8, uint16, or uint32 per slot)
// from the dense insertion-ordered Entries slice, or points split at a shared-key PEP 412 table.
// Header MUST be at offset 0.
type PyDict struct {
	Header  ObjectHeader
	Entries []DictEntry
	indices []byte
	Attrs   *PyDict
	// Live, when non-nil, turns this dict into a zero-copy view over a
	// Shape-backed instance: every read and write is resolved through
	// Live.Shape and Live.Slots (see NewInstanceDictProxy).
	Live *PyInstance
	// owner is set when this dict is the namespace of a type. Every mutation
	// then invalidates the owner version tag (and its subclasses), so lookup
	// caches never need to trust call sites to invalidate explicitly.
	owner      *PyType
	split      *splitDictData
	VersionTag uint64
	used       int32
	filled     int32
	mu         sync.RWMutex
	mask       uint32
	indexShift uint8 // 0: uint8 slots, 1: uint16 slots, 2: uint32 slots
	// Shared marks a dictionary that several VMs can reach at the same
	// time, such as the builtins of an engine. Such a dictionary always
	// takes its lock, even when the runtime believes it is
	// single-threaded.
	Shared bool
}

// PyCell wraps a shared mutable Value on the heap for closures and nonlocal variables.
// Header MUST be at offset 0.
type PyCell struct {
	Header ObjectHeader
	Value  Value
	mu     sync.RWMutex
}

// PyChan represents a Go channel wrapped as a first-class Python Value.
// Header MUST be at offset 0.
type PyChan struct {
	Header ObjectHeader
	Ch     chan Value
	closed atomic.Uint32
}

// PyModule represents a Python module or package object.
// Header MUST be at offset 0.
type PyModule struct {
	Header  ObjectHeader
	Name    string
	File    string
	Dict    *PyDict
	Package string
}

// PyBytes represents an immutable sequence of bytes on the heap.
// Header MUST be at offset 0.
type PyBytes struct {
	Header ObjectHeader
	Data   []byte
	// RuneCache holds a lazily computed []rune view of Data.
	RuneCache atomic.Pointer[[]rune]
}

// PyByteArray represents a mutable sequence of bytes on the heap.
// Header MUST be at offset 0.
type PyByteArray struct {
	Header ObjectHeader
	Data   []byte
	mu     sync.RWMutex
}

// The access modes of the mmap module.
const (
	ACCESS_READ    = 1
	ACCESS_WRITE   = 2
	ACCESS_COPY    = 3
	ACCESS_DEFAULT = 0
)

// The mapping flags of the mmap module.
const (
	MAP_SHARED  = 1
	MAP_PRIVATE = 2
)

// The protection flags of the mmap module.
const (
	PROT_READ  = 1
	PROT_WRITE = 2
	PROT_EXEC  = 4
)

// PyMemoryView represents a zero-copy buffer view on the heap (PEP 3118).
// Header MUST be at offset 0.
type PyMemoryView struct {
	Header   ObjectHeader
	Obj      Value
	Buf      []byte
	ReadOnly bool
	ItemSize int
	released bool
	// PEP 3118 typed / multi-dimensional view metadata.
	Format string
	NDim   int
	// Dim0 and Stride0 hold the extent and the stride of the first dimension
	// inline. The common 1-D case therefore needs no Shape/Strides slices at
	// all, which keeps slicing allocation-free apart from the view itself.
	Dim0    int
	Stride0 int
	// Shape and Strides are only allocated for views with more than one
	// dimension.
	Shape      []int
	Strides    []int
	Suboffsets []int
	// Escaped marks a view that is reachable from somewhere other than the
	// single frame slot that owns it. Such a view is never recycled, because
	// the slot that owns it may be overwritten while the other reference is
	// still live.
	Escaped bool
	// OwnerKey identifies the frame slot that owns the view: the frame index
	// shifted left by 16 bits plus the slot index. Zero means that no slot
	// owns the view yet.
	OwnerKey uint32
}

// Dims returns the extent of every dimension of the view.
func (mv *PyMemoryView) Dims() []int {
	if mv == nil {
		return nil
	}
	if len(mv.Shape) > 0 {
		return mv.Shape
	}
	if mv.NDim <= 1 {
		if mv.NDim == 0 {
			return nil
		}
		return []int{mv.Dim0}
	}
	return nil
}

// Dim returns the extent of the i-th dimension of the view.
func (mv *PyMemoryView) Dim(i int) int {
	if mv == nil || i < 0 {
		return 0
	}
	if len(mv.Shape) > 0 {
		if i < len(mv.Shape) {
			return mv.Shape[i]
		}
		return 0
	}
	if i == 0 {
		return mv.Dim0
	}
	return 0
}

// Stride returns the stride of the i-th dimension of the view, in bytes.
func (mv *PyMemoryView) Stride(i int) int {
	if mv == nil || i < 0 {
		return 0
	}
	if len(mv.Strides) > 0 {
		if i < len(mv.Strides) {
			return mv.Strides[i]
		}
		return 0
	}
	if i == 0 {
		return mv.Stride0
	}
	return 0
}

// SetShape installs the shape and strides of a view, keeping the inline
// Dim0/Stride0 fields in sync for the 1-D case.
func (mv *PyMemoryView) SetShape(shape []int, strides []int) {
	if mv.NDim <= 1 {
		// 1-D views keep their extent and stride inline, so no Shape or
		// Strides slice has to be allocated for them.
		mv.Shape = nil
		mv.Strides = nil
		itemSize := mv.ItemSize
		if itemSize <= 0 {
			itemSize = 1
		}
		mv.Dim0 = 0
		if len(shape) > 0 {
			mv.Dim0 = shape[0]
		} else if mv.NDim == 1 {
			mv.Dim0 = len(mv.Buf) / itemSize
		}
		mv.Stride0 = itemSize
		if len(strides) > 0 {
			mv.Stride0 = strides[0]
		}
		return
	}
	mv.Shape = shape
	mv.Strides = strides
	mv.Dim0 = 0
	mv.Stride0 = 0
	if len(shape) > 0 {
		mv.Dim0 = shape[0]
	}
	if len(strides) > 0 {
		mv.Stride0 = strides[0]
	}
}

// PyCInstance represents an instance of a C extension type (PyType_FromSpec / static PyTypeObject).
// Header MUST be at offset 0. The C payload lives off-heap in ShadowArena; this Go struct
// holds only the bookkeeping (dict for Python subclass attrs, dealloc tracking).
type PyCInstance struct {
	Header        ObjectHeader
	Dict          *PyDict // lazily created for Python subclass __dict__
	DeallocCalled bool
	ArenaPtr      unsafe.Pointer // off-heap PyObject* (base, prefix-adjusted view)
}

// PySlice represents a Python slice(start, stop, step) object.
// Header MUST be at offset 0.
type PySlice struct {
	Header ObjectHeader
	Start  Value
	Stop   Value
	Step   Value
}

// Compile-time assertion that PyModule.Header is at byte offset 0.
var _ [0 - unsafe.Offsetof(PyModule{}.Header)]byte

// NewModuleVal wraps a newly created PyModule as a Value of KindModule.
func NewModuleVal(name, file string) Value {
	mod := &PyModule{
		Header: ObjectHeader{
			Kind: KindModule,
			Type: ModuleType,
		},
		Name: name,
		File: file,
		Dict: NewDictVal(16).AsDict(),
	}
	mod.Dict.Set(strName, NewStringVal(name))
	if file != "" {
		mod.Dict.Set(strFile, NewStringVal(file))
	} else {
		mod.Dict.Set(strFile, NoneVal())
	}
	mod.Dict.Set(strDoc, NoneVal())
	return ObjectVal(&mod.Header)
}

// IterKind classifies the source collection of a PyIter.
type IterKind uint8

const (
	IterKindInvalid IterKind = iota
	IterKindList
	IterKindTuple
	IterKindDict
	IterKindRange
	IterKindFunc
	IterKindGenerator
	IterKindString
	IterKindBytes
	IterKindByteArray
	IterKindSet
	IterKindRepeat
	IterKindISlice
	IterKindChain
	IterKindChainFromIterable
	IterKindZip
	IterKindEnumerate
)

// PyIter represents a zero-allocation iterator over lists, tuples, dicts, ranges, or itertools streams.
// Header MUST be at offset 0.
type PyIter struct {
	Header  ObjectHeader
	Obj     Value
	Sub     Value
	NextFn  func() (Value, bool)
	Seq     []Value
	Idx     int
	Cur     int64
	NextIdx int64
	Stop    int64
	Step    int64
	Kind    IterKind
}

// GenState classifies the lifecycle state of a PyGenerator.
type GenState uint8

const (
	GenCreated GenState = iota
	GenRunning
	GenSuspended
	GenClosed
)

// PyGenerator represents a frame-detaching Python generator object on the heap.
// Header MUST be at offset 0.
type PyGenerator struct {
	Header      ObjectHeader
	Name        string
	Code        Value
	Globals     *PyDict
	YieldVal    Value
	RetVal      Value
	LastSent    Value
	ResumeHook  func(ctx *ExecContext, gen *PyGenerator, val Value) (Value, IterStatus)
	ReleaseHook func(gen *PyGenerator)
	Stack       []Value
	Cells       []Value
	YieldTuple  *PyTuple
	CellPtrs    []*PyCell
	SP          int
	Base        int
	IP          int
	MaxSP       int
	State       GenState
	PoolClass   uint8
	OwnCells    bool
}

// ClearYieldTuple zeroes inline element references on the generator's reusable
// yield tuple and clears the pointer so GC does not retain yielded values.
func (g *PyGenerator) ClearYieldTuple() {
	if g != nil && g.YieldTuple != nil {
		g.YieldTuple.ClearInline()
		g.YieldTuple = nil
	}
}

// CoroutineState classifies the lifecycle state of a PyCoroutine.
type CoroutineState uint8

const (
	CoroCreated CoroutineState = iota
	CoroRunning
	CoroSuspended
	CoroClosed
)

// PyCoroutine represents a frame-detaching Python coroutine object on the heap.
// Header MUST be at offset 0.
type PyCoroutine struct {
	Header      ObjectHeader
	Name        string
	Code        Value
	Globals     *PyDict
	YieldVal    Value
	RetVal      Value
	LastSent    Value
	ResumeHook  func(ctx *ExecContext, coro *PyCoroutine, val Value) (Value, IterStatus)
	ReleaseHook func(coro *PyCoroutine)
	Stack       []Value
	Cells       []Value
	CellPtrs    []*PyCell
	SP          int
	Base        int
	IP          int
	MaxSP       int
	State       CoroutineState
	PoolClass   uint8
	OwnCells    bool
}

// FutureState classifies the lifecycle state of a PyFuture.
type FutureState uint8

const (
	FutPending FutureState = iota
	FutFinished
	FutCancelled
)

// PyFuture represents an asynchronous Future computation.
// Header MUST be at offset 0.
type PyFuture struct {
	Header    ObjectHeader
	State     FutureState
	ResultVal Value
	ExcVal    Value
	Callbacks []func(Value)
	mu        sync.Mutex
	doneChan  chan struct{}
	// Attrs is the lazily created __dict__ (Futures and Tasks accept
	// arbitrary attributes, like CPython's _asyncio objects).
	Attrs *PyDict
}

// AsyncAttrs returns the attribute dict of a Future/Task Value, creating it
// when create is true. It returns nil for other kinds or when absent.
func AsyncAttrs(obj Value, create bool) *PyDict {
	var f *PyFuture
	switch obj.Kind() {
	case KindFuture:
		f = obj.AsFuture()
	case KindTask:
		f = &obj.AsTask().Future
	default:
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Attrs == nil && create {
		f.Attrs = NewDictVal(4).AsDict()
	}
	return f.Attrs
}

// PyTask represents an asyncio Task scheduling a Coroutine on an EventLoop.
// Header MUST be at offset 0.
type PyTask struct {
	Header   ObjectHeader
	Future   PyFuture
	Coro     *PyCoroutine
	Loop     any
	NextSent Value
	// Context is the task's isolated context, a shallow copy taken at spawn.
	Context *PyContext
}

// Compile-time assertions that every heap object places Header at offset 0.
var (
	_ [0 - unsafe.Offsetof(PyBigInt{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyString{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyList{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyDict{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyCell{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyIter{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyGenerator{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyCoroutine{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyFuture{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyTask{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyChan{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyBytes{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyByteArray{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyMemoryView{}.Header)]byte
	_ [0 - unsafe.Offsetof(PySlice{}.Header)]byte
)

func init() {
	if unsafe.Sizeof(Value{}) != 16 {
		panic("val: unsafe.Sizeof(Value{}) != 16")
	}
	if unsafe.Offsetof(PyBigInt{}.Header) != 0 ||
		unsafe.Offsetof(PyString{}.Header) != 0 ||
		unsafe.Offsetof(PyList{}.Header) != 0 ||
		unsafe.Offsetof(PyDict{}.Header) != 0 ||
		unsafe.Offsetof(PyCell{}.Header) != 0 ||
		unsafe.Offsetof(PyIter{}.Header) != 0 ||
		unsafe.Offsetof(PyGenerator{}.Header) != 0 ||
		unsafe.Offsetof(PyCoroutine{}.Header) != 0 ||
		unsafe.Offsetof(PyFuture{}.Header) != 0 ||
		unsafe.Offsetof(PyTask{}.Header) != 0 ||
		unsafe.Offsetof(PyChan{}.Header) != 0 ||
		unsafe.Offsetof(PyBytes{}.Header) != 0 ||
		unsafe.Offsetof(PyByteArray{}.Header) != 0 ||
		unsafe.Offsetof(PyMemoryView{}.Header) != 0 ||
		unsafe.Offsetof(PySlice{}.Header) != 0 {
		panic("val: heap object Header must be at offset 0")
	}
}

// NewBigIntVal constructs a heap-allocated KindBigInt Value from a *big.Int.
func NewBigIntVal(bi *big.Int) Value {
	obj := &PyBigInt{
		Header: ObjectHeader{Kind: KindBigInt, Type: BigIntType},
	}
	if bi != nil {
		obj.Val.Set(bi)
	}
	return Value{num: 0, ptr: &obj.Header}
}

// NewBigIntFromInt64 constructs a heap-allocated KindBigInt Value initialized to x.
func NewBigIntFromInt64(x int64) Value {
	obj := &PyBigInt{
		Header: ObjectHeader{Kind: KindBigInt, Type: BigIntType},
	}
	obj.Val.SetInt64(x)
	return Value{num: 0, ptr: &obj.Header}
}

// NewListVal constructs a heap-allocated KindList Value wrapping the provided slice.
func NewListVal(items ...Value) Value {
	obj := &PyList{
		Header: ObjectHeader{Kind: KindList, Type: ListType},
		Items:  items,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// NewListFromOwnedSlice constructs a KindList Value taking ownership of items directly.
func NewListFromOwnedSlice(items []Value) Value {
	obj := &PyList{
		Header: ObjectHeader{Kind: KindList, Type: ListType},
		Items:  items,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// NewListFromSlice constructs a KindList Value by copying items into a single exact-sized slice.
func NewListFromSlice(items []Value) Value {
	var cp []Value
	if len(items) > 0 {
		cp = make([]Value, len(items))
		copy(cp, items)
	}
	obj := &PyList{
		Header: ObjectHeader{Kind: KindList, Type: ListType},
		Items:  cp,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// NewListWithCap constructs an empty KindList Value with preallocated capacity.
// Used for comprehension targets where the first append would otherwise grow
// through 1 -> 2 -> 4 -> 8. The 8-slot floor skips that churn in one shot.
func NewListWithCap(capHint int) Value {
	var items []Value
	if capHint > 0 {
		if capHint < 8 {
			capHint = 8
		}
		items = make([]Value, 0, capHint)
	}
	obj := &PyList{
		Header: ObjectHeader{Kind: KindList, Type: ListType},
		Items:  items,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// ListLenHint returns the exact length of a known-length iterable without
// materializing it. It covers PyList, PyTuple, PyRange, PyDict, PySet,
// PyString and PyBytes, unwrapping PyIter to its source object. ok is false
// for iterators, generators and unknown shapes where pre-sizing is unsafe.
func ListLenHint(v Value) (int, bool) {
	if v.ptr == nil {
		return 0, false
	}
	if v.ptr == &tagRange {
		n := int(int64(v.RangeStop()) - int64(v.RangeStart()))
		return max(n, 0), true
	}
	switch v.ptr.Kind {
	case KindList:
		return len(v.AsList().Items), true
	case KindTuple:
		return len(v.AsTuple().Items), true
	case KindDict:
		return v.AsDict().Len(), true
	case KindSet:
		return v.AsSet().Len(), true
	case KindString:
		return len(v.AsStringObj().Data), true
	case KindBytes:
		return len(v.AsBytes().Data), true
	case KindIter:
		it := v.AsIter()
		if it.Kind == IterKindRange {
			if it.Step > 0 && it.Cur < it.Stop {
				n := (it.Stop-it.Cur-1)/it.Step + 1
				if n > 0 && n <= 1<<20 {
					return int(n), true
				}
			} else if it.Step < 0 && it.Cur > it.Stop {
				n := (it.Cur-it.Stop-1)/(-it.Step) + 1
				if n > 0 && n <= 1<<20 {
					return int(n), true
				}
			}
			return 0, false
		}
		return ListLenHint(it.Obj)
	default:
		return 0, false
	}
}

// dict16Backing fuses the 16-slot index table and dense entry array into a
// single heap allocation when the 16-slot table pool is empty.
type dict16Backing struct {
	entries [Dict16EntryCap]DictEntry
	indices [16]byte
}

// dict32Backing fuses the 32-slot index table and dense entry array into a
// single heap allocation for 11-20 key dicts (e.g. ASGI scope dicts).
type dict32Backing struct {
	entries [Dict32EntryCap]DictEntry
	indices [32]byte
}

// smallDictBundle fuses a PyDict with its 5-entry / 8-index small table into
// a single heap allocation for known-nonempty small dicts and pooled dicts.
// dict MUST remain at offset 0 so &b.dict.Header is offset 0.
type smallDictBundle struct {
	dict    PyDict
	entries [5]DictEntry
	indices [8]byte
}

// smallDictBacking fuses the 5-entry / 8-index small table into a single heap
// allocation when a slim empty PyDict or PySet receives its first entry.
type smallDictBacking struct {
	entries [5]DictEntry
	indices [8]byte
}

// NewSplitDict allocates a PEP 412 split-table PyDict referencing immutable
// shared keys and copying only values[:keys.Count] into a single fused heap
// allocation (zero DictEntry key/hash copying and zero indices allocation).
func NewSplitDict(keys *SplitDictKeys, values []Value, flags uint8) *PyDict {
	count := keys.Count
	switch {
	case count <= 4:
		b := &splitDictBundle4{
			dict: PyDict{
				Header:     ObjectHeader{Kind: KindDict, Flags: flags, Type: DictType},
				VersionTag: 1,
				used:       int32(count),
				filled:     int32(count),
			},
		}
		copy(b.vals[:count], values[:count])
		b.data.keys = keys
		b.data.vals = b.vals[:count]
		b.dict.split = &b.data
		return &b.dict
	case count <= 8:
		b := &splitDictBundle8{
			dict: PyDict{
				Header:     ObjectHeader{Kind: KindDict, Flags: flags, Type: DictType},
				VersionTag: 1,
				used:       int32(count),
				filled:     int32(count),
			},
		}
		copy(b.vals[:count], values[:count])
		b.data.keys = keys
		b.data.vals = b.vals[:count]
		b.dict.split = &b.data
		return &b.dict
	default:
		b := &splitDictBundle16{
			dict: PyDict{
				Header:     ObjectHeader{Kind: KindDict, Flags: flags, Type: DictType},
				VersionTag: 1,
				used:       int32(count),
				filled:     int32(count),
			},
		}
		copy(b.vals[:count], values[:count])
		b.data.keys = keys
		b.data.vals = b.vals[:count]
		b.dict.split = &b.data
		return &b.dict
	}
}

// NewSplitDictFromEntries allocates a PEP 412 split-table PyDict copying
// values directly from entries (zero intermediate Value slice, zero escape).
// Caller guarantees len(entries) == keys.Count and entries hold unique keys.
func NewSplitDictFromEntries(keys *SplitDictKeys, entries []DictEntry, flags uint8) *PyDict {
	count := keys.Count
	switch {
	case count <= 4:
		b := &splitDictBundle4{
			dict: PyDict{
				Header:     ObjectHeader{Kind: KindDict, Flags: flags, Type: DictType},
				VersionTag: 1,
				used:       int32(count),
				filled:     int32(count),
			},
		}
		for i := range count {
			b.vals[i] = entries[i].Value
		}
		b.data.keys = keys
		b.data.vals = b.vals[:count]
		b.dict.split = &b.data
		return &b.dict
	case count <= 8:
		b := &splitDictBundle8{
			dict: PyDict{
				Header:     ObjectHeader{Kind: KindDict, Flags: flags, Type: DictType},
				VersionTag: 1,
				used:       int32(count),
				filled:     int32(count),
			},
		}
		for i := range count {
			b.vals[i] = entries[i].Value
		}
		b.data.keys = keys
		b.data.vals = b.vals[:count]
		b.dict.split = &b.data
		return &b.dict
	default:
		b := &splitDictBundle16{
			dict: PyDict{
				Header:     ObjectHeader{Kind: KindDict, Flags: flags, Type: DictType},
				VersionTag: 1,
				used:       int32(count),
				filled:     int32(count),
			},
		}
		for i := range count {
			b.vals[i] = entries[i].Value
		}
		b.data.keys = keys
		b.data.vals = b.vals[:count]
		b.dict.split = &b.data
		return &b.dict
	}
}

// IsSplit reports whether d currently uses a PEP 412 shared-key split table.
func (d *PyDict) IsSplit() bool {
	return d.split != nil
}

// SplitKeys returns the immutable shared key table if d is split, or nil.
func (d *PyDict) SplitKeys() *SplitDictKeys {
	if d.split == nil {
		return nil
	}
	return d.split.keys
}

// SplitValues returns the dense value slice if d is split, or nil.
func (d *PyDict) SplitValues() []Value {
	if d.split == nil {
		return nil
	}
	return d.split.vals
}

// EnsureCombined promotes a split-table dict to a standard combined table if
// needed. Safe to call on already-combined dicts (single nil check).
func (d *PyDict) EnsureCombined() {
	if d.split == nil {
		return
	}
	if d.lockDict() {
		defer d.mu.Unlock()
	}
	if d.split != nil {
		d.promoteSplitToCombined()
	}
}

//go:noinline
func (d *PyDict) getSplitLocked(key Value) (Value, bool) {
	if idx := d.splitLookup(key); idx >= 0 {
		return d.split.vals[idx], true
	}
	return NoneVal(), false
}

//go:noinline
func (d *PyDict) getWithSlotSplitLocked(key Value) (Value, int, bool) {
	if idx := d.splitLookup(key); idx >= 0 {
		return d.split.vals[idx], idx, true
	}
	return NoneVal(), -1, false
}

//go:noinline
func (d *PyDict) setSplitLocked(key, value Value) bool {
	if idx := d.splitLookup(key); idx >= 0 {
		d.split.vals[idx] = value
		d.bumpVersionTag()
		if d.owner != nil {
			d.owner.bumpVersion()
		}
		return true
	}
	d.promoteSplitToCombined()
	return false
}

func (d *PyDict) splitLookup(key Value) int {
	sk := d.split.keys
	count := sk.Count
	if count <= 5 {
		keys := sk.Keys[:count]
		for i := range keys {
			if keys[i] == key || dictKeyEqual(keys[i], key) {
				return i
			}
		}
		return -1
	}
	h := HashValue(key)
	mask := sk.Mask
	slot := h & mask
	for {
		raw := sk.Indices[slot]
		if raw == dictIndexEmpty {
			return -1
		}
		idx := int(raw) - dictIndexOffset
		if sk.Keys[idx] == key || (sk.Hashes[idx] == h && dictKeyEqual(sk.Keys[idx], key)) {
			return idx
		}
		slot = (slot + 1) & mask
	}
}

// promoteSplitToCombined performs one-way promotion of a split-table dict to a
// standard combined PyDict table (reusing the pre-computed hashes and indices
// without re-hashing). Caller must hold d.mu if concurrent.
//
//go:noinline
func (d *PyDict) promoteSplitToCombined() {
	sp := d.split
	if sp == nil {
		return
	}
	sk := sp.keys
	if sk.Promotions.Add(1) >= 2 {
		sk.DisableSplit.Store(true)
	}
	count := sk.Count
	tableCap := int(sk.Mask) + 1
	d.initTable(max(count, 4))
	// Materialize entries directly; if tableCap matches sk.Mask+1 we can copy
	// pre-computed indices directly without probing.
	if int(d.mask)+1 == tableCap && d.indexShift == 0 && cap(d.Entries) >= count {
		entries := d.Entries[:count]
		for i := range count {
			entries[i] = DictEntry{
				Hash:   sk.Hashes[i],
				Active: true,
				Key:    sk.Keys[i],
				Value:  sp.vals[i],
			}
		}
		d.Entries = entries
		copy(d.indices[:tableCap], sk.Indices[:tableCap])
		d.used = int32(count)
		d.filled = int32(count)
		d.split = nil
		return
	}
	d.used = 0
	d.filled = 0
	d.split = nil
	for i := range count {
		d.PutNewHashed(sk.Keys[i], sp.vals[i], sk.Hashes[i])
	}
}

// NewSmallDictDirect allocates a fresh <=5-entry PyDict with fused small-table
// storage already bound in a single heap allocation.
func NewSmallDictDirect(flags uint8) *PyDict {
	b := &smallDictBundle{
		dict: PyDict{
			Header:     ObjectHeader{Kind: KindDict, Flags: flags, Type: DictType},
			mask:       7,
			VersionTag: 1,
		},
	}
	b.dict.indices = b.indices[:]
	b.dict.Entries = b.entries[:0]
	return &b.dict
}

// NewDictVal constructs an empty heap-allocated KindDict Value with initial capacity.
func NewDictVal(capacity int) Value {
	if capacity <= 0 {
		obj := &PyDict{
			Header: ObjectHeader{Kind: KindDict, Type: DictType},
		}
		return Value{num: 0, ptr: &obj.Header}
	}
	if capacity <= 5 {
		d := NewSmallDictDirect(0)
		return Value{num: 0, ptr: &d.Header}
	}
	obj := &PyDict{
		Header: ObjectHeader{Kind: KindDict, Type: DictType},
	}
	obj.initTable(capacity)
	return Value{num: 0, ptr: &obj.Header}
}

// AsBigInt performs a zero-cost offset-0 downcast from Value to *PyBigInt.
func (v Value) AsBigInt() *PyBigInt {
	return (*PyBigInt)(unsafe.Pointer(v.ptr))
}

// AsStringObj performs a zero-cost offset-0 downcast from Value to *PyString.
func (v Value) AsStringObj() *PyString {
	return (*PyString)(unsafe.Pointer(v.ptr))
}

// AsList performs a zero-cost offset-0 downcast from Value to *PyList.
func (v Value) AsList() *PyList {
	return (*PyList)(unsafe.Pointer(v.ptr))
}

// AsDict performs a zero-cost offset-0 downcast from Value to *PyDict.
func (v Value) AsDict() *PyDict {
	return (*PyDict)(unsafe.Pointer(v.ptr))
}

// NewChanVal constructs a heap-allocated KindChan Value wrapping a native Go chan Value.
func NewChanVal(capacity int) Value {
	if capacity < 0 {
		capacity = 0
	}
	obj := &PyChan{
		Header: ObjectHeader{Kind: KindChan, Type: ChanType},
		Ch:     make(chan Value, capacity),
	}
	return Value{num: 0, ptr: &obj.Header}
}

// AsChan performs a zero-cost offset-0 downcast from Value to *PyChan.
func (v Value) AsChan() *PyChan {
	return (*PyChan)(unsafe.Pointer(v.ptr))
}

// NewBytesVal constructs a heap-allocated KindBytes Value wrapping data.
func NewBytesVal(data []byte) Value {
	obj := &PyBytes{
		Header: ObjectHeader{
			Kind: KindBytes,
			Type: BytesType,
		},
		Data: data,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// NewByteArrayVal constructs a heap-allocated KindByteArray Value wrapping data.
func NewByteArrayVal(data []byte) Value {
	obj := &PyByteArray{
		Header: ObjectHeader{
			Kind: KindByteArray,
			Type: ByteArrayType,
		},
		Data: data,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// LargeBufferThreshold is the size from which a byte buffer is mapped outside
// the Go heap instead of being allocated on it.
//
// A buffer that large would otherwise grow the Go heap past the garbage
// collector's trigger and make the collector mark the whole heap while the
// program is still starting up. Mapping it keeps the collector out of the
// picture entirely.
const LargeBufferThreshold = 1 << 20

// NewMappedBytesVal constructs a heap-allocated KindBytes Value whose payload
// is a freshly allocated buffer of n bytes.
//
// Buffers of at least LargeBufferThreshold bytes are mapped outside the Go
// heap and returned to the operating system when the object is collected.
func NewMappedBytesVal(n int) Value {
	buf, m := allocZeroedBytes(n)
	obj := &PyBytes{
		Header: ObjectHeader{
			Kind: KindBytes,
			Type: BytesType,
		},
		Data: buf,
	}
	attachByteMapping(obj, m)
	return Value{num: 0, ptr: &obj.Header}
}

// NewMappedByteArrayVal constructs a heap-allocated KindByteArray Value whose
// payload is a freshly allocated buffer of n bytes.
//
// Buffers of at least LargeBufferThreshold bytes are mapped outside the Go
// heap and returned to the operating system when the object is collected.
func NewMappedByteArrayVal(n int) Value {
	buf, m := allocZeroedBytes(n)
	obj := &PyByteArray{
		Header: ObjectHeader{
			Kind: KindByteArray,
			Type: ByteArrayType,
		},
		Data: buf,
	}
	attachByteMapping(obj, m)
	return Value{num: 0, ptr: &obj.Header}
}

// NewMappedBytesFromBytes constructs a bytes object holding a copy of src.
//
// Large copies are mapped outside the Go heap.
func NewMappedBytesFromBytes(src []byte) Value {
	buf, m := allocZeroedBytes(len(src))
	copy(buf, src)
	obj := &PyBytes{
		Header: ObjectHeader{
			Kind: KindBytes,
			Type: BytesType,
		},
		Data: buf,
	}
	attachByteMapping(obj, m)
	return Value{num: 0, ptr: &obj.Header}
}

// NewMappedByteArrayFromBytes constructs a bytearray holding a copy of src.
//
// Large copies are mapped outside the Go heap.
func NewMappedByteArrayFromBytes(src []byte) Value {
	buf, m := allocZeroedBytes(len(src))
	copy(buf, src)
	obj := &PyByteArray{
		Header: ObjectHeader{
			Kind: KindByteArray,
			Type: ByteArrayType,
		},
		Data: buf,
	}
	attachByteMapping(obj, m)
	return Value{num: 0, ptr: &obj.Header}
}

// NewMappedRepeatedBytes constructs a bytes object holding n copies of data.
//
// Large results are mapped outside the Go heap.
func NewMappedRepeatedBytes(data []byte, n int) Value {
	buf, m := allocZeroedBytes(len(data) * n)
	for i := range n {
		copy(buf[i*len(data):(i+1)*len(data)], data)
	}
	obj := &PyBytes{
		Header: ObjectHeader{
			Kind: KindBytes,
			Type: BytesType,
		},
		Data: buf,
	}
	attachByteMapping(obj, m)
	return Value{num: 0, ptr: &obj.Header}
}

// NewMappedRepeatedByteArray constructs a bytearray holding n copies of data.
//
// Large results are mapped outside the Go heap.
func NewMappedRepeatedByteArray(data []byte, n int) Value {
	buf, m := allocZeroedBytes(len(data) * n)
	for i := range n {
		copy(buf[i*len(data):(i+1)*len(data)], data)
	}
	obj := &PyByteArray{
		Header: ObjectHeader{
			Kind: KindByteArray,
			Type: ByteArrayType,
		},
		Data: buf,
	}
	attachByteMapping(obj, m)
	return Value{num: 0, ptr: &obj.Header}
}

// NewMappedBytesFromString constructs a bytes object holding the bytes of s.
//
// Large results are mapped outside the Go heap.
func NewMappedBytesFromString(s string) Value {
	buf, m := allocZeroedBytes(len(s))
	copy(buf, s)
	obj := &PyBytes{
		Header: ObjectHeader{
			Kind: KindBytes,
			Type: BytesType,
		},
		Data: buf,
	}
	attachByteMapping(obj, m)
	return Value{num: 0, ptr: &obj.Header}
}

// NewMappedByteArrayFromString constructs a bytearray holding the bytes of s.
//
// Large results are mapped outside the Go heap.
func NewMappedByteArrayFromString(s string) Value {
	buf, m := allocZeroedBytes(len(s))
	copy(buf, s)
	obj := &PyByteArray{
		Header: ObjectHeader{
			Kind: KindByteArray,
			Type: ByteArrayType,
		},
		Data: buf,
	}
	attachByteMapping(obj, m)
	return Value{num: 0, ptr: &obj.Header}
}

// NewBytesFromConcat constructs a bytes object holding a followed by b.
//
// Large results are mapped outside the Go heap.
func NewBytesFromConcat(a, b []byte) Value {
	if len(a)+len(b) >= LargeBufferThreshold {
		buf, m := allocZeroedBytes(len(a) + len(b))
		copy(buf, a)
		copy(buf[len(a):], b)
		obj := &PyBytes{
			Header: ObjectHeader{
				Kind: KindBytes,
				Type: BytesType,
			},
			Data: buf,
		}
		attachByteMapping(obj, m)
		return Value{num: 0, ptr: &obj.Header}
	}
	res := make([]byte, len(a)+len(b))
	copy(res, a)
	copy(res[len(a):], b)
	return NewBytesVal(res)
}

// NewByteArrayFromConcat constructs a bytearray holding a followed by b.
//
// Large results are mapped outside the Go heap.
func NewByteArrayFromConcat(a, b []byte) Value {
	if len(a)+len(b) >= LargeBufferThreshold {
		buf, m := allocZeroedBytes(len(a) + len(b))
		copy(buf, a)
		copy(buf[len(a):], b)
		obj := &PyByteArray{
			Header: ObjectHeader{
				Kind: KindByteArray,
				Type: ByteArrayType,
			},
			Data: buf,
		}
		attachByteMapping(obj, m)
		return Value{num: 0, ptr: &obj.Header}
	}
	res := make([]byte, len(a)+len(b))
	copy(res, a)
	copy(res[len(a):], b)
	return NewByteArrayVal(res)
}

// NewMemoryViewVal constructs a zero-copy PyMemoryView Value referencing the buffer of target.
func NewMemoryViewVal(target Value) Value {
	tp := target.Type()
	if tp == BytesType {
		data := slices.Clip(target.AsBytes().Data)
		obj := &PyMemoryView{
			Header:   ObjectHeader{Kind: KindMemoryView, Type: MemoryViewType},
			Obj:      target,
			Buf:      data,
			ReadOnly: true,
			ItemSize: 1,
			Format:   "B",
			NDim:     1,
			Dim0:     len(data),
			Stride0:  1,
		}
		return Value{num: 0, ptr: &obj.Header}
	}
	if tp == ByteArrayType {
		ba := target.AsByteArray()
		ba.mu.RLock()
		data := ba.Data
		ba.mu.RUnlock()
		obj := &PyMemoryView{
			Header:   ObjectHeader{Kind: KindMemoryView, Type: MemoryViewType},
			Obj:      target,
			Buf:      data,
			ReadOnly: false,
			ItemSize: 1,
			Format:   "B",
			NDim:     1,
			Dim0:     len(data),
			Stride0:  1,
		}
		return Value{num: 0, ptr: &obj.Header}
	}
	return newMemoryViewSlow(target, tp)
}

//go:noinline
func newMemoryViewSlow(target Value, tp *PyType) Value {
	var view PyBuffer
	if tp.Slots.TpAsBuffer == nil {
		panic("TypeError: a bytes-like object is required, not '" + tp.Name + "'")
	}
	if err := tp.Slots.TpAsBuffer(nil, target, &view, PyBUF_STRIDES|PyBUF_FORMAT); err != nil {
		panic("BufferError: " + err.Error())
	}
	// Default to 1D unsigned-byte view if provider left metadata empty.
	format := view.Format
	if format == "" {
		format = "B"
	}
	ndim := view.NDim
	shape := view.Shape
	strides := view.Strides
	itemSize := view.ItemSize
	if itemSize == 0 {
		if sz, err := FormatSize(format); err == nil && sz > 0 {
			itemSize = sz
		} else {
			itemSize = 1
		}
	}
	if ndim == 0 && shape == nil {
		ndim = 1
		// nbytes = len(view.Buf); shape = [nbytes/itemSize]
		if itemSize > 0 {
			shape = []int{len(view.Buf) / itemSize}
		} else {
			shape = []int{len(view.Buf)}
		}
		strides = CContiguousStrides(shape, itemSize)
	}
	if strides == nil && shape != nil {
		strides = CContiguousStrides(shape, itemSize)
	}
	if ndim == 0 {
		ndim = len(shape)
	}
	obj := &PyMemoryView{
		Header:     ObjectHeader{Kind: KindMemoryView, Type: MemoryViewType},
		Obj:        target,
		Buf:        view.Buf,
		ReadOnly:   view.ReadOnly,
		ItemSize:   itemSize,
		Format:     format,
		NDim:       ndim,
		Suboffsets: view.Suboffsets,
	}
	obj.SetShape(shape, strides)
	return Value{num: 0, ptr: &obj.Header}
}

// NewSliceVal constructs a heap-allocated KindSlice Value.
func NewSliceVal(start, stop, step Value) Value {
	obj := &PySlice{
		Header: ObjectHeader{
			Kind: KindSlice,
			Type: SliceType,
		},
		Start: start,
		Stop:  stop,
		Step:  step,
	}
	return Value{num: 0, ptr: &obj.Header}
}

var emptyTuple = PyTuple{
	Header: ObjectHeader{
		Kind: KindTuple,
		Type: TupleType,
	},
	Items: nil,
}

// EmptyTupleVal returns the singleton empty tuple Value.
func EmptyTupleVal() Value {
	return Value{num: 0, ptr: &emptyTuple.Header}
}

// NewTupleView returns a zero-copy sub-slice view of tuple items without element copies.
func NewTupleView(items []Value) Value {
	if len(items) == 0 {
		return EmptyTupleVal()
	}
	obj := &PyTuple{
		Header: ObjectHeader{
			Kind: KindTuple,
			Type: TupleType,
		},
		Items: items,
	}
	return Value{num: 0, ptr: &obj.Header}
}

func (b *PyBytes) GetBuffer(ctx *ExecContext, view *PyBuffer, flags int) error {
	if (flags & PyBUF_WRITABLE) != 0 {
		return errors.New("cannot create writable buffer view into immutable bytes")
	}
	view.Buf = slices.Clip(b.Data)
	view.ReadOnly = true
	view.ItemSize = 1
	view.Obj = ObjectVal(&b.Header)
	view.Format = "B"
	view.NDim = 1
	view.Shape = []int{len(b.Data)}
	view.Strides = []int{1}
	view.Suboffsets = nil
	return nil
}

func (b *PyBytes) ReleaseBuffer(view *PyBuffer) {
	if view != nil && view.Release != nil {
		view.Release()
	}
}

func (ba *PyByteArray) GetBuffer(ctx *ExecContext, view *PyBuffer, flags int) error {
	ba.mu.RLock()
	defer ba.mu.RUnlock()
	view.Buf = ba.Data
	view.ReadOnly = false
	view.ItemSize = 1
	view.Obj = ObjectVal(&ba.Header)
	view.Format = "B"
	view.NDim = 1
	view.Shape = []int{len(ba.Data)}
	view.Strides = []int{1}
	return nil
}

func (ba *PyByteArray) ReleaseBuffer(view *PyBuffer) {
	if view != nil && view.Release != nil {
		view.Release()
	}
}

func (mv *PyMemoryView) GetBuffer(ctx *ExecContext, view *PyBuffer, flags int) error {
	if mv.released {
		return errors.New("operation on released memoryview")
	}
	if mv.ReadOnly && (flags&PyBUF_WRITABLE) != 0 {
		return errors.New("cannot create writable buffer view into read-only memoryview")
	}
	view.Buf = mv.Buf
	view.ReadOnly = mv.ReadOnly
	view.ItemSize = mv.ItemSize
	if view.ItemSize == 0 {
		view.ItemSize = 1
	}
	view.Obj = mv.Obj
	view.Format = mv.Format
	if view.Format == "" {
		view.Format = "B"
	}
	view.NDim = mv.NDim
	if view.NDim == 0 {
		view.NDim = 1
	}
	view.Shape = mv.Shape
	if view.Shape == nil {
		if view.NDim == 1 {
			view.Shape = []int{mv.Dim(0)}
		} else {
			view.Shape = mv.Dims()
		}
	}
	view.Strides = mv.Strides
	if view.Strides == nil {
		if view.NDim == 1 {
			view.Strides = []int{mv.Stride(0)}
		} else {
			view.Strides = CContiguousStrides(view.Shape, view.ItemSize)
		}
	}
	view.Suboffsets = mv.Suboffsets
	// Contiguity checks: if C_CONTIGUOUS requested but view isn't, error.
	if (flags&PyBUF_C_CONTIGUOUS) != 0 && (flags&PyBUF_C_CONTIGUOUS) == PyBUF_C_CONTIGUOUS {
		// PyBUF_C_CONTIGUOUS includes STRIDES bits; check actual contiguity.
		if !IsCContiguous(view.Shape, view.Strides, view.ItemSize) {
			return errors.New("buffer is not C-contiguous")
		}
	}
	if (flags&PyBUF_F_CONTIGUOUS) != 0 && (flags&PyBUF_F_CONTIGUOUS) != PyBUF_STRIDES {
		if !IsFContiguous(view.Shape, view.Strides, view.ItemSize) {
			return errors.New("buffer is not F-contiguous")
		}
	}
	return nil
}

func (mv *PyMemoryView) ReleaseBuffer(view *PyBuffer) {
	if view != nil && view.Release != nil {
		view.Release()
	}
}

// NewCellVal constructs a heap-allocated KindCell Value initialized to v.
func NewCellVal(v Value) Value {
	obj := &PyCell{
		Header: ObjectHeader{Kind: KindCell, Type: CellType},
		Value:  v,
	}
	return Value{num: 0, ptr: &obj.Header}
}

// AsCell performs a zero-cost offset-0 downcast from Value to *PyCell.
func (v Value) AsCell() *PyCell {
	return (*PyCell)(unsafe.Pointer(v.ptr))
}

// Append adds a Value to the end of the PyList in a thread-safe manner.
// The first allocation lands on an 8-slot floor so comprehensions and tight
// append loops skip the 1 -> 2 -> 4 -> 8 geometric churn.
func (l *PyList) Append(v Value) {
	ClearEphemeral(v)
	l.mu.Lock()
	if cap(l.Items) == 0 {
		l.Items = make([]Value, 0, 8)
	} else if len(l.Items) == cap(l.Items) && cap(l.Items) < 8 {
		grown := make([]Value, len(l.Items), 8)
		copy(grown, l.Items)
		l.Items = grown
	}
	l.Items = append(l.Items, v)
	l.mu.Unlock()
}

// EnsureCap grows l.Items in one shot so at least n additional slots are
// available without further allocation. No-op when capacity suffices.
func (l *PyList) EnsureCap(n int) {
	if n <= 0 {
		return
	}
	l.mu.Lock()
	need := len(l.Items) + n
	if need <= cap(l.Items) {
		l.mu.Unlock()
		return
	}
	size := max(cap(l.Items), 8)
	for size < need {
		size <<= 1
	}
	grown := make([]Value, len(l.Items), size)
	copy(grown, l.Items)
	l.Items = grown
	l.mu.Unlock()
}

// Get returns the Value at idx in a thread-safe manner.
func (l *PyList) Get(idx int) (Value, bool) {
	l.mu.RLock()
	if idx < 0 {
		idx += len(l.Items)
	}
	if idx < 0 || idx >= len(l.Items) {
		l.mu.RUnlock()
		return NoneVal(), false
	}
	v := l.Items[idx]
	l.mu.RUnlock()
	return v, true
}

// Set updates the Value at idx in a thread-safe manner.
func (l *PyList) Set(idx int, v Value) bool {
	l.mu.Lock()
	if idx < 0 {
		idx += len(l.Items)
	}
	if idx < 0 || idx >= len(l.Items) {
		l.mu.Unlock()
		return false
	}
	l.Items[idx] = v
	l.mu.Unlock()
	return true
}

// Len returns the number of items in the PyList in a thread-safe manner.
func (l *PyList) Len() int {
	l.mu.RLock()
	n := len(l.Items)
	l.mu.RUnlock()
	return n
}

// Pop removes and returns the last item in a thread-safe manner.
func (l *PyList) Pop() (Value, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(l.Items)
	if n == 0 {
		return NoneVal(), false
	}
	val := l.Items[n-1]
	l.Items = l.Items[:n-1]
	return val, true
}

// Contains reports whether v is in l in a thread-safe manner.
func (l *PyList) Contains(v Value) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, item := range l.Items {
		if Equals(item, v) {
			return true
		}
	}
	return false
}

// CopyItems returns a shallow copy of l.Items in a thread-safe manner.
func (l *PyList) CopyItems() []Value {
	l.mu.RLock()
	defer l.mu.RUnlock()
	res := make([]Value, len(l.Items))
	copy(res, l.Items)
	return res
}

// Get returns the cell's Value in a thread-safe manner.
func (c *PyCell) Get() Value {
	c.mu.RLock()
	v := c.Value
	c.mu.RUnlock()
	return v
}

// Set updates the cell's Value in a thread-safe manner.
func (c *PyCell) Set(v Value) {
	c.mu.Lock()
	c.Value = v
	c.mu.Unlock()
}

// Send sends v over the channel. Returns an error if the channel is closed.
func (c *PyChan) Send(v Value) (err error) {
	if c.closed.Load() == 1 {
		return errors.New("cannot send on closed channel")
	}
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("cannot send on closed channel")
		}
	}()
	c.Ch <- v
	return nil
}

// Recv receives a Value from the channel. Returns (NoneVal(), false) if closed and drained.
func (c *PyChan) Recv() (Value, bool) {
	v, ok := <-c.Ch
	if !ok {
		return NoneVal(), false
	}
	return v, true
}

// Close closes the underlying Go channel. Safe to call concurrently.
func (c *PyChan) Close() bool {
	if c.closed.CompareAndSwap(0, 1) {
		close(c.Ch)
		return true
	}
	return false
}

// NewIterVal constructs a heap-allocated KindIter Value wrapping an iterable.
func NewIterVal(obj Value) Value {
	if obj.IsIter() {
		return obj
	}
	it := &PyIter{
		Header: ObjectHeader{Kind: KindIter, Type: IterType},
		Obj:    obj,
	}
	switch {
	case obj.IsList():
		it.Kind = IterKindList
	case obj.IsTuple():
		it.Kind = IterKindTuple
	case obj.IsDict():
		it.Kind = IterKindDict
	case obj.IsRange():
		it.Kind = IterKindRange
		it.Cur = int64(obj.RangeStart())
		it.Stop = int64(obj.RangeStop())
		it.Step = 1
	case obj.IsGenerator():
		it.Kind = IterKindGenerator
	case obj.IsSet():
		it.Kind = IterKindSet
	case obj.IsAnyString():
		it.Kind = IterKindString
	case obj.IsBytes():
		it.Kind = IterKindBytes
	case obj.IsByteArray():
		it.Kind = IterKindByteArray
	case obj.Type() != nil && obj.Type().Slots.TpIter != nil:
		return obj.Type().Slots.TpIter(nil, obj)
	default:
		it.Kind = IterKindInvalid
	}
	return Value{num: 0, ptr: &it.Header}
}

// NewRangeIterVal constructs a heap-allocated KindIter Value for range(start, stop, step).
func NewRangeIterVal(start, stop, step int64) Value {
	it := &PyIter{
		Header: ObjectHeader{Kind: KindIter, Type: IterType},
		Kind:   IterKindRange,
		Cur:    start,
		Stop:   stop,
		Step:   step,
	}
	return Value{num: 0, ptr: &it.Header}
}

// NewIterValWithNext constructs a heap-allocated KindIter Value with a custom Next function.
func NewIterValWithNext(nextFn func() (Value, bool)) Value {
	it := &PyIter{
		Header: ObjectHeader{Kind: KindIter, Type: IterType},
		Kind:   IterKindFunc,
		NextFn: nextFn,
	}
	return Value{num: 0, ptr: &it.Header}
}

// NewRepeatIterVal constructs a concrete PyIter for itertools.repeat(obj[, times]).
// times < 0 encodes an infinite repeat stream.
func NewRepeatIterVal(obj Value, times int64) Value {
	it := &PyIter{
		Header: ObjectHeader{Kind: KindIter, Type: IterType},
		Kind:   IterKindRepeat,
		Obj:    obj,
		Stop:   times,
	}
	return Value{num: 0, ptr: &it.Header}
}

// NewISliceIterVal constructs a concrete PyIter for itertools.islice(it, start, stop, step).
// stop < 0 encodes an unbounded slice end.
func NewISliceIterVal(iterObj Value, start, stop, step int64) Value {
	it := &PyIter{
		Header:  ObjectHeader{Kind: KindIter, Type: IterType},
		Kind:    IterKindISlice,
		Obj:     iterObj,
		NextIdx: start,
		Stop:    stop,
		Step:    step,
	}
	return Value{num: 0, ptr: &it.Header}
}

// NewChainIterVal constructs a concrete PyIter for itertools.chain(*iterables).
func NewChainIterVal(iterables []Value) Value {
	it := &PyIter{
		Header: ObjectHeader{Kind: KindIter, Type: IterType},
		Kind:   IterKindChain,
		Seq:    iterables,
	}
	return Value{num: 0, ptr: &it.Header}
}

// NewChainFromIterableVal constructs a concrete PyIter for itertools.chain.from_iterable(iterable).
func NewChainFromIterableVal(outerIter Value) Value {
	it := &PyIter{
		Header: ObjectHeader{Kind: KindIter, Type: IterType},
		Kind:   IterKindChainFromIterable,
		Obj:    outerIter,
	}
	return Value{num: 0, ptr: &it.Header}
}

// NewZipIterVal constructs a concrete PyIter for zip(*iterables, strict=...).
// subIters holds one iterator Value per argument (each IsIter); strict records
// the strict flag; Stop is the latch recording exhaustion.
func NewZipIterVal(subIters []Value, strict bool) Value {
	step := int64(0)
	if strict {
		step = 1
	}
	done := int64(0)
	if len(subIters) == 0 {
		done = 1
	}
	it := &PyIter{
		Header: ObjectHeader{Kind: KindIter, Type: IterType},
		Kind:   IterKindZip,
		Seq:    subIters,
		Stop:   done,
		Step:   step,
	}
	return Value{num: 0, ptr: &it.Header}
}

// NewEnumerateIterVal constructs a concrete PyIter for enumerate(iterable, start).
// subIter is the underlying iterator Value; Cur holds the next index to emit.
func NewEnumerateIterVal(subIter Value, start int64) Value {
	it := &PyIter{
		Header: ObjectHeader{Kind: KindIter, Type: IterType},
		Kind:   IterKindEnumerate,
		Obj:    subIter,
		Cur:    start,
	}
	return Value{num: 0, ptr: &it.Header}
}

// AsIter performs a zero-cost offset-0 downcast from Value to *PyIter.
func (v Value) AsIter() *PyIter {
	return (*PyIter)(unsafe.Pointer(v.ptr))
}

func advanceIterNext(ctx *ExecContext, obj Value) (Value, bool) {
	if obj.IsIter() {
		return obj.AsIter().NextCtx(ctx)
	}
	if obj.IsGenerator() {
		v, status := obj.AsGenerator().Send(ctx, NoneVal())
		if status == IterYielded {
			return v, true
		}
		return NoneVal(), false
	}
	if tp := obj.Type(); tp != nil && tp.Slots.TpIterNext != nil {
		v, status := tp.Slots.TpIterNext(ctx, obj)
		if status == IterYielded {
			return v, true
		}
		return NoneVal(), false
	}
	if ctx != nil {
		if nextFn, _, ok := obj.Type().LookupMRO(DunderNext); ok {
			v := ctx.Invoke1(nextFn, obj)
			if !ctx.HasException() {
				return v, true
			}
			if ctx.ActiveException.Type() == StopIterationType {
				ctx.ClearException()
			}
		}
	}
	return NoneVal(), false
}

func resolveChainSubIter(ctx *ExecContext, obj Value) Value {
	if obj.IsIter() || obj.IsGenerator() {
		return obj
	}
	if tp := obj.Type(); tp != nil {
		if tpIter := tp.Slots.TpIter; tpIter != nil {
			res := tpIter(ctx, obj)
			if !res.IsNone() || (ctx == nil || !ctx.HasException()) {
				return res
			}
			return NoneVal()
		}
		if iterFn, _, ok := tp.LookupMRO(DunderIter); ok && !iterFn.IsNone() && ctx != nil {
			res := ctx.Invoke1(iterFn, obj)
			if !ctx.HasException() {
				return res
			}
			return NoneVal()
		}
	}
	return NewIterVal(obj)
}

// Next advances the iterator and returns the next Value and true, or NoneVal() and false when exhausted.
func (it *PyIter) Next() (Value, bool) {
	return it.NextCtx(nil)
}

// NextCtx advances the iterator using ctx when resuming generators or calling custom dunder hooks.
func (it *PyIter) NextCtx(ctx *ExecContext) (Value, bool) {
	switch it.Kind {
	case IterKindList:
		list := it.Obj.AsList()
		if !MultiThreadedFlag.Load() {
			if it.Idx >= len(list.Items) {
				return NoneVal(), false
			}
			res := list.Items[it.Idx]
			it.Idx++
			return res, true
		}
		res, ok := list.Get(it.Idx)
		if !ok {
			return NoneVal(), false
		}
		it.Idx++
		return res, true
	case IterKindTuple:
		tuple := it.Obj.AsTuple()
		if it.Idx >= len(tuple.Items) {
			return NoneVal(), false
		}
		res := tuple.Items[it.Idx]
		it.Idx++
		return res, true
	case IterKindRange:
		if (it.Step > 0 && it.Cur >= it.Stop) || (it.Step < 0 && it.Cur <= it.Stop) {
			return NoneVal(), false
		}
		res := Int64Val(it.Cur)
		it.Cur += it.Step
		return res, true
	case IterKindDict:
		dict := it.Obj.AsDict()
		if dict.Live != nil {
			return dict.liveIterNext(it)
		}
		locked := dict.rlockDict()
		if sp := dict.split; sp != nil {
			if it.Idx < sp.keys.Count {
				key := sp.keys.Keys[it.Idx]
				it.Idx++
				if locked {
					dict.mu.RUnlock()
				}
				return key, true
			}
			if locked {
				dict.mu.RUnlock()
			}
			return NoneVal(), false
		}
		for it.Idx < len(dict.Entries) {
			entry := &dict.Entries[it.Idx]
			it.Idx++
			if entry.Active {
				key := entry.Key
				if locked {
					dict.mu.RUnlock()
				}
				return key, true
			}
		}
		if locked {
			dict.mu.RUnlock()
		}
		return NoneVal(), false
	case IterKindSet:
		set := it.Obj.AsSet()
		for it.Idx < len(set.dict.Entries) {
			entry := &set.dict.Entries[it.Idx]
			it.Idx++
			if entry.Active {
				return entry.Key, true
			}
		}
		return NoneVal(), false
	case IterKindGenerator:
		gen := it.Obj.AsGenerator()
		v, status := gen.Send(ctx, NoneVal())
		if status == IterExhausted || status == IterError {
			return NoneVal(), false
		}
		return v, true
	case IterKindRepeat:
		if it.Stop >= 0 {
			if it.Cur >= it.Stop {
				return NoneVal(), false
			}
			it.Cur++
		}
		return it.Obj, true
	case IterKindISlice:
		if it.Stop >= 0 && it.NextIdx >= it.Stop {
			return NoneVal(), false
		}
		for it.Cur < it.NextIdx {
			if _, ok := advanceIterNext(ctx, it.Obj); !ok {
				it.Stop = it.Cur
				return NoneVal(), false
			}
			it.Cur++
		}
		v, ok := advanceIterNext(ctx, it.Obj)
		if !ok {
			it.Stop = it.Cur
			return NoneVal(), false
		}
		it.Cur++
		const maxInt64 = int64(^uint64(0) >> 1)
		if it.NextIdx > maxInt64-it.Step {
			it.NextIdx = maxInt64
			it.Stop = it.Cur
		} else {
			it.NextIdx += it.Step
		}
		return v, true
	case IterKindChain:
		for {
			if !it.Sub.IsNone() && it.Sub.ptr != nil {
				if v, ok := advanceIterNext(ctx, it.Sub); ok {
					return v, true
				}
				it.Sub = Value{}
				if ctx != nil && ctx.HasException() {
					return NoneVal(), false
				}
			}
			if it.Idx >= len(it.Seq) {
				return NoneVal(), false
			}
			subObj := it.Seq[it.Idx]
			it.Idx++
			sub := resolveChainSubIter(ctx, subObj)
			if ctx != nil && ctx.HasException() {
				return NoneVal(), false
			}
			it.Sub = sub
		}
	case IterKindChainFromIterable:
		for {
			if !it.Sub.IsNone() && it.Sub.ptr != nil {
				if v, ok := advanceIterNext(ctx, it.Sub); ok {
					return v, true
				}
				it.Sub = Value{}
				if ctx != nil && ctx.HasException() {
					return NoneVal(), false
				}
			}
			subIterable, ok := advanceIterNext(ctx, it.Obj)
			if !ok {
				return NoneVal(), false
			}
			sub := resolveChainSubIter(ctx, subIterable)
			if ctx != nil && ctx.HasException() {
				return NoneVal(), false
			}
			it.Sub = sub
		}
	case IterKindZip:
		if it.Stop != 0 {
			return NoneVal(), false
		}
		n := len(it.Seq)
		if n == 0 {
			it.Stop = 1
			return NoneVal(), false
		}
		if n <= 4 {
			var buf [4]Value
			for i := range n {
				v, ok := advanceIterNext(ctx, it.Seq[i])
				if !ok {
					it.Stop = 1
					if ctx != nil && it.Step != 0 {
						if i > 0 {
							plural := " "
							if i > 1 {
								plural = "s 1-"
							}
							RaiseValueError(ctx, "zip() argument "+strconv.Itoa(i+1)+" is shorter than argument"+plural+strconv.Itoa(i))
						} else {
							for j := 1; j < n; j++ {
								if _, more := advanceIterNext(ctx, it.Seq[j]); more {
									plural := " "
									if j > 1 {
										plural = "s 1-"
									}
									RaiseValueError(ctx, "zip() argument "+strconv.Itoa(j+1)+" is longer than argument"+plural+strconv.Itoa(j))
									break
								}
							}
						}
					}
					return NoneVal(), false
				}
				buf[i] = v
			}
			return NewTupleFromSlice(buf[:n]), true
		}
		items := make([]Value, n)
		for i := range n {
			v, ok := advanceIterNext(ctx, it.Seq[i])
			if !ok {
				it.Stop = 1
				if ctx != nil && it.Step != 0 {
					if i > 0 {
						plural := " "
						if i > 1 {
							plural = "s 1-"
						}
						RaiseValueError(ctx, "zip() argument "+strconv.Itoa(i+1)+" is shorter than argument"+plural+strconv.Itoa(i))
					} else {
						for j := 1; j < n; j++ {
							if _, more := advanceIterNext(ctx, it.Seq[j]); more {
								plural := " "
								if j > 1 {
									plural = "s 1-"
								}
								RaiseValueError(ctx, "zip() argument "+strconv.Itoa(j+1)+" is longer than argument"+plural+strconv.Itoa(j))
								break
							}
						}
					}
				}
				return NoneVal(), false
			}
			items[i] = v
		}
		return NewTupleFromSlice(items), true
	case IterKindEnumerate:
		v, ok := advanceIterNext(ctx, it.Obj)
		if !ok {
			return NoneVal(), false
		}
		idx := it.Cur
		it.Cur++
		var buf [2]Value
		buf[0] = Int64Val(idx)
		buf[1] = v
		return NewTupleFromSlice(buf[:]), true
	case IterKindFunc:
		if it.NextFn != nil {
			return it.NextFn()
		}
		return NoneVal(), false
	case IterKindString:
		s := it.Obj.AsString()
		if it.Idx >= len(s) {
			return NoneVal(), false
		}
		_, size := utf8.DecodeRuneInString(s[it.Idx:])
		if size == 0 {
			return NoneVal(), false
		}
		res := NewStringVal(s[it.Idx : it.Idx+size])
		it.Idx += size
		return res, true
	case IterKindBytes:
		b := it.Obj.AsBytes().Data
		if it.Idx >= len(b) {
			return NoneVal(), false
		}
		res := Int64Val(int64(b[it.Idx]))
		it.Idx++
		return res, true
	case IterKindByteArray:
		b := it.Obj.AsByteArray().Data
		if it.Idx >= len(b) {
			return NoneVal(), false
		}
		res := Int64Val(int64(b[it.Idx]))
		it.Idx++
		return res, true
	default:
		return NoneVal(), false
	}
}

// AsGenerator performs a zero-cost offset-0 downcast from Value to *PyGenerator.
func (v Value) AsGenerator() *PyGenerator {
	return (*PyGenerator)(unsafe.Pointer(v.ptr))
}

// NewGeneratorVal constructs a heap-allocated KindGenerator Value with pre-allocated stack.
func NewGeneratorVal(name string, code Value, maxStack int, cells []Value, globals *PyDict) Value {
	if maxStack < 8 {
		maxStack = 8
	}
	return NewGeneratorWithStack(name, code, make([]Value, maxStack), 255, cells, globals)
}

// NewGeneratorWithStack constructs a heap-allocated KindGenerator Value using a caller-provided stack slice.
func NewGeneratorWithStack(name string, code Value, stack []Value, poolClass uint8, cells []Value, globals *PyDict) Value {
	gen := &PyGenerator{
		Header:    ObjectHeader{Kind: KindGenerator, Type: GeneratorType},
		Name:      name,
		Code:      code,
		State:     GenCreated,
		Stack:     stack,
		PoolClass: poolClass,
		SP:        0,
		Base:      0,
		IP:        0,
		Cells:     cells,
		Globals:   globals,
		RetVal:    NoneVal(),
	}
	return Value{num: 0, ptr: &gen.Header}
}

// Send advances the generator with a value, returning (yieldedValue, status).
func (g *PyGenerator) Send(ctx *ExecContext, val Value) (Value, IterStatus) {
	if g.State == GenClosed {
		return NoneVal(), IterExhausted
	}
	if g.ResumeHook != nil {
		return g.ResumeHook(ctx, g, val)
	}
	return NoneVal(), IterExhausted
}

// Close closes the generator, marks it GenClosed, and releases pooled stacks.
func (g *PyGenerator) Close(ctx *ExecContext) {
	if g.State == GenClosed || g.State == GenCreated {
		g.State = GenClosed
		if g.ReleaseHook != nil && g.Stack != nil {
			g.ReleaseHook(g)
		}
		return
	}
	if g.State == GenSuspended && ctx != nil {
		_, status := g.Throw(ctx, NewInstanceOfType(GeneratorExitType))
		if status == IterYielded {
			g.State = GenClosed
			if g.ReleaseHook != nil && g.Stack != nil {
				g.ReleaseHook(g)
			}
			ctx.Raise(NewRuntimeError("generator ignored GeneratorExit"))
			return
		}
		if ctx.HasException() {
			exc := ctx.ActiveException
			if IsInstance(exc, GeneratorExitType) || IsInstance(exc, StopIterationType) {
				ctx.ClearException()
			}
		}
	}
	g.State = GenClosed
	if g.ReleaseHook != nil && g.Stack != nil {
		g.ReleaseHook(g)
	}
}

// Throw resumes the generator by raising an exception at the point of suspension.
func (g *PyGenerator) Throw(ctx *ExecContext, exc Value) (Value, IterStatus) {
	if g.State == GenClosed {
		if ctx != nil {
			ctx.Raise(exc)
		}
		return NoneVal(), IterError
	}
	if ctx != nil {
		ctx.Raise(exc)
	}
	return g.Send(ctx, NoneVal())
}

// AsCoroutine performs a zero-cost offset-0 downcast from Value to *PyCoroutine.
func (v Value) AsCoroutine() *PyCoroutine {
	return (*PyCoroutine)(unsafe.Pointer(v.ptr))
}

// NewCoroutineVal constructs a heap-allocated KindCoroutine Value with pre-allocated stack.
func NewCoroutineVal(name string, code Value, maxStack int, cells []Value, globals *PyDict) Value {
	if maxStack < 8 {
		maxStack = 8
	}
	stack := make([]Value, maxStack)
	return NewCoroutineWithStack(name, code, stack, 255, cells, globals)
}

// NewCoroutineWithStack constructs a KindCoroutine Value wrapping an existing
// (possibly pooled) stack slice.
func NewCoroutineWithStack(name string, code Value, stack []Value, poolClass uint8, cells []Value, globals *PyDict) Value {
	coro := &PyCoroutine{
		Header:    ObjectHeader{Kind: KindCoroutine, Type: CoroutineType},
		Name:      name,
		Code:      code,
		State:     CoroCreated,
		Stack:     stack,
		PoolClass: poolClass,
		SP:        0,
		Base:      0,
		IP:        0,
		Cells:     cells,
		Globals:   globals,
		RetVal:    NoneVal(),
	}
	return Value{num: 0, ptr: &coro.Header}
}

// Send advances the coroutine with a value, returning (yieldedValue, status).
func (c *PyCoroutine) Send(ctx *ExecContext, val Value) (Value, IterStatus) {
	if c.State == CoroClosed {
		return NoneVal(), IterExhausted
	}
	if c.ResumeHook != nil {
		return c.ResumeHook(ctx, c, val)
	}
	return NoneVal(), IterExhausted
}

// Close closes the coroutine, marks it CoroClosed, and releases pooled stacks.
func (c *PyCoroutine) Close(ctx *ExecContext) {
	c.State = CoroClosed
	if c.ReleaseHook != nil && c.Stack != nil {
		c.ReleaseHook(c)
	}
}

// Throw resumes the coroutine by raising an exception at the point of suspension.
func (c *PyCoroutine) Throw(ctx *ExecContext, exc Value) (Value, IterStatus) {
	if c.State == CoroClosed {
		return NoneVal(), IterExhausted
	}
	if ctx != nil {
		ctx.Raise(exc)
	}
	return c.Send(ctx, NoneVal())
}

// AsFuture performs a zero-cost offset-0 downcast from Value to *PyFuture.
func (v Value) AsFuture() *PyFuture {
	return (*PyFuture)(unsafe.Pointer(v.ptr))
}

// NewFutureVal constructs a heap-allocated KindFuture Value.
func NewFutureVal() Value {
	fut := &PyFuture{
		Header:   ObjectHeader{Kind: KindFuture, Type: FutureType},
		State:    FutPending,
		doneChan: make(chan struct{}),
	}
	return Value{num: 0, ptr: &fut.Header}
}

func (f *PyFuture) IsDone() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.State != FutPending
}

func (f *PyFuture) Result() (Value, Value) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ResultVal, f.ExcVal
}

func (f *PyFuture) SetResult(v Value) {
	f.mu.Lock()
	if f.State != FutPending {
		f.mu.Unlock()
		return
	}
	f.State = FutFinished
	f.ResultVal = v
	cbs := f.Callbacks
	f.Callbacks = nil
	close(f.doneChan)
	f.mu.Unlock()

	futVal := Value{num: 0, ptr: &f.Header}
	for _, cb := range cbs {
		cb(futVal)
	}
}

func (f *PyFuture) SetException(exc Value) {
	f.mu.Lock()
	if f.State != FutPending {
		f.mu.Unlock()
		return
	}
	f.State = FutFinished
	f.ExcVal = exc
	cbs := f.Callbacks
	f.Callbacks = nil
	close(f.doneChan)
	f.mu.Unlock()

	futVal := Value{num: 0, ptr: &f.Header}
	for _, cb := range cbs {
		cb(futVal)
	}
}

func (f *PyFuture) AddDoneCallback(cb func(Value)) {
	f.mu.Lock()
	if f.State != FutPending {
		f.mu.Unlock()
		futVal := Value{num: 0, ptr: &f.Header}
		cb(futVal)
		return
	}
	f.Callbacks = append(f.Callbacks, cb)
	f.mu.Unlock()
}

func (f *PyFuture) DoneChan() <-chan struct{} {
	return f.doneChan
}

// AsTask performs a zero-cost offset-0 downcast from Value to *PyTask.
func (v Value) AsTask() *PyTask {
	return (*PyTask)(unsafe.Pointer(v.ptr))
}

// NewTaskVal constructs a heap-allocated KindTask Value.
func NewTaskVal(coro *PyCoroutine) Value {
	task := &PyTask{
		Header: ObjectHeader{Kind: KindTask, Type: TaskType},
		Future: PyFuture{
			Header:   ObjectHeader{Kind: KindFuture, Type: FutureType},
			State:    FutPending,
			doneChan: make(chan struct{}),
		},
		Coro: coro,
	}
	return Value{num: 0, ptr: &task.Header}
}

func (t *PyTask) IsDone() bool {
	return t.Future.IsDone()
}

func (t *PyTask) Result() (Value, Value) {
	return t.Future.Result()
}

func (t *PyTask) SetResult(v Value) {
	t.Future.SetResult(v)
}

func (t *PyTask) SetException(exc Value) {
	t.Future.SetException(exc)
}

func (t *PyTask) AddDoneCallback(cb func(Value)) {
	t.Future.AddDoneCallback(cb)
}

// Standard Exception Constructors

// NewException constructs a new Python exception instance of the specified class.
func NewException(cls *PyType, msg string) Value {
	if cls == nil {
		cls = ExceptionType
	}
	inst := NewInstanceOfType(cls)
	argsTuple := NewTupleVal()
	if msg != "" {
		strVal := InternString(msg)
		argsTuple = NewTupleVal(strVal)
	}
	instObj := inst.AsInstance()
	instObj.SetAttr(strArgs, argsTuple)
	instObj.SetAttr(strTraceback, NoneVal())
	instObj.SetAttr(strCause, NoneVal())
	instObj.SetAttr(strContext, NoneVal())
	instObj.SetAttr(strSuppressContext, BoolVal(false))
	return inst
}

// NewTypeError constructs a TypeError instance.
func NewTypeError(msg string) Value {
	return NewException(TypeErrorType, msg)
}

// NewValueError constructs a ValueError instance.
func NewValueError(msg string) Value {
	return NewException(ValueErrorType, msg)
}

var (
	excShapesOnce    sync.Once
	lazyAttrErrShape *Shape
	keyErrShape      *Shape
	stopIterShape    *Shape
	emptyTupleVal    Value
)

func typeRootShapeOrDefault(tp *PyType) *Shape {
	if tp != nil && tp.RootShape != nil {
		return tp.RootShape
	}
	return defaultRootShape
}

func initExcShapes() {
	excShapesOnce.Do(func() {
		lazyAttrErrShape = typeRootShapeOrDefault(AttributeErrorType).
			Transition(strArgs).
			Transition(strTraceback).
			Transition(strLazyType).
			Transition(strLazyAttr)
		keyErrShape = typeRootShapeOrDefault(KeyErrorType).
			Transition(strArgs).
			Transition(strTraceback).
			Transition(strLazyKey)
		stopIterShape = typeRootShapeOrDefault(StopIterationType).
			Transition(strArgs).
			Transition(strTraceback).
			Transition(strValue)
		emptyTupleVal = NewTupleVal()
	})
}

// NewKeyError constructs a KeyError instance lazily without allocating an args tuple until accessed.
func NewKeyError(key Value) Value {
	initExcShapes()
	obj := allocInstanceBundle(KeyErrorType, keyErrShape, 3, 3)
	obj.Slots[0] = UnboundVal()
	obj.Slots[1] = NoneVal()
	obj.Slots[2] = key
	return Value{num: 0, ptr: &obj.Header}
}

// NewIndexError constructs an IndexError instance.
func NewIndexError(msg string) Value {
	return NewException(IndexErrorType, msg)
}

// NewZeroDivisionError constructs a ZeroDivisionError instance.
func NewZeroDivisionError(msg string) Value {
	return NewException(ZeroDivisionErrorType, msg)
}

// NewOverflowError constructs an OverflowError instance.
func NewOverflowError(msg string) Value {
	return NewException(OverflowErrorType, msg)
}

// NewNameError constructs a NameError instance.
func NewNameError(msg string) Value {
	return NewException(NameErrorType, msg)
}

// NewAttributeError constructs an AttributeError instance.
func NewAttributeError(msg string) Value {
	return NewException(AttributeErrorType, msg)
}

// NewLazyAttributeError constructs an AttributeError instance without eagerly
// formatting or interning the error message string. The message is materialized
// lazily only if .args or __str__() is accessed.
func NewLazyAttributeError(targetType *PyType, attrName Value) Value {
	return NewLazyAttributeErrorCtx(nil, targetType, attrName)
}

// NewLazyAttributeErrorCtx constructs a lazy AttributeError, reusing an instance
// from ctx.LazyAttrErrFreeList when available instead of boxing through sync.Pool.
func NewLazyAttributeErrorCtx(ctx *ExecContext, targetType *PyType, attrName Value) Value {
	initExcShapes()
	var obj *PyInstance
	if ctx != nil && len(ctx.LazyAttrErrFreeList) > 0 {
		n := len(ctx.LazyAttrErrFreeList) - 1
		obj = ctx.LazyAttrErrFreeList[n]
		ctx.LazyAttrErrFreeList[n] = nil
		ctx.LazyAttrErrFreeList = ctx.LazyAttrErrFreeList[:n]
		obj.Slots = obj.Slots[:4]
	} else {
		b := &pyInstanceBundle4{
			inst: PyInstance{
				Header: ObjectHeader{Kind: KindInstance, Type: AttributeErrorType},
			},
		}
		b.inst.Slots = b.slots[:4]
		obj = &b.inst
	}
	var flags uint8
	if ctx != nil && !MultiThreadedFlag.Load() {
		flags = FlagEphemeralAttrErr
	}
	obj.Header.Flags = flags
	obj.Header.Type = AttributeErrorType
	obj.Shape = lazyAttrErrShape
	obj.dictView = nil
	obj.Slots[0] = UnboundVal()
	obj.Slots[1] = NoneVal()
	if targetType != nil {
		obj.Slots[2] = TypeVal(targetType)
	} else {
		obj.Slots[2] = NoneVal()
	}
	obj.Slots[3] = attrName
	return Value{num: 0, ptr: &obj.Header}
}

// ReleaseLazyAttributeError returns an unexposed lazy AttributeError instance
// after clearing its slot references.
func ReleaseLazyAttributeError(exc Value) {
	ReleaseLazyAttributeErrorCtx(nil, exc)
}

// ReleaseLazyAttributeErrorCtx clears and recycles an unexposed lazy AttributeError
// instance into ctx.LazyAttrErrFreeList when ctx is provided.
func ReleaseLazyAttributeErrorCtx(ctx *ExecContext, exc Value) {
	if !exc.IsInstance() {
		return
	}
	inst := exc.AsInstance()
	if lazyAttrErrShape == nil || inst.Shape != lazyAttrErrShape || inst.dictView != nil || len(inst.Slots) < 4 || !inst.Slots[0].IsUnbound() {
		return
	}
	if cap(inst.Slots) != 4 {
		return
	}
	clear(inst.Slots[:4])
	inst.Header.Flags = 0
	inst.Slots = inst.Slots[:0]
	inst.Shape = nil
	if ctx != nil && len(ctx.LazyAttrErrFreeList) < 64 {
		ctx.LazyAttrErrFreeList = append(ctx.LazyAttrErrFreeList, inst)
	}
}

// ExceptionArgs returns the exception's args tuple, lazily materializing it
// for lazy AttributeError and KeyError instances.
func ExceptionArgs(inst *PyInstance) (Value, bool) {
	if inst == nil {
		return Value{}, false
	}
	initExcShapes()
	if inst.Shape == lazyAttrErrShape {
		if !inst.Slots[0].IsUnbound() {
			return inst.Slots[0], true
		}
		tpName := "object"
		if inst.Slots[2].IsType() {
			tpName = inst.Slots[2].AsType().Name
		}
		attrStr := ""
		if inst.Slots[3].IsAnyString() {
			attrStr = inst.Slots[3].AsString()
		} else if !inst.Slots[3].IsNone() && !inst.Slots[3].IsUnbound() {
			attrStr = inst.Slots[3].String()
		}
		msg := "'" + tpName + "' object has no attribute '" + attrStr + "'"
		tup := NewTupleVal(NewStringVal(msg))
		inst.Slots[0] = tup
		return tup, true
	}
	if inst.Shape == keyErrShape {
		if !inst.Slots[0].IsUnbound() {
			return inst.Slots[0], true
		}
		tup := NewTupleVal(inst.Slots[2])
		inst.Slots[0] = tup
		return tup, true
	}
	if argsVal, ok := inst.GetAttr(strArgs); ok {
		if argsVal.IsUnbound() {
			// Check if lazy attributes were preserved across a shape transition (e.g. __traceback__ set).
			if tpVal, ok1 := inst.GetAttr(strLazyType); ok1 {
				if attrVal, ok2 := inst.GetAttr(strLazyAttr); ok2 {
					tpName := "object"
					if tpVal.IsType() {
						tpName = tpVal.AsType().Name
					}
					attrStr := ""
					if attrVal.IsAnyString() {
						attrStr = attrVal.AsString()
					} else if !attrVal.IsNone() && !attrVal.IsUnbound() {
						attrStr = attrVal.String()
					}
					msg := "'" + tpName + "' object has no attribute '" + attrStr + "'"
					tup := NewTupleVal(NewStringVal(msg))
					inst.SetAttr(strArgs, tup)
					return tup, true
				}
			}
			if keyVal, ok1 := inst.GetAttr(strLazyKey); ok1 {
				tup := NewTupleVal(keyVal)
				inst.SetAttr(strArgs, tup)
				return tup, true
			}
			return Value{}, false
		}
		return argsVal, true
	}
	return Value{}, false
}

// NewRuntimeError constructs a RuntimeError instance.
func NewRuntimeError(msg string) Value {
	return NewException(RuntimeErrorType, msg)
}

// NewImportError constructs an ImportError instance.
func NewImportError(msg string) Value {
	return NewException(ImportErrorType, msg)
}

// NewModuleNotFoundError constructs a ModuleNotFoundError instance.
func NewModuleNotFoundError(msg string) Value {
	return NewException(ModuleNotFoundErrorType, msg)
}

// NewStopIteration constructs a StopIteration instance.
func NewStopIteration(retVal Value) Value {
	initExcShapes()
	obj := allocInstanceBundle(StopIterationType, stopIterShape, 3, 3)
	obj.Slots[1] = NoneVal()
	if !retVal.IsNone() && retVal.ptr != nil && retVal.ptr != &tagNone {
		obj.Slots[0] = NewTupleVal(retVal)
		obj.Slots[2] = retVal
	} else {
		obj.Slots[0] = emptyTupleVal
		obj.Slots[2] = NoneVal()
	}
	return Value{num: 0, ptr: &obj.Header}
}

func (d *PyDict) initTable(minEntries int) {
	tableCap := uint32(8)
	for int(tableCap)*2 < minEntries*3 {
		tableCap <<= 1
	}
	shift := uint8(0)
	if tableCap > 65536 {
		shift = 2
	} else if tableCap > 256 {
		shift = 1
	}
	switch {
	case tableCap == 8:
		b := &smallDictBacking{}
		d.indices = b.indices[:]
		d.mask = 7
		d.indexShift = 0
		if cap(d.Entries) < len(b.entries) {
			d.Entries = b.entries[:0]
		}
	case tableCap == 16 && shift == 0 && AcquireDictTable16Hook != nil:
		d.Header.Flags &^= FlagEphemeralDict | FlagEphemeralFrameDict
		if entries, indices, ok := AcquireDictTable16Hook(); ok &&
			cap(entries) == Dict16EntryCap && len(indices) == 16 {
			d.Entries = entries[:0]
			d.indices = indices
			d.mask = 15
			d.indexShift = 0
		} else {
			b := &dict16Backing{}
			d.indices = b.indices[:]
			d.mask = 15
			d.indexShift = 0
			if cap(d.Entries) < Dict16EntryCap {
				d.Entries = b.entries[:0]
			}
		}
	case tableCap == 32 && shift == 0 && minEntries <= Dict32EntryCap:
		d.Header.Flags &^= FlagEphemeralDict | FlagEphemeralFrameDict
		b := &dict32Backing{}
		d.indices = b.indices[:]
		d.mask = 31
		d.indexShift = 0
		if cap(d.Entries) < Dict32EntryCap {
			d.Entries = b.entries[:0]
		}
	default:
		d.Header.Flags &^= FlagEphemeralDict | FlagEphemeralFrameDict
		d.indices = make([]byte, int(tableCap)<<shift)
		d.mask = tableCap - 1
		d.indexShift = shift
		maxDense := int(tableCap) * 2 / 3
		if cap(d.Entries) < maxDense {
			d.Entries = make([]DictEntry, 0, maxDense)
		}
	}
	if d.VersionTag == 0 {
		d.VersionTag = 1
	}
}

// Reserve pre-sizes an empty combined PyDict to hold at least n entries in one
// allocation. No-op for non-empty, live, split, or already-sufficient dicts.
func (d *PyDict) Reserve(n int) {
	if n <= 0 || d.Live != nil || d.split != nil {
		return
	}
	if d.lockDict() {
		defer d.mu.Unlock()
	}
	if d.used != 0 || d.filled != 0 || d.split != nil {
		return
	}
	if len(d.indices) > 0 && int(d.mask+1)*2 >= n*3 {
		return
	}
	d.initTable(n)
}

func (d *PyDict) readIndex(slot uint32) uint32 {
	switch d.indexShift {
	case 0:
		return uint32(d.indices[slot])
	case 1:
		off := slot << 1
		return uint32(d.indices[off]) | (uint32(d.indices[off+1]) << 8)
	default:
		off := slot << 2
		return uint32(d.indices[off]) |
			(uint32(d.indices[off+1]) << 8) |
			(uint32(d.indices[off+2]) << 16) |
			(uint32(d.indices[off+3]) << 24)
	}
}

func (d *PyDict) writeIndex(slot uint32, val uint32) {
	switch d.indexShift {
	case 0:
		d.indices[slot] = uint8(val)
	case 1:
		off := slot << 1
		d.indices[off] = uint8(val)
		d.indices[off+1] = uint8(val >> 8)
	default:
		off := slot << 2
		d.indices[off] = uint8(val)
		d.indices[off+1] = uint8(val >> 8)
		d.indices[off+2] = uint8(val >> 16)
		d.indices[off+3] = uint8(val >> 24)
	}
}

func (d *PyDict) maxIndexValue() int {
	switch d.indexShift {
	case 0:
		return 0xFF
	case 1:
		return 0xFFFF
	default:
		return 0x7FFFFFFF
	}
}

func (d *PyDict) resize(minEntries int) {
	d.Header.Flags &^= FlagEphemeralDict | FlagEphemeralFrameDict
	tableCap := uint32(8)
	for int(tableCap)*2 <= minEntries*3 {
		tableCap <<= 1
	}
	shift := uint8(0)
	if tableCap > 65536 {
		shift = 2
	} else if tableCap > 256 {
		shift = 1
	}

	oldEntries := d.Entries
	oldIndices := d.indices
	oldIsSmall := cap(oldEntries) == 5 && len(oldIndices) == 8
	maxDense := int(tableCap) * 2 / 3
	var newEntries []DictEntry
	if tableCap == 16 && shift == 0 && AcquireDictTable16Hook != nil {
		if entries, indices, ok := AcquireDictTable16Hook(); ok &&
			cap(entries) == Dict16EntryCap && len(indices) == 16 {
			newEntries = entries[:0]
			d.indices = indices
			clear(d.indices)
		} else {
			newEntries = make([]DictEntry, 0, maxDense)
			d.indices = make([]byte, int(tableCap)<<shift)
		}
	} else {
		newEntries = make([]DictEntry, 0, maxDense)
		d.indices = make([]byte, int(tableCap)<<shift)
	}
	d.mask = tableCap - 1
	d.indexShift = shift
	d.used = 0
	d.filled = 0

	mask := d.mask
	for i := range oldEntries {
		e := &oldEntries[i]
		if !e.Active {
			continue
		}
		newIdx := len(newEntries)
		newEntries = append(newEntries, *e)
		slot := e.Hash & mask
		for d.readIndex(slot) != dictIndexEmpty {
			slot = (slot + 1) & mask
		}
		d.writeIndex(slot, uint32(newIdx+dictIndexOffset))
		d.used++
		d.filled++
	}
	if oldIsSmall {
		clear(oldEntries[:cap(oldEntries)])
	} else if ReleaseDictTable16Hook != nil &&
		len(oldIndices) == 16 && cap(oldEntries) == Dict16EntryCap &&
		tableCap != 16 {
		clear(oldEntries[:cap(oldEntries)])
		clear(oldIndices)
		ReleaseDictTable16Hook(oldEntries[:0], oldIndices)
	}
	d.Entries = newEntries
}

// IsSmallInline reports whether d has no external owner and holds a pre-allocated
// 5-entry / 8-index small table for safe freelist recycling.
func (d *PyDict) IsSmallInline() bool {
	return d.owner == nil &&
		cap(d.Entries) == 5 &&
		len(d.indices) == 8
}

// InitSmallInlineTable configures a pooled small PyDict for reuse without
// heap-allocating backing slices. Pooled structs arrive cleared via ResetForPool.
func (d *PyDict) InitSmallInlineTable() {
	d.mask = 7
	d.indexShift = 0
	d.used = 0
	d.filled = 0
	d.Entries = d.Entries[:0]
}

// ResetForPool clears all entries and metadata on a pooled small PyDict while
// retaining its 5-entry / 8-index backing slices for VM freelist reuse.
func (d *PyDict) ResetForPool() {
	if cap(d.Entries) > 0 {
		clear(d.Entries[:cap(d.Entries)])
		d.Entries = d.Entries[:0]
	}
	clear(d.indices)
	d.mask = 7
	d.used = 0
	d.filled = 0
	d.indexShift = 0
	d.VersionTag = 0
	d.Header.Flags &^= FlagEphemeralDict | FlagEphemeralFrameDict
}

// IsMediumPoolable reports whether d has no external owner and holds a 32-slot
// hash table (up to 16 entries / maxDense 21) for medium dict freelist reuse.
func (d *PyDict) IsMediumPoolable() bool {
	return d.owner == nil && len(d.indices) == 32 && cap(d.Entries) == 21
}

// InitMediumTable prepares a pooled medium PyDict for reuse without reallocating.
// Pooled tables arrive cleared via ResetMediumForPool.
func (d *PyDict) InitMediumTable() {
	d.mask = 31
	d.indexShift = 0
	d.used = 0
	d.filled = 0
	d.Entries = d.Entries[:0]
}

// ResetMediumForPool zeroes all entries/indices while retaining the 32-slot table.
func (d *PyDict) ResetMediumForPool() {
	if cap(d.Entries) > 0 {
		clear(d.Entries[:cap(d.Entries)])
		d.Entries = d.Entries[:0]
	}
	clear(d.indices)
	d.mask = 0
	d.used = 0
	d.filled = 0
	d.indexShift = 0
	d.VersionTag = 0
	d.Header.Flags &^= FlagEphemeralDict | FlagEphemeralFrameDict
}

// Dict16EntryCap is the dense-entry capacity of a 16-slot table
// (16 * 2 / 3 truncated). Dicts with 6-10 entries live here after growing
// past the 5-entry inline store.
const Dict16EntryCap = 10

// IsDict16Poolable reports whether d holds a 16-slot table with no external
// owner for 16-entry freelist reuse.
func (d *PyDict) IsDict16Poolable() bool {
	return d.owner == nil && len(d.indices) == 16 && cap(d.Entries) == Dict16EntryCap
}

// InitDict16Table prepares a pooled 16-slot PyDict for reuse.
// Pooled tables arrive cleared via ResetDict16ForPool.
func (d *PyDict) InitDict16Table() {
	d.mask = 15
	d.indexShift = 0
	d.used = 0
	d.filled = 0
	d.Entries = d.Entries[:0]
}

// ResetDict16ForPool zeroes entries/indices while retaining the 16-slot table.
// Clearing the Value slots keeps pooled slabs invisible to the collector,
// which matters for GODEBUG=gccheckmark=1.
func (d *PyDict) ResetDict16ForPool() {
	if cap(d.Entries) > 0 {
		clear(d.Entries[:cap(d.Entries)])
		d.Entries = d.Entries[:0]
	}
	if len(d.indices) > 0 {
		clear(d.indices)
	}
	d.mask = 0
	d.used = 0
	d.filled = 0
	d.indexShift = 0
	d.VersionTag = 0
	d.Header.Flags &^= FlagEphemeralDict | FlagEphemeralFrameDict
}

// Dict32EntryCap is the dense-entry capacity of a 32-slot table for
// pooled 11-20 entry dicts.
const Dict32EntryCap = 20

// IsDict32Poolable reports whether d holds a 32-slot table with no external
// owner for 32-slot (11-20 entry) freelist reuse.
func (d *PyDict) IsDict32Poolable() bool {
	return d.owner == nil && len(d.indices) == 32 && cap(d.Entries) == Dict32EntryCap
}

// InitDict32Table prepares a pooled 32-slot PyDict for reuse.
// Pooled tables arrive cleared via ResetDict32ForPool.
func (d *PyDict) InitDict32Table() {
	d.mask = 31
	d.indexShift = 0
	d.used = 0
	d.filled = 0
	d.Entries = d.Entries[:0]
}

// ResetDict32ForPool zeroes all entries and indices while retaining the 32-slot table.
// Clearing all Value slots keeps pooled slabs invisible to GODEBUG=gccheckmark=1.
func (d *PyDict) ResetDict32ForPool() {
	if cap(d.Entries) > 0 {
		clear(d.Entries[:cap(d.Entries)])
		d.Entries = d.Entries[:0]
	}
	if len(d.indices) > 0 {
		clear(d.indices)
	}
	d.mask = 0
	d.used = 0
	d.filled = 0
	d.indexShift = 0
	d.VersionTag = 0
	d.Header.Flags &^= FlagEphemeralDict | FlagEphemeralFrameDict
}

// AcquireDictTable16Hook, when set, returns a cleared 16-slot table slab:
// entries with len 0 cap Dict16EntryCap and indices with len 16. The VM
// registers a per-VM pool; val falls back to make when the hook is nil or
// reports a miss. Guarded by !MultiThreadedFlag.Load() by the provider.
var AcquireDictTable16Hook func() (entries []DictEntry, indices []byte, ok bool)

// ReleaseDictTable16Hook, when set, accepts a cleared 16-slot table slab for
// reuse. Providers must clear Value slots before retaining.
var ReleaseDictTable16Hook func(entries []DictEntry, indices []byte)

// Set inserts or updates a key-value pair in PyDict while preserving insertion order
// and keeping the first-inserted key on value updates (Python dict invariant) in a thread-safe manner.
// lockDict takes the dictionary lock when the runtime is multi-threaded.
//
// A single-threaded program never shares a dictionary between goroutines, so
// the mutex is skipped entirely: the hot loop reads globals and builtins on
// every instruction and the read/write lock shows up in profiles there.
func (d *PyDict) lockDict() bool {
	if !d.Shared && !MultiThreadedFlag.Load() {
		return false
	}
	d.mu.Lock()
	return true
}

// rlockDict takes the dictionary read lock when the runtime is multi-threaded.
func (d *PyDict) rlockDict() bool {
	if !d.Shared && !MultiThreadedFlag.Load() {
		return false
	}
	d.mu.RLock()
	return true
}

func (d *PyDict) Set(key, value Value) {
	if d.Live != nil {
		d.Live.SetAttr(InternValue(key), value)
		return
	}
	if d.lockDict() {
		defer d.mu.Unlock()
	}
	d.setLocked(key, value)
}

// dictKeyEqual compares a dict entry key against a lookup key using O(1)
// pointer/inline-string fast paths before falling back to dictKeyEqualSlow.
func dictKeyEqual(a, b Value) bool {
	if a == b {
		return true
	}
	if a.ptr == &tagInlineStr && b.ptr == &tagInlineStr {
		return false
	}
	if a.ptr != nil && b.ptr != nil {
		if (a.ptr.Flags & b.ptr.Flags & FlagInterned) != 0 {
			return false
		}
		if (a.ptr == &tagInlineStr || a.ptr.Kind == KindString) &&
			(b.ptr == &tagInlineStr || b.ptr.Kind == KindString) {
			return StringEquals(a, b)
		}
	}
	return dictKeyEqualSlow(a, b)
}

//go:noinline
func dictKeyEqualSlow(a, b Value) bool {
	if a.ptr != nil && b.ptr != nil &&
		a.ptr.Kind == KindTuple && b.ptr.Kind == KindTuple &&
		a.ptr.Type == TupleType && b.ptr.Type == TupleType {
		return tupleDictKeyEqual(a, b)
	}
	return Equals(a, b)
}

//go:noinline
func tupleDictKeyEqual(a, b Value) bool {
	if a.ptr == b.ptr {
		return true
	}
	ia := (*PyTuple)(unsafe.Pointer(a.ptr)).Items
	ib := (*PyTuple)(unsafe.Pointer(b.ptr)).Items
	if len(ia) != len(ib) {
		return false
	}
	for i := range ia {
		ea, eb := ia[i], ib[i]
		if ea == eb {
			// Matches CPython PyObject_RichCompareBool: identical element
			// identity is equal inside tuple dict/set keys (even for NaN).
			continue
		}
		if ea.ptr == &tagInt64 && eb.ptr == &tagInt64 {
			return false
		}
		if ea.ptr == &tagInlineStr && eb.ptr == &tagInlineStr {
			return false
		}
		if (ea.ptr == &tagInt64 && eb.ptr == &tagBool) || (ea.ptr == &tagBool && eb.ptr == &tagInt64) {
			if ea.num != eb.num {
				return false
			}
			continue
		}
		if ea.ptr != nil && eb.ptr != nil {
			if (ea.ptr.Flags & eb.ptr.Flags & FlagInterned) != 0 {
				return false
			}
			if (ea.ptr == &tagInlineStr || ea.ptr.Kind == KindString) &&
				(eb.ptr == &tagInlineStr || eb.ptr.Kind == KindString) {
				if !StringEquals(ea, eb) {
					return false
				}
				continue
			}
		}
		if !Equals(ea, eb) {
			return false
		}
	}
	return true
}

func (d *PyDict) bumpVersionTag() {
	if d.Shared || MultiThreadedFlag.Load() {
		atomic.AddUint64(&d.VersionTag, 1)
		return
	}
	d.VersionTag++
}

func (d *PyDict) setLocked(key, value Value) {
	if len(d.indices) == 0 {
		if d.Live != nil {
			d.Live.SetAttr(InternValue(key), value)
			return
		}
		if d.split != nil {
			if d.setSplitLocked(key, value) {
				return
			}
		} else {
			d.initTable(4)
		}
	}
	if d.mask == 7 && d.indexShift == 0 {
		for i := range d.Entries {
			e := &d.Entries[i]
			if e.Active && (e.Key == key || dictKeyEqual(e.Key, key)) {
				e.Value = value
				d.bumpVersionTag()
				if d.owner != nil {
					d.owner.bumpVersion()
				}
				return
			}
		}
	}

	h := HashValue(key)
	mask := d.mask
	slot := h & mask
	insertSlot := -1

	if d.indexShift == 0 {
		for {
			raw := d.indices[slot]
			if raw == dictIndexEmpty {
				if insertSlot < 0 {
					insertSlot = int(slot)
				}
				break
			}
			if raw == dictIndexDummy {
				if insertSlot < 0 {
					insertSlot = int(slot)
				}
			} else {
				e := &d.Entries[int(raw)-dictIndexOffset]
				if e.Key == key || (e.Hash == h && e.Active && dictKeyEqual(e.Key, key)) {
					e.Value = value
					d.bumpVersionTag()
					if d.owner != nil {
						d.owner.bumpVersion()
					}
					return
				}
			}
			slot = (slot + 1) & mask
		}
	} else {
		for {
			raw := d.readIndex(slot)
			if raw == dictIndexEmpty {
				if insertSlot < 0 {
					insertSlot = int(slot)
				}
				break
			}
			if raw == dictIndexDummy {
				if insertSlot < 0 {
					insertSlot = int(slot)
				}
			} else {
				e := &d.Entries[int(raw)-dictIndexOffset]
				if e.Key == key || (e.Hash == h && e.Active && dictKeyEqual(e.Key, key)) {
					e.Value = value
					d.bumpVersionTag()
					if d.owner != nil {
						d.owner.bumpVersion()
					}
					return
				}
			}
			slot = (slot + 1) & mask
		}
	}

	// Inserting a new key: check if table needs resizing.
	if (int(d.filled)+1)*3 > int(d.mask+1)*2 || len(d.Entries)+dictIndexOffset >= d.maxIndexValue() {
		d.resize(int(d.used) + 1)
		mask = d.mask
		slot = h & mask
		for d.readIndex(slot) != dictIndexEmpty {
			slot = (slot + 1) & mask
		}
		insertSlot = int(slot)
	}

	entryIdx := len(d.Entries)
	d.Entries = append(d.Entries, DictEntry{
		Hash:   h,
		Active: true,
		Key:    key,
		Value:  value,
	})
	if d.readIndex(uint32(insertSlot)) == dictIndexEmpty {
		d.filled++
	}
	d.writeIndex(uint32(insertSlot), uint32(entryIdx+dictIndexOffset))
	d.used++
	d.bumpVersionTag()
	if d.owner != nil {
		d.owner.bumpVersion()
	}
}

// PutNew inserts a key-value pair into a pre-sized dict (e.g. fresh parser output),
// updating in-place if the key already exists (matching Python duplicate-key semantics).
func (d *PyDict) PutNew(key, value Value) {
	d.PutNewHashed(key, value, HashValue(key))
}

// PutNewHashed inserts a key-value pair with a pre-computed hash into a pre-sized dict,
// updating in-place if the key already exists (matching Python duplicate-key semantics).
func (d *PyDict) PutNewHashed(key, value Value, h uint32) {
	if len(d.indices) == 0 {
		if d.Live != nil {
			d.Live.SetAttr(InternValue(key), value)
			return
		}
		if d.split != nil {
			d.promoteSplitToCombined()
		} else {
			d.initTable(4)
		}
	}
	mask := d.mask
	slot := h & mask
	if (int(d.filled)+1)*3 > int(mask+1)*2 || len(d.Entries)+dictIndexOffset >= d.maxIndexValue() {
		d.resize(int(d.used) + 1)
		mask = d.mask
		slot = h & mask
	}
	if d.indexShift == 0 {
		for {
			raw := d.indices[slot]
			if raw == dictIndexEmpty {
				break
			}
			e := &d.Entries[int(raw)-dictIndexOffset]
			if e.Key == key || (e.Hash == h && e.Active && dictKeyEqual(e.Key, key)) {
				e.Value = value
				d.bumpVersionTag()
				if d.owner != nil {
					d.owner.bumpVersion()
				}
				return
			}
			slot = (slot + 1) & mask
		}
		entryIdx := len(d.Entries)
		d.Entries = append(d.Entries, DictEntry{
			Key:    key,
			Value:  value,
			Hash:   h,
			Active: true,
		})
		d.indices[slot] = byte(entryIdx + dictIndexOffset)
	} else {
		for {
			raw := d.readIndex(slot)
			if raw == dictIndexEmpty {
				break
			}
			e := &d.Entries[int(raw)-dictIndexOffset]
			if e.Key == key || (e.Hash == h && e.Active && dictKeyEqual(e.Key, key)) {
				e.Value = value
				d.bumpVersionTag()
				if d.owner != nil {
					d.owner.bumpVersion()
				}
				return
			}
			slot = (slot + 1) & mask
		}
		entryIdx := len(d.Entries)
		d.Entries = append(d.Entries, DictEntry{
			Key:    key,
			Value:  value,
			Hash:   h,
			Active: true,
		})
		d.writeIndex(slot, uint32(entryIdx+dictIndexOffset))
	}
	d.used++
	d.filled++
	d.bumpVersionTag()
	if d.owner != nil {
		d.owner.bumpVersion()
	}
}

// InitArenaDict initializes a PyDict allocated from a slab or arena.
func InitArenaDict(d *PyDict, capHint int, entries []DictEntry) {
	d.Header = ObjectHeader{Kind: KindDict, Type: DictType}
	tableCap := uint32(8)
	for int(tableCap)*2 < capHint*3 {
		tableCap <<= 1
	}
	switch {
	case tableCap == 8:
		d.indices = make([]byte, 8)
	case tableCap == 16 && AcquireDictTable16Hook != nil && !MultiThreadedFlag.Load():
		if pooledEntries, pooledIndices, ok := AcquireDictTable16Hook(); ok &&
			cap(pooledEntries) == Dict16EntryCap && len(pooledIndices) == 16 {
			d.indices = pooledIndices
			clear(d.indices)
			d.Entries = pooledEntries[:0]
			d.mask = tableCap - 1
			d.indexShift = 0
			d.VersionTag = 1
			return
		}
		d.indices = make([]byte, int(tableCap))
	default:
		d.indices = make([]byte, int(tableCap))
	}
	d.mask = tableCap - 1
	d.indexShift = 0
	d.Entries = entries[:0]
	d.VersionTag = 1
}

// Get retrieves a value by key from PyDict in a thread-safe manner with 0 heap allocations.
func (d *PyDict) Get(key Value) (Value, bool) {
	if d.Live != nil {
		return d.Live.GetAttr(key)
	}
	if d.rlockDict() {
		defer d.mu.RUnlock()
	}
	return d.getLocked(key)
}

// GetWithSlot returns the value, slot index inside Entries, and whether the key was found.
// This allows inline cache revalidation in O(1) without re-hashing when dict version changes.
func (d *PyDict) GetWithSlot(key Value) (Value, int, bool) {
	if d.Live != nil {
		return d.liveGetWithSlot(key)
	}
	if d.rlockDict() {
		defer d.mu.RUnlock()
	}
	return d.getWithSlotLocked(key)
}

// GetWithSlotHint checks the cached slot index first under RLock before falling back to full lookup.
func (d *PyDict) GetWithSlotHint(key Value, slotHint int) (Value, int, bool) {
	if d.Live != nil {
		return d.liveGetWithSlot(key)
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if len(d.Entries) == 0 {
		if sp := d.split; sp != nil {
			if slotHint >= 0 && slotHint < sp.keys.Count && (sp.keys.Keys[slotHint] == key || dictKeyEqual(sp.keys.Keys[slotHint], key)) {
				return sp.vals[slotHint], slotHint, true
			}
			if idx := d.splitLookup(key); idx >= 0 {
				return sp.vals[idx], idx, true
			}
		}
		return NoneVal(), -1, false
	}
	if slotHint >= 0 && slotHint < len(d.Entries) {
		e := &d.Entries[slotHint]
		if e.Active && dictKeyEqual(e.Key, key) {
			return e.Value, slotHint, true
		}
	}
	return d.getWithSlotLocked(key)
}

func (d *PyDict) getWithSlotLocked(key Value) (Value, int, bool) {
	if len(d.Entries) == 0 {
		if d.Live != nil {
			return d.liveGetWithSlot(key)
		}
		if d.split != nil {
			return d.getWithSlotSplitLocked(key)
		}
		return NoneVal(), -1, false
	}
	if d.mask == 7 && d.indexShift == 0 {
		for i := range d.Entries {
			e := &d.Entries[i]
			if e.Active && (e.Key == key || dictKeyEqual(e.Key, key)) {
				return e.Value, i, true
			}
		}
		return NoneVal(), -1, false
	}

	h := HashValue(key)
	mask := d.mask
	slot := h & mask

	if d.indexShift == 0 {
		for {
			raw := d.indices[slot]
			if raw == dictIndexEmpty {
				return NoneVal(), -1, false
			}
			if raw >= dictIndexOffset {
				idx := int(raw) - dictIndexOffset
				e := &d.Entries[idx]
				if e.Key == key || (e.Hash == h && e.Active && dictKeyEqual(e.Key, key)) {
					return e.Value, idx, true
				}
			}
			slot = (slot + 1) & mask
		}
	}

	for {
		raw := d.readIndex(slot)
		if raw == dictIndexEmpty {
			return NoneVal(), -1, false
		}
		if raw >= dictIndexOffset {
			idx := int(raw) - dictIndexOffset
			e := &d.Entries[idx]
			if e.Key == key || (e.Hash == h && e.Active && dictKeyEqual(e.Key, key)) {
				return e.Value, idx, true
			}
		}
		slot = (slot + 1) & mask
	}
}

func (d *PyDict) getLocked(key Value) (Value, bool) {
	if len(d.Entries) == 0 {
		if d.Live != nil {
			return d.Live.GetAttr(key)
		}
		if d.split != nil {
			return d.getSplitLocked(key)
		}
		return NoneVal(), false
	}
	if d.mask == 7 && d.indexShift == 0 {
		for i := range d.Entries {
			e := &d.Entries[i]
			if e.Active && (e.Key == key || dictKeyEqual(e.Key, key)) {
				return e.Value, true
			}
		}
		return NoneVal(), false
	}

	h := HashValue(key)
	mask := d.mask
	slot := h & mask

	if d.indexShift == 0 {
		for {
			raw := d.indices[slot]
			if raw == dictIndexEmpty {
				return NoneVal(), false
			}
			if raw >= dictIndexOffset {
				e := &d.Entries[int(raw)-dictIndexOffset]
				if e.Key == key || (e.Hash == h && e.Active && dictKeyEqual(e.Key, key)) {
					return e.Value, true
				}
			}
			slot = (slot + 1) & mask
		}
	}

	for {
		raw := d.readIndex(slot)
		if raw == dictIndexEmpty {
			return NoneVal(), false
		}
		if raw >= dictIndexOffset {
			e := &d.Entries[int(raw)-dictIndexOffset]
			if e.Key == key || (e.Hash == h && e.Active && dictKeyEqual(e.Key, key)) {
				return e.Value, true
			}
		}
		slot = (slot + 1) & mask
	}
}

// Delete removes key from PyDict if present, returning true if an entry was removed in a thread-safe manner.
func (d *PyDict) Delete(key Value) bool {
	if d.Live != nil {
		return d.Live.DeleteAttr(InternValue(key))
	}
	if d.lockDict() {
		defer d.mu.Unlock()
	}
	return d.deleteLocked(key)
}

func (d *PyDict) deleteLocked(key Value) bool {
	if len(d.Entries) == 0 {
		if d.Live != nil {
			return d.Live.DeleteAttr(InternValue(key))
		}
		if d.split == nil {
			return false
		}
		if d.splitLookup(key) < 0 {
			return false
		}
		d.promoteSplitToCombined()
	}

	h := HashValue(key)
	mask := d.mask
	slot := h & mask

	for {
		raw := d.readIndex(slot)
		if raw == dictIndexEmpty {
			return false
		}
		if raw >= dictIndexOffset {
			e := &d.Entries[int(raw)-dictIndexOffset]
			if e.Key == key || (e.Hash == h && e.Active && dictKeyEqual(e.Key, key)) {
				e.Active = false
				e.Key = NoneVal()
				e.Value = NoneVal()
				d.writeIndex(slot, dictIndexDummy)
				d.used--
				d.bumpVersionTag()
				if d.owner != nil {
					d.owner.bumpVersion()
				}
				return true
			}
		}
		slot = (slot + 1) & mask
	}
}

// Len returns the number of active entries in PyDict in a thread-safe manner.
func (d *PyDict) Len() int {
	if d.Live != nil {
		return d.liveLen()
	}
	if d.rlockDict() {
		defer d.mu.RUnlock()
	}
	return int(d.used)
}

// Keys returns a slice of all active keys in PyDict in a thread-safe manner.
func (d *PyDict) Keys() []Value {
	if d.Live != nil {
		items := d.liveItems()
		keys := make([]Value, len(items))
		for i := range items {
			keys[i] = items[i].Key
		}
		return keys
	}
	if d.rlockDict() {
		defer d.mu.RUnlock()
	}
	if sp := d.split; sp != nil {
		keys := make([]Value, sp.keys.Count)
		copy(keys, sp.keys.Keys[:sp.keys.Count])
		return keys
	}
	keys := make([]Value, 0, d.used)
	for i := range d.Entries {
		if d.Entries[i].Active {
			keys = append(keys, d.Entries[i].Key)
		}
	}
	return keys
}

// DictVal wraps an existing *PyDict as a Value.
func DictVal(d *PyDict) Value {
	if d == nil {
		return NoneVal()
	}
	return Value{num: 0, ptr: &d.Header}
}

// Values returns a slice of all active values in PyDict in a thread-safe manner.
func (d *PyDict) Values() []Value {
	if d == nil {
		return nil
	}
	if d.Live != nil {
		items := d.liveItems()
		vals := make([]Value, len(items))
		for i := range items {
			vals[i] = items[i].Value
		}
		return vals
	}
	if d.rlockDict() {
		defer d.mu.RUnlock()
	}
	if sp := d.split; sp != nil {
		vals := make([]Value, sp.keys.Count)
		copy(vals, sp.vals[:sp.keys.Count])
		return vals
	}
	vals := make([]Value, 0, d.used)
	for i := range d.Entries {
		if d.Entries[i].Active {
			vals = append(vals, d.Entries[i].Value)
		}
	}
	return vals
}

// DictItem represents a key-value pair in PyDict.
type DictItem struct {
	Key   Value
	Value Value
}

// Items returns a slice of all active key-value pairs in PyDict in a thread-safe manner.
func (d *PyDict) Items() []DictItem {
	if d == nil {
		return nil
	}
	if d.Live != nil {
		return d.liveItems()
	}
	if d.rlockDict() {
		defer d.mu.RUnlock()
	}
	if sp := d.split; sp != nil {
		items := make([]DictItem, sp.keys.Count)
		for i := range sp.keys.Count {
			items[i] = DictItem{Key: sp.keys.Keys[i], Value: sp.vals[i]}
		}
		return items
	}
	items := make([]DictItem, 0, d.used)
	for i := range d.Entries {
		if d.Entries[i].Active {
			items = append(items, DictItem{Key: d.Entries[i].Key, Value: d.Entries[i].Value})
		}
	}
	return items
}

// PySet represents a heap-allocated Python set backed by an open-addressing PyDict table.
type PySet struct {
	Header ObjectHeader
	dict   PyDict
}

// NewSetVal constructs a new PySet Value.
func NewSetVal(items ...Value) Value {
	s := &PySet{
		Header: ObjectHeader{
			Kind: KindSet,
			Type: SetType,
		},
	}
	s.dict.Header = ObjectHeader{Kind: KindDict, Type: DictType}
	if len(items) > 0 {
		s.dict.initTable(len(items))
		for _, item := range items {
			s.dict.Set(item, NoneVal())
		}
	}
	return Value{ptr: &s.Header}
}

// SetVal wraps an existing *PySet into a Value.
func SetVal(s *PySet) Value {
	if s == nil {
		return NoneVal()
	}
	return Value{ptr: &s.Header}
}

// Copy returns a shallow copy of d, using a zero-probe memcpy fast path for
// tombstone-free concrete dicts and a pre-sized PutNewHashed compaction path
// when tombstones are present.
func (d *PyDict) Copy() *PyDict {
	if d.Live == nil && !d.Shared && !MultiThreadedFlag.Load() {
		if len(d.Entries) == 0 {
			if sp := d.split; sp != nil {
				sk := sp.keys
				if !sk.DisableSplit.Load() {
					return NewSplitDict(sk, sp.vals, 0)
				}
				var newD *PyDict
				if sk.Count <= 5 {
					newD = NewSmallDictDirect(0)
				} else {
					newD = NewDictVal(sk.Count).AsDict()
				}
				for i := range sk.Count {
					newD.PutNewHashed(sk.Keys[i], sp.vals[i], sk.Hashes[i])
				}
				return newD
			}
			return &PyDict{Header: ObjectHeader{Kind: KindDict, Type: DictType}}
		}
		if d.used == 0 {
			return &PyDict{Header: ObjectHeader{Kind: KindDict, Type: DictType}}
		}
		if d.used == d.filled && len(d.Entries) == int(d.used) && len(d.indices) > 0 {
			if d.IsSmallInline() {
				newD := NewSmallDictDirect(0)
				copy(newD.Entries[:d.used], d.Entries[:d.used])
				newD.Entries = newD.Entries[:d.used]
				copy(newD.indices, d.indices)
				newD.used = d.used
				newD.filled = d.filled
				newD.mask = d.mask
				newD.indexShift = d.indexShift
				return newD
			}
			entries := make([]DictEntry, len(d.Entries), cap(d.Entries))
			copy(entries, d.Entries)
			indices := make([]byte, len(d.indices))
			copy(indices, d.indices)
			return &PyDict{
				Header:     ObjectHeader{Kind: KindDict, Type: DictType},
				Entries:    entries,
				indices:    indices,
				used:       d.used,
				filled:     d.filled,
				mask:       d.mask,
				indexShift: d.indexShift,
			}
		}
		var newD *PyDict
		if d.used <= 5 {
			newD = NewSmallDictDirect(0)
		} else {
			newD = NewDictVal(int(d.used)).AsDict()
		}
		for i := range d.Entries {
			e := &d.Entries[i]
			if e.Active {
				newD.PutNewHashed(e.Key, e.Value, e.Hash)
			}
		}
		return newD
	}
	newD := NewDictVal(d.Len()).AsDict()
	d.ForEachActive(func(k, v Value) bool {
		newD.Set(k, v)
		return true
	})
	return newD
}

// UpdateFromDict copies all active entries from other into d, using a pre-sized
// PutNewHashed fast path for concrete single-threaded non-live dicts.
func (d *PyDict) UpdateFromDict(other *PyDict) {
	if other == d {
		return
	}
	if d.Live == nil && other.Live == nil && !d.Shared && !other.Shared && !MultiThreadedFlag.Load() {
		if other.used == 0 {
			return
		}
		if d.split != nil {
			d.promoteSplitToCombined()
		}
		need := int(d.used) + int(other.used)
		if len(d.indices) == 0 {
			d.initTable(max(4, need))
		} else if d.used != d.filled || (int(d.filled)+int(other.used))*3 > int(d.mask+1)*2 || len(d.Entries)+int(other.used)+dictIndexOffset >= d.maxIndexValue() {
			// Resizing compacts away any tombstones so PutNewHashed is safe.
			d.resize(need)
		}
		if osp := other.split; osp != nil {
			sk := osp.keys
			for i := range sk.Count {
				d.PutNewHashed(sk.Keys[i], osp.vals[i], sk.Hashes[i])
			}
			return
		}
		for i := range other.Entries {
			e := &other.Entries[i]
			if e.Active {
				d.PutNewHashed(e.Key, e.Value, e.Hash)
			}
		}
		return
	}
	other.ForEachActive(func(k, v Value) bool {
		d.Set(k, v)
		return true
	})
}

// NewSetFromDict creates a new PySet containing the keys of d, using a pre-sized
// PutNewHashed fast path for concrete single-threaded non-live dicts.
func NewSetFromDict(d *PyDict) Value {
	if d.Live == nil && !d.Shared && !MultiThreadedFlag.Load() {
		s := &PySet{
			Header: ObjectHeader{Kind: KindSet, Type: SetType},
		}
		if d.used > 0 {
			s.dict.initTable(max(4, int(d.used)))
			if sp := d.split; sp != nil {
				sk := sp.keys
				for i := range sk.Count {
					s.dict.PutNewHashed(sk.Keys[i], NoneVal(), sk.Hashes[i])
				}
			} else {
				for i := range d.Entries {
					e := &d.Entries[i]
					if e.Active {
						s.dict.PutNewHashed(e.Key, NoneVal(), e.Hash)
					}
				}
			}
		}
		return Value{ptr: &s.Header}
	}
	return NewSetVal(d.Keys()...)
}

// Copy returns a shallow copy of s, using a zero-probe memcpy fast path when
// tombstone-free and a pre-sized PutNewHashed compaction path otherwise.
func (s *PySet) Copy() *PySet {
	d := &s.dict
	if d.Live == nil && !d.Shared && !MultiThreadedFlag.Load() {
		if d.used > 0 && d.used == d.filled && len(d.Entries) == int(d.used) && len(d.indices) > 0 {
			entries := make([]DictEntry, len(d.Entries), cap(d.Entries))
			copy(entries, d.Entries)
			indices := make([]byte, len(d.indices))
			copy(indices, d.indices)
			return &PySet{
				Header: ObjectHeader{Kind: KindSet, Type: SetType},
				dict: PyDict{
					Entries:    entries,
					indices:    indices,
					used:       d.used,
					filled:     d.filled,
					mask:       d.mask,
					indexShift: d.indexShift,
				},
			}
		}
		out := &PySet{
			Header: ObjectHeader{Kind: KindSet, Type: SetType},
		}
		if d.used > 0 {
			out.dict.initTable(max(4, int(d.used)))
			for i := range d.Entries {
				e := &d.Entries[i]
				if e.Active {
					out.dict.PutNewHashed(e.Key, NoneVal(), e.Hash)
				}
			}
		}
		return out
	}
	return NewSetVal(s.Items()...).AsSet()
}

// Add adds an element to the set.
func (s *PySet) Add(v Value) {
	s.dict.Set(v, NoneVal())
}

// Reserve pre-sizes an empty PySet to hold at least n elements in one allocation.
func (s *PySet) Reserve(n int) {
	s.dict.Reserve(n)
}

// DictEntries returns the underlying dict entry slice for direct iteration.
func (s *PySet) DictEntries() []DictEntry {
	return s.dict.Entries
}

// IsUnhashableValue reports whether v contains an unhashable type name.
func IsUnhashableValue(v Value) (string, bool) {
	return isUnhashableValue(v)
}

func isUnhashableValue(v Value) (string, bool) {
	if v.ptr == nil || v.ptr == &tagInt64 || v.ptr == &tagBool || v.ptr == &tagFloat64 ||
		v.ptr == &tagInlineStr || v.ptr == &tagNone {
		return "", false
	}
	switch v.ptr.Kind {
	case KindString, KindBigInt, KindComplex, KindBytes, KindRange:
		return "", false
	case KindList, KindDict, KindSet, KindByteArray:
		tp := v.Type()
		if tp != nil {
			return tp.Name, true
		}
		return v.Kind().String(), true
	case KindTuple:
		tp := v.ptr.Type
		if tp == nil || tp == TupleType || tp.dunderHash.ptr == nil {
			for _, it := range v.AsTuple().Items {
				if name, bad := isUnhashableValue(it); bad {
					return name, true
				}
			}
			return "", false
		}
	}
	if tp := v.Type(); tp != nil && tp.dunderHash.ptr != nil && tp.dunderHash.IsNone() {
		return tp.Name, true
	}
	return "", false
}

// AddChecked adds v to the set, returning an error exception Value if v is unhashable.
func (s *PySet) AddChecked(v Value) (Value, bool) {
	if badType, bad := isUnhashableValue(v); bad {
		return NewTypeError("unhashable type: '" + badType + "'"), false
	}
	s.dict.Set(v, NoneVal())
	return Value{}, true
}

// Contains reports whether v is an element of the set.
func (s *PySet) Contains(v Value) bool {
	_, ok := s.dict.Get(v)
	return ok
}

// Len returns the count of elements in the set.
func (s *PySet) Len() int {
	return s.dict.Len()
}

// Items returns a slice of all elements in the set.
func (s *PySet) Items() []Value {
	return s.dict.Keys()
}

// Union returns a new set containing all elements from both s and other.
func (s *PySet) Union(other *PySet) Value {
	res := NewSetVal().AsSet()
	for _, it := range s.Items() {
		res.Add(it)
	}
	if other != nil {
		for _, it := range other.Items() {
			res.Add(it)
		}
	}
	return Value{ptr: &res.Header}
}

// Intersection returns a new set containing elements present in both s and other.
func (s *PySet) Intersection(other *PySet) Value {
	res := NewSetVal().AsSet()
	if other != nil {
		for _, it := range s.Items() {
			if other.Contains(it) {
				res.Add(it)
			}
		}
	}
	return Value{ptr: &res.Header}
}

// SymmetricDifference returns a new set with elements in either s or other, but not both.
func (s *PySet) SymmetricDifference(other *PySet) Value {
	res := NewSetVal().AsSet()
	for _, it := range s.Items() {
		if other == nil || !other.Contains(it) {
			res.Add(it)
		}
	}
	if other != nil {
		for _, it := range other.Items() {
			if !s.Contains(it) {
				res.Add(it)
			}
		}
	}
	return Value{ptr: &res.Header}
}

// Remove removes v from the set. Returns true if present.
func (s *PySet) Remove(v Value) bool {
	return s.dict.Delete(v)
}

// Discard removes v from the set if present.
func (s *PySet) Discard(v Value) {
	s.dict.Delete(v)
}

// Clear removes all elements from the set.
func (s *PySet) Clear() {
	s.dict.Clear()
}

// Pop removes and returns an arbitrary element from the set.
func (s *PySet) Pop() (Value, bool) {
	keys := s.dict.Keys()
	if len(keys) == 0 {
		return NoneVal(), false
	}
	k := keys[0]
	s.dict.Delete(k)
	return k, true
}

// Difference returns a new set with elements in s that are not in other.
func (s *PySet) Difference(other *PySet) Value {
	res := NewSetVal().AsSet()
	for _, it := range s.Items() {
		if other == nil || !other.Contains(it) {
			res.Add(it)
		}
	}
	return Value{ptr: &res.Header}
}

// IsSubset reports whether every element in s is in other.
func (s *PySet) IsSubset(other *PySet) bool {
	if other == nil {
		return s.Len() == 0
	}
	for _, k := range s.Items() {
		if !other.Contains(k) {
			return false
		}
	}
	return true
}

// IsSuperset reports whether every element in other is in s.
func (s *PySet) IsSuperset(other *PySet) bool {
	if other == nil {
		return true
	}
	return other.IsSubset(s)
}

// PyExceptionGroup represents a PEP 654 ExceptionGroup / BaseExceptionGroup.
type PyExceptionGroup struct {
	Header     ObjectHeader
	Message    string
	Exceptions []Value
}

// NewExceptionGroupVal constructs a new PyExceptionGroup Value.
func NewExceptionGroupVal(cls *PyType, msg string, excs []Value) Value {
	if cls == nil {
		cls = ExceptionGroupType
	}
	eg := &PyExceptionGroup{
		Header: ObjectHeader{
			Kind: KindExceptionGroup,
			Type: cls,
		},
		Message:    msg,
		Exceptions: excs,
	}
	return Value{ptr: &eg.Header}
}

// Split partitions the exception group according to matching condition against targetType.
// Returns (matchingGroup, restGroup).
func (eg *PyExceptionGroup) Split(targetType *PyType) (Value, Value) {
	var matches []Value
	var rest []Value
	for _, exc := range eg.Exceptions {
		matched := false
		if exc.IsType() {
			matched = IsSubclass(exc.AsType(), targetType)
		} else if exc.ptr != nil && exc.Type() != nil {
			matched = IsSubclass(exc.Type(), targetType)
		}
		if matched {
			matches = append(matches, exc)
		} else {
			rest = append(rest, exc)
		}
	}
	var matchVal Value
	if len(matches) > 0 {
		matchVal = NewExceptionGroupVal(eg.Header.Type, eg.Message, matches)
	} else {
		matchVal = NoneVal()
	}
	var restVal Value
	if len(rest) > 0 {
		restVal = NewExceptionGroupVal(eg.Header.Type, eg.Message, rest)
	} else {
		restVal = NoneVal()
	}
	return matchVal, restVal
}

// PyTypeVar represents a PEP 484 / PEP 695 type variable.
type PyTypeVar struct {
	Header              ObjectHeader
	Name                string
	Bound               Value
	Constraints         []Value
	Default             Value
	Covariant           bool
	Contravariant       bool
	InferVariance       bool
	Module              string
	EvaluateBound       Value
	EvaluateConstraints Value
	EvaluateDefault     Value
}

func NewTypeVarVal(name string, bound Value) Value {
	tv := &PyTypeVar{
		Header: ObjectHeader{
			Kind: KindNative,
			Type: TypeVarType,
		},
		Name:  name,
		Bound: bound,
	}
	return Value{ptr: &tv.Header}
}

// PyTypeVarTuple represents a PEP 646 TypeVarTuple.
type PyTypeVarTuple struct {
	Header  ObjectHeader
	Name    string
	Default Value
	Module  string
}

func NewTypeVarTupleVal(name string) Value {
	tvt := &PyTypeVarTuple{
		Header: ObjectHeader{
			Kind: KindNative,
			Type: TypeVarTupleType,
		},
		Name: name,
	}
	return Value{ptr: &tvt.Header}
}

// PyParamSpec represents a PEP 612 ParamSpec.
type PyParamSpec struct {
	Header        ObjectHeader
	Name          string
	Bound         Value
	Default       Value
	Covariant     bool
	Contravariant bool
	InferVariance bool
	Module        string
	Args          Value
	Kwargs        Value
}

func NewParamSpecVal(name string) Value {
	ps := &PyParamSpec{
		Header: ObjectHeader{
			Kind: KindNative,
			Type: ParamSpecType,
		},
		Name: name,
	}
	return Value{ptr: &ps.Header}
}

// PyTypeAliasType represents a PEP 695 TypeAliasType.
type PyTypeAliasType struct {
	Header     ObjectHeader
	Name       string
	Value      Value
	TypeParams []Value
}

func NewTypeAliasTypeVal(name string, val Value, typeParams []Value) Value {
	tat := &PyTypeAliasType{
		Header: ObjectHeader{
			Kind: KindNative,
			Type: TypeAliasTypeType,
		},
		Name:       name,
		Value:      val,
		TypeParams: typeParams,
	}
	return Value{ptr: &tat.Header}
}

// FormatValueBytes formats a Value according to format specifier bytes without
// forcing a stack-allocated spec slice to escape to the heap.
func FormatValueBytes(v Value, spec []byte, conversion uint8) Value {
	return FormatValueBytesCtx(nil, v, spec, conversion)
}

func FormatValueBytesCtx(ctx *ExecContext, v Value, spec []byte, conversion uint8) Value {
	if conversion == 's' || conversion == 'r' || conversion == 'a' {
		v = NewStringVal(v.String())
	}
	if len(spec) == 0 {
		if v.IsAnyString() {
			return v
		}
		if v.IsInt64() {
			return IntToStringVal(v.AsInt64())
		}
		if v.IsFloat64() {
			var buf [48]byte
			return NewStringFromBytes(AppendFloatRepr(buf[:0], v.AsFloat64()))
		}
		if v.IsBool() {
			if v.IsTruthy() {
				return NewStringVal("True")
			}
			return NewStringVal("False")
		}
		if v.IsNone() {
			return NewStringVal("None")
		}
		return NewStringVal(v.String())
	}

	switch {
	case v.IsInt64():
		return FormatInt64SpecBytesCtx(ctx, v.AsInt64(), spec)
	case v.Kind() == KindBigInt:
		return NewStringVal(FormatIntSpecCtx(ctx, &v.AsBigInt().Val, string(spec)))
	case v.IsBool():
		return NewStringVal(FormatBoolSpecCtx(ctx, v.AsBool(), string(spec)))
	case v.IsFloat64():
		return FormatFloatSpecBytesCtx(ctx, v.AsFloat64(), spec)
	case v.IsAnyString():
		var vbuf [7]byte
		if vb := v.AsBytesScratch(&vbuf); vb != nil {
			return FormatStrSpecBytesCtx(ctx, string(vb), spec)
		}
		return FormatStrSpecBytesCtx(ctx, v.AsString(), spec)
	}
	return RaiseTypeError(ctx, "unsupported format string passed to "+v.Type().Name+".__format__")
}

// FormatValue formats a Value according to format specifier spec and optional conversion ('s', 'r', 'a').
// Zero-allocation fast path for Int64, Float64, and strings.
func FormatValue(v Value, spec string, conversion uint8) Value {
	return FormatValueCtx(nil, v, spec, conversion)
}

func FormatValueCtx(ctx *ExecContext, v Value, spec string, conversion uint8) Value {
	if conversion == 's' || conversion == 'r' || conversion == 'a' {
		v = NewStringVal(v.String())
	}
	if spec == "" {
		if v.IsAnyString() {
			return v
		}
		if v.IsInt64() {
			return IntToStringVal(v.AsInt64())
		}
		if v.IsFloat64() {
			return NewStringVal(FloatRepr(v.AsFloat64()))
		}
		if v.IsBool() {
			if v.IsTruthy() {
				return NewStringVal("True")
			}
			return NewStringVal("False")
		}
		if v.IsNone() {
			return NewStringVal("None")
		}
		return NewStringVal(v.String())
	}

	switch {
	case v.IsInt64():
		return NewStringVal(FormatInt64SpecCtx(ctx, v.AsInt64(), spec))
	case v.Kind() == KindBigInt:
		return NewStringVal(FormatIntSpecCtx(ctx, &v.AsBigInt().Val, spec))
	case v.IsBool():
		return NewStringVal(FormatBoolSpecCtx(ctx, v.AsBool(), spec))
	case v.IsFloat64():
		return NewStringVal(FormatFloatSpecCtx(ctx, v.AsFloat64(), spec))
	case v.IsAnyString():
		var vbuf [7]byte
		if vb := v.AsBytesScratch(&vbuf); vb != nil {
			return NewStringVal(FormatStrSpecCtx(ctx, unsafe.String(unsafe.SliceData(vb), len(vb)), spec))
		}
		return NewStringVal(FormatStrSpecCtx(ctx, v.AsString(), spec))
	}
	return RaiseTypeError(ctx, "unsupported format string passed to "+v.Type().Name+".__format__")
}

// FrameVM abstracts VM execution stack access for PyFrame and PyLocalsProxy.
type FrameVM interface {
	GetStackVal(offset int) Value
	SetStackVal(offset int, v Value)
	MaterializeFrame(frameIdx int) Value
}

// CodeInspector abstracts inspection of KindCode objects across packages.
type CodeInspector interface {
	VarNames(code Value) []Value
	CellVars(code Value) []Value
	FreeVars(code Value) []Value
	DirectCaptureBase(code Value) int
	DirectCaptureCount(code Value) int
	Name(code Value) string
	QualName(code Value) string
	FileName(code Value) string
	FirstLineNo(code Value) int
	ArgCount(code Value) int
	PosOnlyArgCount(code Value) int
	KWOnlyArgCount(code Value) int
	Flags(code Value) uint32
	LineAt(code Value, ip int) int
	LineTable(code Value) []byte
	LineSpans(code Value) []any
	CodeBytes(code Value) []byte
	Consts(code Value) []Value
	StackSize(code Value) int
	Doc(code Value) Value
}

// CodeInspectorHook is configured by pkg/vm to resolve CodeObject metadata from KindCode Values.
var CodeInspectorHook CodeInspector

// PyFrame represents a lazily-materialized Python execution frame on the heap.
// Header MUST be at byte offset 0.
type PyFrame struct {
	Header      ObjectHeader
	VM          any     // *vm.VM implementing FrameVM
	FrameIdx    int     // index on contiguous vm.frames slab if active
	CO          Value   // KindCode wrapping *vm.CodeObject
	BaseOffset  int     // stack slot offset
	IP          int     // instruction pointer
	Globals     *PyDict // frame globals dictionary
	Cells       []Value // closure cells
	Back        Value   // caller PyFrame (or None)
	LocalsProxy Value   // cached FrameLocalsProxy (or None)
	Detached    bool    // true if function has returned
	SavedLocals *PyDict // locals snapshot after return
	ExtraLocals *PyDict // variables dynamically added via f_locals
}

// PyLocalsProxy represents a live PEP 667 mapping view over vm.stack and closure cells.
// Header MUST be at byte offset 0.
type PyLocalsProxy struct {
	Header ObjectHeader
	Frame  *PyFrame
}

// PyTraceback represents a node in an exception traceback chain.
// Header MUST be at byte offset 0.
type PyTraceback struct {
	Header ObjectHeader
	Frame  Value // KindFrame
	LineNo int
	LastI  int
	Next   Value // KindTraceback or None
}

// PyWeakRef represents a weak reference to an object.
// Header MUST be at byte offset 0.
type PyWeakRef struct {
	Header   ObjectHeader
	WP       weak.Pointer[ObjectHeader]
	Callback Value
	Hash     int64
	HasHash  bool
	IsDead   atomic.Bool
}

// PyWeakProxy represents a transparent proxy forwarding to a weakly-referenced object.
// Header MUST be at byte offset 0.
type PyWeakProxy struct {
	Header     ObjectHeader
	WP         weak.Pointer[ObjectHeader]
	Callback   Value
	IsCallable bool
}

// Compile-time assertions that all new heap structs place Header at byte offset 0.
var (
	_ [0 - unsafe.Offsetof(PyFrame{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyLocalsProxy{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyTraceback{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyWeakRef{}.Header)]byte
	_ [0 - unsafe.Offsetof(PyWeakProxy{}.Header)]byte
)

// Downcasters from Value:

func (v Value) AsFrame() *PyFrame {
	return (*PyFrame)(unsafe.Pointer(v.ptr))
}

func (v Value) AsLocalsProxy() *PyLocalsProxy {
	return (*PyLocalsProxy)(unsafe.Pointer(v.ptr))
}

func (v Value) AsTraceback() *PyTraceback {
	return (*PyTraceback)(unsafe.Pointer(v.ptr))
}

func (v Value) AsWeakRef() *PyWeakRef {
	return (*PyWeakRef)(unsafe.Pointer(v.ptr))
}

func (v Value) AsWeakProxy() *PyWeakProxy {
	return (*PyWeakProxy)(unsafe.Pointer(v.ptr))
}

// Detach snapshots the active stack locals into SavedLocals and marks the frame as detached.
func (f *PyFrame) Detach() {
	if f.Detached {
		return
	}
	f.Detached = true
	if f.Back.IsNone() && f.VM != nil && f.FrameIdx > 0 {
		if vm, ok := f.VM.(FrameVM); ok {
			f.Back = vm.MaterializeFrame(f.FrameIdx - 1)
		}
	}
	snapshot := NewDictVal(16).AsDict()
	if CodeInspectorHook != nil && f.CO.ptr != nil {
		varnames := CodeInspectorHook.VarNames(f.CO)
		if vm, ok := f.VM.(FrameVM); ok {
			for i, name := range varnames {
				v := vm.GetStackVal(f.BaseOffset + i)
				if !v.IsNone() || v.ptr != nil {
					snapshot.Set(name, v)
				}
			}
		}
		cellvars := CodeInspectorHook.CellVars(f.CO)
		for i, name := range cellvars {
			if i < len(f.Cells) && f.Cells[i].IsCell() {
				snapshot.Set(name, f.Cells[i].AsCell().Value)
			}
		}
		freevars := CodeInspectorHook.FreeVars(f.CO)
		dcCount := CodeInspectorHook.DirectCaptureCount(f.CO)
		dcBase := CodeInspectorHook.DirectCaptureBase(f.CO)
		for j, name := range freevars {
			if j < dcCount {
				if vm, ok := f.VM.(FrameVM); ok {
					snapshot.Set(name, vm.GetStackVal(f.BaseOffset+dcBase+j))
					continue
				}
			}
			cellIdx := len(cellvars) + j
			if cellIdx < len(f.Cells) && f.Cells[cellIdx].IsCell() {
				snapshot.Set(name, f.Cells[cellIdx].AsCell().Value)
			}
		}
	}
	if f.ExtraLocals != nil {
		for _, e := range f.ExtraLocals.Entries {
			if !e.Key.IsUnbound() {
				snapshot.Set(e.Key, e.Value)
			}
		}
	}
	f.SavedLocals = snapshot
}

// NewPyFrame constructs an active heap PyFrame wrapping a CallFrame slot.
func NewPyFrame(vm FrameVM, frameIdx int, baseOffset int, co Value, globals *PyDict, cells []Value) *PyFrame {
	f := &PyFrame{
		Header: ObjectHeader{
			Kind: KindFrame,
			Type: FrameType,
		},
		VM:          vm,
		FrameIdx:    frameIdx,
		CO:          co,
		BaseOffset:  baseOffset,
		Globals:     globals,
		Cells:       cells,
		Back:        NoneVal(),
		LocalsProxy: NoneVal(),
	}
	return f
}

// NewFrameVal wraps a *PyFrame as a Value with KindFrame.
func NewFrameVal(f *PyFrame) Value {
	f.Header.Kind = KindFrame
	f.Header.Type = FrameType
	return Value{ptr: &f.Header}
}

// NewLocalsProxyVal wraps a *PyLocalsProxy as a Value with KindLocalsProxy.
func NewLocalsProxyVal(p *PyLocalsProxy) Value {
	p.Header.Kind = KindLocalsProxy
	p.Header.Type = LocalsProxyType
	return Value{ptr: &p.Header}
}

// NewTracebackVal constructs a new PyTraceback Value.
func NewTracebackVal(frame Value, lineno int, lasti int, next Value) Value {
	tb := &PyTraceback{
		Header: ObjectHeader{
			Kind: KindTraceback,
			Type: TracebackType,
		},
		Frame:  frame,
		LineNo: lineno,
		LastI:  lasti,
		Next:   next,
	}
	return Value{ptr: &tb.Header}
}

// NewWeakRefVal wraps a *PyWeakRef as a Value with KindWeakRef.
func NewWeakRefVal(ref *PyWeakRef) Value {
	ref.Header.Kind = KindWeakRef
	ref.Header.Type = WeakRefType
	return Value{ptr: &ref.Header}
}

// NewWeakProxyVal wraps a *PyWeakProxy as a Value with KindWeakProxy.
func NewWeakProxyVal(proxy *PyWeakProxy) Value {
	proxy.Header.Kind = KindWeakProxy
	if proxy.IsCallable {
		proxy.Header.Type = CallableWeakProxyType
	} else {
		proxy.Header.Type = WeakProxyType
	}
	return Value{ptr: &proxy.Header}
}

// PyMappingProxy represents a read-only mappingproxy view of a dictionary or mapping.
type PyMappingProxy struct {
	Header  ObjectHeader
	Mapping Value
}

// NewMappingProxyVal wraps a mapping Value in a read-only PyMappingProxy.
func NewMappingProxyVal(mapping Value) Value {
	proxy := &PyMappingProxy{
		Header: ObjectHeader{
			Kind: KindMappingProxy,
			Type: MappingProxyType,
		},
		Mapping: mapping,
	}
	return Value{ptr: &proxy.Header}
}

// AsMappingProxy performs a downcast from Value to *PyMappingProxy.
func (v Value) AsMappingProxy() *PyMappingProxy {
	return (*PyMappingProxy)(unsafe.Pointer(v.ptr))
}

// IsMappingProxy returns true if v is a KindMappingProxy.
func (v Value) IsMappingProxy() bool {
	return v.Kind() == KindMappingProxy
}

// NewInstanceDictProxy returns a dict Value that is a live, zero-copy view of
// inst's Shape-backed attribute storage. The proxy is cached lazily on inst so
// `obj.__dict__ is obj.__dict__` holds and repeated `vars(obj)` calls do not
// allocate. Writes go through the same path as setattr/delattr, so the instance
// is never deoptimized into a hash table.
func NewInstanceDictProxy(inst *PyInstance) Value {
	if !MultiThreadedFlag.Load() {
		if inst.dictView == nil {
			inst.dictView = &PyDict{
				Header: ObjectHeader{Kind: KindDict, Type: DictType},
				Live:   inst,
			}
		}
		return Value{num: 0, ptr: &inst.dictView.Header}
	}
	mu := inst.lock()
	mu.Lock()
	d := inst.dictView
	if d == nil {
		d = &PyDict{
			Header: ObjectHeader{Kind: KindDict, Type: DictType},
			Live:   inst,
		}
		inst.dictView = d
	}
	mu.Unlock()
	return Value{num: 0, ptr: &d.Header}
}

func (d *PyDict) liveGetWithSlot(key Value) (Value, int, bool) {
	inst := d.Live
	if MultiThreadedFlag.Load() {
		mu := inst.lock()
		mu.RLock()
		defer mu.RUnlock()
	}
	idx, ok := inst.Shape.Lookup(key)
	if !ok || idx >= len(inst.Slots) || inst.Slots[idx].IsUnbound() {
		return NoneVal(), -1, false
	}
	return inst.Slots[idx], idx, true
}

func (d *PyDict) liveLen() int {
	inst := d.Live
	if MultiThreadedFlag.Load() {
		mu := inst.lock()
		mu.RLock()
		defer mu.RUnlock()
	}
	n := 0
	for _, a := range inst.Shape.Attrs {
		if a.Index < len(inst.Slots) && !inst.Slots[a.Index].IsUnbound() {
			n++
		}
	}
	return n
}

func (d *PyDict) liveItems() []DictItem {
	inst := d.Live
	if MultiThreadedFlag.Load() {
		mu := inst.lock()
		mu.RLock()
		defer mu.RUnlock()
	}
	items := make([]DictItem, 0, len(inst.Shape.Attrs))
	for _, a := range inst.Shape.Attrs {
		if a.Index < len(inst.Slots) && !inst.Slots[a.Index].IsUnbound() {
			items = append(items, DictItem{Key: a.Name, Value: inst.Slots[a.Index]})
		}
	}
	return items
}

func (d *PyDict) liveIterNext(it *PyIter) (Value, bool) {
	inst := d.Live
	if MultiThreadedFlag.Load() {
		mu := inst.lock()
		mu.RLock()
		defer mu.RUnlock()
	}
	attrs := inst.Shape.Attrs
	for it.Idx < len(attrs) {
		a := attrs[it.Idx]
		it.Idx++
		if a.Index < len(inst.Slots) && !inst.Slots[a.Index].IsUnbound() {
			return a.Name, true
		}
	}
	return NoneVal(), false
}

// ActiveEntries returns a snapshot of all live entries in insertion order.
// It is the supported way to enumerate a dict from outside the package because
// it also works for instance-backed live dicts.
func (d *PyDict) ActiveEntries() []DictEntry {
	if d == nil {
		return nil
	}
	if d.Live != nil {
		items := d.liveItems()
		out := make([]DictEntry, len(items))
		for i, it := range items {
			out[i] = DictEntry{Hash: HashValue(it.Key), Active: true, Key: it.Key, Value: it.Value}
		}
		return out
	}
	if d.rlockDict() {
		defer d.mu.RUnlock()
	}
	if sp := d.split; sp != nil {
		sk := sp.keys
		out := make([]DictEntry, sk.Count)
		for i := range sk.Count {
			out[i] = DictEntry{Hash: sk.Hashes[i], Active: true, Key: sk.Keys[i], Value: sp.vals[i]}
		}
		return out
	}
	out := make([]DictEntry, 0, d.used)
	for i := range d.Entries {
		if d.Entries[i].Active {
			out = append(out, d.Entries[i])
		}
	}
	return out
}

// ForEachActive invokes fn for each live entry in insertion order without
// allocating a []DictEntry snapshot on unshared single-threaded dicts.
// No lock is held while fn executes so callers may safely mutate d (e.g. d.update(d)).
func (d *PyDict) ForEachActive(fn func(key, value Value) bool) {
	if d == nil {
		return
	}
	if d.Live != nil {
		for _, it := range d.liveItems() {
			if !fn(it.Key, it.Value) {
				return
			}
		}
		return
	}
	if d.Shared || MultiThreadedFlag.Load() {
		for _, e := range d.ActiveEntries() {
			if !fn(e.Key, e.Value) {
				return
			}
		}
		return
	}
	if sp := d.split; sp != nil {
		sk := sp.keys
		for i := range sk.Count {
			if !fn(sk.Keys[i], sp.vals[i]) {
				return
			}
		}
		return
	}
	for i := range len(d.Entries) {
		e := d.Entries[i]
		if e.Active && !fn(e.Key, e.Value) {
			return
		}
	}
}

// ForEachActiveErr invokes fn for each live entry in insertion order without
// allocating a []DictEntry snapshot on unshared single-threaded dicts.
// Unlike ForEachActive, no lock is held while fn executes.
func (d *PyDict) ForEachActiveErr(fn func(key, value Value) error) error {
	if d == nil {
		return nil
	}
	if d.Live != nil {
		for _, it := range d.liveItems() {
			if err := fn(it.Key, it.Value); err != nil {
				return err
			}
		}
		return nil
	}
	if d.Shared || MultiThreadedFlag.Load() {
		for _, e := range d.ActiveEntries() {
			if err := fn(e.Key, e.Value); err != nil {
				return err
			}
		}
		return nil
	}
	if sp := d.split; sp != nil {
		sk := sp.keys
		for i := range sk.Count {
			if err := fn(sk.Keys[i], sp.vals[i]); err != nil {
				return err
			}
		}
		return nil
	}
	for i := 0; ; i++ {
		if i >= len(d.Entries) {
			break
		}
		e := d.Entries[i]
		if e.Active {
			if err := fn(e.Key, e.Value); err != nil {
				return err
			}
		}
	}
	return nil
}

// Indices exposes the sparse index table for pre-packed template copies.
func (d *PyDict) Indices() []byte {
	return d.indices
}

// SetUsedFilled sets the dense/sparse occupancy counters for template fills.
func (d *PyDict) SetUsedFilled(used, filled int) {
	d.used = int32(used)
	d.filled = int32(filled)
}

// BumpVersion bumps the dict version tag for template fills.
func (d *PyDict) BumpVersion() {
	d.bumpVersionTag()
	if d.owner != nil {
		d.owner.bumpVersion()
	}
}

// Clear removes every entry, preserving the live-instance mode if set.
func (d *PyDict) Clear() {
	if d.Live != nil {
		for _, it := range d.liveItems() {
			d.Live.DeleteAttr(it.Key)
		}
		return
	}
	if d.lockDict() {
		defer d.mu.Unlock()
	}
	if d.split != nil {
		clear(d.split.vals)
		d.split = nil
	}
	clear(d.Entries)
	d.Entries = d.Entries[:0]
	clear(d.indices)
	d.used = 0
	d.filled = 0
	d.bumpVersionTag()
	if d.owner != nil {
		d.owner.bumpVersion()
	}
}

// PopItem removes and returns the last (key, value) pair in LIFO order.
// Returns (key, value, true) if popped, or (None, None, false) if empty.
func (d *PyDict) PopItem() (Value, Value, bool) {
	if d == nil {
		return NoneVal(), NoneVal(), false
	}
	if d.Live != nil {
		items := d.liveItems()
		if len(items) == 0 {
			return NoneVal(), NoneVal(), false
		}
		last := items[len(items)-1]
		d.Live.DeleteAttr(last.Key)
		return last.Key, last.Value, true
	}
	if d.lockDict() {
		defer d.mu.Unlock()
	}
	if d.split != nil {
		d.promoteSplitToCombined()
	}
	for i := len(d.Entries) - 1; i >= 0; i-- {
		e := &d.Entries[i]
		if e.Active {
			k := e.Key
			v := e.Value
			d.deleteLocked(k)
			return k, v, true
		}
	}
	return NoneVal(), NoneVal(), false
}

// PyLazyImport represents a PEP 690 lazy import placeholder.
type PyLazyImport struct {
	Header  ObjectHeader
	Module  string
	Symbol  string
	Level   int
	Package string
}

func NewLazyImportVal(module, symbol string, level int, pkg string) Value {
	li := &PyLazyImport{
		Header:  ObjectHeader{Kind: KindLazyImport, Type: ObjectType},
		Module:  module,
		Symbol:  symbol,
		Level:   level,
		Package: pkg,
	}
	return Value{num: 0, ptr: &li.Header}
}

func (v Value) AsLazyImport() *PyLazyImport {
	return (*PyLazyImport)(unsafe.Pointer(v.ptr))
}

func (v Value) IsLazyImport() bool {
	return v.Kind() == KindLazyImport
}

// StringValRunes returns a []rune view of a string Value, reusing
// PyString.RuneCache when v is a heap PyString.
func StringValRunes(v Value) []rune {
	if v.ptr != nil && v.ptr.Kind == KindString {
		return (*PyString)(unsafe.Pointer(v.ptr)).Runes()
	}
	return []rune(v.AsString())
}

// Runes returns a cached []rune view of ps.Data, computing it at most once.
func (ps *PyString) Runes() []rune {
	if p := ps.RuneCache.Load(); p != nil {
		return *p
	}
	rs := []rune(ps.Data)
	ps.RuneCache.Store(&rs)
	return rs
}

// Runes returns a cached []rune view of pb.Data, computing it at most once.
func (pb *PyBytes) Runes() []rune {
	if p := pb.RuneCache.Load(); p != nil {
		return *p
	}
	rs := make([]rune, len(pb.Data))
	for i, b := range pb.Data {
		rs[i] = rune(b)
	}
	pb.RuneCache.Store(&rs)
	return rs
}

// IsASCII reports whether ps.Data contains only ASCII runes, caching the
// result atomically in the tail padding after BufClaimed (O(1) after first check).
func (ps *PyString) IsASCII() bool {
	if s := ps.asciiState.Load(); s != 0 {
		return s == 1
	}
	for i := range len(ps.Data) {
		if ps.Data[i] >= 0x80 {
			ps.asciiState.Store(2)
			return false
		}
	}
	ps.asciiState.Store(1)
	return true
}
