//go:build !icstats

package vm

import (
	"io"

	"github.com/servusdei2018/fython/pkg/val"
)

const (
	icRecvInstance = iota
	icRecvTuple
	icRecvType
	icRecvSuper
	icRecvModule
	icRecvOther
)

// ICStatsEnabled reports whether build-tagged IC counters are active.
func ICStatsEnabled() bool { return false }

// ICSlotHitCount returns 0 when icstats are disabled.
func ICSlotHitCount(k ICKind) uint64 { return 0 }

// LoadAttrInlinePropHitCount returns 0 when icstats are disabled.
func LoadAttrInlinePropHitCount() uint64 { return 0 }

// ResetICStats is a no-op when icstats are disabled.
func ResetICStats() {}

// DumpICStats is a no-op when icstats are disabled.
func DumpICStats(w io.Writer) {}

func recordICSlowHit(kind ICKind)                             {}
func recordLoadAttrInlinePropHit()                            {}
func recordAttrPICHit()                                       {}
func recordLoadAttrSlow()                                     {}
func recordLoadAttrSlowRecv(cat int)                          {}
func recordLoadAttrSlowCold(cat int, obj, attrName val.Value) {}
func recordGetAttrCall(obj, attrName val.Value, hit, hasDefault bool) {
}
func recordHasAttrCall(obj, attrName val.Value, hit bool) {}
func recordStoreAttrSlow()                                {}
func recordLoadMethodSlow()                               {}
func recordGenStackPool(hit bool)                         {}
func recordFrameSlabPool(hit bool)                        {}
func recordGenCellPool(hit bool)                          {}
