package compiler

import (
	"fmt"
)

// Lexer implements a zero-copy Python tokenizer with indentation state machine.
type Lexer struct {
	src         []byte
	pos         int
	line        uint32
	col         uint32
	indentStack []int
	parenDepth  int
	pendingToks []Token
	atBOL       bool // at Beginning of Line
	err         error
}

// NewLexer creates a new Lexer for the source buffer.
func NewLexer(src []byte) *Lexer {
	return &Lexer{
		src:         src,
		pos:         0,
		line:        1,
		col:         1,
		indentStack: []int{0},
		parenDepth:  0,
		pendingToks: make([]Token, 0, 8),
		atBOL:       true,
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
	l.err = nil
}

// NextToken returns the next zero-copy Token.
func (l *Lexer) NextToken() Token {
	if len(l.pendingToks) > 0 {
		tok := l.pendingToks[0]
		l.pendingToks = l.pendingToks[1:]
		return tok
	}

	for l.pos < len(l.src) {
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
			l.atBOL = true

			// If inside parentheses/brackets/braces, suppress NEWLINE token
			if l.parenDepth > 0 {
				continue
			}

			return Token{
				Type:  TOKEN_NEWLINE,
				Start: start,
				End:   uint32(l.pos),
				Line:  line,
				Col:   col,
			}
		}

		// Start of a regular token
		startPos := uint32(l.pos)
		tokLine := l.line
		tokCol := l.col

		// Identifier or Keyword (or prefixed string)
		if isIdentStart(ch) {
			// Check for string prefix (r, b, f, etc.) followed by quote
			if (ch == 'r' || ch == 'R' || ch == 'b' || ch == 'B' || ch == 'f' || ch == 'F') &&
				l.pos+1 < len(l.src) && (l.src[l.pos+1] == '\'' || l.src[l.pos+1] == '"') {
				l.pos++
				l.col++
				return l.readString(startPos, tokLine, tokCol)
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

func (l *Lexer) readOperatorOrDelimiter(startPos uint32, line, col uint32) Token {
	ch := l.src[l.pos]
	l.pos++
	l.col++

	switch ch {
	case '(':
		l.parenDepth++
		return Token{Type: TOKEN_LPAREN, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
	case ')':
		if l.parenDepth > 0 {
			l.parenDepth--
		}
		return Token{Type: TOKEN_RPAREN, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
	case '[':
		l.parenDepth++
		return Token{Type: TOKEN_LBRACKET, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
	case ']':
		if l.parenDepth > 0 {
			l.parenDepth--
		}
		return Token{Type: TOKEN_RBRACKET, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
	case '{':
		l.parenDepth++
		return Token{Type: TOKEN_LBRACE, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
	case '}':
		if l.parenDepth > 0 {
			l.parenDepth--
		}
		return Token{Type: TOKEN_RBRACE, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
	case ':':
		return Token{Type: TOKEN_COLON, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
	case ',':
		return Token{Type: TOKEN_COMMA, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
	case '.':
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
		return Token{Type: TOKEN_ERROR, Start: startPos, End: uint32(l.pos), Line: line, Col: col}

	case '<':
		if l.pos < len(l.src) && l.src[l.pos] == '=' {
			l.pos++
			l.col++
			return Token{Type: TOKEN_LTE, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		return Token{Type: TOKEN_LT, Start: startPos, End: uint32(l.pos), Line: line, Col: col}

	case '>':
		if l.pos < len(l.src) && l.src[l.pos] == '=' {
			l.pos++
			l.col++
			return Token{Type: TOKEN_GTE, Start: startPos, End: uint32(l.pos), Line: line, Col: col}
		}
		return Token{Type: TOKEN_GT, Start: startPos, End: uint32(l.pos), Line: line, Col: col}

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
	default:
		return TOKEN_IDENT
	}
}
