// watch.go is the Bubble Tea program that runs INSIDE the panel's list pane
// (`duck panel watch <outer>`): a live, clickable list of the launched agents.
// It polls the companion session every 2s (tmux is the source of truth —
// nothing stored) and drives selection with select-window, which the nested
// viewport client follows.
package panel

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/DigiBugCat/duck/internal/paths"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// pollEvery is the agent-list refresh cadence. Cheap: one local
// `tmux list-windows` per tick.
const pollEvery = 2 * time.Second

type tickMsg time.Time
type agentsMsg struct {
	agents   []Agent
	statuses map[string]string
	err      error
}

// StatusFn classifies an agent window's state for the roster ("working",
// "done", "idle"). Injected by the command layer (internal/channel implements
// it over codex rollouts) so panel stays free of the channel dependency.
type StatusFn func(windowID string) string

// watchModel is the list-pane TUI state.
type watchModel struct {
	run         Runner
	outer       string
	statusFn    StatusFn
	agents      []Agent
	statuses    map[string]string // windowID → working|done|idle, refreshed with each poll
	tab         int               // active tab (index into tabs)
	cursor      int               // index into filtered() — the active tab's items
	width       int
	height      int
	focused     bool   // pane focus (tmux focus-events): the click that focuses us must not also select
	swallowNext bool   // the next click is the one that focused the pane — ignore exactly one
	armedKill   string // windowID armed for kill: x arms, second x confirms (accidental-kill guard)
	cmdMode     bool   // the : palette (the roster is the minibuffer)
	cmdInput    string
	cmdErr      string // last palette error, shown until the next keypress
}

// Watch runs the list TUI until the user quits (q / ^c). It is the body of
// the hidden `duck panel watch` command.
func Watch(run Runner, outer string, status StatusFn) error {
	m := watchModel{run: run, outer: outer, statusFn: status}
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithReportFocus()).Run()
	return err
}

func (m watchModel) Init() tea.Cmd {
	return tea.Batch(m.load, tick())
}

func tick() tea.Cmd {
	return tea.Tick(pollEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m watchModel) load() tea.Msg {
	agents, err := Agents(m.run, m.outer)
	statuses := map[string]string{}
	if m.statusFn != nil {
		for _, a := range agents {
			statuses[a.PaneID] = m.statusFn(a.PaneID)
		}
	}
	return agentsMsg{agents: agents, statuses: statuses, err: err}
}

// headerLines is the number of rendered lines above the first roster row
// (the tab bar and the rule) — the contract between View and the mouse-click
// row mapping.
const headerLines = 2

// tabs returns the tab bar contents: the base set (always shown, stable
// order) plus any OTHER kind currently carried by a window, sorted — tabs
// appear and disappear with their windows, no declaration anywhere.
func (m watchModel) tabs() []string {
	out := append([]string{}, BaseKinds...)
	base := map[string]bool{}
	for _, k := range BaseKinds {
		base[k] = true
	}
	extra := map[string]bool{}
	for _, a := range m.agents {
		if !base[a.Kind] && !extra[a.Kind] {
			extra[a.Kind] = true
			out = append(out, a.Kind)
		}
	}
	sort.Strings(out[len(BaseKinds):])
	return out
}

// activeKind is the kind (tab name) the active tab shows, clamped in case a
// dynamic tab vanished out from under the cursor.
func (m watchModel) activeKind() string {
	t := m.tabs()
	if m.tab >= len(t) {
		return t[len(t)-1]
	}
	return t[m.tab]
}

// filtered returns the indexes into m.agents belonging to the ACTIVE tab.
func (m watchModel) filtered() []int {
	kind := m.activeKind()
	var idx []int
	for i, a := range m.agents {
		if a.Kind == kind {
			idx = append(idx, i)
		}
	}
	return idx
}

// tabSpan is one rendered tab's clickable x-range on the bar line.
type tabSpan struct{ start, end, tab int }

// renderTabs draws the tab bar and returns it with the click spans. The same
// function feeds View and the mouse handler so a click can never land on the
// wrong tab.
func (m watchModel) renderTabs() (string, []tabSpan) {
	var b strings.Builder
	var spans []tabSpan
	x := 0
	activeKind := ""
	for _, a := range m.agents {
		if a.Active {
			activeKind = a.Kind
		}
	}
	for ti, kind := range m.tabs() {
		n := 0
		for _, a := range m.agents {
			if a.Kind == kind {
				n++
			}
		}
		// ▶ on the tab whose window the viewport is SHOWING right now — the
		// browse position (highlight) and the shown item live on different
		// tabs often enough that both need a signal.
		view := " "
		if kind == activeKind {
			view = "▶"
		}
		label := fmt.Sprintf("%s%s %d ", view, kind, n)
		cell := tabStyle.Render(label)
		if ti == m.tab {
			cell = tabActiveStyle.Render(label)
		}
		w := lipgloss.Width(cell)
		spans = append(spans, tabSpan{start: x, end: x + w, tab: ti})
		b.WriteString(cell)
		x += w
		b.WriteString(" ")
		x++
	}
	return b.String(), spans
}

func (m watchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		return m, tea.Batch(m.load, tick())
	case agentsMsg:
		// A dead companion (last agent closed) is not an error state — render an
		// empty list; spawn recreates everything.
		if msg.err != nil {
			m.agents = nil
		} else {
			m.agents = msg.agents
		}
		m.statuses = msg.statuses
		if n := len(m.tabs()); m.tab >= n {
			m.tab = n - 1
		}
		if n := len(m.filtered()); m.cursor >= n {
			m.cursor = max(0, n-1)
		}
	case tea.FocusMsg:
		// tmux delivers focus-in BEFORE the click that caused it, so arm a
		// one-shot swallow for that click; a second click is a real selection.
		m.focused = true
		m.swallowNext = true
	case tea.BlurMsg:
		m.focused = false
	case tea.MouseMsg:
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			// The click that brings the pane into focus is just that — a focus
			// click; only a click on an already-focused pane selects a row.
			if !m.focused || m.swallowNext {
				m.focused, m.swallowNext = true, false
				return m, nil
			}
			// Tab bar click: switch tabs.
			if msg.Y == 0 {
				_, spans := m.renderTabs()
				for _, sp := range spans {
					if msg.X >= sp.start && msg.X < sp.end {
						m.tab, m.cursor = sp.tab, 0
						return m, nil
					}
				}
				return m, nil
			}
			// Row click: select that item in the viewport.
			idx := m.filtered()
			row := msg.Y - headerLines
			if row >= 0 && row < len(idx) {
				m.cursor = row
				wid := m.agents[idx[row]].PaneID
				// Swap FIRST so a stale preview respawns at slot geometry, not
				// at the parked pane's tiny tiled size.
				_ = Select(m.run, m.outer, wid)
				RefreshIfStale(m.run, wid, FileMtime)
				return m, m.load
			}
		}
	case tea.KeyMsg:
		if m.cmdMode {
			switch msg.String() {
			case "esc", "ctrl+c":
				m.cmdMode, m.cmdInput = false, ""
			case "enter":
				m.cmdErr = m.runPalette(strings.TrimSpace(m.cmdInput))
				m.cmdMode, m.cmdInput = false, ""
				return m, m.load
			case "backspace":
				if len(m.cmdInput) > 0 {
					r := []rune(m.cmdInput)
					m.cmdInput = string(r[:len(r)-1])
				}
			default:
				if msg.Type == tea.KeyRunes || msg.String() == " " {
					m.cmdInput += string(msg.Runes)
					if msg.String() == " " && len(msg.Runes) == 0 {
						m.cmdInput += " "
					}
				}
			}
			return m, nil
		}
		m.cmdErr = ""
		switch msg.String() {
		case ":":
			m.cmdMode, m.cmdInput = true, ""
		case "q", "ctrl+c":
			// Close the whole panel: the viewport pane too, then quit (our own
			// pane dies with the process).
			_ = Close(m.run, m.outer)
			return m, tea.Quit
		case "tab", "right", "l":
			m.tab, m.cursor, m.armedKill = (m.tab+1)%len(m.tabs()), 0, ""
		case "shift+tab", "left", "h":
			n := len(m.tabs())
			m.tab, m.cursor, m.armedKill = (m.tab+n-1)%n, 0, ""
		case "up", "k":
			m.armedKill = ""
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			m.armedKill = ""
			if m.cursor < len(m.filtered())-1 {
				m.cursor++
			}
		case "enter", " ":
			if idx := m.filtered(); m.cursor < len(idx) {
				wid := m.agents[idx[m.cursor]].PaneID
				_ = Select(m.run, m.outer, wid)
				RefreshIfStale(m.run, wid, FileMtime)
				return m, m.load
			}
		case "x":
			// Two-press guard: killing an agent kills its process — too easy to
			// fat-finger. First x arms (footer shows it), second x on the same
			// item confirms; moving the cursor or switching tabs disarms.
			if idx := m.filtered(); m.cursor < len(idx) {
				wid := m.agents[idx[m.cursor]].PaneID
				if m.armedKill == wid {
					m.armedKill = ""
					wasActive := m.agents[idx[m.cursor]].Active
					_ = Kill(m.run, wid)
					if wasActive {
						EnsureSlot(m.run, m.outer) // killed on display — heal the slot
					}
					return m, m.load
				}
				m.armedKill = wid
			}
		case "e":
			// Jump to the workspace scratch buffer (create if missing).
			EnsureScratch(m.run, m.outer)
			for _, a := range m.agents {
				if a.Kind == KindBuffer && a.Name == "scratch" {
					_ = Select(m.run, m.outer, a.PaneID)
					break
				}
			}
			return m, m.load
		case "s":
			// Quick shell agent in the outer session's dir.
			if dir, err := CurrentPanePath(m.run); err == nil {
				EnsureSlot(m.run, m.outer)
				_, _ = Spawn(m.run, m.outer, "shell", dir, "", KindShell)
			}
			return m, m.load
		}
	}
	return m, nil
}

// runPalette executes one palette line. Grammar (v1, deliberately tiny):
//
//	<bare words>        fuzzy-jump to the agent/buffer whose name matches
//	s|spawn <cmd...>    spawn an agent running cmd
//	e|edit [name]       open a pad (workspace pad when no name)
//	k|kill <name>       kill by roster name (no confirm — you typed it)
//
// Returns "" on success or a short error for the footer.
func (m *watchModel) runPalette(line string) string {
	if line == "" {
		return ""
	}
	f := strings.Fields(line)
	switch f[0] {
	case "s", "spawn":
		if len(f) < 2 {
			return "spawn what?"
		}
		dir, err := CurrentPanePath(m.run)
		if err != nil {
			return err.Error()
		}
		name := f[1]
		if i := strings.LastIndex(f[1], "/"); i >= 0 {
			name = f[1][i+1:]
		}
		if _, err := Spawn(m.run, m.outer, name, dir, strings.Join(f[1:], " "), KindAgent); err != nil {
			return err.Error()
		}
		return ""
	case "e", "edit":
		name := m.outer
		if len(f) > 1 {
			name = f[1]
		}
		path, err := PadPath(name)
		if err != nil {
			return err.Error()
		}
		dir, _ := CurrentPanePath(m.run)
		for _, a := range m.agents {
			if a.Kind == KindBuffer && a.Name == name {
				_ = Select(m.run, m.outer, a.PaneID)
				return ""
			}
		}
		cmd := "sh -c 'while :; do \"${EDITOR:-vim}\" " + paths.Quote(path) + "; sleep 0.3; done'"
		if _, err := Spawn(m.run, m.outer, name, dir, cmd, KindBuffer); err != nil {
			return err.Error()
		}
		return ""
	case "k", "kill":
		if len(f) < 2 {
			return "kill what?"
		}
		if a := m.fuzzyAgent(strings.Join(f[1:], " ")); a != nil {
			_ = Kill(m.run, a.PaneID)
			return ""
		}
		return "no match: " + f[1]
	}
	// Bare words: switch-to-buffer.
	if a := m.fuzzyAgent(line); a != nil {
		_ = Select(m.run, m.outer, a.PaneID)
		return ""
	}
	return "no match: " + line
}

// fuzzyAgent finds the best roster match: exact name, then prefix, then
// subsequence — first hit in roster order wins each tier.
func (m *watchModel) fuzzyAgent(q string) *Agent {
	q = strings.ToLower(q)
	var prefix, subseq *Agent
	for i := range m.agents {
		a := &m.agents[i]
		n := strings.ToLower(a.Name)
		if n == q {
			return a
		}
		if prefix == nil && strings.HasPrefix(n, q) {
			prefix = a
		}
		if subseq == nil && isSubseq(n, q) {
			subseq = a
		}
	}
	if prefix != nil {
		return prefix
	}
	return subseq
}

func isSubseq(hay, needle string) bool {
	i := 0
	for _, c := range hay {
		if i < len(needle) && byte(c) == needle[i] {
			i++
		}
	}
	return i == len(needle)
}

var (
	titleStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11"))
	dimStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	selectedStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("11"))
	activeStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	workingStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))  // yellow: turn in flight
	doneStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // green: finished, result waiting
	tabStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	tabActiveStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("6"))
)

// statusGlyph renders the notification dot for one agent state.
func statusGlyph(status string) string {
	switch status {
	case "working":
		return workingStyle.Render("●")
	case "done":
		return doneStyle.Render("✔")
	default:
		return dimStyle.Render("○")
	}
}

func (m watchModel) View() string {
	var b strings.Builder
	working, done := 0, 0
	for _, s := range m.statuses {
		switch s {
		case "working":
			working++
		case "done":
			done++
		}
	}
	bar, _ := m.renderTabs()
	if working > 0 {
		bar += workingStyle.Render(fmt.Sprintf(" ●%d", working))
	}
	if done > 0 {
		bar += doneStyle.Render(fmt.Sprintf(" ✔%d", done))
	}
	b.WriteString(truncate(bar, m.width) + "\n")
	b.WriteString(dimStyle.Render(strings.Repeat("─", max(1, m.width))) + "\n")
	idx := m.filtered()
	if len(idx) == 0 {
		switch m.activeKind() {
		case KindArtifact:
			b.WriteString(dimStyle.Render("\n  no artifacts\n\n  duck preview <file|url>"))
		case KindShell:
			b.WriteString(dimStyle.Render("\n  no shells\n\n  press s for one"))
		default:
			b.WriteString(dimStyle.Render("\n  nothing here\n\n  duck spawn <cmd>"))
		}
	}
	for row, i := range idx {
		a := m.agents[i]
		marker := "  "
		if a.Active {
			marker = activeStyle.Render("▶ ")
		}
		label := a.Name
		if a.Command != "" && !strings.EqualFold(a.Command, a.Name) {
			label += dimStyle.Render(" · " + a.Command)
		}
		line := marker + statusGlyph(m.statuses[a.PaneID]) + " " + label
		if row == m.cursor {
			line = marker + statusGlyph(m.statuses[a.PaneID]) + " " + selectedStyle.Render(" "+a.Name+" ")
		}
		b.WriteString(truncate(line, m.width) + "\n")
	}
	viewing := ""
	for _, a := range m.agents {
		if a.Active {
			viewing = a.Name
		}
	}
	help := dimStyle.Render(" : cmd · ⇥ tab · ↵ view · e scratch · x kill · s shell · q close")
	if viewing != "" {
		help = activeStyle.Render(" ▶ viewing "+viewing) + dimStyle.Render(" · ⇥ tab · ↵ view · x kill · q close")
	}
	if m.cmdMode {
		help = titleStyle.Render(" :") + m.cmdInput + selectedStyle.Render(" ")
	} else if m.cmdErr != "" {
		help = workingStyle.Render(" " + m.cmdErr)
	}
	if m.armedKill != "" {
		for _, a := range m.agents {
			if a.PaneID == m.armedKill {
				help = workingStyle.Render(" x again to kill " + a.Name + " ")
			}
		}
	}
	// Pin help to the bottom when we know the height.
	body := b.String()
	if m.height > 0 {
		lines := strings.Count(body, "\n") + 1
		for i := lines; i < m.height-1; i++ {
			body += "\n"
		}
	}
	return body + help
}

// truncate clips a rendered line to w columns (rune-naive is fine for labels).
func truncate(s string, w int) string {
	if w <= 0 || lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	if len(r) > w {
		return string(r[:w])
	}
	return s
}
