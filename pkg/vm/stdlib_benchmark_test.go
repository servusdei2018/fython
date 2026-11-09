package vm_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

// BenchmarkNativeJSONLoads benchmarks parsing an array of 1,000 JSON objects with short keys (<= 7 bytes).
func BenchmarkNativeJSONLoads(b *testing.B) {
	// Generate JSON array containing 1,000 objects with keys: "id", "name", "age", "active", "score" (all <= 7 bytes)
	var sb strings.Builder
	sb.WriteString("[\n")
	for i := range 1000 {
		if i > 0 {
			sb.WriteString(",\n")
		}
		fmt.Fprintf(&sb, `  {"id": %d, "name": "usr%d", "age": %d, "active": true, "score": 95.5}`, i, i, 20+(i%50))
	}
	sb.WriteString("\n]")
	jsonBytes := []byte(sb.String())

	engine := vm.NewEngine()
	v := engine.AcquireVM()
	defer engine.ReleaseVM(v)
	jsonMod, err := v.ImportModule("json", 0, true, nil)
	if err != nil {
		b.Fatalf("failed to import json: %v", err)
	}
	loadsFn, ok := jsonMod.AsModule().Dict.Get(val.InternString("loads"))
	if !ok {
		b.Fatalf("json.loads not found")
	}
	arg := []val.Value{val.NewBytesVal(jsonBytes)}

	b.ReportAllocs()
	for b.Loop() {
		res := loadsFn.AsBuiltinFunc().Fn(nil, loadsFn, arg, nil)
		_ = res
	}
}

// TestNativeJSONLoadsZeroHeapStringsForShortKeys asserts that parsing 1,000 JSON objects
// where keys are <= 7 bytes produces KindInlineStr keys with 0 heap strings allocated for the keys.
func TestNativeJSONLoadsZeroHeapStringsForShortKeys(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("[\n")
	for i := range 1000 {
		if i > 0 {
			sb.WriteString(",\n")
		}
		fmt.Fprintf(&sb, `  {"id": %d, "name": "usr%d", "age": %d, "active": true, "score": 95.5}`, i, i, 20+(i%50))
	}
	sb.WriteString("\n]")
	jsonBytes := []byte(sb.String())

	engine := vm.NewEngine()
	v := engine.AcquireVM()
	defer engine.ReleaseVM(v)
	jsonMod, err := v.ImportModule("json", 0, true, nil)
	if err != nil {
		t.Fatalf("failed to import json: %v", err)
	}
	loadsFn, _ := jsonMod.AsModule().Dict.Get(val.InternString("loads"))
	res := loadsFn.AsBuiltinFunc().Fn(nil, loadsFn, []val.Value{val.NewBytesVal(jsonBytes)}, nil)

	if !res.IsList() {
		t.Fatalf("expected list result, got %v", res.Kind())
	}
	items := res.AsList().Items
	if len(items) != 1000 {
		t.Fatalf("expected 1000 items, got %d", len(items))
	}

	// Verify all keys across all 1,000 objects are KindInlineStr (0 heap strings!)
	for objIdx, objVal := range items {
		dict := objVal.AsDict()
		for _, entry := range dict.Entries {
			if !entry.Active {
				continue
			}
			key := entry.Key
			if !key.IsInlineStr() {
				t.Fatalf("object %d: key %q has kind %v, expected KindInlineStr (zero heap string)", objIdx, key.AsString(), key.Kind())
			}
		}
	}
}
