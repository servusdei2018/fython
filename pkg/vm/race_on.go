//go:build race

package vm

// IsRaceEnabled reports whether the binary was built with the Go race detector enabled.
const IsRaceEnabled = true
