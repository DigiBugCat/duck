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
const wsTab = "workspaces"

// schedTab is the pinned pseudo-tab showing this workspace's routines (its
// standing schedules). Rows come from `duck routines --tsv` — the roster
// can't import internal/routines (it imports panel), so it shells out to its
// own binary like the other command-layer verbs.
const schedTab = "routines"

// routineRow is one schedule as shown on the ⏰ routines tab.
type routineRow struct {
	Name, Trigger, Sched, Model, Last, Next, Status string
	Path                                            string // the prompt .md (trailing TSV field; may be empty)
}

type tickMsg time.Time
type agentsMsg struct {
	agents     []Agent
	statuses   map[string]string
	workspaces []Workspace
	routines   []routineRow
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
	{"exit", "exit  — kill this ENTIRE workspace (session + all agents)"},
	{"help", "help  — how this panel works"},
}

type watchModel struct {
	run      Runner
	outer    string
	statusFn StatusFn

	agents     []Agent
	statuses   map[string]string
	workspaces []Workspace
	routines   []routineRow
	tabKind    string         // active tab BY NAME
	cursor     int            // index into visible() rows
	tabCursor  map[string]int // last cursor per tab, so switching back restores it
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
	m := watchModel{run: run, outer: outer, statusFn: status, input: ti, tabKind: KindAgent, tabCursor: map[string]int{}}
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithReportFocus()).Run()
	return err
}

func (m watchModel) Init() tea.Cmd { return tea.Batch(m.load, tick(), textinput.Blink) }

func tick() tea.Cmd {
	return tea.Tick(pollEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m watchModel) load() tea.Msg {
	// Self-heal: when the viewport occupant's PROCESS dies (codex exec
	// finishing, a shell exiting), its pane vanishes and the panel is broken
	// until something re-runs Open. The roster is the pane that survives, so
	// it re-asserts the panel itself — a fresh terminal viewport appears
	// within one poll instead of the user facing a mangled layout.
	if roles, rerr := Panes(m.run, m.outer); rerr == nil && roles["viewport"] == "" && roles["list"] != "" {
		if self, serr := os.Executable(); serr == nil {
			_ = Open(m.run, m.outer, Companion(m.outer), self)
		}
	}
	agents, err := Agents(m.run, m.outer)
	statuses := map[string]string{}
	if m.statusFn != nil {
		for _, a := range agents {
			statuses[a.PaneID] = m.statusFn(a.PaneID)
		}
	}
	ws, _ := Workspaces(m.run, m.outer)
	return agentsMsg{agents: agents, statuses: statuses, workspaces: ws, routines: loadRoutines(), err: err}
}

// loadRoutines fetches this workspace's schedules for the ⏰ routines tab by
// shelling out to `duck routines --tsv` (the roster can't import
// internal/routines — import cycle). Best-effort: any failure renders as an
// empty tab, never an error.
func loadRoutines() []routineRow {
	self, err := os.Executable()
	if err != nil {
		return nil
	}
	out, err := exec.Command(self, "routines", "--tsv").Output()
	if err != nil {
		return nil
	}
	var rows []routineRow
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		f := strings.SplitN(line, "\t", 9)
		if len(f) < 8 {
			continue
		}
		r := routineRow{Name: f[1], Trigger: f[2], Sched: f[3], Model: f[4], Last: f[5], Next: f[6], Status: f[7]}
		if len(f) > 8 { // trailing free-text field: tolerate its absence
			r.Path = f[8]
		}
		rows = append(rows, r)
	}
	return rows
}

// headerLines: tab bar + rule, above the first row (mouse-mapping contract).
const headerLines = 2

// tabs: all base kinds (anatomy, always visible), dynamic kinds sorted, and
// the pinned ⌂ ws tab last.
func (m watchModel) tabs() []string {
	count := map[string]int{}
	owned := m.routineNames()
	for _, a := range m.agents {
		if a.Kind == KindRun && owned[a.Name] {
			continue // folded under the ⏰ tab (see routineNames)
		}
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
	return append(out, schedTab, wsTab)
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
	m.switchTab(t[i])
}

// switchTab makes `kind` the active tab, restoring the cursor to the item that
// was selected there last (0 if never visited), and swaps the viewport to it —
// no ↵ needed. The old tab's cursor is saved first so returning restores it.
// Item tabs (agents/shells/artifacts/scratchpad) auto-view; the routines tab
// has no viewport action and the workspaces tab would teleport the terminal to
// another workspace, so a mere tab-switch must never trigger either.
func (m *watchModel) switchTab(kind string) {
	m.tabCursor[m.tabKind] = m.cursor
	m.tabKind, m.armedKill = kind, ""
	m.cursor = m.tabCursor[kind]
	if n := len(m.visible()); m.cursor >= n { // list shrank while we were away
		m.cursor = 0
	}
	if m.tabKind == schedTab {
		// Arriving on ⏰ shows the selected routine's card.
		if idx := m.visible(); m.cursor < len(idx) {
			m.showRoutineCard(m.routines[idx[m.cursor]])
			return
		}
		_ = ShowEmpty(m.run, m.outer, m.tabKind)
		return
	}
	if m.tabKind == wsTab {
		m.previewWorkspace()
		return
	}
	if len(m.visible()) == 0 {
		_ = ShowEmpty(m.run, m.outer, m.tabKind)
		return
	}
	m.viewSelected()
}

// previewWorkspace shows a still snapshot of the workspace under the cursor on
// the ⌂ ws tab — a colored capture of its main pane, so you can read what a
// workspace is before switching to it. The current workspace previews itself
// (harmless); an empty tab is a no-op.
func (m *watchModel) previewWorkspace() {
	idx := m.visible()
	if m.cursor >= len(idx) {
		return
	}
	w := m.workspaces[idx[m.cursor]]
	_ = ShowWorkspacePreview(m.run, m.outer, w.Display, w.MainPane)
}

// goWorkspace commits the switch: teleport the terminal to the highlighted
// workspace on the ⌂ ws tab. This is the explicit action (`g`) — arrow/Enter
// only preview. No-op on the current workspace.
func (m *watchModel) goWorkspace() string {
	if m.tabKind != wsTab {
		return ""
	}
	idx := m.visible()
	if m.cursor >= len(idx) {
		return ""
	}
	w := m.workspaces[idx[m.cursor]]
	if w.Current {
		return "already here"
	}
	// Restore the real viewport before leaving — the destination client must
	// not inherit our hidden filler pane swapped into the slot.
	RestoreViewport(m.run, m.outer)
	self, _ := os.Executable()
	if err := SwitchTo(m.run, m.outer, w.Name, self); err != nil {
		return err.Error()
	}
	return ""
}

// backWorkspace returns to the previously-connected workspace via tmux's own
// last-session (`switch-client -l`) — no self-tracked state, which would
// evaporate anyway since the roster reinits per workspace.
func (m *watchModel) backWorkspace() string {
	RestoreViewport(m.run, m.outer)
	out, err := m.run("list-clients", "-t", m.outer, "-F", "#{client_name}")
	if err != nil {
		return err.Error()
	}
	client := strings.TrimSpace(strings.Split(strings.TrimSpace(out), "\n")[0])
	if client == "" {
		return "no client"
	}
	if _, err := m.run("switch-client", "-c", client, "-l"); err != nil {
		return "no previous workspace"
	}
	return ""
}

// filterText is the box content when it should act as a live filter.
func (m watchModel) filterText() string {
	if !m.cmdFocus || m.isVerb() {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(m.input.Value()))
}

// rowBudget is how many rows fit between the header and the input+hint.
func (m watchModel) rowBudget() int {
	b := m.height - headerLines - 2
	if b < 1 {
		b = 1
	}
	return b
}

// rowWindow returns the scroll window over visible(): start offset and the
// slice on screen, keeping the cursor inside the window. View and the mouse
// row-mapping share it so a click can never hit the wrong row.
func (m watchModel) rowWindow() (int, []int) {
	idx := m.visible()
	budget := m.rowBudget()
	if len(idx) <= budget {
		return 0, idx
	}
	start := m.cursor - budget/2
	if start < 0 {
		start = 0
	}
	if start > len(idx)-budget {
		start = len(idx) - budget
	}
	return start, idx[start : start+budget]
}

// visible returns the row indexes for the active tab: agent indexes, or
// workspace indexes on the workspaces tab — both honoring the live filter.
func (m watchModel) visible() []int {
	q := m.filterText()
	var idx []int
	if m.tabKind == schedTab {
		for i, r := range m.routines {
			if q != "" && !isSubseq(strings.ToLower(r.Name), q) {
				continue
			}
			idx = append(idx, i)
		}
		return idx
	}
	if m.tabKind == wsTab {
		for i, w := range m.workspaces {
			if q != "" && !isSubseq(strings.ToLower(w.Name+" "+w.Display), q) {
				continue
			}
			idx = append(idx, i)
		}
		return idx
	}
	owned := m.routineNames()
	for i, a := range m.agents {
		if a.Kind != m.tabKind {
			continue
		}
		if a.Kind == KindRun && owned[a.Name] {
			continue // routine-backed run: lives under the ⏰ tab, not here
		}
		if q != "" && !isSubseq(strings.ToLower(a.Name), q) {
			continue
		}
		idx = append(idx, i)
	}
	return idx
}

// routineNames is the set of this workspace's routine names. Executor panes
// (kind=runs) named after a routine are that routine's NESTED view: they fold
// under the ⏰ tab (↵ on the routine shows the run) instead of cluttering a
// separate runs tab. Ad-hoc runs (no matching routine) still get the runs tab.
func (m watchModel) routineNames() map[string]bool {
	names := make(map[string]bool, len(m.routines))
	for _, r := range m.routines {
		names[r.Name] = true
	}
	return names
}

// showRoutineCard puts the routine's detail card in the viewport: header +
// schedule/model/fire-times meta line + the glow-rendered prompt md. Arrow
// keys, ↵, clicks, and tab-arrival all land here; the raw run pane stays one
// keypress away (`v`).
func (m *watchModel) showRoutineCard(r routineRow) {
	meta := string(r.Trigger) + " " + r.Sched
	if r.Model != "" && r.Model != "—" {
		meta += " · " + r.Model
	}
	meta += " · last " + r.Last + " · next " + r.Next + " · " + r.Status
	_ = ShowRoutineDetail(m.run, m.outer, r.Name, meta, r.Path)
}

// routineRun finds the live executor pane backing a routine, if any.
func (m watchModel) routineRun(name string) (Agent, bool) {
	for _, a := range m.agents {
		if a.Kind == KindRun && a.Name == name {
			return a, true
		}
	}
	return Agent{}, false
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
	case schedTab:
		return "new  — schedule with: duck routines add <name> --cron/--every … <prompt>"
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
	if m.tabKind == schedTab {
		// ↵/click = the routine's CARD (prompt md + schedule meta), like the
		// Codex automation detail view. The live run is `v`; firing is `f`.
		m.showRoutineCard(m.routines[idx[m.cursor]])
		return ""
	}
	if m.tabKind == wsTab {
		// Preview only — actually switching is the explicit `g` (go). Enter and
		// click on a ws row bring its snapshot into the viewport, never teleport.
		m.previewWorkspace()
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
		case schedTab:
			return "schedule with: duck routines add <name> --cron/--every … <prompt>"
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
		path, err := EnsurePad(PadRoot(m.run, m.outer), name)
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
		m.switchTab(wsTab)
		return ""
	case "close":
		_ = Close(m.run, m.outer)
		return "__quit__"
	case "exit":
		// Kill the ENTIRE workspace directly via tmux (`duck kill` is the
		// laptop-client verb — it wants a configured hub, and the roster runs
		// ON the hub). Companion first: the outer kill takes this TUI down
		// with it, so it must be the last thing we ask tmux to do.
		_, _ = m.run("kill-session", "-t", Companion(m.outer))
		_, _ = m.run("kill-session", "-t", m.outer)
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
		m.routines = msg.routines
		if n := len(m.visible()); m.cursor >= n {
			m.cursor = max(0, n-1)
		}
	case tea.FocusMsg:
		m.focused, m.swallowNext = true, true
	case tea.BlurMsg:
		m.focused = false
	case tea.MouseMsg:
		// Wheel scrolls the selection (the row window follows the cursor).
		// Without this, wheel events fall through undefined — in a
		// mouse-tracking pane tmux hands the wheel to US, not to copy-mode.
		if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
			if msg.Action != tea.MouseActionPress {
				return m, nil
			}
			delta := 1
			if msg.Button == tea.MouseButtonWheelUp {
				delta = -1
			}
			if n := len(m.visible()); n > 0 {
				m.cursor = max(0, min(n-1, m.cursor+delta))
			}
			return m, nil
		}
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			if !m.focused || m.swallowNext {
				m.focused, m.swallowNext = true, false
				return m, nil
			}
			if msg.Y == 0 { // tab bar
				_, spans := m.renderTabs()
				for _, sp := range spans {
					if msg.X >= sp.start && msg.X < sp.end {
						m.switchTab(m.tabs()[sp.tab])
						return m, nil
					}
				}
				return m, nil
			}
			start, win := m.rowWindow()
			row := msg.Y - headerLines
			if row >= 0 && row < len(win) {
				m.cursor = start + row
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
				if m.tabKind == wsTab {
					m.previewWorkspace()
				} else if m.tabKind == schedTab {
					if idx := m.visible(); m.cursor < len(idx) {
						m.showRoutineCard(m.routines[idx[m.cursor]])
					}
				}
			}
		case "down", "j":
			m.armedKill = ""
			if m.cursor < len(m.visible())-1 {
				m.cursor++
				if m.tabKind == wsTab {
					m.previewWorkspace()
				} else if m.tabKind == schedTab {
					if idx := m.visible(); m.cursor < len(idx) {
						m.showRoutineCard(m.routines[idx[m.cursor]])
					}
				}
			}
		case "left", "shift+tab", "h":
			m.cycleTab(-1)
		case "right", "tab", "l":
			m.cycleTab(1)
		case "enter":
			m.lastMsg = m.viewSelected()
			return m, m.load
		case "v": // view the highlighted routine's live run pane (⏰ tab only)
			if m.tabKind == schedTab {
				if idx := m.visible(); m.cursor < len(idx) {
					r := m.routines[idx[m.cursor]]
					if a, ok := m.routineRun(r.Name); ok {
						_ = Select(m.run, m.outer, a.PaneID)
					} else {
						m.lastMsg = "no run yet for " + r.Name + " — press f to fire it"
					}
					return m, nil
				}
			}
		case "e": // edit the highlighted routine's prompt md (⏰ tab only)
			if m.tabKind == schedTab {
				if idx := m.visible(); m.cursor < len(idx) {
					r := m.routines[idx[m.cursor]]
					if r.Path == "" {
						m.lastMsg = "no prompt file known for " + r.Name
						return m, nil
					}
					if out := m.duckExec("edit", r.Path); out != "" {
						m.lastMsg = out
					}
					return m, m.load
				}
			}
		case "f": // fire the highlighted routine now (⏰ tab only)
			if m.tabKind == schedTab {
				if idx := m.visible(); m.cursor < len(idx) {
					r := m.routines[idx[m.cursor]]
					if out := m.duckExec("routines", "fire", r.Name); out != "" {
						m.lastMsg = out
					} else {
						m.lastMsg = "fired " + r.Name
					}
					return m, m.load
				}
			}
		case "g": // go: commit switch to the highlighted workspace (ws tab only)
			if m.tabKind == wsTab {
				m.lastMsg = m.goWorkspace()
				return m, m.load
			}
		case "b": // back: return to the previously-connected workspace
			if m.tabKind == wsTab {
				m.lastMsg = m.backWorkspace()
				return m, m.load
			}
		case "x":
			if m.tabKind == schedTab {
				m.lastMsg = "retire schedules with: duck routines rm <name>"
				return m, nil
			}
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
	// First pass: render every cell and note the active one.
	kinds := m.tabs()
	cells := make([]string, len(kinds))
	widths := make([]int, len(kinds))
	activeIdx := 0
	for ti, kind := range kinds {
		n := 0
		if kind == wsTab {
			n = len(m.workspaces)
		} else if kind == schedTab {
			n = len(m.routines)
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
			activeIdx = ti
		}
		cells[ti] = cell
		widths[ti] = lipgloss.Width(cell) + 1 // +1 = inter-tab space
	}
	// Second pass: window the bar so the ACTIVE tab is always on screen (a
	// narrow pane must never hide where you are — see the ⏰-tab-invisible
	// incident). Drop leading tabs until active fits; "‹" marks the cut.
	start := 0
	if m.width > 0 {
		total := func(from int) int {
			t := 0
			for i := from; i <= activeIdx; i++ {
				t += widths[i]
			}
			return t
		}
		for start < activeIdx && total(start)+2 > m.width { // +2 = "‹ " marker
			start++
		}
	}
	if start > 0 {
		marker := dimStyle.Render("‹ ")
		b.WriteString(marker)
		x += lipgloss.Width(marker)
	}
	for ti := start; ti < len(kinds); ti++ {
		spans = append(spans, tabSpan{start: x, end: x + widths[ti] - 1, tab: ti})
		b.WriteString(cells[ti])
		x += widths[ti] - 1
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

	start, win := m.rowWindow()
	if len(win) == 0 {
		b.WriteString(dimStyle.Render("  nothing here — press : and try `new`") + "\n")
	}
	if start > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  ↑ %d more", start)) + "\n")
	}
	for w, i := range win {
		row := start + w
		var line string
		if m.tabKind == schedTab {
			r := m.routines[i]
			meta := r.Trigger + " " + r.Sched
			if r.Model != "" && r.Model != "—" {
				meta += " · " + r.Model
			}
			label := r.Name + dimStyle.Render(" · "+meta+" · next "+r.Next+" · "+r.Status)
			line = "  " + label
			if row == m.cursor {
				hint := " ↵ card · e edit · f fire"
				if _, ok := m.routineRun(r.Name); ok {
					hint = " ↵ card · v run · e edit · f fire"
				}
				line = "  " + selectedStyle.Render(" "+r.Name+" ") + dimStyle.Render(hint)
			}
			b.WriteString(truncate(line, m.width) + "\n")
			continue
		}
		if m.tabKind == wsTab {
			w := m.workspaces[i]
			disp := w.Display
			if disp == "" {
				disp = w.Name
			}
			label := disp
			if disp != w.Name {
				label += dimStyle.Render(" · " + w.Name)
			}
			if w.Current {
				label += dimStyle.Render(" · here")
			} else if w.Attached {
				label += dimStyle.Render(" · attached")
			}
			line = "  " + label
			if row == m.cursor {
				line = "  " + selectedStyle.Render(" "+disp+" ")
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
	if rest := len(m.visible()) - (start + len(win)); rest > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  ↓ %d more", rest)) + "\n")
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
	case m.tabKind == wsTab:
		hintLine = dimStyle.Render(" ↵ preview · g go · b back · ←→ tabs · q close")
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
