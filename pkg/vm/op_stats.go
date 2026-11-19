//go:build opstats

package vm

import (
	"fmt"
	"io"
	"sort"
	"sync/atomic"
)

// OpStats holds build-tagged dynamic opcode execution counters.
type OpStats struct {
	Counts [256]atomic.Uint64
}

// GlobalOpStats aggregates dynamic opcode execution counts across all VMs.
var GlobalOpStats OpStats

// OpStatsEnabled reports whether build-tagged opcode counters are active.
func OpStatsEnabled() bool { return true }

// OpExecCount returns the recorded execution count for op.
func OpExecCount(op Opcode) uint64 {
	return GlobalOpStats.Counts[op].Load()
}

// ResetOpStats zeroes all opcode execution counters.
func ResetOpStats() {
	for i := range GlobalOpStats.Counts {
		GlobalOpStats.Counts[i].Store(0)
	}
}

func recordOpcode(op Opcode) {
	GlobalOpStats.Counts[op].Add(1)
}

// DumpOpStats writes a sorted summary of dynamic opcode frequencies to w.
func DumpOpStats(w io.Writer) {
	type entry struct {
		op    Opcode
		count uint64
	}
	var entries []entry
	var total uint64
	for i := range GlobalOpStats.Counts {
		if c := GlobalOpStats.Counts[i].Load(); c > 0 {
			entries = append(entries, entry{op: Opcode(i), count: c})
			total += c
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].count != entries[j].count {
			return entries[i].count > entries[j].count
		}
		return entries[i].op < entries[j].op
	})
	fmt.Fprintf(w, "=== Fython Opcode Stats (total=%d, distinct=%d) ===\n", total, len(entries))
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
