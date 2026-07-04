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
	comp        string
	statusFn    StatusFn
	agents      []Agent
	statuses    map[string]string // windowID → working|done|idle, refreshed with each poll
	tab         int               // active tab (index into tabs)
	cursor      int               // index into filtered() — the active tab's items
	width       int
	height      int
	focused     bool // pane focus (tmux focus-events): the click that focuses us must not also select
	swallowNext bool // the next click is the one that focused the pane — ignore exactly one
}

// Watch runs the list TUI until the user quits (q / ^c). It is the body of
// the hidden `duck panel watch` command.
func Watch(run Runner, outer string, status StatusFn) error {
	m := watchModel{run: run, outer: outer, comp: Companion(outer), statusFn: status}
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
	agents, err := Agents(m.run, m.comp)
	statuses := map[string]string{}
	if m.statusFn != nil {
		for _, a := range agents {
			statuses[a.WindowID] = m.statusFn(a.WindowID)
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
	for ti, kind := range m.tabs() {
		n := 0
		for _, a := range m.agents {
			if a.Kind == kind {
				n++
			}
		}
		label := fmt.Sprintf(" %s %d ", kind, n)
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
				_ = Select(m.run, m.agents[idx[row]].WindowID)
				return m, m.load
			}
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			// Close the whole panel: the viewport pane too, then quit (our own
			// pane dies with the process).
			_ = Close(m.run, m.outer)
			return m, tea.Quit
		case "tab", "right", "l":
			m.tab, m.cursor = (m.tab+1)%len(m.tabs()), 0
		case "shift+tab", "left", "h":
			n := len(m.tabs())
			m.tab, m.cursor = (m.tab+n-1)%n, 0
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.filtered())-1 {
				m.cursor++
			}
		case "enter", " ":
			if idx := m.filtered(); m.cursor < len(idx) {
				_ = Select(m.run, m.agents[idx[m.cursor]].WindowID)
				return m, m.load
			}
		case "x":
			if idx := m.filtered(); m.cursor < len(idx) {
				_ = Kill(m.run, m.agents[idx[m.cursor]].WindowID)
				return m, m.load
			}
		case "s":
			// Quick shell agent in the outer session's dir.
			if dir, err := CurrentPanePath(m.run); err == nil {
				_, _ = Spawn(m.run, m.comp, "shell", dir, "", KindShell)
			}
			return m, m.load
		}
	}
	return m, nil
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
		line := marker + statusGlyph(m.statuses[a.WindowID]) + " " + label
		if row == m.cursor {
			line = marker + statusGlyph(m.statuses[a.WindowID]) + " " + selectedStyle.Render(" "+a.Name+" ")
		}
		b.WriteString(truncate(line, m.width) + "\n")
	}
	help := dimStyle.Render(" ⇥ tab · ↵ view · x kill · s shell · q close")
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
