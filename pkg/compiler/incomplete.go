package compiler

import (
	"strings"
)

// IncompleteInput reports whether src is a syntactically incomplete
// (but not yet invalid) interactive statement: more input is required
// before the statement can be compiled. It mirrors the classification
// made by CPython's codeop module (compile_command returning None):
// open brackets, unterminated triple-quoted strings, trailing line
// continuations, and compound-statement blocks that have not been
// terminated by a blank line are incomplete; outright malformed
// input is not.
func IncompleteInput(src []byte) bool {
	if len(src) == 0 {
		return false
	}

	// Line-continuation backslash on the final line.
	for line := range strings.SplitSeq(strings.TrimRight(string(src), " \t\r\n"), "\n") {
		if trimmed := strings.TrimRight(line, " \t\r"); trimmed != "" {
			if strings.HasSuffix(trimmed, `\`) {
				return true
			}
			break
		}
	}

	// Token pass: bracket depth, unterminated strings, and code presence.
	lx := NewLexer(src)
	depth := 0
	sawCode := false
	for {
		t := lx.NextToken()
		if t.Type == TOKEN_EOF {
			break
		}
		if t.Type == TOKEN_ERROR {
			frag := ""
			if int(t.End) <= len(src) && int(t.Start) <= len(src) {
				frag = string(src[t.Start:t.End])
			}
			if strings.Contains(frag, `"""`) || strings.Contains(frag, `'''`) {
				return true
			}
			return false
		}
		switch t.Type {
		case TOKEN_LPAREN, TOKEN_LBRACKET, TOKEN_LBRACE,
			TOKEN_FSTRING_START, TOKEN_TSTRING_START:
			depth++
		case TOKEN_RPAREN, TOKEN_RBRACKET, TOKEN_RBRACE, TOKEN_FSTRING_END:
			depth--
		case TOKEN_NEWLINE, TOKEN_INDENT, TOKEN_DEDENT:
			// not code by themselves
		default:
			sawCode = true
		}
	}
	if !sawCode {
		return false
	}
	if depth != 0 {
		return true
	}

	// Line pass: block headers and blank-line termination, plus line
	// continuation backslashes. A blank line terminates an open block
	// only once a body line has been seen after its header.
	lines := strings.Split(string(src), "\n")
	openBlock := false
	bodySeen := false
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(trimmed) == "" {
			if openBlock && bodySeen {
				openBlock = false
				bodySeen = false
			}
			continue
		}
		if strings.HasSuffix(trimmed, `\`) {
			return true
		}
		if strings.HasSuffix(trimmed, ":") && isBlockHeaderLine(trimmed) {
			openBlock = true
			bodySeen = false
			continue
		}
		if openBlock {
			bodySeen = true
		}
	}
	return openBlock
}

// isBlockHeaderLine reports whether a colon-terminated line is a plausible
// compound statement header (so "def 123:" stays a hard syntax error).
func isBlockHeaderLine(line string) bool {
	head := strings.TrimSpace(line)
	if head == "" {
		return false
	}
	fields := strings.FieldsFunc(head, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '(' || r == ':'
	})
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "def", "class":
		if len(fields) < 2 {
			return false
		}
		name := fields[1]
		for i, r := range name {
			if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9') {
				continue
			}
			return false
		}
		return true
	case "if", "elif", "else", "for", "while", "with", "try", "except",
		"finally", "match", "case", "async":
		return true
	}
	// A lone colon line or one starting with a keyword is a header only
	// for the compound keywords above; anything else (e.g. a dict entry
	// or slice) is left to the parser.
	return false
}
