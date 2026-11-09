//go:build windows

package dynload

import (
	"github.com/ebitengine/purego"
	"golang.org/x/sys/windows"
)

type Handle = windows.Handle

func Open(path string) (Handle, error) {
	h, err := windows.LoadLibrary(path)
	return h, err
}

func Sym(h Handle, name string) (uintptr, error) {
	proc, err := windows.GetProcAddress(h, name)
	if err != nil {
		return 0, err
	}
	return proc, nil
}

func Close(h Handle) error {
	return windows.FreeLibrary(h)
}

func NewCallback(fn any) uintptr          { return purego.NewCallback(fn) }
func RegisterFunc(fptr any, addr uintptr) { purego.RegisterFunc(fptr, addr) }
func RegisterLibFunc(fptr any, h Handle, name string) {
	sym, err := Sym(h, name)
	if err != nil {
		panic(err)
	}
	RegisterFunc(fptr, sym)
}
