package val_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func callCasefold(t *testing.T, s string) string {
	t.Helper()
	fn, ok := val.StrType.Dict.Get(val.InternString("casefold"))
	if !ok {
		t.Fatal("str.casefold not found")
	}
	ctx := &val.ExecContext{}
	res := fn.AsBuiltinFunc().Fn(ctx, fn, []val.Value{val.NewStringVal(s)}, nil)
	if ctx.HasException() {
		t.Fatalf("casefold raised: %v", ctx.ActiveException)
	}
	return res.AsString()
}

func TestCasefoldSharpSParity(t *testing.T) {
	if got := callCasefold(t, "ẞ"); got != "ss" {
		t.Fatalf(`"ẞ".casefold() = %q, want "ss"`, got)
	}
	if got := callCasefold(t, "ß"); got != "ss" {
		t.Fatalf(`"ß".casefold() = %q, want "ss"`, got)
	}
	if got := callCasefold(t, "ßẞ"); got != "ssss" {
		t.Fatalf(`"ßẞ".casefold() = %q, want "ssss"`, got)
	}
}
