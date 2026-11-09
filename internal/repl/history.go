package repl

import (
	"os"
	"path/filepath"
	"strings"
)

// History persists REPL entries across sessions (one entry per line;
// multiline entries keep their internal newlines).
type History struct {
	Entries []string
	path    string
	limit   int
}

// HistoryPath resolves the history file location.
func HistoryPath() string {
	if p := os.Getenv("PYTHON_HISTORY"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".fython_history"
	}
	return filepath.Join(home, ".fython_history")
}

// LoadHistory reads history entries from the default location.
func LoadHistory() *History {
	h := &History{path: HistoryPath(), limit: 1000}
	if data, err := os.ReadFile(h.path); err == nil {
		for line := range strings.SplitSeq(string(data), "\n") {
			if line != "" {
				h.Entries = append(h.Entries, unescapeEntry(line))
			}
		}
		if len(h.Entries) > h.limit {
			h.Entries = h.Entries[len(h.Entries)-h.limit:]
		}
	}
	return h
}

// Add appends an entry (skipping blank and consecutive duplicates).
func (h *History) Add(entry string) {
	if strings.TrimSpace(entry) == "" {
		return
	}
	if n := len(h.Entries); n > 0 && h.Entries[n-1] == entry {
		return
	}
	h.Entries = append(h.Entries, entry)
	if len(h.Entries) > h.limit {
		h.Entries = h.Entries[len(h.Entries)-h.limit:]
	}
}

// Save writes entries back to disk.
func (h *History) Save() {
	if h.path == "" || len(h.Entries) == 0 {
		return
	}
	lines := make([]string, len(h.Entries))
	for i, e := range h.Entries {
		lines[i] = escapeEntry(e)
	}
	_ = os.WriteFile(h.path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

func escapeEntry(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, "\n", `\n`)
}

func unescapeEntry(s string) string {
	s = strings.ReplaceAll(s, `\n`, "\n")
	return strings.ReplaceAll(s, `\\`, `\`)
}

// Last returns the most recent entries, oldest first.
func (h *History) Last(n int) []string {
	if n <= 0 || n > len(h.Entries) {
		n = len(h.Entries)
	}
	out := make([]string, n)
	copy(out, h.Entries[len(h.Entries)-n:])
	return out
}
