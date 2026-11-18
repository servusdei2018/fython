//go:build !icstats

package vm

// ICStatsEnabled reports whether build-tagged IC counters are active.
func ICStatsEnabled() bool { return false }

// ICSlotHitCount returns 0 when icstats are disabled.
func ICSlotHitCount(k ICKind) uint64 { return 0 }

// ResetICStats is a no-op when icstats are disabled.
func ResetICStats() {}

func recordICSlowHit(kind ICKind)  {}
func recordAttrPICHit()            {}
func recordLoadAttrSlow()          {}
func recordStoreAttrSlow()         {}
func recordLoadMethodSlow()        {}
func recordGenStackPool(hit bool)  {}
func recordFrameSlabPool(hit bool) {}
func recordGenCellPool(hit bool)   {}
