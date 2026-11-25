package vm

import (
	"testing"
)

func TestInlineLocalsDefinitelyAssigned(t *testing.T) {
	// Case 1: straight-line assign local 1 before reading it (1 param: local 0).
	bodyAssignFirst := []uint32{
		Encode(OP_LOAD_FAST, 0, 0),
		Encode(OP_STORE_FAST, 0, 1),
		Encode(OP_RETURN_FAST, 0, 1),
	}
	if !inlineLocalsDefinitelyAssigned(bodyAssignFirst, 0, 1, 2) {
		t.Fatalf("expected definitely assigned for straight-line store-before-load")
	}

	// Case 2: read local 1 before assigning it -> must return false.
	bodyReadBeforeWrite := []uint32{
		Encode(OP_LOAD_FAST, 0, 1),
		Encode(OP_RETURN, 0, 0),
	}
	if inlineLocalsDefinitelyAssigned(bodyReadBeforeWrite, 0, 1, 2) {
		t.Fatalf("expected false when non-param local is read before write")
	}

	// Case 3: assigned on both branches of an if/else before join read -> true.
	bodyBothBranches := []uint32{
		Encode(OP_LOAD_FAST, 0, 0),     // 0
		Encode(OP_JUMP_IF_FALSE, 0, 5), // 1 -> 5
		Encode(OP_LOAD_CONST, 0, 0),    // 2
		Encode(OP_STORE_FAST, 0, 1),    // 3
		Encode(OP_JUMP, 0, 7),          // 4 -> 7
		Encode(OP_LOAD_CONST, 0, 1),    // 5
		Encode(OP_STORE_FAST, 0, 1),    // 6
		Encode(OP_RETURN_FAST, 0, 1),   // 7
	}
	if !inlineLocalsDefinitelyAssigned(bodyBothBranches, 0, 1, 2) {
		t.Fatalf("expected definitely assigned when both branches store local 1")
	}

	// Case 4: assigned on only one branch before join read -> false.
	bodyOneBranch := []uint32{
		Encode(OP_LOAD_FAST, 0, 0),     // 0
		Encode(OP_JUMP_IF_FALSE, 0, 4), // 1 -> 4
		Encode(OP_LOAD_CONST, 0, 0),    // 2
		Encode(OP_STORE_FAST, 0, 1),    // 3
		Encode(OP_RETURN_FAST, 0, 1),   // 4
	}
	if inlineLocalsDefinitelyAssigned(bodyOneBranch, 0, 1, 2) {
		t.Fatalf("expected false when only one branch assigns local 1")
	}
}
