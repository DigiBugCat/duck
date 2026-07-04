// watch.go is the Bubble Tea program that runs INSIDE the panel's roster
// pane (`duck panel watch <outer>`): the workspace's citizens as a list.
//
// Two modes, deliberately boring:
//   - BROWSE (default): arrows pick, ←→/⇥ switch tabs, ↵ shows the selected
//     item in the viewport, x (twice) kills it. Letters do nothing.
//   - COMMAND: entered by typing `:` or clicking the box. The always-visible
//     box at the bottom takes over: typing live-filters the list and jumps
//     by name on ↵; verbs (spawn/edit/preview/render/kill/close/help/ws)
//     ghost-complete with a hint line saying what ↵ will do. esc returns.
//
// The last tab is ⌂ ws — the hub's workspaces; ↵ on one switches your
// terminal there (pure switch-client: the workspace you leave is untouched).
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
)

// pollEvery is the roster refresh cadence.
const pollEvery = 2 * time.Second

// wsTab is the pinned pseudo-tab showing hub workspaces.
const wsTab = "⌂ ws"

type tickMsg time.Time
type agentsMsg struct {
	agents     []Agent
	statuses   map[string]string
	workspaces []Workspace
	err        error
}

// StatusFn classifies an agent pane's state ("working", "done", "idle").
type StatusFn func(paneID string) string

// verbs the command box understands. Order = suggestion priority.
var verbs = []struct{ name, usage string }{
	{"new", ""}, // contextual: usage depends on the active tab (see newUsage)
	{"spawn", "spawn <cmd…>  — launch an agent"},
	{"edit", "edit [pad]  — open/create a pad (no name: workspace pad)"},
	{"preview", "preview <file|url>  — render in the viewport"},
	{"render", "render <file|url>  — open in your laptop browser"},
	{"kill", "kill <name>  — kill an agent/buffer"},
	{"workspaces", "workspaces  — jump to the ⌂ ws tab"},
	{"close", "close  — close this panel (everything keeps running)"},
	{"help", "help  — how this panel works"},
}

type watchModel struct {
	run      Runner
	outer    string
	statusFn StatusFn

	agents     []Agent
	statuses   map[string]string
	workspaces []Workspace
	tabKind    string // active tab BY NAME
	cursor     int    // index into visible() rows
	width      int
	height     int

	input       textinput.Model
	cmdFocus    bool   // command mode: the box owns the keyboard
	helpMode    bool   // cheat sheet shown instead of rows
	armedKill   string // pane armed by first x; second x kills
	lastMsg     string // last command result/error, until next keypress
	focused     bool
	swallowNext bool // the click that focuses the pane must not also select
}

// Watch runs the roster TUI until close/ctrl+c.
func Watch(run Runner, outer string, status StatusFn) error {
	ti := textinput.New()
	ti.Prompt = "❯ "
	ti.Placeholder = ": for commands · ↵ view · x kill · ←→ tabs"
	m := watchModel{run: run, outer: outer, statusFn: status, input: ti, tabKind: KindAgent}
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
	ws, _ := Workspaces(m.run, m.outer)
	return agentsMsg{agents: agents, statuses: statuses, workspaces: ws, err: err}
}

// headerLines: tab bar + rule, above the first row (mouse-mapping contract).
const headerLines = 2

// tabs: all base kinds (anatomy, always visible), dynamic kinds sorted, and
// the pinned ⌂ ws tab last.
func (m watchModel) tabs() []string {
	count := map[string]int{}
	for _, a := range m.agents {
		count[a.Kind]++
	}
	out := append([]string{}, BaseKinds...)
	base := map[string]bool{}
	for _, k := range BaseKinds {
		base[k] = true
	}
	var extra []string
	for k := range count {
		if !base[k] && k != "" {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	out = append(out, extra...)
	return append(out, wsTab)
}

func (m watchModel) activeKind() string { return m.tabKind }

func (m watchModel) tabIndex() int {
	for i, k := range m.tabs() {
		if k == m.tabKind {
			return i
		}
	}
	return 0
}

func (m *watchModel) cycleTab(delta int) {
	t := m.tabs()
	i := (m.tabIndex() + delta + len(t)) % len(t)
	m.tabKind, m.cursor, m.armedKill = t[i], 0, ""
}

// filterText is the box content when it should act as a live filter.
func (m watchModel) filterText() string {
	if !m.cmdFocus || m.isVerb() {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(m.input.Value()))
}

// visible returns the row indexes for the active tab: agent indexes, or
// workspace indexes on the ⌂ ws tab — both honoring the live filter.
func (m watchModel) visible() []int {
	q := m.filterText()
	var idx []int
	if m.tabKind == wsTab {
		for i, w := range m.workspaces {
			if q != "" && !isSubseq(strings.ToLower(w.Name), q) {
				continue
			}
			idx = append(idx, i)
		}
		return idx
	}
	for i, a := range m.agents {
		if a.Kind != m.tabKind {
			continue
		}
		if q != "" && !isSubseq(strings.ToLower(a.Name), q) {
			continue
		}
		idx = append(idx, i)
	}
	return idx
}

func (m watchModel) isVerb() bool {
	f := strings.Fields(m.input.Value())
	if len(f) == 0 {
		return false
	}
	if f[0] == "ws" {
		return true
	}
	for _, v := range verbs {
		if f[0] == v.name || (len(f) > 1 && strings.HasPrefix(v.name, f[0])) {
			return true
		}
	}
	return false
}

// newUsage is the tab-aware meaning of the `new` verb.
func (m watchModel) newUsage() string {
	switch m.activeKind() {
	case KindShell:
		return "new  — open a fresh shell here"
	case KindBuffer:
		return "new <name>  — create a pad"
	case KindArtifact:
		return "new <file|url>  — preview something"
	case KindAgent:
		return "new <cmd…>  — launch an agent"
	case wsTab:
		return "new  — (workspaces are made by `duck` in a directory)"
	default:
		return "new <cmd…>  — launch into the " + m.activeKind() + " tab"
	}
}

// suggest computes ghost completion + hint for the current input.
func (m watchModel) suggest() (ghost, hint string) {
	val := m.input.Value()
	f := strings.Fields(val)
	if len(f) == 0 {
		return "", ""
	}
	usage := func(v string) string {
		if v == "new" {
			return m.newUsage()
		}
		for _, e := range verbs {
			if e.name == v {
				return e.usage
			}
		}
		return ""
	}
	if f[0] == "ws" {
		return "", usage("workspaces")
	}
	if len(f) == 1 && !strings.HasSuffix(val, " ") {
		for _, v := range verbs {
			if strings.HasPrefix(v.name, f[0]) && v.name != f[0] {
				return v.name[len(f[0]):], usage(v.name)
			}
		}
	}
	for _, v := range verbs {
		if f[0] == v.name {
			return "", usage(v.name)
		}
	}
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

// viewSelected shows the selected row: swap an agent into the viewport, or
// switch the terminal to a workspace on the ⌂ ws tab.
func (m *watchModel) viewSelected() string {
	idx := m.visible()
	if m.cursor >= len(idx) {
		return ""
	}
	if m.tabKind == wsTab {
		w := m.workspaces[idx[m.cursor]]
		if w.Current {
			return "already here"
		}
		self, _ := os.Executable()
		if err := SwitchTo(m.run, m.outer, w.Name, self); err != nil {
			return err.Error()
		}
		return ""
	}
	wid := m.agents[idx[m.cursor]].PaneID
	_ = Select(m.run, m.outer, wid) // swap first: previews respawn at slot geometry
	RefreshIfStale(m.run, wid, FileMtime)
	return ""
}

// runInput executes the command box's content.
func (m *watchModel) runInput() string {
	line := strings.TrimSpace(m.input.Value())
	if line == "" {
		return m.viewSelected()
	}
	f := strings.Fields(line)
	if f[0] == "ws" {
		f[0] = "workspaces"
	}
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
	case "new":
		switch m.activeKind() {
		case KindShell:
			dir, _ := CurrentPanePath(m.run)
			if _, err := Spawn(m.run, m.outer, "shell", dir, "", KindShell); err != nil {
				return err.Error()
			}
			return "new shell"
		case KindBuffer:
			if len(f) < 2 {
				return "new <name> — pads need names"
			}
			m.input.SetValue("edit " + f[1])
			return m.runInput()
		case KindArtifact:
			if len(f) < 2 {
				return "new <file|url>"
			}
			return m.duckExec("preview", f[1])
		case wsTab:
			return "workspaces are made by running `duck` in a directory"
		default:
			if len(f) < 2 {
				return "new <cmd…>"
			}
			dir, err := CurrentPanePath(m.run)
			if err != nil {
				return err.Error()
			}
			name := f[1]
			if i := strings.LastIndex(name, "/"); i >= 0 {
				name = name[i+1:]
			}
			if _, err := Spawn(m.run, m.outer, name, dir, strings.Join(f[1:], " "), m.activeKind()); err != nil {
				return err.Error()
			}
			return "spawned " + name + " → " + m.activeKind()
		}
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
		name := "scratch"
		if len(f) > 1 {
			name = f[1]
		}
		for _, a := range m.agents {
			if a.Kind == KindBuffer && a.Name == name {
				_ = Select(m.run, m.outer, a.PaneID)
				return ""
			}
		}
		path, err := PadPath(ProjectName(m.run, m.outer), name)
		if err != nil {
			return err.Error()
		}
		dir, _ := CurrentPanePath(m.run)
		if _, err := Spawn(m.run, m.outer, name, dir, PadCmd(path), KindBuffer); err != nil {
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
	case "workspaces":
		m.tabKind, m.cursor = wsTab, 0
		return ""
	case "close":
		_ = Close(m.run, m.outer)
		return "__quit__"
	case "help":
		m.helpMode = true
		return ""
	}
	if a := m.fuzzyAgent(strings.ToLower(line)); a != nil {
		_ = Select(m.run, m.outer, a.PaneID)
		return ""
	}
	return "no match: " + line
}

// duckExec shells out to this duck binary for command-layer verbs.
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
		m.workspaces = msg.workspaces
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
						m.tabKind, m.cursor, m.armedKill = m.tabs()[sp.tab], 0, ""
						return m, nil
					}
				}
				return m, nil
			}
			idx := m.visible()
			row := msg.Y - headerLines
			if row >= 0 && row < len(idx) {
				m.cursor = row
				m.lastMsg = m.viewSelected()
				return m, m.load
			}
			// Click at/below the box line: enter command mode.
			if msg.Y >= m.height-2 {
				m.cmdFocus = true
				return m, m.input.Focus()
			}
		}
	case tea.KeyMsg:
		m.lastMsg = ""
		if m.helpMode {
			m.helpMode = false // any key returns
			return m, nil
		}
		if m.cmdFocus {
			switch msg.String() {
			case "esc":
				m.cmdFocus = false
				m.input.SetValue("")
				m.input.Blur()
			case "ctrl+c":
				_ = Close(m.run, m.outer)
				return m, tea.Quit
			case "enter":
				res := m.runInput()
				if res == "__quit__" {
					return m, tea.Quit
				}
				m.lastMsg = res
				m.cmdFocus = false
				m.input.SetValue("")
				m.input.Blur()
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
				if g, _ := m.suggest(); g != "" && m.input.Value() != "" {
					m.input.SetValue(m.input.Value() + g)
					m.input.CursorEnd()
				}
			default:
				var cmd tea.Cmd
				m.input, cmd = m.input.Update(msg)
				m.cursor = 0
				return m, cmd
			}
			return m, nil
		}
		// BROWSE mode.
		switch msg.String() {
		case ":":
			m.cmdFocus = true
			return m, m.input.Focus()
		case "ctrl+c", "q":
			_ = Close(m.run, m.outer)
			return m, tea.Quit
		case "up", "k":
			m.armedKill = ""
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			m.armedKill = ""
			if m.cursor < len(m.visible())-1 {
				m.cursor++
			}
		case "left", "shift+tab", "h":
			m.cycleTab(-1)
		case "right", "tab", "l":
			m.cycleTab(1)
		case "enter":
			m.lastMsg = m.viewSelected()
			return m, m.load
		case "x":
			if m.tabKind == wsTab {
				m.lastMsg = "workspaces aren't killed from here"
				return m, nil
			}
			if idx := m.visible(); m.cursor < len(idx) {
				wid := m.agents[idx[m.cursor]].PaneID
				if m.armedKill == wid {
					m.armedKill = ""
					wasActive := m.agents[idx[m.cursor]].Active
					_ = Kill(m.run, wid)
					if wasActive {
						EnsureSlot(m.run, m.outer)
					}
					return m, m.load
				}
				m.armedKill = wid
			}
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
		if kind == wsTab {
			n = len(m.workspaces)
		} else {
			for _, a := range m.agents {
				if a.Kind == kind {
					n++
				}
			}
		}
		view := " "
		if kind == activeKind {
			view = "▶"
		}
		label := view + kind + " "
		if n > 0 && kind != wsTab {
			label = fmt.Sprintf("%s%s %d ", view, kind, n)
		}
		cell := tabStyle.Render(label)
		if kind == m.tabKind {
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

// helpLines is the cheat sheet the help verb shows.
func helpLines(m watchModel) []string {
	h := []string{
		activeStyle.Render(" this panel = your workspace"),
		dimStyle.Render(" tabs group what's running; ▶ marks what the big pane shows;"),
		dimStyle.Render(" ● working  ✔ done  ○ idle · ⌂ ws lists hub workspaces (↵ switches)"),
		"",
		activeStyle.Render(" browsing (default):"),
		" ↑↓ pick · ←→/⇥ tabs · ↵ view/switch · x x kill · q close",
		"",
		activeStyle.Render(" commands — press : (or click the box), then type:"),
	}
	for _, v := range verbs {
		u := v.usage
		if v.name == "new" {
			u = m.newUsage()
		}
		if u != "" {
			h = append(h, " "+dimStyle.Render(u))
		}
	}
	h = append(h, " "+dimStyle.Render("or type any name — fuzzy-jumps to it"))
	h = append(h, "", dimStyle.Render(" from any shell: duck spawn · duck edit · duck preview · duck render"))
	return h
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

	if m.helpMode {
		for _, line := range helpLines(m) {
			b.WriteString(truncate(line, m.width) + "\n")
		}
		body := b.String()
		if m.height > 0 {
			lines := strings.Count(body, "\n") + 1
			for i := lines; i < m.height-1; i++ {
				body += "\n"
			}
		}
		return body + dimStyle.Render(" any key to return")
	}

	idx := m.visible()
	if len(idx) == 0 {
		b.WriteString(dimStyle.Render("  nothing here — press : and try `new`") + "\n")
	}
	for row, i := range idx {
		var line string
		if m.tabKind == wsTab {
			w := m.workspaces[i]
			label := w.Name
			if w.Current {
				label += dimStyle.Render(" · here")
			} else if w.Attached {
				label += dimStyle.Render(" · attached")
			}
			line = "  " + label
			if row == m.cursor {
				line = "  " + selectedStyle.Render(" "+w.Name+" ")
			}
		} else {
			a := m.agents[i]
			marker := "  "
			if a.Active {
				marker = activeStyle.Render("▶ ")
			}
			label := a.Name
			if a.Command != "" && !strings.EqualFold(a.Command, a.Name) {
				label += dimStyle.Render(" · " + a.Command)
			}
			line = marker + statusGlyph(m.statuses[a.PaneID]) + " " + label
			if row == m.cursor {
				line = marker + statusGlyph(m.statuses[a.PaneID]) + " " + selectedStyle.Render(" "+a.Name+" ")
			}
		}
		b.WriteString(truncate(line, m.width) + "\n")
	}

	ghost, hint := "", ""
	if m.cmdFocus {
		ghost, hint = m.suggest()
	}
	inputLine := " " + m.input.View()
	if ghost != "" {
		inputLine += ghostStyle.Render(ghost)
	}
	var hintLine string
	switch {
	case m.armedKill != "":
		name := ""
		for _, a := range m.agents {
			if a.PaneID == m.armedKill {
				name = a.Name
			}
		}
		hintLine = workingStyle.Render(" x again to kill " + name)
	case m.lastMsg != "":
		hintLine = workingStyle.Render(" " + m.lastMsg)
	case m.cmdFocus && hint != "":
		hintLine = dimStyle.Render(" " + hint)
	case m.cmdFocus:
		hintLine = dimStyle.Render(" type a name or a verb · esc back · help for the guide")
	default:
		hintLine = dimStyle.Render(" ↵ view · x kill · ←→ tabs · : commands · q close")
	}

	body := b.String()
	if m.height > 0 {
		lines := strings.Count(body, "\n") + 2
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
