package vm

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func buildMapTempCode(code []uint32, isGen, isCoro, hasCells bool, exc []ExceptionEntry) *CodeObject {
	co := &CodeObject{
		Code:           code,
		IsGenerator:    isGen,
		IsCoroutine:    isCoro,
		ExceptionTable: exc,
	}
	if hasCells {
		co.CellVars = []val.Value{val.NewStringVal("x")}
	}
	return co
}

func TestComputeLocalDictSafeQualifying(t *testing.T) {
	code := []uint32{
		Encode(OP_BUILD_MAP, 0, 2),
		Encode(OP_STORE_FAST, 0, 0),
		Encode(OP_LOAD_FAST, 0, 0),
		Encode(OP_CALL_FUNCTION_EX, 1, 0),
		Encode(OP_RETURN, 0, 0),
	}
	co := buildMapTempCode(code, false, false, false, nil)
	co.computeParamShape()
	if len(co.MapTempSafe) == 0 || !co.MapTempSafe[0] {
		t.Fatalf("expected MapTempSafe[0]==true, got %v", co.MapTempSafe)
	}
}

func TestComputeLocalDictSafeRejects(t *testing.T) {
	cases := map[string]struct {
		code     []uint32
		isGen    bool
		isCoro   bool
		hasCells bool
		exc      []ExceptionEntry
	}{
		"too many entries": {
			code: []uint32{
				Encode(OP_BUILD_MAP, 0, 17),
				Encode(OP_STORE_FAST, 0, 0),
				Encode(OP_LOAD_FAST, 0, 0),
				Encode(OP_CALL_FUNCTION_EX, 1, 0),
			},
		},
		"double store": {
			code: []uint32{
				Encode(OP_BUILD_MAP, 0, 2),
				Encode(OP_STORE_FAST, 0, 0),
				Encode(OP_STORE_FAST, 0, 0),
				Encode(OP_LOAD_FAST, 0, 0),
				Encode(OP_CALL_FUNCTION_EX, 1, 0),
			},
		},
		"double load": {
			code: []uint32{
				Encode(OP_BUILD_MAP, 0, 2),
				Encode(OP_STORE_FAST, 0, 0),
				Encode(OP_LOAD_FAST, 0, 0),
				Encode(OP_LOAD_FAST, 0, 0),
				Encode(OP_CALL_FUNCTION_EX, 1, 0),
			},
		},
		"wrong call kind": {
			code: []uint32{
				Encode(OP_BUILD_MAP, 0, 2),
				Encode(OP_STORE_FAST, 0, 0),
				Encode(OP_LOAD_FAST, 0, 0),
				Encode(OP_CALL_FUNCTION_EX, 0, 0),
			},
		},
		"not followed by store": {
			code: []uint32{
				Encode(OP_BUILD_MAP, 0, 2),
				Encode(OP_LOAD_FAST, 0, 1),
				Encode(OP_LOAD_FAST, 0, 1),
				Encode(OP_CALL_FUNCTION_EX, 1, 0),
			},
		},
		"generator": {
			code: []uint32{
				Encode(OP_BUILD_MAP, 0, 2),
				Encode(OP_STORE_FAST, 0, 0),
				Encode(OP_LOAD_FAST, 0, 0),
				Encode(OP_CALL_FUNCTION_EX, 1, 0),
			},
			isGen: true,
		},
		"cells": {
			code: []uint32{
				Encode(OP_BUILD_MAP, 0, 2),
				Encode(OP_STORE_FAST, 0, 0),
				Encode(OP_LOAD_FAST, 0, 0),
				Encode(OP_CALL_FUNCTION_EX, 1, 0),
			},
			hasCells: true,
		},
		"jump target in window": {
			code: []uint32{
				Encode(OP_BUILD_MAP, 0, 2),
				Encode(OP_STORE_FAST, 0, 0),
				Encode(OP_LOAD_FAST, 0, 0),
				Encode(OP_CALL_FUNCTION_EX, 1, 0),
				Encode(OP_JUMP, 0, 1),
			},
		},
		"exception overlap": {
			code: []uint32{
				Encode(OP_BUILD_MAP, 0, 2),
				Encode(OP_STORE_FAST, 0, 0),
				Encode(OP_LOAD_FAST, 0, 0),
				Encode(OP_CALL_FUNCTION_EX, 1, 0),
			},
			exc: []ExceptionEntry{{StartIP: 0, EndIP: 4, TargetIP: 10}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// For the jump-target case, append a jump that targets the
			// store slot so the window is blocked.
			code := tc.code
			if name == "jump target in window" {
				// Jump from index 4 to index 1 (the STORE_FAST).
				code[4] = Encode(OP_JUMP, 0, 1)
			}
			co := buildMapTempCode(code, tc.isGen, tc.isCoro, tc.hasCells, tc.exc)
			co.computeParamShape()
			if len(co.MapTempSafe) != 0 {
				for i, v := range co.MapTempSafe {
					if v {
						t.Fatalf("expected no safe site, got MapTempSafe[%d]==true", i)
					}
				}
			}
		})
	}
}
