package vm

import (
	"context"
	"errors"

	"github.com/servusdei2018/fython/pkg/val"
)

// Sandbox resource limits sentinel errors.
var (
	ErrStepLimitExceeded     = errors.New("instruction step quota exceeded")
	ErrTimeoutExceeded       = errors.New("execution timed out or cancelled")
	ErrMaxStackDepthExceeded = errors.New("maximum call stack depth exceeded")
)

// RuntimeLimits specifies optional resource constraints for VM execution.
type RuntimeLimits struct {
	Context       context.Context
	MaxSteps      uint64
	MaxStackDepth int
}

// SetLimits configures execution guardrails on the VM.
func (vm *VM) SetLimits(limits RuntimeLimits) {
	vm.limits = limits
	vm.stepCount = 0
}

// Limits returns the current RuntimeLimits on the VM.
func (vm *VM) Limits() RuntimeLimits {
	return vm.limits
}

// RunCodeWithLimits runs co subject to the provided limits.
func (vm *VM) RunCodeWithLimits(co *CodeObject, limits RuntimeLimits) (res val.Value, err error) {
	oldLimits := vm.limits
	vm.limits = limits
	vm.stepCount = 0
	defer func() {
		vm.limits = oldLimits
		if r := recover(); r != nil {
			if e, ok := r.(error); ok && (errors.Is(e, ErrTimeoutExceeded) || errors.Is(e, ErrStepLimitExceeded) || errors.Is(e, ErrMaxStackDepthExceeded)) {
				err = e
				res = val.NoneVal()
				return
			}
			panic(r)
		}
	}()

	res = vm.RunCode(co)
	if limits.Context != nil && limits.Context.Err() != nil {
		return val.NoneVal(), ErrTimeoutExceeded
	}
	return res, nil
}
