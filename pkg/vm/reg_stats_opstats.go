//go:build opstats

package vm

import (
	"fmt"
	"io"
	"sort"
	"sync/atomic"
)

// RegStats holds build-tagged register-instruction execution counters.
type RegStats struct {
	Counts [256]atomic.Uint64
}

// GlobalRegStats aggregates dynamic register instruction counts.
var GlobalRegStats RegStats

// RegStatsEnabled reports whether build-tagged register counters are active.
func RegStatsEnabled() bool { return true }

// RegOpExecCount returns the recorded execution count for op.
func RegOpExecCount(op RegOp) uint64 {
	return GlobalRegStats.Counts[op].Load()
}

// ResetRegStats zeroes all register instruction counters.
func ResetRegStats() {
	for i := range GlobalRegStats.Counts {
		GlobalRegStats.Counts[i].Store(0)
	}
}

func recordRegOp(op RegOp) {
	GlobalRegStats.Counts[op].Add(1)
}

// DumpRegStats writes a sorted summary of register instruction frequencies.
func DumpRegStats(w io.Writer) {
	type entry struct {
		op    RegOp
		count uint64
	}
	var entries []entry
	var total uint64
	for i := range GlobalRegStats.Counts {
		if c := GlobalRegStats.Counts[i].Load(); c > 0 {
			entries = append(entries, entry{op: RegOp(i), count: c})
			total += c
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].count != entries[j].count {
			return entries[i].count > entries[j].count
		}
		return entries[i].op < entries[j].op
	})
	fmt.Fprintf(w, "=== Fython RegCode Stats (total=%d, distinct=%d) ===\n", total, len(entries))
	if total == 0 {
		return
	}
	var cum uint64
	for idx, e := range entries {
		cum += e.count
		pct := float64(e.count) * 100 / float64(total)
		cumPct := float64(cum) * 100 / float64(total)
		fmt.Fprintf(w, "%3d. %-42s %12d %6.2f%% %7.2f%%\n", idx+1, e.op.String(), e.count, pct, cumPct)
	}
}
