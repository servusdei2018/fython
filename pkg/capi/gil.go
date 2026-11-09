package capi

import (
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/servusdei2018/fython/pkg/val"
)

// Virtual GIL — cooperative C-extension mutex.
// Pure-Python goroutines remain lock-free; the lock is taken only when
// entering a C extension function (no GIL overhead on the hot path).
var (
	capiMu     sync.Mutex
	gilOwner   int64
	gilDepth   int
	activeCtx  atomic.Pointer[val.ExecContext]
	gilEnabled atomic.Bool
)

func EnableGIL()       { gilEnabled.Store(true) }
func GILEnabled() bool { return gilEnabled.Load() }

// goroutine ID via runtime.Stack parsing (no external dep).
func currentGID() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	// format: "goroutine 19 [running]:"
	var id int64
	start := len("goroutine ")
	for i := start; i < n && buf[i] >= '0' && buf[i] <= '9'; i++ {
		id = id*10 + int64(buf[i]-'0')
	}
	return id
}

// AcquireGIL binds ctx as the active C dispatch context and holds the mutex.
// Returns a release function. Re-entrant for the same goroutine.
func AcquireGIL(ctx *val.ExecContext) func() {
	gid := currentGID()
	capiMu.Lock()
	if gilOwner == gid {
		gilDepth++
		capiMu.Unlock()
		prev := activeCtx.Load()
		activeCtx.Store(ctx)
		return func() {
			capiMu.Lock()
			gilDepth--
			if gilDepth == 0 {
				gilOwner = 0
				activeCtx.Store(prev)
				capiMu.Unlock()
			} else {
				capiMu.Unlock()
			}
		}
	}
	gilOwner = gid
	gilDepth = 1
	capiMu.Unlock()
	prev := activeCtx.Load()
	activeCtx.Store(ctx)
	return func() {
		capiMu.Lock()
		gilDepth--
		if gilDepth <= 0 {
			gilDepth = 0
			gilOwner = 0
			activeCtx.Store(prev)
		}
		capiMu.Unlock()
	}
}

// CurrentContext returns the ctx bound by the current GIL holder.
// Because the GIL serializes all C-API activity, a single global suffices.
func CurrentContext() *val.ExecContext {
	if p := activeCtx.Load(); p != nil {
		return p
	}
	return nil
}

// SetError stores exc on the current context (PyErr_*).
func SetError(exc val.Value) {
	if ctx := CurrentContext(); ctx != nil {
		ctx.Raise(exc)
	}
}

// ClearError clears the current context error indicator.
func ClearError() {
	if ctx := CurrentContext(); ctx != nil {
		ctx.ClearException()
	}
}

// HasError reports whether an error is set on the current context.
func HasError() bool {
	if ctx := CurrentContext(); ctx != nil {
		return ctx.HasException()
	}
	return false
}

// ErrOccurred returns the current exception value or None.
func ErrOccurred() val.Value {
	if ctx := CurrentContext(); ctx != nil && ctx.HasException() {
		return ctx.ActiveException
	}
	return val.NoneVal()
}

// TryWithGIL executes fn while holding the GIL with ctx bound.
func TryWithGIL(ctx *val.ExecContext, fn func()) {
	release := AcquireGIL(ctx)
	defer release()
	fn()
}
