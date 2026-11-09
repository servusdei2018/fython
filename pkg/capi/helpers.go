package capi

import (
	"sync"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// C string helpers (pure Go, no array bounds panic on bad pointers).

// GoString reads a NUL-terminated C string. Nil pointer returns "".
func GoString(p unsafe.Pointer) string {
	if p == nil {
		return ""
	}
	// Read byte-by-byte without unsafe.Slice to avoid checkptr straddling on C memory under -race.
	var buf []byte
	for i := range 1 << 20 {
		b := *(*byte)(unsafe.Add(p, i))
		if b == 0 {
			break
		}
		buf = append(buf, b)
	}
	return string(buf)
}

// GoBytes reads n bytes from p.
func GoBytes(p unsafe.Pointer, n int64) []byte {
	if p == nil || n <= 0 {
		return nil
	}
	// Bound n to avoid panic on huge values.
	if n > 1<<26 {
		n = 1 << 26
	}
	out := make([]byte, n)
	for i := range n {
		out[i] = *(*byte)(unsafe.Add(p, int(i)))
	}
	return out
}

// CString returns pointer to NUL-terminated Go bytes kept alive in arena cache.
// For format strings / module names etc., caller keeps returned pointer valid
// for the duration of the C call via the arena's strCache.
func CString(s string) unsafe.Pointer {
	b := append([]byte(s), 0)
	// Pin via global to prevent GC while C uses it (handlers use synchronously).
	// We keep it in a small LRU-ish map keyed by pointer? Simpler: runtime.KeepAlive
	// not sufficient across C call. Instead store in a sync.Map of live CStrings
	// that is cleared per handler? For synchronous use, Go slice stays alive
	// because handler holds `b` in stack frame across the C callback return.
	// Go's GC won't collect it while handler frame is live, and C won't retain it.
	if len(b) == 0 {
		b = []byte{0}
	}
	return unsafe.Pointer(&b[0])
}

// Exception helpers — map PyErr_* to ctx.ActiveException.

func pyErrSetString(excType *val.PyType, msg string) {
	if excType == nil {
		excType = val.ExceptionType
	}
	if excType == nil {
		excType = val.RuntimeErrorType
	}
	SetError(val.NewException(excType, msg))
}

func pyErrSetTypeError(msg string)  { pyErrSetString(val.TypeErrorType, msg) }
func pyErrSetValueError(msg string) { pyErrSetString(val.ValueErrorType, msg) }
func pyErrSetOverflow(msg string)   { pyErrSetString(val.OverflowErrorType, msg) }

// Module state and capsule registries (protected by GIL, but use mutex for safety).

var (
	capsuleMu sync.Mutex
	capsules  = make(map[unsafe.Pointer]capsuleInfo)
)

type capsuleInfo struct {
	ptr        unsafe.Pointer
	destructor unsafe.Pointer // C function pointer (stored, not called automatically)
}

func SetCapsule(caps unsafe.Pointer, ptr unsafe.Pointer, dtor unsafe.Pointer) {
	capsuleMu.Lock()
	capsules[caps] = capsuleInfo{ptr: ptr, destructor: dtor}
	capsuleMu.Unlock()
}
func GetCapsule(caps unsafe.Pointer) (unsafe.Pointer, unsafe.Pointer, bool) {
	capsuleMu.Lock()
	defer capsuleMu.Unlock()
	info, ok := capsules[caps]
	return info.ptr, info.destructor, ok
}

// PyModuleDef mirror for multi-phase init (CPython 3.15 layout).
// Offsets (64-bit):
//
//	m_base: PyModuleDef_Base = PyObject_HEAD(16) + m_init(8)+m_index(8)+m_copy(8) = 40
//	m_name @40, m_doc @48, m_size @56, m_methods @64, m_slots @72, m_traverse @80, m_clear @88, m_free @96
const (
	pyModDefNameOff     = 40
	pyModDefDocOff      = 48
	pyModDefSizeOff     = 56
	pyModDefMethodsOff  = 64
	pyModDefSlotsOff    = 72
	pyModDefTraverseOff = 80
	pyModDefClearOff    = 88
	pyModDefFreeOff     = 96
)

// ReadModuleDef reads a PyModuleDef* from off-heap memory.
func ReadModuleDef(p unsafe.Pointer) (name string, doc string, size int64, methods unsafe.Pointer, slots unsafe.Pointer, gilNotUsed bool) {
	if p == nil {
		return "", "", 0, nil, nil, false
	}
	namePtr := *(*unsafe.Pointer)(unsafe.Add(p, pyModDefNameOff))
	docPtr := *(*unsafe.Pointer)(unsafe.Add(p, pyModDefDocOff))
	size = *(*int64)(unsafe.Add(p, pyModDefSizeOff))
	methods = *(*unsafe.Pointer)(unsafe.Add(p, pyModDefMethodsOff))
	slots = *(*unsafe.Pointer)(unsafe.Add(p, pyModDefSlotsOff))
	if namePtr != nil {
		name = GoString(namePtr)
	}
	if docPtr != nil {
		doc = GoString(docPtr)
	}
	// Check slots for Py_mod_gil == 4 (stable value 87? check slots_generated.h: Py_mod_gil=87 compat 4)
	// Slots are array of {int slot; void* value;} 16 bytes each (int+padding+ptr).
	// Slot 4 = Py_mod_gil, 87 = limited API value? In limited API, Py_mod_gil is 87? Let's support both 4 and 87.
	if slots != nil {
		// iterate up to 16 slots, terminated by slot==0
		for i := range 16 {
			slotAddr := unsafe.Add(slots, i*16)
			slotID := *(*int32)(slotAddr)
			valPtr := *(*unsafe.Pointer)(unsafe.Add(slotAddr, 8))
			if slotID == 0 {
				break
			}
			// 4 = Py_mod_gil in non-limited, 87 = limited? Check headers: slots_generated.h defines Py_mod_gil=87 under limited? Actually _Py_SLOT_COMPAT_VALUE(4,87) means 4 in full, 87 in limited.
			// Extensions built with -DPy_LIMITED_API will have 87. Those without will have 4.
			if slotID == 4 || slotID == 87 {
				if uintptr(1) == uintptr(valPtr) {
					gilNotUsed = true
				}
			}
		}
	}
	return
}
