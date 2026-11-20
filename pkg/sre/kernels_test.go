package sre

import (
	"bytes"
	"slices"
	"testing"
)

func TestKernelBoundaryParity(t *testing.T) {
	wordRe, err := Compile([]rune(`\w+`), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	var cs *charSet
	for i := range wordRe.prog {
		if wordRe.prog[i].op == opRepeat && wordRe.prog[i].sub == opSet {
			cs = wordRe.prog[i].set
			break
		}
	}
	if cs == nil {
		t.Fatal("expected opRepeat/opSet in \\w+")
	}

	for length := range 130 {
		allWordBytes := bytes.Repeat([]byte{'a'}, length)
		if got := scanCharSetBytes(allWordBytes, cs); got != length || got != scanCharSetBytesScalar(allWordBytes, cs) || got != scanCharSetBytesSWAR(allWordBytes, cs) {
			t.Fatalf("allWordBytes len=%d got=%d", length, got)
		}
		if got := scanByteEq(allWordBytes, 'a'); got != length || got != scanByteEqScalar(allWordBytes, 'a') || got != scanByteEqSWAR(allWordBytes, 'a') {
			t.Fatalf("scanByteEq len=%d got=%d", length, got)
		}

		allWordRunes := slices.Repeat([]rune{'a'}, length)
		if got := scanCharSetRunes(allWordRunes, cs); got != length || got != scanCharSetRunesScalar(allWordRunes, cs) || got != scanCharSetRunesSWAR(allWordRunes, cs) {
			t.Fatalf("allWordRunes len=%d got=%d", length, got)
		}
		if got := indexRuneFast(allWordRunes, 'z'); got != length || got != indexRuneScalar(allWordRunes, 'z') || got != indexRuneSWAR(allWordRunes, 'z') {
			t.Fatalf("indexRuneFast miss len=%d got=%d", length, got)
		}

		for _, stopByte := range []byte{' ', '-', 0x80, 0xc3, 0xff} {
			for pos := range length {
				if length > 40 && pos > 5 && pos < length-5 && pos != 7 && pos != 8 && pos != 15 && pos != 16 && pos != 31 && pos != 32 && pos != 33 && pos != 63 && pos != 64 {
					continue
				}
				b := make([]byte, length)
				copy(b, allWordBytes)
				b[pos] = stopByte
				wantB := scanCharSetBytesScalar(b, cs)
				if got := scanCharSetBytes(b, cs); got != wantB || scanCharSetBytesSWAR(b, cs) != wantB {
					t.Fatalf("scanCharSetBytes len=%d pos=%d byte=0x%02x got=%d want=%d", length, pos, stopByte, got, wantB)
				}
				wantEq := scanByteEqScalar(b, 'a')
				if got := scanByteEq(b, 'a'); got != wantEq || scanByteEqSWAR(b, 'a') != wantEq {
					t.Fatalf("scanByteEq len=%d pos=%d byte=0x%02x got=%d want=%d", length, pos, stopByte, got, wantEq)
				}
			}
		}

		for _, stopRune := range []rune{' ', 'é', '😀'} {
			for pos := range length {
				if length > 40 && pos > 5 && pos < length-5 && pos != 7 && pos != 8 && pos != 15 && pos != 16 && pos != 31 && pos != 32 && pos != 33 && pos != 63 && pos != 64 {
					continue
				}
				rs := make([]rune, length)
				copy(rs, allWordRunes)
				rs[pos] = stopRune
				wantR := scanCharSetRunesScalar(rs, cs)
				if got := scanCharSetRunes(rs, cs); got != wantR || scanCharSetRunesSWAR(rs, cs) != wantR {
					t.Fatalf("scanCharSetRunes len=%d pos=%d rune=%U got=%d want=%d", length, pos, stopRune, got, wantR)
				}
				wantIdx := indexRuneScalar(rs, stopRune)
				if got := indexRuneFast(rs, stopRune); got != wantIdx || indexRuneSWAR(rs, stopRune) != wantIdx {
					t.Fatalf("indexRuneFast len=%d pos=%d rune=%U got=%d want=%d", length, pos, stopRune, got, wantIdx)
				}
			}
		}
	}
}

func TestExecASCIIIntoParity(t *testing.T) {
	patterns := []struct {
		pat   string
		flags int
	}{
		{`\w+`, 0},
		{`\s+`, 0},
		{`[a-zA-Z0-9_]+`, 0},
		{`def\s+([a-zA-Z_]\w*)\(`, 0},
		{`(?i)hello\s+world`, 0},
		{`(?i)k`, 0},
		{`(?m)^[a-z]+$`, FlagMultiline},
		{`\bfoo\b`, 0},
		{`(a+)\1`, 0},
		{`[^0-9]+`, 0},
		{`.*?end`, 0},
		{`prefix_\d{2,4}`, 0},
		{`é+`, 0},
	}
	subjects := []string{
		"",
		"a",
		"foo",
		"hello   world!",
		"def my_func_1234567890_abcdefghijklmnopqrstuvwxyz():",
		"   \t\n  12345abcdeFGHIJ_0123456789",
		"abc\nhello\nxyz",
		"aaaa",
		"start 0123456789012345678901234567890123456789 end",
		"prefix_12345",
		"K",
	}
	modes := []Mode{ModeSearch, ModeMatch, ModeFullMatch}

	for _, ps := range patterns {
		re, err := Compile([]rune(ps.pat), ps.flags, false)
		if err != nil {
			t.Fatalf("Compile(%q): %v", ps.pat, err)
		}
		for _, sub := range subjects {
			rs := []rune(sub)
			bs := []byte(sub)
			for _, mode := range modes {
				for _, mustAdv := range []bool{false, true} {
					c1, l1, ok1 := re.ExecInto(rs, 0, len(rs), mode, mustAdv, nil)
					c2, l2, ok2 := re.ExecASCIIInto(bs, 0, len(bs), mode, mustAdv, nil)
					if ok1 != ok2 || l1 != l2 || !slices.Equal(c1, c2) {
						t.Fatalf("pat=%q flags=%d sub=%q mode=%v mustAdv=%v: ExecInto=(%v,%d,%v) ExecASCIIInto=(%v,%d,%v)",
							ps.pat, ps.flags, sub, mode, mustAdv, c1, l1, ok1, c2, l2, ok2)
					}
				}
			}
		}
	}
}
