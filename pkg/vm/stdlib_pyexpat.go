package vm

import (
	"bytes"
	"encoding/xml"
	"io"
	"sync"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	expatErrorType  *val.PyType
	expatParserType *val.PyType
	expatTypesOnce  sync.Once
)

func initExpatTypes() {
	expatTypesOnce.Do(func() {
		expatErrorType = val.MustNewType("ExpatError", []*val.PyType{val.ExceptionType}, nil)
		expatErrorType.InvalidateVersion()

		t := val.MustNewType("xmlparser", []*val.PyType{val.ObjectType}, nil)
		t.Dict.Set(val.InternString("Parse"), val.NewBuiltinFuncVal("Parse", expatParse))
		t.Dict.Set(val.InternString("ParseFile"), val.NewBuiltinFuncVal("ParseFile", expatParseFile))
		t.InvalidateVersion()
		expatParserType = t
	})
}

func expatFeedBytes(ctx *val.ExecContext, self val.Value, data []byte) {
	inst := self.AsInstance()
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false

	startName := val.InternString("StartElementHandler")
	endName := val.InternString("EndElementHandler")
	charName := val.InternString("CharacterDataHandler")
	lineName := val.InternString("CurrentLineNumber")

	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			ctx.Raise(val.NewException(expatErrorType, err.Error()))
			return
		}
		line, _ := dec.InputPos()
		inst.SetAttr(lineName, val.Int64Val(int64(line)))

		switch tok := tok.(type) {
		case xml.StartElement:
			depth++
			if h, ok := inst.GetAttr(startName); ok && !h.IsNone() {
				attrs := val.NewDictVal(len(tok.Attr)).AsDict()
				for _, a := range tok.Attr {
					attrs.Set(val.NewStringVal(a.Name.Local), val.NewStringVal(a.Value))
				}
				ctx.Invoke2(h, val.NewStringVal(tok.Name.Local), val.DictVal(attrs))
				if ctx.HasException() {
					return
				}
			}
		case xml.EndElement:
			depth--
			if h, ok := inst.GetAttr(endName); ok && !h.IsNone() {
				ctx.Invoke1(h, val.NewStringVal(tok.Name.Local))
				if ctx.HasException() {
					return
				}
			}
		case xml.CharData:
			if depth > 0 && len(tok) > 0 {
				if h, ok := inst.GetAttr(charName); ok && !h.IsNone() {
					ctx.Invoke1(h, val.NewStringVal(string(tok)))
					if ctx.HasException() {
						return
					}
				}
			}
		}
	}
}

func expatParse(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	pa := posArgs(args, kwnames)
	if len(pa) < 2 {
		ctx.Raise(val.NewException(val.TypeErrorType, "Parse() missing required argument 'data'"))
		return val.NoneVal()
	}
	var data []byte
	switch {
	case pa[1].IsBytes():
		data = pa[1].AsBytes().Data
	case pa[1].IsByteArray():
		data = pa[1].AsByteArray().Data
	case pa[1].IsAnyString():
		data = []byte(pa[1].AsString())
	default:
		ctx.Raise(val.NewException(val.TypeErrorType, "Parse() argument must be bytes or str"))
		return val.NoneVal()
	}
	expatFeedBytes(ctx, pa[0], data)
	return val.Int64Val(1)
}

func expatParseFile(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	pa := posArgs(args, kwnames)
	if len(pa) < 2 {
		ctx.Raise(val.NewException(val.TypeErrorType, "ParseFile() missing required argument 'file'"))
		return val.NoneVal()
	}
	fileObj := pa[1]
	var chunk val.Value
	if fileObj.IsInstance() {
		if readFn, ok := fileObj.AsInstance().GetAttr(val.InternString("read")); ok {
			chunk = ctx.Invoke0(readFn)
		} else if readFn, _, ok := fileObj.Type().LookupMRO(val.InternString("read")); ok {
			chunk = ctx.Invoke1(readFn, fileObj)
		} else {
			ctx.Raise(val.NewException(val.TypeErrorType, "argument must have 'read' attribute"))
			return val.NoneVal()
		}
	} else if readFn, _, ok := fileObj.Type().LookupMRO(val.InternString("read")); ok {
		chunk = ctx.Invoke1(readFn, fileObj)
	} else {
		ctx.Raise(val.NewException(val.TypeErrorType, "argument must have 'read' attribute"))
		return val.NoneVal()
	}
	var data []byte
	switch {
	case chunk.IsBytes():
		data = chunk.AsBytes().Data
	case chunk.IsByteArray():
		data = chunk.AsByteArray().Data
	case chunk.IsAnyString():
		data = []byte(chunk.AsString())
	default:
		ctx.Raise(val.NewException(val.TypeErrorType, "read() did not return bytes or str"))
		return val.NoneVal()
	}
	expatFeedBytes(ctx, pa[0], data)
	return val.Int64Val(1)
}

func (e *Engine) createPyExpatModule() val.Value {
	initExpatTypes()
	modVal := val.NewModuleVal("pyexpat", "Expat XML parser interface")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("error"), val.TypeVal(expatErrorType))
	mod.Dict.Set(val.InternString("ExpatError"), val.TypeVal(expatErrorType))
	mod.Dict.Set(val.InternString("XMLParserType"), val.TypeVal(expatParserType))
	mod.Dict.Set(val.InternString("EXPAT_VERSION"), val.NewStringVal("expat_2.6.0"))
	mod.Dict.Set(val.InternString("version_info"), val.NewTupleVal(val.Int64Val(2), val.Int64Val(6), val.Int64Val(0)))

	errorsMod := val.NewModuleVal("pyexpat.errors", "Expat error constants")
	modelMod := val.NewModuleVal("pyexpat.model", "Expat content model constants")
	mod.Dict.Set(val.InternString("errors"), errorsMod)
	mod.Dict.Set(val.InternString("model"), modelMod)

	mod.Dict.Set(val.InternString("ParserCreate"), val.NewBuiltinFuncVal("ParserCreate", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		v := val.NewInstanceOfType(expatParserType)
		inst := v.AsInstance()
		inst.SetAttr(val.InternString("StartElementHandler"), val.NoneVal())
		inst.SetAttr(val.InternString("EndElementHandler"), val.NoneVal())
		inst.SetAttr(val.InternString("CharacterDataHandler"), val.NoneVal())
		inst.SetAttr(val.InternString("EntityDeclHandler"), val.NoneVal())
		inst.SetAttr(val.InternString("CurrentLineNumber"), val.Int64Val(1))
		inst.SetAttr(val.InternString("buffer_text"), val.BoolVal(false))
		inst.SetAttr(val.InternString("ordered_attributes"), val.BoolVal(false))
		inst.SetAttr(val.InternString("specified_attributes"), val.BoolVal(false))
		return v
	}))

	return modVal
}
