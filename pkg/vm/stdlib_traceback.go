package vm

import (
	"fmt"
	"os"
	"strings"

	"github.com/servusdei2018/fython/pkg/val"
)

// FormatException formats a Python exception Value and its __traceback__ chain into a human-readable traceback string.
func (vm *VM) FormatException(exc val.Value) string {
	if exc.IsNone() || exc.HeaderPtr() == nil {
		return ""
	}
	var tb val.Value = val.NoneVal()
	if exc.IsInstance() {
		tb, _ = exc.AsInstance().GetAttr(val.InternString("__traceback__"))
	}
	var sb strings.Builder
	sb.WriteString("Traceback (most recent call last):\n")
	for _, l := range formatTracebackChain(tb) {
		sb.WriteString(l)
	}
	sb.WriteString(formatExceptionMessage(&vm.execCtx, exc))
	return sb.String()
}

func formatTracebackChain(tbVal val.Value) []string {
	var lines []string
	curr := tbVal
	for !curr.IsNone() && curr.IsTraceback() {
		tb := curr.AsTraceback()
		frameVal := tb.Frame
		fnName := "<unknown>"
		fileName := "<unknown>"
		if frameVal.IsFrame() {
			f := frameVal.AsFrame()
			if val.CodeInspectorHook != nil && f.CO.HeaderPtr() != nil {
				fnName = val.CodeInspectorHook.QualName(f.CO)
				fileName = val.CodeInspectorHook.FileName(f.CO)
			}
		}
		lines = append(lines, fmt.Sprintf("  File \"%s\", line %d, in %s\n", fileName, tb.LineNo, fnName))
		curr = tb.Next
	}
	return lines
}

func (e *Engine) createTracebackModule() val.Value {
	modVal := val.NewModuleVal("traceback", "")
	mod := modVal.AsModule()

	// format_tb(tb, limit=None)
	mod.Dict.Set(val.InternString("format_tb"), val.NewBuiltinFuncVal("format_tb", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.NewListVal()
		}
		lines := formatTracebackChain(args[0])
		listItems := make([]val.Value, len(lines))
		for i, l := range lines {
			listItems[i] = val.NewStringVal(l)
		}
		return val.NewListVal(listItems...)
	}))

	// extract_tb(tb, limit=None)
	mod.Dict.Set(val.InternString("extract_tb"), val.NewBuiltinFuncVal("extract_tb", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.NewListVal()
		}
		var listItems []val.Value
		curr := args[0]
		for !curr.IsNone() && curr.IsTraceback() {
			tb := curr.AsTraceback()
			frameVal := tb.Frame
			fnName := "<unknown>"
			fileName := "<unknown>"
			if frameVal.IsFrame() {
				f := frameVal.AsFrame()
				if val.CodeInspectorHook != nil && f.CO.HeaderPtr() != nil {
					fnName = val.CodeInspectorHook.QualName(f.CO)
					fileName = val.CodeInspectorHook.FileName(f.CO)
				}
			}
			tup := val.NewTupleVal(
				val.NewStringVal(fileName),
				val.Int64Val(int64(tb.LineNo)),
				val.NewStringVal(fnName),
				val.NewStringVal(""),
			)
			listItems = append(listItems, tup)
			curr = tb.Next
		}
		return val.NewListVal(listItems...)
	}))

	// format_exception(exc, [value, tb])
	mod.Dict.Set(val.InternString("format_exception"), val.NewBuiltinFuncVal("format_exception", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			return val.NewListVal()
		}
		var exc val.Value
		var tb val.Value = val.NoneVal()
		if len(args) == 1 {
			exc = args[0]
			if exc.IsInstance() {
				tb, _ = exc.AsInstance().GetAttr(val.InternString("__traceback__"))
			}
		} else if len(args) >= 3 {
			exc = args[1]
			tb = args[2]
		} else {
			exc = args[0]
		}

		var lines []val.Value
		lines = append(lines, val.NewStringVal("Traceback (most recent call last):\n"))
		tbLines := formatTracebackChain(tb)
		for _, l := range tbLines {
			lines = append(lines, val.NewStringVal(l))
		}
		lines = append(lines, val.NewStringVal(formatExceptionMessage(ctx, exc)))
		return val.NewListVal(lines...)
	}))

	// format_exc()
	mod.Dict.Set(val.InternString("format_exc"), val.NewBuiltinFuncVal("format_exc", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		vmInstance := ctx.VM.(*VM)
		exc := vmInstance.lastException
		if exc.IsNone() || exc.HeaderPtr() == nil {
			return val.NewStringVal("NoneType: None\n")
		}
		tb, _ := exc.AsInstance().GetAttr(val.InternString("__traceback__"))
		var sb strings.Builder
		sb.WriteString("Traceback (most recent call last):\n")
		tbLines := formatTracebackChain(tb)
		for _, l := range tbLines {
			sb.WriteString(l)
		}
		sb.WriteString(formatExceptionMessage(ctx, exc))
		return val.NewStringVal(sb.String())
	}))

	// print_exc()
	mod.Dict.Set(val.InternString("print_exc"), val.NewBuiltinFuncVal("print_exc", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		fmtExcFn, _ := mod.Dict.Get(val.InternString("format_exc"))
		res := ctx.Invoke0(fmtExcFn)
		os.Stderr.WriteString(res.AsString())
		return val.NoneVal()
	}))

	// print_tb(tb)
	mod.Dict.Set(val.InternString("print_tb"), val.NewBuiltinFuncVal("print_tb", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.NoneVal()
		}
		tbLines := formatTracebackChain(args[0])
		for _, l := range tbLines {
			os.Stderr.WriteString(l)
		}
		return val.NoneVal()
	}))

	// format_stack(f=None, limit=None)
	mod.Dict.Set(val.InternString("format_stack"), val.NewBuiltinFuncVal("format_stack", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		vmInstance := ctx.VM.(*VM)
		var startFrame val.Value
		if len(args) > 0 && !args[0].IsNone() {
			startFrame = args[0]
		} else {
			startFrame = vmInstance.materializeFrame(vmInstance.activeFrameIdx)
		}

		var frames []val.Value
		curr := startFrame
		for !curr.IsNone() && curr.IsFrame() {
			frames = append(frames, curr)
			f := curr.AsFrame()
			fBack, _ := val.FrameType.Dict.Get(val.InternString("f_back"))
			_ = fBack
			curr = f.Back
		}

		// Reverse so outermost caller is first
		var listItems []val.Value
		for i := len(frames) - 1; i >= 0; i-- {
			f := frames[i].AsFrame()
			fileName := "<unknown>"
			fnName := "<unknown>"
			line := f.IP
			if val.CodeInspectorHook != nil && f.CO.HeaderPtr() != nil {
				fileName = val.CodeInspectorHook.FileName(f.CO)
				fnName = val.CodeInspectorHook.QualName(f.CO)
				line = val.CodeInspectorHook.LineAt(f.CO, f.IP)
			}
			listItems = append(listItems, val.NewStringVal(fmt.Sprintf("  File \"%s\", line %d, in %s\n", fileName, line, fnName)))
		}
		return val.NewListVal(listItems...)
	}))

	return modVal
}

func formatExceptionMessage(ctx *val.ExecContext, exc val.Value) string {
	typeName := "Exception"
	if exc.Type() != nil {
		typeName = exc.Type().Name
	}
	msg := ""
	if exc.IsInstance() {
		if argsVal, ok := exc.AsInstance().GetAttr(val.InternString("args")); ok && argsVal.IsTuple() {
			items := argsVal.AsTuple().Items
			if len(items) == 1 {
				msg = items[0].String()
			} else if len(items) > 1 {
				msg = argsVal.String()
			}
		}
	}
	if msg == "" && !exc.IsInstance() {
		msg = exc.String()
	}
	if msg != "" {
		return fmt.Sprintf("%s: %s\n", typeName, msg)
	}
	return fmt.Sprintf("%s\n", typeName)
}
