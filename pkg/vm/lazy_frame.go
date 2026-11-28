package vm

import (
	"github.com/servusdei2018/fython/pkg/val"
)

func init() {
	val.LazyFrameHook = materializeLazyFrame
}

// materializeLazyFrame implements val.LazyFrameHook: it builds the *PyFrame
// for a lazy traceback node on first tb_frame access and caches it with
// CompareAndSwap, so concurrent readers of a shared traceback observe one
// identical frame (tb.tb_frame is tb.tb_frame) under -race. The frame is
// detached and snapshot-free, exactly matching deferred-unwind frames:
// f_back is None, f_locals is empty, and f_globals/f_code/f_lineno/f_lasti
// serve from the node's lightweight metadata.
func materializeLazyFrame(ctx *val.ExecContext, tb *val.PyTraceback) val.Value {
	if tb == nil {
		return val.NoneVal()
	}
	if !tb.Frame.IsNone() {
		return tb.Frame
	}
	if f := tb.CachedFrame.Load(); f != nil {
		return val.NewFrameVal(f)
	}
	var vm *VM
	if ctx != nil && ctx.VM != nil {
		vm, _ = ctx.VM.(*VM)
	}
	if vm == nil {
		return val.NoneVal()
	}
	f := val.NewPyFrame(vm, -1, 0, tb.Code, tb.Globals, nil)
	f.IP = tb.LastI
	f.Detached = true
	if tb.CachedFrame.CompareAndSwap(nil, f) {
		return val.FrameValOf(f)
	}
	return val.FrameValOf(tb.CachedFrame.Load())
}
