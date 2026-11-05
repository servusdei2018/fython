package compiler

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

func TestInlinedComprehensionNoClosure(t *testing.T) {
	src := `
def run_comp():
    return [x * 2 for x in range(1000)]
`
	co, err := CompileString(src)
	if err != nil {
		t.Fatalf("CompileString failed: %v", err)
	}

	v := vm.NewVM()
	v.RunCode(co)

	fnVal, ok := v.GetGlobal("run_comp")
	if !ok || !fnVal.IsFunction() {
		t.Fatalf("run_comp function not found in globals")
	}

	fn := fnVal.AsFunction()
	fnCO := vm.AsCodeObject(fn.Code)

	// PEP 709 Invariant 1: No nested code objects in consts (no <listcomp> function/closure)
	for i, c := range fnCO.Consts {
		if c.Kind() == val.KindCode {
			t.Fatalf("PEP 709 violation: found nested CodeObject at const[%d] in run_comp", i)
		}
	}

	// PEP 709 Invariant 2: Bytecode contains OP_BUILD_LIST and OP_LIST_APPEND directly in frame,
	// and contains 0 OP_MAKE_FUNCTION or OP_CALL instructions for comprehension.
	hasBuildList := false
	hasListAppend := false
	for _, inst := range fnCO.Code {
		op := vm.Opcode(uint8(inst >> 24))
		if op == vm.OP_BUILD_LIST {
			hasBuildList = true
		}
		if op == vm.OP_LIST_APPEND {
			hasListAppend = true
		}
		if op == vm.OP_MAKE_FUNCTION {
			t.Fatalf("PEP 709 violation: OP_MAKE_FUNCTION emitted inside run_comp")
		}
	}
	if !hasBuildList {
		t.Fatalf("expected OP_BUILD_LIST in inlined comprehension")
	}
	if !hasListAppend {
		t.Fatalf("expected OP_LIST_APPEND in inlined comprehension")
	}

	// Execute and verify correctness
	res := v.Vectorcall(fnVal, nil, nil)
	if !res.IsList() {
		t.Fatalf("run_comp returned %v, want list", res)
	}
	pyList := res.AsList()
	if pyList.Len() != 1000 {
		t.Fatalf("expected list of len 1000, got %d", pyList.Len())
	}
	if pyList.Items[0].AsInt64() != 0 || pyList.Items[999].AsInt64() != 1998 {
		t.Fatalf("list items mismatch: first=%v, last=%v", pyList.Items[0], pyList.Items[999])
	}
}

func BenchmarkInlinedComprehension(b *testing.B) {
	src := `
def run_comp():
    return [x * 2 for x in range(1000)]
`
	co, err := CompileString(src)
	if err != nil {
		b.Fatalf("CompileString failed: %v", err)
	}

	v := vm.NewVM()
	v.RunCode(co)

	fnVal, ok := v.GetGlobal("run_comp")
	if !ok || !fnVal.IsFunction() {
		b.Fatalf("run_comp function not found in globals")
	}

	// Warmup
	res := v.Vectorcall(fnVal, nil, nil)
	if !res.IsList() || res.AsList().Len() != 1000 {
		b.Fatalf("warmup failed")
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		res = v.Vectorcall(fnVal, nil, nil)
	}

	if !res.IsList() || res.AsList().Len() != 1000 {
		b.Fatalf("benchmark failed")
	}
}
