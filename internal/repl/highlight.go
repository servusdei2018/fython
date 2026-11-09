package repl

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"github.com/servusdei2018/fython/pkg/compiler"
)

// ColorEnabled reports whether ANSI styling may be written to the given
// stdout file: NO_COLOR disables it, PYTHON_COLORS=0 disables it,
// PYTHON_COLORS=1 forces it on, otherwise it follows the terminal.
func ColorEnabled(stdout *os.File) bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	if pc := os.Getenv("PYTHON_COLORS"); pc != "" {
		return pc != "0"
	}
	if stdout == nil {
		stdout = os.Stdout
	}
	return term.IsTerminal(int(stdout.Fd()))
}

// colorizeTraceback tints a formatted traceback when colors are enabled.
func colorizeTraceback(cfg Config, tb string) string {
	if tb == "" || !ColorEnabled(cfg.StdoutFile) {
		return tb
	}
	return colorizeTracebackText(tb)
}

var (
	highlightKeyword  = lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Bold(true)
	highlightBuiltin  = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	highlightString   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	highlightNumber   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	highlightOperator = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	highlightIdent    = lipgloss.NewStyle()
	highlightOther    = lipgloss.NewStyle()
	tbCaret           = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	tbFrame           = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	tbHeader          = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
)

// Highlight styles the active input buffer using the compiler's lexer,
// mapping token classes to themed styles. When colors are disabled it
// returns src unchanged.
func Highlight(src string, enabled bool) string {
	if !enabled || src == "" {
		return src
	}
	lx := compiler.NewLexer([]byte(src))
	var sb strings.Builder
	last := 0
	for {
		t := lx.NextToken()
		if t.Type == compiler.TOKEN_EOF {
			break
		}
		start, end := int(t.Start), int(t.End)
		if start < 0 || end < start || end > len(src) {
			continue
		}
		if start > last {
			sb.WriteString(highlightOther.Render(src[last:start]))
		}
		sb.WriteString(styleFor(t, src[start:end]))
		last = end
	}
	if last < len(src) {
		sb.WriteString(highlightOther.Render(src[last:]))
	}
	return sb.String()
}

func styleFor(t compiler.Token, text string) string {
	switch t.Type {
	case compiler.TOKEN_STRING, compiler.TOKEN_FSTRING_START,
		compiler.TOKEN_FSTRING_MIDDLE, compiler.TOKEN_TSTRING_START:
		return highlightString.Render(text)
	case compiler.TOKEN_INT, compiler.TOKEN_FLOAT, compiler.TOKEN_IMAG:
		return highlightNumber.Render(text)
	case compiler.TOKEN_IDENT:
		if isKeyword(text) {
			return highlightKeyword.Render(text)
		}
		if isBuiltinName(text) {
			return highlightBuiltin.Render(text)
		}
		return highlightIdent.Render(text)
	default:
		if isKeywordToken(t.Type) {
			return highlightKeyword.Render(text)
		}
		return highlightOperator.Render(text)
	}
}

// colorizeTracebackText themes the header line, caret source lines, and
// exception line of a formatted traceback.
func colorizeTracebackText(tb string) string {
	lines := strings.Split(tb, "\n")
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "Traceback"):
			lines[i] = tbHeader.Render(line)
		case strings.HasPrefix(line, "  File"):
			lines[i] = tbFrame.Render(line)
		case strings.TrimSpace(line) != "" && !strings.HasPrefix(line, " "):
			lines[i] = tbHeader.Render(line)
		case strings.Contains(line, "^"):
			lines[i] = tbCaret.Render(line)
		}
	}
	return strings.Join(lines, "\n")
}

// highlightDisabled reports whether syntax highlighting should be off.
func highlightDisabled() bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return true
	}
	if pc := os.Getenv("PYTHON_COLORS"); pc == "0" {
		return true
	}
	return false
}
