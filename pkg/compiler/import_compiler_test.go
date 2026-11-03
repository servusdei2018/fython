package compiler_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/vm"
)

func TestCompileImportStatements(t *testing.T) {
	tests := []struct {
		name     string
		src      string
		validate func(t *testing.T, co *vm.CodeObject)
	}{
		{
			name: "import simple",
			src:  "import math",
			validate: func(t *testing.T, co *vm.CodeObject) {
				if len(co.Code) < 2 {
					t.Fatalf("expected at least 2 instructions, got %d", len(co.Code))
				}
				op0 := vm.DecodeOpcode(co.Code[0])
				if op0 != vm.OP_IMPORT_NAME {
					t.Errorf("op0 = %v, want OP_IMPORT_NAME", op0)
				}
				op1 := vm.DecodeOpcode(co.Code[1])
				if op1 != vm.OP_STORE_GLOBAL {
					t.Errorf("op1 = %v, want OP_STORE_GLOBAL", op1)
				}
			},
		},
		{
			name: "import with alias",
			src:  "import math as m",
			validate: func(t *testing.T, co *vm.CodeObject) {
				op0 := vm.DecodeOpcode(co.Code[0])
				if op0 != vm.OP_IMPORT_NAME {
					t.Errorf("op0 = %v, want OP_IMPORT_NAME", op0)
				}
				argA := vm.DecodeArgA(co.Code[0])
				if (argA & 0x80) == 0 {
					t.Errorf("expected leaf flag bit 7 to be set for import with alias")
				}
			},
		},
		{
			name: "from import symbols",
			src:  "from math import sin, cos as c",
			validate: func(t *testing.T, co *vm.CodeObject) {
				// OP_IMPORT_NAME, OP_IMPORT_FROM, STORE, OP_IMPORT_FROM, STORE, POP_TOP
				op0 := vm.DecodeOpcode(co.Code[0])
				if op0 != vm.OP_IMPORT_NAME {
					t.Errorf("op0 = %v, want OP_IMPORT_NAME", op0)
				}
				op1 := vm.DecodeOpcode(co.Code[1])
				if op1 != vm.OP_IMPORT_FROM {
					t.Errorf("op1 = %v, want OP_IMPORT_FROM", op1)
				}
			},
		},
		{
			name: "from relative import",
			src:  "from ..sub import helper",
			validate: func(t *testing.T, co *vm.CodeObject) {
				op0 := vm.DecodeOpcode(co.Code[0])
				if op0 != vm.OP_IMPORT_NAME {
					t.Errorf("op0 = %v, want OP_IMPORT_NAME", op0)
				}
				level := vm.DecodeArgA(co.Code[0]) & 0x7F
				if level != 2 {
					t.Errorf("expected relative level 2, got %d", level)
				}
			},
		},
		{
			name: "from import star",
			src:  "from math import *",
			validate: func(t *testing.T, co *vm.CodeObject) {
				op0 := vm.DecodeOpcode(co.Code[0])
				if op0 != vm.OP_IMPORT_NAME {
					t.Errorf("op0 = %v, want OP_IMPORT_NAME", op0)
				}
				op1 := vm.DecodeOpcode(co.Code[1])
				if op1 != vm.OP_IMPORT_STAR {
					t.Errorf("op1 = %v, want OP_IMPORT_STAR", op1)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			co, err := compiler.CompileString(tt.src)
			if err != nil {
				t.Fatalf("CompileString(%q) failed: %v", tt.src, err)
			}
			tt.validate(t, co)
		})
	}
}
