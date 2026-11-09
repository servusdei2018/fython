package vm

import (
	"context"
	"errors"

	"github.com/servusdei2018/fython/pkg/capi"
	"github.com/servusdei2018/fython/pkg/val"
)

func init() {
	capi.NativeChargeHook = func(ctx *val.ExecContext) {
		if ctx == nil {
			return
		}
		if vm, ok := ctx.VM.(*VM); ok && vm != nil {
			vm.chargeNative()
		}
	}
	capi.CheckContextFn = func(ctx *val.ExecContext) {
		if ctx == nil {
			return
		}
		if vm, ok := ctx.VM.(*VM); ok && vm != nil {
			vm.checkLimitsContext()
		}
	}
}

// Sandbox resource limits sentinel errors.
var (
	ErrStepLimitExceeded     = errors.New("instruction step quota exceeded")
	ErrTimeoutExceeded       = errors.New("execution timed out or cancelled")
	ErrMaxStackDepthExceeded = errors.New("maximum call stack depth exceeded")
)

// RuntimeLimits specifies optional resource constraints for VM execution.
type RuntimeLimits struct {
	Context               context.Context
	MaxSteps              uint64
	MaxStackDepth         int
	AllowNativeExtensions bool
	NativeStepCost        uint64
}

// SetLimits configures execution guardrails on the VM.
func (vm *VM) SetLimits(limits RuntimeLimits) {
	vm.limits = limits
	vm.limitsActive = true
	vm.stepCount = 0
}

// Limits returns the current RuntimeLimits on the VM.
func (vm *VM) Limits() RuntimeLimits {
	return vm.limits
}

// nativeStepCost returns the effective per-native-call charge.
func (vm *VM) nativeStepCost() uint64 {
	if vm.limits.NativeStepCost != 0 {
		return vm.limits.NativeStepCost
	}
	if vm.limits.MaxSteps > 0 {
		return 100
	}
	return 0
}

// chargeNative charges a native-boundary step cost and checks cancellation.
// It panics with the sentinel errors on quota/timeout so RunCodeWithLimits
// can convert them, mirroring the run-loop charging in vm.go.
func (vm *VM) chargeNative() {
	if vm == nil || !vm.limitsActive {
		return
	}
	if vm.limits.Context != nil {
		if err := vm.limits.Context.Err(); err != nil {
			vm.unhandledException = val.NewRuntimeError("execution timed out or cancelled")
			panic(ErrTimeoutExceeded)
		}
	}
	if w := vm.nativeStepCost(); w != 0 && vm.limits.MaxSteps > 0 {
		vm.stepCount += w
		if vm.stepCount >= vm.limits.MaxSteps {
			vm.unhandledException = val.NewRuntimeError("instruction step quota exceeded")
			panic(ErrStepLimitExceeded)
		}
	}
}

// checkLimitsContext re-checks cancellation immediately after a C-API call.
func (vm *VM) checkLimitsContext() {
	if vm == nil || !vm.limitsActive {
		return
	}
	if vm.limits.Context != nil {
		if err := vm.limits.Context.Err(); err != nil {
			vm.unhandledException = val.NewRuntimeError("execution timed out or cancelled")
			panic(ErrTimeoutExceeded)
		}
	}
}

// sandboxBlocksNative reports whether sandbox policy forbids native code.
func (vm *VM) sandboxBlocksNative() bool {
	return vm != nil && vm.limitsActive && !vm.limits.AllowNativeExtensions
}

// RunCodeWithLimits runs co subject to the provided limits.
func (vm *VM) RunCodeWithLimits(co *CodeObject, limits RuntimeLimits) (res val.Value, err error) {
	oldLimits := vm.limits
	oldActive := vm.limitsActive
	vm.limits = limits
	vm.limitsActive = true
	vm.stepCount = 0
	defer func() {
		vm.limits = oldLimits
		vm.limitsActive = oldActive
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
