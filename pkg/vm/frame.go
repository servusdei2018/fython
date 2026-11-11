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
	DefaultStackSize = 65536
	// DefaultFrameSlabSize is the default pre-allocated CallFrame slab capacity per VM instance.
	DefaultFrameSlabSize = 256
	// DefaultICSlabSize is the initial Inline Cache side-table capacity per
	// VM instance. The slabs grow on demand, so a small initial size keeps
	// the resident set of the interpreter low.
	DefaultICSlabSize = 4096
)

// ICKind identifies the cached resolution category in an ICSlot.
type ICKind uint8

const (
	ICKindNone           ICKind = iota
	ICKindInstanceSlot          // Hit in PyInstance.Slots[SlotIndex]
	ICKindClassMethod           // Hit on PyType MRO method (CachedVal holds raw callable)
	ICKindDataDescriptor        // Hit on PyType MRO data descriptor (CachedVal holds descriptor)
	ICKindClassAttr             // Hit on PyType MRO non-descriptor class attribute
	ICKindGlobal                // Hit on module globals dict
	ICKindBuiltin               // Hit on builtins dict fallback
	ICKindModuleAttr            // Hit on module dict
)

// AttrPICEntry represents a single slot in a 4-way polymorphic inline cache for attribute access.
type AttrPICEntry struct {
	Shape     *val.Shape
	SlotIndex int
}

// ICSlot caches [ExpectedShapeID, ExpectedTypeVer, SlotIndex, CachedVal] for O(1)
// zero-allocation attribute and method lookup in OP_LOAD_ATTR, OP_STORE_ATTR, and OP_LOAD_METHOD.
// Also caches [ExpectedGlobalsVer, ExpectedBuiltinsVer, CachedVal] for OP_LOAD_GLOBAL.
type ICSlot struct {
	ExpectedShapeID     uint32
	Kind                ICKind
	SlotIndex           int
	ExpectedTypeVer     uint64
	CodePtr             *uint32
	CachedVal           val.Value
	ExpectedGlobalsVer  uint64
	ExpectedBuiltinsVer uint64
}

// ExceptionEntry records a protected instruction range and handler target in a static exception table.
type ExceptionEntry struct {
	StartIP          int
	EndIP            int
	TargetIP         int
	TargetStackDepth int
	PushLasti        bool
}

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
	// SimplePositionalOnly reports that every parameter of the code object is
	// a required positional-only parameter: no keyword-only parameter, no
	// *args and no **kwargs. PositionalParamCount is then the exact number of
	// positional parameters, which lets a call skip argument binding.
	SimplePositionalOnly bool
	PositionalParamCount int
	ParamShapeComputed   bool
	// stream publishes the instruction slice the VM executes. It is created
	// together with the code object, before the object is shared between VMs.
	stream *codeStream
	// StreamGen counts the publications of the instruction stream. A VM uses
	// it to notice that a newer stream is available without having to load
	// the stream itself on every frame switch.
	StreamGen int32
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

// refreshCode returns the instruction slice that must be executed for co.
//
// The stream is only reloaded when the Tier-2 optimizer published a newer
// one; the generation counter makes that check a single atomic load.
func refreshCode(co *CodeObject, cache *VMCodeCache) []uint32 {
	if gen := atomic.LoadInt32(&co.StreamGen); gen != cache.streamGen {
		return reloadCode(co, cache, gen)
	}
	return cache.active
}

// reloadCode installs the instruction stream that the code object published.
//
//go:noinline
func reloadCode(co *CodeObject, cache *VMCodeCache, gen int32) []uint32 {
	cache.streamGen = gen
	cache.active = co.Active()
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
	ic        []ICSlot
	methodPIC [][4]val.MethodPICEntry
	attrPIC   [][4]AttrPICEntry
	// hotness counts how often this VM executed the code object. It is
	// VM-local, so it never shares a cache line with another VM.
	hotness uint32
	// active is the instruction stream this VM loaded for the code object,
	// and streamGen the generation it belongs to. The pair lets the VM pick
	// up a stream published by the Tier-2 optimizer without loading it on
	// every frame switch.
	active    []uint32
	streamGen int32
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
		nameStr := name.AsString()
		for p, pn := range co.ParamNames {
			if val.StringEquals(pn, name) {
				indices[i] = p
				break
			}
			pnStr := pn.AsString()
			if len(pnStr) > 1 && pnStr[0] == '*' {
				clean := pnStr[1:]
				if len(clean) > 0 && clean[0] == '*' {
					clean = clean[1:]
				}
				if clean == nameStr {
					indices[i] = p
					break
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
	engine             *Engine
	stack              []val.Value
	frames             []CallFrame
	codeCaches         []VMCodeCache
	builtinRange       val.Value
	builtinLen         val.Value
	builtinOpen        val.Value
	strScratch         []byte
	internPool         *val.InternPool
	execCtx            val.ExecContext
	activeFrameIdx     int
	activeCO           *CodeObject
	activeBaseOffset   int
	activeCells        []val.Value
	activeCellPtrs     []*val.PyCell
	kwScratch          [64]val.Value
	kwSet              [64]bool
	globals            *val.PyDict
	builtins           *val.PyDict
	modules            *val.PyDict
	lastException      val.Value
	unhandledException val.Value
	excStack           []val.Value
	context            *val.PyContext
	bindingOSPath      bool
	coroFreeList       []*val.PyCoroutine
	// mvFree holds the memory views that are no longer referenced and can
	// be handed out again by the next slice.
	mvFree       []*val.PyMemoryView
	rootCO       CodeObject
	rootCache    VMCodeCache
	limits       RuntimeLimits
	limitsActive bool
	stepCount    uint64
	eventLoop    *EventLoop
}

// loadWindow returns the instruction window of co as a base pointer and a
// length. The window is refreshed when the Tier-2 optimizer published a newer
// instruction stream for the code object.
func (vm *VM) loadWindow(co *CodeObject, cache *VMCodeCache) (unsafe.Pointer, int) {
	stream := refreshCode(co, cache)
	return unsafe.Pointer(unsafe.SliceData(stream)), len(stream)
}

func (vm *VM) getCodeCache(co *CodeObject) *VMCodeCache {
	if co == nil || co == &vm.rootCO {
		// The root slabs are only used by the low-level Run() API, so they
		// are allocated on demand instead of for every VM.
		cache := &vm.rootCache
		need := DefaultICSlabSize
		if co != nil && len(co.Code) > need {
			need = len(co.Code)
		}
		if len(cache.ic) < need {
			cache.ic = make([]ICSlot, need)
			cache.methodPIC = make([][4]val.MethodPICEntry, need)
			cache.attrPIC = make([][4]AttrPICEntry, need)
		}
		if co != nil {
			// The root code object is recreated for every Run() call, so its
			// instruction stream is always fetched anew.
			cache.streamGen = atomic.LoadInt32(&co.StreamGen)
			cache.active = co.Active()
		}
		return cache
	}
	id := int(co.initID())
	if id >= len(vm.codeCaches) {
		newCaches := make([]VMCodeCache, id+16)
		copy(newCaches, vm.codeCaches)
		vm.codeCaches = newCaches
	}
	cache := &vm.codeCaches[id]
	if cache.active == nil {
		// The generation is read before the stream, so a publication that
		// happens in between is picked up by the next refresh.
		cache.streamGen = atomic.LoadInt32(&co.StreamGen)
		cache.active = co.Active()
	}
	if len(cache.ic) < len(co.Code) || len(cache.methodPIC) < len(co.Code) || len(cache.attrPIC) < len(co.Code) {
		cache.ic = make([]ICSlot, len(co.Code))
		cache.methodPIC = make([][4]val.MethodPICEntry, len(co.Code))
		cache.attrPIC = make([][4]AttrPICEntry, len(co.Code))
	}
	return cache
}

// Engine returns the parent Engine managing this VM.
func (vm *VM) Engine() *Engine {
	return vm.engine
}

// Reset clears the active stack and execution frame state for reuse in Engine's sync.Pool.
func (vm *VM) Reset() {
	sp := min(vm.execCtx.SP, len(vm.stack))
	for i := range sp {
		vm.stack[i] = val.NoneVal()
	}
	for i := range vm.frames {
		vm.frames[i].shadowFrame = nil
	}
	vm.execCtx.SP = 0
	vm.execCtx.Stack = vm.stack
	vm.execCtx.ClearException()
	vm.activeFrameIdx = 0
	vm.activeCO = nil
	vm.activeBaseOffset = 0
	vm.activeCells = nil
	vm.activeCellPtrs = nil
	vm.lastException = val.NoneVal()
	vm.unhandledException = val.NoneVal()
	vm.context = val.NewPyContext()
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
	fSlot := &vm.frames[targetIdx]
	if fSlot.shadowFrame != nil {
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
	val.AcquireExecContextHook = func() (*val.ExecContext, func()) {
		v := standaloneVMPool.Get().(*VM)
		v.Reset()
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
	simple := co.KWOnlyArgCount == 0 && co.Flags&(CO_VARARGS|CO_VARKEYWORDS) == 0
	for _, pn := range co.ParamNames {
		if !pn.IsAnyString() {
			simple = false
			break
		}
		name := pn.AsString()
		if len(name) > 0 && name[0] == '*' {
			simple = false
			break
		}
		co.PositionalParamCount++
	}
	co.SimplePositionalOnly = simple && co.PositionalParamCount == len(co.ParamNames)
}
