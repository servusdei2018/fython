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
	ICKindSuperMethod               // super(ThisClass, Self) MRO resolution guarded by selfType + ThisClass versions
	ICKindPropertySet               // @property setter (CachedVal holds fset) dispatched without descriptor machinery
	ICKindInstanceTransition        // Shape-transitioning attribute store (OldShape -> NewShape via transitionIC)
	ICKindCompareDunder             // Quickened comparison dunder (CachedVal=*PyFunction, CalleeCache=*VMCodeCache, ExpectedTypeVer=left, ExpectedAuxVer=right, SlotIndex=mode)
)

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

// KwPICEntry caches a monomorphic/polymorphic OP_CALL_KW parameter binding plan so
// keyword calls write directly into callee frame slots with 0 heap allocations.
type KwPICEntry struct {
	TargetCo      *CodeObject
	TargetFn      *val.PyFunction
	TargetType    *val.PyType
	TypeVersion   uint64
	DefaultsLen   int
	HasKwDefaults bool
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
	genModeNext        // next(gen): raise StopIteration(retVal) on exhaustion
	genModeNextDefault // next(gen, default): return genDefault on exhaustion
	genModeSend        // gen.send(v): raise StopIteration(retVal) on exhaustion
	genModeAwait       // await aw: store retVal at stack[retSP-1] on completion
	genModeCoroSend    // coro.send(v): store retVal at stack[retSP] on completion
)

// CallFrame is an unboxed value struct stored in the contiguous VM.frames slab.
// It is never heap-allocated per function call.
type CallFrame struct {
	co          *CodeObject
	codePtr     unsafe.Pointer
	codeLen     int
	consts      []val.Value
	ip          int
	baseOffset  int
	retSP       int // Caller's stack pointer where the return value will be written
	cells       []val.Value
	cellPtrs    []*val.PyCell
	globals     *val.PyDict
	locals      val.Value
	cache       *VMCodeCache
	shadowFrame *val.PyFrame
	excStackLen int
	stackIsVM   bool
	// Inline generator/coroutine linkage. When genMode != genModeNone, the child
	// frame is executing on its own stack slice, and stack holds the caller's
	// stack slice to restore on yield/suspend/return/unwind.
	stack       []val.Value
	activeGen   *val.PyGenerator
	activeCoro  *val.PyCoroutine
	genFrameIdx int
	genMode     uint8
	genDefault  val.Value
	genExitIP   int
	// initInstance holds the freshly allocated receiver for an in-loop
	// __init__ call. When set, OP_RETURN substitutes it for a None
	// return and raises TypeError otherwise.
	initInstance val.Value
	// cmpLeft/cmpRight/cmpOp record an in-loop dunder comparison so
	// OP_RETURN can fall back to the generic comparison when the dunder
	// answers NotImplemented. cmpActive marks the record live.
	cmpLeft   val.Value
	cmpRight  val.Value
	cmpOp     uint8
	cmpActive bool
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
	EffectiveLocals      int
	FrameStackNeed       int
	HasCells             bool
	HasCellVars          bool
	ParamShapeComputed   bool
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
			case OP_CALL_PY_EXACT_ARGS, OP_CALL_BUILTIN_FAST, OP_CALL_LEN, OP_CALL_ISINSTANCE, OP_CALL_TYPE_INIT:
				if DecodeOpcode(priv[i]) == OP_CALL {
					priv[i] = oldInst
				}
			case OP_CALL_METHOD_DESCR:
				if DecodeOpcode(priv[i]) == OP_CALL_METHOD_PIC {
					priv[i] = oldInst
				}
			case OP_FOR_ITER_GEN:
				if DecodeOpcode(priv[i]) == OP_FOR_ITER {
					priv[i] = oldInst
				}
			case OP_COMPARE_DUNDER:
				if op := DecodeOpcode(priv[i]); op == OP_COMPARE_OP || op == OP_COMPARE_LT {
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

// VMCodeCache holds per-CodeObject inline caches isolated per VM instance (0 races, 0 cross-call thrashing).
type VMCodeCache struct {
	// owner is the VM this cache belongs to. It lets a code object publish
	// its cache without carrying a VM pointer of its own.
	owner        *VM
	co           *CodeObject
	ic           []ICSlot
	methodPIC    [][4]val.MethodPICEntry
	attrPIC      [][4]AttrPICEntry
	transitionIC []TransitionICEntry
	kwPIC        [][2]KwPICEntry
	// hotness counts how often this VM executed the code object. It is
	// VM-local, so it never shares a cache line with another VM.
	hotness uint32
	// active is the instruction stream this VM loaded for the code object,
	// and streamGen the generation it belongs to. The pair lets the VM pick
	// up a stream published by the Tier-2 optimizer without loading it on
	// every frame switch.
	active        []uint32
	codePtr       unsafe.Pointer
	codeLen       int
	streamGen     int32
	privateActive bool
}

func (c *VMCodeCache) ensureMethodPIC() [][4]val.MethodPICEntry {
	if len(c.methodPIC) < len(c.ic) {
		c.methodPIC = make([][4]val.MethodPICEntry, len(c.ic))
	}
	return c.methodPIC
}

func (c *VMCodeCache) ensureAttrPIC() [][4]AttrPICEntry {
	if len(c.attrPIC) < len(c.ic) {
		c.attrPIC = make([][4]AttrPICEntry, len(c.ic))
	}
	return c.attrPIC
}

func (c *VMCodeCache) ensureTransitionIC() []TransitionICEntry {
	if len(c.transitionIC) < len(c.ic) {
		c.transitionIC = make([]TransitionICEntry, len(c.ic))
	}
	return c.transitionIC
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

// VM represents a single execution thread with contiguous Value, CallFrame, and ICSlot slabs,
// plus a VM-managed string interning pool and zero-allocation Vectorcall execution context.
type VM struct {
	engine               *Engine
	stack                []val.Value
	frames               []CallFrame
	codeCaches           []*VMCodeCache
	builtinRange         val.Value
	builtinLen           val.Value
	builtinOpen          val.Value
	builtinIsInstance    val.Value
	builtinNext          val.Value
	strScratch           []byte
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
	context              *val.PyContext
	bindingOSPath        bool
	pendingVirtualenvPth bool
	coroFreeList         []*val.PyCoroutine
	genFreeList          []*val.PyGenerator
	iterFreeList         []*val.PyIter
	loopStatePool        []*loopState
	genStackPools        [4][][]val.Value
	genCellValPools      [8][][]val.Value
	genCellPtrPools      [8][][]*val.PyCell
	genResumeHook        func(ctx *val.ExecContext, gen *val.PyGenerator, val val.Value) (val.Value, val.IterStatus)
	genReleaseHook       func(gen *val.PyGenerator)
	coroResumeHook       func(ctx *val.ExecContext, coro *val.PyCoroutine, val val.Value) (val.Value, val.IterStatus)
	coroReleaseHook      func(coro *val.PyCoroutine)
	jsonParser           *jsonParser
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

var genStackSizes = [4]int{256, 512, 1024, 2048}

func (vm *VM) acquireGeneratorStack(minSize int) ([]val.Value, uint8) {
	if minSize <= 256 {
		minSize = 256
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
	it.NextFn = nil
	it.Idx = 0
	it.Cur = 0
	it.Stop = 0
	it.Step = 0
	if len(vm.iterFreeList) < 128 {
		vm.iterFreeList = append(vm.iterFreeList, it)
	}
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

func (vm *VM) materializeFrame(targetIdx int) val.Value {
	if targetIdx < 0 || targetIdx >= len(vm.frames) {
		return val.NoneVal()
	}
	if vm.activeState != nil && targetIdx == vm.activeState.frameIdx {
		vm.syncActiveFrame(vm.activeState, vm.activeState.ip)
	}
	fSlot := &vm.frames[targetIdx]
	if fSlot.shadowFrame != nil {
		fSlot.shadowFrame.IP = fSlot.ip
		return val.NewFrameVal(fSlot.shadowFrame)
	}
	co := fSlot.co
	baseOffset := fSlot.baseOffset
	cells := fSlot.cells
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
	globals := fSlot.globals
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
	pyFrame.IP = fSlot.ip
	if fSlot.locals.IsDict() {
		pyFrame.ExtraLocals = fSlot.locals.AsDict()
	}
	fSlot.shadowFrame = pyFrame
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
}
