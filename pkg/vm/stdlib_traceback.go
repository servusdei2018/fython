package vm

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	frameSummaryTypeOnce sync.Once
	FrameSummaryType     *val.PyType
)

func initFrameSummaryType() {
	frameSummaryTypeOnce.Do(func() {
		d := val.NewDictVal(8).AsDict()
		d.Set(val.InternString("__init__"), val.NewBuiltinFuncVal("__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 4 {
				panic("TypeError: FrameSummary.__init__() missing required arguments")
			}
			self := args[0]
			fn := args[1]
			lineno := args[2]
			name := args[3]
			line := val.NewStringVal("")
			npos := len(args) - len(kwnames)
			for i, kn := range kwnames {
				if kn.AsString() == "line" {
					line = args[npos+i]
				}
			}
			if line.IsNone() || line.IsUnbound() {
				line = val.NewStringVal("")
			}
			val.SetAttrCtx(ctx, self, val.InternString("filename"), fn)
			val.SetAttrCtx(ctx, self, val.InternString("lineno"), lineno)
			val.SetAttrCtx(ctx, self, val.InternString("name"), name)
			val.SetAttrCtx(ctx, self, val.InternString("line"), line)
			val.SetAttrCtx(ctx, self, val.InternString("locals"), val.NoneVal())
			return val.NoneVal()
		}))
		d.Set(val.InternString("__getitem__"), val.NewBuiltinFuncVal("__getitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: __getitem__ requires 1 argument")
			}
			self := args[0]
			fn := val.GetAttrCtx(ctx, self, val.InternString("filename"))
			lineno := val.GetAttrCtx(ctx, self, val.InternString("lineno"))
			name := val.GetAttrCtx(ctx, self, val.InternString("name"))
			line := val.GetAttrCtx(ctx, self, val.InternString("line"))
			tup := val.NewTupleVal(fn, lineno, name, line)
			return ctx.Invoke1(val.GetAttrCtx(ctx, tup, val.InternString("__getitem__")), args[1])
		}))
		d.Set(val.InternString("__iter__"), val.NewBuiltinFuncVal("__iter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: __iter__ requires self")
			}
			self := args[0]
			fn := val.GetAttrCtx(ctx, self, val.InternString("filename"))
			lineno := val.GetAttrCtx(ctx, self, val.InternString("lineno"))
			name := val.GetAttrCtx(ctx, self, val.InternString("name"))
			line := val.GetAttrCtx(ctx, self, val.InternString("line"))
			return val.NewIterVal(val.NewTupleVal(fn, lineno, name, line))
		}))
		d.Set(val.InternString("__len__"), val.NewBuiltinFuncVal("__len__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.Int64Val(4)
		}))
		d.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			self := args[0]
			fn := val.GetAttrCtx(ctx, self, val.InternString("filename"))
			lineno := val.GetAttrCtx(ctx, self, val.InternString("lineno"))
			name := val.GetAttrCtx(ctx, self, val.InternString("name"))
			line := val.GetAttrCtx(ctx, self, val.InternString("line"))
			return val.NewStringVal(fmt.Sprintf("<FrameSummary file %s, line %s, name %s, line %s>", fn.String(), lineno.String(), name.String(), line.String()))
		}))
		FrameSummaryType = val.MustNewType("FrameSummary", []*val.PyType{val.ObjectType}, d)
	})
}

func newFrameSummaryVal(ctx *val.ExecContext, fileName string, lineno int, fnName string, line string) val.Value {
	initFrameSummaryType()
	inst := val.NewInstanceOfType(FrameSummaryType)
	val.SetAttrCtx(ctx, inst, val.InternString("filename"), val.NewStringVal(fileName))
	val.SetAttrCtx(ctx, inst, val.InternString("lineno"), val.Int64Val(int64(lineno)))
	val.SetAttrCtx(ctx, inst, val.InternString("name"), val.NewStringVal(fnName))
	val.SetAttrCtx(ctx, inst, val.InternString("line"), val.NewStringVal(line))
	val.SetAttrCtx(ctx, inst, val.InternString("locals"), val.NoneVal())
	return inst
}

// FormatException formats a Python exception Value and its __traceback__ chain into a human-readable traceback string.
func (vm *VM) FormatException(exc val.Value) string {
	if exc.IsNone() || exc.HeaderPtr() == nil {
		return ""
	}
	tb := val.NoneVal()
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
	initFrameSummaryType()
	modVal := val.NewModuleVal("traceback", "")
	mod := modVal.AsModule()
	mod.Dict.Set(val.InternString("FrameSummary"), val.TypeVal(FrameSummaryType))

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
			summary := newFrameSummaryVal(ctx, fileName, tb.LineNo, fnName, "")
			listItems = append(listItems, summary)
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
		tb := val.NoneVal()
		switch {
		case len(args) == 1:
			exc = args[0]
			if exc.IsInstance() {
				tb, _ = exc.AsInstance().GetAttr(val.InternString("__traceback__"))
			}
		case len(args) >= 3:
			exc = args[1]
			tb = args[2]
		default:
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

	// format_exception_only(exc, [value])
	mod.Dict.Set(val.InternString("format_exception_only"), val.NewBuiltinFuncVal("format_exception_only", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			return val.NewListVal()
		}
		exc := args[0]
		if len(args) > 1 && !args[1].IsNone() && !args[1].IsUnbound() {
			exc = args[1]
		}
		msg := formatExceptionMessage(ctx, exc)
		return val.NewListVal(val.NewStringVal(msg))
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
		_, _ = os.Stderr.WriteString(res.AsString())
		return val.NoneVal()
	}))

	// print_tb(tb)
	mod.Dict.Set(val.InternString("print_tb"), val.NewBuiltinFuncVal("print_tb", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.NoneVal()
		}
		tbLines := formatTracebackChain(args[0])
		for _, l := range tbLines {
			_, _ = os.Stderr.WriteString(l)
		}
		return val.NoneVal()
	}))

	// print_exception(exc, [value, tb], limit=None, file=None, chain=True)
	mod.Dict.Set(val.InternString("print_exception"), val.NewBuiltinFuncVal("print_exception", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		nkw := len(kwnames)
		npos := len(args) - nkw
		var posArgs []val.Value
		if npos > 0 {
			posArgs = args[:npos]
		}
		fileVal := val.NoneVal()
		for i, kn := range kwnames {
			if kn.AsString() == "file" {
				fileVal = args[npos+i]
			}
		}
		if fileVal.IsNone() && npos >= 5 {
			fileVal = posArgs[4]
		}

		fmtExcFn, _ := mod.Dict.Get(val.InternString("format_exception"))
		linesVal := ctx.Call(fmtExcFn, posArgs, nil)
		var text string
		if linesVal.IsList() {
			items := linesVal.AsList().Items
			var sb strings.Builder
			for _, item := range items {
				sb.WriteString(item.AsString())
			}
			text = sb.String()
		}

		if !fileVal.IsNone() {
			writeFn := val.GetAttrCtx(ctx, fileVal, val.InternString("write"))
			if !writeFn.IsUnbound() && !writeFn.IsNone() {
				ctx.ClearException()
				ctx.Invoke1(writeFn, val.NewStringVal(text))
				return val.NoneVal()
			}
			ctx.ClearException()
		}
		_, _ = os.Stderr.WriteString(text)
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
		for _, frameVal := range slices.Backward(frames) {
			f := frameVal.AsFrame()
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
	return typeName + "\n"
}
