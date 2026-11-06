package sre

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"reflect"
	"testing"
)

type tcase struct {
	Pat   string `json:"pat"`
	Str   string `json:"str"`
	Flags int    `json:"flags"`
	Op    string `json:"op"` // search, match, fullmatch
	Pos   int    `json:"pos"`
	End   int    `json:"end"`
	Bytes bool   `json:"bytes"`
}

// result is the comparable outcome: error flag or (caps, lastindex).
type result struct {
	Err  bool  `json:"err"`
	Caps []int `json:"caps"`
	Last int   `json:"last"`
}

var cases = []tcase{
	{Pat: `(?<=a)b`, Str: "ab"},
	{Pat: `(?<!a)b`, Str: "ab cb"},
	{Pat: `(?<=ab|cd)x`, Str: "cdx"},
	{Pat: `(?<=a+)b`, Str: "aab"}, // error: not fixed width
	{Pat: `a(?=b)`, Str: "ac ab"},
	{Pat: `a(?!b)`, Str: "ab ac"},
	{Pat: `(a)\1`, Str: "xaa"},
	{Pat: `(?P<n>a)(?P=n)`, Str: "aa"},
	{Pat: `(a)|b`, Str: "b"},
	{Pat: `(a)|(b)`, Str: "b"},
	{Pat: `((a)(b))`, Str: "ab"},
	{Pat: `(a)(b)`, Str: "ab"},
	{Pat: `(?(1)b|c)`, Str: "c"},
	{Pat: `(a)?(?(1)b|c)`, Str: "ab"},
	{Pat: `(a)?(?(1)b|c)`, Str: "c"},
	{Pat: `(?P<x>a)?(?(x)b|c)`, Str: "ab"},
	{Pat: `(?>a+)a`, Str: "aaa"},
	{Pat: `(?>a|ab)c`, Str: "abc"},
	{Pat: `a*+a`, Str: "aaa"},
	{Pat: `a++b`, Str: "aaab"},
	{Pat: `a?+a`, Str: "a"},
	{Pat: `(?:ab)*+c`, Str: "ababc"},
	{Pat: `a*?b`, Str: "aaab"},
	{Pat: `a+?`, Str: "aaa"},
	{Pat: `a??b`, Str: "ab"},
	{Pat: `a{2,3}`, Str: "aaaa"},
	{Pat: `a{2,3}?`, Str: "aaaa"},
	{Pat: `a{,2}`, Str: "aaa"},
	{Pat: `a{2}`, Str: "aaa"},
	{Pat: `a{2,}`, Str: "aaaa"},
	{Pat: `a{`, Str: "a{"},
	{Pat: `a{}`, Str: "a{}"},
	{Pat: `a{1,x}`, Str: "a{1,x}"},
	{Pat: `(ab){2,3}`, Str: "ababab"},
	{Pat: `(ab){2,3}?`, Str: "ababab"},
	{Pat: `(a|b)*`, Str: "abba"},
	{Pat: `(a*)*`, Str: "aaa"},
	{Pat: `(a*)+`, Str: "aaa"},
	{Pat: `(a|ab)(c|bcd)(d*)`, Str: "abcd"},
	{Pat: `(?i)abc`, Str: "xABC"},
	{Pat: `(?i:a)b`, Str: "Ab AB"},
	{Pat: `a(?i:b)c`, Str: "aBc aBC"},
	{Pat: `(?i)a(?-i:b)`, Str: "AB Ab"},
	{Pat: `(?s).`, Str: "\n"},
	{Pat: `.`, Str: "\n"},
	{Pat: `(?m)^b$`, Str: "a\nb\nc"},
	{Pat: `^b$`, Str: "a\nb\nc"},
	{Pat: `a$`, Str: "a\n"},
	{Pat: `a\Z`, Str: "a\n"},
	{Pat: `a\z`, Str: "a"},
	{Pat: `\Aa`, Str: "a"},
	{Pat: `(?x) a b # comment
	  c`, Str: "abc"},
	{Pat: `a b`, Flags: 64, Str: "ab"},
	{Pat: `[a-c]+`, Str: "xabcx"},
	{Pat: `[^a-c]+`, Str: "abxyc"},
	{Pat: `[]a]+`, Str: "]a]"},
	{Pat: `[a\]]+`, Str: "a]"},
	{Pat: `[\d_]+`, Str: "a1_2b"},
	{Pat: `[\w-]+`, Str: "foo-bar baz"},
	{Pat: `[a-]+`, Str: "a-a"},
	{Pat: `[z-a]`, Str: "a"},
	{Pat: `\d+`, Str: "ab123"},
	{Pat: `\d+`, Str: "ab١٢٣"},
	{Pat: `(?a)\d+`, Str: "ab١٢٣1"},
	{Pat: `\w+`, Str: "héllo wörld"},
	{Pat: `(?a)\w+`, Str: "héllo"},
	{Pat: `\s+`, Str: "a \t\n b"},
	{Pat: `\bfoo\b`, Str: "a foo b"},
	{Pat: `\Boo`, Str: "foo"},
	{Pat: `\b`, Str: ""},
	{Pat: `x*`, Str: "aaa"},
	{Pat: `(?=a)*`, Str: "a"},
	{Pat: `*a`, Str: "a"},
	{Pat: `a**`, Str: "a"},
	{Pat: `(?:a*)*`, Str: "aa"},
	{Pat: `(`, Str: "a"},
	{Pat: `)`, Str: "a"},
	{Pat: `(?P<a>x)(?P<a>y)`, Str: "xy"},
	{Pat: `\1`, Str: "a"},
	{Pat: `(a)\2`, Str: "aa"},
	{Pat: `(a\1)`, Str: "aa"},
	{Pat: `\101`, Str: "A"},
	{Pat: `\0`, Str: "\x00"},
	{Pat: `\x41\u0042\U00000043`, Str: "ABC"},
	{Pat: `\q`, Str: "q"},
	{Pat: `\.`, Str: "."},
	{Pat: `é+`, Str: "xééé"},
	{Pat: `(?i)é`, Str: "É"},
	{Pat: `(?i)ß`, Str: "SS"},
	{Pat: `(?i)k`, Str: "\u212a"},
	{Pat: `(?ia)k`, Str: "\u212a"},
	{Pat: `(?i)(a)\1`, Str: "aA"},
	{Pat: `(a)(?i)\1`, Str: "aA"},
	{Pat: `(?i)[a-c]+`, Str: "ABC"},
	{Pat: `(?i)[^a]`, Str: "A"},
	{Pat: `.*foo`, Str: "xx foo yy foo zz"},
	{Pat: `.*?foo`, Str: "xx foo yy foo zz"},
	{Pat: `(?:a|b)+c`, Str: "ababc"},
	{Pat: `a|b|c`, Str: "xxc"},
	{Pat: `(?:)`, Str: "abc"},
	{Pat: `a(?#comment)b`, Str: "ab"},
	{Pat: `(?<=\d{3})x`, Str: "123x"},
	{Pat: `(?<!\d{2})x`, Str: "1x 12x"},
	{Pat: `(?<=(a))b`, Str: "ab"},
	{Pat: `(?=(a))a`, Str: "a"},
	{Pat: `(?!(a))b`, Str: "b"},
	{Pat: `(?<=^)a`, Str: "a"},
	{Pat: `(?<![a-z])\d+`, Str: "a12 34"},
	{Pat: `\b(\w+)\s+\1\b`, Str: "the the cat"},
	{Pat: `(?<=\w)-(?=\w)`, Str: "a-b - c"},
	{Pat: `(?<=[\w!"'&.,?])-{2,}(?=\w)`, Str: "foo--bar"},
	{Pat: `(?<!-)-{1}`, Str: "a--b"},
	{Pat: `(?x) ( \d+ ) \s* ( [a-z]+ )`, Str: "42   abc"},
	{Pat: `(?P<year>\d{4})-(?P<mon>\d{2})`, Str: "on 2024-05-06"},
	{Pat: `a.c`, Str: "abc", Op: "match"},
	{Pat: `a.c`, Str: "xabc", Op: "match"},
	{Pat: `a|ab`, Str: "ab", Op: "fullmatch"},
	{Pat: `a+`, Str: "aab", Op: "fullmatch"},
	{Pat: `(a+)(b*)`, Str: "aabb", Op: "fullmatch"},
	{Pat: `a`, Str: "xxa", Pos: 1, End: 3},
	{Pat: `^a`, Str: "xa", Pos: 1, End: 2},
	{Pat: `a$`, Str: "ab", Pos: 0, End: 1},
	{Pat: `(?<=x)a`, Str: "xa", Pos: 1, End: 2},
	{Pat: `\bb`, Str: "ab", Pos: 1, End: 2},
	{Pat: `é`, Str: "aé", Op: "search"},
	{Pat: `\xe9`, Str: "\u00e9", Bytes: true},
	{Pat: `\w+`, Str: "ab\u00e9", Bytes: true},
	{Pat: `(?i)A`, Str: "a", Bytes: true},
	{Pat: `\u0041`, Str: "A", Bytes: true},
	{Pat: `(?u)a`, Str: "a", Bytes: true},
	{Pat: `(?L)a`, Str: "a"},
	{Pat: `(?a)(?u)a`, Str: "a"},
	{Pat: `a(?i)b`, Str: "ab"},
	{Pat: `(?x)a b`, Str: "ab"},
	{Pat: `(?i-i:a)`, Str: "a"},
	{Pat: `(?-i:a)`, Str: "a"},
	{Pat: `(?i-s:a)`, Str: "A"},
	{Pat: `(?-a:a)`, Str: "a"},
	{Pat: `(?(1)a)`, Str: "a"},
	{Pat: `(a)(?(1)b|c|d)`, Str: "ab"},
	{Pat: `(?P<1a>x)`, Str: "x"},
	{Pat: `(?P<a b>x)`, Str: "x"},
	{Pat: `(?P=nope)`, Str: "x"},
	{Pat: `(?z)`, Str: "x"},
	{Pat: `[\d-z]`, Str: "-"},
	{Pat: `[a-\d]`, Str: "-"},
	{Pat: `(?:(a)|b)*`, Str: "ab"},
	{Pat: `(?:(a)|(b))+`, Str: "ab"},
	{Pat: `(a)*b`, Str: "aab"},
	{Pat: `(a*)*b`, Str: "aab"},
	{Pat: `(?:a*)+b`, Str: "b"},
	{Pat: `(\d+)(?:\.(\d+))?`, Str: "3.14"},
	{Pat: `(\d+)(?:\.(\d+))?`, Str: "3"},
	{Pat: `[\s\S]+`, Str: "a\nb"},
	{Pat: `[^\n]*`, Str: "ab\ncd"},
	{Pat: `\S+@\S+`, Str: "mail me@x.org now"},
	{Pat: `a{0}b`, Str: "ab"},
	{Pat: `(a){0}b`, Str: "ab"},
	{Pat: `(?:a{2}){2}`, Str: "aaaaa"},
	{Pat: `(?:a|b){3}`, Str: "abab"},
	{Pat: `x{1000}`, Str: "x"},
	{Pat: `\8`, Str: "8"},
	{Pat: `[\8]`, Str: "8"},
	{Pat: `[\x41-\x43]+`, Str: "ABCD"},
	{Pat: `[\101]`, Str: "A"},
}

const pyOracle = `
import re, sys, json
cases = json.load(sys.stdin)
out = []
for c in cases:
    pat, s = c["pat"], c["str"]
    if c["bytes"]:
        pat = pat.encode('latin-1'); s = s.encode('latin-1')
    try:
        r = re.compile(pat, c["flags"])
    except (re.error, ValueError):
        out.append({"err": True, "caps": None, "last": -1}); continue
    end = c["end"] if c["end"] else len(s)
    fn = {"search": r.search, "match": r.match, "fullmatch": r.fullmatch}[c["op"] or "search"]
    m = fn(s, c["pos"], end)
    if m is None:
        out.append({"err": False, "caps": None, "last": -1}); continue
    caps = []
    for i in range(r.groups + 1):
        caps += [m.start(i), m.end(i)]
    out.append({"err": False, "caps": caps, "last": m.lastindex if m.lastindex is not None else -1})
print(json.dumps(out))
`

func runGo(c tcase) result {
	var in []rune
	var pat []rune
	if c.Bytes {
		for _, b := range []byte(latin1(c.Str)) {
			in = append(in, rune(b))
		}
		for _, b := range []byte(latin1(c.Pat)) {
			pat = append(pat, rune(b))
		}
	} else {
		in = []rune(c.Str)
		pat = []rune(c.Pat)
	}
	re, err := Compile(pat, c.Flags, c.Bytes)
	if err != nil {
		return result{Err: true, Last: -1}
	}
	end := c.End
	if end == 0 {
		end = len(in)
	}
	mode := ModeSearch
	switch c.Op {
	case "match":
		mode = ModeMatch
	case "fullmatch":
		mode = ModeFullMatch
	}
	r := re.Exec(in, c.Pos, end, mode)
	if r == nil {
		return result{Last: -1}
	}
	return result{Caps: r.Caps, Last: r.LastIndex}
}

func latin1(s string) string {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		b = append(b, byte(r))
	}
	return string(b)
}

func TestDifferentialAgainstCPython(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	payload, _ := json.Marshal(cases)
	cmd := exec.Command(py, "-c", pyOracle)
	cmd.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("python3 oracle failed: %v: %s", err, stderr.String())
	}
	var want []result
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		got := runGo(c)
		if !reflect.DeepEqual(got, want[i]) {
			t.Errorf("case %d %q on %q flags=%d op=%q: got %+v want %+v", i, c.Pat, c.Str, c.Flags, c.Op, got, want[i])
		}
	}
}

func TestNoStackOverflow(t *testing.T) {
	re, err := Compile([]rune(`(?:a|b)*c`), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	in := make([]rune, 200000)
	for i := range in {
		in[i] = 'a'
	}
	in = append(in, 'c')
	r := re.Exec(in, 0, len(in), ModeMatch)
	if r == nil || r.Caps[1] != len(in) {
		t.Fatalf("unexpected result %v", r)
	}
	_ = fmt.Sprint
}
