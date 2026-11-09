package vm

import (
	"strings"
	"unicode"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

const (
	tokENDMARKER = 0
	tokNAME      = 1
	tokNUMBER    = 2
	tokSTRING    = 3
	tokNEWLINE   = 4
	tokINDENT    = 5
	tokDEDENT    = 6
	tokOP        = 55
	tokCOMMENT   = 65
	tokNL        = 66
	tokERROR     = 67
	tokENCODING  = 68
)

var TokenizerIterType *val.PyType

type pyTokenizerIter struct {
	val.ObjectHeader
	tokens []val.Value
	idx    int
}

func asTokenizerIter(v val.Value) *pyTokenizerIter {
	return (*pyTokenizerIter)(unsafe.Pointer(v.HeaderPtr()))
}

func initTokenizeTypes() {
	if TokenizerIterType != nil {
		return
	}
	TokenizerIterType = val.MustNewType("TokenizerIter", []*val.PyType{val.ObjectType}, nil)
	TokenizerIterType.Dict.Set(val.InternString("__module__"), val.InternString("_tokenize"))

	TokenizerIterType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic(val.NewTypeError("TokenizerIter() missing 1 required positional argument: 'source'"))
		}
		source := args[0]
		extraTokens := true
		for i, kn := range kwnames {
			if kn.AsString() == "extra_tokens" {
				valArg := args[len(args)-len(kwnames)+i]
				extraTokens = valArg.IsTruthy()
			}
		}

		lines := readAllSourceLines(ctx, source)
		toks := tokenizeSourceLines(lines, extraTokens)

		it := &pyTokenizerIter{
			ObjectHeader: val.ObjectHeader{Kind: val.KindNative, Type: TokenizerIterType},
			tokens:       toks,
			idx:          0,
		}
		return val.ObjectVal(&it.ObjectHeader)
	}

	TokenizerIterType.Dict.Set(val.InternString("__iter__"), val.NewBuiltinFuncVal("TokenizerIter.__iter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic(val.NewTypeError("descriptor '__iter__' requires an argument"))
		}
		return args[0]
	}))

	TokenizerIterType.Dict.Set(val.InternString("__next__"), val.NewBuiltinFuncVal("TokenizerIter.__next__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic(val.NewTypeError("descriptor '__next__' requires an argument"))
		}
		it := asTokenizerIter(args[0])
		if it.idx >= len(it.tokens) {
			if ctx != nil {
				ctx.Raise(val.NewStopIteration(val.NoneVal()))
			}
			panic(val.NewStopIteration(val.NoneVal()))
		}
		tok := it.tokens[it.idx]
		it.idx++
		return tok
	}))

	TokenizerIterType.InvalidateVersion()
}

func (e *Engine) createTokenizeModule() val.Value {
	initTokenizeTypes()
	modVal := val.NewModuleVal("_tokenize", "")
	mod := modVal.AsModule()
	mod.Dict.Set(val.InternString("TokenizerIter"), val.TypeVal(TokenizerIterType))
	return modVal
}

func readAllSourceLines(ctx *val.ExecContext, source val.Value) []string {
	var lines []string
	if source.IsAnyString() {
		raw := source.AsString()
		return strings.SplitAfter(raw, "\n")
	}

readLoop:
	for {
		res, stop := safeCallReadline(ctx, source)
		if stop || res.IsNone() {
			break
		}
		var line string
		switch {
		case res.IsBytes():
			line = string(res.AsBytes().Data)
		case res.IsAnyString():
			line = res.AsString()
		default:
			break readLoop
		}
		if len(line) == 0 {
			break
		}
		lines = append(lines, line)
	}
	return lines
}

func safeCallReadline(ctx *val.ExecContext, callable val.Value) (res val.Value, stop bool) {
	defer func() {
		if r := recover(); r != nil {
			if exc, ok := r.(val.Value); ok && val.IsInstance(exc, val.StopIterationType) {
				stop = true
				return
			}
			panic(r)
		}
	}()
	res = ctx.Call(callable, nil, nil)
	if ctx != nil && ctx.HasException() && val.IsInstance(ctx.ActiveException, val.StopIterationType) {
		ctx.ClearException()
		return val.NoneVal(), true
	}
	return res, false
}

func tokenizeSourceLines(lines []string, extraTokens bool) []val.Value {
	var tokens []val.Value
	emit := func(tokType int64, s string, srow, scol, erow, ecol int, line string) {
		tok := val.NewTupleVal(
			val.Int64Val(tokType),
			val.InternString(s),
			val.NewTupleVal(val.Int64Val(int64(srow)), val.Int64Val(int64(scol))),
			val.NewTupleVal(val.Int64Val(int64(erow)), val.Int64Val(int64(ecol))),
			val.NewStringVal(line),
		)
		tokens = append(tokens, tok)
	}

	indents := []int{0}
	parenDepth := 0
	row := 0

	inTriple := false
	tripleQuote := ""
	tripleStartRow := 0
	tripleStartCol := 0
	var tripleBuilder strings.Builder

	for rowIdx := range lines {
		row = rowIdx + 1
		line := lines[rowIdx]

		if inTriple {
			closeIdx := findClosingTripleQuote(line, tripleQuote)
			if closeIdx >= 0 {
				tripleBuilder.WriteString(line[:closeIdx+len(tripleQuote)])
				endCol := closeIdx + len(tripleQuote)
				emit(tokSTRING, tripleBuilder.String(), tripleStartRow, tripleStartCol, row, endCol, line)
				inTriple = false
				tripleQuote = ""
				tripleBuilder.Reset()
				col := endCol
				scanLineRest(line, &col, row, parenDepth, &indents, emit, extraTokens, &inTriple, &tripleQuote, &tripleStartRow, &tripleStartCol, &tripleBuilder)
				continue
			} else {
				tripleBuilder.WriteString(line)
				continue
			}
		}

		// Calculate leading whitespace indentation
		col := 0
		for col < len(line) && (line[col] == ' ' || line[col] == '\t') {
			if line[col] == ' ' {
				col++
			} else {
				col = (col/8 + 1) * 8
			}
		}

		trimmed := strings.TrimRight(line, "\r\n")
		rest := strings.TrimSpace(trimmed)
		isBlankOrComment := len(rest) == 0 || rest[0] == '#'

		if parenDepth == 0 {
			if !isBlankOrComment {
				curIndent := indents[len(indents)-1]
				if col > curIndent {
					indents = append(indents, col)
					emit(tokINDENT, line[:col], row, 0, row, col, line)
				} else if col < curIndent {
					for len(indents) > 1 && col < indents[len(indents)-1] {
						indents = indents[:len(indents)-1]
						emit(tokDEDENT, "", row, col, row, col, line)
					}
				}
			}
		}

		if isBlankOrComment {
			if len(rest) > 0 && rest[0] == '#' {
				commentStart := strings.Index(line, "#")
				commentEnd := len(trimmed)
				if extraTokens {
					emit(tokCOMMENT, line[commentStart:commentEnd], row, commentStart, row, commentEnd, line)
				}
			}
			if extraTokens && len(line) > len(trimmed) {
				emit(tokNL, line[len(trimmed):], row, len(trimmed), row, len(line), line)
			}
			continue
		}

		scanLineRest(line, &col, row, parenDepth, &indents, emit, extraTokens, &inTriple, &tripleQuote, &tripleStartRow, &tripleStartCol, &tripleBuilder)
	}

	// Emit trailing dedents and endmarker at EOF
	finalRow := row + 1
	if len(lines) == 0 {
		finalRow = 1
	}
	for len(indents) > 1 {
		indents = indents[:len(indents)-1]
		emit(tokDEDENT, "", finalRow, 0, finalRow, 0, "")
	}
	emit(tokENDMARKER, "", finalRow, 0, finalRow, 0, "")

	return tokens
}

func findClosingTripleQuote(line, quote string) int {
	for i := 0; i+len(quote) <= len(line); i++ {
		if strings.HasPrefix(line[i:], quote) {
			backslashes := 0
			for j := i - 1; j >= 0 && line[j] == '\\'; j-- {
				backslashes++
			}
			if backslashes%2 == 0 {
				return i
			}
		}
	}
	return -1
}

func scanLineRest(
	line string,
	col *int,
	row int,
	parenDepth int,
	indents *[]int,
	emit func(tokType int64, s string, srow, scol, erow, ecol int, line string),
	extraTokens bool,
	inTriple *bool,
	tripleQuote *string,
	tripleStartRow *int,
	tripleStartCol *int,
	tripleBuilder *strings.Builder,
) {
	n := len(line)
	for *col < n {
		c := line[*col]

		// Whitespace
		if c == ' ' || c == '\t' {
			*col++
			continue
		}

		// Comment
		if c == '#' {
			trimmed := strings.TrimRight(line[*col:], "\r\n")
			startCol := *col
			endCol := startCol + len(trimmed)
			if extraTokens {
				emit(tokCOMMENT, trimmed, row, startCol, row, endCol, line)
			}
			*col = endCol
			if *col < n {
				newlineStr := line[*col:]
				if parenDepth > 0 {
					if extraTokens {
						emit(tokNL, newlineStr, row, *col, row, n, line)
					}
				} else {
					emit(tokNEWLINE, newlineStr, row, *col, row, n, line)
				}
				*col = n
			}
			return
		}

		// Newline
		if c == '\n' || c == '\r' {
			newlineStr := line[*col:]
			if parenDepth > 0 {
				if extraTokens {
					emit(tokNL, newlineStr, row, *col, row, n, line)
				}
			} else {
				emit(tokNEWLINE, newlineStr, row, *col, row, n, line)
			}
			*col = n
			return
		}

		// Line continuation \
		if c == '\\' && *col+1 < n && (line[*col+1] == '\n' || line[*col+1] == '\r') {
			*col = n
			return
		}

		// Check string prefix or identifier
		if isIdentStart(c) {
			// Check if it's a string prefix: [rRuUbBfFtT]* followed by ' or "
			strPrefixLen := matchStringPrefix(line[*col:])
			if strPrefixLen >= 0 {
				startCol := *col
				quoteStart := *col + strPrefixLen
				q := line[quoteStart]
				isTriple := quoteStart+3 <= n && line[quoteStart:quoteStart+3] == string([]byte{q, q, q})
				quoteDelim := string(q)
				delimLen := 1
				if isTriple {
					quoteDelim = string([]byte{q, q, q})
					delimLen = 3
				}

				if isTriple {
					closeIdx := findClosingTripleQuote(line[quoteStart+delimLen:], quoteDelim)
					if closeIdx >= 0 {
						endCol := quoteStart + delimLen + closeIdx + delimLen
						emit(tokSTRING, line[startCol:endCol], row, startCol, row, endCol, line)
						*col = endCol
						continue
					} else {
						*inTriple = true
						*tripleQuote = quoteDelim
						*tripleStartRow = row
						*tripleStartCol = startCol
						tripleBuilder.Reset()
						tripleBuilder.WriteString(line[startCol:])
						*col = n
						return
					}
				} else {
					// Single-line string
					closeCol := findClosingSingleQuote(line, quoteStart+delimLen, q)
					emit(tokSTRING, line[startCol:closeCol], row, startCol, row, closeCol, line)
					*col = closeCol
					continue
				}
			}

			// Regular identifier
			startCol := *col
			for *col < n && isIdentPart(line[*col]) {
				*col++
			}
			emit(tokNAME, line[startCol:*col], row, startCol, row, *col, line)
			continue
		}

		// Strings without prefix
		if c == '\'' || c == '"' {
			startCol := *col
			isTriple := *col+3 <= n && line[*col:*col+3] == string([]byte{c, c, c})
			quoteDelim := string(c)
			delimLen := 1
			if isTriple {
				quoteDelim = string([]byte{c, c, c})
				delimLen = 3
			}

			if isTriple {
				closeIdx := findClosingTripleQuote(line[*col+delimLen:], quoteDelim)
				if closeIdx >= 0 {
					endCol := *col + delimLen + closeIdx + delimLen
					emit(tokSTRING, line[startCol:endCol], row, startCol, row, endCol, line)
					*col = endCol
					continue
				} else {
					*inTriple = true
					*tripleQuote = quoteDelim
					*tripleStartRow = row
					*tripleStartCol = startCol
					tripleBuilder.Reset()
					tripleBuilder.WriteString(line[startCol:])
					*col = n
					return
				}
			} else {
				closeCol := findClosingSingleQuote(line, *col+delimLen, c)
				emit(tokSTRING, line[startCol:closeCol], row, startCol, row, closeCol, line)
				*col = closeCol
				continue
			}
		}

		// Numbers
		if unicode.IsDigit(rune(c)) || (c == '.' && *col+1 < n && unicode.IsDigit(rune(line[*col+1]))) {
			startCol := *col
			switch {
			case c == '0' && *col+1 < n && (line[*col+1] == 'x' || line[*col+1] == 'X'):
				*col += 2
				for *col < n && (isHexDigit(line[*col]) || line[*col] == '_') {
					*col++
				}
			case c == '0' && *col+1 < n && (line[*col+1] == 'b' || line[*col+1] == 'B'):
				*col += 2
				for *col < n && (line[*col] == '0' || line[*col] == '1' || line[*col] == '_') {
					*col++
				}
			case c == '0' && *col+1 < n && (line[*col+1] == 'o' || line[*col+1] == 'O'):
				*col += 2
				for *col < n && ((line[*col] >= '0' && line[*col] <= '7') || line[*col] == '_') {
					*col++
				}
			default:
				for *col < n && (unicode.IsDigit(rune(line[*col])) || line[*col] == '_') {
					*col++
				}
				if *col < n && line[*col] == '.' {
					*col++
					for *col < n && (unicode.IsDigit(rune(line[*col])) || line[*col] == '_') {
						*col++
					}
				}
				if *col < n && (line[*col] == 'e' || line[*col] == 'E') {
					*col++
					if *col < n && (line[*col] == '+' || line[*col] == '-') {
						*col++
					}
					for *col < n && (unicode.IsDigit(rune(line[*col])) || line[*col] == '_') {
						*col++
					}
				}
				if *col < n && (line[*col] == 'j' || line[*col] == 'J') {
					*col++
				}
			}
			emit(tokNUMBER, line[startCol:*col], row, startCol, row, *col, line)
			continue
		}

		// Operators & Delimiters
		opLen := matchOperator(line[*col:])
		if opLen > 0 {
			opStr := line[*col : *col+opLen]
			startCol := *col
			endCol := *col + opLen
			emit(tokOP, opStr, row, startCol, row, endCol, line)
			for i := range opLen {
				ch := line[*col+i]
				switch ch {
				case '(', '[', '{':
					parenDepth++
				case ')', ']', '}':
					if parenDepth > 0 {
						parenDepth--
					}
				}
			}
			*col = endCol
			continue
		}

		// Single unhandled character
		startCol := *col
		*col++
		emit(tokERROR, string(c), row, startCol, row, *col, line)
	}
}

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func matchStringPrefix(s string) int {
	prefixes := []string{
		"rf", "fr", "rb", "br",
		"RF", "FR", "RB", "BR",
		"Rf", "rF", "Fr", "fR",
		"Rb", "rB", "Br", "bR",
		"r", "u", "b", "f", "t",
		"R", "U", "B", "F", "T",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			rest := s[len(p):]
			if len(rest) > 0 && (rest[0] == '\'' || rest[0] == '"') {
				return len(p)
			}
		}
	}
	return -1
}

func findClosingSingleQuote(line string, from int, q byte) int {
	n := len(line)
	for i := from; i < n; i++ {
		if line[i] == q {
			backslashes := 0
			for j := i - 1; j >= from && line[j] == '\\'; j-- {
				backslashes++
			}
			if backslashes%2 == 0 {
				return i + 1
			}
		}
		if line[i] == '\n' || line[i] == '\r' {
			return i
		}
	}
	return n
}

func matchOperator(s string) int {
	if len(s) >= 3 {
		switch s[:3] {
		case "**=", "//=", "<<=", ">>=", "...":
			return 3
		}
	}
	if len(s) >= 2 {
		switch s[:2] {
		case "==", "!=", "<=", ">=", "+=", "-=", "*=", "/=", "%=",
			"&=", "|=", "^=", "**", "//", "<<", ">>", "->", ":=", "@=":
			return 2
		}
	}
	if len(s) >= 1 {
		switch s[0] {
		case '(', ')', '[', ']', '{', '}', ':', ',', ';', '.', '+',
			'-', '*', '/', '%', '&', '|', '^', '~', '<', '>', '=', '@':
			return 1
		}
	}
	return 0
}
