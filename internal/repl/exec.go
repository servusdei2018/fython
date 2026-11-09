package repl

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

// Result describes one completed execution.
type Result struct {
	// Output is everything Python wrote to sys.stdout (including the
	// single-mode expression echo).
	Output string
	// Traceback is the formatted traceback of an unhandled exception,
	// or "" when execution succeeded. Compile errors also surface
	// here because the REPL reports them through the same path.
	Traceback string
	// CompileError is the raw compile failure message, when any.
	CompileError string
	// Exit is non-nil when the code raised SystemExit.
	Exit *ExitError
	// Startup is true when the result came from the startup file.
	Startup bool
}

type job struct {
	src      string
	filename string
	mode     compiler.CompileMode
	startup  bool
	reply    chan Result
}

type completeJob struct {
	src    string
	cursor int
	reply  chan []Candidate
}

// Worker owns a single *vm.VM on one goroutine. All execution and all
// namespace introspection (completion) run on that goroutine; callers
// submit jobs over channels and never touch the VM directly.
type Worker struct {
	vm        *vm.VM
	optimize  int
	jobs      chan job
	complete  chan completeJob
	stop      chan struct{}
	inputHook func(prompt string) (string, error)
}

// SetInputHook installs the input() reader for this worker's VM.
func (w *Worker) SetInputHook(fn func(prompt string) (string, error)) {
	w.inputHook = fn
	w.vm.Engine().SetInputHook(fn)
}

// NewWorker starts the VM owner goroutine.
func NewWorker(v *vm.VM, optimize int) *Worker {
	w := &Worker{
		vm:       v,
		optimize: optimize,
		jobs:     make(chan job),
		complete: make(chan completeJob),
		stop:     make(chan struct{}),
	}
	go w.loop()
	return w
}

// Close stops the worker goroutine.
func (w *Worker) Close() { close(w.stop) }

// RequestInterrupt schedules a catchable KeyboardInterrupt in the running
// statement, if any.
func (w *Worker) RequestInterrupt() {
	w.vm.RequestInterrupt()
}

func (w *Worker) loop() {
	for {
		select {
		case <-w.stop:
			return
		case j := <-w.jobs:
			j.reply <- w.execute(j)
		case c := <-w.complete:
			c.reply <- w.completeAt(c.src, c.cursor)
		}
	}
}

// Execute runs src in "single" mode and waits for it to finish.
func (w *Worker) Execute(src, filename string) Result {
	return w.ExecuteMode(src, filename, compiler.ModeSingle)
}

// ExecuteMode runs src in the given compile mode.
func (w *Worker) ExecuteMode(src, filename string, mode compiler.CompileMode) Result {
	reply := make(chan Result, 1)
	w.jobs <- job{src: src, filename: filename, mode: mode, reply: reply}
	return <-reply
}

// ExecuteSource runs src in "exec" mode (startup file).
func (w *Worker) ExecuteSource(src, filename string) Result {
	reply := make(chan Result, 1)
	w.jobs <- job{src: src, filename: filename, mode: compiler.ModeExec, startup: true, reply: reply}
	return <-reply
}

// Complete returns completion candidates for the source/cursor pair.
func (w *Worker) Complete(src string, cursor int) []Candidate {
	reply := make(chan []Candidate, 1)
	w.complete <- completeJob{src: src, cursor: cursor, reply: reply}
	return <-reply
}

func (w *Worker) execute(j job) Result {
	res := Result{Startup: j.startup}

	co, err := compiler.CompileSourceModeWithOptimize([]byte(j.src), j.filename, j.mode, w.optimize)
	if err != nil {
		res.CompileError = err.Error()
		res.Traceback = syntaxTraceback(j.filename, err)
		return res
	}

	outText, errText := captureRun(w.vm, func() { w.vm.RunCode(co) })
	res.Output = outText
	if uerr := w.vm.UnhandledException(); !uerr.IsNone() && uerr.HeaderPtr() != nil {
		if exit := exitFromException(w.vm); exit != nil {
			res.Exit = exit
		} else {
			res.Traceback = errText + w.vm.FormatException(uerr)
		}
		w.vm.ClearUnhandledException()
	} else if errText != "" {
		res.Output += errText
	}
	return res
}

// captureRun substitutes sys.stdout/sys.stderr with pipe-backed streams
// for the duration of fn, returning what was written to each.
func captureRun(v *vm.VM, fn func()) (string, string) {
	sysVal, ok := v.GetModule("sys")
	if !ok || !sysVal.IsModule() {
		fn()
		return "", ""
	}
	sysDict := sysVal.AsModule().Dict
	prevOut, _ := sysDict.Get(val.InternString("stdout"))
	prevErr, _ := sysDict.Get(val.InternString("stderr"))

	outR, outW, outErr := os.Pipe()
	errR, errW, errErr := os.Pipe()
	if outErr != nil || errErr != nil {
		fn()
		return "", ""
	}
	var outBuf, errBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(&outBuf, outR) }()
	go func() { defer wg.Done(); _, _ = io.Copy(&errBuf, errR) }()

	sysDict.Set(val.InternString("stdout"), v.Engine().NewFileStream(outW, "<stdout>", true))
	sysDict.Set(val.InternString("stderr"), v.Engine().NewFileStream(errW, "<stderr>", true))
	fn()
	sysDict.Set(val.InternString("stdout"), prevOut)
	sysDict.Set(val.InternString("stderr"), prevErr)
	_ = outW.Close()
	_ = errW.Close()
	wg.Wait()
	_ = outR.Close()
	_ = errR.Close()
	return outBuf.String(), errBuf.String()
}

// syntaxTraceback renders a compile error like a CPython REPL would.
func syntaxTraceback(filename string, err error) string {
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "SyntaxError: ")
	return fmt.Sprintf("Traceback (most recent call last):\nSyntaxError: %s\n", msg)
}
