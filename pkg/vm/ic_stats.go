//go:build icstats

package vm

import "sync/atomic"

// ICStats holds build-tagged counters used to verify inline-cache and pool hit rates.
type ICStats struct {
	ICSlotHits            [24]atomic.Uint64
	AttrPICHits           atomic.Uint64
	LoadAttrSlowEntries   atomic.Uint64
	StoreAttrSlowEntries  atomic.Uint64
	LoadMethodSlowEntries atomic.Uint64
	GenStackPoolHits      atomic.Uint64
	GenStackPoolMisses    atomic.Uint64
	FrameSlabPoolHits     atomic.Uint64
	FrameSlabPoolMisses   atomic.Uint64
	GenCellPoolHits       atomic.Uint64
	GenCellPoolMisses     atomic.Uint64
}

var GlobalICStats ICStats

// ICStatsEnabled reports whether build-tagged IC counters are active.
func ICStatsEnabled() bool { return true }

// ICSlotHitCount returns the hit counter for kind k.
func ICSlotHitCount(k ICKind) uint64 {
	if uint(k) < uint(len(GlobalICStats.ICSlotHits)) {
		return GlobalICStats.ICSlotHits[k].Load()
	}
	return 0
}

// ResetICStats zeroes all inline-cache and pool counters.
func ResetICStats() {
	for i := range GlobalICStats.ICSlotHits {
		GlobalICStats.ICSlotHits[i].Store(0)
	}
	GlobalICStats.AttrPICHits.Store(0)
	GlobalICStats.LoadAttrSlowEntries.Store(0)
	GlobalICStats.StoreAttrSlowEntries.Store(0)
	GlobalICStats.LoadMethodSlowEntries.Store(0)
	GlobalICStats.GenStackPoolHits.Store(0)
	GlobalICStats.GenStackPoolMisses.Store(0)
	GlobalICStats.FrameSlabPoolHits.Store(0)
	GlobalICStats.FrameSlabPoolMisses.Store(0)
	GlobalICStats.GenCellPoolHits.Store(0)
	GlobalICStats.GenCellPoolMisses.Store(0)
}

func recordICSlowHit(kind ICKind) {
	if uint(kind) < uint(len(GlobalICStats.ICSlotHits)) {
		GlobalICStats.ICSlotHits[kind].Add(1)
	}
}

func recordAttrPICHit() {
	GlobalICStats.AttrPICHits.Add(1)
}

func recordLoadAttrSlow() {
	GlobalICStats.LoadAttrSlowEntries.Add(1)
}

func recordStoreAttrSlow() {
	GlobalICStats.StoreAttrSlowEntries.Add(1)
}

func recordLoadMethodSlow() {
	GlobalICStats.LoadMethodSlowEntries.Add(1)
}

func recordGenStackPool(hit bool) {
	if hit {
		GlobalICStats.GenStackPoolHits.Add(1)
	} else {
		GlobalICStats.GenStackPoolMisses.Add(1)
	}
}

func recordFrameSlabPool(hit bool) {
	if hit {
		GlobalICStats.FrameSlabPoolHits.Add(1)
	} else {
		GlobalICStats.FrameSlabPoolMisses.Add(1)
	}
}

func recordGenCellPool(hit bool) {
	if hit {
		GlobalICStats.GenCellPoolHits.Add(1)
	} else {
		GlobalICStats.GenCellPoolMisses.Add(1)
	}
}
