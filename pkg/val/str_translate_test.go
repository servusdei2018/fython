package val

import (
	"testing"
)

func callStrMethod(t *testing.T, name string, args ...Value) (Value, *ExecContext) {
	t.Helper()
	fn, ok := StrType.Dict.Get(InternString(name))
	if !ok {
		t.Fatalf("str.%s not found", name)
	}
	ctx := &ExecContext{}
	res := fn.AsBuiltinFunc().Fn(ctx, fn, args, nil)
	return res, ctx
}

func TestStrTranslateAndMaketrans(t *testing.T) {
	// 2-arg maketrans + 1:1 ASCII translate (both short <=7 and long >7 outputs).
	tbl, ctx := callStrMethod(t, "maketrans", NewStringVal("abc"), NewStringVal("XYZ"))
	if ctx.HasException() {
		t.Fatalf("maketrans raised: %v", ctx.ActiveException)
	}
	got, ctx := callStrMethod(t, "translate", NewStringVal("cab"), tbl)
	if ctx.HasException() || got.AsString() != "ZXY" || !got.IsInlineStr() {
		t.Fatalf("short translate = %q (inline=%v, exc=%v), want ZXY", got.AsString(), got.IsInlineStr(), ctx.HasException())
	}
	got, ctx = callStrMethod(t, "translate", NewStringVal("abc_def_abc_12345"), tbl)
	if ctx.HasException() || got.AsString() != "XYZ_def_XYZ_12345" {
		t.Fatalf("long translate = %q (exc=%v)", got.AsString(), ctx.HasException())
	}

	// 3-arg maketrans with deletions.
	tblDel, ctx := callStrMethod(t, "maketrans", NewStringVal("ab"), NewStringVal("AB"), NewStringVal("0123456789"))
	if ctx.HasException() {
		t.Fatalf("3-arg maketrans raised: %v", ctx.ActiveException)
	}
	got, ctx = callStrMethod(t, "translate", NewStringVal("a1b2c3"), tblDel)
	if ctx.HasException() || got.AsString() != "ABc" {
		t.Fatalf("delete translate = %q, want ABc", got.AsString())
	}

	// 1-arg maketrans with multi-char string replacement & None deletion.
	d1 := NewDictVal(4).AsDict()
	d1.Set(NewStringVal("a"), NewStringVal("ALPHA"))
	d1.Set(NewStringVal("b"), NoneVal())
	d1.Set(NewStringVal("é"), NewStringVal("E"))
	tbl1, ctx := callStrMethod(t, "maketrans", Value{ptr: &d1.Header})
	if ctx.HasException() {
		t.Fatalf("1-arg maketrans raised: %v", ctx.ActiveException)
	}
	got, ctx = callStrMethod(t, "translate", NewStringVal("ab_café_🎉"), tbl1)
	if ctx.HasException() || got.AsString() != "ALPHA_cALPHAfE_🎉" {
		t.Fatalf("unicode translate = %q, want ALPHA_cALPHAfE_🎉", got.AsString())
	}

	// Bool and integral float keys (True == 1, False == 0, 65.0 == 65).
	dEq := NewDictVal(4).AsDict()
	dEq.Set(BoolVal(true), NewStringVal("ONE"))
	dEq.Set(Float64Val(65.0), NewStringVal("Z"))
	got, ctx = callStrMethod(t, "translate", NewStringVal("\x01A!"), Value{ptr: &dEq.Header})
	if ctx.HasException() || got.AsString() != "ONEZ!" {
		t.Fatalf("bool/float key translate = %q, want ONEZ!", got.AsString())
	}

	// Lazy ValueError: invalid codepoint only raises if actually hit.
	dBad := NewDictVal(4).AsDict()
	dBad.Set(Int64Val('A'), Int64Val(0x110000))
	dBad.Set(Int64Val('B'), Int64Val(-1))
	got, ctx = callStrMethod(t, "translate", NewStringVal("hello"), Value{ptr: &dBad.Header})
	if ctx.HasException() || got.AsString() != "hello" {
		t.Fatalf("unhit invalid codepoint should succeed, got %q exc=%v", got.AsString(), ctx.HasException())
	}
	_, ctx = callStrMethod(t, "translate", NewStringVal("A"), Value{ptr: &dBad.Header})
	if !ctx.HasException() || ctx.ActiveException.Type() != ValueErrorType {
		t.Fatalf("expected ValueError for 0x110000, got %v", ctx.ActiveException)
	}
	_, ctx = callStrMethod(t, "translate", NewStringVal("B"), Value{ptr: &dBad.Header})
	if !ctx.HasException() || ctx.ActiveException.Type() != ValueErrorType {
		t.Fatalf("expected ValueError for -1, got %v", ctx.ActiveException)
	}

	// Lazy TypeError: invalid value type only raises if actually hit.
	dType := NewDictVal(2).AsDict()
	dType.Set(Int64Val('X'), Float64Val(1.5))
	got, ctx = callStrMethod(t, "translate", NewStringVal("abc"), Value{ptr: &dType.Header})
	if ctx.HasException() || got.AsString() != "abc" {
		t.Fatalf("unhit invalid type should succeed, got %q exc=%v", got.AsString(), ctx.HasException())
	}
	_, ctx = callStrMethod(t, "translate", NewStringVal("X"), Value{ptr: &dType.Header})
	if !ctx.HasException() || ctx.ActiveException.Type() != TypeErrorType {
		t.Fatalf("expected TypeError for float value, got %v", ctx.ActiveException)
	}
}

func TestStrUnicodeIndexSliceAndCase(t *testing.T) {
	s := NewStringVal("Héllo 日本語 🎉🚀!")
	ctx := &ExecContext{}
	cases := []struct {
		idx  int64
		want string
	}{
		{0, "H"},
		{1, "é"},
		{6, "日"},
		{10, "🎉"},
		{-1, "!"},
		{-2, "🚀"},
		{-3, "🎉"},
	}
	for _, tc := range cases {
		got := strSqItem(ctx, s, Int64Val(tc.idx))
		if ctx.HasException() || got.AsString() != tc.want {
			t.Fatalf("s[%d] = %q (exc=%v), want %q", tc.idx, got.AsString(), ctx.HasException(), tc.want)
		}
	}

	// Positive step=1 and step=2 slices without materializing RuneCache.
	sl1 := NewSliceVal(Int64Val(1), Int64Val(9), NoneVal())
	got1 := strMpSubscript(ctx, s, sl1)
	if got1.AsString() != "éllo 日本語" {
		t.Fatalf("s[1:9] = %q, want %q", got1.AsString(), "éllo 日本語")
	}
	sl2 := NewSliceVal(Int64Val(0), Int64Val(12), Int64Val(2))
	got2 := strMpSubscript(ctx, s, sl2)
	if got2.AsString() != "Hlo日語🎉" {
		t.Fatalf("s[0:12:2] = %q, want %q", got2.AsString(), "Hlo日語🎉")
	}
	slNeg := NewSliceVal(NoneVal(), NoneVal(), Int64Val(-1))
	gotNeg := strMpSubscript(ctx, s, slNeg)
	if gotNeg.AsString() != "!🚀🎉 語本日 olléH" {
		t.Fatalf("s[::-1] = %q", gotNeg.AsString())
	}

	// StringEqualsASCII on inline and heap strings.
	if !StringEqualsASCII(SmallStringVal("newline"), "newline") || StringEqualsASCII(SmallStringVal("newline"), "padded") {
		t.Fatalf("StringEqualsASCII inline mismatch")
	}
	if !StringEqualsASCII(NewStringVal("strict_mode"), "strict_mode") || StringEqualsASCII(NewStringVal("strict_mode"), "strict") {
		t.Fatalf("StringEqualsASCII heap mismatch")
	}

	// lower/upper unchanged fast path on exact str vs str subclass.
	exact := NewStringVal("already_lower_12345")
	gotLow, ctx := callStrMethod(t, "lower", exact)
	if ctx.HasException() || gotLow.AsString() != "already_lower_12345" || gotLow.Type() != StrType {
		t.Fatalf("exact lower failed: %v", gotLow)
	}
	subType := MustNewType("MyStr", []*PyType{StrType}, nil)
	subInst := NewInstanceOfType(subType)
	subInst.AsInstance().SetSlot(0, exact)
	gotSub, ctx := callStrMethod(t, "lower", subInst)
	if ctx.HasException() || gotSub.Type() != StrType || gotSub.AsString() != "already_lower_12345" {
		t.Fatalf("subclass lower must return exact str, got type=%v val=%q", gotSub.Type(), gotSub.AsString())
	}
}
