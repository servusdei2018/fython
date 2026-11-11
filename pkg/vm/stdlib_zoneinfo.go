package vm

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/servusdei2018/fython/pkg/val"
)

// zoneInfoType is the native type of the _zoneinfo.ZoneInfo class.
var zoneInfoType *val.PyType

// zoneInfoCache holds the time zones that were already read.
var zoneInfoCache sync.Map

// tzifData holds the parsed contents of a TZif file.
type tzifData struct {
	// transitions holds the UTC timestamps of the zone changes.
	transitions []int64
	// offsets holds the UTC offset in seconds before every transition, and
	// the offset in effect before the first transition.
	offsets []int32
	// abbreviations holds the abbreviation of every transition.
	abbreviations []string
	// dstOffsets holds the daylight saving offset of every transition.
	dstOffsets []int32
	// firstOffset is the offset in effect before the first transition.
	firstOffset int32
	// firstAbbr is the abbreviation in effect before the first transition.
	firstAbbr string
	// firstDST is the daylight saving offset before the first transition.
	firstDST int32
}

// pyZoneInfo is the native payload of a _zoneinfo.ZoneInfo object.
type pyZoneInfo struct {
	inst val.PyInstance // must be first: Header lives at offset 0
	key  string
	data *tzifData
}

// zoneInfoPaths returns the directories that hold the time zone database.
func zoneInfoPaths() []string {
	paths := []string{"/usr/share/zoneinfo", "/usr/lib/zoneinfo", "/usr/share/lib/zoneinfo", "/etc/zoneinfo"}
	if tz := os.Getenv("TZDIR"); tz != "" {
		paths = append([]string{tz}, paths...)
	}
	return paths
}

// parseTZif reads the transitions and the type table of a TZif file.
//
// A data block holds the transition times, the transition types, the local
// time type records, the time zone designations, the leap second records and
// the standard and UT indicators, in that order.
func parseTZif(data []byte) (*tzifData, error) {
	if len(data) < 44 || string(data[:4]) != "TZif" {
		return nil, os.ErrInvalid
	}
	version := data[4]
	readCounts := func(off int) (isutcnt, isstdcnt, leapcnt, timecnt, typecnt, charcnt, next int, ok bool) {
		if off+24 > len(data) {
			return 0, 0, 0, 0, 0, 0, 0, false
		}
		isutcnt = int(int32(binary.BigEndian.Uint32(data[off:])))
		isstdcnt = int(int32(binary.BigEndian.Uint32(data[off+4:])))
		leapcnt = int(int32(binary.BigEndian.Uint32(data[off+8:])))
		timecnt = int(int32(binary.BigEndian.Uint32(data[off+12:])))
		typecnt = int(int32(binary.BigEndian.Uint32(data[off+16:])))
		charcnt = int(int32(binary.BigEndian.Uint32(data[off+20:])))
		return isutcnt, isstdcnt, leapcnt, timecnt, typecnt, charcnt, off + 24, true
	}
	isutcnt, isstdcnt, leapcnt, timecnt, typecnt, charcnt, off, ok := readCounts(20)
	if !ok {
		return nil, os.ErrInvalid
	}
	skip := func(n int) bool {
		if off+n > len(data) {
			return false
		}
		off += n
		return true
	}
	// The first block holds 32 bit transition times.
	if !skip(timecnt*4) || !skip(timecnt) || !skip(typecnt*6) || !skip(charcnt) ||
		!skip(leapcnt*8) || !skip(isstdcnt) || !skip(isutcnt) {
		return nil, os.ErrInvalid
	}
	if version >= 2 {
		// The second block repeats the header and holds 64 bit times.
		if off+44 > len(data) || string(data[off:off+4]) != "TZif" {
			return nil, os.ErrInvalid
		}
		// The counts of the second block that are not needed here are
		// dropped: only the transition and type counts are used.
		_, _, _, timecnt, typecnt, charcnt, off, ok = readCounts(off + 20)
		if !ok {
			return nil, os.ErrInvalid
		}
	}
	if off+timecnt*8 > len(data) {
		return nil, os.ErrInvalid
	}
	out := &tzifData{}
	for i := range timecnt {
		out.transitions = append(out.transitions, int64(binary.BigEndian.Uint64(data[off+i*8:])))
	}
	off += timecnt * 8
	if off+timecnt > len(data) {
		return nil, os.ErrInvalid
	}
	typeIndexes := make([]byte, timecnt)
	copy(typeIndexes, data[off:off+timecnt])
	off += timecnt
	if off+typecnt*6 > len(data) {
		return nil, os.ErrInvalid
	}
	typeTable := off
	typeOffsets := make([]int32, typecnt)
	typeIsDst := make([]bool, typecnt)
	typeAbbr := make([]string, typecnt)
	for i := range typecnt {
		typeOffsets[i] = int32(binary.BigEndian.Uint32(data[typeTable+i*6:]))
		typeIsDst[i] = data[typeTable+i*6+4] != 0
	}
	off += typecnt * 6
	if off+charcnt > len(data) {
		return nil, os.ErrInvalid
	}
	chars := string(data[off : off+charcnt])
	for i := range typecnt {
		idx := int(data[typeTable+i*6+5])
		if idx < len(chars) {
			end := strings.IndexByte(chars[idx:], 0)
			if end < 0 {
				typeAbbr[i] = chars[idx:]
			} else {
				typeAbbr[i] = chars[idx : idx+end]
			}
		}
	}
	for _, ti := range typeIndexes {
		if int(ti) < len(typeOffsets) {
			out.offsets = append(out.offsets, typeOffsets[ti])
			out.abbreviations = append(out.abbreviations, typeAbbr[ti])
			if typeIsDst[ti] {
				out.dstOffsets = append(out.dstOffsets, typeOffsets[ti])
			} else {
				out.dstOffsets = append(out.dstOffsets, 0)
			}
		} else {
			out.offsets = append(out.offsets, 0)
			out.abbreviations = append(out.abbreviations, "UTC")
			out.dstOffsets = append(out.dstOffsets, 0)
		}
	}
	if len(out.offsets) > 0 {
		out.firstOffset = out.offsets[0]
		out.firstAbbr = out.abbreviations[0]
		out.firstDST = out.dstOffsets[0]
	}
	return out, nil
}

// lookupZone reads the time zone with the given key.
func lookupZone(key string) (*tzifData, error) {
	if cached, ok := zoneInfoCache.Load(key); ok {
		return cached.(*tzifData), nil
	}
	clean := strings.TrimPrefix(key, "/")
	for _, dir := range zoneInfoPaths() {
		path := filepath.Join(dir, clean)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		parsed, err := parseTZif(data)
		if err != nil {
			continue
		}
		zoneInfoCache.Store(key, parsed)
		return parsed, nil
	}
	return nil, os.ErrNotExist
}

// zoneInfoAt returns the offset, the daylight saving offset and the
// abbreviation in effect at the given UTC timestamp.
func (z *tzifData) zoneInfoAt(ts int64) (int32, int32, string) {
	if len(z.transitions) == 0 {
		return z.firstOffset, z.firstDST, z.firstAbbr
	}
	idx := 0
	for i, t := range z.transitions {
		if t <= ts {
			idx = i
		} else {
			break
		}
	}
	return z.offsets[idx], z.dstOffsets[idx], z.abbreviations[idx]
}

// datetimeTimestamp extracts the UTC timestamp of a datetime object.
func datetimeTimestamp(v val.Value) (int64, bool) {
	if !v.IsInstance() {
		return 0, false
	}
	inst := v.AsInstance()
	if inst.Header.Type != nil && inst.Header.Type.Name == "datetime" {
		dt := (*pyDateTime)(val.AsPointer(v))
		t := time.Date(dt.year, time.Month(dt.month), dt.day, dt.hour, dt.minute, dt.second, dt.microsecond*1000, time.UTC)
		return t.Unix(), true
	}
	if inst.Header.Type != nil && inst.Header.Type.Name == "date" {
		d := (*pyDate)(val.AsPointer(v))
		t := time.Date(d.year, time.Month(d.month), d.day, 0, 0, 0, 0, time.UTC)
		return t.Unix(), true
	}
	return 0, false
}

// initZoneInfoType creates the native ZoneInfo type.
func initZoneInfoType() {
	if zoneInfoType != nil {
		return
	}
	zoneInfoType = val.MustNewType("ZoneInfo", []*val.PyType{val.ObjectType}, nil)
	zoneInfoType.Dict.Set(val.InternString("__module__"), val.InternString("zoneinfo"))

	zoneInfoType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: ZoneInfo() takes at least 1 argument")
		}
		key := args[0].AsString()
		data, err := lookupZone(key)
		if err != nil {
			panic("ZoneInfoNotFoundError: no time zone found with key " + key)
		}
		z := &pyZoneInfo{
			inst: val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: zoneInfoType}, Shape: zoneInfoType.RootShape},
			key:  key,
			data: data,
		}
		return val.ObjectVal(&z.inst.Header)
	}

	zoneInfoType.Dict.Set(val.InternString("key"), val.NewPropertyVal(
		val.NewBuiltinFuncVal("ZoneInfo.key", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			z := (*pyZoneInfo)(val.AsPointer(args[0]))
			return val.NewStringVal(z.key)
		}),
		val.NoneVal(),
		val.NoneVal(),
	))

	zoneInfoType.Dict.Set(val.InternString("utcoffset"), val.NewBuiltinFuncVal("ZoneInfo.utcoffset", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		z := (*pyZoneInfo)(val.AsPointer(args[0]))
		ts, ok := datetimeTimestamp(args[1])
		if !ok {
			return val.NoneVal()
		}
		offset, _, _ := z.data.zoneInfoAt(ts)
		return newTimeDeltaVal(0, int64(offset), 0)
	}))

	zoneInfoType.Dict.Set(val.InternString("dst"), val.NewBuiltinFuncVal("ZoneInfo.dst", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		z := (*pyZoneInfo)(val.AsPointer(args[0]))
		ts, ok := datetimeTimestamp(args[1])
		if !ok {
			return val.NoneVal()
		}
		_, dst, _ := z.data.zoneInfoAt(ts)
		return newTimeDeltaVal(0, int64(dst), 0)
	}))

	zoneInfoType.Dict.Set(val.InternString("tzname"), val.NewBuiltinFuncVal("ZoneInfo.tzname", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		z := (*pyZoneInfo)(val.AsPointer(args[0]))
		ts, ok := datetimeTimestamp(args[1])
		if !ok {
			return val.NoneVal()
		}
		_, _, name := z.data.zoneInfoAt(ts)
		return val.NewStringVal(name)
	}))

	zoneInfoType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("ZoneInfo.__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		z := (*pyZoneInfo)(val.AsPointer(args[0]))
		return val.NewStringVal("zoneinfo.ZoneInfo(key=" + z.key + ")")
	}))

	zoneInfoType.Dict.Set(val.InternString("__str__"), val.NewBuiltinFuncVal("ZoneInfo.__str__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		z := (*pyZoneInfo)(val.AsPointer(args[0]))
		return val.NewStringVal(z.key)
	}))

	zoneInfoType.Dict.Set(val.InternString("no_cache"), val.NewBuiltinFuncVal("ZoneInfo.no_cache", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	zoneInfoType.Dict.Set(val.InternString("clear_cache"), val.NewBuiltinFuncVal("ZoneInfo.clear_cache", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		zoneInfoCache.Range(func(k, _ any) bool {
			zoneInfoCache.Delete(k)
			return true
		})
		return val.NoneVal()
	}))

	zoneInfoType.Dict.Set(val.InternString("from_file"), val.NewBuiltinFuncVal("ZoneInfo.from_file", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: from_file() takes at least 2 arguments")
		}
		key := args[1].AsString()
		data, err := lookupZone(key)
		if err != nil {
			panic("ZoneInfoNotFoundError: no time zone found with key " + key)
		}
		z := &pyZoneInfo{
			inst: val.PyInstance{Header: val.ObjectHeader{Kind: val.KindInstance, Type: zoneInfoType}, Shape: zoneInfoType.RootShape},
			key:  key,
			data: data,
		}
		return val.ObjectVal(&z.inst.Header)
	}))

	zoneInfoType.InvalidateVersion()
}

// createZoneInfoModule builds the _zoneinfo accelerator.
func (e *Engine) createZoneInfoModule() val.Value {
	initZoneInfoType()
	modVal := val.NewModuleVal("_zoneinfo", "time zone database")
	mod := modVal.AsModule()
	mod.Dict.Set(val.InternString("ZoneInfo"), val.TypeVal(zoneInfoType))
	mod.Dict.Set(val.InternString("ZoneInfoNotFoundError"), val.TypeVal(val.KeyErrorType))
	mod.Dict.Set(val.InternString("InvalidTZPathWarning"), val.TypeVal(val.UserWarningType))
	return modVal
}
