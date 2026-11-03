package vm_test

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

func TestFycSerializationRoundTrip(t *testing.T) {
	// Build a nested CodeObject tree with various constant kinds
	childCO := &vm.CodeObject{
		Name:         "child_func",
		Code:         []uint32{vm.Encode(vm.OP_LOAD_FAST, 0, 0), vm.Encode(vm.OP_RETURN, 0, 0)},
		Consts:       []val.Value{val.Int64Val(100), val.Float64Val(3.14159)},
		NumLocals:    1,
		NumParams:    1,
		MaxStackSize: 4,
	}

	rootCO := &vm.CodeObject{
		Name: "<module>",
		Code: []uint32{
			vm.Encode(vm.OP_LOAD_CONST, 0, 0),
			vm.Encode(vm.OP_LOAD_CONST, 0, 1),
			vm.Encode(vm.OP_BINARY_ADD, 0, 0),
			vm.Encode(vm.OP_RETURN, 0, 0),
		},
		Consts: []val.Value{
			val.Int64Val(42),
			val.Float64Val(2.71828),
			val.InternString("hello_fython"),
			val.BoolVal(true),
			val.BoolVal(false),
			val.NoneVal(),
			val.NewTupleVal(val.Int64Val(1), val.InternString("nested")),
			vm.NewCodeVal(childCO),
		},
		ParamNames:   []val.Value{val.InternString("x")},
		CellVars:     []val.Value{val.InternString("cell_a")},
		FreeVars:     []val.Value{val.InternString("free_b")},
		NumLocals:    2,
		NumParams:    1,
		MaxStackSize: 16,
		IsGenerator:  false,
		ExceptionTable: []vm.ExceptionEntry{
			{StartIP: 0, EndIP: 2, TargetIP: 3, TargetStackDepth: 1, PushLasti: true},
		},
	}

	srcSize := uint64(1024)
	srcMTime := time.Now().UnixNano()
	var srcHash [32]byte
	srcHash[0] = 0xAA
	srcHash[31] = 0xBB

	// Serialize
	data, err := vm.SerializeCodeObject(rootCO, srcSize, srcMTime, srcHash)
	if err != nil {
		t.Fatalf("SerializeCodeObject failed: %v", err)
	}

	// Validate header directly
	if len(data) < vm.FycHeaderSize {
		t.Fatalf("serialized data shorter than FycHeaderSize: %d", len(data))
	}

	// Deserialize
	deserCO, header, err := vm.DeserializeCodeObject(data)
	if err != nil {
		t.Fatalf("DeserializeCodeObject failed: %v", err)
	}

	// Validate header fields
	if header.Magic != vm.FycMagic {
		t.Errorf("header.Magic = 0x%X, want 0x%X", header.Magic, vm.FycMagic)
	}
	if header.Version != vm.FycVersion {
		t.Errorf("header.Version = %d, want %d", header.Version, vm.FycVersion)
	}
	if header.SourceSize != srcSize {
		t.Errorf("header.SourceSize = %d, want %d", header.SourceSize, srcSize)
	}
	if header.SourceMTimeNano != srcMTime {
		t.Errorf("header.SourceMTimeNano = %d, want %d", header.SourceMTimeNano, srcMTime)
	}
	if header.SourceHash != srcHash {
		t.Errorf("header.SourceHash mismatch")
	}

	// Validate CodeObject fields
	if deserCO.Name != rootCO.Name {
		t.Errorf("deserCO.Name = %q, want %q", deserCO.Name, rootCO.Name)
	}
	if deserCO.NumLocals != rootCO.NumLocals || deserCO.NumParams != rootCO.NumParams {
		t.Errorf("locals/params mismatch: %d/%d vs %d/%d", deserCO.NumLocals, deserCO.NumParams, rootCO.NumLocals, rootCO.NumParams)
	}
	if len(deserCO.Code) != len(rootCO.Code) {
		t.Fatalf("code length mismatch: %d vs %d", len(deserCO.Code), len(rootCO.Code))
	}
	for i := range rootCO.Code {
		if deserCO.Code[i] != rootCO.Code[i] {
			t.Errorf("instruction %d mismatch: 0x%08X vs 0x%08X", i, deserCO.Code[i], rootCO.Code[i])
		}
	}
	if len(deserCO.Consts) != len(rootCO.Consts) {
		t.Fatalf("consts length mismatch: %d vs %d", len(deserCO.Consts), len(rootCO.Consts))
	}
	if deserCO.Consts[0].AsInt64() != 42 {
		t.Errorf("consts[0] = %v, want 42", deserCO.Consts[0])
	}
	if deserCO.Consts[2].AsString() != "hello_fython" {
		t.Errorf("consts[2] = %v, want 'hello_fython'", deserCO.Consts[2])
	}
	if !deserCO.Consts[3].IsBool() || !deserCO.Consts[3].AsBool() {
		t.Errorf("consts[3] = %v, want True", deserCO.Consts[3])
	}

	// Verify nested code object
	nestedVal := deserCO.Consts[7]
	if nestedVal.Kind() != val.KindCode {
		t.Fatalf("expected consts[7] to be KindCode, got %v", nestedVal.Kind())
	}
	nestedCO := vm.AsCodeObject(nestedVal)
	if nestedCO.Name != "child_func" {
		t.Errorf("nestedCO.Name = %q, want 'child_func'", nestedCO.Name)
	}
	if len(nestedCO.Consts) != 2 || nestedCO.Consts[0].AsInt64() != 100 {
		t.Errorf("nestedCO.Consts mismatch")
	}

	// Verify ExceptionTable
	if len(deserCO.ExceptionTable) != 1 {
		t.Fatalf("ExceptionTable len = %d, want 1", len(deserCO.ExceptionTable))
	}
	exc := deserCO.ExceptionTable[0]
	if exc.StartIP != 0 || exc.EndIP != 2 || exc.TargetIP != 3 || !exc.PushLasti {
		t.Errorf("ExceptionTable entry mismatch: %+v", exc)
	}

	// Verify validation function
	if !vm.ValidateFyc(header, srcSize, srcMTime, srcHash) {
		t.Errorf("ValidateFyc returned false for matching attributes")
	}
	if vm.ValidateFyc(header, srcSize+1, srcMTime, srcHash) {
		t.Errorf("ValidateFyc returned true for size mismatch")
	}
	if vm.ValidateFyc(header, srcSize, srcMTime+1, srcHash) {
		t.Errorf("ValidateFyc returned true for mtime mismatch")
	}
}

func TestFycFileSaveAndLoad(t *testing.T) {
	tempDir := t.TempDir()
	fycPath := filepath.Join(tempDir, "__pycache__", "sample.fyc")

	co := &vm.CodeObject{
		Name:   "test_mod",
		Code:   []uint32{vm.Encode(vm.OP_LOAD_CONST, 0, 0), vm.Encode(vm.OP_RETURN, 0, 0)},
		Consts: []val.Value{val.Int64Val(999)},
	}

	srcHash := vm.ComputeSourceHash([]byte("print(999)"))
	mtime := time.Now().UnixNano()
	err := vm.SaveFyc(fycPath, co, 10, mtime, srcHash)
	if err != nil {
		t.Fatalf("SaveFyc failed: %v", err)
	}

	loadedCO, header, err := vm.LoadFyc(fycPath)
	if err != nil {
		t.Fatalf("LoadFyc failed: %v", err)
	}
	if loadedCO.Name != "test_mod" {
		t.Errorf("loadedCO.Name = %q, want 'test_mod'", loadedCO.Name)
	}
	if !vm.ValidateFyc(header, 10, mtime, srcHash) {
		t.Errorf("loaded header validation failed")
	}
}

func TestFycCorruptedHeader(t *testing.T) {
	data := []byte("Not a fyc file with enough bytes to pass header length check 12345678901234567890")
	_, _, err := vm.DeserializeCodeObject(data)
	if err != vm.ErrInvalidFycMagic {
		t.Errorf("got error %v, want ErrInvalidFycMagic", err)
	}

	shortData := []byte("short")
	_, _, err = vm.DeserializeCodeObject(shortData)
	if err != vm.ErrCorruptFycData {
		t.Errorf("got error %v, want ErrCorruptFycData", err)
	}

	// Corrupted version
	validData, _ := vm.SerializeCodeObject(&vm.CodeObject{Name: "ok"}, 0, 0, [32]byte{})
	corruptVer := bytes.Clone(validData)
	corruptVer[4] = 99 // Bad version
	_, _, err = vm.DeserializeCodeObject(corruptVer)
	if err != vm.ErrUnsupportedFycVer {
		t.Errorf("got error %v, want ErrUnsupportedFycVer", err)
	}
}
