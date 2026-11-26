package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestFormatValueSplitAndUnicodeRuneCache(t *testing.T) {
	runSnippet(t, `
# FormatValueCtx int64 & bool formatting
assert f"{0}" == "0"
assert f"{7}" == "7"
assert f"{-1}" == "-1"
assert f"{123456}" == "123456"
assert f"{-999999}" == "-999999"
assert f"{9223372036854775807}" == "9223372036854775807"
assert f"{-9223372036854775808}" == "-9223372036854775808"
assert f"{True}" == "True"
assert f"{False}" == "False"
assert f"{42}:{True}:{False}:{-7}" == "42:True:False:-7"

# str.split() ASCII & Unicode whitespace, maxsplit, single/multi-char sep, empty sep error
assert "".split() == []
assert "   \t\n\r  ".split() == []
assert "  a   b\tc\nd  ".split() == ["a", "b", "c", "d"]
assert "  a   b   c  ".split(None, 0) == ["a   b   c  "]
assert "  a   b   c  ".split(None, 1) == ["a", "b   c  "]
assert "a\u00a0b\u2003c\u3000d".split() == ["a", "b", "c", "d"]
assert "a\u00a0b\u2003c".split(None, 1) == ["a", "b\u2003c"]

assert "".split(",") == [""]
assert "a,b,c,d".split(",") == ["a", "b", "c", "d"]
assert "a,b,c,d".split(",", 0) == ["a,b,c,d"]
assert "a,b,c,d".split(",", 2) == ["a", "b", "c,d"]
assert "one--two--three".split("--") == ["one", "two", "three"]
assert "one--two--three".split("--", 1) == ["one", "two--three"]

raised = False
try:
    "abc".split("")
except ValueError as e:
    raised = "empty separator" in str(e)
assert raised

# Non-ASCII str indexing and slicing
u = "café 日本語 αβγ"
assert u[0] == "c"
assert u[3] == "é"
assert u[5] == "日"
assert u[-1] == "γ"
assert u[0:4] == "café"
assert u[5:8] == "日本語"
assert u[-3:] == "αβγ"
assert u[3:8:2] == "é日語"
assert u[::-1][0] == "γ"
a, b, c = "αβγ"
assert (a, b, c) == ("α", "β", "γ")
`)

	// Direct verification that PyString.RuneCache is populated and reused.
	sv := val.NewStringVal("café 日本語 αβγ")
	ps := sv.AsStringObj()
	if ps.RuneCache.Load() != nil {
		t.Fatalf("expected RuneCache to start nil")
	}
	r1 := val.StringValRunes(sv)
	if len(r1) != 12 {
		t.Fatalf("expected 12 runes, got %d", len(r1))
	}
	p1 := ps.RuneCache.Load()
	if p1 == nil {
		t.Fatalf("expected RuneCache to be populated after StringValRunes")
	}
	r2 := val.StringValRunes(sv)
	if len(r2) != 12 || &r1[0] != &r2[0] {
		t.Fatalf("expected StringValRunes to reuse cached backing slice")
	}
}
