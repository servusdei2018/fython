package repl

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/servusdei2018/fython/pkg/compiler"
)

// execStartedMsg asks the model to release the terminal and run the
// statement to completion.
type execStartedMsg struct {
	src string
}

// execDoneMsg carries the finished execution back to the model.
type execDoneMsg struct {
	src string
	res Result
}

// replModel is the BubbleTea model for the interactive TUI.
type replModel struct {
	cfg    Config
	worker *Worker
	prog   *tea.Program

	buf     string
	cursor  int // byte offset into buf
	busy    bool
	paste   bool
	width   int
	hist    *History
	histIdx int // -1 = editing current buffer
	saved   string

	completions     []Candidate
	compIdx         int
	showCompletions bool
	compStart       int // byte offset where the completed prefix starts

	searchMode  bool
	searchQuery string
	searchIdx   int

	showHistory bool
}

// newReplModel prepares the TUI model for the given configuration.
func newReplModel(cfg Config, worker *Worker, hist *History) *replModel {
	return &replModel{
		cfg:     cfg,
		worker:  worker,
		hist:    hist,
		histIdx: -1,
	}
}

func (m *replModel) Init() tea.Cmd { return nil }

// runRich starts the inline BubbleTea program.
func runRich(cfg Config) error {
	cfg = cfg.withDefaults()
	worker := NewWorker(cfg.VM, cfg.Optimize)
	defer worker.Close()
	hist := LoadHistory()
	defer hist.Save()

	m := newReplModel(cfg, worker, hist)
	p := tea.NewProgram(m)
	m.prog = p

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)
	defer signal.Stop(sigCh)
	go func() {
		for range sigCh {
			p.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
		}
	}()

	_, err := p.Run()
	if err != nil {
		return err
	}
	return nil
}

// Update implements tea.Model.
func (m *replModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case execStartedMsg:
		// Release the terminal so interactive input() calls inside
		// executed code can read from the user directly.
		var releaseCmd tea.Cmd
		if m.prog != nil {
			_ = m.prog.ReleaseTerminal()
		}
		execCmd := tea.Cmd(func() tea.Msg {
			res := m.worker.Execute(msg.src, "<stdin>")
			return execDoneMsg{src: msg.src, res: res}
		})
		if releaseCmd != nil {
			return m, tea.Batch(releaseCmd, execCmd)
		}
		return m, execCmd

	case execDoneMsg:
		if m.prog != nil {
			_ = m.prog.RestoreTerminal()
		}
		m.busy = false
		var cmds []tea.Cmd
		echo := msg.src + "\n\n"
		if msg.res.Output != "" {
			echo += msg.res.Output
		}
		if msg.res.Traceback != "" {
			echo += msg.res.Traceback
		}
		cmds = append(cmds, tea.Println(echo))
		if msg.res.Exit != nil {
			m.hist.Save()
			cmds = append(cmds, tea.Quit)
		}
		if len(cmds) == 0 {
			return m, nil
		}
		return m, tea.Batch(cmds...)
	}

	return m, nil
}

func (m *replModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.searchMode {
		return m.handleSearchKey(msg)
	}
	if m.showHistory {
		if msg.Type == tea.KeyF2 || msg.Type == tea.KeyEsc {
			m.showHistory = false
			return m, nil
		}
		if msg.Type == tea.KeyEnter {
			m.showHistory = false
			model, cmd := m.recallAt(msg)
			return model, cmd
		}
	}

	// Bracketed paste auto-enters paste mode.
	if msg.Paste {
		m.paste = true
		text := strings.TrimSuffix(string(msg.Runes), "\n")
		m.insertText(text)
		return m, nil
	}

	switch msg.Type {
	case tea.KeyCtrlC:
		if m.busy {
			m.worker.RequestInterrupt()
			return m, nil
		}
		m.buf = ""
		m.cursor = 0
		m.completions = nil
		m.showCompletions = false
		return m, tea.Println("KeyboardInterrupt")

	case tea.KeyCtrlD:
		if m.paste {
			m.paste = false
			model, cmd := m.submit()
			return model, cmd
		}
		if m.buf == "" {
			m.hist.Save()
			return m, tea.Quit
		}
		if m.cursor < len(m.buf) {
			m.buf = m.buf[:m.cursor] + m.buf[m.cursor+1:]
		}
		return m, nil

	case tea.KeyEnter:
		if m.paste {
			m.insertText("\n")
			return m, nil
		}
		if m.showCompletions && len(m.completions) > 0 {
			model, cmd := m.applyCompletion(m.completions[m.compIdx])
			return model, cmd
		}
		model, cmd := m.submit()
		return model, cmd

	case tea.KeyTab:
		if m.busy {
			return m, nil
		}
		model, cmd := m.requestCompletions()
		return model, cmd

	case tea.KeyUp:
		if m.busy {
			return m, nil
		}
		model, cmd := m.historyUp()
		return model, cmd

	case tea.KeyDown:
		if m.busy {
			return m, nil
		}
		model, cmd := m.historyDown()
		return model, cmd

	case tea.KeyCtrlR:
		if m.busy {
			return m, nil
		}
		m.searchMode = true
		m.searchQuery = ""
		m.searchIdx = len(m.hist.Entries) - 1
		return m, nil

	case tea.KeyF2:
		m.showHistory = !m.showHistory
		return m, nil

	case tea.KeyF3:
		m.paste = !m.paste
		if !m.paste {
			model, cmd := m.submit()
			return model, cmd
		}
		return m, nil

	case tea.KeyBackspace:
		if m.cursor > 0 {
			m.buf = m.buf[:m.cursor-1] + m.buf[m.cursor:]
			m.cursor--
			m.showCompletions = false
		}
		return m, nil

	case tea.KeyDelete:
		if m.cursor < len(m.buf) {
			m.buf = m.buf[:m.cursor] + m.buf[m.cursor+1:]
		}
		return m, nil

	case tea.KeyLeft:
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil

	case tea.KeyRight:
		if m.cursor < len(m.buf) {
			m.cursor++
		}
		return m, nil

	case tea.KeyHome:
		m.cursor = lineStart(m.buf, m.cursor)
		return m, nil

	case tea.KeyEnd:
		m.cursor = lineEnd(m.buf, m.cursor)
		return m, nil
	}

	// Printable runes.
	if len(msg.Runes) > 0 && !msg.Alt {
		m.insertText(string(msg.Runes))
		return m, nil
	}
	return m, nil
}

// submit buffers the current input: incomplete input continues the block,
// bare commands act immediately, complete statements run on the worker.
func (m *replModel) submit() (tea.Model, tea.Cmd) {
	if m.buf == "" {
		return m, nil
	}
	if bare := bareCommand(m.buf); bare != "" {
		switch bare {
		case "exit", "quit":
			m.hist.Save()
			return m, tea.Quit
		case "clear":
			m.buf = ""
			m.cursor = 0
			return m, tea.ClearScreen
		}
	}
	if compiler.IncompleteInput([]byte(m.buf)) {
		// Continue the multiline block with fresh indentation.
		lastLine := m.buf[strings.LastIndex(m.buf, "\n")+1:]
		indent := lastLine[:len(lastLine)-len(strings.TrimLeft(lastLine, " \t"))]
		m.buf += "\n"
		if strings.HasSuffix(strings.TrimRight(lastLine, " \t"), ":") {
			indent += "    "
		}
		m.buf += indent
		m.cursor = len(m.buf)
		return m, nil
	}
	src := m.buf
	m.hist.Add(src)
	m.buf = ""
	m.cursor = 0
	m.histIdx = -1
	m.completions = nil
	m.showCompletions = false
	m.busy = true
	return m, func() tea.Msg { return execStartedMsg{src: src} }
}

func (m *replModel) insertText(text string) {
	m.buf = m.buf[:m.cursor] + text + m.buf[m.cursor:]
	m.cursor += len(text)
	m.showCompletions = false
}

// recallAt loads the history entry under the current search selection.
func (m *replModel) recallAt(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if len(m.hist.Entries) == 0 {
		return m, nil
	}
	idx := m.searchIdx
	if idx < 0 || idx >= len(m.hist.Entries) {
		idx = len(m.hist.Entries) - 1
	}
	m.buf = m.hist.Entries[idx]
	m.cursor = len(m.buf)
	m.searchMode = false
	return m, nil
}

// historyUp moves the cursor up one visual line, then walks history.
func (m *replModel) historyUp() (tea.Model, tea.Cmd) {
	if lineStart(m.buf, m.cursor) > 0 {
		m.cursor = lineStart(m.buf, m.cursor-1)
		return m, nil
	}
	if len(m.hist.Entries) == 0 {
		return m, nil
	}
	if m.histIdx == -1 {
		m.saved = m.buf
		m.histIdx = len(m.hist.Entries) - 1
	} else if m.histIdx > 0 {
		m.histIdx--
	}
	m.buf = m.hist.Entries[m.histIdx]
	m.cursor = len(m.buf)
	return m, nil
}

// historyDown moves the cursor down one visual line, then walks history.
func (m *replModel) historyDown() (tea.Model, tea.Cmd) {
	if lineEnd(m.buf, m.cursor) < len(m.buf) {
		m.cursor = lineEnd(m.buf, m.cursor+1)
		return m, nil
	}
	if m.histIdx == -1 {
		return m, nil
	}
	if m.histIdx < len(m.hist.Entries)-1 {
		m.histIdx++
		m.buf = m.hist.Entries[m.histIdx]
	} else {
		m.histIdx = -1
		m.buf = m.saved
	}
	m.cursor = len(m.buf)
	return m, nil
}

// requestCompletions asks the worker for candidates at the cursor.
func (m *replModel) requestCompletions() (tea.Model, tea.Cmd) {
	cands := m.worker.Complete(m.buf, m.cursor)
	if len(cands) == 0 {
		return m, nil
	}
	// Find where the current prefix starts.
	start := m.cursor
	for start > 0 {
		c := m.buf[start-1]
		if c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' {
			start--
		} else {
			break
		}
	}
	m.compStart = start
	m.completions = cands
	m.compIdx = 0
	m.showCompletions = len(cands) > 1
	model, cmd := m.applyCompletion(cands[0])
	return model, cmd
}

// applyCompletion inserts a candidate replacing the current prefix span.
func (m *replModel) applyCompletion(cand Candidate) (tea.Model, tea.Cmd) {
	text := cand.Text
	if cand.Callable {
		text += "("
	}
	m.buf = m.buf[:m.compStart] + text + m.buf[m.cursor:]
	m.cursor = m.compStart + len(text)
	m.compIdx++
	return m, nil
}

func (m *replModel) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.searchMode = false
		return m, nil
	case tea.KeyEnter:
		return m.recallAt(msg)
	case tea.KeyUp:
		if m.searchIdx > 0 {
			m.searchIdx--
		}
		return m, nil
	case tea.KeyDown:
		if m.searchIdx < len(m.hist.Entries)-1 {
			m.searchIdx++
		}
		return m, nil
	case tea.KeyBackspace:
		if len(m.searchQuery) > 0 {
			m.searchQuery = m.searchQuery[:len(m.searchQuery)-1]
		}
		return m, nil
	}
	if len(msg.Runes) > 0 && !msg.Alt {
		m.searchQuery += string(msg.Runes)
		m.searchIdx = searchFrom(m.hist.Entries, m.searchQuery, m.searchIdx)
	}
	return m, nil
}

func searchFrom(entries []string, query string, from int) int {
	if query == "" {
		return from
	}
	for i := from; i >= 0; i-- {
		if strings.Contains(entries[i], query) {
			return i
		}
	}
	for i := len(entries) - 1; i > from; i-- {
		if strings.Contains(entries[i], query) {
			return i
		}
	}
	return from
}

func lineStart(s string, pos int) int {
	i := strings.LastIndex(s[:pos], "\n")
	if i < 0 {
		return 0
	}
	return i + 1
}

func lineEnd(s string, pos int) int {
	i := strings.Index(s[pos:], "\n")
	if i < 0 {
		return len(s)
	}
	return pos + i
}

// View renders the prompt, highlighted buffer, and auxiliary panels.
func (m *replModel) View() string {
	var sb strings.Builder
	if m.paste {
		sb.WriteString("(paste) ")
	}
	highlighted := Highlight(m.buf, !highlightDisabled())
	lines := strings.Split(m.buf, "\n")
	prompt1 := psString(len(lines) > 1 && m.cursor > strings.Index(m.buf, "\n"))
	_ = prompt1
	sb.WriteString(renderBuffer(m.buf, highlighted))
	sb.WriteString("\n")
	if m.showCompletions && len(m.completions) > 0 {
		sb.WriteString(renderCompletions(m.completions, m.compIdx, m.width))
	}
	if m.searchMode {
		_, _ = fmt.Fprintf(&sb, "(reverse-i-search)%s: %s\n", m.searchQuery, currentSearchEntry(m.hist, m.searchIdx))
	}
	if m.showHistory {
		sb.WriteString(renderHistory(m.hist, m.histIdx, m.width))
	}
	if m.width > 0 {
		bar := strings.Repeat("─", min(m.width, 120))
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(bar) + "\n")
	}
	return sb.String()
}

func renderBuffer(buf, highlighted string) string {
	lines := strings.Split(buf, "\n")
	hlLines := strings.Split(highlighted, "\n")
	var sb strings.Builder
	for i := range lines {
		if i > 0 {
			sb.WriteString("\n")
		}
		if i == 0 {
			sb.WriteString(">>> ")
		} else {
			sb.WriteString("... ")
		}
		if i < len(hlLines) {
			sb.WriteString(hlLines[i])
		}
	}
	return sb.String()
}

func renderCompletions(cands []Candidate, selected, width int) string {
	if width <= 0 {
		width = 80
	}
	// Pack candidates into columns that fit the width.
	const spacing = 2
	rows := [][]string{}
	var cur []string
	curWidth := 0
	_ = spacing
	for _, c := range cands {
		text := c.Text
		if c.Callable {
			text += "("
		}
		w := len(text)
		if len(cur) > 0 && curWidth+w+spacing > width {
			rows = append(rows, cur)
			cur = nil
			curWidth = 0
		}
		cur = append(cur, text)
		curWidth += w + spacing
	}
	if len(cur) > 0 {
		rows = append(rows, cur)
	}
	var sb strings.Builder
	for _, row := range rows {
		sb.WriteString(strings.Join(row, "  ") + "\n")
	}
	return sb.String()
}

func renderHistory(hist *History, selected, width int) string {
	var sb strings.Builder
	sb.WriteString("History (F2 to close, Enter to recall):\n")
	entries := hist.Entries
	start := 0
	if len(entries) > 20 {
		start = len(entries) - 20
	}
	for i := start; i < len(entries); i++ {
		prefix := "  "
		if i == selected {
			prefix = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Render("> ")
		}
		sb.WriteString(prefix + strings.ReplaceAll(entries[i], "\n", "⏎") + "\n")
	}
	return sb.String()
}

func currentSearchEntry(hist *History, idx int) string {
	if len(hist.Entries) == 0 || idx < 0 || idx >= len(hist.Entries) {
		return ""
	}
	return hist.Entries[idx]
}
