//go:build icstats

package vm

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/servusdei2018/fython/pkg/val"
)

// Receiver category indices for opLoadAttrSlow breakdown.
const (
	icRecvInstance = iota
	icRecvTuple
	icRecvType
	icRecvSuper
	icRecvModule
	icRecvOther
	icRecvCount
)

var icRecvNames = [icRecvCount]string{
	"instance",
	"tuple",
	"type",
	"super",
	"module",
	"other",
}

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

	LoadAttrInlinePropHits atomic.Uint64
	LoadAttrSlowByRecv     [icRecvCount]atomic.Uint64
	LoadAttrSlowColdByRecv [icRecvCount]atomic.Uint64
	GetAttrCalls           atomic.Uint64
	GetAttrHits            atomic.Uint64
	GetAttrMissingDefault  atomic.Uint64
	GetAttrMissingRaise    atomic.Uint64
	HasAttrCalls           atomic.Uint64
	HasAttrHits            atomic.Uint64
	HasAttrMisses          atomic.Uint64

	mu                 sync.Mutex
	loadAttrMissDetail map[string]uint64
	getAttrDetail      map[string]uint64
	hasAttrDetail      map[string]uint64
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

// LoadAttrInlinePropHitCount returns the inline @property dispatch hit counter.
func LoadAttrInlinePropHitCount() uint64 {
	return GlobalICStats.LoadAttrInlinePropHits.Load()
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
	GlobalICStats.LoadAttrInlinePropHits.Store(0)
	for i := range GlobalICStats.LoadAttrSlowByRecv {
		GlobalICStats.LoadAttrSlowByRecv[i].Store(0)
		GlobalICStats.LoadAttrSlowColdByRecv[i].Store(0)
	}
	GlobalICStats.GetAttrCalls.Store(0)
	GlobalICStats.GetAttrHits.Store(0)
	GlobalICStats.GetAttrMissingDefault.Store(0)
	GlobalICStats.GetAttrMissingRaise.Store(0)
	GlobalICStats.HasAttrCalls.Store(0)
	GlobalICStats.HasAttrHits.Store(0)
	GlobalICStats.HasAttrMisses.Store(0)
	GlobalICStats.mu.Lock()
	GlobalICStats.loadAttrMissDetail = nil
	GlobalICStats.getAttrDetail = nil
	GlobalICStats.hasAttrDetail = nil
	GlobalICStats.mu.Unlock()
}

func recordICSlowHit(kind ICKind) {
	if uint(kind) < uint(len(GlobalICStats.ICSlotHits)) {
		GlobalICStats.ICSlotHits[kind].Add(1)
	}
}

func recordLoadAttrInlinePropHit() {
	GlobalICStats.LoadAttrInlinePropHits.Add(1)
	GlobalICStats.ICSlotHits[ICKindProperty].Add(1)
}

func recordAttrPICHit() {
	GlobalICStats.AttrPICHits.Add(1)
}

func recordLoadAttrSlow() {
	GlobalICStats.LoadAttrSlowEntries.Add(1)
}

func recordLoadAttrSlowRecv(cat int) {
	if uint(cat) < icRecvCount {
		GlobalICStats.LoadAttrSlowByRecv[cat].Add(1)
	}
}

func recordLoadAttrSlowCold(cat int, obj, attrName val.Value) {
	if uint(cat) < icRecvCount {
		GlobalICStats.LoadAttrSlowColdByRecv[cat].Add(1)
	}
	tpName := "<nil>"
	if tp := obj.Type(); tp != nil {
		tpName = tp.Name
	}
	attrStr := ""
	if attrName.IsAnyString() {
		attrStr = attrName.AsString()
	}
	key := icRecvNames[cat] + ":" + tpName + "." + attrStr
	GlobalICStats.mu.Lock()
	if GlobalICStats.loadAttrMissDetail == nil {
		GlobalICStats.loadAttrMissDetail = make(map[string]uint64)
	}
	GlobalICStats.loadAttrMissDetail[key]++
	GlobalICStats.mu.Unlock()
}

func recordGetAttrCall(obj, attrName val.Value, hit, hasDefault bool) {
	GlobalICStats.GetAttrCalls.Add(1)
	status := "hit"
	if hit {
		GlobalICStats.GetAttrHits.Add(1)
	} else if hasDefault {
		GlobalICStats.GetAttrMissingDefault.Add(1)
		status = "miss_def"
	} else {
		GlobalICStats.GetAttrMissingRaise.Add(1)
		status = "miss_err"
	}
	tpName := "<nil>"
	if tp := obj.Type(); tp != nil {
		tpName = tp.Name
	}
	attrStr := ""
	if attrName.IsAnyString() {
		attrStr = attrName.AsString()
	}
	key := status + ":" + tpName + "." + attrStr
	GlobalICStats.mu.Lock()
	if GlobalICStats.getAttrDetail == nil {
		GlobalICStats.getAttrDetail = make(map[string]uint64)
	}
	GlobalICStats.getAttrDetail[key]++
	GlobalICStats.mu.Unlock()
}

func recordHasAttrCall(obj, attrName val.Value, hit bool) {
	GlobalICStats.HasAttrCalls.Add(1)
	status := "miss"
	if hit {
		GlobalICStats.HasAttrHits.Add(1)
		status = "hit"
	} else {
		GlobalICStats.HasAttrMisses.Add(1)
	}
	tpName := "<nil>"
	if tp := obj.Type(); tp != nil {
		tpName = tp.Name
	}
	attrStr := ""
	if attrName.IsAnyString() {
		attrStr = attrName.AsString()
	}
	key := status + ":" + tpName + "." + attrStr
	GlobalICStats.mu.Lock()
	if GlobalICStats.hasAttrDetail == nil {
		GlobalICStats.hasAttrDetail = make(map[string]uint64)
	}
	GlobalICStats.hasAttrDetail[key]++
	GlobalICStats.mu.Unlock()
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

var icKindNames = [...]string{
	ICKindNone:               "None",
	ICKindInstanceSlot:       "InstanceSlot",
	ICKindClassMethod:        "ClassMethod",
	ICKindDataDescriptor:     "DataDescriptor",
	ICKindProperty:           "Property",
	ICKindClassAttr:          "ClassAttr",
	ICKindGlobal:             "Global",
	ICKindBuiltin:            "Builtin",
	ICKindModuleAttr:         "ModuleAttr",
	ICKindSlotMember:         "SlotMember",
	ICKindNativeDescr:        "NativeDescr",
	ICKindClassFunc:          "ClassFunc",
	ICKindTupleGetter:        "TupleGetter",
	ICKindTypeAttr:           "TypeAttr",
	ICKindTypeMethod:         "TypeMethod",
	ICKindSuperMethod:        "SuperMethod",
	ICKindPropertySet:        "PropertySet",
	ICKindInstanceTransition: "InstanceTransition",
	ICKindCompareDunder:      "CompareDunder",
	ICKindMethodDescr:        "MethodDescr",
}

func dumpTopMap(w io.Writer, title string, m map[string]uint64, limit int) {
	if len(m) == 0 {
		return
	}
	type kv struct {
		k string
		v uint64
	}
	items := make([]kv, 0, len(m))
	for k, v := range m {
		items = append(items, kv{k, v})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].v != items[j].v {
			return items[i].v > items[j].v
		}
		return items[i].k < items[j].k
	})
	fmt.Fprintf(w, "  %s (top %d of %d):\n", title, min(limit, len(items)), len(items))
	for i := 0; i < len(items) && i < limit; i++ {
		fmt.Fprintf(w, "    %10d  %s\n", items[i].v, items[i].k)
	}
}

// DumpICStats writes a human-readable summary of IC counters to w.
func DumpICStats(w io.Writer) {
	fmt.Fprintf(w, "=== FYTHON ICSTATS ===\n")
	fmt.Fprintf(w, "LoadAttrSlowEntries: %d (inlinePropHits=%d)\n",
		GlobalICStats.LoadAttrSlowEntries.Load(), GlobalICStats.LoadAttrInlinePropHits.Load())
	fmt.Fprintf(w, "StoreAttrSlowEntries: %d, LoadMethodSlowEntries: %d\n",
		GlobalICStats.StoreAttrSlowEntries.Load(), GlobalICStats.LoadMethodSlowEntries.Load())
	fmt.Fprintf(w, "By receiver (entries / cold_misses):\n")
	for i := range icRecvCount {
		fmt.Fprintf(w, "  %-10s %10d / %10d\n",
			icRecvNames[i],
			GlobalICStats.LoadAttrSlowByRecv[i].Load(),
			GlobalICStats.LoadAttrSlowColdByRecv[i].Load())
	}
	fmt.Fprintf(w, "ICSlotHits:\n")
	for i := range len(icKindNames) {
		if c := GlobalICStats.ICSlotHits[i].Load(); c > 0 {
			fmt.Fprintf(w, "  %-20s %10d\n", icKindNames[i], c)
		}
	}
	fmt.Fprintf(w, "getattr: calls=%d hits=%d miss_def=%d miss_err=%d\n",
		GlobalICStats.GetAttrCalls.Load(),
		GlobalICStats.GetAttrHits.Load(),
		GlobalICStats.GetAttrMissingDefault.Load(),
		GlobalICStats.GetAttrMissingRaise.Load())
	fmt.Fprintf(w, "hasattr: calls=%d hits=%d misses=%d\n",
		GlobalICStats.HasAttrCalls.Load(),
		GlobalICStats.HasAttrHits.Load(),
		GlobalICStats.HasAttrMisses.Load())

	GlobalICStats.mu.Lock()
	defer GlobalICStats.mu.Unlock()
	dumpTopMap(w, "opLoadAttrSlow cold misses", GlobalICStats.loadAttrMissDetail, 20)
	dumpTopMap(w, "getattr detail", GlobalICStats.getAttrDetail, 20)
	dumpTopMap(w, "hasattr detail", GlobalICStats.hasAttrDetail, 20)
}
