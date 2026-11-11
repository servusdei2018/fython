//go:build !linux && !darwin

package val

// byteMapping describes an anonymous mapping that backs a Python buffer.
// Platforms without anonymous mappings never create one.
type byteMapping struct {
	base *byte
	size int
}

// allocZeroedBytes returns a zero-initialised buffer of n bytes.
//
// Platforms without anonymous mappings always allocate on the Go heap.
func allocZeroedBytes(n int) ([]byte, *byteMapping) {
	return make([]byte, n), nil
}

// attachByteMapping is a no-op on platforms without anonymous mappings.
func attachByteMapping[T any](obj *T, m *byteMapping) {}
