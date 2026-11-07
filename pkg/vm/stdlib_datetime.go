package vm

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	datetimeTypesOnce sync.Once
	timedeltaType     *val.PyType
	tzinfoType        *val.PyType
	timezoneType      *val.PyType
	dateType          *val.PyType
	timeType          *val.PyType
	datetimeType      *val.PyType
	isoCalDateType    *val.PyType

	utcTimezoneVal val.Value
)

type pyTimeDelta struct {
	inst         val.PyInstance
	days         int64
	seconds      int64
	microseconds int64
}

type pyTimezone struct {
	inst   val.PyInstance
	offset int64 // in seconds
	name   string
}

type pyDate struct {
	inst  val.PyInstance
	year  int
	month int
	day   int
}

type pyTime struct {
	inst        val.PyInstance
	hour        int
	minute      int
	second      int
	microsecond int
	tzinfo      val.Value
	fold        int
}

type pyDateTime struct {
	inst        val.PyInstance
	year        int
	month       int
	day         int
	hour        int
	minute      int
	second      int
	microsecond int
	tzinfo      val.Value
	fold        int
}

func normTimeDelta(d, s, us int64) (int64, int64, int64) {
	totalUs := (d*86400+s)*1000000 + us
	totalSec := totalUs / 1000000
	remUs := totalUs % 1000000
	if remUs < 0 {
		remUs += 1000000
		totalSec--
	}
	days := totalSec / 86400
	remSec := totalSec % 86400
	if remSec < 0 {
		remSec += 86400
		days--
	}
	return days, remSec, remUs
}

func newTimeDeltaVal(days, seconds, microseconds int64) val.Value {
	d, s, us := normTimeDelta(days, seconds, microseconds)
	td := &pyTimeDelta{
		inst: val.PyInstance{
			Header: val.ObjectHeader{Kind: val.KindInstance, Type: timedeltaType},
			Shape:  timedeltaType.RootShape,
		},
		days:         d,
		seconds:      s,
		microseconds: us,
	}
	return val.ObjectVal(&td.inst.Header)
}

func newTimezoneVal(offsetSeconds int64, name string) val.Value {
	tz := &pyTimezone{
		inst: val.PyInstance{
			Header: val.ObjectHeader{Kind: val.KindInstance, Type: timezoneType},
			Shape:  timezoneType.RootShape,
		},
		offset: offsetSeconds,
		name:   name,
	}
	return val.ObjectVal(&tz.inst.Header)
}

func newDateVal(year, month, day int) val.Value {
	d := &pyDate{
		inst: val.PyInstance{
			Header: val.ObjectHeader{Kind: val.KindInstance, Type: dateType},
			Shape:  dateType.RootShape,
		},
		year:  year,
		month: month,
		day:   day,
	}
	return val.ObjectVal(&d.inst.Header)
}

func newTimeVal(hour, min, sec, us int, tz val.Value, fold int) val.Value {
	if tz.IsNone() {
		tz = val.NoneVal()
	}
	t := &pyTime{
		inst: val.PyInstance{
			Header: val.ObjectHeader{Kind: val.KindInstance, Type: timeType},
			Shape:  timeType.RootShape,
		},
		hour:        hour,
		minute:      min,
		second:      sec,
		microsecond: us,
		tzinfo:      tz,
		fold:        fold,
	}
	return val.ObjectVal(&t.inst.Header)
}

func newDateTimeVal(year, month, day, hour, min, sec, us int, tz val.Value, fold int) val.Value {
	if tz.IsNone() {
		tz = val.NoneVal()
	}
	dt := &pyDateTime{
		inst: val.PyInstance{
			Header: val.ObjectHeader{Kind: val.KindInstance, Type: datetimeType},
			Shape:  datetimeType.RootShape,
		},
		year:        year,
		month:       month,
		day:         day,
		hour:        hour,
		minute:      min,
		second:      sec,
		microsecond: us,
		tzinfo:      tz,
		fold:        fold,
	}
	return val.ObjectVal(&dt.inst.Header)
}

func initDatetimeTypes() {
	datetimeTypesOnce.Do(func() {
		initTzinfoType()
		initTimezoneType()
		initTimeDeltaType()
		initDateType()
		initTimeType()
		initDateTimeType()
	})
}

func initTzinfoType() {
	tzinfoType = val.MustNewType("tzinfo", []*val.PyType{val.ObjectType}, nil)
	tzinfoType.Dict.Set(val.InternString("__module__"), val.InternString("_datetime"))
	tzinfoType.InvalidateVersion()
}

func initTimezoneType() {
	timezoneType = val.MustNewType("timezone", []*val.PyType{tzinfoType}, nil)
	timezoneType.Dict.Set(val.InternString("__module__"), val.InternString("_datetime"))

	timezoneType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if callable.IsType() {
			if len(args) < 1 {
				panic("TypeError: timezone() takes at least 1 argument")
			}
			offsetArg := args[0]
			var offsetSec int64
			if offsetArg.Type() == timedeltaType {
				td := (*pyTimeDelta)(val.AsPointer(offsetArg))
				offsetSec = td.days*86400 + td.seconds
			} else if offsetArg.IsInt64() {
				offsetSec = offsetArg.AsInt64()
			}
			name := ""
			if len(args) > 1 && args[1].IsAnyString() {
				name = args[1].AsString()
			}
			return newTimezoneVal(offsetSec, name)
		}
		panic("TypeError: timezone objects are not callable")
	}

	timezoneType.Dict.Set(val.InternString("utcoffset"), val.NewBuiltinFuncVal("utcoffset", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		tz := (*pyTimezone)(val.AsPointer(args[0]))
		return newTimeDeltaVal(0, tz.offset, 0)
	}))

	timezoneType.Dict.Set(val.InternString("tzname"), val.NewBuiltinFuncVal("tzname", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		tz := (*pyTimezone)(val.AsPointer(args[0]))
		if tz.name != "" {
			return val.NewStringVal(tz.name)
		}
		if tz.offset == 0 {
			return val.NewStringVal("UTC")
		}
		sign := "+"
		off := tz.offset
		if off < 0 {
			sign = "-"
			off = -off
		}
		h := off / 3600
		m := (off % 3600) / 60
		return val.NewStringVal(fmt.Sprintf("UTC%s%02d:%02d", sign, h, m))
	}))

	timezoneType.Dict.Set(val.InternString("dst"), val.NewBuiltinFuncVal("dst", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	timezoneType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		tz := (*pyTimezone)(val.AsPointer(args[0]))
		if tz.offset == 0 && (tz.name == "" || tz.name == "UTC") {
			return val.NewStringVal("datetime.timezone.utc")
		}
		td := newTimeDeltaVal(0, tz.offset, 0)
		if tz.name != "" {
			return val.NewStringVal(fmt.Sprintf("datetime.timezone(%s, %q)", val.PyRepr(ctx, td), tz.name))
		}
		return val.NewStringVal(fmt.Sprintf("datetime.timezone(%s)", val.PyRepr(ctx, td)))
	}))

	utcTimezoneVal = newTimezoneVal(0, "UTC")
	timezoneType.Dict.Set(val.InternString("utc"), utcTimezoneVal)
	timezoneType.InvalidateVersion()
}

func initTimeDeltaType() {
	timedeltaType = val.MustNewType("timedelta", []*val.PyType{val.ObjectType}, nil)
	timedeltaType.Dict.Set(val.InternString("__module__"), val.InternString("_datetime"))

	timedeltaType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if callable.IsType() {
			var d, s, us, ms, min, h, w int64
			pa := posArgs(args, kwnames)
			if len(pa) > 0 && pa[0].IsInt64() {
				d = pa[0].AsInt64()
			}
			if len(pa) > 1 && pa[1].IsInt64() {
				s = pa[1].AsInt64()
			}
			if len(pa) > 2 && pa[2].IsInt64() {
				us = pa[2].AsInt64()
			}
			if len(pa) > 3 && pa[3].IsInt64() {
				ms = pa[3].AsInt64()
			}
			if len(pa) > 4 && pa[4].IsInt64() {
				min = pa[4].AsInt64()
			}
			if len(pa) > 5 && pa[5].IsInt64() {
				h = pa[5].AsInt64()
			}
			if len(pa) > 6 && pa[6].IsInt64() {
				w = pa[6].AsInt64()
			}
			if v, ok := kwArg(args, kwnames, "days"); ok && v.IsInt64() {
				d = v.AsInt64()
			}
			if v, ok := kwArg(args, kwnames, "seconds"); ok && v.IsInt64() {
				s = v.AsInt64()
			}
			if v, ok := kwArg(args, kwnames, "microseconds"); ok && v.IsInt64() {
				us = v.AsInt64()
			}
			if v, ok := kwArg(args, kwnames, "milliseconds"); ok && v.IsInt64() {
				ms = v.AsInt64()
			}
			if v, ok := kwArg(args, kwnames, "minutes"); ok && v.IsInt64() {
				min = v.AsInt64()
			}
			if v, ok := kwArg(args, kwnames, "hours"); ok && v.IsInt64() {
				h = v.AsInt64()
			}
			if v, ok := kwArg(args, kwnames, "weeks"); ok && v.IsInt64() {
				w = v.AsInt64()
			}
			d += w * 7
			s += h*3600 + min*60
			us += ms * 1000
			return newTimeDeltaVal(d, s, us)
		}
		panic("TypeError: timedelta objects are not callable")
	}

	timedeltaType.Slots.TpGetAttr = func(ctx *val.ExecContext, obj, name val.Value) val.Value {
		td := (*pyTimeDelta)(val.AsPointer(obj))
		switch name.AsString() {
		case "days":
			return val.Int64Val(td.days)
		case "seconds":
			return val.Int64Val(td.seconds)
		case "microseconds":
			return val.Int64Val(td.microseconds)
		}
		return val.ObjectGetAttr(ctx, obj, name)
	}

	timedeltaType.Dict.Set(val.InternString("total_seconds"), val.NewBuiltinFuncVal("total_seconds", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		td := (*pyTimeDelta)(val.AsPointer(args[0]))
		sec := float64(td.days)*86400.0 + float64(td.seconds) + float64(td.microseconds)/1e6
		return val.Float64Val(sec)
	}))

	timedeltaType.Slots.NbAdd = func(ctx *val.ExecContext, a, b val.Value) val.Value {
		if a.Type() == timedeltaType && b.Type() == timedeltaType {
			t1 := (*pyTimeDelta)(val.AsPointer(a))
			t2 := (*pyTimeDelta)(val.AsPointer(b))
			return newTimeDeltaVal(t1.days+t2.days, t1.seconds+t2.seconds, t1.microseconds+t2.microseconds)
		}
		return val.NotImplementedVal()
	}

	timedeltaType.Slots.NbSub = func(ctx *val.ExecContext, a, b val.Value) val.Value {
		if a.Type() == timedeltaType && b.Type() == timedeltaType {
			t1 := (*pyTimeDelta)(val.AsPointer(a))
			t2 := (*pyTimeDelta)(val.AsPointer(b))
			return newTimeDeltaVal(t1.days-t2.days, t1.seconds-t2.seconds, t1.microseconds-t2.microseconds)
		}
		return val.NotImplementedVal()
	}

	timedeltaType.Slots.NbMul = func(ctx *val.ExecContext, a, b val.Value) val.Value {
		tdVal, other := a, b
		if b.Type() == timedeltaType {
			tdVal, other = b, a
		}
		if tdVal.Type() != timedeltaType {
			return val.NotImplementedVal()
		}
		td := (*pyTimeDelta)(val.AsPointer(tdVal))
		if other.IsInt64() {
			n := other.AsInt64()
			return newTimeDeltaVal(td.days*n, td.seconds*n, td.microseconds*n)
		}
		if other.IsFloat64() {
			f := other.AsFloat64()
			totalSec := (float64(td.days)*86400.0 + float64(td.seconds) + float64(td.microseconds)/1e6) * f
			sec := int64(totalSec)
			us := int64(math.Round((totalSec - float64(sec)) * 1e6))
			return newTimeDeltaVal(0, sec, us)
		}
		return val.NotImplementedVal()
	}

	timedeltaType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		td := (*pyTimeDelta)(val.AsPointer(args[0]))
		parts := []string{}
		if td.days != 0 {
			parts = append(parts, fmt.Sprintf("days=%d", td.days))
		}
		if td.seconds != 0 {
			parts = append(parts, fmt.Sprintf("seconds=%d", td.seconds))
		}
		if td.microseconds != 0 {
			parts = append(parts, fmt.Sprintf("microseconds=%d", td.microseconds))
		}
		if len(parts) == 0 {
			return val.NewStringVal("datetime.timedelta(0)")
		}
		return val.NewStringVal("datetime.timedelta(" + strings.Join(parts, ", ") + ")")
	}))

	timedeltaType.Dict.Set(val.InternString("__str__"), val.NewBuiltinFuncVal("__str__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		td := (*pyTimeDelta)(val.AsPointer(args[0]))
		h := td.seconds / 3600
		m := (td.seconds % 3600) / 60
		s := td.seconds % 60
		dayStr := ""
		if td.days == 1 {
			dayStr = "1 day, "
		} else if td.days != 0 {
			dayStr = fmt.Sprintf("%d days, ", td.days)
		}
		if td.microseconds != 0 {
			return val.NewStringVal(fmt.Sprintf("%s%d:%02d:%02d.%06d", dayStr, h, m, s, td.microseconds))
		}
		return val.NewStringVal(fmt.Sprintf("%s%d:%02d:%02d", dayStr, h, m, s))
	}))

	timedeltaType.Dict.Set(val.InternString("__eq__"), val.NewBuiltinFuncVal("__eq__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if args[1].Type() != timedeltaType {
			return val.BoolVal(false)
		}
		t1 := (*pyTimeDelta)(val.AsPointer(args[0]))
		t2 := (*pyTimeDelta)(val.AsPointer(args[1]))
		return val.BoolVal(t1.days == t2.days && t1.seconds == t2.seconds && t1.microseconds == t2.microseconds)
	}))

	timedeltaType.Dict.Set(val.InternString("__lt__"), val.NewBuiltinFuncVal("__lt__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if args[1].Type() != timedeltaType {
			panic("TypeError: '<' not supported between instances of 'timedelta' and '" + args[1].Type().Name + "'")
		}
		t1 := (*pyTimeDelta)(val.AsPointer(args[0]))
		t2 := (*pyTimeDelta)(val.AsPointer(args[1]))
		us1 := (t1.days*86400+t1.seconds)*1000000 + t1.microseconds
		us2 := (t2.days*86400+t2.seconds)*1000000 + t2.microseconds
		return val.BoolVal(us1 < us2)
	}))

	timedeltaType.Dict.Set(val.InternString("__le__"), val.NewBuiltinFuncVal("__le__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if args[1].Type() != timedeltaType {
			panic("TypeError: '<=' not supported between instances of 'timedelta' and '" + args[1].Type().Name + "'")
		}
		t1 := (*pyTimeDelta)(val.AsPointer(args[0]))
		t2 := (*pyTimeDelta)(val.AsPointer(args[1]))
		us1 := (t1.days*86400+t1.seconds)*1000000 + t1.microseconds
		us2 := (t2.days*86400+t2.seconds)*1000000 + t2.microseconds
		return val.BoolVal(us1 <= us2)
	}))

	timedeltaType.Dict.Set(val.InternString("__gt__"), val.NewBuiltinFuncVal("__gt__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if args[1].Type() != timedeltaType {
			panic("TypeError: '>' not supported between instances of 'timedelta' and '" + args[1].Type().Name + "'")
		}
		t1 := (*pyTimeDelta)(val.AsPointer(args[0]))
		t2 := (*pyTimeDelta)(val.AsPointer(args[1]))
		us1 := (t1.days*86400+t1.seconds)*1000000 + t1.microseconds
		us2 := (t2.days*86400+t2.seconds)*1000000 + t2.microseconds
		return val.BoolVal(us1 > us2)
	}))

	timedeltaType.Dict.Set(val.InternString("__ge__"), val.NewBuiltinFuncVal("__ge__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if args[1].Type() != timedeltaType {
			panic("TypeError: '>=' not supported between instances of 'timedelta' and '" + args[1].Type().Name + "'")
		}
		t1 := (*pyTimeDelta)(val.AsPointer(args[0]))
		t2 := (*pyTimeDelta)(val.AsPointer(args[1]))
		us1 := (t1.days*86400+t1.seconds)*1000000 + t1.microseconds
		us2 := (t2.days*86400+t2.seconds)*1000000 + t2.microseconds
		return val.BoolVal(us1 >= us2)
	}))

	timedeltaType.Dict.Set(val.InternString("__hash__"), val.NewBuiltinFuncVal("__hash__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		td := (*pyTimeDelta)(val.AsPointer(args[0]))
		us := (td.days*86400+td.seconds)*1000000 + td.microseconds
		return val.Int64Val(us)
	}))

	timedeltaType.InvalidateVersion()
}

func initDateType() {
	dateType = val.MustNewType("date", []*val.PyType{val.ObjectType}, nil)
	dateType.Dict.Set(val.InternString("__module__"), val.InternString("_datetime"))

	dateType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if callable.IsType() {
			if len(args) < 3 {
				panic("TypeError: date() takes 3 arguments (year, month, day)")
			}
			y := int(args[0].AsInt64())
			m := int(args[1].AsInt64())
			d := int(args[2].AsInt64())
			if y < 1 || y > 9999 || m < 1 || m > 12 || d < 1 || d > 31 {
				panic("ValueError: date component out of range")
			}
			return newDateVal(y, m, d)
		}
		panic("TypeError: date objects are not callable")
	}

	dateType.Slots.TpGetAttr = func(ctx *val.ExecContext, obj, name val.Value) val.Value {
		d := (*pyDate)(val.AsPointer(obj))
		switch name.AsString() {
		case "year":
			return val.Int64Val(int64(d.year))
		case "month":
			return val.Int64Val(int64(d.month))
		case "day":
			return val.Int64Val(int64(d.day))
		}
		return val.ObjectGetAttr(ctx, obj, name)
	}

	dateType.Dict.Set(val.InternString("today"), val.NewBuiltinFuncVal("today", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		now := time.Now()
		return newDateVal(now.Year(), int(now.Month()), now.Day())
	}))

	dateType.Dict.Set(val.InternString("fromtimestamp"), val.NewBuiltinFuncVal("fromtimestamp", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		var ts float64
		if len(args) > 1 {
			if args[1].IsInt64() {
				ts = float64(args[1].AsInt64())
			} else if args[1].IsFloat64() {
				ts = args[1].AsFloat64()
			}
		}
		sec := int64(ts)
		nsec := int64((ts - float64(sec)) * 1e9)
		t := time.Unix(sec, nsec)
		return newDateVal(t.Year(), int(t.Month()), t.Day())
	}))

	dateType.Dict.Set(val.InternString("fromisoformat"), val.NewBuiltinFuncVal("fromisoformat", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		s := ""
		if len(args) > 1 {
			s = args[1].AsString()
		}
		parts := strings.Split(s, "-")
		if len(parts) == 3 {
			y, _ := strconv.Atoi(parts[0])
			m, _ := strconv.Atoi(parts[1])
			d, _ := strconv.Atoi(parts[2])
			if y >= 1 && y <= 9999 && m >= 1 && m <= 12 && d >= 1 && d <= 31 {
				return newDateVal(y, m, d)
			}
		}
		panic("ValueError: Invalid isoformat string: " + s)
	}))

	dateType.Dict.Set(val.InternString("isoformat"), val.NewBuiltinFuncVal("isoformat", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		d := (*pyDate)(val.AsPointer(args[0]))
		return val.NewStringVal(fmt.Sprintf("%04d-%02d-%02d", d.year, d.month, d.day))
	}))

	dateType.Dict.Set(val.InternString("weekday"), val.NewBuiltinFuncVal("weekday", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		d := (*pyDate)(val.AsPointer(args[0]))
		t := time.Date(d.year, time.Month(d.month), d.day, 0, 0, 0, 0, time.UTC)
		// Go Sunday = 0, Python Monday = 0
		w := (int(t.Weekday()) + 6) % 7
		return val.Int64Val(int64(w))
	}))

	dateType.Dict.Set(val.InternString("isoweekday"), val.NewBuiltinFuncVal("isoweekday", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		d := (*pyDate)(val.AsPointer(args[0]))
		t := time.Date(d.year, time.Month(d.month), d.day, 0, 0, 0, 0, time.UTC)
		w := (int(t.Weekday())+6)%7 + 1
		return val.Int64Val(int64(w))
	}))

	dateType.Dict.Set(val.InternString("replace"), val.NewBuiltinFuncVal("replace", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		d := (*pyDate)(val.AsPointer(args[0]))
		y, m, day := d.year, d.month, d.day
		if v, ok := kwArg(args, kwnames, "year"); ok && v.IsInt64() {
			y = int(v.AsInt64())
		}
		if v, ok := kwArg(args, kwnames, "month"); ok && v.IsInt64() {
			m = int(v.AsInt64())
		}
		if v, ok := kwArg(args, kwnames, "day"); ok && v.IsInt64() {
			day = int(v.AsInt64())
		}
		return newDateVal(y, m, day)
	}))

	dateType.Dict.Set(val.InternString("timetuple"), val.NewBuiltinFuncVal("timetuple", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		d := (*pyDate)(val.AsPointer(args[0]))
		t := time.Date(d.year, time.Month(d.month), d.day, 0, 0, 0, 0, time.UTC)
		w := (int(t.Weekday()) + 6) % 7
		yday := t.YearDay()
		return val.NewTupleVal(
			val.Int64Val(int64(d.year)),
			val.Int64Val(int64(d.month)),
			val.Int64Val(int64(d.day)),
			val.Int64Val(0), val.Int64Val(0), val.Int64Val(0),
			val.Int64Val(int64(w)),
			val.Int64Val(int64(yday)),
			val.Int64Val(-1),
		)
	}))

	dateType.Dict.Set(val.InternString("strftime"), val.NewBuiltinFuncVal("strftime", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: strftime() requires a format string")
		}
		d := (*pyDate)(val.AsPointer(args[0]))
		t := time.Date(d.year, time.Month(d.month), d.day, 0, 0, 0, 0, time.UTC)
		return val.NewStringVal(pythonStrftime(t, 0, args[1].AsString()))
	}))

	dateType.Slots.NbAdd = func(ctx *val.ExecContext, a, b val.Value) val.Value {
		if a.Type() == dateType && b.Type() == timedeltaType {
			d := (*pyDate)(val.AsPointer(a))
			td := (*pyTimeDelta)(val.AsPointer(b))
			t := time.Date(d.year, time.Month(d.month), d.day, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(td.days))
			return newDateVal(t.Year(), int(t.Month()), t.Day())
		}
		return val.NotImplementedVal()
	}

	dateType.Slots.NbSub = func(ctx *val.ExecContext, a, b val.Value) val.Value {
		if a.Type() == dateType && b.Type() == timedeltaType {
			d := (*pyDate)(val.AsPointer(a))
			td := (*pyTimeDelta)(val.AsPointer(b))
			t := time.Date(d.year, time.Month(d.month), d.day, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(-td.days))
			return newDateVal(t.Year(), int(t.Month()), t.Day())
		}
		if a.Type() == dateType && b.Type() == dateType {
			d1 := (*pyDate)(val.AsPointer(a))
			d2 := (*pyDate)(val.AsPointer(b))
			t1 := time.Date(d1.year, time.Month(d1.month), d1.day, 0, 0, 0, 0, time.UTC)
			t2 := time.Date(d2.year, time.Month(d2.month), d2.day, 0, 0, 0, 0, time.UTC)
			days := int64(t1.Sub(t2).Hours() / 24)
			return newTimeDeltaVal(days, 0, 0)
		}
		return val.NotImplementedVal()
	}

	dateType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		d := (*pyDate)(val.AsPointer(args[0]))
		return val.NewStringVal(fmt.Sprintf("datetime.date(%d, %d, %d)", d.year, d.month, d.day))
	}))

	dateType.Dict.Set(val.InternString("__str__"), val.NewBuiltinFuncVal("__str__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		d := (*pyDate)(val.AsPointer(args[0]))
		return val.NewStringVal(fmt.Sprintf("%04d-%02d-%02d", d.year, d.month, d.day))
	}))

	dateType.Dict.Set(val.InternString("__eq__"), val.NewBuiltinFuncVal("__eq__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if args[1].Type() != dateType {
			return val.BoolVal(false)
		}
		d1 := (*pyDate)(val.AsPointer(args[0]))
		d2 := (*pyDate)(val.AsPointer(args[1]))
		return val.BoolVal(d1.year == d2.year && d1.month == d2.month && d1.day == d2.day)
	}))

	dateType.Dict.Set(val.InternString("__lt__"), val.NewBuiltinFuncVal("__lt__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if args[1].Type() != dateType {
			panic("TypeError: '<' not supported between instances of 'date' and '" + args[1].Type().Name + "'")
		}
		d1 := (*pyDate)(val.AsPointer(args[0]))
		d2 := (*pyDate)(val.AsPointer(args[1]))
		if d1.year != d2.year {
			return val.BoolVal(d1.year < d2.year)
		}
		if d1.month != d2.month {
			return val.BoolVal(d1.month < d2.month)
		}
		return val.BoolVal(d1.day < d2.day)
	}))

	dateType.Dict.Set(val.InternString("__hash__"), val.NewBuiltinFuncVal("__hash__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		d := (*pyDate)(val.AsPointer(args[0]))
		return val.Int64Val(int64(d.year*10000 + d.month*100 + d.day))
	}))

	dateType.InvalidateVersion()
}

func initTimeType() {
	timeType = val.MustNewType("time", []*val.PyType{val.ObjectType}, nil)
	timeType.Dict.Set(val.InternString("__module__"), val.InternString("_datetime"))

	timeType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if callable.IsType() {
			var h, m, s, us, fold int
			tz := val.NoneVal()
			pa := posArgs(args, kwnames)
			if len(pa) > 0 && pa[0].IsInt64() {
				h = int(pa[0].AsInt64())
			}
			if len(pa) > 1 && pa[1].IsInt64() {
				m = int(pa[1].AsInt64())
			}
			if len(pa) > 2 && pa[2].IsInt64() {
				s = int(pa[2].AsInt64())
			}
			if len(pa) > 3 && pa[3].IsInt64() {
				us = int(pa[3].AsInt64())
			}
			if len(pa) > 4 {
				tz = pa[4]
			}
			if v, ok := kwArg(args, kwnames, "hour"); ok && v.IsInt64() {
				h = int(v.AsInt64())
			}
			if v, ok := kwArg(args, kwnames, "minute"); ok && v.IsInt64() {
				m = int(v.AsInt64())
			}
			if v, ok := kwArg(args, kwnames, "second"); ok && v.IsInt64() {
				s = int(v.AsInt64())
			}
			if v, ok := kwArg(args, kwnames, "microsecond"); ok && v.IsInt64() {
				us = int(v.AsInt64())
			}
			if v, ok := kwArg(args, kwnames, "tzinfo"); ok {
				tz = v
			}
			if v, ok := kwArg(args, kwnames, "fold"); ok && v.IsInt64() {
				fold = int(v.AsInt64())
			}
			return newTimeVal(h, m, s, us, tz, fold)
		}
		panic("TypeError: time objects are not callable")
	}

	timeType.Slots.TpGetAttr = func(ctx *val.ExecContext, obj, name val.Value) val.Value {
		tm := (*pyTime)(val.AsPointer(obj))
		switch name.AsString() {
		case "hour":
			return val.Int64Val(int64(tm.hour))
		case "minute":
			return val.Int64Val(int64(tm.minute))
		case "second":
			return val.Int64Val(int64(tm.second))
		case "microsecond":
			return val.Int64Val(int64(tm.microsecond))
		case "tzinfo":
			return tm.tzinfo
		case "fold":
			return val.Int64Val(int64(tm.fold))
		}
		return val.ObjectGetAttr(ctx, obj, name)
	}

	timeType.Dict.Set(val.InternString("isoformat"), val.NewBuiltinFuncVal("isoformat", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		tm := (*pyTime)(val.AsPointer(args[0]))
		res := fmt.Sprintf("%02d:%02d:%02d", tm.hour, tm.minute, tm.second)
		if tm.microsecond != 0 {
			res += fmt.Sprintf(".%06d", tm.microsecond)
		}
		if !tm.tzinfo.IsNone() && tm.tzinfo.Type() == timezoneType {
			tz := (*pyTimezone)(val.AsPointer(tm.tzinfo))
			if tz.offset == 0 {
				res += "+00:00"
			} else {
				sign := "+"
				off := tz.offset
				if off < 0 {
					sign = "-"
					off = -off
				}
				res += fmt.Sprintf("%s%02d:%02d", sign, off/3600, (off%3600)/60)
			}
		}
		return val.NewStringVal(res)
	}))

	timeType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		tm := (*pyTime)(val.AsPointer(args[0]))
		if tm.microsecond != 0 {
			return val.NewStringVal(fmt.Sprintf("datetime.time(%d, %d, %d, %d)", tm.hour, tm.minute, tm.second, tm.microsecond))
		}
		return val.NewStringVal(fmt.Sprintf("datetime.time(%d, %d, %d)", tm.hour, tm.minute, tm.second))
	}))

	timeType.Dict.Set(val.InternString("__str__"), val.NewBuiltinFuncVal("__str__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		tm := (*pyTime)(val.AsPointer(args[0]))
		if tm.microsecond != 0 {
			return val.NewStringVal(fmt.Sprintf("%02d:%02d:%02d.%06d", tm.hour, tm.minute, tm.second, tm.microsecond))
		}
		return val.NewStringVal(fmt.Sprintf("%02d:%02d:%02d", tm.hour, tm.minute, tm.second))
	}))

	timeType.InvalidateVersion()
}

func initDateTimeType() {
	datetimeType = val.MustNewType("datetime", []*val.PyType{dateType}, nil)
	datetimeType.Dict.Set(val.InternString("__module__"), val.InternString("_datetime"))

	datetimeType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if callable.IsType() {
			var y, m, d, h, min, s, us, fold int
			tz := val.NoneVal()
			pa := posArgs(args, kwnames)
			if len(pa) > 0 && pa[0].IsInt64() {
				y = int(pa[0].AsInt64())
			}
			if len(pa) > 1 && pa[1].IsInt64() {
				m = int(pa[1].AsInt64())
			}
			if len(pa) > 2 && pa[2].IsInt64() {
				d = int(pa[2].AsInt64())
			}
			if len(pa) > 3 && pa[3].IsInt64() {
				h = int(pa[3].AsInt64())
			}
			if len(pa) > 4 && pa[4].IsInt64() {
				min = int(pa[4].AsInt64())
			}
			if len(pa) > 5 && pa[5].IsInt64() {
				s = int(pa[5].AsInt64())
			}
			if len(pa) > 6 && pa[6].IsInt64() {
				us = int(pa[6].AsInt64())
			}
			if len(pa) > 7 {
				tz = pa[7]
			}
			if v, ok := kwArg(args, kwnames, "year"); ok && v.IsInt64() {
				y = int(v.AsInt64())
			}
			if v, ok := kwArg(args, kwnames, "month"); ok && v.IsInt64() {
				m = int(v.AsInt64())
			}
			if v, ok := kwArg(args, kwnames, "day"); ok && v.IsInt64() {
				d = int(v.AsInt64())
			}
			if v, ok := kwArg(args, kwnames, "hour"); ok && v.IsInt64() {
				h = int(v.AsInt64())
			}
			if v, ok := kwArg(args, kwnames, "minute"); ok && v.IsInt64() {
				min = int(v.AsInt64())
			}
			if v, ok := kwArg(args, kwnames, "second"); ok && v.IsInt64() {
				s = int(v.AsInt64())
			}
			if v, ok := kwArg(args, kwnames, "microsecond"); ok && v.IsInt64() {
				us = int(v.AsInt64())
			}
			if v, ok := kwArg(args, kwnames, "tzinfo"); ok {
				tz = v
			}
			if v, ok := kwArg(args, kwnames, "fold"); ok && v.IsInt64() {
				fold = int(v.AsInt64())
			}
			return newDateTimeVal(y, m, d, h, min, s, us, tz, fold)
		}
		panic("TypeError: datetime objects are not callable")
	}

	datetimeType.Slots.TpGetAttr = func(ctx *val.ExecContext, obj, name val.Value) val.Value {
		dt := (*pyDateTime)(val.AsPointer(obj))
		switch name.AsString() {
		case "year":
			return val.Int64Val(int64(dt.year))
		case "month":
			return val.Int64Val(int64(dt.month))
		case "day":
			return val.Int64Val(int64(dt.day))
		case "hour":
			return val.Int64Val(int64(dt.hour))
		case "minute":
			return val.Int64Val(int64(dt.minute))
		case "second":
			return val.Int64Val(int64(dt.second))
		case "microsecond":
			return val.Int64Val(int64(dt.microsecond))
		case "tzinfo":
			return dt.tzinfo
		case "fold":
			return val.Int64Val(int64(dt.fold))
		}
		return val.ObjectGetAttr(ctx, obj, name)
	}

	datetimeType.Dict.Set(val.InternString("now"), val.NewBuiltinFuncVal("now", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		now := time.Now()
		tz := val.NoneVal()
		pa := posArgs(args, kwnames)
		if len(pa) > 0 {
			if pa[0].IsType() && len(pa) > 1 {
				tz = pa[1]
			} else if !pa[0].IsType() {
				tz = pa[0]
			}
		} else if v, ok := kwArg(args, kwnames, "tz"); ok {
			tz = v
		}
		if !tz.IsNone() && tz.Type() == timezoneType {
			offsetSec := (*pyTimezone)(val.AsPointer(tz)).offset
			now = now.UTC().Add(time.Duration(offsetSec) * time.Second)
		}
		return newDateTimeVal(now.Year(), int(now.Month()), now.Day(), now.Hour(), now.Minute(), now.Second(), now.Nanosecond()/1000, tz, 0)
	}))

	datetimeType.Dict.Set(val.InternString("utcnow"), val.NewBuiltinFuncVal("utcnow", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		now := time.Now().UTC()
		return newDateTimeVal(now.Year(), int(now.Month()), now.Day(), now.Hour(), now.Minute(), now.Second(), now.Nanosecond()/1000, val.NoneVal(), 0)
	}))

	datetimeType.Dict.Set(val.InternString("fromtimestamp"), val.NewBuiltinFuncVal("fromtimestamp", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		var ts float64
		pa := posArgs(args, kwnames)
		tsArgIdx := 0
		if len(pa) > 0 && pa[0].IsType() {
			tsArgIdx = 1
		}
		if len(pa) > tsArgIdx {
			if pa[tsArgIdx].IsInt64() {
				ts = float64(pa[tsArgIdx].AsInt64())
			} else if pa[tsArgIdx].IsFloat64() {
				ts = pa[tsArgIdx].AsFloat64()
			}
		}
		tz := val.NoneVal()
		if len(pa) > tsArgIdx+1 {
			tz = pa[tsArgIdx+1]
		} else if v, ok := kwArg(args, kwnames, "tz"); ok {
			tz = v
		}
		sec := int64(ts)
		nsec := int64((ts - float64(sec)) * 1e9)
		t := time.Unix(sec, nsec)
		if !tz.IsNone() && tz.Type() == timezoneType {
			off := (*pyTimezone)(val.AsPointer(tz)).offset
			t = t.UTC().Add(time.Duration(off) * time.Second)
		}
		return newDateTimeVal(t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond()/1000, tz, 0)
	}))

	datetimeType.Dict.Set(val.InternString("fromisoformat"), val.NewBuiltinFuncVal("fromisoformat", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		s := ""
		pa := posArgs(args, kwnames)
		if len(pa) == 1 {
			s = pa[0].AsString()
		} else if len(pa) > 1 {
			s = pa[1].AsString()
		}
		dt, err := parseIsoFormat(s)
		if err != nil {
			panic("ValueError: Invalid isoformat string: " + s)
		}
		return dt
	}))

	datetimeType.Dict.Set(val.InternString("combine"), val.NewBuiltinFuncVal("combine", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 2 {
			panic("TypeError: combine() requires date and time arguments")
		}
		dVal := pa[0]
		tVal := pa[1]
		if pa[0].IsType() && len(pa) >= 3 {
			dVal = pa[1]
			tVal = pa[2]
		}
		var y, m, day, h, min, sec, us int
		tz := val.NoneVal()
		if dVal.Type() == dateType || dVal.Type() == datetimeType {
			d := (*pyDate)(val.AsPointer(dVal))
			y, m, day = d.year, d.month, d.day
		}
		if tVal.Type() == timeType {
			tm := (*pyTime)(val.AsPointer(tVal))
			h, min, sec, us = tm.hour, tm.minute, tm.second, tm.microsecond
			tz = tm.tzinfo
		}
		if len(pa) > 3 {
			tz = pa[3]
		} else if v, ok := kwArg(args, kwnames, "tzinfo"); ok {
			tz = v
		}
		return newDateTimeVal(y, m, day, h, min, sec, us, tz, 0)
	}))

	datetimeType.Dict.Set(val.InternString("date"), val.NewBuiltinFuncVal("date", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		dt := (*pyDateTime)(val.AsPointer(args[0]))
		return newDateVal(dt.year, dt.month, dt.day)
	}))

	datetimeType.Dict.Set(val.InternString("time"), val.NewBuiltinFuncVal("time", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		dt := (*pyDateTime)(val.AsPointer(args[0]))
		return newTimeVal(dt.hour, dt.minute, dt.second, dt.microsecond, val.NoneVal(), dt.fold)
	}))

	datetimeType.Dict.Set(val.InternString("timetz"), val.NewBuiltinFuncVal("timetz", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		dt := (*pyDateTime)(val.AsPointer(args[0]))
		return newTimeVal(dt.hour, dt.minute, dt.second, dt.microsecond, dt.tzinfo, dt.fold)
	}))

	datetimeType.Dict.Set(val.InternString("isoformat"), val.NewBuiltinFuncVal("isoformat", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		dt := (*pyDateTime)(val.AsPointer(args[0]))
		sep := "T"
		if len(args) > 1 && args[1].IsAnyString() {
			sep = args[1].AsString()
		}
		res := fmt.Sprintf("%04d-%02d-%02d%s%02d:%02d:%02d", dt.year, dt.month, dt.day, sep, dt.hour, dt.minute, dt.second)
		if dt.microsecond != 0 {
			res += fmt.Sprintf(".%06d", dt.microsecond)
		}
		if !dt.tzinfo.IsNone() && dt.tzinfo.Type() == timezoneType {
			tz := (*pyTimezone)(val.AsPointer(dt.tzinfo))
			if tz.offset == 0 {
				res += "+00:00"
			} else {
				sign := "+"
				off := tz.offset
				if off < 0 {
					sign = "-"
					off = -off
				}
				res += fmt.Sprintf("%s%02d:%02d", sign, off/3600, (off%3600)/60)
			}
		}
		return val.NewStringVal(res)
	}))

	datetimeType.Dict.Set(val.InternString("strftime"), val.NewBuiltinFuncVal("strftime", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: strftime() requires a format string")
		}
		dt := (*pyDateTime)(val.AsPointer(args[0]))
		t := time.Date(dt.year, time.Month(dt.month), dt.day, dt.hour, dt.minute, dt.second, dt.microsecond*1000, time.UTC)
		return val.NewStringVal(pythonStrftime(t, dt.microsecond, args[1].AsString()))
	}))

	datetimeType.Dict.Set(val.InternString("timestamp"), val.NewBuiltinFuncVal("timestamp", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		dt := (*pyDateTime)(val.AsPointer(args[0]))
		t := time.Date(dt.year, time.Month(dt.month), dt.day, dt.hour, dt.minute, dt.second, dt.microsecond*1000, time.UTC)
		if !dt.tzinfo.IsNone() && dt.tzinfo.Type() == timezoneType {
			off := (*pyTimezone)(val.AsPointer(dt.tzinfo)).offset
			t = t.Add(-time.Duration(off) * time.Second)
		}
		ts := float64(t.Unix()) + float64(dt.microsecond)/1e6
		return val.Float64Val(ts)
	}))

	datetimeType.Dict.Set(val.InternString("weekday"), val.NewBuiltinFuncVal("weekday", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		dt := (*pyDateTime)(val.AsPointer(args[0]))
		t := time.Date(dt.year, time.Month(dt.month), dt.day, 0, 0, 0, 0, time.UTC)
		w := (int(t.Weekday()) + 6) % 7
		return val.Int64Val(int64(w))
	}))

	datetimeType.Dict.Set(val.InternString("isoweekday"), val.NewBuiltinFuncVal("isoweekday", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		dt := (*pyDateTime)(val.AsPointer(args[0]))
		t := time.Date(dt.year, time.Month(dt.month), dt.day, 0, 0, 0, 0, time.UTC)
		w := (int(t.Weekday())+6)%7 + 1
		return val.Int64Val(int64(w))
	}))

	datetimeType.Dict.Set(val.InternString("replace"), val.NewBuiltinFuncVal("replace", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		dt := (*pyDateTime)(val.AsPointer(args[0]))
		y, m, d := dt.year, dt.month, dt.day
		h, min, s, us := dt.hour, dt.minute, dt.second, dt.microsecond
		tz := dt.tzinfo
		fold := dt.fold
		if v, ok := kwArg(args, kwnames, "year"); ok && v.IsInt64() {
			y = int(v.AsInt64())
		}
		if v, ok := kwArg(args, kwnames, "month"); ok && v.IsInt64() {
			m = int(v.AsInt64())
		}
		if v, ok := kwArg(args, kwnames, "day"); ok && v.IsInt64() {
			d = int(v.AsInt64())
		}
		if v, ok := kwArg(args, kwnames, "hour"); ok && v.IsInt64() {
			h = int(v.AsInt64())
		}
		if v, ok := kwArg(args, kwnames, "minute"); ok && v.IsInt64() {
			min = int(v.AsInt64())
		}
		if v, ok := kwArg(args, kwnames, "second"); ok && v.IsInt64() {
			s = int(v.AsInt64())
		}
		if v, ok := kwArg(args, kwnames, "microsecond"); ok && v.IsInt64() {
			us = int(v.AsInt64())
		}
		if v, ok := kwArg(args, kwnames, "tzinfo"); ok {
			tz = v
		}
		if v, ok := kwArg(args, kwnames, "fold"); ok && v.IsInt64() {
			fold = int(v.AsInt64())
		}
		return newDateTimeVal(y, m, d, h, min, s, us, tz, fold)
	}))

	datetimeType.Slots.NbAdd = func(ctx *val.ExecContext, a, b val.Value) val.Value {
		if a.Type() == datetimeType && b.Type() == timedeltaType {
			dt := (*pyDateTime)(val.AsPointer(a))
			td := (*pyTimeDelta)(val.AsPointer(b))
			t := time.Date(dt.year, time.Month(dt.month), dt.day, dt.hour, dt.minute, dt.second, dt.microsecond*1000, time.UTC)
			dur := time.Duration(td.days)*24*time.Hour + time.Duration(td.seconds)*time.Second + time.Duration(td.microseconds)*time.Microsecond
			t = t.Add(dur)
			return newDateTimeVal(t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond()/1000, dt.tzinfo, dt.fold)
		}
		return val.NotImplementedVal()
	}

	datetimeType.Slots.NbSub = func(ctx *val.ExecContext, a, b val.Value) val.Value {
		if a.Type() == datetimeType && b.Type() == timedeltaType {
			dt := (*pyDateTime)(val.AsPointer(a))
			td := (*pyTimeDelta)(val.AsPointer(b))
			t := time.Date(dt.year, time.Month(dt.month), dt.day, dt.hour, dt.minute, dt.second, dt.microsecond*1000, time.UTC)
			dur := time.Duration(td.days)*24*time.Hour + time.Duration(td.seconds)*time.Second + time.Duration(td.microseconds)*time.Microsecond
			t = t.Add(-dur)
			return newDateTimeVal(t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond()/1000, dt.tzinfo, dt.fold)
		}
		if a.Type() == datetimeType && b.Type() == datetimeType {
			dt1 := (*pyDateTime)(val.AsPointer(a))
			dt2 := (*pyDateTime)(val.AsPointer(b))
			t1 := time.Date(dt1.year, time.Month(dt1.month), dt1.day, dt1.hour, dt1.minute, dt1.second, dt1.microsecond*1000, time.UTC)
			t2 := time.Date(dt2.year, time.Month(dt2.month), dt2.day, dt2.hour, dt2.minute, dt2.second, dt2.microsecond*1000, time.UTC)
			dur := t1.Sub(t2)
			us := dur.Microseconds()
			return newTimeDeltaVal(0, 0, us)
		}
		return val.NotImplementedVal()
	}

	datetimeType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		dt := (*pyDateTime)(val.AsPointer(args[0]))
		parts := []string{fmt.Sprintf("%d, %d, %d, %d, %d", dt.year, dt.month, dt.day, dt.hour, dt.minute)}
		if dt.second != 0 || dt.microsecond != 0 {
			parts = append(parts, fmt.Sprintf("%d", dt.second))
		}
		if dt.microsecond != 0 {
			parts = append(parts, fmt.Sprintf("%d", dt.microsecond))
		}
		if !dt.tzinfo.IsNone() {
			parts = append(parts, fmt.Sprintf("tzinfo=%s", val.PyRepr(ctx, dt.tzinfo)))
		}
		return val.NewStringVal("datetime.datetime(" + strings.Join(parts, ", ") + ")")
	}))

	datetimeType.Dict.Set(val.InternString("__str__"), val.NewBuiltinFuncVal("__str__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		dt := (*pyDateTime)(val.AsPointer(args[0]))
		res := fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d", dt.year, dt.month, dt.day, dt.hour, dt.minute, dt.second)
		if dt.microsecond != 0 {
			res += fmt.Sprintf(".%06d", dt.microsecond)
		}
		return val.NewStringVal(res)
	}))

	datetimeType.Dict.Set(val.InternString("__eq__"), val.NewBuiltinFuncVal("__eq__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if args[1].Type() != datetimeType {
			return val.BoolVal(false)
		}
		dt1 := (*pyDateTime)(val.AsPointer(args[0]))
		dt2 := (*pyDateTime)(val.AsPointer(args[1]))
		return val.BoolVal(dt1.year == dt2.year && dt1.month == dt2.month && dt1.day == dt2.day &&
			dt1.hour == dt2.hour && dt1.minute == dt2.minute && dt1.second == dt2.second && dt1.microsecond == dt2.microsecond)
	}))

	datetimeType.Dict.Set(val.InternString("__lt__"), val.NewBuiltinFuncVal("__lt__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if args[1].Type() != datetimeType {
			panic("TypeError: '<' not supported between instances of 'datetime' and '" + args[1].Type().Name + "'")
		}
		dt1 := (*pyDateTime)(val.AsPointer(args[0]))
		dt2 := (*pyDateTime)(val.AsPointer(args[1]))
		t1 := time.Date(dt1.year, time.Month(dt1.month), dt1.day, dt1.hour, dt1.minute, dt1.second, dt1.microsecond*1000, time.UTC)
		t2 := time.Date(dt2.year, time.Month(dt2.month), dt2.day, dt2.hour, dt2.minute, dt2.second, dt2.microsecond*1000, time.UTC)
		return val.BoolVal(t1.Before(t2))
	}))

	datetimeType.Dict.Set(val.InternString("__hash__"), val.NewBuiltinFuncVal("__hash__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		dt := (*pyDateTime)(val.AsPointer(args[0]))
		t := time.Date(dt.year, time.Month(dt.month), dt.day, dt.hour, dt.minute, dt.second, dt.microsecond*1000, time.UTC)
		return val.Int64Val(t.UnixNano())
	}))

	datetimeType.InvalidateVersion()
}

func parseIsoFormat(s string) (val.Value, error) {
	s = strings.TrimSpace(s)
	// Try standard RFC3339 formats first
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			var tz val.Value = val.NoneVal()
			if strings.HasSuffix(s, "Z") || strings.Contains(s, "+") || (strings.Count(s, "-") > 2) {
				_, offset := t.Zone()
				tz = newTimezoneVal(int64(offset), "")
			}
			us := t.Nanosecond() / 1000
			return newDateTimeVal(t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second(), us, tz, 0), nil
		}
	}
	return val.NoneVal(), fmt.Errorf("invalid isoformat: %s", s)
}

func pythonStrftime(t time.Time, us int, format string) string {
	var buf strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] == '%' && i+1 < len(format) {
			i++
			switch format[i] {
			case '%':
				buf.WriteByte('%')
			case 'Y':
				buf.WriteString(fmt.Sprintf("%04d", t.Year()))
			case 'y':
				buf.WriteString(fmt.Sprintf("%02d", t.Year()%100))
			case 'm':
				buf.WriteString(fmt.Sprintf("%02d", int(t.Month())))
			case 'B':
				buf.WriteString(t.Month().String())
			case 'b', 'h':
				buf.WriteString(t.Month().String()[:3])
			case 'd':
				buf.WriteString(fmt.Sprintf("%02d", t.Day()))
			case 'H':
				buf.WriteString(fmt.Sprintf("%02d", t.Hour()))
			case 'I':
				h := t.Hour() % 12
				if h == 0 {
					h = 12
				}
				buf.WriteString(fmt.Sprintf("%02d", h))
			case 'p':
				if t.Hour() < 12 {
					buf.WriteString("AM")
				} else {
					buf.WriteString("PM")
				}
			case 'M':
				buf.WriteString(fmt.Sprintf("%02d", t.Minute()))
			case 'S':
				buf.WriteString(fmt.Sprintf("%02d", t.Second()))
			case 'f':
				buf.WriteString(fmt.Sprintf("%06d", us))
			case 'z':
				_, off := t.Zone()
				sign := "+"
				if off < 0 {
					sign = "-"
					off = -off
				}
				buf.WriteString(fmt.Sprintf("%s%02d%02d", sign, off/3600, (off%3600)/60))
			case 'Z':
				name, _ := t.Zone()
				buf.WriteString(name)
			case 'j':
				buf.WriteString(fmt.Sprintf("%03d", t.YearDay()))
			case 'w':
				buf.WriteString(fmt.Sprintf("%d", int(t.Weekday())))
			case 'u':
				w := (int(t.Weekday())+6)%7 + 1
				buf.WriteString(fmt.Sprintf("%d", w))
			case 'a':
				buf.WriteString(t.Weekday().String()[:3])
			case 'A':
				buf.WriteString(t.Weekday().String())
			default:
				buf.WriteByte('%')
				buf.WriteByte(format[i])
			}
		} else {
			buf.WriteByte(format[i])
		}
	}
	return buf.String()
}

func (e *Engine) createDateTimeModule() val.Value {
	initDatetimeTypes()
	modVal := val.NewModuleVal("_datetime", "Accelerated datetime C-extension")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("MINYEAR"), val.Int64Val(1))
	mod.Dict.Set(val.InternString("MAXYEAR"), val.Int64Val(9999))
	mod.Dict.Set(val.InternString("UTC"), utcTimezoneVal)

	mod.Dict.Set(val.InternString("tzinfo"), val.TypeVal(tzinfoType))
	mod.Dict.Set(val.InternString("timezone"), val.TypeVal(timezoneType))
	mod.Dict.Set(val.InternString("timedelta"), val.TypeVal(timedeltaType))
	mod.Dict.Set(val.InternString("date"), val.TypeVal(dateType))
	mod.Dict.Set(val.InternString("time"), val.TypeVal(timeType))
	mod.Dict.Set(val.InternString("datetime"), val.TypeVal(datetimeType))

	return modVal
}
