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
	// DefaultICSlabSize is the default pre-allocated Inline Cache side-table capacity per VM instance.
	DefaultICSlabSize = 65536
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
	code        []uint32
	consts      []val.Value
	ip          int
	baseOffset  int
	retSP       int // Caller's stack pointer where the return value will be written
	cells       []val.Value
	globals     *val.PyDict
	locals      val.Value
	cache       *VMCodeCache
	shadowFrame *val.PyFrame
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
	NumLocals        int
	NumParams        int
	MaxStackSize     int
	IsGenerator      bool
	IsCoroutine      bool
	Flags            uint32
	ExceptionTable   []ExceptionEntry
	LineSpans        []LineSpanEntry
	Hotness          int32
	IsTier2Optimized int32
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
}

// Compile-time check that CodeObject.Header is at byte offset 0.
var _ [0 - unsafe.Offsetof(CodeObject{}.Header)]byte

// NewCodeVal wraps a *CodeObject as a val.Value with KindCode.
func NewCodeVal(co *CodeObject) val.Value {
	co.initID()
	co.Header.Kind = val.KindCode
	co.Header.Type = val.CodeType
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
	ic                 []ICSlot
	methodPIC          [][4]val.MethodPICEntry
	attrPIC            [][4]AttrPICEntry
	codeCaches         []VMCodeCache
	builtinRange       val.Value
	internPool         *val.InternPool
	execCtx            val.ExecContext
	activeFrameIdx     int
	activeCO           *CodeObject
	activeBaseOffset   int
	activeCells        []val.Value
	kwScratch          [64]val.Value
	kwSet              [64]bool
	globals            *val.PyDict
	builtins           *val.PyDict
	modules            *val.PyDict
	lastException      val.Value
	unhandledException val.Value
	context            *val.PyContext
	bindingOSPath      bool
	coroFreeList       []*val.PyCoroutine
	rootCO             CodeObject
	rootCache          VMCodeCache
}

func (vm *VM) getCodeCache(co *CodeObject) *VMCodeCache {
	if co == nil || co == &vm.rootCO {
		return &vm.rootCache
	}
	id := int(co.initID())
	if id >= len(vm.codeCaches) {
		newCaches := make([]VMCodeCache, id+16)
		copy(newCaches, vm.codeCaches)
		vm.codeCaches = newCaches
	}
	cache := &vm.codeCaches[id]
	if len(cache.ic) < len(co.Code) {
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
	sp := vm.execCtx.SP
	if sp > len(vm.stack) {
		sp = len(vm.stack)
	}
	for i := 0; i < sp; i++ {
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
	if len(co.VarNames) > 0 {
		return co.VarNames
	}
	return co.ParamNames
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
	for _, p := range co.ParamNames {
		s := p.AsString()
		if strings.HasPrefix(s, "*") {
			break
		}
		argCount++
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
