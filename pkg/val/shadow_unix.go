//go:build linux || darwin || freebsd

package val

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

func arenaMmap(n int) ([]byte, unsafe.Pointer, error) {
	b, err := unix.Mmap(-1, 0, n, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.MAP_PRIVATE)
	if err != nil {
		return nil, nil, err
	}
	if len(b) == 0 {
		return nil, nil, nil
	}
	return b, unsafe.Pointer(&b[0]), nil
}
