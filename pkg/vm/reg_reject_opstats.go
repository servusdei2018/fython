//go:build opstats

package vm

import (
	"fmt"
	"io"
	"sort"
	"sync"
)

// RegRejectEntry aggregates one rejection cause for TryCompileRegCode.
type RegRejectEntry struct {
	Func   string
	Reason string
	Count  uint64
}

var (
	regRejectMu     sync.Mutex
	regRejectCounts = map[string]*RegRejectEntry{}
)

// recordRegReject notes a TryCompileRegCode rejection. It is a no-op unless
// the opstats tag is enabled. co may be nil.
func recordRegReject(co *CodeObject, reason string) {
	name := "<nil>"
	if co != nil {
		if co.QualName != "" {
			name = co.QualName
		} else if co.Name != "" {
			name = co.Name
		} else {
			name = "<anon>"
		}
		if co.FileName != "" {
			name += " @ " + co.FileName
		}
	}
	key := name + "|" + reason
	regRejectMu.Lock()
	e, ok := regRejectCounts[key]
	if !ok {
		e = &RegRejectEntry{Func: name, Reason: reason}
		regRejectCounts[key] = e
	}
	e.Count++
	regRejectMu.Unlock()
}

// DumpRegRejects writes the top rejection causes sorted by count.
func DumpRegRejects(w io.Writer) {
	regRejectMu.Lock()
	entries := make([]*RegRejectEntry, 0, len(regRejectCounts))
	for _, e := range regRejectCounts {
		cp := *e
		entries = append(entries, &cp)
	}
	regRejectMu.Unlock()
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Count != entries[j].Count {
			return entries[i].Count > entries[j].Count
		}
		if entries[i].Func != entries[j].Func {
			return entries[i].Func < entries[j].Func
		}
		return entries[i].Reason < entries[j].Reason
	})
	fmt.Fprintf(w, "=== Fython RegCode Rejects (distinct=%d) ===\n", len(entries))
	var total uint64
	for _, e := range entries {
		total += e.Count
	}
	fmt.Fprintf(w, "total rejects=%d\n", total)
	n := len(entries)
	if n > 50 {
		n = 50
	}
	for i := 0; i < n; i++ {
		e := entries[i]
		pct := float64(e.Count) * 100 / float64(total)
		if total == 0 {
			pct = 0
		}
		fmt.Fprintf(w, "%3d. %-60s %-42s %8d %6.2f%%\n", i+1, e.Func, e.Reason, e.Count, pct)
	}
}

// ResetRegRejects clears rejection counters.
func ResetRegRejects() {
	regRejectMu.Lock()
	regRejectCounts = map[string]*RegRejectEntry{}
	regRejectMu.Unlock()
}
