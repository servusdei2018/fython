//go:build !opstats

package vm

import "io"

// RegStatsEnabled reports whether build-tagged register counters are active.
func RegStatsEnabled() bool { return false }

// RegOpExecCount returns 0 when opstats are disabled.
func RegOpExecCount(op RegOp) uint64 { return 0 }

// ResetRegStats is a no-op when opstats are disabled.
func ResetRegStats() {}

// DumpRegStats is a no-op when opstats are disabled.
func DumpRegStats(w io.Writer) {}

func recordRegOp(op RegOp) {}
