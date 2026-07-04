// watch.go is the Bubble Tea program that runs INSIDE the panel's roster
// pane (`duck panel watch <outer>`): the workspace's citizens as a list,
// with an ALWAYS-VISIBLE command box underneath — no hidden modes, no
// memorized chords. Typing filters and jumps (fuzzy, fzf-style); verbs
// (spawn/edit/preview/render/kill/close) complete with ghost text and a hint
// line that says what Enter will do. tmux stays the source of truth (2s
// polls); selection drives swap-pane through Select.
package panel

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/DigiBugCat/duck/internal/paths"
)

// pollEvery is the roster refresh cadence. Cheap: one local list-panes tick.
const pollEvery = 2 * time.Second

type tickMsg time.Time
type agentsMsg struct {
	agents   []Agent
	statuses map[string]string
	err      error
}

// StatusFn classifies an agent pane's state ("working", "done", "idle").
// Injected by the command layer (internal/channel implements it over codex
// rollouts) so panel stays free of the channel dependency.
type StatusFn func(paneID string) string

// verbs the command box understands, with the usage hint the suggestion
// line shows. Order = suggestion priority.
var verbs = []struct{ name, usage string }{
	{"spawn", "spawn <cmd…>  — launch an agent"},
	{"edit", "edit [pad]  — open/create a pad (no name: workspace pad)"},
	{"preview", "preview <file|url>  — render in the viewport"},
	{"render", "render <file|url>  — open in your laptop browser"},
	{"kill", "kill <name>  — kill an agent/buffer"},
	{"close", "close  — close this panel (everything keeps running)"},
}

type watchModel struct {
	run      Runner
	outer    string
	statusFn StatusFn

	agents   []Agent
	statuses map[string]string
	tab      int
	cursor   int // index into visible() rows
	width    int
	height   int

	input       textinput.Model
	lastMsg     string // result/error of the last command, shown until next keypress
	focused     bool
	swallowNext bool // the click that focuses the pane must not also select
}

// Watch runs the roster TUI until close/ctrl+c.
func Watch(run Runner, outer string, status StatusFn) error {
	ti := textinput.New()
	ti.Prompt = "❯ "
	ti.Placeholder = "type a name to jump · spawn <cmd> · edit <pad> · …"
	ti.Focus()
	m := watchModel{run: run, outer: outer, statusFn: status, input: ti}
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithReportFocus()).Run()
	return err
}

func (m watchModel) Init() tea.Cmd { return tea.Batch(m.load, tick(), textinput.Blink) }

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

// headerLines: tab bar + rule, above the first row (mouse-mapping contract).
const headerLines = 2

// tabs: base set always visible, dynamic kinds appended sorted.
func (m watchModel) tabs() []string {
	out := append([]string{}, BaseKinds...)
	base := map[string]bool{}
	for _, k := range BaseKinds {
		base[k] = true
	}
	seen := map[string]bool{}
	for _, a := range m.agents {
		if !base[a.Kind] && !seen[a.Kind] {
			seen[a.Kind] = true
			out = append(out, a.Kind)
		}
	}
	sort.Strings(out[len(BaseKinds):])
	return out
}

func (m watchModel) activeKind() string {
	t := m.tabs()
	if m.tab >= len(t) {
		return t[len(t)-1]
	}
	return t[m.tab]
}

// visible returns the rows on screen: the active tab's items, live-filtered
// by the command box whenever its text isn't a verb command.
func (m watchModel) visible() []int {
	kind := m.activeKind()
	q := strings.ToLower(strings.TrimSpace(m.input.Value()))
	if m.isVerb() {
		q = ""
	}
	var idx []int
	for i, a := range m.agents {
		if a.Kind != kind {
			continue
		}
		if q != "" && !isSubseq(strings.ToLower(a.Name), q) {
			continue
		}
		idx = append(idx, i)
	}
	return idx
}

// isVerb reports whether the box currently holds a verb command (vs a jump/
// filter query).
func (m watchModel) isVerb() bool {
	f := strings.Fields(m.input.Value())
	if len(f) == 0 {
		return false
	}
	for _, v := range verbs {
		if f[0] == v.name || (len(f) > 1 && strings.HasPrefix(v.name, f[0])) {
			return true
		}
	}
	return false
}

// suggest computes the ghost completion and the hint line for the current
// input. ghost is the REMAINDER to display after the typed text.
func (m watchModel) suggest() (ghost, hint string) {
	val := m.input.Value()
	f := strings.Fields(val)
	if len(f) == 0 {
		return "", ""
	}
	// Verb completion (first token only).
	if len(f) == 1 && !strings.HasSuffix(val, " ") {
		for _, v := range verbs {
			if strings.HasPrefix(v.name, f[0]) && v.name != f[0] {
				return v.name[len(f[0]):], v.usage
			}
		}
	}
	for _, v := range verbs {
		if f[0] == v.name {
			return "", v.usage
		}
	}
	// Jump: best fuzzy match across ALL agents (any tab).
	if a := m.fuzzyAgent(strings.ToLower(strings.TrimSpace(val))); a != nil {
		g := ""
		if strings.HasPrefix(strings.ToLower(a.Name), strings.ToLower(val)) {
			g = a.Name[len(val):]
		}
		return g, "↵ view " + a.Name + " (" + a.Kind + ")"
	}
	return "", "no match — ↵ does nothing"
}

func (m watchModel) fuzzyAgent(q string) *Agent {
	if q == "" {
		return nil
	}
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

// runInput executes the command box's content. Empty + a selected row →
// view it. Returns a footer message ("" for silent success, "__quit__" to
// exit).
func (m *watchModel) runInput() string {
	line := strings.TrimSpace(m.input.Value())
	if line == "" {
		if idx := m.visible(); m.cursor < len(idx) {
			_ = Select(m.run, m.outer, m.agents[idx[m.cursor]].PaneID)
		}
		return ""
	}
	f := strings.Fields(line)
	// Accept unambiguous verb prefixes ("sp cargo…" == spawn).
	verb := ""
	for _, v := range verbs {
		if f[0] == v.name {
			verb = v.name
			break
		}
	}
	if verb == "" {
		var hits []string
		for _, v := range verbs {
			if strings.HasPrefix(v.name, f[0]) {
				hits = append(hits, v.name)
			}
		}
		if len(hits) == 1 && len(f) > 1 {
			verb = hits[0]
		}
	}
	switch verb {
	case "spawn":
		if len(f) < 2 {
			return "spawn what?"
		}
		dir, err := CurrentPanePath(m.run)
		if err != nil {
			return err.Error()
		}
		name := f[1]
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if _, err := Spawn(m.run, m.outer, name, dir, strings.Join(f[1:], " "), KindAgent); err != nil {
			return err.Error()
		}
		return "spawned " + name
	case "edit":
		name := m.outer
		if len(f) > 1 {
			name = f[1]
		}
		for _, a := range m.agents {
			if a.Kind == KindBuffer && a.Name == name {
				_ = Select(m.run, m.outer, a.PaneID)
				return ""
			}
		}
		path, err := PadPath(name)
		if err != nil {
			return err.Error()
		}
		dir, _ := CurrentPanePath(m.run)
		cmd := `sh -c 'while :; do "${EDITOR:-vim}" ` + paths.Quote(path) + `; sleep 0.3; done'`
		if _, err := Spawn(m.run, m.outer, name, dir, cmd, KindBuffer); err != nil {
			return err.Error()
		}
		return "pad " + name
	case "preview", "render":
		if len(f) < 2 {
			return verb + " what?"
		}
		return m.duckExec(verb, f[1])
	case "kill":
		if len(f) < 2 {
			return "kill what?"
		}
		if a := m.fuzzyAgent(strings.ToLower(strings.Join(f[1:], " "))); a != nil {
			_ = Kill(m.run, a.PaneID)
			return "killed " + a.Name
		}
		return "no match: " + f[1]
	case "close":
		_ = Close(m.run, m.outer)
		return "__quit__"
	}
	// Bare words: jump.
	if a := m.fuzzyAgent(strings.ToLower(line)); a != nil {
		_ = Select(m.run, m.outer, a.PaneID)
		return ""
	}
	return "no match: " + line
}

// duckExec shells out to this same duck binary for verbs whose logic lives
// in the command layer (preview/render). Context is safe: panel context is
// pane-anchored via TMUX_PANE.
func (m *watchModel) duckExec(args ...string) string {
	self, err := os.Executable()
	if err != nil {
		return err.Error()
	}
	out, err := exec.Command(self, args...).CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if err != nil {
		if msg == "" {
			msg = err.Error()
		}
		return firstLine(msg)
	}
	if msg == "" {
		msg = strings.Join(args, " ") + " ✓"
	}
	return firstLine(msg)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func (m watchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.Width = max(10, m.width-4)
	case tickMsg:
		return m, tea.Batch(m.load, tick())
	case agentsMsg:
		if msg.err != nil {
			m.agents = nil
		} else {
			m.agents = msg.agents
		}
		m.statuses = msg.statuses
		if n := len(m.tabs()); m.tab >= n {
			m.tab = n - 1
		}
		if n := len(m.visible()); m.cursor >= n {
			m.cursor = max(0, n-1)
		}
	case tea.FocusMsg:
		m.focused, m.swallowNext = true, true
	case tea.BlurMsg:
		m.focused = false
	case tea.MouseMsg:
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			if !m.focused || m.swallowNext {
				m.focused, m.swallowNext = true, false
				return m, nil
			}
			if msg.Y == 0 { // tab bar
				_, spans := m.renderTabs()
				for _, sp := range spans {
					if msg.X >= sp.start && msg.X < sp.end {
						m.tab, m.cursor = sp.tab, 0
						return m, nil
					}
				}
				return m, nil
			}
			idx := m.visible()
			row := msg.Y - headerLines
			if row >= 0 && row < len(idx) {
				m.cursor = row
				wid := m.agents[idx[row]].PaneID
				_ = Select(m.run, m.outer, wid) // swap first: previews respawn at slot geometry
				RefreshIfStale(m.run, wid, FileMtime)
				return m, m.load
			}
		}
	case tea.KeyMsg:
		m.lastMsg = ""
		switch msg.String() {
		case "ctrl+c":
			_ = Close(m.run, m.outer)
			return m, tea.Quit
		case "esc":
			m.input.SetValue("")
		case "enter":
			res := m.runInput()
			if res == "__quit__" {
				return m, tea.Quit
			}
			m.lastMsg = res
			m.input.SetValue("")
			return m, m.load
		case "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down":
			if m.cursor < len(m.visible())-1 {
				m.cursor++
			}
		case "tab":
			// With a suggestion pending, tab completes; otherwise it cycles tabs.
			if g, _ := m.suggest(); g != "" && m.input.Value() != "" {
				m.input.SetValue(m.input.Value() + g)
				m.input.CursorEnd()
			} else {
				m.tab, m.cursor = (m.tab+1)%len(m.tabs()), 0
			}
		case "shift+tab":
			n := len(m.tabs())
			m.tab, m.cursor = (m.tab+n-1)%n, 0
		default:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			m.cursor = 0 // typing re-anchors the filter selection
			return m, cmd
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

type tabSpan struct{ start, end, tab int }

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

var (
	dimStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	selectedStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("11"))
	activeStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	workingStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	doneStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	tabStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	tabActiveStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("6"))
	ghostStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
)

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
	bar, _ := m.renderTabs()
	working, done := 0, 0
	for _, s := range m.statuses {
		switch s {
		case "working":
			working++
		case "done":
			done++
		}
	}
	if working > 0 {
		bar += workingStyle.Render(fmt.Sprintf(" ●%d", working))
	}
	if done > 0 {
		bar += doneStyle.Render(fmt.Sprintf(" ✔%d", done))
	}
	b.WriteString(truncate(bar, m.width) + "\n")
	b.WriteString(dimStyle.Render(strings.Repeat("─", max(1, m.width))) + "\n")

	idx := m.visible()
	if len(idx) == 0 {
		b.WriteString(dimStyle.Render("  nothing here — try the box below") + "\n")
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

	// Bottom: the command box + its hint, pinned.
	ghost, hint := m.suggest()
	inputLine := " " + m.input.View()
	if ghost != "" {
		inputLine += ghostStyle.Render(ghost)
	}
	var hintLine string
	switch {
	case m.lastMsg != "":
		hintLine = workingStyle.Render(" " + m.lastMsg)
	case hint != "":
		hintLine = dimStyle.Render(" " + hint)
	default:
		hintLine = dimStyle.Render(" ↵ view selected · ⇥ tabs/complete · spawn · edit · preview · render · kill · close")
	}

	body := b.String()
	if m.height > 0 {
		lines := strings.Count(body, "\n") + 2 // + input + hint
		for i := lines; i < m.height-1; i++ {
			body += "\n"
		}
	}
	return body + truncate(inputLine, m.width) + "\n" + truncate(hintLine, m.width)
}

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
