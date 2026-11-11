package vm

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"

	"github.com/servusdei2018/fython/pkg/val"
)

// Standard Python marshal type tags
const (
	tagNull     = '0'
	tagNone     = 'N'
	tagFalse    = 'F'
	tagTrue     = 'T'
	tagStopIter = 'S'
	tagInt      = 'i'
	tagFloat    = 'g'
	tagString   = 's'
	tagBytes    = 'y'
	tagTuple    = '('
	tagList     = '['
	tagDict     = '{'
	tagSet      = '<'
	tagCode     = 'c'
)

func marshalValue(b *bytes.Buffer, v val.Value) error {
	if v.IsNone() {
		b.WriteByte(tagNone)
		return nil
	}
	if v.IsBool() {
		if v.AsBool() {
			b.WriteByte(tagTrue)
		} else {
			b.WriteByte(tagFalse)
		}
		return nil
	}
	if v.IsInt64() {
		b.WriteByte(tagInt)
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], uint64(v.AsInt64()))
		b.Write(buf[:])
		return nil
	}
	if v.IsFloat64() {
		b.WriteByte(tagFloat)
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], math.Float64bits(v.AsFloat64()))
		b.Write(buf[:])
		return nil
	}
	if v.IsAnyString() {
		b.WriteByte(tagString)
		s := v.AsString()
		var lenBuf [4]byte
		binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(s)))
		b.Write(lenBuf[:])
		b.WriteString(s)
		return nil
	}
	if v.IsBytes() {
		b.WriteByte(tagBytes)
		data := v.AsBytes().Data
		var lenBuf [4]byte
		binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(data)))
		b.Write(lenBuf[:])
		b.Write(data)
		return nil
	}
	if v.IsTuple() {
		b.WriteByte(tagTuple)
		t := v.AsTuple()
		items := t.Items
		var lenBuf [4]byte
		binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(items)))
		b.Write(lenBuf[:])
		for _, item := range items {
			if err := marshalValue(b, item); err != nil {
				return err
			}
		}
		return nil
	}
	if v.IsList() {
		b.WriteByte(tagList)
		l := v.AsList()
		var lenBuf [4]byte
		binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(l.Items)))
		b.Write(lenBuf[:])
		for _, item := range l.Items {
			if err := marshalValue(b, item); err != nil {
				return err
			}
		}
		return nil
	}
	if v.IsDict() {
		b.WriteByte(tagDict)
		d := v.AsDict()
		for _, kv := range d.Entries {
			if err := marshalValue(b, kv.Key); err != nil {
				return err
			}
			if err := marshalValue(b, kv.Value); err != nil {
				return err
			}
		}
		b.WriteByte(tagNull)
		return nil
	}
	if v.IsCode() {
		b.WriteByte(tagCode)
		co := (*CodeObject)(val.AsPointer(v))
		// Serialize key CodeObject fields: Name, FileName, Code, Consts, NumLocals, NumParams
		var lenBuf [4]byte

		// Name
		binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(co.Name)))
		b.Write(lenBuf[:])
		b.WriteString(co.Name)

		// FileName
		binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(co.FileName)))
		b.Write(lenBuf[:])
		b.WriteString(co.FileName)

		// FirstLineNo, NumLocals, NumParams, MaxStackSize
		var intBuf [16]byte
		binary.LittleEndian.PutUint32(intBuf[0:4], uint32(co.FirstLineNo))
		binary.LittleEndian.PutUint32(intBuf[4:8], uint32(co.NumLocals))
		binary.LittleEndian.PutUint32(intBuf[8:12], uint32(co.NumParams))
		binary.LittleEndian.PutUint32(intBuf[12:16], uint32(co.MaxStackSize))
		b.Write(intBuf[:])

		// Code instructions
		binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(co.Code)))
		b.Write(lenBuf[:])
		for _, inst := range co.Code {
			binary.LittleEndian.PutUint32(lenBuf[:], inst)
			b.Write(lenBuf[:])
		}

		// Consts
		binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(co.Consts)))
		b.Write(lenBuf[:])
		for _, c := range co.Consts {
			if err := marshalValue(b, c); err != nil {
				return err
			}
		}
		return nil
	}

	return fmt.Errorf("ValueError: unmarshallable object of type %s", v.Type().Name)
}

func unmarshalValue(r *bytes.Reader) (val.Value, error) {
	tag, err := r.ReadByte()
	if err != nil {
		return val.NoneVal(), err
	}
	switch tag {
	case tagNone:
		return val.NoneVal(), nil
	case tagTrue:
		return val.BoolVal(true), nil
	case tagFalse:
		return val.BoolVal(false), nil
	case tagInt:
		var buf [8]byte
		if _, err := r.Read(buf[:]); err != nil {
			return val.NoneVal(), err
		}
		return val.Int64Val(int64(binary.LittleEndian.Uint64(buf[:]))), nil
	case tagFloat:
		var buf [8]byte
		if _, err := r.Read(buf[:]); err != nil {
			return val.NoneVal(), err
		}
		bits := binary.LittleEndian.Uint64(buf[:])
		return val.Float64Val(math.Float64frombits(bits)), nil
	case tagString:
		var lenBuf [4]byte
		if _, err := r.Read(lenBuf[:]); err != nil {
			return val.NoneVal(), err
		}
		length := int(binary.LittleEndian.Uint32(lenBuf[:]))
		strBytes := make([]byte, length)
		if _, err := r.Read(strBytes); err != nil {
			return val.NoneVal(), err
		}
		return val.NewStringVal(string(strBytes)), nil
	case tagBytes:
		var lenBuf [4]byte
		if _, err := r.Read(lenBuf[:]); err != nil {
			return val.NoneVal(), err
		}
		length := int(binary.LittleEndian.Uint32(lenBuf[:]))
		data := make([]byte, length)
		if _, err := r.Read(data); err != nil {
			return val.NoneVal(), err
		}
		return val.NewBytesVal(data), nil
	case tagTuple:
		var lenBuf [4]byte
		if _, err := r.Read(lenBuf[:]); err != nil {
			return val.NoneVal(), err
		}
		length := int(binary.LittleEndian.Uint32(lenBuf[:]))
		items := make([]val.Value, length)
		for i := range length {
			v, err := unmarshalValue(r)
			if err != nil {
				return val.NoneVal(), err
			}
			items[i] = v
		}
		return val.NewTupleVal(items...), nil
	case tagList:
		var lenBuf [4]byte
		if _, err := r.Read(lenBuf[:]); err != nil {
			return val.NoneVal(), err
		}
		length := int(binary.LittleEndian.Uint32(lenBuf[:]))
		items := make([]val.Value, length)
		for i := range length {
			v, err := unmarshalValue(r)
			if err != nil {
				return val.NoneVal(), err
			}
			items[i] = v
		}
		return val.NewListVal(items...), nil
	case tagDict:
		d := val.NewDictVal(8)
		for {
			peek, err := r.ReadByte()
			if err != nil {
				return val.NoneVal(), err
			}
			if peek == tagNull {
				break
			}
			if err := r.UnreadByte(); err != nil {
				return val.NoneVal(), err
			}
			k, err := unmarshalValue(r)
			if err != nil {
				return val.NoneVal(), err
			}
			v, err := unmarshalValue(r)
			if err != nil {
				return val.NoneVal(), err
			}
			d.AsDict().Set(k, v)
		}
		return d, nil
	case tagCode:
		var lenBuf [4]byte
		if _, err := r.Read(lenBuf[:]); err != nil {
			return val.NoneVal(), err
		}
		nameLen := int(binary.LittleEndian.Uint32(lenBuf[:]))
		nameBytes := make([]byte, nameLen)
		if _, err := r.Read(nameBytes); err != nil {
			return val.NoneVal(), err
		}

		if _, err := r.Read(lenBuf[:]); err != nil {
			return val.NoneVal(), err
		}
		fileLen := int(binary.LittleEndian.Uint32(lenBuf[:]))
		fileBytes := make([]byte, fileLen)
		if _, err := r.Read(fileBytes); err != nil {
			return val.NoneVal(), err
		}

		var intBuf [16]byte
		if _, err := r.Read(intBuf[:]); err != nil {
			return val.NoneVal(), err
		}
		firstLine := int(binary.LittleEndian.Uint32(intBuf[0:4]))
		numLocals := int(binary.LittleEndian.Uint32(intBuf[4:8]))
		numParams := int(binary.LittleEndian.Uint32(intBuf[8:12]))
		maxStack := int(binary.LittleEndian.Uint32(intBuf[12:16]))

		if _, err := r.Read(lenBuf[:]); err != nil {
			return val.NoneVal(), err
		}
		codeLen := int(binary.LittleEndian.Uint32(lenBuf[:]))
		code := make([]uint32, codeLen)
		for i := range codeLen {
			if _, err := r.Read(lenBuf[:]); err != nil {
				return val.NoneVal(), err
			}
			code[i] = binary.LittleEndian.Uint32(lenBuf[:])
		}

		if _, err := r.Read(lenBuf[:]); err != nil {
			return val.NoneVal(), err
		}
		constsLen := int(binary.LittleEndian.Uint32(lenBuf[:]))
		consts := make([]val.Value, constsLen)
		for i := range constsLen {
			c, err := unmarshalValue(r)
			if err != nil {
				return val.NoneVal(), err
			}
			consts[i] = c
		}

		co := &CodeObject{
			Header:       val.ObjectHeader{Kind: val.KindCode, Type: val.CodeType},
			Name:         string(nameBytes),
			FileName:     string(fileBytes),
			FirstLineNo:  firstLine,
			NumLocals:    numLocals,
			NumParams:    numParams,
			MaxStackSize: maxStack,
			Code:         code,
			Consts:       consts,
		}
		co.Init()
		return val.ObjectVal(&co.Header), nil
	default:
		return val.NoneVal(), fmt.Errorf("ValueError: bad marshal data (unknown tag %q)", tag)
	}
}

func (e *Engine) createMarshalModule() val.Value {
	modVal := val.NewModuleVal("marshal", "This module contains functions that can read and write Python values in binary format.")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("version"), val.Int64Val(4))

	mod.Dict.Set(val.InternString("dumps"), val.NewBuiltinFuncVal("dumps", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: dumps() takes at least 1 argument")
		}
		var buf bytes.Buffer
		if err := marshalValue(&buf, args[0]); err != nil {
			panic(err.Error())
		}
		return val.NewBytesVal(buf.Bytes())
	}))

	mod.Dict.Set(val.InternString("loads"), val.NewBuiltinFuncVal("loads", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: loads() takes at least 1 argument")
		}
		var b []byte
		switch {
		case args[0].IsBytes():
			b = args[0].AsBytes().Data
		case args[0].IsByteArray():
			b = args[0].AsByteArray().Data
		default:
			panic("TypeError: a bytes-like object is required")
		}
		r := bytes.NewReader(b)
		v, err := unmarshalValue(r)
		if err != nil {
			panic(err.Error())
		}
		return v
	}))

	mod.Dict.Set(val.InternString("dump"), val.NewBuiltinFuncVal("dump", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: dump() takes at least 2 arguments")
		}
		var buf bytes.Buffer
		if err := marshalValue(&buf, args[0]); err != nil {
			panic(err.Error())
		}
		bytesVal := val.NewBytesVal(buf.Bytes())
		writeMeth, ok := getAttrOrNone(ctx, args[1], val.InternString("write"))
		if !ok {
			panic("AttributeError: file object has no 'write' attribute")
		}
		ctx.Invoke1(writeMeth, bytesVal)
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("load"), val.NewBuiltinFuncVal("load", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: load() takes at least 1 argument")
		}
		readMeth, ok := getAttrOrNone(ctx, args[0], val.InternString("read"))
		if !ok {
			panic("AttributeError: file object has no 'read' attribute")
		}
		content := ctx.Invoke0(readMeth)
		var b []byte
		switch {
		case content.IsBytes():
			b = content.AsBytes().Data
		case content.IsByteArray():
			b = content.AsByteArray().Data
		default:
			panic("TypeError: a bytes-like object is required")
		}
		r := bytes.NewReader(b)
		v, err := unmarshalValue(r)
		if err != nil {
			panic(err.Error())
		}
		return v
	}))

	return modVal
}
