//go:build !amd64 || !linux

package vm

import "github.com/servusdei2018/fython/pkg/val"

type jitCode struct{}

func jitCompileRegCode(e *Engine, rc *RegCode, consts []val.Value) *jitCode {
	return nil
}

func (jc *jitCode) release() {}

func (rf *regFrame) runJIT(jc *jitCode) (val.Value, bool, bool) {
	return val.Value{}, false, false
}
