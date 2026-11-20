//go:build windows

package vm

import (
	"golang.org/x/sys/windows"

	"github.com/servusdei2018/fython/pkg/dynload"
)

// ctypesDlopen loads a DLL for ctypes. On Windows dynload.Handle is
// windows.Handle, so a conversion to the canonical uintptr is required.
func ctypesDlopen(name string) (uintptr, error) {
	h, err := dynload.Open(name)
	return uintptr(h), err
}

// ctypesDlsym resolves a symbol in a ctypes library handle.
func ctypesDlsym(handle uintptr, name string) (uintptr, error) {
	return dynload.Sym(windows.Handle(handle), name)
}
