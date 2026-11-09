// Package unicodedata is a pure-Go, dependency-free subset of CPython's
// unicodedata module: character names (for \N{...} escapes, lookup and name),
// general categories, decimal digits and a table-driven normalize.
//
// Names come from a compact embedded table covering the blocks listed in
// gen.py; CJK ideographs and Hangul syllables are derived algorithmically.
package unicodedata

//go:generate python3 gen.py

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

//go:embed names.txt.gz
var namesGz []byte

//go:embed decomp.txt.gz
var decompGz []byte

var (
	loadOnce sync.Once
	byName   map[string]rune
	byRune   map[rune]string
	decomp   map[rune]decomposition
	compose  map[[2]rune]rune
	catList  []string
)

type decomposition struct {
	compat bool
	runes  []rune
}

func gunzipLines(b []byte, fn func(line string)) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return
	}
	defer func() { _ = zr.Close() }()
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		fn(sc.Text())
	}
}

func load() {
	loadOnce.Do(func() {
		byName = make(map[string]rune, 7000)
		byRune = make(map[rune]string, 7000)
		gunzipLines(namesGz, func(line string) {
			alias := strings.HasPrefix(line, "@")
			if alias {
				line = line[1:]
			}
			hexCP, name, ok := strings.Cut(line, "\t")
			if !ok {
				return
			}
			cp, err := strconv.ParseUint(hexCP, 16, 32)
			if err != nil {
				return
			}
			byName[name] = rune(cp)
			if !alias {
				byRune[rune(cp)] = name
			}
		})
		decomp = make(map[rune]decomposition, 1200)
		compose = make(map[[2]rune]rune, 1000)
		gunzipLines(decompGz, func(line string) {
			hexCP, rest, ok := strings.Cut(line, "\t")
			if !ok {
				return
			}
			cp, err := strconv.ParseUint(hexCP, 16, 32)
			if err != nil {
				return
			}
			var d decomposition
			for f := range strings.FieldsSeq(rest) {
				if strings.HasPrefix(f, "<") {
					d.compat = true
					continue
				}
				r, err := strconv.ParseUint(f, 16, 32)
				if err != nil {
					return
				}
				d.runes = append(d.runes, rune(r))
			}
			decomp[rune(cp)] = d
			if !d.compat && len(d.runes) == 2 {
				compose[[2]rune{d.runes[0], d.runes[1]}] = rune(cp)
			}
		})
		for k := range unicode.Categories {
			if len(k) == 2 && k != "LC" && k != "Cn" {
				catList = append(catList, k)
			}
		}
		sort.Strings(catList)
	})
}

// Hangul syllable constants (Unicode §3.12).
const (
	sBase  = 0xAC00
	lCount = 19
	vCount = 21
	tCount = 28
	nCount = vCount * tCount
	sCount = lCount * nCount
)

var (
	jamoL = []string{"G", "GG", "N", "D", "DD", "R", "M", "B", "BB", "S", "SS", "", "J", "JJ", "C", "K", "T", "P", "H"}
	jamoV = []string{"A", "AE", "YA", "YAE", "EO", "E", "YEO", "YE", "O", "WA", "WAE", "OE", "YO", "U", "WEO", "WE", "WI", "YU", "EU", "YI", "I"}
	jamoT = []string{"", "G", "GG", "GS", "N", "NJ", "NH", "D", "L", "LG", "LM", "LB", "LS", "LT", "LP", "LH", "M", "B", "BS", "S", "SS", "NG", "J", "C", "K", "T", "P", "H"}
)

const (
	cjkPrefix    = "CJK UNIFIED IDEOGRAPH-"
	hangulPrefix = "HANGUL SYLLABLE "
)

func isCJK(r rune) bool {
	return (r >= 0x3400 && r <= 0x4DBF) || (r >= 0x4E00 && r <= 0x9FFF) ||
		(r >= 0x20000 && r <= 0x2A6DF) || (r >= 0x2A700 && r <= 0x2EBEF) || (r >= 0x30000 && r <= 0x323AF)
}

// Name returns the official Unicode name of r. Aliases are not returned.
func Name(r rune) (string, bool) {
	load()
	if n, ok := byRune[r]; ok {
		return n, true
	}
	if isCJK(r) {
		return cjkPrefix + strings.ToUpper(strconv.FormatInt(int64(r), 16)), true
	}
	if r >= sBase && r < sBase+sCount {
		s := int(r - sBase)
		return hangulPrefix + jamoL[s/nCount] + jamoV[(s%nCount)/tCount] + jamoT[s%tCount], true
	}
	return "", false
}

// Lookup resolves a character name or alias (case-insensitively), as accepted
// by \N{...} and unicodedata.lookup.
func Lookup(name string) (rune, bool) {
	load()
	name = strings.ToUpper(name)
	if r, ok := byName[name]; ok {
		return r, true
	}
	if rest, ok := strings.CutPrefix(name, cjkPrefix); ok {
		if v, err := strconv.ParseUint(rest, 16, 32); err == nil && len(rest) >= 4 && isCJK(rune(v)) {
			return rune(v), true
		}
	}
	if rest, ok := strings.CutPrefix(name, hangulPrefix); ok {
		for s := range sCount {
			if jamoL[s/nCount]+jamoV[(s%nCount)/tCount]+jamoT[s%tCount] == rest {
				return rune(sBase + s), true
			}
		}
	}
	return 0, false
}

// Category returns the two-letter general category of r ("Lu", "Nd", ...).
func Category(r rune) string {
	load()
	for _, c := range catList {
		if unicode.Is(unicode.Categories[c], r) {
			return c
		}
	}
	return "Cn"
}

// Decimal returns the decimal digit value of r when it is in category Nd.
func Decimal(r rune) (int, bool) {
	if !unicode.Is(unicode.Nd, r) {
		return 0, false
	}
	for _, rg := range unicode.Nd.R16 {
		if r >= rune(rg.Lo) && r <= rune(rg.Hi) {
			return int(r-rune(rg.Lo)) % 10, true
		}
	}
	for _, rg := range unicode.Nd.R32 {
		if r >= rune(rg.Lo) && r <= rune(rg.Hi) {
			return int(r-rune(rg.Lo)) % 10, true
		}
	}
	return 0, false
}

func decompose(dst []rune, r rune, compat bool) []rune {
	if r >= sBase && r < sBase+sCount {
		s := int(r - sBase)
		dst = append(dst, rune(0x1100+s/nCount), rune(0x1161+(s%nCount)/tCount))
		if t := s % tCount; t != 0 {
			dst = append(dst, rune(0x11A7+t))
		}
		return dst
	}
	if d, ok := decomp[r]; ok && (compat || !d.compat) {
		for _, x := range d.runes {
			dst = decompose(dst, x, compat)
		}
		return dst
	}
	return append(dst, r)
}

// Normalize implements NFC, NFD, NFKC and NFKD over the embedded
// decomposition tables (Latin, Greek, Cyrillic, ligatures, compatibility
// forms and Hangul). ok is false for an unknown form.
func Normalize(form, s string) (string, bool) {
	load()
	var compat, comp bool
	switch form {
	case "NFC":
		comp = true
	case "NFD":
	case "NFKC":
		compat, comp = true, true
	case "NFKD":
		compat = true
	default:
		return "", false
	}
	var rs []rune
	for _, r := range s {
		rs = decompose(rs, r, compat)
	}
	if !comp || len(rs) == 0 {
		return string(rs), true
	}
	out := rs[:1:1]
	out = append([]rune(nil), out...)
	starter := -1
	if !unicode.Is(unicode.Mn, out[0]) {
		starter = 0
	}
	for _, r := range rs[1:] {
		if starter >= 0 {
			last := out[len(out)-1]
			if starter == len(out)-1 || unicode.Is(unicode.Mn, last) {
				if c, ok := composePair(out[starter], r); ok {
					out[starter] = c
					continue
				}
			}
		}
		out = append(out, r)
		if !unicode.Is(unicode.Mn, r) {
			starter = len(out) - 1
		}
	}
	return string(out), true
}

func composePair(a, b rune) (rune, bool) {
	// Hangul LV / LVT composition.
	if a >= 0x1100 && a < 0x1100+lCount && b >= 0x1161 && b < 0x1161+vCount {
		return rune(sBase + ((int(a-0x1100)*vCount)+int(b-0x1161))*tCount), true
	}
	if a >= sBase && a < sBase+sCount && (int(a-sBase))%tCount == 0 && b > 0x11A7 && b < 0x11A7+tCount {
		return a + (b - 0x11A7), true
	}
	c, ok := compose[[2]rune{a, b}]
	return c, ok
}
