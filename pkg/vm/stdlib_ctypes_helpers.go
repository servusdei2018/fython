package vm

import (
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

func isSimpleCData(v val.Value) bool {
	return v.HeaderPtr() != nil && v.HeaderPtr().Kind == val.KindNative && v.Type() != nil && val.IsSubclass(v.Type(), simpleCDataTyp)
}
func asSimpleCData(v val.Value) *pySimpleCData {
	return (*pySimpleCData)(unsafe.Pointer(v.HeaderPtr()))
}
func isCStruct(v val.Value) bool {
	return v.HeaderPtr() != nil && v.HeaderPtr().Kind == val.KindNative && v.Type() != nil && (val.IsSubclass(v.Type(), structTyp) || val.IsSubclass(v.Type(), unionTyp))
}
func asCStruct(v val.Value) *pyCStruct {
	return (*pyCStruct)(unsafe.Pointer(v.HeaderPtr()))
}
func isCArray(v val.Value) bool {
	return v.HeaderPtr() != nil && v.HeaderPtr().Kind == val.KindNative && v.Type() != nil && val.IsSubclass(v.Type(), arrayTyp)
}
func asCArray(v val.Value) *pyCArray {
	return (*pyCArray)(unsafe.Pointer(v.HeaderPtr()))
}
func isCPointer(v val.Value) bool {
	return v.HeaderPtr() != nil && v.HeaderPtr().Kind == val.KindNative && v.Type() != nil && val.IsSubclass(v.Type(), pointerTyp)
}
func asCPointer(v val.Value) *pyCPointer {
	return (*pyCPointer)(unsafe.Pointer(v.HeaderPtr()))
}
func isCFuncPtr(v val.Value) bool {
	return v.HeaderPtr() != nil && v.HeaderPtr().Kind == val.KindNative && v.Type() != nil && val.IsSubclass(v.Type(), cfuncPtrTyp)
}
func asCFuncPtr(v val.Value) *pyCFuncPtr {
	return (*pyCFuncPtr)(unsafe.Pointer(v.HeaderPtr()))
}
