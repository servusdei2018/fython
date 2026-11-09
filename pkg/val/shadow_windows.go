//go:build windows

package val

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

func arenaMmap(n int) ([]byte, unsafe.Pointer, error) {
	size := uintptr(n)
	base, err := windows.VirtualAlloc(0, size, windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
	if err != nil {
		return nil, nil, err
	}
	if base == 0 {
		return nil, nil, errors.New("VirtualAlloc failed")
	}
	// Convert uintptr address to unsafe.Pointer without direct uintptr->ptr conversion (vet).
	ptr := *(*unsafe.Pointer)(unsafe.Pointer(&base))
	b := unsafe.Slice((*byte)(ptr), n)
	return b, ptr, nil
}
