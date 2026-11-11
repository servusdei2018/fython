//go:build linux || darwin

package val

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// byteMapping describes an anonymous mapping that backs a Python buffer.
type byteMapping struct {
	base *byte
	size int
}

// allocZeroedBytes returns a zero-initialised buffer of n bytes.
//
// Buffers of at least LargeBufferThreshold bytes are mapped outside the Go
// heap. The Go heap never learns about them, so a multi-megabyte buffer does
// not push the heap over the garbage collector's trigger, and the runtime does
// not have to clear every page up front: the kernel supplies zero pages on
// demand. The second result is the mapping that backs the buffer, or nil when
// the buffer lives on the Go heap.
//
// The mapping is anonymous and private, so it is invisible to the rest of the
// process and is returned to the operating system as soon as the owning
// Python object is collected.
func allocZeroedBytes(n int) ([]byte, *byteMapping) {
	if n < LargeBufferThreshold {
		return make([]byte, n), nil
	}
	base, err := unix.MmapPtr(-1, 0, nil, uintptr(n),
		unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.MAP_PRIVATE)
	if err != nil {
		return make([]byte, n), nil
	}
	buf := unsafe.Slice((*byte)(base), n)
	return buf, &byteMapping{base: &buf[0], size: n}
}

// attachByteMapping returns the mapping to the operating system once obj
// becomes unreachable.
//
// The cleanup captures the mapping itself and not the buffer of the object, so
// replacing the buffer later cannot unmap the wrong region or unmap it twice.
func attachByteMapping[T any](obj *T, m *byteMapping) {
	if m == nil {
		return
	}
	runtime.AddCleanup(obj, func(m byteMapping) {
		_ = unix.MunmapPtr(unsafe.Pointer(m.base), uintptr(m.size))
	}, *m)
}
