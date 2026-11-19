//go:build !opstats

package vm

import "io"

// OpStatsEnabled reports whether build-tagged opcode counters are active.
func OpStatsEnabled() bool { return false }

// OpExecCount returns 0 when opstats are disabled.
func OpExecCount(op Opcode) uint64 { return 0 }

// ResetOpStats is a no-op when opstats are disabled.
func ResetOpStats() {}

// DumpOpStats is a no-op when opstats are disabled.
func DumpOpStats(w io.Writer) {}

func recordOpcode(op Opcode) {}
