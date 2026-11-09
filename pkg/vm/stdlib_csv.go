package vm

import (
	"errors"
	"fmt"
	"strings"
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
}

type pyCsvReader struct {
	inst    val.PyInstance
	iter    val.Value
	dialect *csvDialect
	lineNum int64
}

func initCsvTypes() {
	csvOnce.Do(func() {
		csvErrorType = val.MustNewType("Error", []*val.PyType{val.ExceptionType}, nil)
		csvErrorType.Dict.Set(val.InternString("__module__"), val.InternString("_csv"))

		csvDialectType = val.MustNewType("Dialect", []*val.PyType{val.ObjectType}, nil)
		csvDialectType.Dict.Set(val.InternString("__module__"), val.InternString("_csv"))
		csvDialectType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: Dialect requires an argument")
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
				panic("TypeError: writerow() missing row argument")
			}
			pw := (*pyCsvWriter)(val.AsPointer(args[0]))
			row := args[1]
			items := val.ExtractIterableItems(ctx, row)
			d := pw.dialect

			var parts []string
			for _, item := range items {
				var s string
				switch {
				case item.IsAnyString():
					s = item.AsString()
				case item.IsNone():
					s = ""
				default:
					s = item.String()
				}

				needQuote := false
				switch d.quoting {
				case quoteAll:
					needQuote = true
				case quoteMinimal:
					needQuote = strings.Contains(s, d.delimiter) ||
						(d.quotechar != "" && strings.Contains(s, d.quotechar)) ||
						strings.Contains(s, "\r") || strings.Contains(s, "\n")
				case quoteNonNumeric:
					if !item.IsInt64() && !item.IsFloat64() {
						needQuote = true
					}
				case quoteStrings:
					if item.IsAnyString() {
						needQuote = true
					}
				case quoteNotNull:
					if !item.IsNone() {
						needQuote = true
					}
				case quoteNone:
					needQuote = false
				}

				if needQuote && d.quotechar != "" {
					if d.doublequote {
						s = strings.ReplaceAll(s, d.quotechar, d.quotechar+d.quotechar)
					} else if d.escapechar != "" {
						s = strings.ReplaceAll(s, d.quotechar, d.escapechar+d.quotechar)
					}
					s = d.quotechar + s + d.quotechar
				} else if d.quoting == quoteNone && d.escapechar != "" {
					s = strings.ReplaceAll(s, d.delimiter, d.escapechar+d.delimiter)
				}
				parts = append(parts, s)
			}

			line := strings.Join(parts, d.delimiter) + d.lineterminator
			writeMethod := val.ObjectGetAttr(ctx, pw.fileobj, val.InternString("write"))
			ctx.Invoke1(writeMethod, val.NewStringVal(line))
			return val.NoneVal()
		}))

		csvWriterType.Dict.Set(val.InternString("writerows"), val.NewBuiltinFuncVal("writerows", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: writerows() missing rows argument")
			}
			rowIter := val.ExtractIterableItems(ctx, args[1])
			wrow := val.ObjectGetAttr(ctx, args[0], val.InternString("writerow"))
			for _, r := range rowIter {
				ctx.Invoke1(wrow, r)
			}
			return val.NoneVal()
		}))

		// CSV Reader Type
		csvReaderType = val.MustNewType("reader", []*val.PyType{val.ObjectType}, nil)
		csvReaderType.Dict.Set(val.InternString("__module__"), val.InternString("_csv"))
		csvReaderType.Slots.TpIter = func(ctx *val.ExecContext, o val.Value) val.Value { return o }
		csvReaderType.Slots.TpGetAttr = func(ctx *val.ExecContext, obj, name val.Value) val.Value {
			pr := (*pyCsvReader)(val.AsPointer(obj))
			if name.AsString() == "line_num" {
				return val.Int64Val(pr.lineNum)
			}
			return val.ObjectGetAttr(ctx, obj, name)
		}

		csvReaderType.Slots.TpIterNext = func(ctx *val.ExecContext, o val.Value) (val.Value, val.IterStatus) {
			pr := (*pyCsvReader)(val.AsPointer(o))
			itObj := pr.iter.AsIter()
			lineVal, ok := itObj.Next()
			if !ok {
				return val.NoneVal(), val.IterExhausted
			}
			pr.lineNum++
			line := lineVal.AsString()
			line = strings.TrimRight(line, "\r\n")

			d := pr.dialect
			var fields []val.Value

			// Parse CSV fields according to dialect
			inQuotes := false
			var currentField strings.Builder
			runes := []rune(line)
			delimRune := rune(d.delimiter[0])
			var quoteRune rune
			if d.quotechar != "" {
				quoteRune = rune(d.quotechar[0])
			}
			var escRune rune
			if d.escapechar != "" {
				escRune = rune(d.escapechar[0])
			}

			i := 0
			for i < len(runes) {
				r := runes[i]
				if inQuotes {
					if escRune != 0 && r == escRune && i+1 < len(runes) {
						currentField.WriteRune(runes[i+1])
						i += 2
						continue
					}
					if quoteRune != 0 && r == quoteRune {
						if d.doublequote && i+1 < len(runes) && runes[i+1] == quoteRune {
							currentField.WriteRune(quoteRune)
							i += 2
							continue
						}
						inQuotes = false
						i++
						continue
					}
					currentField.WriteRune(r)
					i++
				} else {
					if escRune != 0 && r == escRune && i+1 < len(runes) {
						currentField.WriteRune(runes[i+1])
						i += 2
						continue
					}
					if quoteRune != 0 && r == quoteRune && (currentField.Len() == 0 || (d.skipinitialspace && strings.TrimSpace(currentField.String()) == "")) {
						inQuotes = true
						currentField.Reset()
						i++
						continue
					}
					if r == delimRune {
						f := currentField.String()
						if d.skipinitialspace {
							f = strings.TrimLeft(f, " \t")
						}
						fields = append(fields, val.NewStringVal(f))
						currentField.Reset()
						i++
						continue
					}
					currentField.WriteRune(r)
					i++
				}
			}
			f := currentField.String()
			if d.skipinitialspace && !inQuotes {
				f = strings.TrimLeft(f, " \t")
			}
			fields = append(fields, val.NewStringVal(f))
			return val.NewListVal(fields...), val.IterYielded
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
