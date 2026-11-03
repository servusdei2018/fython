package vm_test

import (
	"testing"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// BenchmarkMemoryViewSlice verifies that slicing a 10MB bytes or memoryview object
// copies 0 bytes of payload memory.
func BenchmarkMemoryViewSlice(b *testing.B) {
	const payloadSize = 10 * 1024 * 1024 // 10 MB
	raw := make([]byte, payloadSize)
	for i := range raw {
		raw[i] = byte(i % 256)
	}

	bytesVal := val.NewBytesVal(raw)
	mvVal := val.NewMemoryViewVal(bytesVal)
	sl := val.NewSliceVal(val.Int64Val(100), val.Int64Val(100000), val.NoneVal())

	b.ReportAllocs()
	for b.Loop() {
		sub := val.GetItemCtx(nil, mvVal, sl)
		_ = sub
	}
}

// TestMemoryViewSliceZeroPayloadCopied verifies that slicing a 10MB memoryview
// 1,000,000 times copies 0 bytes of the underlying payload memory.
func TestMemoryViewSliceZeroPayloadCopied(t *testing.T) {
	const payloadSize = 10 * 1024 * 1024 // 10 MB
	raw := make([]byte, payloadSize)
	for i := range raw {
		raw[i] = byte(i % 256)
	}

	bytesVal := val.NewBytesVal(raw)
	mvVal := val.NewMemoryViewVal(bytesVal)
	sl := val.NewSliceVal(val.Int64Val(1000), val.Int64Val(2000), val.NoneVal())

	const iterations = 1000000
	for i := 0; i < iterations; i++ {
		sub := val.GetItemCtx(nil, mvVal, sl)
		mv := sub.AsMemoryView()
		// Pointer must point directly into raw at offset 1000
		if unsafe.Pointer(&raw[1000]) != unsafe.Pointer(&mv.Buf[0]) {
			t.Fatalf("sub-slice payload was copied: pointer mismatch")
		}
	}
}
