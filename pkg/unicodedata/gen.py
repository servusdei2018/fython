#!/usr/bin/env python3
"""Regenerates the embedded Unicode name/decomposition tables.

    go generate ./pkg/unicodedata/

The tables come from the running interpreter's `unicodedata` module and cover
the blocks the standard library and its tests actually use (Basic Latin through
Latin Extended-B, IPA, Greek, Cyrillic, general punctuation through dingbats,
ligatures, emoji) plus the C0/C1 control-character aliases accepted by
``\\N{...}``. CJK ideographs and Hangul syllables are computed algorithmically
at run time.
"""
import gzip
import unicodedata

RANGES = [
    (0x20, 0x24F), (0x250, 0x2FF), (0x300, 0x36F), (0x370, 0x3FF), (0x400, 0x52F),
    (0x590, 0x5FF), (0x600, 0x6FF), (0x900, 0x97F), (0xE00, 0xE7F), (0x1E00, 0x1FFF),
    (0x2000, 0x27BF), (0x2900, 0x2BFF), (0x3000, 0x303F), (0x3040, 0x30FF),
    (0xFB00, 0xFB06), (0xFE00, 0xFE0F), (0xFEFF, 0xFEFF), (0xFFFC, 0xFFFD),
    (0x1F300, 0x1F64F), (0x1F680, 0x1F6FF), (0x1F900, 0x1F9FF),
]

# Name aliases (control characters and corrections) known to \N{...}.
ALIASES = {
    "NULL": 0x00, "START OF HEADING": 0x01, "START OF TEXT": 0x02, "END OF TEXT": 0x03,
    "END OF TRANSMISSION": 0x04, "ENQUIRY": 0x05, "ACKNOWLEDGE": 0x06, "ALERT": 0x07,
    "BEL": 0x07, "BACKSPACE": 0x08, "CHARACTER TABULATION": 0x09, "HORIZONTAL TABULATION": 0x09,
    "TAB": 0x09, "LINE FEED": 0x0A, "NEW LINE": 0x0A, "END OF LINE": 0x0A, "LF": 0x0A,
    "NL": 0x0A, "EOL": 0x0A, "LINE TABULATION": 0x0B, "VERTICAL TABULATION": 0x0B,
    "FORM FEED": 0x0C, "FF": 0x0C, "CARRIAGE RETURN": 0x0D, "CR": 0x0D, "SHIFT OUT": 0x0E,
    "SHIFT IN": 0x0F, "DATA LINK ESCAPE": 0x10, "DEVICE CONTROL ONE": 0x11,
    "DEVICE CONTROL TWO": 0x12, "DEVICE CONTROL THREE": 0x13, "DEVICE CONTROL FOUR": 0x14,
    "NEGATIVE ACKNOWLEDGE": 0x15, "SYNCHRONOUS IDLE": 0x16, "END OF TRANSMISSION BLOCK": 0x17,
    "CANCEL": 0x18, "END OF MEDIUM": 0x19, "SUBSTITUTE": 0x1A, "ESCAPE": 0x1B, "ESC": 0x1B,
    "INFORMATION SEPARATOR FOUR": 0x1C, "FILE SEPARATOR": 0x1C,
    "INFORMATION SEPARATOR THREE": 0x1D, "GROUP SEPARATOR": 0x1D,
    "INFORMATION SEPARATOR TWO": 0x1E, "RECORD SEPARATOR": 0x1E,
    "INFORMATION SEPARATOR ONE": 0x1F, "UNIT SEPARATOR": 0x1F, "DELETE": 0x7F, "DEL": 0x7F,
    "NEXT LINE": 0x85, "NEL": 0x85, "NO-BREAK SPACE": 0xA0, "NBSP": 0xA0,
    "ZWNJ": 0x200C, "ZWJ": 0x200D, "BOM": 0xFEFF, "BYTE ORDER MARK": 0xFEFF,
    "ZERO WIDTH NO-BREAK SPACE": 0xFEFF, "SOFT HYPHEN": 0xAD, "SHY": 0xAD,
}

names = []
for lo, hi in RANGES:
    for cp in range(lo, hi + 1):
        try:
            names.append((cp, unicodedata.name(chr(cp))))
        except ValueError:
            pass

with gzip.GzipFile("names.txt.gz", "wb", mtime=0) as f:
    text = "".join("%X\t%s\n" % (cp, n) for cp, n in names)
    text += "".join("@%X\t%s\n" % (cp, n) for n, cp in sorted(ALIASES.items(), key=lambda kv: (kv[1], kv[0])))
    f.write(text.encode("ascii"))

# Canonical decompositions (and compatibility for the same ranges) used by
# unicodedata.normalize.
dec = []
for lo, hi in [(0xC0, 0x24F), (0x370, 0x3FF), (0x400, 0x4FF), (0x1E00, 0x1FFF), (0xFB00, 0xFB06), (0x2000, 0x2122), (0xFF00, 0xFFEF)]:
    for cp in range(lo, hi + 1):
        d = unicodedata.decomposition(chr(cp))
        if d:
            dec.append((cp, d))
with gzip.GzipFile("decomp.txt.gz", "wb", mtime=0) as f:
    f.write("".join("%X\t%s\n" % (cp, d) for cp, d in dec).encode("ascii"))
print(len(names), "names,", len(ALIASES), "aliases,", len(dec), "decompositions")
