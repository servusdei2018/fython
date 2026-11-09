package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

func BenchmarkSingleThreadSlotAccess(b *testing.B) {
	val.SetMultiThreaded(false)
	cls := val.MustNewType("BenchPoint", []*val.PyType{val.ObjectType}, val.NewDictVal(8).AsDict())
	xName := val.InternString("x")
	yName := val.InternString("y")

	instVal := val.NewInstanceOfType(cls)
	inst := instVal.AsInstance()
	xIdx := inst.SetAttr(xName, val.Int64Val(10))
	yIdx := inst.SetAttr(yName, val.Int64Val(20))

	b.ResetTimer()
	b.ReportAllocs()

	for i := range b.N {
		inst.SetSlot(xIdx, val.Int64Val(int64(i)))
		inst.SetSlot(yIdx, val.Int64Val(int64(i+1)))
		xVal, _ := inst.GetSlot(xIdx)
		yVal, _ := inst.GetSlot(yIdx)
		_ = xVal.AsInt64() + yVal.AsInt64()
	}
}

func BenchmarkMultiThreadSlotAccess(b *testing.B) {
	val.SetMultiThreaded(true)
	defer val.SetMultiThreaded(false)
	cls := val.MustNewType("BenchPointMT", []*val.PyType{val.ObjectType}, val.NewDictVal(8).AsDict())
	xName := val.InternString("x")
	yName := val.InternString("y")

	instVal := val.NewInstanceOfType(cls)
	inst := instVal.AsInstance()
	xIdx := inst.SetAttr(xName, val.Int64Val(10))
	yIdx := inst.SetAttr(yName, val.Int64Val(20))

	b.ResetTimer()
	b.ReportAllocs()

	for i := range b.N {
		inst.SetSlot(xIdx, val.Int64Val(int64(i)))
		inst.SetSlot(yIdx, val.Int64Val(int64(i+1)))
		xVal, _ := inst.GetSlot(xIdx)
		yVal, _ := inst.GetSlot(yIdx)
		_ = xVal.AsInt64() + yVal.AsInt64()
	}
}

func BenchmarkVMOOPSingleThread(b *testing.B) {
	val.SetMultiThreaded(false)
	src := `
class Point:
    def __init__(self, x, y):
        self.x = x
        self.y = y
    def dist_sq(self):
        return self.x * self.x + self.y * self.y

p = Point(1, 2)
s = 0
for i in range(1000):
    p.x = i
    p.y = i + 1
    s += p.dist_sq()
`
	engine := vm.NewEngine()
	v := engine.NewVM()
	co, err := compiler.CompileString(src)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for range b.N {
		v.RunCode(co)
	}
}
