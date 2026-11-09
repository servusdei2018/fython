//go:build linux

package vm

import (
	"golang.org/x/sys/unix"
)

func memfdCreate(name string) (int, error) {
	return unix.MemfdCreate(name, unix.MFD_CLOEXEC)
}
