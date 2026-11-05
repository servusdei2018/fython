package compiler

// TokenType represents a lexical token category.
type TokenType uint16

const (
	TOKEN_EOF TokenType = iota
	TOKEN_ERROR
	TOKEN_NEWLINE
	TOKEN_INDENT
	TOKEN_DEDENT

	// Literals & Identifiers
	TOKEN_IDENT
	TOKEN_INT
	TOKEN_FLOAT
	TOKEN_STRING

	// Keywords
	TOKEN_DEF
	TOKEN_CLASS
	TOKEN_RETURN
	TOKEN_IF
	TOKEN_ELIF
	TOKEN_ELSE
	TOKEN_WHILE
	TOKEN_FOR
	TOKEN_IN
	TOKEN_BREAK
	TOKEN_CONTINUE
	TOKEN_PASS
	TOKEN_GLOBAL
	TOKEN_NONLOCAL
	TOKEN_AND
	TOKEN_OR
	TOKEN_NOT
	TOKEN_IS
	TOKEN_LAMBDA
	TOKEN_NONE
	TOKEN_TRUE
	TOKEN_FALSE
	TOKEN_TRY
	TOKEN_EXCEPT
	TOKEN_FINALLY
	TOKEN_RAISE
	TOKEN_WITH
	TOKEN_AS
	TOKEN_YIELD
	TOKEN_IMPORT
	TOKEN_FROM
	TOKEN_ASYNC
	TOKEN_AWAIT
	TOKEN_MATCH
	TOKEN_CASE
	TOKEN_TYPE
	TOKEN_ASSERT
	TOKEN_DEL

	// Delimiters
	TOKEN_LPAREN   // (
	TOKEN_RPAREN   // )
	TOKEN_LBRACKET // [
	TOKEN_RBRACKET // ]
	TOKEN_LBRACE   // {
	TOKEN_RBRACE   // }
	TOKEN_COLON    // :
	TOKEN_COMMA    // ,
	TOKEN_DOT      // .
	TOKEN_SEMI     // ;
	TOKEN_ELLIPSIS // ...
	TOKEN_ARROW    // ->

	// Arithmetic, Comparison & Augmented Assignment Operators
	TOKEN_ASSIGN       // =
	TOKEN_PLUS         // +
	TOKEN_MINUS        // -
	TOKEN_STAR         // *
	TOKEN_SLASH        // /
	TOKEN_SLASHSLASH   // //
	TOKEN_PERCENT      // %
	TOKEN_STARSTAR     // **
	TOKEN_EQ           // ==
	TOKEN_NEQ          // !=
	TOKEN_LT           // <
	TOKEN_LTE          // <=
	TOKEN_GT           // >
	TOKEN_GTE          // >=
	TOKEN_PLUSEQ       // +=
	TOKEN_MINUSEQ      // -=
	TOKEN_STAREQ       // *=
	TOKEN_SLASHEQ      // /=
	TOKEN_SLASHSLASHEQ // //=
	TOKEN_PERCENTEQ    // %=
	TOKEN_STARSTAREQ   // **=

	// Matrix & Assignment Expressions
	TOKEN_WALRUS // :=
	TOKEN_AT     // @
	TOKEN_ATEQ   // @=

	// Bitwise & Shift Operators
	TOKEN_AMP      // &
	TOKEN_AMPEQ    // &=
	TOKEN_PIPE     // |
	TOKEN_PIPEEQ   // |=
	TOKEN_CARET    // ^
	TOKEN_CARETEQ  // ^=
	TOKEN_TILDE    // ~
	TOKEN_LSHIFT   // <<
	TOKEN_LSHIFTEQ // <<=
	TOKEN_RSHIFT   // >>
	TOKEN_RSHIFTEQ // >>=

	// Formatted String Literals
	TOKEN_FSTRING_START
	TOKEN_FSTRING_MIDDLE
	TOKEN_FSTRING_END
)

// String returns a human-readable representation of the token type.
func (t TokenType) String() string {
	switch t {
	case TOKEN_EOF:
		return "EOF"
	case TOKEN_ERROR:
		return "ERROR"
	case TOKEN_NEWLINE:
		return "NEWLINE"
	case TOKEN_INDENT:
		return "INDENT"
	case TOKEN_DEDENT:
		return "DEDENT"
	case TOKEN_IDENT:
		return "IDENT"
	case TOKEN_INT:
		return "INT"
	case TOKEN_FLOAT:
		return "FLOAT"
	case TOKEN_STRING:
		return "STRING"
	case TOKEN_DEF:
		return "def"
	case TOKEN_CLASS:
		return "class"
	case TOKEN_RETURN:
		return "return"
	case TOKEN_IF:
		return "if"
	case TOKEN_ELIF:
		return "elif"
	case TOKEN_ELSE:
		return "else"
	case TOKEN_WHILE:
		return "while"
	case TOKEN_FOR:
		return "for"
	case TOKEN_IN:
		return "in"
	case TOKEN_BREAK:
		return "break"
	case TOKEN_CONTINUE:
		return "continue"
	case TOKEN_PASS:
		return "pass"
	case TOKEN_GLOBAL:
		return "global"
	case TOKEN_NONLOCAL:
		return "nonlocal"
	case TOKEN_AND:
		return "and"
	case TOKEN_OR:
		return "or"
	case TOKEN_NOT:
		return "not"
	case TOKEN_IS:
		return "is"
	case TOKEN_LAMBDA:
		return "lambda"
	case TOKEN_NONE:
		return "None"
	case TOKEN_TRUE:
		return "True"
	case TOKEN_FALSE:
		return "False"
	case TOKEN_TRY:
		return "try"
	case TOKEN_EXCEPT:
		return "except"
	case TOKEN_FINALLY:
		return "finally"
	case TOKEN_RAISE:
		return "raise"
	case TOKEN_WITH:
		return "with"
	case TOKEN_AS:
		return "as"
	case TOKEN_YIELD:
		return "yield"
	case TOKEN_IMPORT:
		return "import"
	case TOKEN_FROM:
		return "from"
	case TOKEN_ASYNC:
		return "async"
	case TOKEN_AWAIT:
		return "await"
	case TOKEN_LPAREN:
		return "("
	case TOKEN_RPAREN:
		return ")"
	case TOKEN_LBRACKET:
		return "["
	case TOKEN_RBRACKET:
		return "]"
	case TOKEN_LBRACE:
		return "{"
	case TOKEN_RBRACE:
		return "}"
	case TOKEN_COLON:
		return ":"
	case TOKEN_COMMA:
		return ","
	case TOKEN_DOT:
		return "."
	case TOKEN_SEMI:
		return ";"
	case TOKEN_ASSIGN:
		return "="
	case TOKEN_PLUS:
		return "+"
	case TOKEN_MINUS:
		return "-"
	case TOKEN_STAR:
		return "*"
	case TOKEN_SLASH:
		return "/"
	case TOKEN_SLASHSLASH:
		return "//"
	case TOKEN_PERCENT:
		return "%"
	case TOKEN_STARSTAR:
		return "**"
	case TOKEN_EQ:
		return "=="
	case TOKEN_NEQ:
		return "!="
	case TOKEN_LT:
		return "<"
	case TOKEN_LTE:
		return "<="
	case TOKEN_GT:
		return ">"
	case TOKEN_GTE:
		return ">="
	case TOKEN_PLUSEQ:
		return "+="
	case TOKEN_MINUSEQ:
		return "-="
	case TOKEN_STAREQ:
		return "*="
	case TOKEN_SLASHEQ:
		return "/="
	case TOKEN_SLASHSLASHEQ:
		return "//="
	case TOKEN_PERCENTEQ:
		return "%="
	case TOKEN_STARSTAREQ:
		return "**="
	case TOKEN_MATCH:
		return "match"
	case TOKEN_CASE:
		return "case"
	case TOKEN_TYPE:
		return "type"
	case TOKEN_ASSERT:
		return "assert"
	case TOKEN_DEL:
		return "del"
	case TOKEN_ELLIPSIS:
		return "..."
	case TOKEN_ARROW:
		return "->"
	case TOKEN_WALRUS:
		return ":="
	case TOKEN_AT:
		return "@"
	case TOKEN_ATEQ:
		return "@="
	case TOKEN_AMP:
		return "&"
	case TOKEN_AMPEQ:
		return "&="
	case TOKEN_PIPE:
		return "|"
	case TOKEN_PIPEEQ:
		return "|="
	case TOKEN_CARET:
		return "^"
	case TOKEN_CARETEQ:
		return "^="
	case TOKEN_TILDE:
		return "~"
	case TOKEN_LSHIFT:
		return "<<"
	case TOKEN_LSHIFTEQ:
		return "<<="
	case TOKEN_RSHIFT:
		return ">>"
	case TOKEN_RSHIFTEQ:
		return ">>="
	case TOKEN_FSTRING_START:
		return "FSTRING_START"
	case TOKEN_FSTRING_MIDDLE:
		return "FSTRING_MIDDLE"
	case TOKEN_FSTRING_END:
		return "FSTRING_END"
	default:
		return "UNKNOWN"
	}
}

// Token represents a zero-copy lexical token storing byte offsets into the original source buffer.
type Token struct {
	Type  TokenType
	Start uint32
	End   uint32
	Line  uint32
	Col   uint32
}
