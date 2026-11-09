//go:build linux || darwin || freebsd

package dynload

import (
	"github.com/ebitengine/purego"
)

type Handle = uintptr

func Open(path string) (Handle, error) {
	// RTLD_NOW | RTLD_GLOBAL so extension symbols resolve against already-loaded shim
	return purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
}

func Sym(h Handle, name string) (uintptr, error) {
	return purego.Dlsym(h, name)
}

func Close(h Handle) error {
	return purego.Dlclose(h)
}

func NewCallback(fn any) uintptr                      { return purego.NewCallback(fn) }
func RegisterFunc(fptr any, addr uintptr)             { purego.RegisterFunc(fptr, addr) }
func RegisterLibFunc(fptr any, h Handle, name string) { purego.RegisterLibFunc(fptr, h, name) }
