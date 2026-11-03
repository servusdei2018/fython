package vm

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/servusdei2018/fython/pkg/val"
)

const (
	// FycMagic is the 4-byte magic signature for Fython bytecode files ("FYTH" in ASCII).
	FycMagic uint32 = 0x48545946
	// FycVersion is the current bytecode format version.
	FycVersion uint32 = 1
	// FycHeaderSize is the fixed size of the .fyc file header in bytes.
	FycHeaderSize = 64
)

// Constant serialization tags.
const (
	TagNone uint8 = iota
	TagFalse
	TagTrue
	TagInt64
	TagFloat64
	TagString
	TagTuple
	TagCode
	TagBytes
)

// FycHeader represents the 64-byte deterministic header of a .fyc file.
type FycHeader struct {
	Magic           uint32
	Version         uint32
	Flags           uint32
	HeaderSize      uint32
	SourceSize      uint64
	SourceMTimeNano int64
	SourceHash      [32]byte
}

var (
	ErrInvalidFycMagic   = errors.New("fyc: invalid magic bytes (not a Fython bytecode file)")
	ErrUnsupportedFycVer = errors.New("fyc: unsupported bytecode format version")
	ErrCorruptFycData    = errors.New("fyc: unexpected EOF or corrupted bytecode data")
)

type stringCollector struct {
	indexMap map[string]uint32
	strings  []string
}

func newStringCollector() *stringCollector {
	return &stringCollector{
		indexMap: make(map[string]uint32),
		strings:  make([]string, 0, 32),
	}
}

func (sc *stringCollector) add(s string) uint32 {
	if idx, ok := sc.indexMap[s]; ok {
		return idx
	}
	idx := uint32(len(sc.strings))
	sc.indexMap[s] = idx
	sc.strings = append(sc.strings, s)
	return idx
}

func (sc *stringCollector) collectFromCodeObject(co *CodeObject) {
	if co == nil {
		return
	}
	sc.add(co.Name)

	for _, v := range co.ParamNames {
		sc.add(v.AsString())
	}
	for _, v := range co.CellVars {
		sc.add(v.AsString())
	}
	for _, v := range co.FreeVars {
		sc.add(v.AsString())
	}

	for _, c := range co.Consts {
		sc.collectFromValue(c)
	}
}

func (sc *stringCollector) collectFromValue(v val.Value) {
	switch v.Kind() {
	case val.KindInlineStr, val.KindString:
		sc.add(v.AsString())
	case val.KindTuple:
		for _, item := range v.AsTuple().Items {
			sc.collectFromValue(item)
		}
	case val.KindCode:
		sc.collectFromCodeObject(AsCodeObject(v))
	}
}

// SerializeCodeObject serializes a CodeObject tree and source file metadata into a deterministic .fyc byte slice.
func SerializeCodeObject(co *CodeObject, srcSize uint64, srcMTimeNano int64, srcHash [32]byte) ([]byte, error) {
	// 1. Collect all strings across the CodeObject tree into a deduplicated table
	sc := newStringCollector()
	sc.collectFromCodeObject(co)

	// Pre-calculate string table blob size
	totalBlobSize := 0
	for _, s := range sc.strings {
		totalBlobSize += len(s)
	}

	// 2. Encode header
	buf := make([]byte, 0, FycHeaderSize+len(sc.strings)*4+totalBlobSize+len(co.Code)*4+512)
	headerBytes := make([]byte, FycHeaderSize)
	binary.LittleEndian.PutUint32(headerBytes[0:4], FycMagic)
	binary.LittleEndian.PutUint32(headerBytes[4:8], FycVersion)
	binary.LittleEndian.PutUint32(headerBytes[8:12], 0) // Flags
	binary.LittleEndian.PutUint32(headerBytes[12:16], FycHeaderSize)
	binary.LittleEndian.PutUint64(headerBytes[16:24], srcSize)
	binary.LittleEndian.PutUint64(headerBytes[24:32], uint64(srcMTimeNano))
	copy(headerBytes[32:64], srcHash[:])
	buf = append(buf, headerBytes...)

	// 3. Encode string table
	var tmp4 [4]byte
	binary.LittleEndian.PutUint32(tmp4[:], uint32(len(sc.strings)))
	buf = append(buf, tmp4[:]...)

	binary.LittleEndian.PutUint32(tmp4[:], uint32(totalBlobSize))
	buf = append(buf, tmp4[:]...)

	for _, s := range sc.strings {
		binary.LittleEndian.PutUint32(tmp4[:], uint32(len(s)))
		buf = append(buf, tmp4[:]...)
	}
	for _, s := range sc.strings {
		buf = append(buf, s...)
	}

	// 4. Encode CodeObject tree
	buf = encodeCodeObject(buf, co, sc)
	return buf, nil
}

func encodeCodeObject(buf []byte, co *CodeObject, sc *stringCollector) []byte {
	var tmp4 [4]byte

	// Metadata
	nameIdx := sc.add(co.Name)
	binary.LittleEndian.PutUint32(tmp4[:], nameIdx)
	buf = append(buf, tmp4[:]...)

	binary.LittleEndian.PutUint32(tmp4[:], uint32(co.NumLocals))
	buf = append(buf, tmp4[:]...)

	binary.LittleEndian.PutUint32(tmp4[:], uint32(co.NumParams))
	buf = append(buf, tmp4[:]...)

	binary.LittleEndian.PutUint32(tmp4[:], uint32(co.MaxStackSize))
	buf = append(buf, tmp4[:]...)

	flags := uint32(0)
	if co.IsGenerator {
		flags |= 1
	}
	binary.LittleEndian.PutUint32(tmp4[:], flags)
	buf = append(buf, tmp4[:]...)

	// Instructions
	binary.LittleEndian.PutUint32(tmp4[:], uint32(len(co.Code)))
	buf = append(buf, tmp4[:]...)
	for _, inst := range co.Code {
		binary.LittleEndian.PutUint32(tmp4[:], inst)
		buf = append(buf, tmp4[:]...)
	}

	// Constants
	binary.LittleEndian.PutUint32(tmp4[:], uint32(len(co.Consts)))
	buf = append(buf, tmp4[:]...)
	for _, c := range co.Consts {
		buf = encodeValue(buf, c, sc)
	}

	// ParamNames
	binary.LittleEndian.PutUint32(tmp4[:], uint32(len(co.ParamNames)))
	buf = append(buf, tmp4[:]...)
	for _, v := range co.ParamNames {
		binary.LittleEndian.PutUint32(tmp4[:], sc.add(v.AsString()))
		buf = append(buf, tmp4[:]...)
	}

	// CellVars
	binary.LittleEndian.PutUint32(tmp4[:], uint32(len(co.CellVars)))
	buf = append(buf, tmp4[:]...)
	for _, v := range co.CellVars {
		binary.LittleEndian.PutUint32(tmp4[:], sc.add(v.AsString()))
		buf = append(buf, tmp4[:]...)
	}

	// FreeVars
	binary.LittleEndian.PutUint32(tmp4[:], uint32(len(co.FreeVars)))
	buf = append(buf, tmp4[:]...)
	for _, v := range co.FreeVars {
		binary.LittleEndian.PutUint32(tmp4[:], sc.add(v.AsString()))
		buf = append(buf, tmp4[:]...)
	}

	// ExceptionTable
	binary.LittleEndian.PutUint32(tmp4[:], uint32(len(co.ExceptionTable)))
	buf = append(buf, tmp4[:]...)
	for _, entry := range co.ExceptionTable {
		binary.LittleEndian.PutUint32(tmp4[:], uint32(entry.StartIP))
		buf = append(buf, tmp4[:]...)
		binary.LittleEndian.PutUint32(tmp4[:], uint32(entry.EndIP))
		buf = append(buf, tmp4[:]...)
		binary.LittleEndian.PutUint32(tmp4[:], uint32(entry.TargetIP))
		buf = append(buf, tmp4[:]...)
		binary.LittleEndian.PutUint32(tmp4[:], uint32(entry.TargetStackDepth))
		buf = append(buf, tmp4[:]...)
		pushByte := uint8(0)
		if entry.PushLasti {
			pushByte = 1
		}
		buf = append(buf, pushByte)
	}

	return buf
}

func encodeValue(buf []byte, v val.Value, sc *stringCollector) []byte {
	var tmp4 [4]byte
	var tmp8 [8]byte

	switch v.Kind() {
	case val.KindNone:
		buf = append(buf, TagNone)

	case val.KindBool:
		if v.AsBool() {
			buf = append(buf, TagTrue)
		} else {
			buf = append(buf, TagFalse)
		}

	case val.KindInt64:
		buf = append(buf, TagInt64)
		binary.LittleEndian.PutUint64(tmp8[:], uint64(v.AsInt64()))
		buf = append(buf, tmp8[:]...)

	case val.KindFloat64:
		buf = append(buf, TagFloat64)
		binary.LittleEndian.PutUint64(tmp8[:], math.Float64bits(v.AsFloat64()))
		buf = append(buf, tmp8[:]...)

	case val.KindInlineStr, val.KindString:
		buf = append(buf, TagString)
		strIdx := sc.add(v.AsString())
		binary.LittleEndian.PutUint32(tmp4[:], strIdx)
		buf = append(buf, tmp4[:]...)

	case val.KindTuple:
		buf = append(buf, TagTuple)
		items := v.AsTuple().Items
		binary.LittleEndian.PutUint32(tmp4[:], uint32(len(items)))
		buf = append(buf, tmp4[:]...)
		for _, item := range items {
			buf = encodeValue(buf, item, sc)
		}

	case val.KindCode:
		buf = append(buf, TagCode)
		buf = encodeCodeObject(buf, AsCodeObject(v), sc)

	case val.KindBytes:
		buf = append(buf, TagBytes)
		data := v.AsBytes().Data
		binary.LittleEndian.PutUint32(tmp4[:], uint32(len(data)))
		buf = append(buf, tmp4[:]...)
		buf = append(buf, data...)

	default:
		// Fallback for sentinels
		buf = append(buf, TagNone)
	}
	return buf
}

// BinaryReader provides fast, allocation-free slice reading.
type binaryReader struct {
	data []byte
	pos  int
}

func (r *binaryReader) has(n int) bool {
	return r.pos+n <= len(r.data)
}

func (r *binaryReader) readUint8() (uint8, error) {
	if !r.has(1) {
		return 0, ErrCorruptFycData
	}
	b := r.data[r.pos]
	r.pos++
	return b, nil
}

func (r *binaryReader) readUint32() (uint32, error) {
	if !r.has(4) {
		return 0, ErrCorruptFycData
	}
	v := binary.LittleEndian.Uint32(r.data[r.pos : r.pos+4])
	r.pos += 4
	return v, nil
}

func (r *binaryReader) readUint64() (uint64, error) {
	if !r.has(8) {
		return 0, ErrCorruptFycData
	}
	v := binary.LittleEndian.Uint64(r.data[r.pos : r.pos+8])
	r.pos += 8
	return v, nil
}

func (r *binaryReader) readBytes(n int) ([]byte, error) {
	if !r.has(n) {
		return nil, ErrCorruptFycData
	}
	b := r.data[r.pos : r.pos+n]
	r.pos += n
	return b, nil
}

// DeserializeCodeObject deserializes a compiled CodeObject tree and FycHeader from a little-endian binary buffer.
func DeserializeCodeObject(data []byte) (*CodeObject, *FycHeader, error) {
	if len(data) < FycHeaderSize {
		return nil, nil, ErrCorruptFycData
	}

	magic := binary.LittleEndian.Uint32(data[0:4])
	if magic != FycMagic {
		return nil, nil, ErrInvalidFycMagic
	}

	version := binary.LittleEndian.Uint32(data[4:8])
	if version != FycVersion {
		return nil, nil, ErrUnsupportedFycVer
	}

	header := &FycHeader{
		Magic:           magic,
		Version:         version,
		Flags:           binary.LittleEndian.Uint32(data[8:12]),
		HeaderSize:      binary.LittleEndian.Uint32(data[12:16]),
		SourceSize:      binary.LittleEndian.Uint64(data[16:24]),
		SourceMTimeNano: int64(binary.LittleEndian.Uint64(data[24:32])),
	}
	copy(header.SourceHash[:], data[32:64])

	r := &binaryReader{data: data, pos: int(header.HeaderSize)}

	// 1. Read string table
	stringCount, err := r.readUint32()
	if err != nil {
		return nil, nil, err
	}
	totalBlobSize, err := r.readUint32()
	if err != nil {
		return nil, nil, err
	}

	lengths := make([]uint32, stringCount)
	for i := uint32(0); i < stringCount; i++ {
		l, err := r.readUint32()
		if err != nil {
			return nil, nil, err
		}
		lengths[i] = l
	}

	blob, err := r.readBytes(int(totalBlobSize))
	if err != nil {
		return nil, nil, err
	}

	// Bulk-intern all strings with zero duplicate allocations
	stringTable := make([]val.Value, stringCount)
	blobOff := 0
	for i := uint32(0); i < stringCount; i++ {
		l := int(lengths[i])
		s := string(blob[blobOff : blobOff+l])
		stringTable[i] = val.InternString(s)
		blobOff += l
	}

	// 2. Decode root CodeObject
	co, err := decodeCodeObject(r, stringTable)
	if err != nil {
		return nil, nil, err
	}

	return co, header, nil
}

func decodeCodeObject(r *binaryReader, stringTable []val.Value) (*CodeObject, error) {
	nameIdx, err := r.readUint32()
	if err != nil {
		return nil, err
	}
	if int(nameIdx) >= len(stringTable) {
		return nil, ErrCorruptFycData
	}
	name := stringTable[nameIdx].AsString()

	numLocals, err := r.readUint32()
	if err != nil {
		return nil, err
	}
	numParams, err := r.readUint32()
	if err != nil {
		return nil, err
	}
	maxStackSize, err := r.readUint32()
	if err != nil {
		return nil, err
	}
	flags, err := r.readUint32()
	if err != nil {
		return nil, err
	}

	// Instructions
	codeLen, err := r.readUint32()
	if err != nil {
		return nil, err
	}
	code := make([]uint32, codeLen)
	for i := uint32(0); i < codeLen; i++ {
		inst, err := r.readUint32()
		if err != nil {
			return nil, err
		}
		code[i] = inst
	}

	// Constants
	constsLen, err := r.readUint32()
	if err != nil {
		return nil, err
	}
	consts := make([]val.Value, constsLen)
	for i := uint32(0); i < constsLen; i++ {
		v, err := decodeValue(r, stringTable)
		if err != nil {
			return nil, err
		}
		consts[i] = v
	}

	// ParamNames
	paramLen, err := r.readUint32()
	if err != nil {
		return nil, err
	}
	paramNames := make([]val.Value, paramLen)
	for i := uint32(0); i < paramLen; i++ {
		idx, err := r.readUint32()
		if err != nil {
			return nil, err
		}
		if int(idx) >= len(stringTable) {
			return nil, ErrCorruptFycData
		}
		paramNames[i] = stringTable[idx]
	}

	// CellVars
	cellLen, err := r.readUint32()
	if err != nil {
		return nil, err
	}
	cellVars := make([]val.Value, cellLen)
	for i := uint32(0); i < cellLen; i++ {
		idx, err := r.readUint32()
		if err != nil {
			return nil, err
		}
		if int(idx) >= len(stringTable) {
			return nil, ErrCorruptFycData
		}
		cellVars[i] = stringTable[idx]
	}

	// FreeVars
	freeLen, err := r.readUint32()
	if err != nil {
		return nil, err
	}
	freeVars := make([]val.Value, freeLen)
	for i := uint32(0); i < freeLen; i++ {
		idx, err := r.readUint32()
		if err != nil {
			return nil, err
		}
		if int(idx) >= len(stringTable) {
			return nil, ErrCorruptFycData
		}
		freeVars[i] = stringTable[idx]
	}

	// ExceptionTable
	excLen, err := r.readUint32()
	if err != nil {
		return nil, err
	}
	var excTable []ExceptionEntry
	if excLen > 0 {
		excTable = make([]ExceptionEntry, excLen)
		for i := uint32(0); i < excLen; i++ {
			sIP, err := r.readUint32()
			if err != nil {
				return nil, err
			}
			eIP, err := r.readUint32()
			if err != nil {
				return nil, err
			}
			tIP, err := r.readUint32()
			if err != nil {
				return nil, err
			}
			depth, err := r.readUint32()
			if err != nil {
				return nil, err
			}
			pushB, err := r.readUint8()
			if err != nil {
				return nil, err
			}
			excTable[i] = ExceptionEntry{
				StartIP:          int(sIP),
				EndIP:            int(eIP),
				TargetIP:         int(tIP),
				TargetStackDepth: int(depth),
				PushLasti:        pushB != 0,
			}
		}
	}

	co := &CodeObject{
		Name:           name,
		Code:           code,
		Consts:         consts,
		ParamNames:     paramNames,
		CellVars:       cellVars,
		FreeVars:       freeVars,
		NumLocals:      int(numLocals),
		NumParams:      int(numParams),
		MaxStackSize:   int(maxStackSize),
		IsGenerator:    (flags & 1) != 0,
		ExceptionTable: excTable,
	}
	co.Header.Kind = val.KindCode
	co.Header.Type = val.CodeType
	return co, nil
}

func decodeValue(r *binaryReader, stringTable []val.Value) (val.Value, error) {
	tag, err := r.readUint8()
	if err != nil {
		return val.NoneVal(), err
	}

	switch tag {
	case TagNone:
		return val.NoneVal(), nil

	case TagFalse:
		return val.BoolVal(false), nil

	case TagTrue:
		return val.BoolVal(true), nil

	case TagInt64:
		bits, err := r.readUint64()
		if err != nil {
			return val.NoneVal(), err
		}
		return val.Int64Val(int64(bits)), nil

	case TagFloat64:
		bits, err := r.readUint64()
		if err != nil {
			return val.NoneVal(), err
		}
		return val.Float64Val(math.Float64frombits(bits)), nil

	case TagString:
		idx, err := r.readUint32()
		if err != nil {
			return val.NoneVal(), err
		}
		if int(idx) >= len(stringTable) {
			return val.NoneVal(), ErrCorruptFycData
		}
		return stringTable[idx], nil

	case TagTuple:
		count, err := r.readUint32()
		if err != nil {
			return val.NoneVal(), err
		}
		items := make([]val.Value, count)
		for i := uint32(0); i < count; i++ {
			item, err := decodeValue(r, stringTable)
			if err != nil {
				return val.NoneVal(), err
			}
			items[i] = item
		}
		return val.NewTupleVal(items...), nil

	case TagCode:
		subCO, err := decodeCodeObject(r, stringTable)
		if err != nil {
			return val.NoneVal(), err
		}
		return NewCodeVal(subCO), nil

	case TagBytes:
		count, err := r.readUint32()
		if err != nil {
			return val.NoneVal(), err
		}
		data, err := r.readBytes(int(count))
		if err != nil {
			return val.NoneVal(), err
		}
		res := make([]byte, count)
		copy(res, data)
		return val.NewBytesVal(res), nil

	default:
		return val.NoneVal(), fmt.Errorf("fyc: unknown constant tag %d", tag)
	}
}

// SaveFyc atomically writes serialized CodeObject bytecode to fycPath.
func SaveFyc(fycPath string, co *CodeObject, srcSize uint64, srcMTimeNano int64, srcHash [32]byte) error {
	data, err := SerializeCodeObject(co, srcSize, srcMTimeNano, srcHash)
	if err != nil {
		return err
	}
	dir := filepath.Dir(fycPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmpFile := fycPath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpFile, fycPath)
}

// LoadFyc reads and deserializes a .fyc file from fycPath.
func LoadFyc(fycPath string) (*CodeObject, *FycHeader, error) {
	data, err := os.ReadFile(fycPath)
	if err != nil {
		return nil, nil, err
	}
	return DeserializeCodeObject(data)
}

// ValidateFyc checks whether header matches the current source file attributes.
func ValidateFyc(header *FycHeader, srcSize uint64, srcMTimeNano int64, srcHash [32]byte) bool {
	if header == nil {
		return false
	}
	if header.Magic != FycMagic || header.Version != FycVersion {
		return false
	}
	if header.SourceSize != srcSize {
		return false
	}
	if header.SourceMTimeNano != srcMTimeNano {
		return false
	}
	// Verify SHA-256 hash if non-zero
	var zeroHash [32]byte
	if srcHash != zeroHash && header.SourceHash != zeroHash && header.SourceHash != srcHash {
		return false
	}
	return true
}

// ComputeSourceHash returns the SHA-256 hash of the source buffer.
func ComputeSourceHash(src []byte) [32]byte {
	return sha256.Sum256(src)
}
