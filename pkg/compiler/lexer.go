package compiler

import (
	"fmt"
	"strings"
)

type fstringState struct {
	quote      byte
	isTriple   bool
	raw        bool
	isTemplate bool
	braceDepth int
	parenDepth int
	// inSpec is set once a top-level ':' opens a format spec; there '#' is
	// literal spec text (e.g. "{n:#x}"), not a comment.
	inSpec bool
}

// Lexer implements a zero-copy Python tokenizer with indentation state machine.
type Lexer struct {
	src             []byte
	pos             int
	line            uint32
	col             uint32
	indentStack     []int
	parenDepth      int
	pendingToks     []Token
	atBOL           bool // at Beginning of Line
	hasTokensOnLine bool
	err             error
	fstringStack    []fstringState
}

// NewLexer creates a new Lexer for the source buffer.
func NewLexer(src []byte) *Lexer {
	return &Lexer{
		src:          src,
		pos:          0,
		line:         1,
		col:          1,
		indentStack:  []int{0},
		parenDepth:   0,
		pendingToks:  make([]Token, 0, 8),
		atBOL:        true,
		fstringStack: make([]fstringState, 0, 4),
	}
}

// Reset resets the Lexer state to tokenize a new source buffer without reallocation.
func (l *Lexer) Reset(src []byte) {
	l.src = src
	l.pos = 0
	l.line = 1
	l.col = 1
	if cap(l.indentStack) > 0 {
		l.indentStack = l.indentStack[:1]
		l.indentStack[0] = 0
	} else {
		l.indentStack = []int{0}
	}
	l.parenDepth = 0
	l.pendingToks = l.pendingToks[:0]
	l.atBOL = true
	l.hasTokensOnLine = false
	l.err = nil
	l.fstringStack = l.fstringStack[:0]
}

// NextToken returns the next zero-copy Token.
func (l *Lexer) NextToken() Token {
	if len(l.pendingToks) > 0 {
		tok := l.pendingToks[0]
		l.pendingToks = l.pendingToks[1:]
		return tok
	}

	for l.pos < len(l.src) {
		if len(l.fstringStack) > 0 {
			top := &l.fstringStack[len(l.fstringStack)-1]
			if top.braceDepth == 0 {
				l.hasTokensOnLine = true
				return l.readFStringMiddle(top)
			}
		}

		if l.atBOL && l.parenDepth == 0 {
			l.handleBOL()
			if len(l.pendingToks) > 0 {
				tok := l.pendingToks[0]
				l.pendingToks = l.pendingToks[1:]
				return tok
			}
			if l.pos >= len(l.src) {
				break
			}
		}

		ch := l.src[l.pos]

		// Whitespace (horizontal)
		if ch == ' ' || ch == '\t' {
			l.pos++
			l.col++
			continue
		}

		// '#' inside a format spec is spec text
		if ch == '#' && len(l.fstringStack) > 0 && l.fstringStack[len(l.fstringStack)-1].inSpec {
			l.hasTokensOnLine = true
			start := uint32(l.pos)
			line, col := l.line, l.col
			l.pos++
			l.col++
			return Token{Type: TOKEN_FSTRING_MIDDLE, Start: start, End: uint32(l.pos), Line: line, Col: col}
		}

		// Comment
		if ch == '#' {
			for l.pos < len(l.src) && l.src[l.pos] != '\n' && l.src[l.pos] != '\r' {
				l.pos++
			}
			continue
		}

		// Explicit line continuation
		if ch == '\\' && l.pos+1 < len(l.src) && (l.src[l.pos+1] == '\n' || l.src[l.pos+1] == '\r') {
			l.pos++
			if l.pos < len(l.src) && l.src[l.pos] == '\r' {
				l.pos++
			}
			if l.pos < len(l.src) && l.src[l.pos] == '\n' {
				l.pos++
			}
			l.line++
			l.col = 1
			continue
		}

		// Newline
		if ch == '\n' || ch == '\r' {
			start := uint32(l.pos)
			line := l.line
			col := l.col
			if ch == '\r' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '\n' {
				l.pos++
			}
			l.pos++
			l.line++
			l.col = 1

			// If inside parentheses/brackets/braces, suppress NEWLINE token and ignore indentation
			if l.parenDepth > 0 {
				continue
			}

			l.atBOL = true
			if !l.hasTokensOnLine {
				continue
			}
			l.hasTokensOnLine = false

			return Token{
				Type:  TOKEN_NEWLINE,
				Start: start,
				End:   uint32(l.pos),
				Line:  line,
				Col:   col,
			}
		}

		// Start of a regular token
		l.hasTokensOnLine = true
		startPos := uint32(l.pos)
		tokLine := l.line
		tokCol := l.col

		// Identifier or Keyword (or prefixed string)
		if isIdentStart(ch) {
			if tok, ok := l.tryReadPrefixedString(startPos, tokLine, tokCol); ok {
				return tok
			}

			for l.pos < len(l.src) && isIdentPart(l.src[l.pos]) {
				l.pos++
				l.col++
			}
			text := string(l.src[startPos:l.pos])
			tokType := lookupKeyword(text)
			return Token{
				Type:  tokType,
				Start: startPos,
				End:   uint32(l.pos),
				Line:  tokLine,
				Col:   tokCol,
			}
		}

		// Numeric Literal (Int or Float)
		if isDigit(ch) || (ch == '.' && l.pos+1 < len(l.src) && isDigit(l.src[l.pos+1])) {
			return l.readNumber(startPos, tokLine, tokCol)
		}

		// String Literal
		if ch == '\'' || ch == '"' {
			return l.readString(startPos, tokLine, tokCol)
		}

		// Operators & Delimiters
		return l.readOperatorOrDelimiter(startPos, tokLine, tokCol)
	}

	if len(l.fstringStack) > 0 {
		l.err = fmt.Errorf("SyntaxError: unterminated f-string at line %d", l.line)
		return Token{
			Type:  TOKEN_ERROR,
			Start: uint32(l.pos),
			End:   uint32(l.pos),
			Line:  l.line,
			Col:   l.col,
		}
	}

	// At EOF: unwind indentation stack
	if len(l.indentStack) > 1 {
		l.indentStack = l.indentStack[:len(l.indentStack)-1]
		return Token{
			Type:  TOKEN_DEDENT,
			Start: uint32(l.pos),
			End:   uint32(l.pos),
			Line:  l.line,
			Col:   l.col,
		}
	}

	return Token{
		Type:  TOKEN_EOF,
		Start: uint32(l.pos),
		End:   uint32(l.pos),
		Line:  l.line,
		Col:   l.col,
	}
}

// handleBOL computes indentation at the beginning of a physical line.
func (l *Lexer) handleBOL() {
	l.atBOL = false
	indent := 0
	scanPos := l.pos
	scanCol := l.col

	for scanPos < len(l.src) {
		c := l.src[scanPos]
		if c == ' ' {
			indent++
			scanPos++
			scanCol++
		} else if c == '\t' {
			indent = (indent + 8) & ^7
			scanPos++
			scanCol++
		} else {
			break
		}
	}

	// Blank line or comment-only line: ignore indentation
	if scanPos < len(l.src) && (l.src[scanPos] == '\n' || l.src[scanPos] == '\r' || l.src[scanPos] == '#') {
		l.pos = scanPos
		l.col = scanCol
		return
	}

	// If we reached EOF with only whitespace, don't change indent
	if scanPos >= len(l.src) {
		l.pos = scanPos
		l.col = scanCol
		return
	}

	l.pos = scanPos
	l.col = scanCol

	currentIndent := l.indentStack[len(l.indentStack)-1]
	if indent > currentIndent {
		l.indentStack = append(l.indentStack, indent)
		l.pendingToks = append(l.pendingToks, Token{
			Type:  TOKEN_INDENT,
			Start: uint32(scanPos),
			End:   uint32(scanPos),
			Line:  l.line,
			Col:   l.col,
		})
	} else if indent < currentIndent {
		for len(l.indentStack) > 1 && l.indentStack[len(l.indentStack)-1] > indent {
			l.indentStack = l.indentStack[:len(l.indentStack)-1]
			l.pendingToks = append(l.pendingToks, Token{
				Type:  TOKEN_DEDENT,
				Start: uint32(scanPos),
				End:   uint32(scanPos),
				Line:  l.line,
				Col:   l.col,
			})
		}
		if l.indentStack[len(l.indentStack)-1] != indent {
			l.err = fmt.Errorf("IndentationError: unindent does not match any outer indentation level at line %d", l.line)
			l.pendingToks = append(l.pendingToks, Token{
				Type:  TOKEN_ERROR,
				Start: uint32(scanPos),
				End:   uint32(scanPos),
				Line:  l.line,
				Col:   l.col,
			})
		}
	}
}

func (l *Lexer) readNumber(startPos uint32, line, col uint32) Token {
	isFloat := false

	// Handle hex, bin, oct
	if l.src[l.pos] == '0' && l.pos+1 < len(l.src) {
		next := l.src[l.pos+1]
		if next == 'x' || next == 'X' {
			l.pos += 2
			l.col += 2
			for l.pos < len(l.src) && (isHexDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
				l.pos++
				l.col++
			}
			return Token{Type: TOKEN_INT, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		} else if next == 'b' || next == 'B' {
			l.pos += 2
			l.col += 2
			for l.pos < len(l.src) && (l.src[l.pos] == '0' || l.src[l.pos] == '1' || l.src[l.pos] == '_') {
				l.pos++
				l.col++
			}
			return Token{Type: TOKEN_INT, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		} else if next == 'o' || next == 'O' {
			l.pos += 2
			l.col += 2
			for l.pos < len(l.src) && ((l.src[l.pos] >= '0' && l.src[l.pos] <= '7') || l.src[l.pos] == '_') {
				l.pos++
				l.col++
			}
			return Token{Type: TOKEN_INT, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
	}

	for l.pos < len(l.src) && (isDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
		l.pos++
		l.col++
	}

	if l.pos < len(l.src) && l.src[l.pos] == '.' {
		// Distinguish float from attribute access on integer (e.g. 5.foo vs 5.2)
		if l.pos+1 < len(l.src) && isDigit(l.src[l.pos+1]) {
			isFloat = true
			l.pos++
			l.col++
			for l.pos < len(l.src) && (isDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
				l.pos++
				l.col++
			}
		} else if l.pos+1 >= len(l.src) || (!isIdentStart(l.src[l.pos+1]) && l.src[l.pos+1] != '.') {
			isFloat = true
			l.pos++
			l.col++
		}
	}

	// Exponent
	if l.pos < len(l.src) && (l.src[l.pos] == 'e' || l.src[l.pos] == 'E') {
		isFloat = true
		l.pos++
		l.col++
		if l.pos < len(l.src) && (l.src[l.pos] == '+' || l.src[l.pos] == '-') {
			l.pos++
			l.col++
		}
		for l.pos < len(l.src) && (isDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
			l.pos++
			l.col++
		}
	}

	tokType := TOKEN_INT
	if isFloat {
		tokType = TOKEN_FLOAT
	}
	if l.pos < len(l.src) && (l.src[l.pos] == 'j' || l.src[l.pos] == 'J') {
		l.pos++
		l.col++
		tokType = TOKEN_IMAG
	}
	return Token{Type: tokType, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
}

func (l *Lexer) readString(startPos uint32, line, col uint32) Token {
	quote := l.src[l.pos]
	isTriple := false

	if l.pos+2 < len(l.src) && l.src[l.pos+1] == quote && l.src[l.pos+2] == quote {
		isTriple = true
		l.pos += 3
		l.col += 3
	} else {
		l.pos++
		l.col++
	}

	for l.pos < len(l.src) {
		ch := l.src[l.pos]
		if ch == '\\' {
			l.pos += 2
			l.col += 2
			continue
		}
		if isTriple {
			if ch == quote && l.pos+2 < len(l.src) && l.src[l.pos+1] == quote && l.src[l.pos+2] == quote {
				l.pos += 3
				l.col += 3
				return Token{Type: TOKEN_STRING, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
			}
			if ch == '\n' {
				l.line++
				l.col = 1
				l.pos++
				continue
			}
		} else {
			if ch == quote {
				l.pos++
				l.col++
				return Token{Type: TOKEN_STRING, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
			}
			if ch == '\n' || ch == '\r' {
				// Unterminated single-line string
				return Token{Type: TOKEN_ERROR, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
			}
		}
		l.pos++
		l.col++
	}

	return Token{Type: TOKEN_ERROR, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
}

func (l *Lexer) tryReadPrefixedString(startPos uint32, line, col uint32) (Token, bool) {
	pos := l.pos
	var pfx [2]byte
	pfxLen := 0
	for pos < len(l.src) && pfxLen < 2 && isIdentStart(l.src[pos]) {
		pfx[pfxLen] = l.src[pos]
		pfxLen++
		pos++
	}
	if pfxLen == 0 || pos >= len(l.src) {
		return Token{}, false
	}
	quote := l.src[pos]
	if quote != '\'' && quote != '"' {
		if pfxLen == 2 && (l.src[pos-1] == '\'' || l.src[pos-1] == '"') {
			pfxLen = 1
			pos--
			quote = l.src[pos]
		} else {
			return Token{}, false
		}
	}

	isFString := false
	isRaw := false
	isTemplate := false
	isValid := false

	pStr := strings.ToLower(string(pfx[:pfxLen]))
	switch pStr {
	case "f":
		isFString = true
		isValid = true
	case "r":
		isRaw = true
		isValid = true
	case "b", "u":
		isValid = true
	case "t":
		isFString = true
		isTemplate = true
		isValid = true
	case "fr", "rf":
		isFString = true
		isRaw = true
		isValid = true
	case "br", "rb":
		isRaw = true
		isValid = true
	case "tr", "rt":
		isFString = true
		isRaw = true
		isTemplate = true
		isValid = true
	}

	if !isValid {
		return Token{}, false
	}

	l.pos = pos
	l.col += uint32(pfxLen)

	if isFString {
		quoteChar := l.src[l.pos]
		isTriple := false
		if l.pos+2 < len(l.src) && l.src[l.pos+1] == quoteChar && l.src[l.pos+2] == quoteChar {
			isTriple = true
			l.pos += 3
			l.col += 3
		} else {
			l.pos++
			l.col++
		}
		l.fstringStack = append(l.fstringStack, fstringState{
			quote:      quoteChar,
			isTriple:   isTriple,
			raw:        isRaw,
			isTemplate: isTemplate,
			braceDepth: 0,
			parenDepth: 0,
		})
		tokType := TOKEN_FSTRING_START
		if isTemplate {
			tokType = TOKEN_TSTRING_START
		}
		return Token{Type: tokType, Start: startPos, End: uint32(l.pos), Line: line, Col: col}, true
	}

	return l.readString(startPos, line, col), true
}

func (l *Lexer) readFStringMiddle(top *fstringState) Token {
	startPos := uint32(l.pos)
	line := l.line
	col := l.col

	if l.pos >= len(l.src) {
		l.err = fmt.Errorf("SyntaxError: unterminated f-string at line %d", line)
		return Token{Type: TOKEN_ERROR, Start: startPos, End: startPos, Line: line, Col: col}
	}

	// Check for closing quote
	if top.isTriple {
		if l.pos+2 < len(l.src) && l.src[l.pos] == top.quote && l.src[l.pos+1] == top.quote && l.src[l.pos+2] == top.quote {
			l.pos += 3
			l.col += 3
			l.fstringStack = l.fstringStack[:len(l.fstringStack)-1]
			return Token{Type: TOKEN_FSTRING_END, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
	} else {
		if l.src[l.pos] == top.quote {
			l.pos++
			l.col++
			l.fstringStack = l.fstringStack[:len(l.fstringStack)-1]
			return Token{Type: TOKEN_FSTRING_END, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
	}

	// Check for {
	if l.src[l.pos] == '{' {
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '{' {
			l.pos += 2
			l.col += 2
			return Token{Type: TOKEN_FSTRING_MIDDLE, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		l.pos++
		l.col++
		l.parenDepth++
		top.braceDepth = 1
		top.parenDepth = 0
		top.inSpec = false
		return Token{Type: TOKEN_LBRACE, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
	}

	// Check for }
	if l.src[l.pos] == '}' {
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '}' {
			l.pos += 2
			l.col += 2
			return Token{Type: TOKEN_FSTRING_MIDDLE, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		l.pos++
		l.col++
		l.err = fmt.Errorf("SyntaxError: f-string: single '}' is not allowed at line %d, col %d", line, col)
		return Token{Type: TOKEN_ERROR, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
	}

	for l.pos < len(l.src) {
		ch := l.src[l.pos]
		if top.isTriple {
			if l.pos+2 < len(l.src) && ch == top.quote && l.src[l.pos+1] == top.quote && l.src[l.pos+2] == top.quote {
				break
			}
		} else {
			if ch == top.quote {
				break
			}
		}

		if ch == '{' || ch == '}' {
			break
		}

		if ch == '\\' {
			if !top.raw {
				l.pos++
				l.col++
				if l.pos < len(l.src) {
					if l.src[l.pos] == '\n' {
						l.line++
						l.col = 1
					} else {
						l.col++
					}
					isN := l.src[l.pos] == 'N'
					l.pos++
					// \N{NAME}: the braces belong to the escape, not to a
					// replacement field.
					if isN && l.pos < len(l.src) && l.src[l.pos] == '{' {
						j := l.pos + 1
						for j < len(l.src) && l.src[j] != '}' && l.src[j] != '\n' && l.src[j] != top.quote {
							j++
						}
						if j < len(l.src) && l.src[j] == '}' {
							l.col += uint32(j + 1 - l.pos)
							l.pos = j + 1
						}
					}
				}
				continue
			}
		}

		if ch == '\n' {
			if !top.isTriple {
				l.err = fmt.Errorf("SyntaxError: unterminated f-string at line %d", line)
				return Token{Type: TOKEN_ERROR, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
			}
			l.line++
			l.col = 1
			l.pos++
			continue
		}

		l.pos++
		l.col++
	}

	return Token{Type: TOKEN_FSTRING_MIDDLE, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
}

func (l *Lexer) readOperatorOrDelimiter(startPos uint32, line, col uint32) Token {
	ch := l.src[l.pos]
	l.pos++
	l.col++

	switch ch {
	case '(':
		l.parenDepth++
		if len(l.fstringStack) > 0 {
			l.fstringStack[len(l.fstringStack)-1].parenDepth++
		}
		return Token{Type: TOKEN_LPAREN, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
	case ')':
		if l.parenDepth > 0 {
			l.parenDepth--
		}
		if len(l.fstringStack) > 0 {
			top := &l.fstringStack[len(l.fstringStack)-1]
			if top.parenDepth > 0 {
				top.parenDepth--
			}
		}
		return Token{Type: TOKEN_RPAREN, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
	case '[':
		l.parenDepth++
		if len(l.fstringStack) > 0 {
			l.fstringStack[len(l.fstringStack)-1].parenDepth++
		}
		return Token{Type: TOKEN_LBRACKET, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
	case ']':
		if l.parenDepth > 0 {
			l.parenDepth--
		}
		if len(l.fstringStack) > 0 {
			top := &l.fstringStack[len(l.fstringStack)-1]
			if top.parenDepth > 0 {
				top.parenDepth--
			}
		}
		return Token{Type: TOKEN_RBRACKET, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
	case '{':
		l.parenDepth++
		if len(l.fstringStack) > 0 {
			l.fstringStack[len(l.fstringStack)-1].braceDepth++
		}
		return Token{Type: TOKEN_LBRACE, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
	case '}':
		if l.parenDepth > 0 {
			l.parenDepth--
		}
		if len(l.fstringStack) > 0 {
			top := &l.fstringStack[len(l.fstringStack)-1]
			if top.braceDepth > 0 {
				top.braceDepth--
			}
		}
		return Token{Type: TOKEN_RBRACE, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
	case ':':
		atSpecStart := false
		if len(l.fstringStack) > 0 {
			top := &l.fstringStack[len(l.fstringStack)-1]
			// A ':' at the top level of a replacement field starts the format
			// spec, so ":=" there is a colon followed by spec text, not walrus.
			atSpecStart = top.parenDepth == 0 && top.braceDepth <= 1
			if atSpecStart {
				top.inSpec = true
			}
		}
		if !atSpecStart && l.pos < len(l.src) && l.src[l.pos] == '=' {
			l.pos++
			l.col++
			return Token{Type: TOKEN_WALRUS, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		return Token{Type: TOKEN_COLON, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
	case ',':
		return Token{Type: TOKEN_COMMA, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
	case '.':
		if l.pos+1 < len(l.src) && l.src[l.pos] == '.' && l.src[l.pos+1] == '.' {
			l.pos += 2
			l.col += 2
			return Token{Type: TOKEN_ELLIPSIS, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		return Token{Type: TOKEN_DOT, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
	case ';':
		return Token{Type: TOKEN_SEMI, Start: startPos, End: uint32(l.pos), Line: line, Col: col}

	case '+':
		if l.pos < len(l.src) && l.src[l.pos] == '=' {
			l.pos++
			l.col++
			return Token{Type: TOKEN_PLUSEQ, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		return Token{Type: TOKEN_PLUS, Start: startPos, End: uint32(l.pos), Line: line, Col: col}

	case '-':
		if l.pos < len(l.src) && l.src[l.pos] == '>' {
			l.pos++
			l.col++
			return Token{Type: TOKEN_ARROW, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		if l.pos < len(l.src) && l.src[l.pos] == '=' {
			l.pos++
			l.col++
			return Token{Type: TOKEN_MINUSEQ, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		return Token{Type: TOKEN_MINUS, Start: startPos, End: uint32(l.pos), Line: line, Col: col}

	case '*':
		if l.pos < len(l.src) && l.src[l.pos] == '*' {
			l.pos++
			l.col++
			if l.pos < len(l.src) && l.src[l.pos] == '=' {
				l.pos++
				l.col++
				return Token{Type: TOKEN_STARSTAREQ, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
			}
			return Token{Type: TOKEN_STARSTAR, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		if l.pos < len(l.src) && l.src[l.pos] == '=' {
			l.pos++
			l.col++
			return Token{Type: TOKEN_STAREQ, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		return Token{Type: TOKEN_STAR, Start: startPos, End: uint32(l.pos), Line: line, Col: col}

	case '/':
		if l.pos < len(l.src) && l.src[l.pos] == '/' {
			l.pos++
			l.col++
			if l.pos < len(l.src) && l.src[l.pos] == '=' {
				l.pos++
				l.col++
				return Token{Type: TOKEN_SLASHSLASHEQ, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
			}
			return Token{Type: TOKEN_SLASHSLASH, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		if l.pos < len(l.src) && l.src[l.pos] == '=' {
			l.pos++
			l.col++
			return Token{Type: TOKEN_SLASHEQ, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		return Token{Type: TOKEN_SLASH, Start: startPos, End: uint32(l.pos), Line: line, Col: col}

	case '%':
		if l.pos < len(l.src) && l.src[l.pos] == '=' {
			l.pos++
			l.col++
			return Token{Type: TOKEN_PERCENTEQ, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		return Token{Type: TOKEN_PERCENT, Start: startPos, End: uint32(l.pos), Line: line, Col: col}

	case '=':
		if l.pos < len(l.src) && l.src[l.pos] == '=' {
			l.pos++
			l.col++
			return Token{Type: TOKEN_EQ, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		return Token{Type: TOKEN_ASSIGN, Start: startPos, End: uint32(l.pos), Line: line, Col: col}

	case '!':
		if l.pos < len(l.src) && l.src[l.pos] == '=' {
			l.pos++
			l.col++
			return Token{Type: TOKEN_NEQ, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		return Token{Type: TOKEN_EXCLAIM, Start: startPos, End: uint32(l.pos), Line: line, Col: col}

	case '<':
		if l.pos < len(l.src) && l.src[l.pos] == '<' {
			l.pos++
			l.col++
			if l.pos < len(l.src) && l.src[l.pos] == '=' {
				l.pos++
				l.col++
				return Token{Type: TOKEN_LSHIFTEQ, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
			}
			return Token{Type: TOKEN_LSHIFT, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		if l.pos < len(l.src) && l.src[l.pos] == '=' {
			l.pos++
			l.col++
			return Token{Type: TOKEN_LTE, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		return Token{Type: TOKEN_LT, Start: startPos, End: uint32(l.pos), Line: line, Col: col}

	case '>':
		if l.pos < len(l.src) && l.src[l.pos] == '>' {
			l.pos++
			l.col++
			if l.pos < len(l.src) && l.src[l.pos] == '=' {
				l.pos++
				l.col++
				return Token{Type: TOKEN_RSHIFTEQ, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
			}
			return Token{Type: TOKEN_RSHIFT, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		if l.pos < len(l.src) && l.src[l.pos] == '=' {
			l.pos++
			l.col++
			return Token{Type: TOKEN_GTE, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		return Token{Type: TOKEN_GT, Start: startPos, End: uint32(l.pos), Line: line, Col: col}

	case '@':
		if l.pos < len(l.src) && l.src[l.pos] == '=' {
			l.pos++
			l.col++
			return Token{Type: TOKEN_ATEQ, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		return Token{Type: TOKEN_AT, Start: startPos, End: uint32(l.pos), Line: line, Col: col}

	case '&':
		if l.pos < len(l.src) && l.src[l.pos] == '=' {
			l.pos++
			l.col++
			return Token{Type: TOKEN_AMPEQ, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		return Token{Type: TOKEN_AMP, Start: startPos, End: uint32(l.pos), Line: line, Col: col}

	case '|':
		if l.pos < len(l.src) && l.src[l.pos] == '=' {
			l.pos++
			l.col++
			return Token{Type: TOKEN_PIPEEQ, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		return Token{Type: TOKEN_PIPE, Start: startPos, End: uint32(l.pos), Line: line, Col: col}

	case '^':
		if l.pos < len(l.src) && l.src[l.pos] == '=' {
			l.pos++
			l.col++
			return Token{Type: TOKEN_CARETEQ, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		return Token{Type: TOKEN_CARET, Start: startPos, End: uint32(l.pos), Line: line, Col: col}

	case '~':
		return Token{Type: TOKEN_TILDE, Start: startPos, End: uint32(l.pos), Line: line, Col: col}

	default:
		return Token{Type: TOKEN_ERROR, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
	}
}

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func lookupKeyword(s string) TokenType {
	switch s {
	case "def":
		return TOKEN_DEF
	case "class":
		return TOKEN_CLASS
	case "return":
		return TOKEN_RETURN
	case "if":
		return TOKEN_IF
	case "elif":
		return TOKEN_ELIF
	case "else":
		return TOKEN_ELSE
	case "while":
		return TOKEN_WHILE
	case "for":
		return TOKEN_FOR
	case "in":
		return TOKEN_IN
	case "break":
		return TOKEN_BREAK
	case "continue":
		return TOKEN_CONTINUE
	case "pass":
		return TOKEN_PASS
	case "global":
		return TOKEN_GLOBAL
	case "nonlocal":
		return TOKEN_NONLOCAL
	case "and":
		return TOKEN_AND
	case "or":
		return TOKEN_OR
	case "not":
		return TOKEN_NOT
	case "is":
		return TOKEN_IS
	case "lambda":
		return TOKEN_LAMBDA
	case "None":
		return TOKEN_NONE
	case "True":
		return TOKEN_TRUE
	case "False":
		return TOKEN_FALSE
	case "try":
		return TOKEN_TRY
	case "except":
		return TOKEN_EXCEPT
	case "finally":
		return TOKEN_FINALLY
	case "raise":
		return TOKEN_RAISE
	case "with":
		return TOKEN_WITH
	case "as":
		return TOKEN_AS
	case "yield":
		return TOKEN_YIELD
	case "import":
		return TOKEN_IMPORT
	case "from":
		return TOKEN_FROM
	case "async":
		return TOKEN_ASYNC
	case "await":
		return TOKEN_AWAIT
	case "match":
		return TOKEN_MATCH
	case "case":
		return TOKEN_CASE
	case "type":
		return TOKEN_TYPE
	case "assert":
		return TOKEN_ASSERT
	case "del":
		return TOKEN_DEL
	default:
		return TOKEN_IDENT
	}
}
