package vm

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func mustRegCompile(t *testing.T, co *CodeObject) *RegCode {
	t.Helper()
	co.Init()
	OptimizeCodeObject(co)
	rc := co.RegCode()
	if rc == nil {
		t.Fatalf("expected RegCode for %s", co.Name)
	}
	return rc
}

// TestRegSmokeArithmetic compiles return a + b and runs it through the
// register loop.
func TestRegSmokeArithmetic(t *testing.T) {
	co := &CodeObject{
		Name:      "add",
		Code:      []uint32{Encode(OP_LOAD_FAST, 0, 0), Encode(OP_LOAD_FAST, 0, 1), Encode(OP_BINARY_ADD, 0, 0), Encode(OP_RETURN, 0, 0)},
		NumLocals: 2,
		NumParams: 2,
	}
	for range 19 {
		co.Code = append(co.Code, Encode(OP_NOP, 0, 0))
	}
	co.Code = append(co.Code, Encode(OP_JUMP, 0, 0))
	mustRegCompile(t, co)
	v := NewVM()
	v.stack[0] = val.Int64Val(40)
	v.stack[1] = val.Int64Val(2)
	res := v.RunCode(co)
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("unhandled exception: %v", exc)
	}
	if !res.IsInt64() || res.AsInt64() != 42 {
		t.Fatalf("result = %v, want 42", res)
	}
}
