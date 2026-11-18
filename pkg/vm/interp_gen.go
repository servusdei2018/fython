package vm

import (
	"fmt"

	"github.com/servusdei2018/fython/pkg/val"
)

//go:noinline
func (vm *VM) opYieldValue(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	sp--
	yieldVal := stack[sp]
	if st.activeGen != nil {
		if vm.activeJoinGen == st.activeGen && st.frameIdx == st.startFrameIdx {
			vm.appendJoinItem(yieldVal)
			stack[sp] = val.NoneVal()
			sp++
			ip++
			return ip, sp, ctlNext
		}
		st.activeGen.YieldVal = yieldVal
		st.activeGen.State = val.GenSuspended
		st.activeGen.SP = sp
		st.activeGen.IP = ip + 1
		st.result = yieldVal
		return ip, sp, ctlExit
	}
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opYieldFrom(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	subiter := stack[sp-1]
	if !subiter.IsIter() && !subiter.IsGenerator() && !subiter.IsCoroutine() {
		if tpIter := subiter.Type().Slots.TpIter; tpIter != nil {
			subiter = tpIter(&vm.execCtx, subiter)
			stack[sp-1] = subiter
		}
	}
	sentVal := val.NoneVal()
	if st.activeGen != nil {
		sentVal = st.activeGen.LastSent
		st.activeGen.LastSent = val.NoneVal()
	} else if st.activeCoro != nil {
		sentVal = st.activeCoro.LastSent
		st.activeCoro.LastSent = val.NoneVal()
	}
	if st.activeGen != nil && vm.activeJoinGen == st.activeGen && st.frameIdx == st.startFrameIdx {
		for {
			var nextVal val.Value
			var status val.IterStatus
			if subiter.IsGenerator() {
				nextVal, status = subiter.AsGenerator().Send(&vm.execCtx, val.NoneVal())
			} else if subiter.IsCoroutine() {
				nextVal, status = vm.resumeCoroutine(&vm.execCtx, subiter.AsCoroutine(), val.NoneVal())
			} else if tpIterNext := subiter.Type().Slots.TpIterNext; tpIterNext != nil {
				nextVal, status = tpIterNext(&vm.execCtx, subiter)
			} else {
				break
			}
			if status == val.IterYielded {
				vm.appendJoinItem(nextVal)
				continue
			}
			if status == val.IterExhausted {
				retVal := val.NoneVal()
				if subiter.IsGenerator() {
					retVal = subiter.AsGenerator().RetVal
				} else if subiter.IsCoroutine() {
					retVal = subiter.AsCoroutine().RetVal
					vm.releaseCoroutine(subiter.AsCoroutine())
				}
				stack[sp-1] = retVal
				break
			}
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			break
		}
		ip++
		return ip, sp, ctlReload
	}
	var nextVal val.Value
	var status val.IterStatus
	if subiter.IsGenerator() {
		nextVal, status = subiter.AsGenerator().Send(&vm.execCtx, sentVal)
	} else if subiter.IsCoroutine() {
		nextVal, status = vm.resumeCoroutine(&vm.execCtx, subiter.AsCoroutine(), sentVal)
	} else if tpIterNext := subiter.Type().Slots.TpIterNext; tpIterNext != nil {
		nextVal, status = tpIterNext(&vm.execCtx, subiter)
	} else {
		ip++
		return ip, sp, ctlReload
	}

	switch {
	case status == val.IterYielded:
		if st.activeGen != nil {
			st.activeGen.YieldVal = nextVal
			st.activeGen.State = val.GenSuspended
			st.activeGen.SP = sp
			st.activeGen.IP = ip
			st.result = nextVal
			return ip, sp, ctlExit
		}
		if st.activeCoro != nil {
			st.activeCoro.YieldVal = nextVal
			st.activeCoro.State = val.CoroSuspended
			st.activeCoro.SP = sp
			st.activeCoro.IP = ip
			st.result = nextVal
			return ip, sp, ctlExit
		}
	case status == val.IterExhausted:
		retVal := val.NoneVal()
		if subiter.IsGenerator() {
			retVal = subiter.AsGenerator().RetVal
		} else if subiter.IsCoroutine() {
			retVal = subiter.AsCoroutine().RetVal
			vm.releaseCoroutine(subiter.AsCoroutine())
		}
		stack[sp-1] = retVal
		ip++
	case vm.execCtx.HasException():
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	return ip, sp, ctlReload
}

//go:noinline
func (vm *VM) opGetAwaitable(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	top := stack[sp-1]
	if top.IsCoroutine() || top.IsFuture() || top.IsTask() {
		ip++
		return ip, sp, ctlNext
	}
	if top.IsInstance() {
		inst := top.AsInstance()
		if m, ok := inst.GetAttr(nameAwait); ok {
			stack[sp-1] = vm.Vectorcall(m, nil, nil)
			ip++
			return ip, sp, ctlNext
		}
	}
	if tpIter := top.Type().Slots.TpIter; tpIter != nil {
		stack[sp-1] = tpIter(&vm.execCtx, top)
		ip++
		return ip, sp, ctlNext
	}
	exc := val.NewTypeError(fmt.Sprintf("object %s can't be used in 'await' expression", top.Type().Name))
	st.raiseExc = exc
	return ip, sp, ctlRaise

}

//go:noinline
func (vm *VM) opAwait(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	aw := stack[sp-1]
	if aw.IsFuture() || aw.IsTask() {
		fut := aw.AsFuture()
		if aw.IsTask() {
			fut = &aw.AsTask().Future
		}
		if fut.IsDone() {
			res, exc := fut.Result()
			if !exc.IsNone() && exc.HeaderPtr() != nil {
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			stack[sp-1] = res
			ip++
			return ip, sp, ctlReload
		}
		if st.activeCoro != nil {
			st.activeCoro.YieldVal = aw
			st.activeCoro.State = val.CoroSuspended
			st.activeCoro.SP = sp
			st.activeCoro.IP = ip
			st.result = aw
			return ip, sp, ctlExit
		}
	} else if aw.IsCoroutine() {
		childCoro := aw.AsCoroutine()
		sentVal := val.NoneVal()
		if st.activeCoro != nil {
			sentVal = st.activeCoro.LastSent
			st.activeCoro.LastSent = val.NoneVal()
		}
		_, status := vm.resumeCoroutine(&vm.execCtx, childCoro, sentVal)
		switch status {
		case val.IterExhausted:
			stack[sp-1] = childCoro.RetVal
			vm.releaseCoroutine(childCoro)
			ip++
			return ip, sp, ctlReload
		case val.IterYielded:
			if st.activeCoro != nil {
				st.activeCoro.YieldVal = childCoro.YieldVal
				st.activeCoro.State = val.CoroSuspended
				st.activeCoro.SP = sp
				st.activeCoro.IP = ip
				st.result = childCoro.YieldVal
				return ip, sp, ctlExit
			}
		default:
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
		}
	} else if aw.IsGenerator() {
		subiter := aw.AsGenerator()
		sentVal := val.NoneVal()
		if st.activeCoro != nil {
			sentVal = st.activeCoro.LastSent
			st.activeCoro.LastSent = val.NoneVal()
		}
		nextVal, status := subiter.Send(&vm.execCtx, sentVal)
		switch status {
		case val.IterExhausted:
			stack[sp-1] = subiter.RetVal
			ip++
			return ip, sp, ctlReload
		case val.IterYielded:
			if st.activeCoro != nil {
				st.activeCoro.YieldVal = nextVal
				st.activeCoro.State = val.CoroSuspended
				st.activeCoro.SP = sp
				st.activeCoro.IP = ip
				st.result = nextVal
				return ip, sp, ctlExit
			}
		default:
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
		}
	} else if tpIterNext := aw.Type().Slots.TpIterNext; tpIterNext != nil {
		nextVal, status := tpIterNext(&vm.execCtx, aw)
		switch status {
		case val.IterExhausted:
			stack[sp-1] = nextVal
			ip++
			return ip, sp, ctlReload
		case val.IterYielded:
			if st.activeCoro != nil {
				st.activeCoro.YieldVal = nextVal
				st.activeCoro.State = val.CoroSuspended
				st.activeCoro.SP = sp
				st.activeCoro.IP = ip
				st.result = nextVal
				return ip, sp, ctlExit
			}
		}
	} else {
		ip++
		return ip, sp, ctlReload
	}
	return ip, sp, ctlReload
}
