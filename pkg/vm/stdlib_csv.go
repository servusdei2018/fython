package vm

import (
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/servusdei2018/fython/pkg/val"
)

const (
	quoteMinimal    = 0
	quoteAll        = 1
	quoteNonNumeric = 2
	quoteNone       = 3
	quoteStrings    = 4
	quoteNotNull    = 5
)

var (
	csvOnce        sync.Once
	csvErrorType   *val.PyType
	csvDialectType *val.PyType
	csvReaderType  *val.PyType
	csvWriterType  *val.PyType

	csvDialectsMu sync.RWMutex
	csvDialects   = make(map[string]*csvDialect)
	fieldSizeLim  = 131072
)

type csvDialect struct {
	name             string
	delimiter        string
	quotechar        string
	escapechar       string
	doublequote      bool
	skipinitialspace bool
	lineterminator   string
	quoting          int
	strict           bool
}

func extractKwArgs(args, kwnames []val.Value) map[string]val.Value {
	if len(kwnames) == 0 {
		return nil
	}
	kw := make(map[string]val.Value, len(kwnames))
	start := len(args) - len(kwnames)
	for i, kn := range kwnames {
		kw[kn.AsString()] = args[start+i]
	}
	return kw
}

func defaultDialect() *csvDialect {
	return &csvDialect{
		name:             "excel",
		delimiter:        ",",
		quotechar:        "\"",
		escapechar:       "",
		doublequote:      true,
		skipinitialspace: false,
		lineterminator:   "\r\n",
		quoting:          quoteMinimal,
	}
}

func getDialectAttr(v val.Value, attr string) val.Value {
	if v.IsType() {
		if valAttr, _, ok := v.AsType().LookupMRO(val.InternString(attr)); ok {
			return valAttr
		}
		return val.NoneVal()
	}
	if v.IsInstance() {
		if valAttr, ok := v.AsInstance().GetAttr(val.InternString(attr)); ok {
			return valAttr
		}
		if valAttr, _, ok := v.AsInstance().Header.Type.LookupMRO(val.InternString(attr)); ok {
			return valAttr
		}
		return val.NoneVal()
	}
	return val.NoneVal()
}

func resolveDialect(v val.Value, kw map[string]val.Value) (*csvDialect, error) {
	d := defaultDialect()
	if !v.IsNone() {
		if v.IsAnyString() {
			name := v.AsString()
			csvDialectsMu.RLock()
			registered, ok := csvDialects[name]
			csvDialectsMu.RUnlock()
			if !ok {
				return nil, fmt.Errorf("unknown dialect '%s'", name)
			}
			*d = *registered
		} else if v.IsInstance() || v.IsType() {
			valDelim := getDialectAttr(v, "delimiter")
			if !valDelim.IsNone() && valDelim.IsAnyString() {
				d.delimiter = valDelim.AsString()
			}
			valQuote := getDialectAttr(v, "quotechar")
			if !valQuote.IsNone() {
				if valQuote.IsAnyString() {
					d.quotechar = valQuote.AsString()
				}
			}
			valEsc := getDialectAttr(v, "escapechar")
			if !valEsc.IsNone() {
				if valEsc.IsAnyString() {
					d.escapechar = valEsc.AsString()
				}
			}
			valTerm := getDialectAttr(v, "lineterminator")
			if !valTerm.IsNone() && valTerm.IsAnyString() {
				d.lineterminator = valTerm.AsString()
			}
			valDQ := getDialectAttr(v, "doublequote")
			if !valDQ.IsNone() {
				d.doublequote = valDQ.IsTruthy()
			}
			valSkip := getDialectAttr(v, "skipinitialspace")
			if !valSkip.IsNone() {
				d.skipinitialspace = valSkip.IsTruthy()
			}
			valQuoting := getDialectAttr(v, "quoting")
			if !valQuoting.IsNone() && valQuoting.IsInt64() {
				d.quoting = int(valQuoting.AsInt64())
			}
			valStrict := getDialectAttr(v, "strict")
			if !valStrict.IsNone() {
				d.strict = valStrict.IsTruthy()
			}
		}
	}

	if kw != nil {
		if v, ok := kw["delimiter"]; ok && v.IsAnyString() {
			d.delimiter = v.AsString()
		}
		if v, ok := kw["quotechar"]; ok {
			if v.IsNone() {
				d.quotechar = ""
			} else if v.IsAnyString() {
				d.quotechar = v.AsString()
			}
		}
		if v, ok := kw["escapechar"]; ok {
			if v.IsNone() {
				d.escapechar = ""
			} else if v.IsAnyString() {
				d.escapechar = v.AsString()
			}
		}
		if v, ok := kw["lineterminator"]; ok && v.IsAnyString() {
			d.lineterminator = v.AsString()
		}
		if v, ok := kw["doublequote"]; ok {
			d.doublequote = v.IsTruthy()
		}
		if v, ok := kw["skipinitialspace"]; ok {
			d.skipinitialspace = v.IsTruthy()
		}
		if v, ok := kw["quoting"]; ok && v.IsInt64() {
			d.quoting = int(v.AsInt64())
		}
		if v, ok := kw["strict"]; ok {
			d.strict = v.IsTruthy()
		}
	}

	if len(d.delimiter) != 1 {
		return nil, errors.New("delimiter must be a 1-character string")
	}
	if len(d.quotechar) > 1 {
		return nil, errors.New("quotechar must be set to a 1-character string")
	}
	if len(d.escapechar) > 1 {
		return nil, errors.New("escapechar must be set to a 1-character string")
	}
	return d, nil
}

type pyCsvWriter struct {
	inst    val.PyInstance
	fileobj val.Value
	dialect *csvDialect
	rowBuf  []byte
}

type pyCsvReader struct {
	inst     val.PyInstance
	iter     val.Value
	dialect  *csvDialect
	lineNum  int64
	fieldBuf []byte
}

func csvFieldNeedsQuote(field []byte, delimByte, quoteByte, escByte byte) bool {
	for _, b := range field {
		if b == delimByte || b == '\r' || b == '\n' || (quoteByte != 0 && b == quoteByte) || (escByte != 0 && b == escByte) {
			return true
		}
	}
	return false
}

func csvFieldNeedsQuoteStr(field string, delimByte, quoteByte, escByte byte) bool {
	for i := range len(field) {
		b := field[i]
		if b == delimByte || b == '\r' || b == '\n' || (quoteByte != 0 && b == quoteByte) || (escByte != 0 && b == escByte) {
			return true
		}
	}
	return false
}

func appendCsvQuotedStr(buf []byte, s string, quoteByte, escByte byte, doublequote bool) []byte {
	buf = append(buf, quoteByte)
	start := 0
	for i := range len(s) {
		b := s[i]
		if b == quoteByte {
			if i > start {
				buf = append(buf, s[start:i]...)
			}
			switch {
			case doublequote:
				buf = append(buf, quoteByte, quoteByte)
			case escByte != 0:
				buf = append(buf, escByte, quoteByte)
			default:
				buf = append(buf, quoteByte)
			}
			start = i + 1
		} else if escByte != 0 && b == escByte {
			if i > start {
				buf = append(buf, s[start:i]...)
			}
			buf = append(buf, escByte, escByte)
			start = i + 1
		}
	}
	if start < len(s) {
		buf = append(buf, s[start:]...)
	}
	return append(buf, quoteByte)
}

func appendCsvEscapedStr(buf []byte, s string, delimByte, escByte byte) []byte {
	start := 0
	for i := range len(s) {
		b := s[i]
		if b == delimByte || b == escByte || b == '\r' || b == '\n' {
			if i > start {
				buf = append(buf, s[start:i]...)
			}
			buf = append(buf, escByte, b)
			start = i + 1
		}
	}
	if start < len(s) {
		buf = append(buf, s[start:]...)
	}
	return buf
}

func csvWriteRow(ctx *val.ExecContext, pw *pyCsvWriter, writeMethod val.Value, row val.Value) val.Value {
	var items []val.Value
	switch {
	case row.IsList() && row.Type() == val.ListType:
		items = row.AsList().Items
	case row.IsTuple() && row.Type() == val.TupleType:
		items = row.AsTuple().Items
	default:
		items = val.ExtractIterableItems(ctx, row)
	}
	d := pw.dialect
	delimByte := d.delimiter[0]
	var quoteByte byte
	if len(d.quotechar) > 0 {
		quoteByte = d.quotechar[0]
	}
	var escByte byte
	if len(d.escapechar) > 0 {
		escByte = d.escapechar[0]
	}

	buf := pw.rowBuf[:0]
	var numTmp [64]byte
	for idx, item := range items {
		if idx > 0 {
			buf = append(buf, delimByte)
		}
		var s string
		var numBytes []byte
		isNumBytes := false
		isNumeric := false
		switch {
		case item.IsAnyString():
			s = item.AsString()
		case item.IsNone():
			s = ""
		case item.IsInt64():
			numBytes = strconv.AppendInt(numTmp[:0], item.AsInt64(), 10)
			isNumBytes = true
			isNumeric = true
		case item.IsFloat64():
			numBytes = val.AppendFloatRepr(numTmp[:0], item.AsFloat64())
			isNumBytes = true
			isNumeric = true
		case item.IsBool():
			if item.AsBool() {
				s = "True"
			} else {
				s = "False"
			}
		default:
			s = val.PyStr(ctx, item)
		}

		needQuote := false
		switch d.quoting {
		case quoteAll:
			needQuote = true
		case quoteMinimal:
			if isNumBytes {
				needQuote = csvFieldNeedsQuote(numBytes, delimByte, quoteByte, escByte)
			} else {
				needQuote = csvFieldNeedsQuoteStr(s, delimByte, quoteByte, escByte)
			}
		case quoteNonNumeric:
			needQuote = !isNumeric
		case quoteStrings:
			needQuote = item.IsAnyString()
		case quoteNotNull:
			needQuote = !item.IsNone()
		case quoteNone:
			needQuote = false
		}

		switch {
		case needQuote && quoteByte != 0:
			if isNumBytes {
				buf = append(buf, quoteByte)
				buf = append(buf, numBytes...)
				buf = append(buf, quoteByte)
			} else {
				buf = appendCsvQuotedStr(buf, s, quoteByte, escByte, d.doublequote)
			}
		case d.quoting == quoteNone && escByte != 0:
			if isNumBytes {
				buf = append(buf, numBytes...)
			} else {
				buf = appendCsvEscapedStr(buf, s, delimByte, escByte)
			}
		default:
			if isNumBytes {
				buf = append(buf, numBytes...)
			} else {
				buf = append(buf, s...)
			}
		}
	}

	buf = append(buf, d.lineterminator...)
	pw.rowBuf = buf
	return ctx.Invoke1(writeMethod, val.NewStringFromBytes(buf))
}

func csvEmitField(ctx *val.ExecContext, fields []val.Value, s string, useBuf bool, fieldBuf []byte, wasQuoted bool, quoting int) ([]val.Value, bool) {
	if quoting == quoteNonNumeric && !wasQuoted {
		var raw string
		if useBuf {
			raw = string(fieldBuf)
		} else {
			raw = s
		}
		if len(raw) == 0 {
			return append(fields, val.SmallStringVal("")), true
		}
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			ctx.Raise(val.NewException(val.ValueErrorType, "could not convert string to float: '"+raw+"'"))
			return fields, false
		}
		return append(fields, val.Float64Val(f)), true
	}
	if useBuf {
		return append(fields, val.NewStringFromBytes(fieldBuf)), true
	}
	return append(fields, val.NewStringVal(s)), true
}

func csvReadNextRow(ctx *val.ExecContext, pr *pyCsvReader) (val.Value, val.IterStatus) {
	itObj := pr.iter.AsIter()
	lineVal, ok := itObj.Next()
	if !ok {
		return val.NoneVal(), val.IterExhausted
	}
	pr.lineNum++
	line := lineVal.AsString()

	// Empty line (including bare \n, \r\n, or \r) yields an empty list [].
	if len(line) == 0 || line == "\n" || line == "\r\n" || line == "\r" {
		return val.NewListVal(), val.IterYielded
	}

	d := pr.dialect
	delimByte := d.delimiter[0]
	var quoteByte byte
	if len(d.quotechar) > 0 {
		quoteByte = d.quotechar[0]
	}
	var escByte byte
	if len(d.escapechar) > 0 {
		escByte = d.escapechar[0]
	}

	fields := make([]val.Value, 0, 8)
	fieldBuf := pr.fieldBuf[:0]
	useBuf := false
	inQuotes := false
	wasQuoted := false
	atFieldStart := true
	fieldStart := 0

	for {
		n := len(line)
		i := 0
		endOfRecord := false
		for i < n {
			b := line[i]
			if inQuotes {
				if escByte != 0 && b == escByte {
					if i+1 < n {
						fieldBuf = append(fieldBuf, line[i+1])
						i += 2
						continue
					}
					break
				}
				if quoteByte != 0 && b == quoteByte {
					if d.doublequote && i+1 < n && line[i+1] == quoteByte {
						fieldBuf = append(fieldBuf, quoteByte)
						i += 2
						continue
					}
					inQuotes = false
					i++
					continue
				}
				runStart := i
				i++
				for i < n {
					c := line[i]
					if (quoteByte != 0 && c == quoteByte) || (escByte != 0 && c == escByte) {
						break
					}
					i++
				}
				fieldBuf = append(fieldBuf, line[runStart:i]...)
				continue
			}

			// Not inQuotes.
			if b == '\r' || b == '\n' {
				var okEmit bool
				fields, okEmit = csvEmitField(ctx, fields, line[fieldStart:i], useBuf, fieldBuf, wasQuoted, d.quoting)
				if !okEmit {
					pr.fieldBuf = fieldBuf[:0]
					return val.NoneVal(), val.IterError
				}
				endOfRecord = true
				break
			}
			if atFieldStart {
				if d.skipinitialspace && b == ' ' {
					i++
					fieldStart = i
					continue
				}
				if quoteByte != 0 && d.quoting != quoteNone && b == quoteByte {
					inQuotes = true
					wasQuoted = true
					atFieldStart = false
					useBuf = true
					fieldBuf = fieldBuf[:0]
					i++
					continue
				}
			}
			if escByte != 0 && b == escByte {
				if !useBuf {
					fieldBuf = append(fieldBuf[:0], line[fieldStart:i]...)
					useBuf = true
				}
				if i+1 < n {
					fieldBuf = append(fieldBuf, line[i+1])
					i += 2
				} else {
					i++
				}
				atFieldStart = false
				continue
			}
			if b == delimByte {
				var okEmit bool
				fields, okEmit = csvEmitField(ctx, fields, line[fieldStart:i], useBuf, fieldBuf, wasQuoted, d.quoting)
				if !okEmit {
					pr.fieldBuf = fieldBuf[:0]
					return val.NoneVal(), val.IterError
				}
				atFieldStart = true
				useBuf = false
				wasQuoted = false
				fieldBuf = fieldBuf[:0]
				i++
				fieldStart = i
				continue
			}
			atFieldStart = false
			if useBuf {
				fieldBuf = append(fieldBuf, b)
			}
			i++
		}

		if endOfRecord {
			break
		}
		if inQuotes {
			nextVal, ok := itObj.Next()
			if !ok {
				if d.strict {
					pr.fieldBuf = fieldBuf[:0]
					ctx.Raise(val.NewException(csvErrorType, "unexpected end of data"))
					return val.NoneVal(), val.IterError
				}
				var okEmit bool
				fields, okEmit = csvEmitField(ctx, fields, "", true, fieldBuf, wasQuoted, d.quoting)
				if !okEmit {
					pr.fieldBuf = fieldBuf[:0]
					return val.NoneVal(), val.IterError
				}
				break
			}
			pr.lineNum++
			line = nextVal.AsString()
			fieldStart = 0
			continue
		}
		var okEmit bool
		fields, okEmit = csvEmitField(ctx, fields, line[fieldStart:n], useBuf, fieldBuf, wasQuoted, d.quoting)
		if !okEmit {
			pr.fieldBuf = fieldBuf[:0]
			return val.NoneVal(), val.IterError
		}
		break
	}

	pr.fieldBuf = fieldBuf[:0]
	return val.NewListFromOwnedSlice(fields), val.IterYielded
}

func initCsvTypes() {
	csvOnce.Do(func() {
		csvErrorType = val.MustNewType("Error", []*val.PyType{val.ExceptionType}, nil)
		csvErrorType.Dict.Set(val.InternString("__module__"), val.InternString("_csv"))

		csvDialectType = val.MustNewType("Dialect", []*val.PyType{val.ObjectType}, nil)
		csvDialectType.Dict.Set(val.InternString("__module__"), val.InternString("_csv"))
		csvDialectType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewException(val.TypeErrorType, "Dialect requires an argument"))
				return val.NoneVal()
			}
			_, err := resolveDialect(args[0], nil)
			if err != nil {
				exc := val.NewException(val.TypeErrorType, err.Error())
				panic(exc)
			}
			return val.NewInstanceOfType(csvDialectType)
		}

		// CSV Writer Type
		csvWriterType = val.MustNewType("writer", []*val.PyType{val.ObjectType}, nil)
		csvWriterType.Dict.Set(val.InternString("__module__"), val.InternString("_csv"))

		csvWriterType.Dict.Set(val.InternString("writerow"), val.NewBuiltinFuncVal("writerow", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				ctx.Raise(val.NewException(val.TypeErrorType, "writerow() missing row argument"))
				return val.NoneVal()
			}
			pw := (*pyCsvWriter)(val.AsPointer(args[0]))
			writeMethod := val.ObjectGetAttr(ctx, pw.fileobj, val.InternString("write"))
			return csvWriteRow(ctx, pw, writeMethod, args[1])
		}))

		csvWriterType.Dict.Set(val.InternString("writerows"), val.NewBuiltinFuncVal("writerows", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				ctx.Raise(val.NewException(val.TypeErrorType, "writerows() missing rows argument"))
				return val.NoneVal()
			}
			pw := (*pyCsvWriter)(val.AsPointer(args[0]))
			writeMethod := val.ObjectGetAttr(ctx, pw.fileobj, val.InternString("write"))
			var rows []val.Value
			switch {
			case args[1].IsList() && args[1].Type() == val.ListType:
				rows = args[1].AsList().Items
			case args[1].IsTuple() && args[1].Type() == val.TupleType:
				rows = args[1].AsTuple().Items
			default:
				rows = val.ExtractIterableItems(ctx, args[1])
			}
			for _, r := range rows {
				csvWriteRow(ctx, pw, writeMethod, r)
			}
			return val.NoneVal()
		}))

		// CSV Reader Type
		csvReaderType = val.MustNewType("reader", []*val.PyType{val.ObjectType}, nil)
		csvReaderType.Dict.Set(val.InternString("__module__"), val.InternString("_csv"))
		csvReaderType.Slots.TpIter = func(ctx *val.ExecContext, o val.Value) val.Value { return o }
		csvReaderType.Slots.TpGetAttr = func(ctx *val.ExecContext, obj, name val.Value) val.Value {
			pr := (*pyCsvReader)(val.AsPointer(obj))
			if val.StringEqualsASCII(name, "line_num") {
				return val.Int64Val(pr.lineNum)
			}
			return val.ObjectGetAttr(ctx, obj, name)
		}

		csvReaderType.Slots.TpIterNext = func(ctx *val.ExecContext, o val.Value) (val.Value, val.IterStatus) {
			pr := (*pyCsvReader)(val.AsPointer(o))
			return csvReadNextRow(ctx, pr)
		}

		// Register built-in default dialects
		csvDialects["excel"] = defaultDialect()
		csvDialects["excel-tab"] = &csvDialect{
			name:             "excel-tab",
			delimiter:        "\t",
			quotechar:        "\"",
			escapechar:       "",
			doublequote:      true,
			skipinitialspace: false,
			lineterminator:   "\r\n",
			quoting:          quoteMinimal,
		}
		csvDialects["unix"] = &csvDialect{
			name:             "unix",
			delimiter:        ",",
			quotechar:        "\"",
			escapechar:       "",
			doublequote:      true,
			skipinitialspace: false,
			lineterminator:   "\n",
			quoting:          quoteAll,
		}
	})
}

func (e *Engine) createCsvModule() val.Value {
	initCsvTypes()
	modVal := val.NewModuleVal("_csv", "Accelerated CSV processing module")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("Error"), val.TypeVal(csvErrorType))
	mod.Dict.Set(val.InternString("Dialect"), val.TypeVal(csvDialectType))

	mod.Dict.Set(val.InternString("QUOTE_MINIMAL"), val.Int64Val(quoteMinimal))
	mod.Dict.Set(val.InternString("QUOTE_ALL"), val.Int64Val(quoteAll))
	mod.Dict.Set(val.InternString("QUOTE_NONNUMERIC"), val.Int64Val(quoteNonNumeric))
	mod.Dict.Set(val.InternString("QUOTE_NONE"), val.Int64Val(quoteNone))
	mod.Dict.Set(val.InternString("QUOTE_STRINGS"), val.Int64Val(quoteStrings))
	mod.Dict.Set(val.InternString("QUOTE_NOTNULL"), val.Int64Val(quoteNotNull))

	set := func(name string, fn val.VectorcallFunc) {
		mod.Dict.Set(val.InternString(name), val.NewModuleFuncVal(name, fn))
	}

	set("register_dialect", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		kw := extractKwArgs(args, kwnames)
		if len(pa) < 1 {
			exc := val.NewException(csvErrorType, "register_dialect takes at least 1 argument")
			panic(exc)
		}
		name := pa[0].AsString()
		diagVal := val.NoneVal()
		if len(pa) > 1 {
			diagVal = pa[1]
		}
		d, err := resolveDialect(diagVal, kw)
		if err != nil {
			exc := val.NewException(csvErrorType, err.Error())
			panic(exc)
		}
		d.name = name
		csvDialectsMu.Lock()
		csvDialects[name] = d
		csvDialectsMu.Unlock()
		return val.NoneVal()
	})

	set("unregister_dialect", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			exc := val.NewException(csvErrorType, "unregister_dialect takes 1 argument")
			panic(exc)
		}
		name := pa[0].AsString()
		csvDialectsMu.Lock()
		delete(csvDialects, name)
		csvDialectsMu.Unlock()
		return val.NoneVal()
	})

	set("get_dialect", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			exc := val.NewException(csvErrorType, "get_dialect takes 1 argument")
			panic(exc)
		}
		name := pa[0].AsString()
		csvDialectsMu.RLock()
		d, ok := csvDialects[name]
		csvDialectsMu.RUnlock()
		if !ok {
			exc := val.NewException(csvErrorType, fmt.Sprintf("unknown dialect '%s'", name))
			panic(exc)
		}
		inst := val.NewInstanceOfType(csvDialectType).AsInstance()
		inst.SetAttr(val.InternString("delimiter"), val.NewStringVal(d.delimiter))
		inst.SetAttr(val.InternString("quotechar"), val.NewStringVal(d.quotechar))
		inst.SetAttr(val.InternString("escapechar"), val.NewStringVal(d.escapechar))
		inst.SetAttr(val.InternString("doublequote"), val.BoolVal(d.doublequote))
		inst.SetAttr(val.InternString("skipinitialspace"), val.BoolVal(d.skipinitialspace))
		inst.SetAttr(val.InternString("lineterminator"), val.NewStringVal(d.lineterminator))
		inst.SetAttr(val.InternString("quoting"), val.Int64Val(int64(d.quoting)))
		return val.ObjectVal(&inst.Header)
	})

	set("list_dialects", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		csvDialectsMu.RLock()
		names := make([]val.Value, 0, len(csvDialects))
		for k := range csvDialects {
			names = append(names, val.NewStringVal(k))
		}
		csvDialectsMu.RUnlock()
		return val.NewListVal(names...)
	})

	set("field_size_limit", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		old := fieldSizeLim
		pa := posArgs(args, kwnames)
		if len(pa) > 0 && pa[0].IsInt64() {
			fieldSizeLim = int(pa[0].AsInt64())
		}
		return val.Int64Val(int64(old))
	})

	set("writer", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		kw := extractKwArgs(args, kwnames)
		if len(pa) < 1 {
			exc := val.NewException(csvErrorType, "writer() takes at least 1 argument")
			panic(exc)
		}
		dVal := val.NoneVal()
		if len(pa) > 1 {
			dVal = pa[1]
		}
		d, err := resolveDialect(dVal, kw)
		if err != nil {
			exc := val.NewException(csvErrorType, err.Error())
			panic(exc)
		}
		pw := &pyCsvWriter{
			inst: val.PyInstance{
				Header: val.ObjectHeader{Kind: val.KindInstance, Type: csvWriterType},
				Shape:  csvWriterType.RootShape,
			},
			fileobj: pa[0],
			dialect: d,
		}
		return val.ObjectVal(&pw.inst.Header)
	})

	set("reader", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		kw := extractKwArgs(args, kwnames)
		if len(pa) < 1 {
			exc := val.NewException(csvErrorType, "reader() takes at least 1 argument")
			panic(exc)
		}
		dVal := val.NoneVal()
		if len(pa) > 1 {
			dVal = pa[1]
		}
		d, err := resolveDialect(dVal, kw)
		if err != nil {
			exc := val.NewException(csvErrorType, err.Error())
			panic(exc)
		}
		it := val.NewIterVal(pa[0])
		pr := &pyCsvReader{
			inst: val.PyInstance{
				Header: val.ObjectHeader{Kind: val.KindInstance, Type: csvReaderType},
				Shape:  csvReaderType.RootShape,
			},
			iter:    it,
			dialect: d,
			lineNum: 0,
		}
		return val.ObjectVal(&pr.inst.Header)
	})

	return modVal
}
