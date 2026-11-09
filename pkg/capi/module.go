package capi

import (
	"unsafe"

	"github.com/servusdei2018/fython/pkg/dynload"
	"github.com/servusdei2018/fython/pkg/val"
)

// PyModuleDef_Init: in CPython it sets up module def; for fython just returns the def pointer.
func PyModuleDef_Init(def unsafe.Pointer) unsafe.Pointer {
	if def == nil {
		pyErrSetTypeError("PyModuleDef_Init called with NULL")
		return nil
	}
	// Validate mt_name? Nothing to do.
	return def
}

// ModuleState for multi-phase state size: allocate state buffer via Go heap and keep alive.
// Map mod stub -> state pointer (Go slice backing array or mmap? Use Go []byte pinned via map).
var modStates = map[unsafe.Pointer][]byte{}

// PyModule_Create2: single-phase legacy init. def is PyModuleDef*, state is int (ignored for fython state size handling).
func PyModule_Create2(def unsafe.Pointer, _ int32) unsafe.Pointer {
	if def == nil {
		pyErrSetTypeError("PyModule_Create2 called with NULL")
		return nil
	}
	name, doc, size, methods, slots, _ := ReadModuleDef(def)
	if name == "" {
		name = "unnamed"
	}
	modVal := val.NewModuleVal(name, "")
	mod := modVal.AsModule()
	if doc != "" {
		mod.Dict.Set(val.InternString("__doc__"), val.NewStringVal(doc))
	}
	// Allocate state if size > 0
	if size > 0 {
		// Allocate zeroed buffer; keep in modStates map keyed by module stub (to be filled after ToStub?? Need pointer)
		// We'll allocate after ToStub returns stub, but we need state pointer before return. Allocate Go buffer now, attach after.
		buf := make([]byte, size)
		// store temporarily; will bind to stub below
		stub := val.ArenaToStub(modVal)
		modStates[stub] = buf
		// Also need to handle m_methods/m_slots for single-phase Create2 in Python 3.15: PyModule_Create2 ignores m_methods? Actually single-phase legacy: methods are added via PyModule_AddObject inside PyInit, not via def? But some single-phase modules set def.m_methods.
		// Process methods if present.
		if methods != nil {
			addMethodsFromDef(modVal, mod, methods)
		}
		if slots != nil {
			// Slots may contain Py_mod_create/exec etc for hybrid? Ignore for single-phase Create2 (Create triggers exec if PyInit does?).
			// But for compatibility, process slots create/exec if present.
			handleSlots(modVal, mod, slots)
		}
		return stub
	}
	stub := val.ArenaToStub(modVal)
	if methods != nil {
		addMethodsFromDef(modVal, mod, methods)
	}
	if slots != nil {
		handleSlots(modVal, mod, slots)
	}
	return stub
}

// addMethodsFromDef parses PyMethodDef array terminated by ml_name==NULL.
func addMethodsFromDef(modVal val.Value, mod *val.PyModule, methods unsafe.Pointer) {
	// PyMethodDef size 32: name(0), meth(8), flags int32(16), doc(24)
	const stride = 32
	for i := 0; ; i++ {
		entry := unsafe.Add(methods, i*stride)
		namePtr := *(*unsafe.Pointer)(entry)
		if namePtr == nil {
			break
		}
		methPtr := *(*unsafe.Pointer)(unsafe.Add(entry, 8))
		flags := *(*int32)(unsafe.Add(entry, 16))
		// doc at 24 not needed beyond setting __doc__?
		fnVal := WrapPyCFunction(methPtr, flags, modVal, namePtr)
		name := GoString(namePtr)
		mod.Dict.Set(val.InternString(name), fnVal)
	}
}

// handleSlots processes PyModuleDef_Slot array for Py_mod_create/exec.
func handleSlots(modVal val.Value, mod *val.PyModule, slots unsafe.Pointer) {
	for i := range 16 {
		slotAddr := unsafe.Add(slots, i*16)
		slotID := *(*int32)(slotAddr)
		valPtr := *(*unsafe.Pointer)(unsafe.Add(slotAddr, 8))
		if slotID == 0 {
			break
		}
		// Slot 1=create, 2=exec (non-limited), 84/85 for limited ATF? Check slots_generated: Py_mod_create 84 limited, 1 full.
		if slotID == 1 || slotID == 84 {
			// Py_mod_create: PyObject* (*)(PyModuleDef*, void* state?) Actually signature: PyObject* (*create)(PyModuleDef*, void*)? In 3.13, Py_mod_create is `PyObject* (*)(PyModuleDef*, void*)`. Call it to create module.
			// For fython, we already have modVal; call create to potentially return new module? If create returns non-NULL, use it.
			if valPtr != nil {
				var createFn func(def unsafe.Pointer, state unsafe.Pointer) unsafe.Pointer
				dynload.RegisterFunc(&createFn, uintptr(valPtr))
				// Pass module def pointer? Need def pointer — not available in slots-only. We have no def base here; reconstruct? For MVP, call with (modDef=nil, state=nil) may crash if create expects def.
				// Alternative: store pending slot callbacks for loader to invoke after Create2? For now, skip if def unknown.
				// We will handle slot create/exec in loader (extension_loader) rather than here, where def is known.
				_ = createFn
			}
		}
		if slotID == 2 || slotID == 85 {
			// Py_mod_exec: int (*)(PyModuleDef*, void* module)
			// Defer to exec_slots registry for loader.
			RegisterExecSlot(modVal, valPtr)
		}
	}
}

// Exec slots registry for multi-phase: loader will run these after PyModuleDef_Init returns module.
var execSlots = map[unsafe.Pointer]unsafe.Pointer{} // module Value(ptr) -> exec fn ptr

func RegisterExecSlot(modVal val.Value, execFn unsafe.Pointer) {
	// Use stub pointer as key? ModVal ptr is header. Store by modVal.HeaderPtr().
	execSlots[unsafe.Pointer(modVal.HeaderPtr())] = execFn
}
func GetExecSlot(modVal val.Value) unsafe.Pointer {
	return execSlots[unsafe.Pointer(modVal.HeaderPtr())]
}

// For loader to run exec slots after creating module via PyModuleDef_Init (multi-phase).
func RunExecSlot(modVal val.Value) bool {
	fn := GetExecSlot(modVal)
	if fn == nil {
		return true
	}
	// Signature: int (*)(PyModuleDef*, void* module)
	// We need def pointer — stored alongside? For now, need to store def too.
	// Caller (loader) should register def+exec; this helper expects loader to have stored def.
	// Fallback: if no def, just call with nil def.
	var callFn func(def unsafe.Pointer, mod unsafe.Pointer) int32
	dynload.RegisterFunc(&callFn, uintptr(fn))
	// We don't have def here; loader will call with proper def via RunExecSlotWithDef.
	return true
}
func RunExecSlotWithDef(modVal val.Value, def unsafe.Pointer) bool {
	fn := GetExecSlot(modVal)
	if fn == nil {
		return true
	}
	var callFn func(def unsafe.Pointer, mod unsafe.Pointer) int32
	dynload.RegisterFunc(&callFn, uintptr(fn))
	modStub := val.ArenaBorrowStub(modVal)
	ret := callFn(def, modStub)
	return ret == 0
}

func PyModule_AddObject(m unsafe.Pointer, name unsafe.Pointer, obj unsafe.Pointer) int32 {
	if m == nil || name == nil || obj == nil {
		pyErrSetTypeError("PyModule_AddObject called with NULL")
		return -1
	}
	modVal := val.ArenaFromStub(m)
	if !modVal.IsModule() {
		pyErrSetTypeError("PyModule_AddObject argument must be module")
		return -1
	}
	key := GoString(name)
	// Steals reference.
	modVal.AsModule().Dict.Set(val.InternString(key), val.ArenaFromStub(obj))
	return 0
}
func PyModule_AddObjectRef(m unsafe.Pointer, name unsafe.Pointer, obj unsafe.Pointer) int32 {
	// Non-stealing: same as AddObject but we need to incref? Our Value is borrowed; adding to dict keeps it alive via dict's Value copy (Go heap). No refcount change needed because Go GC handles Value lifetime, and handle table keeps stub separate? But object was created via ArenaToStub with refcnt=1; if we just store Value in dict without incref, stub refcnt stays 1 (owned by caller). When caller DECREFs stub to 0, handle table cleared, but dict still holds Value copy (Go heap object still alive via dict). Next ToStub for that Value would recreate stub (different address). That's okay but identity changes. For AddObjectRef, we should incref to keep stub alive as long as module holds it.
	// Implement incref: get stub, increment refcnt.
	if m == nil || name == nil || obj == nil {
		pyErrSetTypeError("PyModule_AddObjectRef called with NULL")
		return -1
	}
	modVal := val.ArenaFromStub(m)
	if !modVal.IsModule() {
		pyErrSetTypeError("PyModule_AddObjectRef argument must be module")
		return -1
	}
	key := GoString(name)
	// Incref the object stub
	if obj != nil {
		rc := *(*int64)(obj)
		if rc < val.ImmortalRefcnt {
			*(*int64)(obj) = rc + 1
		}
	}
	modVal.AsModule().Dict.Set(val.InternString(key), val.ArenaFromStub(obj))
	return 0
}
func PyModule_AddIntConstant(m unsafe.Pointer, name unsafe.Pointer, v int64) int32 {
	if m == nil || name == nil {
		pyErrSetTypeError("PyModule_AddIntConstant called with NULL")
		return -1
	}
	modVal := val.ArenaFromStub(m)
	if !modVal.IsModule() {
		pyErrSetTypeError("PyModule_AddIntConstant argument must be module")
		return -1
	}
	key := GoString(name)
	modVal.AsModule().Dict.Set(val.InternString(key), val.Int64Val(v))
	return 0
}
func PyModule_AddStringConstant(m unsafe.Pointer, name unsafe.Pointer, v unsafe.Pointer) int32 {
	if m == nil || name == nil || v == nil {
		pyErrSetTypeError("PyModule_AddStringConstant called with NULL")
		return -1
	}
	modVal := val.ArenaFromStub(m)
	if !modVal.IsModule() {
		pyErrSetTypeError("PyModule_AddStringConstant argument must be module")
		return -1
	}
	key := GoString(name)
	valStr := GoString(v)
	modVal.AsModule().Dict.Set(val.InternString(key), val.NewStringVal(valStr))
	return 0
}
func PyModule_GetState(m unsafe.Pointer) unsafe.Pointer {
	if m == nil {
		return nil
	}
	if buf, ok := modStates[m]; ok {
		if len(buf) == 0 {
			return nil
		}
		return unsafe.Pointer(&buf[0])
	}
	return nil
}

// Capsule
func PyCapsule_New(ptr unsafe.Pointer, name unsafe.Pointer, dtor unsafe.Pointer) unsafe.Pointer {
	// Create a capsule object: use val.NewInstanceOfType(CapsuleType?) fython has no capsule type; emulate via Module? Use KindNative instance with capsule storage via global map.
	// For MVP, create a dict? Better: create instance of ObjectType and store ptr in capsule map keyed by stub.
	capsVal := val.NewInstanceOfType(val.ObjectType)
	capsStub := val.ArenaToStub(capsVal)
	capsuleName := ""
	if name != nil {
		capsuleName = GoString(name)
	}
	SetCapsule(capsStub, ptr, dtor)
	// Also store name as attr for debugging
	capsVal.AsInstance().SetAttr(val.InternString("__capsule_name__"), val.NewStringVal(capsuleName))
	_ = dtor
	return capsStub
}
func PyCapsule_GetPointer(caps unsafe.Pointer, name unsafe.Pointer) unsafe.Pointer {
	if caps == nil {
		pyErrSetTypeError("PyCapsule_GetPointer called with NULL")
		return nil
	}
	ptr, _, ok := GetCapsule(caps)
	if !ok {
		// Capsule may be stored under different stub (from Import)? Check by Value identity?
		pyErrSetTypeError("PyCapsule_GetPointer called with invalid capsule")
		return nil
	}
	return ptr
}
func PyCapsule_Import(name unsafe.Pointer, noInst int32) unsafe.Pointer {
	// For now, handle "ffi" etc? In spec, PyCapsule_Import is for importing capsule by dotted name "module.attr".
	// We search for module + attr: split name by '.'
	if name == nil {
		pyErrSetTypeError("PyCapsule_Import called with NULL")
		return nil
	}
	nameStr := GoString(name)
	// Simple: try to find capsule by name in registry? We need global capsule name registry.
	// For MVP, return NULL with NameError if not found, but tests that use PyCapsule_Import for specific names will fail.
	// Alternative: if name is "fython.capsule" etc? Need spec-defined behavior: PyCapsule_Import("ffi.capsule") looks up capsule exported via PyCapsule_New in module.
	// Since we don't have module registry for capsules, we can maintain capsuleByName map.
	// This function is called with full name like "ffi" ??? Actually signature: PyCapsule_Import(const char *name, int no_inherit) — looks up capsule named `name` exported from sys.modules or imported?
	// Simplify: check capsuleNameRegistry first.
	if stub, ok := capsuleByName[nameStr]; ok {
		return stub
	}
	// Not found — set error (CPython would set PyExc_ImportError / AttributeError)
	// But for tests, we can return None stub? Better to set error and return NULL.
	pyErrSetString(val.ImportErrorType, "capsule import failed")
	return nil
}

// capsule name registry for Import
var capsuleByName = make(map[string]unsafe.Pointer)

func RegisterCapsuleByName(name string, stub unsafe.Pointer) {
	capsuleByName[name] = stub
}
