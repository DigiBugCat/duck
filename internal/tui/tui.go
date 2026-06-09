// Package tui is duck's picker: a bubbletea/lipgloss TUI over the live remote
// tmux sessions (DESIGN §6), reached via `duck --resume` / `duck ls` — NOT the
// bare-`duck` default. It ports flok's internal/tui scaffolding — the
// Model/Init/Update/View shape, the visibleWindow + render-hardening, the
// palette, and crucially the `svc Service` + `load func() tea.Msg` injection
// seam — but renders model.Row (one row per session, raw display name) instead
// of flok's bundle tree.
//
// The dependency flows tui → app (Service) and tui → model (Row); tui imports
// neither session/names/namer directly nor anything that imports tui, so there
// is no cycle.
//
// The behavior — fuzzy filter, ^s/^a scopes, enter-attach (teardown →
// ExecAttach), ^r rename overlay, ^n codex re-name, ^k kill — is implemented on
// top of the ported Model/Init/Update/View shape and the svc Service seam.
package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/DigiBugCat/duck/internal/app"
	rowmodel "github.com/DigiBugCat/duck/internal/model"
)

// Run launches the picker against the given Service. It blocks until the user
// quits or chooses a session. On a choice it returns the chosen session's tmux
// name (the caller execs the attach AFTER the TUI has fully torn down, so ssh/
// tmux own a clean TTY); on a plain quit it returns "".
// Run launches the picker. checkUpdate is an optional background command (nil to
// disable) that reports a newer release via UpdateAvailableMsg; when one is
// available the picker shows a banner and ^u sets doUpdate so the caller can
// self-update after teardown. Returns the chosen tmux name (or ""), and doUpdate.
func Run(svc app.Service, cwdDir string, checkUpdate func() tea.Msg) (tmuxName string, doUpdate bool, err error) {
	// Detect (and PIN) the terminal background ONCE, before bubbletea takes over
	// stdin — same as flock/internal/tui. Otherwise lipgloss's AdaptiveColor
	// detection probes the terminal (OSC 11) lazily during the render loop while
	// bubbletea owns the input stream; the reply never arrives, detection defaults
	// to dark, and every adaptive color falls back to its Dark variant (the
	// washed-out look on a light terminal). SetHasDarkBackground pins the result so
	// no probe runs mid-loop.
	lipgloss.SetHasDarkBackground(lipgloss.HasDarkBackground())
	m := initialModel(svc)
	m.cwdDir = cwdDir
	m.checkUpdate = checkUpdate
	if cwdDir != "" {
		// Default to THIS folder's sessions, like `claude --resume` (which opens on
		// the current project). ^a widens to every folder; ^s narrows back. Starting
		// scoped keeps the folder you ran --resume in from being buried under every
		// other dir's sessions. The scope is per-run (not persisted), so each resume
		// reopens on the current folder.
		m.scope = scopeThisDir
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return "", false, err
	}
	fm, ok := final.(model)
	if !ok {
		return "", false, nil
	}
	return fm.selected, fm.doUpdate, nil
}

type loadState int

const (
	stateLoading loadState = iota
	stateLoaded
	stateError
)

// uiMode is the interaction overlay on top of the data state: browse owns the
// keyboard; input captures the rename overlay text; confirm captures a y/n for
// kill.
type uiMode int

const (
	modeBrowse  uiMode = iota
	modeInput          // rename overlay (raw UTF-8)
	modeConfirm        // kill confirmation
)

// scope is the picker's dir filter: this-dir-only (^s) or all (^a).
type scope int

const (
	scopeAll scope = iota
	scopeThisDir
)

type model struct {
	state  loadState
	err    error
	rows   []rowmodel.Row // resolved display rows (the data the picker renders)
	cursor int
	width  int
	height int

	filter string // always-on fuzzy query over display name + dir
	scope  scope  // ^s this-dir / ^a all
	cwdDir string // tilde-form cwd, used by scopeThisDir

	mode      uiMode
	input     textinput.Model // rename overlay
	inputErr  string
	renameFor string // tmux name being renamed in modeInput
	killFor   string // tmux name pending kill in modeConfirm

	busy      bool
	spinner   spinner.Model
	status    string
	statusErr bool

	selected     string // chosen tmux name on enter-attach; read by Run after quit
	updateLatest string // newer release tag from the background check; "" = none/up-to-date
	doUpdate     bool   // ^u pressed with an update available; read by Run after quit

	svc         app.Service    // operations; injectable for tests (flok's seam)
	load        func() tea.Msg // row loader; injectable for tests (flok's seam)
	checkUpdate func() tea.Msg // optional background update check; nil disables it
}

// initialModel builds the picker model around an injected Service, wiring the
// default loader. Tests construct a model with a fakeService + a canned load.
func initialModel(svc app.Service) model {
	ti := textinput.New()
	ti.Placeholder = "new name"
	ti.CharLimit = 256
	ti.Width = 40
	ti.Prompt = "› "
	sp := spinner.New()
	sp.Style = spinnerStyle
	return model{
		state:   stateLoading,
		input:   ti,
		spinner: sp,
		svc:     svc,
		load:    loadCmd(svc),
	}
}

// ---- Messages ----

type loadedMsg struct{ rows []rowmodel.Row }

type errMsg struct{ err error }

// UpdateAvailableMsg is emitted by the injected checkUpdate command when a newer
// release exists; Latest is the release tag (e.g. "v0.2.7"). Exported so the
// command layer (which knows the version + release API) can construct it.
type UpdateAvailableMsg struct{ Latest string }

// actionDoneMsg reports the outcome of an async mutating action (rename / name-
// now / kill), optionally requesting a refresh so the rows update.
type actionDoneMsg struct {
	summary string
	err     error
	refresh bool
}

// ---- Commands ----

// loadCmd refreshes the rows from the Service off the UI thread. It closes over
// svc so the model stays a value type, mirroring flok's loadCmd seam. On a hub
// error it emits errMsg so the picker shows the failure instead of an empty
// list.
func loadCmd(svc app.Service) func() tea.Msg {
	return func() tea.Msg {
		rows, err := svc.Refresh()
		if err != nil {
			return errMsg{err: err}
		}
		return loadedMsg{rows: rows}
	}
}

// renameCmd / nameNowCmd / killCmd run a Service mutation off the UI thread and
// report an actionDoneMsg. They close over svc so the model stays a value type.

func (m model) renameCmd(tmuxName, display string) tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		if err := svc.Rename(tmuxName, display); err != nil {
			return actionDoneMsg{err: err}
		}
		return actionDoneMsg{summary: "renamed", refresh: true}
	}
}

func (m model) nameNowCmd(tmuxName string) tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		name, err := svc.NameNow(tmuxName)
		if err != nil {
			return actionDoneMsg{err: err}
		}
		return actionDoneMsg{summary: "named: " + name, refresh: true}
	}
}

func (m model) killCmd(tmuxName string) tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		if err := svc.Kill(tmuxName); err != nil {
			return actionDoneMsg{err: err}
		}
		return actionDoneMsg{summary: "killed", refresh: true}
	}
}

// runAction marks the model busy and batches the action with a spinner tick.
func (m model) runAction(cmd tea.Cmd) (model, tea.Cmd) {
	m.busy = true
	m.status = ""
	m.statusErr = false
	return m, tea.Batch(cmd, m.spinner.Tick)
}

// ---- tea.Model ----

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.load, textinput.Blink}
	if m.checkUpdate != nil {
		cmds = append(cmds, m.checkUpdate) // background; posts UpdateAvailableMsg if newer
	}
	return tea.Batch(cmds...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case spinner.TickMsg:
		if m.busy {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil

	case loadedMsg:
		m.state = stateLoaded
		m.rows = msg.rows
		if m.cursor >= len(m.visibleRows()) {
			m.cursor = max(0, len(m.visibleRows())-1)
		}
		return m, nil

	case errMsg:
		m.state = stateError
		m.err = msg.err
		return m, nil

	case UpdateAvailableMsg:
		m.updateLatest = msg.Latest
		return m, nil

	case actionDoneMsg:
		return m.handleActionDone(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m model) handleActionDone(msg actionDoneMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	if msg.err != nil {
		m.status = msg.err.Error()
		m.statusErr = true
		return m, nil
	}
	m.status = msg.summary
	m.statusErr = false
	if msg.refresh {
		m.state = stateLoading
		return m, m.load
	}
	return m, nil
}

// handleKey dispatches a keypress by mode/state. ctrl+c is the universal hard
// abort in EVERY mode (browse + the input/confirm overlays), honouring the
// ^c-aborts-the-picker convention; it is checked before the rune-append path so
// it quits even mid-filter. The this-dir scope that ^c used to hold moved to ^s
// (^a stays = all). esc clears an active filter (else quits), giving a shown way
// to undo a filter without leaving the picker.
func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.mode {
	case modeInput:
		return m.handleInputKey(msg)
	case modeConfirm:
		return m.handleConfirmKey(msg)
	}
	return m.handleBrowseKey(msg)
}

// handleBrowseKey owns the keyboard in the row list: navigation, the ^s/^a
// scopes, enter→attach (stash selected + quit; the caller execs after teardown),
// ^r rename / ^n name-now / ^k kill, and always-on filter typing. ^c (quit) is
// handled upstream in handleKey before this runs.
func (m model) handleBrowseKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.busy {
		// Mutations are ignored while an action is in flight (flok parity).
		return m, nil
	}
	// On the error screen (a propagated hub/List failure — now reachable since
	// session.List stopped swallowing transport errors), `r` retries the load,
	// matching the footer's {r retry · q quit} hint. Guarded to stateError so `r`
	// stays a plain filter rune during normal browsing.
	if m.state == stateError && msg.String() == "r" {
		m.state = stateLoading
		m.err = nil
		return m, m.load
	}
	// Bare `q` quits ONLY when not building a filter; once a filter is active,
	// `q` is just a filter rune (so "query"/"queue" type normally). ^c always
	// quits (handled upstream); esc clears the filter when one is active.
	if msg.String() == "q" && m.filter == "" {
		return m, tea.Quit
	}

	switch msg.String() {
	case "esc":
		// esc clears an active filter (the shown way to undo a narrow); with no
		// filter it quits the picker.
		if m.filter != "" {
			m.filter = ""
			m.cursor = 0
			return m, nil
		}
		return m, tea.Quit
	case "up", "ctrl+p":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down":
		if m.cursor < len(m.visibleRows())-1 {
			m.cursor++
		}
		return m, nil
	case "ctrl+s": // scope to this dir (moved off ^c so ^c can quit, gap #6)
		m.scope = scopeThisDir
		m.cursor = 0
		return m, nil
	case "ctrl+a": // `^a`: all dirs
		m.scope = scopeAll
		m.cursor = 0
		return m, nil
	case "enter":
		if r, ok := m.cursorRow(); ok {
			m.selected = r.TmuxName
			return m, tea.Quit
		}
		return m, nil
	case "ctrl+r":
		return m.startRename()
	case "ctrl+n":
		if r, ok := m.cursorRow(); ok {
			return m.runAction(m.nameNowCmd(r.TmuxName))
		}
		return m, nil
	case "ctrl+k":
		if r, ok := m.cursorRow(); ok {
			m.mode = modeConfirm
			m.killFor = r.TmuxName
			m.status = ""
			m.statusErr = false
		}
		return m, nil
	case "ctrl+u":
		// Only meaningful when the background check found a newer release; quit and
		// let the caller (runResume) self-update after the TUI tears down.
		if m.updateLatest != "" {
			m.doUpdate = true
			return m, tea.Quit
		}
		return m, nil
	}

	// Always-on fuzzy filter: printable runes append, backspace deletes.
	switch msg.Type {
	case tea.KeyRunes:
		m.filter += string(msg.Runes)
		m.cursor = 0
		return m, nil
	case tea.KeySpace:
		m.filter += " "
		m.cursor = 0
		return m, nil
	case tea.KeyBackspace:
		if m.filter != "" {
			r := []rune(m.filter)
			m.filter = string(r[:len(r)-1])
			m.cursor = 0
		}
		return m, nil
	}
	return m, nil
}

// startRename opens the rename overlay prefilled with the cursor row's current
// display name.
func (m model) startRename() (tea.Model, tea.Cmd) {
	r, ok := m.cursorRow()
	if !ok {
		return m, nil
	}
	m.mode = modeInput
	m.renameFor = r.TmuxName
	m.inputErr = ""
	m.input.SetValue(r.Display)
	m.input.CursorEnd()
	return m, m.input.Focus()
}

// handleInputKey owns the rename overlay: enter submits the raw-UTF-8 name via
// renameCmd, esc cancels back to browse. An empty name is rejected in-place.
func (m model) handleInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeBrowse
		m.inputErr = ""
		m.input.Blur()
		return m, nil
	case "enter":
		name := strings.TrimSpace(m.input.Value())
		if name == "" {
			m.inputErr = "name cannot be empty"
			return m, nil
		}
		target := m.renameFor
		m.mode = modeBrowse
		m.inputErr = ""
		m.input.Blur()
		return m.runAction(m.renameCmd(target, name))
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// handleConfirmKey owns the kill confirmation: y kills via killCmd, n/esc
// cancels.
func (m model) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		target := m.killFor
		m.mode = modeBrowse
		return m.runAction(m.killCmd(target))
	case "n", "N", "esc":
		m.mode = modeBrowse
		m.status = "cancelled"
		m.statusErr = false
	}
	return m, nil
}

// visibleRows applies the dir scope (^s this-dir / ^a all) then the always-on
// fuzzy filter then the attached-then-recency rank to the rows. The rows are
// already ranked by the Service, but re-ranking after a scope/filter keeps the
// order stable as the subset changes.
func (m model) visibleRows() []rowmodel.Row {
	rows := m.rows
	if m.scope == scopeThisDir && m.cwdDir != "" {
		scoped := make([]rowmodel.Row, 0, len(rows))
		for _, r := range rows {
			if r.Dir == m.cwdDir {
				scoped = append(scoped, r)
			}
		}
		rows = scoped
	}
	rows = rowmodel.Filter(rows, m.filter)
	return rowmodel.Rank(rows)
}

// cursorRow returns the row under the cursor, ok=false when the visible set is
// empty or the cursor is out of range.
func (m model) cursorRow() (rowmodel.Row, bool) {
	rows := m.visibleRows()
	if m.cursor < 0 || m.cursor >= len(rows) {
		return rowmodel.Row{}, false
	}
	return rows[m.cursor], true
}

// ---- Styles ----

// The palette uses lipgloss.AdaptiveColor so the picker is readable on BOTH a
// light- and a dark-background terminal: lipgloss detects the terminal
// background once (the first AdaptiveColor it resolves is the attachedGlyph
// below, at package init — before bubbletea grabs the TTY — so the answer is
// cached before any frame renders) and picks the Light or Dark variant. The
// previous fixed near-white foregrounds (#E5E7EB / #FAFAFA) washed out to
// near-invisible on a light-background terminal. NOTE: if the terminal does not
// answer the OSC-11 background query, lipgloss falls back to the Dark variant —
// the readable-on-dark set — so a detection miss degrades to the old look, not
// to unreadable.
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FAFAFA")).
			Background(lipgloss.Color("#7D56F4")).
			Padding(0, 1)

	hubLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#4B5563", Dark: "#9CA3AF"})

	filterTextStyle  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#111827", Dark: "#FAFAFA"})
	filterCaretStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"})

	displayStyle    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#1F2937", Dark: "#E5E7EB"})
	displaySelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#F9FAFB", Dark: "#1F2937"}).
			Background(lipgloss.AdaptiveColor{Light: "#374151", Dark: "#E5E7EB"})
	dirStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#0369A1", Dark: "#7DD3FC"})
	ageStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))

	attachedGlyph = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#059669", Dark: "#34D399"}).Bold(true).Render("●")
	liveGlyph     = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#D97706", Dark: "#FBBF24"}).Render("◐")
	idleGlyph     = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Render("○")
	// loopGlyph marks a session running a /loop (pinned to the top). The recycle
	// arrow reads as "running on a loop"; the purple ties it to duck's accent.
	loopGlyph = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"}).Bold(true).Render("↻")
)

// idleThreshold splits "live-detached" (◐) from "idle/old" (○) by recency.
// DESIGN §6's example bounds it to (1h, 3h] (1h detached → ◐, 3h detached → ○);
// 2h is a guess in that window. FLAG for the integrator: confirm/centralize this
// constant if liveness should key on something stronger than session_activity.
const idleThreshold = 2 * time.Hour

// glyphFor maps state to the picker's status glyph: ↻ looped (running a /loop —
// pinned at the top, outranks everything), ● attached, ◐ live-detached (active
// within idleThreshold), ○ idle/old. It is a pure function of the looped/attached
// flags and the last-active age so it is unit-testable without a wall clock
// (renderRow passes time.Since(r.LastSeen)). Looped is checked first so a running
// loop is always recognisable even when also attached.
func glyphFor(looped, attached bool, age time.Duration) string {
	switch {
	case looped:
		return loopGlyph
	case attached:
		return attachedGlyph
	case age < idleThreshold:
		return liveGlyph
	default:
		return idleGlyph
	}
}

var (
	mutedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Italic(true)
	errStyle     = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#DC2626", Dark: "#F87171"}).Bold(true)
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#059669", Dark: "#34D399"}).Bold(true)
	helpStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	keyStyle     = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"}).Bold(true)
	spinnerStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"})
	updateStyle  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}).Bold(true)

	cardStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#7D56F4")).
			Padding(1, 2)

	// searchBoxStyle is the persistent, full-width filter box at the top of the
	// picker (Claude-Code-resume style). Width is set per-render to span the term.
	searchBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.AdaptiveColor{Light: "#9CA3AF", Dark: "#4B5563"}).
			Padding(0, 1)
	searchIconStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"})
	caretStyle      = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"}).Bold(true)
)

// ---- View ----

func (m model) View() string {
	// Split layout: header + search box + list sit at the TOP, while the status +
	// footer hints are PINNED to the bottom edge, with blank space filling the
	// middle when the list is short. The search box lives at the top (with the
	// list it filters) so typing and results stay together.
	var tb strings.Builder
	tb.WriteString(m.headerView())
	tb.WriteString("\n\n")
	if sv := m.searchView(); sv != "" {
		tb.WriteString(sv)
		tb.WriteString("\n\n")
	}
	tb.WriteString(m.bodyView())
	top := tb.String()

	var bb strings.Builder
	if s := m.statusView(); s != "" {
		bb.WriteString(s)
		bb.WriteString("\n")
	}
	bb.WriteString(m.footerView())
	bottom := bb.String()

	// Render hardening (ported from flok): bound every line to the width so
	// nothing wraps into ghost rows.
	if m.width > 0 {
		st := lipgloss.NewStyle().MaxWidth(m.width)
		top = st.Render(top)
		bottom = st.Render(bottom)
	}
	if m.height <= 0 {
		return top + "\n\n" + bottom
	}
	// Push the bottom block to the terminal's bottom edge: the gap between top and
	// bottom absorbs all the slack. The +1 accounts for the newline that ends the
	// top block's last line, so the footer lands flush on the bottom row (N blank
	// lines between needs N+1 newlines). visibleWindow caps the list so both blocks
	// fit; clamp to ≥1 (blocks adjacent) as a safety net against overflow.
	gap := m.height - lipgloss.Height(top) - lipgloss.Height(bottom) + 1
	if gap < 1 {
		gap = 1
	}
	return top + strings.Repeat("\n", gap) + bottom
}

func (m model) headerView() string {
	title := titleStyle.Render("duck")
	var right string
	if m.busy {
		right = m.spinner.View() + " " + hubLabelStyle.Render("working…")
	} else {
		switch m.state {
		case stateLoading:
			right = hubLabelStyle.Render("loading…")
		case stateError:
			right = errStyle.Render("error")
		case stateLoaded:
			// Count the visible (scoped + filtered) rows so the header tracks what's
			// shown; name the folder when scoped so the narrowing is never a mystery.
			n := len(m.visibleRows())
			label := plural2(n, "session", "sessions")
			if m.scope == scopeThisDir && m.cwdDir != "" {
				label += " in " + baseName(m.cwdDir)
			}
			right = hubLabelStyle.Render(label)
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Center, title, "  ", right)
}

// searchView is the persistent, full-width search box at the top of the picker
// (Claude-Code-resume style): always visible while browsing so the always-on
// fuzzy filter has an obvious home — a placeholder when empty, the live query
// (with a caret) when typing. It spans the terminal width so the picker reads as
// a full-screen app, not a top-left blob. Hidden during loading/overlays.
func (m model) searchView() string {
	if m.state != stateLoaded || m.mode != modeBrowse {
		return ""
	}
	w := m.width
	if w <= 0 {
		w = 80
	}
	// Border (2) + horizontal padding (2) sit outside the content width, so set
	// the content width to w-4 to make the whole box span the terminal.
	inner := w - 4
	if inner < 10 {
		inner = 10
	}
	var content string
	if m.filter == "" {
		content = searchIconStyle.Render("⌕ ") + mutedStyle.Render("type to filter…")
	} else {
		content = searchIconStyle.Render("⌕ ") + filterTextStyle.Render(m.filter) + filterCaretStyle.Render("▏")
	}
	return searchBoxStyle.Width(inner).Render(content)
}

func (m model) bodyView() string {
	if m.mode == modeInput {
		return m.inputCardView()
	}
	if m.mode == modeConfirm {
		return m.confirmCardView()
	}
	switch m.state {
	case stateLoading:
		return mutedStyle.Render("loading sessions from hub…")
	case stateError:
		return cardStyle.
			BorderForeground(lipgloss.Color("#F87171")).
			Render(errStyle.Render("✗ ") + m.err.Error())
	}

	rows := m.visibleRows()
	if len(rows) == 0 {
		// Distinguish "nothing in this folder" (other folders have sessions — point
		// at ^a) from "nothing on the hub at all", so the default scoping never reads
		// as an empty hub.
		if m.scope == scopeThisDir && m.cwdDir != "" && m.filter == "" && len(m.rows) > 0 {
			msg := "No sessions in " + baseName(m.cwdDir) + " — press ^a to show all " +
				plural2(len(m.rows), "session", "sessions") + "."
			return cardStyle.Render(mutedStyle.Render(msg))
		}
		return cardStyle.Render(mutedStyle.Render("No sessions on the hub yet."))
	}

	start, end := m.visibleWindow()
	var sb strings.Builder
	if start > 0 {
		sb.WriteString(mutedStyle.Render("  ↑ more"))
		sb.WriteString("\n")
	}
	for i := start; i < end; i++ {
		sb.WriteString(m.renderRow(rows[i], i == m.cursor))
		if i < end-1 {
			sb.WriteString("\n")
		}
	}
	if end < len(rows) {
		sb.WriteString("\n")
		sb.WriteString(mutedStyle.Render("  ↓ more"))
	}
	return sb.String()
}

// renderRow renders a single session row spanning the FULL terminal width: a
// selection caret, the liveness glyph (attached ● / live ◐ / idle ○), the raw
// display name and dir filling the left, and the age + window count right-
// aligned to the terminal edge. Columns scale to the width so the row uses the
// whole screen (Claude-Code-resume feel) instead of a narrow left blob; each
// text column is truncated with lipgloss.Width so nothing wraps into a ghost row.
func (m model) renderRow(r rowmodel.Row, selected bool) string {
	w := m.width
	if w <= 0 {
		w = 80
	}
	caret := "  "
	if selected {
		caret = caretStyle.Render("› ")
	}
	glyph := glyphFor(r.Looped, r.Attached, time.Since(r.LastSeen))

	ageStr := r.Age
	winStr := itoa(r.Windows) + "w"
	rightW := lipgloss.Width(ageStr) + 2 + lipgloss.Width(winStr)

	// Left area = full width minus the caret+glyph prefix (4), the "  " between
	// name and dir (2), the right metadata block, and a 2-col gap before it. Split
	// it name/dir so both grow with the terminal. Subtracting all of it here makes
	// the assembled row exactly w wide (pad below lands at the 2-col gap).
	avail := w - 8 - rightW
	if avail < 20 {
		avail = 20
	}
	nameW := avail * 9 / 20 // ~45% to the name, the rest to the dir
	if nameW < 10 {
		nameW = 10
	}
	dirW := avail - nameW
	if dirW < 6 {
		dirW = 6
	}

	name := padTrunc(r.Display, nameW)
	if selected {
		name = displaySelStyle.Render(name)
	} else {
		name = displayStyle.Render(name)
	}
	dir := dirStyle.Render(padTrunc(r.Dir, dirW))

	left := caret + glyph + " " + name + "  " + dir
	leftW := 4 + nameW + 2 + dirW // caret(2)+glyph(1)+space(1) + name + "  " + dir
	pad := w - leftW - rightW
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + ageStyle.Render(ageStr) + "  " + ageStyle.Render(winStr)
}

// padTrunc fits s into exactly w display columns: truncating with an ellipsis
// when it overflows, padding with spaces when it underflows. Uses lipgloss.Width
// so multi-byte/wide runes are measured correctly.
func padTrunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s + strings.Repeat(" ", w-lipgloss.Width(s))
	}
	// Truncate rune-by-rune leaving room for a one-column ellipsis.
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > w {
		runes = runes[:len(runes)-1]
	}
	out := string(runes) + "…"
	if pad := w - lipgloss.Width(out); pad > 0 {
		out += strings.Repeat(" ", pad)
	}
	return out
}

func (m model) inputCardView() string {
	lines := []string{
		"Rename session",
		"",
		helpStyle.Render("Raw UTF-8 — spaces, caps, emoji all fine."),
		"",
		m.input.View(),
	}
	if m.inputErr != "" {
		lines = append(lines, "", errStyle.Render("✗ ")+m.inputErr)
	}
	return cardStyle.Render(strings.Join(lines, "\n"))
}

func (m model) confirmCardView() string {
	lines := []string{
		"Kill this session?",
		helpStyle.Render("Ends the remote tmux session and forgets its name."),
		"",
		keyStyle.Render("y") + helpStyle.Render(" yes") + "   " + keyStyle.Render("n") + helpStyle.Render(" no"),
	}
	return cardStyle.
		BorderForeground(lipgloss.Color("#F87171")).
		Render(strings.Join(lines, "\n"))
}

func (m model) statusView() string {
	if m.status == "" {
		return ""
	}
	if m.statusErr {
		return errStyle.Render("✗ ") + m.status
	}
	return successStyle.Render("✓ ") + m.status
}

// visibleWindow returns the [start, end) row indices to render, keeping the
// cursor in view and leaving room for chrome (ported from flok).
func (m model) visibleWindow() (int, int) {
	rows := m.visibleRows()
	// Split layout: the TOP block is header(2) + search box(3) + its blank(1) +
	// list; the bottom block (footer) is pinned to the bottom. chrome = header(2) +
	// search box+blank(4) + footer(3: rule + 2 spread rows) + one blank gap(1) =
	// 10. status adds its line, the update banner adds one footer line.
	chrome := 10
	if m.status != "" {
		chrome++
	}
	if m.updateLatest != "" {
		chrome++
	}
	avail := m.height - chrome
	if avail <= 0 || m.height == 0 {
		return 0, len(rows)
	}
	if len(rows) <= avail {
		return 0, len(rows)
	}
	start := m.cursor - avail/2
	if start < 0 {
		start = 0
	}
	end := start + avail
	if end > len(rows) {
		end = len(rows)
		start = end - avail
	}
	return start, end
}

type hint struct{ key, desc string }

// hintSeg renders one "key desc" hint segment.
func hintSeg(h hint) string {
	return keyStyle.Render(h.key) + " " + helpStyle.Render(h.desc)
}

// spreadHints lays the hint segments out JUSTIFIED across the full width w: the
// first segment starts at the left edge, the last ends at the right edge, and the
// slack is distributed evenly between them — so the footer spans the row instead
// of cramming into the bottom-left. Falls back to a " · " join when the segments
// can't fit (the View's MaxWidth then trims).
func spreadHints(w int, hs []hint) string {
	segs := make([]string, len(hs))
	total := 0
	for i, h := range hs {
		segs[i] = hintSeg(h)
		total += lipgloss.Width(segs[i])
	}
	if len(segs) == 0 {
		return ""
	}
	gaps := len(segs) - 1
	if w <= 0 || gaps == 0 {
		return strings.Join(segs, "  ")
	}
	space := w - total
	if space < gaps {
		return strings.Join(segs, helpStyle.Render(" · "))
	}
	each, extra := space/gaps, space%gaps
	var b strings.Builder
	for i, s := range segs {
		b.WriteString(s)
		if i < gaps {
			g := each
			if i < extra { // hand the remainder to the leftmost gaps
				g++
			}
			b.WriteString(strings.Repeat(" ", g))
		}
	}
	return b.String()
}

func (m model) footerView() string {
	w := m.width
	if w <= 0 {
		w = 80
	}
	sep := helpStyle.Render(strings.Repeat("─", w)) // full-width rule
	switch {
	case m.mode == modeInput:
		return sep + "\n" + spreadHints(w, []hint{{"⏎", "submit"}, {"esc", "cancel"}})
	case m.mode == modeConfirm:
		return sep + "\n" + spreadHints(w, []hint{{"y", "yes"}, {"n", "no"}})
	case m.state == stateError:
		return sep + "\n" + spreadHints(w, []hint{{"r", "retry"}, {"q", "quit"}})
	case m.state == stateLoaded:
		// Show the toggle that does something from HERE: scoped → offer ^a (all),
		// all → offer ^s (this folder). Clearer than a static "this/all".
		scopeHint := hint{"^s", "this dir"}
		if m.scope == scopeThisDir {
			scopeHint = hint{"^a", "all dirs"}
		}
		row1 := spreadHints(w, []hint{{"↑↓", "move"}, {"⏎", "attach"}, scopeHint, {"^c", "quit"}})
		// The always-on filter is invisible without a hint; advertise how to type
		// one and how to clear it (esc) so a narrowed list is never a dead end.
		filterHint := hint{"type", "to filter"}
		if m.filter != "" {
			filterHint = hint{"esc", "clear filter"}
		}
		row2 := spreadHints(w, []hint{{"^r", "rename"}, {"^n", "name-now"}, {"^k", "kill"}, filterHint})
		body := sep + "\n" + row1 + "\n" + row2
		// When the background check found a newer release, surface a one-line banner
		// above the hints with the ^u affordance (handled in handleBrowseKey).
		if m.updateLatest != "" {
			banner := updateStyle.Render("↑ duck "+m.updateLatest+" available") +
				helpStyle.Render(" — press ") + keyStyle.Render("^u") + helpStyle.Render(" to update")
			return banner + "\n" + body
		}
		return body
	default:
		return sep + "\n" + spreadHints(w, []hint{{"q", "quit"}})
	}
}

// baseName returns the last path segment of a tilde-form dir (~/dev/foo → foo)
// for the header's folder label. Mirrors names.Derive without importing it.
func baseName(dir string) string {
	d := strings.TrimRight(dir, "/")
	if i := strings.LastIndex(d, "/"); i >= 0 {
		d = d[i+1:]
	}
	if d == "" {
		return "~"
	}
	return d
}

// plural2 renders "N one" / "N many" for the header session count.
func plural2(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return itoa(n) + " " + many
}

// itoa avoids a strconv import for the single header use.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
