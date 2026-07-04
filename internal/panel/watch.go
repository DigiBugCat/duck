// watch.go is the Bubble Tea program that runs INSIDE the panel's list pane
// (`duck panel watch <outer>`): a live, clickable list of the launched agents.
// It polls the companion session every 2s (tmux is the source of truth —
// nothing stored) and drives selection with select-window, which the nested
// viewport client follows.
package panel

import (
	"fmt"
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
	cursor      int
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

// headerLines is the number of rendered lines above the first roster row —
// the contract between View and the mouse-click row mapping.
const headerLines = 2

// rowRef is one rendered roster line: a section header (agentIdx == -1) or
// an entry pointing into m.agents. View and the mouse/cursor mapping share
// this so a click can never land on the wrong item.
type rowRef struct {
	section  string // non-empty → section header line
	agentIdx int    // index into m.agents; -1 for headers
}

// rows lays out the roster: agents section first, then artifacts — each with
// a header line, omitted when the section is empty.
func (m watchModel) rows() []rowRef {
	var out []rowRef
	appendKind := func(section, kind string) {
		first := true
		for i, a := range m.agents {
			if a.Kind != kind {
				continue
			}
			if first {
				out = append(out, rowRef{section: section, agentIdx: -1})
				first = false
			}
			out = append(out, rowRef{agentIdx: i})
		}
	}
	appendKind("agents", KindAgent)
	appendKind("artifacts", KindArtifact)
	return out
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
		if m.cursor >= len(m.agents) {
			m.cursor = max(0, len(m.agents)-1)
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
			rows := m.rows()
			row := msg.Y - headerLines
			if row >= 0 && row < len(rows) && rows[row].agentIdx >= 0 {
				m.cursor = rows[row].agentIdx
				_ = Select(m.run, m.agents[m.cursor].WindowID)
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
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.agents)-1 {
				m.cursor++
			}
		case "enter", " ":
			if m.cursor < len(m.agents) {
				_ = Select(m.run, m.agents[m.cursor].WindowID)
				return m, m.load
			}
		case "x":
			if m.cursor < len(m.agents) {
				_ = Kill(m.run, m.agents[m.cursor].WindowID)
				return m, m.load
			}
		case "s":
			// Quick shell agent in the outer session's dir.
			if dir, err := CurrentPanePath(m.run); err == nil {
				_, _ = Spawn(m.run, m.comp, "shell", dir, "", KindAgent)
			}
			return m, m.load
		}
	}
	return m, nil
}

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("11"))
	activeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	workingStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))  // yellow: turn in flight
	doneStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // green: finished, result waiting
	sectionStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
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
	head := titleStyle.Render(" duck ") + dimStyle.Render(fmt.Sprintf("(%d)", len(m.agents)))
	if working > 0 {
		head += workingStyle.Render(fmt.Sprintf("  ● %d working", working))
	}
	if done > 0 {
		head += doneStyle.Render(fmt.Sprintf("  ✔ %d done", done))
	}
	b.WriteString(head + "\n")
	b.WriteString(dimStyle.Render(strings.Repeat("─", max(1, m.width))) + "\n")
	if len(m.agents) == 0 {
		b.WriteString(dimStyle.Render("\n  nothing running\n\n  duck spawn <cmd>\n  duck preview <file>\n  or press s for a shell"))
	}
	for _, r := range m.rows() {
		if r.agentIdx < 0 {
			b.WriteString(sectionStyle.Render(" "+r.section) + "\n")
			continue
		}
		a := m.agents[r.agentIdx]
		marker := "  "
		if a.Active {
			marker = activeStyle.Render("▶ ")
		}
		label := a.Name
		if a.Command != "" && !strings.EqualFold(a.Command, a.Name) {
			label += dimStyle.Render(" · " + a.Command)
		}
		line := marker + statusGlyph(m.statuses[a.WindowID]) + " " + label
		if r.agentIdx == m.cursor {
			line = marker + statusGlyph(m.statuses[a.WindowID]) + " " + selectedStyle.Render(" "+a.Name+" ")
		}
		b.WriteString(truncate(line, m.width) + "\n")
	}
	help := dimStyle.Render(" ↵ view · x kill · s shell · q close")
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
