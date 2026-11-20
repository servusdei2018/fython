//go:build unix

package vm

import "github.com/servusdei2018/fython/pkg/dynload"

// ctypesDlopen loads a shared library for ctypes. On Unix dynload.Handle is an
// alias for uintptr, so no conversion is required here.
func ctypesDlopen(name string) (uintptr, error) {
	h, err := dynload.Open(name)
	return h, err
}

// ctypesDlsym resolves a symbol in a ctypes library handle.
func ctypesDlsym(handle uintptr, name string) (uintptr, error) {
	return dynload.Sym(handle, name)
}
