package vm

import (
	"encoding/binary"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

const (
	// DefaultStackSize is the default pre-allocated Value slab capacity per VM instance.
	DefaultStackSize = 4096
	// DefaultFrameSlabSize is the default pre-allocated CallFrame slab capacity per VM instance.
	DefaultFrameSlabSize = 64
	// DefaultICSlabSize is the initial Inline Cache side-table capacity per
	// VM instance. The slabs grow on demand, so a small initial size keeps
	// the resident set of the interpreter low.
	DefaultICSlabSize = 4096
)

// ICKind identifies the cached resolution category in an ICSlot.
type ICKind uint8

const (
	ICKindNone               ICKind = iota
	ICKindInstanceSlot              // Hit in PyInstance.Slots[SlotIndex]
	ICKindClassMethod               // Hit on PyType MRO method (CachedVal holds raw callable)
	ICKindDataDescriptor            // Hit on PyType MRO data descriptor (CachedVal holds descriptor)
	ICKindProperty                  // Hit on a @property whose fget is cached for in-loop dispatch
	ICKindClassAttr                 // Hit on PyType MRO non-descriptor class attribute (CachedVal holds the value)
	ICKindGlobal                    // Hit on module globals dict
	ICKindBuiltin                   // Hit on builtins dict fallback
	ICKindModuleAttr                // Hit on module dict
	ICKindSlotMember                // __slots__ member descriptor: direct read/write of Slots[SlotIndex]
	ICKindNativeDescr               // Native data descriptor with a Go getter/setter (CachedVal holds descriptor)
	ICKindClassFunc                 // Plain function found on the class: bind to the receiver without an MRO walk
	ICKindTupleGetter               // namedtuple field: read Items[SlotIndex] of a tuple-kind receiver
	ICKindTypeAttr                  // Receiver is a type; CachedVal holds the already-resolved class attribute
	ICKindTypeMethod                // Receiver is a type; CachedVal/CalleeCache/NativeFn holds resolved method call target
	ICKindSuperMethod               // super(ThisClass, Self) MRO resolution guarded by selfType + ThisClass versions
	ICKindPropertySet               // @property setter (CachedVal holds fset) dispatched without descriptor machinery
	ICKindInstanceTransition        // Shape-transitioning attribute store (OldShape -> NewShape via transitionIC)
	ICKindCompareDunder             // Quickened comparison dunder (CachedVal=*PyFunction, CalleeCache=*VMCodeCache, ExpectedTypeVer=left, ExpectedAuxVer=right, SlotIndex=mode)
	ICKindMethodDescr               // Monomorphic native method descriptor (CachedVal=BuiltinFunc, NativeFn=fn, ExpectedTypeVer=type ver, ExpectedShapeID=shape ID or 0)
	ICKindGetItemDunder             // Custom __getitem__ on PyInstance (CachedVal=fn Value, CalleeCache=CC, ExpectedTypeVer=type ver, ExpectedShapeID=shape ID)
	ICKindInstanceCall              // Callable PyInstance.__call__ (CachedVal=fn Value, CalleeCache=CC, ExpectedTypeVer=type ver, ExpectedShapeID=shape ID, SlotIndex=argc+1)
	ICKindIterDunder                // Custom __iter__ on PyInstance (CachedVal=fn Value, CalleeCache=CC, ExpectedTypeVer=type ver, ExpectedShapeID=shape ID, SlotIndex=gen flag)
	ICKindIsinstanceTuple           // isinstance(x, (T1, ...)) (CachedVal=tuple Value, ExpectedTypeVer=lhs ver, ExpectedShapeID=shape ID or 0, SlotIndex=bool result)
	ICKindConstKeyMap               // Pre-hashed const-key dict template (template lives in constKeyMaps[ip])
	ICKindCoroClosure               // Quickened coroutine closure site (CalleeCache=CC, SlotIndex: 1=direct non-suspending, 2=direct suspending, 3=non-direct)
)

const (
	attrQueryNone uint8 = iota
	attrQueryMiss
	attrQuerySlot
	attrQueryClassAttr
	attrQueryClassFunc
)

// attrQueryMissingSlot marks a confirmed-absent attribute in AttrQueryPICEntry.
// Missing on the type plus missing in the shape with no custom lookup hooks
// guarantees absence, so later probes hit O(1) without another MRO walk.
const attrQueryMissingSlot int32 = -2

// AttrQueryPICEntry caches a 4-way polymorphic hasattr/getattr lookup keyed on
// (Name, TypeVer, ShapeID) so repeated attribute probes resolve in O(1).
type AttrQueryPICEntry struct {
	Name      val.Value
	CachedVal val.Value
	TypeVer   uint64
	ShapeID   uint32
	SlotIndex int32
	Kind      uint8
}

// AttrPICEntry represents a single slot in a 4-way polymorphic inline cache for attribute access.
//
// Shape identifies the concrete type and the absence of any shadowing instance
// attribute; TypeVer is the low 32 bits of the type version tag, so a data
// descriptor added to the class later invalidates the entry. Truncation is
// sound because Shape is compared as well.
type AttrPICEntry struct {
	Shape     *val.Shape
	SlotIndex int32
	TypeVer   uint32
}

// TransitionICEntry caches a monomorphic shape transition for OP_STORE_ATTR
// (such as self.x = val in __init__) so fresh instances transition from OldShape
// to NewShape in O(1) without LookupMRO or Shape.Transition.
type TransitionICEntry struct {
	OldShape *val.Shape
	NewShape *val.Shape
	TypeVer  uint64
	SlotIdx  int32
}

// ICSlot caches resolved attribute, method, global, and call targets for O(1)
// lookup. Sized to 64 bytes (one cache line) with pointer fields grouped first
// to minimize GC pointer-scan work.
type ICSlot struct {
	CachedVal   val.Value          // 16 B: cached attr/callable/global/descriptor/fget/fset
	CalleeCache *VMCodeCache       //  8 B: quickened call target cache
	NativeFn    val.VectorcallFunc //  8 B: quickened builtin function pointer
	// ExpectedTypeVer is the full 64-bit primary version tag (type version for
	// attr/descriptor caches, module dict version for ICKindModuleAttr, or
	// globals dict version for OP_LOAD_GLOBAL). Entries that skip the shape
	// guard (data descriptors) rely on version uniqueness across types, so it
	// must never be truncated.
	ExpectedTypeVer uint64 //  8 B
	// ExpectedAuxVer is the full 64-bit secondary version tag (metaclass version
	// for ICKindTypeAttr, ThisClass version for ICKindSuperMethod, or builtins
	// dict version for ICKindBuiltin).
	ExpectedAuxVer  uint64 //  8 B
	ExpectedShapeID uint32 //  4 B: Shape.ID (when non-data guard is needed)
	SlotIndex       int32  //  4 B: PyInstance.Slots / PyTuple.Items index or dict slot hint
	Kind            ICKind //  1 B
}

var (
	_ [16 - unsafe.Sizeof(AttrPICEntry{})]byte
	_ [unsafe.Sizeof(AttrPICEntry{}) - 16]byte
	_ [64 - unsafe.Sizeof(ICSlot{})]byte
	_ [unsafe.Sizeof(ICSlot{}) - 64]byte
)

// KwDefaultFill describes how to populate an omitted parameter slot in a quickened OP_CALL_KW site.
type KwDefaultFill struct {
	Slot   uint8
	DefIdx int8 // >= 0: index into fn.Defaults; -1: use Val (from fn.KwDefaults)
	Val    val.Value
}

const (
	kwCopyScratch   uint8 = iota
	kwCopyIdentity        // every KwSlots[i] == posCount + i (0 copies)
	kwCopyDirectFwd       // forward copy i=0..kwCount-1 is conflict-free
	kwCopyDirectRev       // reverse copy i=kwCount-1..0 is conflict-free
)

// KwPICEntry caches a monomorphic/polymorphic OP_CALL_KW parameter binding plan so
// keyword calls write directly into callee frame slots with 0 heap allocations.
type KwPICEntry struct {
	TargetCo      *CodeObject
	TargetFn      *val.PyFunction
	TargetType    *val.PyType
	TypeVersion   uint64
	DefaultsLen   int
	HasKwDefaults bool
	KwCopyMode    uint8
	KwDefaultsVer uint64
	NumLocals     int
	PosCount      int
	TotalArgs     int
	CalleeCache   *VMCodeCache
	KwSlots       [8]uint8
	DefaultsFill  []KwDefaultFill
}

// ExceptionEntry records a protected instruction range and handler target in a static exception table.
type ExceptionEntry struct {
	StartIP          int
	EndIP            int
	TargetIP         int
	TargetStackDepth int
	PushLasti        bool
}

// Inline generator and coroutine resumption modes stored on the caller CallFrame.
const (
	genModeNone uint8 = iota
	genModeForIter
	genModeYieldFrom
	genModeNext         // next(gen): raise StopIteration(retVal) on exhaustion
	genModeNextDefault  // next(gen, default): return genDefault on exhaustion
	genModeSend         // gen.send(v): raise StopIteration(retVal) on exhaustion
	genModeAwait        // await aw: store retVal at stack[retSP-1] on completion
	genModeCoroSend     // coro.send(v): store retVal at stack[retSP] on completion
	genModeStrJoin      // sep.join(gen): drain yielded strings directly into CallFrame.joinState
	genModeCollectList  // list(gen): drain yielded values directly into CallFrame.collectState
	genModeCollectTuple // tuple(gen): drain yielded values directly into CallFrame.collectState
	genModeCollectSet   // set(gen): drain yielded values directly into CallFrame.collectState
	genModeListExtend   // list.extend(gen): drain yielded values directly into CallFrame.collectState.list
	genModeCollectDict  // dict(gen): drain 2-item yielded pairs directly into CallFrame.collectState.dict
	genModeAny          // any(gen): short-circuit True on first truthy yield
	genModeAll          // all(gen): short-circuit False on first falsy yield
	genModeSum          // sum(gen[, start]): accumulate yielded numbers inline
	genModeMin          // min(gen): track minimum yielded item inline
	genModeMax          // max(gen): track maximum yielded item inline
)

type strJoinState struct {
	buf    strings.Builder
	sep    val.Value
	sepLen uint8
	first  bool
	count  int
}

type collectState struct {
	items      []val.Value
	set        *val.PySet
	list       *val.PyList
	dict       *val.PyDict
	acc        val.Value
	hasAcc     bool
	isSlice    bool
	sliceCur   int64
	sliceNext  int64
	sliceStop  int64
	sliceStep  int64
	isliceIter *val.PyIter
}

// CallFrame flags bitmask.
const (
	frameFlagStackIsVM   uint8 = 1 << 0
	frameFlagSameGlobals uint8 = 1 << 1 // callee shares caller globals; caller.globals not written/restored
	frameFlagInit        uint8 = 1 << 2 // caller awaiting __init__ return (initInstance valid)
	frameFlagCmp         uint8 = 1 << 3 // caller awaiting dunder compare return (frameAux.cmp* valid)
	frameFlagHasAux      uint8 = 1 << 4 // caller saved cells/cellPtrs/locals in vm.frameAux[idx]
	frameFlagHasShadow   uint8 = 1 << 5 // frame has materialized shadowFrame in vm.frameAux[idx]
	frameFlagGenOrCoro   uint8 = 1 << 6 // inline generator/coroutine suspension frame (frameAux.gen* valid)
)

const frameFlagSlowReturn = frameFlagHasAux | frameFlagCmp | frameFlagGenOrCoro

// frameAux holds cold CallFrame state in VM.frameAux (lazily allocated side slice
// indexed by frameIdx) so the hot CallFrame slab stays 64 bytes per entry.
type frameAux struct {
	cells        []val.Value
	cellPtrs     []*val.PyCell
	locals       val.Value
	shadowFrame  *val.PyFrame
	stack        []val.Value
	activeGen    *val.PyGenerator
	activeCoro   *val.PyCoroutine
	genFrameIdx  int
	genMode      uint8
	genDefault   val.Value
	genExitIP    int
	joinState    *strJoinState
	collectState *collectState
	cmpLeft      val.Value
	cmpRight     val.Value
	cmpOp        uint8
}

// CallFrame is an unboxed 64-byte (single cache line) hot value struct stored in
// the contiguous VM.frames slab. All 3 pointer words sit in the first 32 bytes so
// Go's GC pointer bitmap ends halfway through the struct.
type CallFrame struct {
	cache        *VMCodeCache //  8 B (derives co, codePtr, codeLen, consts)
	globals      *val.PyDict  //  8 B (only written when !frameFlagSameGlobals)
	initInstance val.Value    // 16 B (kept inline so C(...) never touches frameAux)
	ip           int          //  8 B
	baseOffset   int          //  8 B
	retSP        int          //  8 B
	excStackLen  int32        //  4 B
	flags        uint8        //  1 B (+ 3 B pad)
}

var (
	_ [64 - unsafe.Sizeof(CallFrame{})]byte
	_ [unsafe.Sizeof(CallFrame{}) - 64]byte
)

func (f *CallFrame) codeObject() *CodeObject {
	if f.cache != nil {
		return f.cache.co
	}
	return nil
}

func (f *CallFrame) stackIsVM() bool {
	return (f.flags & frameFlagStackIsVM) != 0
}

// LineSpanEntry maps an instruction pointer range [StartIP, EndIP) to source line and column.
type LineSpanEntry struct {
	StartIP uint32
	EndIP   uint32
	Line    uint32
	Col     uint32
}

// Standard Python CodeObject Flags bitmask constants.
const (
	FlagGenerator      uint32 = 1 << 0
	FlagCoroutine      uint32 = 1 << 1
	CO_OPTIMIZED       uint32 = 0x0001
	CO_NEWLOCALS       uint32 = 0x0002
	CO_VARARGS         uint32 = 0x0004
	CO_VARKEYWORDS     uint32 = 0x0008
	CO_NESTED          uint32 = 0x0010
	CO_GENERATOR       uint32 = 0x0020
	CO_NOFREE          uint32 = 0x0040
	CO_COROUTINE       uint32 = 0x0080
	CO_ASYNC_GENERATOR uint32 = 0x0200
)

// CodeObject represents a compiled function or module bytecode unit.
// Header is placed at offset 0 so a *CodeObject can be stored inside a val.Value (KindCode)
// in a constant pool with zero-cost downcasting.
type CodeObject struct {
	Header           val.ObjectHeader
	ID               uint32
	Name             string
	QualName         string
	FileName         string
	FirstLineNo      int
	Doc              val.Value
	PosOnlyArgCount  int
	KWOnlyArgCount   int
	Code             []uint32
	Consts           []val.Value
	ParamNames       []val.Value
	VarNames         []val.Value
	CellVars         []val.Value
	FreeVars         []val.Value
	CellParamIndices []int
	NumLocals        int
	NumParams        int
	MaxStackSize     int
	IsGenerator      bool
	IsCoroutine      bool
	CanSuspend       bool
	Flags            uint32
	ExceptionTable   []ExceptionEntry
	LineSpans        []LineSpanEntry
	Hotness          int32
	IsTier2Optimized int32
	GenStackHint     atomic.Int32
	// SimplePositionalOnly reports that every parameter of the code object is
	// a required positional-only parameter: no keyword-only parameter, no
	// *args and no **kwargs. PositionalParamCount is then the exact number of
	// positional parameters, which lets a call skip argument binding.
	SimplePositionalOnly bool
	PositionalParamCount int
	PosParamCount        int
	VarArgsIdx           int
	VarKWIdx             int
	ClassCellIdx         int16
	VarKWSafe            bool
	MapTempSafe          []bool
	MatchLocalSafe       [4]uint64
	EffectiveLocals      int
	FrameStackNeed       int
	HasCells             bool
	HasCellVars          bool
	ParamShapeComputed   bool
	EntryIP              int
	// stream publishes the instruction slice the VM executes. It is created
	// together with the code object, before the object is shared between VMs.
	stream *codeStream
	// StreamGen counts the publications of the instruction stream. A VM uses
	// it to notice that a newer stream is available without having to load
	// the stream itself on every frame switch.
	StreamGen int32
	// primary publishes the code cache of the VM that most recently executed
	// this code object. The interpreter loop reads it to reach the cache
	// without calling getCodeCache.
	primary unsafe.Pointer
	// inlineMeta caches static per-CodeObject inlining eligibility and scan
	// metadata keyed by StreamGen in a single race-free atomic word.
	inlineMeta atomic.Uint64
}

// Active returns the instruction slice that must be executed for this code
// object. It is the compiled stream until the Tier-2 optimizer publishes a
// fused copy of it.
func (co *CodeObject) Active() []uint32 {
	if co == nil {
		return nil
	}
	if co.stream == nil {
		return co.Code
	}
	return co.stream.Active()
}

// initStream creates the publication stream of the code object. It has to run
// before the object is shared between VMs, so it is called from Init.
func (co *CodeObject) initStream() {
	if co == nil || co.stream != nil {
		return
	}
	co.stream = &codeStream{}
	co.stream.active.Store(&co.Code)
}

// publish installs a new instruction slice for every VM executing the code
// object.
//
// The publication stream is created together with the code object, so it is
// never created here: doing so would race with the VMs that read it.
func (co *CodeObject) publish(code []uint32) {
	if co == nil || co.stream == nil {
		return
	}
	co.stream.Publish(code)
	// The generation is bumped after the stream was stored, so a VM that
	// observes the new generation also observes the new stream.
	atomic.AddInt32(&co.StreamGen, 1)
}

// reloadCode installs the instruction stream that the code object published.
//
//go:noinline
func reloadCode(co *CodeObject, cache *VMCodeCache, gen int32) []uint32 {
	newStream := co.Active()
	if cache.privateActive && len(cache.active) == len(newStream) {
		priv := make([]uint32, len(newStream))
		copy(priv, newStream)
		for i, oldInst := range cache.active {
			switch DecodeOpcode(oldInst) {
			case OP_CALL_PY_EXACT_ARGS, OP_CALL_PY_DEFAULTS, OP_CALL_BUILTIN_FAST, OP_CALL_LEN, OP_CALL_ISINSTANCE, OP_CALL_TYPE_INIT,
				OP_CALL_HASATTR, OP_CALL_GETATTR, OP_CALL_BOUND_NATIVE, OP_CALL_INSTANCE_DUNDER:
				if DecodeOpcode(priv[i]) == OP_CALL {
					priv[i] = oldInst
				}
			case OP_CALL_METHOD_DESCR, OP_CALL_METHOD_DESCR_FAST:
				if DecodeOpcode(priv[i]) == OP_CALL_METHOD_PIC {
					priv[i] = oldInst
				}
			case OP_FOR_ITER_GEN:
				if DecodeOpcode(priv[i]) == OP_FOR_ITER {
					priv[i] = oldInst
				}
			case OP_COMPARE_DUNDER:
				if i >= 2 && DecodeOpcode(priv[i-2]) == OP_COMPARE_FAST_FAST && DecodeOpcode(priv[i-1]) == OP_NOP && DecodeOpcode(priv[i]) == OP_NOP {
					regA := uint8(priv[i-2] >> 8)
					regB := uint8(priv[i-2])
					priv[i-2] = Encode(OP_LOAD_FAST, 0, uint16(regA))
					priv[i-1] = Encode(OP_LOAD_FAST, 0, uint16(regB))
					priv[i] = oldInst
				} else if op := DecodeOpcode(priv[i]); op == OP_COMPARE_OP || op == OP_COMPARE_LT {
					priv[i] = oldInst
				} else if op == OP_COMPARE_JUMP_IF_FALSE || op == OP_COMPARE_JUMP_IF_TRUE {
					jumpOp := OP_JUMP_IF_FALSE
					if op == OP_COMPARE_JUMP_IF_TRUE {
						jumpOp = OP_JUMP_IF_TRUE
					}
					target := DecodeArgBx(priv[i])
					priv[i] = oldInst
					if i+1 < len(priv) {
						priv[i+1] = Encode(jumpOp, 0, target)
					}
				} else if i > 0 {
					if prevOp := DecodeOpcode(priv[i-1]); prevOp == OP_CMP_CONST_JUMP_IF_FALSE || prevOp == OP_CMP_CONST_JUMP_IF_TRUE {
						jumpOp := OP_JUMP_IF_FALSE
						if prevOp == OP_CMP_CONST_JUMP_IF_TRUE {
							jumpOp = OP_JUMP_IF_TRUE
						}
						constIdx := uint16(DecodeArgB(priv[i-1]))
						target := uint16(i-1) + uint16(DecodeArgC(priv[i-1]))
						priv[i-1] = Encode(OP_LOAD_CONST, 0, constIdx)
						priv[i] = oldInst
						if i+1 < len(priv) {
							priv[i+1] = Encode(jumpOp, 0, target)
						}
					}
				}
			case OP_LOAD_FAST:
				if (DecodeOpcode(priv[i]) == OP_LOAD_FAST_ATTR || DecodeOpcode(priv[i]) == OP_STORE_FAST_ATTR) && i+1 < len(priv) {
					attrOp := OP_LOAD_ATTR
					if DecodeOpcode(priv[i]) == OP_STORE_FAST_ATTR {
						attrOp = OP_STORE_ATTR
					}
					priv[i+1] = Encode(attrOp, 0, DecodeArgBx(priv[i]))
					priv[i] = oldInst
				}
			}
		}
		cache.active = priv
		cache.codePtr = unsafe.Pointer(unsafe.SliceData(priv))
		cache.codeLen = len(priv)
		cache.streamGen = gen
		return cache.active
	}
	cache.streamGen = gen
	cache.active = newStream
	cache.codePtr = unsafe.Pointer(unsafe.SliceData(newStream))
	cache.codeLen = len(newStream)
	cache.privateActive = false
	return cache.active
}

var nextCodeObjectID uint32

func (co *CodeObject) initID() uint32 {
	id := atomic.LoadUint32(&co.ID)
	if id != 0 {
		return id
	}
	newID := atomic.AddUint32(&nextCodeObjectID, 1)
	if atomic.CompareAndSwapUint32(&co.ID, 0, newID) {
		return newID
	}
	return atomic.LoadUint32(&co.ID)
}

// LineAt resolves the 1-based source line number for instruction pointer ip.
func (co *CodeObject) LineAt(ip int) int {
	if co == nil {
		return 1
	}
	uip := uint32(ip)
	for i := len(co.LineSpans) - 1; i >= 0; i-- {
		sp := &co.LineSpans[i]
		if uip >= sp.StartIP && uip < sp.EndIP {
			return int(sp.Line)
		}
	}
	if co.FirstLineNo > 0 {
		return co.FirstLineNo
	}
	return 1
}

// LineTableBytes encodes LineSpans into a compact byte slice representation.
func (co *CodeObject) LineTableBytes() []byte {
	if co == nil || len(co.LineSpans) == 0 {
		return nil
	}
	buf := make([]byte, len(co.LineSpans)*16)
	for i, sp := range co.LineSpans {
		binary.LittleEndian.PutUint32(buf[i*16:], sp.StartIP)
		binary.LittleEndian.PutUint32(buf[i*16+4:], sp.EndIP)
		binary.LittleEndian.PutUint32(buf[i*16+8:], sp.Line)
		binary.LittleEndian.PutUint32(buf[i*16+12:], sp.Col)
	}
	return buf
}

// ConstKeyMapTemplate caches a pre-hashed dict literal shape for
// OP_BUILD_CONST_KEY_MAP sites with 2..16 interned-string keys.
type ConstKeyMapTemplate struct {
	Hashes      [16]uint64
	Indices8    [8]byte
	Indices16   [16]byte
	Indices32   [32]byte
	Keys        [16]val.Value
	KeysTuple   val.Value
	Count       int
	Valid       bool
	IsEphemeral bool
}

// InlineExpectEntry records the expected callee identity for a spliced
// guard site so later polymorphic changes miss safely.
type InlineExpectEntry struct {
	Fn             val.Value
	TypeVer        uint64
	AuxVer         uint64
	ShapeID        uint32
	Shape2         uint32
	TotalArgs      uint8
	SavedSlot      uint8
	ZeroFrom       uint8
	Dunder         bool
	IsFastAttrProp bool
}

// inlineSite records metadata for a spliced inline body so exception unwinding
// and frame materialization map spliced IPs back to the callee and caller.
type inlineSite struct {
	GuardIP   int
	BodyStart int
	StubStart int
	SavedSlot int
	Callee    *CodeObject
	Fn        *val.PyFunction
	IPMap     []uint16
}
type VMCodeCache struct {
	// owner is the VM this cache belongs to. It lets a code object publish
	// its cache without carrying a VM pointer of its own.
	owner        *VM
	co           *CodeObject
	ic           []ICSlot
	methodPIC    [][4]val.MethodPICEntry
	attrPIC      [][4]AttrPICEntry
	attrQueryPIC [][4]AttrQueryPICEntry
	transitionIC []TransitionICEntry
	kwPIC        [][2]KwPICEntry
	inlineExpect []InlineExpectEntry
	inlineConsts []val.Value
	inlineSites  []inlineSite
	constKeyMaps []ConstKeyMapTemplate
	// hotness counts how often this VM executed the code object. It is
	// VM-local, so it never shares a cache line with another VM.
	hotness uint32
	// active is the instruction stream this VM loaded for the code object,
	// and streamGen the generation it belongs to. The pair lets the VM pick
	// up a stream published by the Tier-2 optimizer without loading it on
	// every frame switch.
	active          []uint32
	codePtr         unsafe.Pointer
	codeLen         int
	streamGen       int32
	privateActive   bool
	tier2Done       uint8
	attrQueryPICExt [][4]AttrQueryPICEntry
	setAttrIC       []setAttrICEntry
}

// setAttrICEntry caches monomorphic object.__setattr__(self, const_name, value)
// resolutions at OP_CALL_BOUND_NATIVE sites (such as attrs/dataclass __init__).
type setAttrICEntry struct {
	name          val.Value
	typeVer       uint64
	shapeID       uint32
	slotIdx       int32
	nextShape     *val.Shape
	isMemberDescr bool
}

func (c *VMCodeCache) ensureSetAttrIC() []setAttrICEntry {
	if len(c.setAttrIC) < len(c.ic) {
		n := make([]setAttrICEntry, len(c.ic))
		copy(n, c.setAttrIC)
		c.setAttrIC = n
	}
	return c.setAttrIC
}

func (c *VMCodeCache) ensureMethodPIC() [][4]val.MethodPICEntry {
	if len(c.methodPIC) < len(c.ic) {
		n := make([][4]val.MethodPICEntry, len(c.ic))
		copy(n, c.methodPIC)
		c.methodPIC = n
	}
	return c.methodPIC
}

func (c *VMCodeCache) ensureAttrPIC() [][4]AttrPICEntry {
	if len(c.attrPIC) < len(c.ic) {
		n := make([][4]AttrPICEntry, len(c.ic))
		copy(n, c.attrPIC)
		c.attrPIC = n
	}
	return c.attrPIC
}

func (c *VMCodeCache) ensureAttrQueryPIC() [][4]AttrQueryPICEntry {
	if len(c.attrQueryPIC) < len(c.ic) {
		n := make([][4]AttrQueryPICEntry, len(c.ic))
		copy(n, c.attrQueryPIC)
		c.attrQueryPIC = n
	}
	return c.attrQueryPIC
}

func (c *VMCodeCache) ensureAttrQueryPICExt() [][4]AttrQueryPICEntry {
	if len(c.attrQueryPICExt) < len(c.ic) {
		n := make([][4]AttrQueryPICEntry, len(c.ic))
		copy(n, c.attrQueryPICExt)
		c.attrQueryPICExt = n
	}
	return c.attrQueryPICExt
}

func (c *VMCodeCache) ensureTransitionIC() []TransitionICEntry {
	if len(c.transitionIC) < len(c.ic) {
		n := make([]TransitionICEntry, len(c.ic))
		copy(n, c.transitionIC)
		c.transitionIC = n
	}
	return c.transitionIC
}

func (c *VMCodeCache) ensureInlineExpect(n int) []InlineExpectEntry {
	if len(c.inlineExpect) < n {
		nw := make([]InlineExpectEntry, n)
		copy(nw, c.inlineExpect)
		c.inlineExpect = nw
	}
	return c.inlineExpect
}

func (c *VMCodeCache) frameConsts(co *CodeObject) []val.Value {
	if c != nil && c.inlineConsts != nil {
		return c.inlineConsts
	}
	return co.Consts
}

func (c *VMCodeCache) findInlineSite(ip int) *inlineSite {
	if c == nil {
		return nil
	}
	for i := range c.inlineSites {
		s := &c.inlineSites[i]
		if ip >= s.BodyStart && ip < s.StubStart+2 {
			return s
		}
	}
	return nil
}

func (c *VMCodeCache) ensureConstKeyMaps(n int) []ConstKeyMapTemplate {
	if len(c.constKeyMaps) < n {
		nw := make([]ConstKeyMapTemplate, n)
		copy(nw, c.constKeyMaps)
		c.constKeyMaps = nw
	}
	return c.constKeyMaps
}

// quickenAt writes a quickened instruction word into this VM's private copy of
// the instruction stream at ip, lazily cloning active once so shared streams
// are never mutated across VMs.
//
//go:noinline
func (c *VMCodeCache) quickenAt(ip int, newInst uint32) unsafe.Pointer {
	if !c.privateActive {
		priv := make([]uint32, len(c.active))
		copy(priv, c.active)
		c.active = priv
		c.codePtr = unsafe.Pointer(unsafe.SliceData(priv))
		c.codeLen = len(priv)
		c.privateActive = true
	}
	c.active[ip] = newInst
	return c.codePtr
}

// ensureKwPIC lazily allocates the OP_CALL_KW PIC slab for this code object.
//
//go:noinline
func (c *VMCodeCache) ensureKwPIC(codeLen int) [][2]KwPICEntry {
	if len(c.kwPIC) < codeLen {
		c.kwPIC = make([][2]KwPICEntry, codeLen)
	}
	return c.kwPIC
}

// codeStream publishes the instruction slice that VMs execute for a code
// object.
//
// The slice a VM reads is never modified in place: the Tier-2 optimizer and
// the deoptimizer both build a fresh copy and publish it with a single atomic
// store. A VM that is already executing therefore never observes a partially
// rewritten instruction window.
type codeStream struct {
	active atomic.Pointer[[]uint32]
}

// Active returns the instruction slice that must be executed.
func (s *codeStream) Active() []uint32 {
	if s == nil {
		return nil
	}
	p := s.active.Load()
	if p == nil {
		return nil
	}
	return *p
}

// Publish installs a new instruction slice for every VM.
func (s *codeStream) Publish(code []uint32) {
	if s == nil {
		return
	}
	s.active.Store(&code)
}

// computeEntryIP detects a leading docstring prologue (OP_LOAD_CONST + OP_POP_TOP
// or OP_JUMP 2) that is never a branch target or covered by an exception entry
// and sets EntryIP = 2 so frame entry jumps straight to the first live opcode.
func (co *CodeObject) computeEntryIP() {
	if co == nil || co.EntryIP != 0 || len(co.Code) < 3 {
		return
	}
	w0 := co.Code[0]
	w1 := co.Code[1]
	op0 := DecodeOpcode(w0)
	op1 := DecodeOpcode(w1)
	isDocPrologue := (op0 == OP_LOAD_CONST && op1 == OP_POP_TOP &&
		int(DecodeArgBx(w0)) < len(co.Consts) &&
		co.Consts[DecodeArgBx(w0)].Kind() != val.KindMemoryView) ||
		(op0 == OP_JUMP && DecodeArgBx(w0) == 2)
	if !isDocPrologue {
		return
	}
	for _, e := range co.ExceptionTable {
		if e.StartIP < 2 || e.TargetIP < 2 {
			return
		}
	}
	for _, inst := range co.Code {
		switch DecodeOpcode(inst) {
		case OP_JUMP, OP_JUMP_IF_FALSE, OP_JUMP_IF_TRUE, OP_JUMP_IF_FALSE_OR_POP, OP_JUMP_IF_TRUE_OR_POP,
			OP_FOR_ITER, OP_FOR_ITER_GEN, OP_FOR_ITER_RANGE, OP_COMPARE_JUMP_IF_FALSE, OP_COMPARE_JUMP_IF_TRUE,
			OP_FAST_LOOP_CMP_LT_CONST_JUMP, OP_FAST_CMP_EQ_JUMP_IF_FALSE, OP_FAST_JUMP_IF_DEREF_LIST_SUBSCR_TRUE,
			OP_JUMP_IF_BITAND_ZERO, OP_FAST_CMP_EQ_LOCAL_DEREF_JUMP_IF_FALSE:
			if DecodeArgBx(inst) < 2 {
				return
			}
		}
	}
	co.EntryIP = 2
}

// Compile-time check that CodeObject.Header is at byte offset 0.
var _ [0 - unsafe.Offsetof(CodeObject{}.Header)]byte

// Init initializes the CodeObject header and assigns its unique ID safely before execution.
func (co *CodeObject) Init() *CodeObject {
	if co == nil {
		return nil
	}
	co.Header.Kind = val.KindCode
	co.Header.Type = val.CodeType
	co.initID()
	co.InitCellParamIndices()
	co.computeParamShape()
	co.computeEntryIP()
	co.initStream()
	if len(co.Code) < 4 || co.IsGenerator || co.IsCoroutine {
		co.IsTier2Optimized = 1
	}
	return co
}

// InitCellParamIndices precomputes the parameter index for each CellVar to avoid runtime string matching.
func (co *CodeObject) InitCellParamIndices() {
	if co == nil || co.CellParamIndices != nil || len(co.CellVars) == 0 {
		return
	}
	indices := make([]int, len(co.CellVars))
	for i, name := range co.CellVars {
		indices[i] = -1
		nameStr := ""
		for p, pn := range co.ParamNames {
			if val.StringEquals(pn, name) {
				indices[i] = p
				break
			}
			if pn.StringHasBytePrefix('*') {
				pnStr := pn.AsString()
				if len(pnStr) > 1 {
					clean := pnStr[1:]
					if len(clean) > 0 && clean[0] == '*' {
						clean = clean[1:]
					}
					if nameStr == "" {
						nameStr = name.AsString()
					}
					if clean == nameStr {
						indices[i] = p
						break
					}
				}
			}
		}
	}
	co.CellParamIndices = indices
}

// NewCodeVal wraps a *CodeObject as a val.Value with KindCode.
func NewCodeVal(co *CodeObject) val.Value {
	if co == nil {
		return val.NoneVal()
	}
	co.initID()
	co.computeParamShape()
	co.computeEntryIP()
	// The publication stream has to exist before the code object is shared
	// between VMs, and a code object becomes shareable as soon as it is
	// turned into a value.
	co.initStream()
	if co.Header.Kind != val.KindCode {
		co.Header.Kind = val.KindCode
		co.Header.Type = val.CodeType
	}
	if co.IsCoroutine && !co.CanSuspend {
		for _, inst := range co.Code {
			op := DecodeOpcode(inst)
			if op == OP_AWAIT || op == OP_YIELD_VALUE || op == OP_YIELD_FROM {
				co.CanSuspend = true
				break
			}
		}
	}
	return val.ObjectVal(&co.Header)
}

// AsCodeObject performs a zero-cost offset-0 downcast from val.Value to *CodeObject.
func AsCodeObject(v val.Value) *CodeObject {
	return (*CodeObject)(unsafe.Pointer(v.HeaderPtr()))
}

type reASCIIEntry struct {
	ps      *val.PyString
	strLen  int
	isASCII bool
}

// VM represents a single execution thread with contiguous Value, CallFrame, and ICSlot slabs,
// plus a VM-managed string interning pool and zero-allocation Vectorcall execution context.
type VM struct {
	engine               *Engine
	stack                []val.Value
	frames               []CallFrame
	frameAux             []frameAux
	maxFrameAuxLen       int
	codeCaches           []*VMCodeCache
	builtinRange         val.Value
	builtinLen           val.Value
	builtinOpen          val.Value
	builtinIsInstance    val.Value
	builtinNext          val.Value
	builtinAny           val.Value
	builtinAll           val.Value
	builtinSum           val.Value
	builtinMin           val.Value
	builtinMax           val.Value
	builtinSuper         val.Value
	builtinHasattr       val.Value
	builtinGetattr       val.Value
	strBuildBuf          []byte
	internPool           *val.InternPool
	execCtx              val.ExecContext
	activeFrameIdx       int
	activeCO             *CodeObject
	activeBaseOffset     int
	activeCells          []val.Value
	activeCellPtrs       []*val.PyCell
	activeState          *loopState
	kwScratch            [64]val.Value
	kwSet                [64]bool
	kwNamesScratch       [16]val.Value
	globals              *val.PyDict
	builtins             *val.PyDict
	modules              *val.PyDict
	lastException        val.Value
	unhandledException   val.Value
	excStack             []val.Value
	deferredTBExc        val.Value
	deferredTBBound      bool
	deferredTBs          []deferredTBEntry
	context              *val.PyContext
	bindingOSPath        bool
	pendingVirtualenvPth bool
	coroFreeList         []*val.PyCoroutine
	genFreeList          []*val.PyGenerator
	iterFreeList         []*val.PyIter
	tupleFreeList        []*val.PyTuple
	superFreeList        []*val.PySuper
	dictFreeList         []*val.PyDict
	mediumDictFreeList   []*val.PyDict
	dict16FreeList       []*val.PyDict
	dict32FreeList       []*val.PyDict
	strJoinPool          []*strJoinState
	collectPool          []*collectState
	loopStatePool        []*loopState
	genStackPools        [5][][]val.Value
	genCellValPools      [8][][]val.Value
	genCellPtrPools      [8][][]*val.PyCell
	genResumeHook        func(ctx *val.ExecContext, gen *val.PyGenerator, val val.Value) (val.Value, val.IterStatus)
	genReleaseHook       func(gen *val.PyGenerator)
	coroResumeHook       func(ctx *val.ExecContext, coro *val.PyCoroutine, val val.Value) (val.Value, val.IterStatus)
	coroReleaseHook      func(coro *val.PyCoroutine)
	jsonParser           *jsonParser
	jsonDumpBuf          []byte
	maxSP                int
	maxFrameIdx          int
	// Continuous join drain state. While activeJoinGen is set, a generator
	// drained by joinGenerator runs inside a single runLoopWithGlobals
	// invocation: yields append directly into joinSink instead of
	// suspending. The pointer check against the loop's activeGen keeps
	// nested invocations isolated.
	activeJoinGen *val.PyGenerator
	joinSink      *strings.Builder
	joinBuf       strings.Builder
	joinSep       string
	joinFirst     bool
	joinCount     int
	joinErrMsg    string
	// mvFree holds the memory views that are no longer referenced and can
	// be handed out again by the next slice.
	mvFree          []*val.PyMemoryView
	matchFreeList   []*pyMatch
	matchBoolOnly   bool
	matchEphemeral  bool
	lastReStr       *val.PyString
	lastReStrLen    int
	lastReRunes     []rune
	reInlineBuf     [7]rune
	reASCIICache    [4]reASCIIEntry
	reASCIICachePos int
	hasTrackedViews bool
	rootCO          CodeObject
	rootCache       VMCodeCache
	limits          RuntimeLimits
	limitsActive    bool
	stepCount       uint64
	eventLoop       *EventLoop
}

// loadWindow returns the instruction window of co as a base pointer and a
// length. The window is refreshed when the Tier-2 optimizer published a newer
// instruction stream for the code object.
func (vm *VM) loadWindow(co *CodeObject, cache *VMCodeCache) (unsafe.Pointer, int) {
	if gen := atomic.LoadInt32(&co.StreamGen); gen != cache.streamGen {
		reloadCode(co, cache, gen)
	}
	return cache.codePtr, cache.codeLen
}

// cacheFor returns this VM's inline cache for co using a single owner-checked
// load on the fast path, falling back to getCodeCacheSlow on miss.
func (vm *VM) cacheFor(co *CodeObject) *VMCodeCache {
	if co != nil {
		if pc := (*VMCodeCache)(atomic.LoadPointer(&co.primary)); pc != nil && pc.owner == vm {
			return pc
		}
	}
	return vm.getCodeCacheSlow(co)
}

//go:noinline
func (vm *VM) getCodeCacheSlow(co *CodeObject) *VMCodeCache {
	if co == nil || co == &vm.rootCO {
		// The root slabs are only used by the low-level Run() API, so they
		// are allocated on demand instead of for every VM.
		cache := &vm.rootCache
		cache.owner = vm
		cache.co = co
		need := DefaultICSlabSize
		if co != nil && len(co.Code) > need {
			need = len(co.Code)
		}
		if len(cache.ic) < need {
			cache.ic = make([]ICSlot, need)
		}
		if co != nil {
			// The root code object is recreated for every Run() call, so its
			// instruction stream is always fetched anew.
			cache.streamGen = atomic.LoadInt32(&co.StreamGen)
			cache.active = co.Active()
			cache.codePtr = unsafe.Pointer(unsafe.SliceData(cache.active))
			cache.codeLen = len(cache.active)
			cache.privateActive = false
		}
		return cache
	}
	id := int(co.initID())
	if id >= len(vm.codeCaches) {
		newCaches := make([]*VMCodeCache, id+16)
		copy(newCaches, vm.codeCaches)
		vm.codeCaches = newCaches
	}
	cache := vm.codeCaches[id]
	if cache == nil {
		cache = &VMCodeCache{owner: vm, co: co}
		vm.codeCaches[id] = cache
	} else if cache.co == nil {
		cache.co = co
	}
	if cache.active == nil {
		// The generation is read before the stream, so a publication that
		// happens in between is picked up by the next refresh.
		cache.streamGen = atomic.LoadInt32(&co.StreamGen)
		cache.active = co.Active()
		cache.codePtr = unsafe.Pointer(unsafe.SliceData(cache.active))
		cache.codeLen = len(cache.active)
		cache.privateActive = false
	}
	if len(cache.ic) < len(co.Code) {
		cache.ic = make([]ICSlot, len(co.Code))
	}
	// Publish the cache so that the interpreter loop can reach it without a
	// call into getCodeCache.
	atomic.StorePointer(&co.primary, unsafe.Pointer(cache))
	return cache
}

// Engine returns the parent Engine managing this VM.
func (vm *VM) Engine() *Engine {
	return vm.engine
}

var genStackSizes = [5]int{64, 256, 512, 1024, 2048}

func (vm *VM) acquireGeneratorStack(minSize int) ([]val.Value, uint8) {
	if minSize <= 64 {
		minSize = 64
	}
	for cls, sz := range genStackSizes {
		if minSize <= sz {
			pool := vm.genStackPools[cls]
			if n := len(pool); n > 0 {
				s := pool[n-1]
				vm.genStackPools[cls] = pool[:n-1]
				recordGenStackPool(true)
				return s, uint8(cls)
			}
			recordGenStackPool(false)
			return make([]val.Value, sz), uint8(cls)
		}
	}
	recordGenStackPool(false)
	return make([]val.Value, minSize), 255
}

func (vm *VM) releaseStackSlice(s []val.Value) {
	if s == nil {
		return
	}
	for cls, sz := range genStackSizes {
		if len(s) == sz {
			if len(vm.genStackPools[cls]) < 256 {
				clear(s)
				vm.genStackPools[cls] = append(vm.genStackPools[cls], s)
			}
			return
		}
	}
}

func (vm *VM) acquireForIter(obj val.Value) val.Value {
	var it *val.PyIter
	if n := len(vm.iterFreeList); n > 0 {
		it = vm.iterFreeList[n-1]
		vm.iterFreeList[n-1] = nil
		vm.iterFreeList = vm.iterFreeList[:n-1]
	} else {
		it = &val.PyIter{}
	}
	it.Header = val.ObjectHeader{Kind: val.KindIter, Flags: val.FlagEphemeralIter, Type: val.IterType}
	it.Obj = obj
	it.Idx = 0
	switch {
	case obj.IsList():
		it.Kind = val.IterKindList
	case obj.IsTuple():
		it.Kind = val.IterKindTuple
	case obj.IsDict():
		it.Kind = val.IterKindDict
	case obj.IsSet():
		it.Kind = val.IterKindSet
	default:
		it.Kind = val.IterKindInvalid
	}
	return val.ObjectVal(&it.Header)
}

func (vm *VM) releaseForIter(it *val.PyIter) {
	if it == nil || (it.Header.Flags&val.FlagEphemeralIter) == 0 {
		return
	}
	it.Header.Flags = 0
	it.Obj = val.Value{}
	it.Sub = val.Value{}
	it.Seq = nil
	it.NextFn = nil
	it.Idx = 0
	it.Cur = 0
	it.NextIdx = 0
	it.Stop = 0
	it.Step = 0
	if len(vm.iterFreeList) < 256 {
		vm.iterFreeList = append(vm.iterFreeList, it)
	}
}

func (vm *VM) acquireMatch() *pyMatch {
	if n := len(vm.matchFreeList); n > 0 {
		m := vm.matchFreeList[n-1]
		vm.matchFreeList[n-1] = nil
		vm.matchFreeList = vm.matchFreeList[:n-1]
		return m
	}
	return &pyMatch{}
}

func (vm *VM) releaseMatch(m *pyMatch) {
	if m == nil {
		return
	}
	m.Header.Flags = 0
	m.Header.Hash = 0
	m.Pat = nil
	m.StringVal = val.Value{}
	m.extCaps = nil
	m.inlineCaps = [12]int32{}
	m.Pos = 0
	m.EndPos = 0
	m.LastIndex = 0
	m.numCaps = 0
	if len(vm.matchFreeList) < 128 {
		vm.matchFreeList = append(vm.matchFreeList, m)
	}
}

//go:noinline
func (vm *VM) releaseDeadEphemeralIters(dead []val.Value) {
	for i := range dead {
		v := dead[i]
		if v.IsIter() && (v.HeaderPtr().Flags&val.FlagEphemeralIter) != 0 {
			vm.releaseForIter(v.AsIter())
		}
	}
}

func (vm *VM) acquireEphemeralTuple(src []val.Value) val.Value {
	var t *val.PyTuple
	if n := len(vm.tupleFreeList); n > 0 {
		t = vm.tupleFreeList[n-1]
		vm.tupleFreeList[n-1] = nil
		vm.tupleFreeList = vm.tupleFreeList[:n-1]
	} else {
		t = &val.PyTuple{}
	}
	t.Header = val.ObjectHeader{Kind: val.KindTuple, Flags: val.FlagEphemeralTuple, Type: val.TupleType}
	t.Attrs = nil
	t.SetInline(src)
	return val.ObjectVal(&t.Header)
}

//go:noinline
func (vm *VM) releaseEphemeralTupleVal(v val.Value) {
	if !v.IsTuple() || (v.HeaderPtr().Flags&val.FlagEphemeralTuple) == 0 {
		return
	}
	t := v.AsTuple()
	t.Header.Flags = 0
	t.Header.Hash = 0
	t.Attrs = nil
	t.ClearInline()
	if len(vm.tupleFreeList) < 256 {
		vm.tupleFreeList = append(vm.tupleFreeList, t)
		return
	}
	releaseEphemeralTuplePooled(t)
}

func (vm *VM) releaseGeneratorStack(gen *val.PyGenerator) {
	if gen == nil {
		return
	}
	if gen.Stack != nil {
		s := gen.Stack
		gen.Stack = nil
		vm.releaseStackSlice(s)
	}
	vm.releaseGenCellSlices(gen)
}

func (vm *VM) releaseCellPtrSlice(p []*val.PyCell) {
	if n := len(p); n >= 1 && n <= len(vm.genCellPtrPools) && len(vm.genCellPtrPools[n-1]) < 128 {
		clear(p)
		vm.genCellPtrPools[n-1] = append(vm.genCellPtrPools[n-1], p)
	}
}

func (vm *VM) acquireGenCellSlices(n int) ([]val.Value, []*val.PyCell) {
	if n <= 0 {
		return nil, nil
	}
	if n <= len(vm.genCellValPools) {
		idx := n - 1
		var vals []val.Value
		if vPool := vm.genCellValPools[idx]; len(vPool) > 0 {
			vals = vPool[len(vPool)-1]
			vm.genCellValPools[idx] = vPool[:len(vPool)-1]
			recordGenCellPool(true)
		} else {
			recordGenCellPool(false)
			vals = make([]val.Value, n)
		}
		ptrs := vm.acquireGenCellPtrs(n)
		return vals, ptrs
	}
	recordGenCellPool(false)
	return make([]val.Value, n), make([]*val.PyCell, n)
}

func (vm *VM) acquireGenCellPtrs(n int) []*val.PyCell {
	if n <= 0 {
		return nil
	}
	if n <= len(vm.genCellPtrPools) {
		idx := n - 1
		if pPool := vm.genCellPtrPools[idx]; len(pPool) > 0 {
			ptrs := pPool[len(pPool)-1]
			vm.genCellPtrPools[idx] = pPool[:len(pPool)-1]
			recordGenCellPool(true)
			return ptrs
		}
		recordGenCellPool(false)
		return make([]*val.PyCell, n)
	}
	recordGenCellPool(false)
	return make([]*val.PyCell, n)
}

func (vm *VM) releaseCellSlices(own bool, cells []val.Value, ptrs []*val.PyCell) {
	if !own {
		return
	}
	if cells != nil {
		if n := len(cells); n >= 1 && n <= len(vm.genCellValPools) && len(vm.genCellValPools[n-1]) < 128 {
			clear(cells)
			vm.genCellValPools[n-1] = append(vm.genCellValPools[n-1], cells)
		}
	}
	if ptrs != nil {
		if n := len(ptrs); n >= 1 && n <= len(vm.genCellPtrPools) && len(vm.genCellPtrPools[n-1]) < 128 {
			clear(ptrs)
			vm.genCellPtrPools[n-1] = append(vm.genCellPtrPools[n-1], ptrs)
		}
	}
}

func (vm *VM) releaseGenCellSlices(gen *val.PyGenerator) {
	own := gen.OwnCells
	cells, ptrs := gen.Cells, gen.CellPtrs
	gen.OwnCells = false
	gen.Cells = nil
	gen.CellPtrs = nil
	vm.releaseCellSlices(own, cells, ptrs)
}

func (vm *VM) releaseCoroutineStack(coro *val.PyCoroutine) {
	if coro == nil || val.MultiThreadedFlag.Load() {
		return
	}
	if coro.Stack != nil {
		s := coro.Stack
		coro.Stack = nil
		vm.releaseStackSlice(s)
	}
	own := coro.OwnCells
	cells, ptrs := coro.Cells, coro.CellPtrs
	coro.OwnCells = false
	coro.Cells = nil
	coro.CellPtrs = nil
	vm.releaseCellSlices(own, cells, ptrs)
}

// Reset clears the active stack and execution frame state for reuse in Engine's sync.Pool.
func (vm *VM) Reset() {
	sp := min(max(vm.maxSP, vm.execCtx.SP), len(vm.stack))
	if sp > 0 {
		clear(vm.stack[:sp])
	}
	vm.maxSP = 0

	fLimit := min(vm.maxFrameIdx+1, len(vm.frames))
	if fLimit > 0 {
		clear(vm.frames[:fLimit])
	}
	vm.maxFrameIdx = 0
	if vm.maxFrameAuxLen > 0 {
		aLimit := min(vm.maxFrameAuxLen, len(vm.frameAux))
		for i := range aLimit {
			if js := vm.frameAux[i].joinState; js != nil {
				vm.frameAux[i].joinState = nil
				vm.releaseStrJoinState(js)
			}
			if cs := vm.frameAux[i].collectState; cs != nil {
				vm.frameAux[i].collectState = nil
				vm.releaseCollectState(cs)
			}
		}
		clear(vm.frameAux[:aLimit])
		vm.maxFrameAuxLen = 0
	}

	vm.execCtx.SP = 0
	vm.execCtx.Stack = vm.stack
	vm.execCtx.ClearException()
	vm.activeFrameIdx = 0
	vm.activeCO = nil
	vm.activeBaseOffset = 0
	vm.activeCells = nil
	vm.activeCellPtrs = nil
	vm.activeState = nil
	vm.lastException = val.NoneVal()
	vm.unhandledException = val.NoneVal()
	vm.jsonParser = nil
	if cap(vm.jsonDumpBuf) > 65536 {
		vm.jsonDumpBuf = nil
	}
	vm.lastReStr = nil
	vm.lastReStrLen = 0
	vm.lastReRunes = nil
	vm.reASCIICache = [4]reASCIIEntry{}
	vm.reASCIICachePos = 0
	if vm.context == nil || vm.context.Len() > 0 {
		vm.context = val.NewPyContext()
	}
}

// ActiveContext returns the context ContextVar reads and writes on this VM.
func (vm *VM) ActiveContext() *val.PyContext {
	if vm.context == nil {
		vm.context = val.NewPyContext()
	}
	return vm.context
}

// GetStackVal satisfies val.FrameVM.
func (vm *VM) GetStackVal(offset int) val.Value {
	if offset >= 0 && offset < len(vm.stack) {
		return vm.stack[offset]
	}
	return val.NoneVal()
}

// SetStackVal satisfies val.FrameVM.
func (vm *VM) SetStackVal(offset int, v val.Value) {
	if offset >= 0 && offset < len(vm.stack) {
		vm.stack[offset] = v
	}
}

// MaterializeFrame satisfies val.FrameVM.
func (vm *VM) MaterializeFrame(frameIdx int) val.Value {
	return vm.materializeFrame(frameIdx)
}

func (vm *VM) frameAuxAt(idx int) *frameAux {
	if idx >= len(vm.frameAux) {
		vm.growFrameAux(idx + 1)
	}
	if idx+1 > vm.maxFrameAuxLen {
		vm.maxFrameAuxLen = idx + 1
	}
	return &vm.frameAux[idx]
}

//go:noinline
func (vm *VM) growFrameAux(minLen int) {
	newCap := max(len(vm.frames), minLen, DefaultFrameSlabSize)
	next := make([]frameAux, newCap)
	copy(next, vm.frameAux)
	vm.frameAux = next
}

func (vm *VM) frameGlobals(idx int) *val.PyDict {
	if idx < 0 || idx >= len(vm.frames) {
		if vm.activeState != nil && vm.activeState.globals != nil {
			return vm.activeState.globals
		}
		return vm.globals
	}
	top := vm.activeFrameIdx
	if vm.activeState != nil {
		top = vm.activeState.frameIdx
	}
	if idx == top && vm.activeState != nil && vm.activeState.globals != nil {
		return vm.activeState.globals
	}
	for j := idx; j < top && j < len(vm.frames); j++ {
		f := &vm.frames[j]
		if (f.flags & frameFlagSameGlobals) == 0 {
			if f.globals != nil {
				return f.globals
			}
			break
		}
	}
	if vm.activeState != nil && vm.activeState.globals != nil {
		return vm.activeState.globals
	}
	if top >= 0 && top < len(vm.frames) && (vm.frames[top].flags&frameFlagSameGlobals) == 0 && vm.frames[top].globals != nil {
		return vm.frames[top].globals
	}
	return vm.globals
}

func (vm *VM) materializeFrame(targetIdx int) val.Value {
	if targetIdx < 0 || targetIdx >= len(vm.frames) {
		return val.NoneVal()
	}
	if vm.activeState != nil && targetIdx == vm.activeState.frameIdx {
		vm.syncActiveFrame(vm.activeState, vm.activeState.ip)
	}
	fSlot := &vm.frames[targetIdx]
	fIP := fSlot.ip
	if site := fSlot.cache.findInlineSite(fIP); site != nil {
		fIP = site.GuardIP
	}
	if (fSlot.flags&frameFlagHasShadow) != 0 && targetIdx < len(vm.frameAux) && vm.frameAux[targetIdx].shadowFrame != nil {
		vm.frameAux[targetIdx].shadowFrame.IP = fIP
		return val.NewFrameVal(vm.frameAux[targetIdx].shadowFrame)
	}
	co := fSlot.codeObject()
	baseOffset := fSlot.baseOffset
	var cells []val.Value
	var extraLocals val.Value
	if (fSlot.flags&frameFlagHasAux) != 0 && targetIdx < len(vm.frameAux) {
		cells = vm.frameAux[targetIdx].cells
		extraLocals = vm.frameAux[targetIdx].locals
	}
	if co == nil && targetIdx == vm.activeFrameIdx {
		co = vm.activeCO
		baseOffset = vm.activeBaseOffset
		cells = vm.activeCells
	}
	if co == nil && targetIdx == 0 {
		co = &vm.rootCO
	}
	if co == nil {
		return val.NoneVal()
	}
	codeVal := NewCodeVal(co)
	globals := vm.frameGlobals(targetIdx)
	if globals == nil {
		globals = vm.globals
	}
	pyFrame := val.NewPyFrame(
		vm,
		targetIdx,
		baseOffset,
		codeVal,
		globals,
		cells,
	)
	pyFrame.IP = fIP
	if extraLocals.IsDict() {
		pyFrame.ExtraLocals = extraLocals.AsDict()
	}
	aux := vm.frameAuxAt(targetIdx)
	aux.shadowFrame = pyFrame
	fSlot.flags |= frameFlagHasShadow
	return val.NewFrameVal(pyFrame)
}

type defaultCodeInspector struct{}

func (defaultCodeInspector) VarNames(code val.Value) []val.Value {
	co := AsCodeObject(code)
	if co == nil {
		return nil
	}
	if co.VarNames != nil {
		return co.VarNames
	}
	clean := make([]val.Value, 0, len(co.ParamNames))
	for _, p := range co.ParamNames {
		s := p.AsString()
		if strings.HasPrefix(s, "**") {
			s = s[2:]
		} else if strings.HasPrefix(s, "*") {
			s = s[1:]
		}
		if s != "" {
			clean = append(clean, val.InternString(s))
		}
	}
	return clean
}

func (defaultCodeInspector) CellVars(code val.Value) []val.Value {
	co := AsCodeObject(code)
	if co == nil {
		return nil
	}
	return co.CellVars
}

func (defaultCodeInspector) FreeVars(code val.Value) []val.Value {
	co := AsCodeObject(code)
	if co == nil {
		return nil
	}
	return co.FreeVars
}

func (defaultCodeInspector) Name(code val.Value) string {
	co := AsCodeObject(code)
	if co == nil {
		return ""
	}
	return co.Name
}

func (defaultCodeInspector) QualName(code val.Value) string {
	co := AsCodeObject(code)
	if co == nil {
		return ""
	}
	if co.QualName != "" {
		return co.QualName
	}
	return co.Name
}

func (defaultCodeInspector) FileName(code val.Value) string {
	co := AsCodeObject(code)
	if co == nil {
		return "<string>"
	}
	if co.FileName != "" {
		return co.FileName
	}
	return "<string>"
}

func (defaultCodeInspector) FirstLineNo(code val.Value) int {
	co := AsCodeObject(code)
	if co == nil || co.FirstLineNo <= 0 {
		return 1
	}
	return co.FirstLineNo
}

func (defaultCodeInspector) ArgCount(code val.Value) int {
	co := AsCodeObject(code)
	if co == nil {
		return 0
	}
	argCount := 0
	hasStar := false
	for _, p := range co.ParamNames {
		s := p.AsString()
		if strings.HasPrefix(s, "*") {
			hasStar = true
			break
		}
		argCount++
	}
	if !hasStar && co.KWOnlyArgCount > 0 && argCount >= co.KWOnlyArgCount {
		argCount -= co.KWOnlyArgCount
	}
	return argCount
}

func (defaultCodeInspector) PosOnlyArgCount(code val.Value) int {
	co := AsCodeObject(code)
	if co == nil {
		return 0
	}
	return co.PosOnlyArgCount
}

func (defaultCodeInspector) KWOnlyArgCount(code val.Value) int {
	co := AsCodeObject(code)
	if co == nil {
		return 0
	}
	return co.KWOnlyArgCount
}

func (defaultCodeInspector) Flags(code val.Value) uint32 {
	co := AsCodeObject(code)
	if co == nil {
		return 0
	}
	return co.Flags
}

func (defaultCodeInspector) LineAt(code val.Value, ip int) int {
	co := AsCodeObject(code)
	if co == nil {
		return 1
	}
	return co.LineAt(ip)
}

func (defaultCodeInspector) LineTable(code val.Value) []byte {
	co := AsCodeObject(code)
	if co == nil {
		return nil
	}
	return co.LineTableBytes()
}

func (defaultCodeInspector) LineSpans(code val.Value) []any {
	co := AsCodeObject(code)
	if co == nil {
		return nil
	}
	out := make([]any, len(co.LineSpans))
	for i, sp := range co.LineSpans {
		out[i] = val.NewTupleVal(
			val.Int64Val(int64(sp.StartIP)),
			val.Int64Val(int64(sp.EndIP)),
			val.Int64Val(int64(sp.Line)),
		)
	}
	return out
}

func (defaultCodeInspector) CodeBytes(code val.Value) []byte {
	co := AsCodeObject(code)
	if co == nil {
		return nil
	}
	b := make([]byte, len(co.Code)*4)
	for i, inst := range co.Code {
		binary.LittleEndian.PutUint32(b[i*4:], inst)
	}
	return b
}

func (defaultCodeInspector) Consts(code val.Value) []val.Value {
	co := AsCodeObject(code)
	if co == nil {
		return nil
	}
	return co.Consts
}

func (defaultCodeInspector) StackSize(code val.Value) int {
	co := AsCodeObject(code)
	if co == nil {
		return 0
	}
	return co.MaxStackSize
}

func (defaultCodeInspector) Doc(code val.Value) val.Value {
	co := AsCodeObject(code)
	if co == nil || co.Doc.HeaderPtr() == nil {
		return val.NoneVal()
	}
	return co.Doc
}

var standaloneVMPool = sync.Pool{
	New: func() any {
		return NewVM()
	},
}

func init() {
	val.CodeInspectorHook = defaultCodeInspector{}
	val.JoinGeneratorHook = func(ctx *val.ExecContext, sep string, gen *val.PyGenerator) (val.Value, bool) {
		if ctx == nil || ctx.VM == nil {
			return val.Value{}, false
		}
		vm, ok := ctx.VM.(*VM)
		if !ok {
			return val.Value{}, false
		}
		return vm.joinGenerator(ctx, sep, gen)
	}
	val.AcquireExecContextHook = func() (*val.ExecContext, func()) {
		v := standaloneVMPool.Get().(*VM)
		return &v.execCtx, func() {
			v.Reset()
			standaloneVMPool.Put(v)
		}
	}
	val.AcquireDictTable16Hook = acquireDictTable16
	val.ReleaseDictTable16Hook = releaseDictTable16
	val.AcquireEphemeralTupleHook = acquireEphemeralTuplePooled
}

// tuple4Pool caches cleared *val.PyTuple objects with inline [3]Value storage
// for small tuple concatenations. Single-threaded use only.
var tuple4Pool struct {
	mu   sync.Mutex
	free []*val.PyTuple
}

func acquireEphemeralTuplePooled() (*val.PyTuple, bool) {
	if val.MultiThreadedFlag.Load() {
		return nil, false
	}
	tuple4Pool.mu.Lock()
	n := len(tuple4Pool.free)
	if n == 0 {
		tuple4Pool.mu.Unlock()
		return nil, false
	}
	t := tuple4Pool.free[n-1]
	tuple4Pool.free[n-1] = nil
	tuple4Pool.free = tuple4Pool.free[:n-1]
	tuple4Pool.mu.Unlock()
	return t, true
}

func releaseEphemeralTuplePooled(t *val.PyTuple) {
	if t == nil || val.MultiThreadedFlag.Load() {
		return
	}
	t.Header.Flags = 0
	t.Header.Hash = 0
	t.Attrs = nil
	t.ClearInline()
	tuple4Pool.mu.Lock()
	if len(tuple4Pool.free) < 64 {
		tuple4Pool.free = append(tuple4Pool.free, t)
	}
	tuple4Pool.mu.Unlock()
}

// dictTable16Slab pairs a cleared 16-slot index table with its dense entry
// slice so growing past the 5-entry inline store reuses both arrays at once.
type dictTable16Slab struct {
	entries []val.DictEntry
	indices []byte
}

var dictTable16Pool struct {
	mu   sync.Mutex
	free []dictTable16Slab
}

// acquireDictTable16 returns a pooled 16-slot table or reports a miss.
// Single-threaded use only; multi-threaded runs bypass pooling entirely.
func acquireDictTable16() ([]val.DictEntry, []byte, bool) {
	if val.MultiThreadedFlag.Load() {
		return nil, nil, false
	}
	dictTable16Pool.mu.Lock()
	n := len(dictTable16Pool.free)
	if n == 0 {
		dictTable16Pool.mu.Unlock()
		return nil, nil, false
	}
	slab := dictTable16Pool.free[n-1]
	dictTable16Pool.free[n-1] = dictTable16Slab{}
	dictTable16Pool.free = dictTable16Pool.free[:n-1]
	dictTable16Pool.mu.Unlock()
	if cap(slab.entries) != val.Dict16EntryCap || len(slab.indices) != 16 {
		return nil, nil, false
	}
	return slab.entries[:0], slab.indices, true
}

// releaseDictTable16 returns cleared 16-slot tables to the pool. Value slots
// are zeroed so pooled slabs stay invisible to the collector.
func releaseDictTable16(entries []val.DictEntry, indices []byte) {
	if val.MultiThreadedFlag.Load() {
		return
	}
	if cap(entries) != val.Dict16EntryCap || len(indices) != 16 {
		return
	}
	clear(entries[:cap(entries)])
	clear(indices)
	dictTable16Pool.mu.Lock()
	if len(dictTable16Pool.free) < 64 {
		dictTable16Pool.free = append(dictTable16Pool.free, dictTable16Slab{entries: entries[:0], indices: indices})
	}
	dictTable16Pool.mu.Unlock()
}

// UnhandledException returns the uncaught Python exception from the most recent execution, or NoneVal.
func (vm *VM) UnhandledException() val.Value {
	if vm.unhandledException.HeaderPtr() == nil {
		return val.NoneVal()
	}
	return vm.unhandledException
}

// ClearUnhandledException resets the unhandled exception state on the VM.
func (vm *VM) ClearUnhandledException() {
	vm.unhandledException = val.NoneVal()
}

// LastException returns the most recent unhandled exception Value, or NoneVal.
func (vm *VM) LastException() val.Value {
	if vm.lastException.HeaderPtr() == nil {
		return val.NoneVal()
	}
	val.ClearEphemeral(vm.lastException)
	return vm.lastException
}

// SetLastException sets the most recent exception.
func (vm *VM) SetLastException(exc val.Value) {
	vm.lastException = exc
}

var (
	defaultEngine     *Engine
	defaultEngineOnce sync.Once
)

// DefaultEngine returns the shared default Engine singleton.
func DefaultEngine() *Engine {
	defaultEngineOnce.Do(func() {
		defaultEngine = NewEngine()
	})
	return defaultEngine
}

// NewVM allocates a new VM with default contiguous stack (4096 Values), frame (256 CallFrames),
// and inline cache (4096 ICSlots) slabs attached to the default Engine.
func NewVM() *VM {
	return DefaultEngine().NewVM()
}

// NewVMWithCapacity allocates a new VM with custom stack and frame slab sizes attached to the default Engine.
func NewVMWithCapacity(stackCap, frameCap int) *VM {
	return DefaultEngine().NewVMWithCapacity(stackCap, frameCap)
}

// Globals returns the VM's active module globals dictionary.
func (vm *VM) Globals() *val.PyDict {
	return vm.globals
}

// Builtins returns the VM's builtins dictionary.
func (vm *VM) Builtins() *val.PyDict {
	return vm.builtins
}

// Modules returns the VM's active sys.modules dictionary.
func (vm *VM) Modules() *val.PyDict {
	return vm.modules
}

// SetGlobal assigns a variable in the VM's module globals dictionary.
func (vm *VM) SetGlobal(name string, v val.Value) {
	vm.globals.Set(val.InternString(name), v)
}

// GetGlobal resolves a variable from the VM's module globals dictionary.
func (vm *VM) GetGlobal(name string) (val.Value, bool) {
	return vm.globals.Get(val.InternString(name))
}

// Intern canonicalizes an identifier, attribute name, or string constant in the VM's intern pool.
// Delegates to the process-wide val.InternString pool so VM-interned and type-interned identifiers
// share identical canonical pointers.
func (vm *VM) Intern(s string) val.Value {
	return val.InternString(s)
}

// InternValue canonicalizes a string Value in the canonical intern pool.
func (vm *VM) InternValue(v val.Value) val.Value {
	return val.InternValue(v)
}

// computeParamShape derives the precomputed argument-binding shape of the
// code object.
func (co *CodeObject) computeParamShape() {
	if co == nil || co.ParamShapeComputed {
		return
	}
	co.ParamShapeComputed = true
	co.PositionalParamCount = 0
	co.VarArgsIdx = -1
	co.VarKWIdx = -1
	posCount := 0
	simple := co.KWOnlyArgCount == 0 && co.Flags&(CO_VARARGS|CO_VARKEYWORDS) == 0
	for p, pn := range co.ParamNames {
		if !pn.IsAnyString() {
			simple = false
			continue
		}
		switch {
		case pn.StringStartsWithTwoBytes('*', '*'):
			co.VarKWIdx = p
			simple = false
		case pn.StringHasBytePrefix('*'):
			simple = false
			if !pn.StringEquals("*") {
				co.VarArgsIdx = p
			}
		default:
			posCount++
			if simple {
				co.PositionalParamCount++
			}
		}
	}
	if len(co.ParamNames) == 0 && co.NumParams > 0 {
		posCount = co.NumParams
	}
	posCount -= co.KWOnlyArgCount
	if posCount < 0 {
		posCount = 0
	}
	co.PosParamCount = posCount
	co.SimplePositionalOnly = simple && co.PositionalParamCount == len(co.ParamNames)
	co.EffectiveLocals = max(co.NumLocals, co.NumParams)
	co.FrameStackNeed = max(256, co.NumLocals+64)
	co.HasCellVars = len(co.CellVars) > 0
	co.HasCells = co.HasCellVars || len(co.FreeVars) > 0
	co.ClassCellIdx = -1
	for i, cv := range co.CellVars {
		if cv.StringEquals("__class__") {
			co.ClassCellIdx = int16(i)
			break
		}
	}
	if co.ClassCellIdx < 0 {
		for i, fv := range co.FreeVars {
			if fv.StringEquals("__class__") {
				co.ClassCellIdx = int16(len(co.CellVars) + i)
				break
			}
		}
	}
	co.VarKWSafe = computeVarKWSafe(co)
	co.MapTempSafe = computeLocalDictSafe(co)
	co.MatchLocalSafe = computeLocalMatchSafe(co)
}

func (co *CodeObject) isMatchLocalSafe(r int) bool {
	return uint(r) < 256 && (co.MatchLocalSafe[r>>6]&(uint64(1)<<uint(r&63))) != 0
}

// computeLocalMatchSafe conservatively marks local slots (0..255) that are only
// ever consumed by truthiness tests or non-escaping Match methods/attributes
// (group, groups, groupdict, start, end, span, lastgroup, lastindex, pos, endpos).
// Used to safely recycle loop-carried ephemeral pyMatch objects on overwrite.
func computeLocalMatchSafe(co *CodeObject) [4]uint64 {
	var mask [4]uint64
	if co.IsGenerator || co.IsCoroutine || co.HasCells || len(co.Code) == 0 || co.NumLocals <= 0 {
		return mask
	}
	n := len(co.Code)
	limitReg := min(co.NumLocals, 256)
	var candidate [4]uint64
	// Start with all non-parameter local slots as candidates, then eliminate any
	// slot that has an unsafe load, store, or closure/attr escape.
	for r := co.NumParams; r < limitReg; r++ {
		candidate[r>>6] |= uint64(1) << uint(r&63)
	}
	clearBit := func(r int) {
		if uint(r) < 256 {
			candidate[r>>6] &^= uint64(1) << uint(r&63)
		}
	}
	hasBit := func(r int) bool {
		return uint(r) < 256 && (candidate[r>>6]&(uint64(1)<<uint(r&63))) != 0
	}
	for i := range n {
		inst := co.Code[i]
		op := DecodeOpcode(inst)
		switch op {
		case OP_LOAD_FAST:
			r := int(DecodeArgBx(inst))
			if !hasBit(r) {
				continue
			}
			if i+1 >= n {
				clearBit(r)
				continue
			}
			w1 := co.Code[i+1]
			op1 := DecodeOpcode(w1)
			switch op1 {
			case OP_JUMP_IF_FALSE, OP_JUMP_IF_TRUE, OP_UNARY_NOT, OP_POP_TOP:
				continue
			case OP_LOAD_ATTR:
				cIdx := int(DecodeArgBx(w1))
				if cIdx < len(co.Consts) {
					name := co.Consts[cIdx]
					if name.StringEquals("lastgroup") || name.StringEquals("lastindex") ||
						name.StringEquals("pos") || name.StringEquals("endpos") {
						continue
					}
				}
				clearBit(r)
			case OP_LOAD_METHOD:
				// Verify method is a non-escaping Match accessor.
				cIdx := int(DecodeArgBx(w1))
				if cIdx < len(co.Consts) {
					name := co.Consts[cIdx]
					if name.StringEquals("group") || name.StringEquals("groups") ||
						name.StringEquals("groupdict") || name.StringEquals("start") ||
						name.StringEquals("end") || name.StringEquals("span") {
						continue
					}
				}
				clearBit(r)
			default:
				// Check for method call pattern: LOAD_FAST r; <0..3 simple args>; CALL_METHOD_PIC/DESCR
				safe := false
				depth := 0
				scanLim := min(n, i+6)
				for k := i + 1; k < scanLim; k++ {
					wk := co.Code[k]
					opk := DecodeOpcode(wk)
					if opk == OP_CALL_METHOD_PIC || opk == OP_CALL_METHOD_DESCR || opk == OP_CALL_METHOD_DESCR_FAST {
						if depth == int(DecodeArgA(wk)) {
							mIdx := int(DecodeArgBx(wk))
							if mIdx < len(co.Consts) {
								mName := co.Consts[mIdx]
								if mName.StringEquals("group") || mName.StringEquals("groups") ||
									mName.StringEquals("groupdict") || mName.StringEquals("start") ||
									mName.StringEquals("end") || mName.StringEquals("span") {
									safe = true
								}
							}
						}
						break
					}
					switch opk {
					case OP_LOAD_FAST, OP_LOAD_CONST, OP_LOAD_GLOBAL, OP_LOAD_DEREF:
						depth++
					default:
						k = scanLim
					}
				}
				if !safe {
					clearBit(r)
				}
			}
		case OP_LOAD_FAST_ATTR:
			r := int(DecodeArgA(inst))
			if !hasBit(r) {
				continue
			}
			cIdx := int(DecodeArgBx(inst))
			if cIdx < len(co.Consts) {
				name := co.Consts[cIdx]
				if name.StringEquals("lastgroup") || name.StringEquals("lastindex") ||
					name.StringEquals("pos") || name.StringEquals("endpos") {
					continue
				}
			}
			clearBit(r)
		case OP_STORE_FAST_ATTR, OP_LOAD_FAST_ATTR_MUL_SELF:
			clearBit(int(DecodeArgA(inst)))
		case OP_COMPARE_FAST_FAST, OP_SUBSCR_FAST_FAST, OP_FAST_ADD_INT_LOCALS:
			clearBit(int(uint8(inst >> 8)))
			clearBit(int(uint8(inst)))
		case OP_SUBSCR_FAST_CONST:
			clearBit(int(uint8(inst >> 8)))
		}
	}
	return candidate
}

func isSafeScalarKeyConst(v val.Value) bool {
	return v.IsInlineStr() ||
		(v.IsString() && v.HeaderPtr().Type == val.StrType) ||
		v.IsInt64()
}

func computeVarKWSafe(co *CodeObject) bool {
	if co.VarKWIdx < 0 || co.VarKWIdx >= 256 || co.IsGenerator || co.IsCoroutine || co.HasCells || len(co.Code) == 0 {
		return false
	}
	n := len(co.Code)
	isJumpTarget := make([]bool, n+1)
	for _, inst := range co.Code {
		switch DecodeOpcode(inst) {
		case OP_JUMP, OP_JUMP_IF_FALSE, OP_JUMP_IF_TRUE, OP_JUMP_IF_FALSE_OR_POP, OP_JUMP_IF_TRUE_OR_POP, OP_FOR_ITER, OP_FOR_ITER_GEN, OP_FOR_ITER_RANGE, OP_COMPARE_JUMP_IF_FALSE, OP_COMPARE_JUMP_IF_TRUE:
			t := int(DecodeArgBx(inst))
			if t >= 0 && t < len(isJumpTarget) {
				isJumpTarget[t] = true
			}
		}
	}
	for _, e := range co.ExceptionTable {
		if e.StartIP >= 0 && e.StartIP < len(isJumpTarget) {
			isJumpTarget[e.StartIP] = true
		}
		if e.EndIP >= 0 && e.EndIP < len(isJumpTarget) {
			isJumpTarget[e.EndIP] = true
		}
		if e.TargetIP >= 0 && e.TargetIP < len(isJumpTarget) {
			isJumpTarget[e.TargetIP] = true
		}
	}
	kwIdx := uint16(co.VarKWIdx)
	for i := range n {
		inst := co.Code[i]
		op := DecodeOpcode(inst)
		bx := DecodeArgBx(inst)
		switch op {
		case OP_STORE_FAST, OP_DELETE_FAST, OP_RETURN_FAST:
			if bx == kwIdx {
				return false
			}
		case OP_LOAD_FAST_2, OP_STORE_LOAD_FAST:
			if uint16(uint8(inst>>8)) == kwIdx || uint16(uint8(inst)) == kwIdx {
				return false
			}
		case OP_LOAD_FAST_ATTR, OP_STORE_FAST_ATTR, OP_LOAD_FAST_ATTR_MUL_SELF:
			if uint16(DecodeArgA(inst)) == kwIdx {
				return false
			}
		case OP_SUBSCR_FAST_CONST:
			if uint16(uint8(inst>>8)) == kwIdx {
				cIdx := int(bx)
				if cIdx >= len(co.Consts) || !isSafeScalarKeyConst(co.Consts[cIdx]) {
					return false
				}
			}
		case OP_LOAD_FAST:
			if bx != kwIdx {
				continue
			}
			if i+1 >= n || isJumpTarget[i+1] {
				return false
			}
			w1 := co.Code[i+1]
			op1 := DecodeOpcode(w1)
			switch op1 {
			case OP_JUMP_IF_FALSE, OP_JUMP_IF_TRUE, OP_POP_TOP, OP_UNARY_NOT:
				continue
			case OP_LOAD_CONST:
				cIdx := int(DecodeArgBx(w1))
				if i+2 < n && !isJumpTarget[i+2] && DecodeOpcode(co.Code[i+2]) == OP_BINARY_SUBSCR &&
					cIdx < len(co.Consts) && isSafeScalarKeyConst(co.Consts[cIdx]) {
					continue
				}
			case OP_COMPARE_OP:
				cmpOp := DecodeArgA(w1)
				if (cmpOp == CMP_IN || cmpOp == CMP_NOT_IN) && i > 0 && !isJumpTarget[i] &&
					DecodeOpcode(co.Code[i-1]) == OP_LOAD_CONST {
					cIdx := int(DecodeArgBx(co.Code[i-1]))
					if cIdx < len(co.Consts) && isSafeScalarKeyConst(co.Consts[cIdx]) {
						continue
					}
				}
			case OP_CALL_FUNCTION_EX:
				if DecodeArgA(w1) == 1 {
					continue
				}
				return false
			case OP_BUILD_MAP_UNPACK:
				if i+2 < n && !isJumpTarget[i+2] && DecodeOpcode(co.Code[i+2]) == OP_CALL_FUNCTION_EX && DecodeArgA(co.Code[i+2]) == 1 {
					continue
				}
				return false
			}
			if i > 0 && !isJumpTarget[i] && DecodeOpcode(co.Code[i-1]) == OP_LOAD_GLOBAL {
				gIdx := int(DecodeArgBx(co.Code[i-1]))
				if gIdx < len(co.Consts) && co.Consts[gIdx].StringEquals("len") && op1 == OP_CALL && DecodeArgA(w1) == 1 {
					continue
				}
			}
			depth := 0
			safe := false
			limit := min(n, i+12)
			for k := i + 1; k < limit; k++ {
				if isJumpTarget[k] {
					break
				}
				wk := co.Code[k]
				opk := DecodeOpcode(wk)
				if opk == OP_CALL_METHOD_PIC || opk == OP_CALL_METHOD_DESCR || opk == OP_CALL_METHOD_DESCR_FAST {
					argc := int(DecodeArgA(wk))
					if depth == argc {
						mIdx := int(DecodeArgBx(wk))
						if mIdx < len(co.Consts) {
							mName := co.Consts[mIdx]
							if mName.StringEquals("get") || mName.StringEquals("pop") || mName.StringEquals("__contains__") || mName.StringEquals("__len__") {
								safe = true
							}
						}
					}
					break
				}
				switch opk {
				case OP_LOAD_FAST, OP_LOAD_CONST, OP_LOAD_GLOBAL, OP_LOAD_DEREF:
					depth++
				default:
					k = limit
				}
			}
			if !safe {
				return false
			}
		}
	}
	return true
}

// computeLocalDictSafe marks BUILD_MAP sites whose result flows through a
// single fast-local slot into a single releasing kwargs consumer. A site ip
// qualifies only when the map holds at most 8 entries, ip+1 stores the fresh
// dict into slot r exactly once, r is loaded exactly once by an instruction
// that directly feeds OP_CALL_FUNCTION_EX with kwargs, the whole window is
// free of jump targets, and no exception range overlaps the window. Consumers
// already return pooled dicts to the freelist, so no new release sites are
// needed; early exits simply leave the flagged dict for collection.
func computeLocalDictSafe(co *CodeObject) []bool {
	if co.IsGenerator || co.IsCoroutine || co.HasCells || len(co.Code) == 0 {
		return nil
	}
	n := len(co.Code)
	isJumpTarget := make([]bool, n+1)
	for _, inst := range co.Code {
		switch DecodeOpcode(inst) {
		case OP_JUMP, OP_JUMP_IF_FALSE, OP_JUMP_IF_TRUE, OP_JUMP_IF_FALSE_OR_POP, OP_JUMP_IF_TRUE_OR_POP, OP_FOR_ITER, OP_FOR_ITER_GEN, OP_FOR_ITER_RANGE, OP_COMPARE_JUMP_IF_FALSE, OP_COMPARE_JUMP_IF_TRUE:
			t := int(DecodeArgBx(inst))
			if t >= 0 && t < len(isJumpTarget) {
				isJumpTarget[t] = true
			}
		}
	}
	for _, e := range co.ExceptionTable {
		if e.StartIP >= 0 && e.StartIP < len(isJumpTarget) {
			isJumpTarget[e.StartIP] = true
		}
		if e.EndIP >= 0 && e.EndIP < len(isJumpTarget) {
			isJumpTarget[e.EndIP] = true
		}
		if e.TargetIP >= 0 && e.TargetIP < len(isJumpTarget) {
			isJumpTarget[e.TargetIP] = true
		}
	}
	const maxReg = 256
	storeCount := make([]int, maxReg)
	loadCount := make([]int, maxReg)
	loadPos := make([]int, maxReg)
	for i, inst := range co.Code {
		switch DecodeOpcode(inst) {
		case OP_STORE_FAST, OP_DELETE_FAST:
			if r := int(DecodeArgBx(inst)); r < maxReg {
				storeCount[r]++
			}
		case OP_LOAD_FAST:
			if r := int(DecodeArgBx(inst)); r < maxReg {
				loadCount[r]++
				loadPos[r] = i
			}
		}
	}
	var safe []bool
	for ip, inst := range co.Code {
		op := DecodeOpcode(inst)
		if op != OP_BUILD_MAP && op != OP_BUILD_CONST_KEY_MAP {
			continue
		}
		if int(uint16(inst)) > 16 {
			continue
		}
		if ip+1 >= n || DecodeOpcode(co.Code[ip+1]) != OP_STORE_FAST {
			continue
		}
		r := int(DecodeArgBx(co.Code[ip+1]))
		if r >= maxReg || storeCount[r] != 1 || loadCount[r] != 1 {
			continue
		}
		j := loadPos[r]
		if j <= ip+1 || j+1 >= n {
			continue
		}
		if DecodeOpcode(co.Code[j+1]) != OP_CALL_FUNCTION_EX || DecodeArgA(co.Code[j+1]) != 1 {
			continue
		}
		end := j + 1
		blocked := false
		for k := ip + 1; k <= end; k++ {
			if isJumpTarget[k] {
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}
		for _, e := range co.ExceptionTable {
			if e.StartIP < end+1 && e.EndIP > ip {
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}
		if safe == nil {
			safe = make([]bool, n)
		}
		safe[ip] = true
	}
	return safe
}
