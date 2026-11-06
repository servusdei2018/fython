package unicodedata

import "testing"

func TestLookupName(t *testing.T) {
	cases := map[string]rune{
		"LATIN SMALL LETTER A": 'a', "snowman": 0x2603, "GREEK SMALL LETTER ALPHA": 0x3B1,
		"EM DASH": 0x2014, "LINE FEED": '\n', "NULL": 0, "BOX DRAWINGS LIGHT HORIZONTAL": 0x2500,
		"CJK UNIFIED IDEOGRAPH-4E2D": 0x4E2D, "HANGUL SYLLABLE GAG": 0xAC01,
		"GRINNING FACE": 0x1F600, "EURO SIGN": 0x20AC,
	}
	for n, want := range cases {
		got, ok := Lookup(n)
		if !ok || got != want {
			t.Errorf("Lookup(%q) = %U,%v want %U", n, got, ok, want)
		}
	}
	if _, ok := Lookup("NO SUCH NAME"); ok {
		t.Error("bogus name resolved")
	}
	for r, want := range map[rune]string{'a': "LATIN SMALL LETTER A", 0xAC01: "HANGUL SYLLABLE GAG", 0x4E2D: "CJK UNIFIED IDEOGRAPH-4E2D"} {
		if n, ok := Name(r); !ok || n != want {
			t.Errorf("Name(%U) = %q,%v", r, n, ok)
		}
	}
	if _, ok := Name('\n'); ok {
		t.Error("control characters have no name")
	}
}

func TestCategoryNormalize(t *testing.T) {
	if Category('A') != "Lu" || Category('1') != "Nd" || Category(0x378) != "Cn" {
		t.Error("category")
	}
	if d, ok := Decimal(0x0663); !ok || d != 3 {
		t.Error("decimal", d)
	}
	for _, c := range []struct{ f, in, want string }{
		{"NFD", "é", "e\u0301"}, {"NFC", "e\u0301", "é"}, {"NFKC", "ﬁ", "fi"},
		{"NFD", "한", "\u1112\u1161\u11AB"}, {"NFC", "\u1112\u1161\u11AB", "한"},
	} {
		got, ok := Normalize(c.f, c.in)
		if !ok || got != c.want {
			t.Errorf("Normalize(%s,%q)=%q want %q", c.f, c.in, got, c.want)
		}
	}
}
