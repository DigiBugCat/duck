// Package picker is duck's one-shot popup list: a bubbletea/lipgloss vertical
// list with an inline fuzzy filter, run inside a tmux display-popup by the
// palette and fleet verbs. It exists because those verbs need a CHOICE, not a
// UI: the component renders candidates, lets the user narrow and pick, and
// returns the chosen item to the caller — the caller (the command layer)
// executes exactly one tmux verb after the program has torn down. Nothing in
// this package touches tmux, so there is no standing state and nothing to
// heal; every popup rebuilds its candidates fresh at open.
//
// The Model/Update/View shape and the render hardening are ported from
// internal/tui (the --resume picker), pared down to the popup's needs: no
// Service seam, no async loads — the items are handed in complete.
package picker

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Item is one selectable row. Label is what the filter matches and the row
// shows; Detail, when set, renders as a dimmed second line (the fleet's
// task/activity line).
type Item struct {
	Label  string
	Detail string
}

// Options configures one picker run.
type Options struct {
	Title string
	Items []Item
	// ExtraKeys are single-rune keys (besides enter) that commit the cursor
	// row, reported back in Result.Key — e.g. the fleet's "x" (kill). While a
	// filter is being typed they revert to plain filter runes, exactly like
	// j/k, so labels containing those letters stay typeable.
	ExtraKeys []string
	// NoFilter disables inline filter typing entirely (the fleet is a short
	// action list, so j/k/x must always be verbs, never query runes).
	NoFilter bool
}

// Result reports how a run ended. Index is the chosen item's position in
// Options.Items (-1 when cancelled); Key is "enter" or the ExtraKeys entry
// that committed it ("" when cancelled).
type Result struct {
	Index int
	Key   string
}

// Run shows the picker and blocks until a choice or a cancel. The caller acts
// on the Result AFTER the TUI has torn down, so its tmux verb owns a clean TTY.
func Run(o Options) (Result, error) {
	// Pin the background detection before bubbletea owns stdin (same rationale
	// as internal/tui: a lazy OSC-11 probe mid-loop never gets its answer and
	// every adaptive color falls back to Dark).
	lipgloss.SetHasDarkBackground(lipgloss.HasDarkBackground())
	m := model{opts: o, visible: visibleIndices(o.Items, ""), chosen: -1}
	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return Result{Index: -1}, err
	}
	fm, ok := final.(model)
	if !ok || fm.chosen < 0 {
		return Result{Index: -1}, nil
	}
	return Result{Index: fm.chosen, Key: fm.chosenKey}, nil
}

// Match reports whether every rune of query appears in s, in order
// (case-insensitive subsequence match — the same lightweight fuzzy the
// --resume picker uses; no scoring, the candidate lists are small).
func Match(query, s string) bool {
	q := []rune(strings.ToLower(query))
	if len(q) == 0 {
		return true
	}
	i := 0
	for _, r := range strings.ToLower(s) {
		if r == q[i] {
			i++
			if i == len(q) {
				return true
			}
		}
	}
	return false
}

// visibleIndices returns the positions in items whose Label matches the
// filter, preserving order. Indices (not copies) so a choice maps straight
// back to the caller's slice.
func visibleIndices(items []Item, filter string) []int {
	var vis []int
	for i, it := range items {
		if Match(filter, it.Label) {
			vis = append(vis, i)
		}
	}
	return vis
}

type model struct {
	opts    Options
	filter  string
	visible []int // indices into opts.Items after the filter
	cursor  int   // position within visible
	width   int
	height  int

	chosen    int    // committed item index; -1 = cancelled
	chosenKey string // "enter" or the ExtraKeys rune that committed
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// handleKey: arrows always navigate; j/k (and the ExtraKeys) act as verbs only
// while no filter is being typed, then become plain filter runes — mirroring
// the --resume picker's bare-q rule so any label stays typeable. esc clears an
// active filter first, then cancels.
func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := msg.String()
	switch s {
	case "ctrl+c":
		return m.cancel()
	case "esc":
		if m.filter != "" {
			return m.setFilter(""), nil
		}
		return m.cancel()
	case "up", "ctrl+p":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "ctrl+n":
		if m.cursor < len(m.visible)-1 {
			m.cursor++
		}
		return m, nil
	case "enter":
		return m.commit("enter")
	}
	if m.filter == "" || m.opts.NoFilter {
		switch s {
		case "q":
			return m.cancel()
		case "j":
			if m.cursor < len(m.visible)-1 {
				m.cursor++
			}
			return m, nil
		case "k":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		}
		for _, k := range m.opts.ExtraKeys {
			if s == k {
				return m.commit(k)
			}
		}
	}
	if m.opts.NoFilter {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyRunes:
		return m.setFilter(m.filter + string(msg.Runes)), nil
	case tea.KeySpace:
		return m.setFilter(m.filter + " "), nil
	case tea.KeyBackspace:
		if m.filter != "" {
			r := []rune(m.filter)
			return m.setFilter(string(r[:len(r)-1])), nil
		}
	}
	return m, nil
}

func (m model) cancel() (tea.Model, tea.Cmd) {
	m.chosen = -1
	m.chosenKey = ""
	return m, tea.Quit
}

func (m model) commit(key string) (tea.Model, tea.Cmd) {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return m, nil
	}
	m.chosen = m.visible[m.cursor]
	m.chosenKey = key
	return m, tea.Quit
}

func (m model) setFilter(f string) model {
	m.filter = f
	m.visible = visibleIndices(m.opts.Items, f)
	m.cursor = 0
	return m
}

// ---- Styles (the internal/tui palette, popup-sized) ----

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FAFAFA")).
			Background(lipgloss.Color("#7D56F4")).
			Padding(0, 1)

	filterStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#111827", Dark: "#FAFAFA"})
	caretStyle    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"}).Bold(true)
	mutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Italic(true)
	detailStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#4B5563", Dark: "#9CA3AF"})
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"}).Bold(true)
	helpStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	keyStyle      = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"}).Bold(true)
)

func (m model) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(m.opts.Title))
	b.WriteString("\n")
	if !m.opts.NoFilter {
		if m.filter == "" {
			b.WriteString(mutedStyle.Render("type to filter…"))
		} else {
			b.WriteString(caretStyle.Render("⌕ ") + filterStyle.Render(m.filter) + caretStyle.Render("▏"))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	if len(m.visible) == 0 {
		b.WriteString(mutedStyle.Render("nothing matches"))
		b.WriteString("\n")
	}
	start, end := m.window()
	if start > 0 {
		b.WriteString(mutedStyle.Render("  ↑ more") + "\n")
	}
	for vi := start; vi < end; vi++ {
		it := m.opts.Items[m.visible[vi]]
		if vi == m.cursor {
			b.WriteString(selectedStyle.Render("› " + it.Label))
		} else {
			b.WriteString("  " + it.Label)
		}
		b.WriteString("\n")
		if it.Detail != "" {
			b.WriteString(detailStyle.Render("    " + it.Detail))
			b.WriteString("\n")
		}
	}
	if end < len(m.visible) {
		b.WriteString(mutedStyle.Render("  ↓ more") + "\n")
	}

	b.WriteString("\n")
	hints := []string{keyStyle.Render("↑↓/jk") + helpStyle.Render(" move"), keyStyle.Render("⏎") + helpStyle.Render(" select")}
	for _, k := range m.opts.ExtraKeys {
		hints = append(hints, keyStyle.Render(k)+helpStyle.Render(" act"))
	}
	hints = append(hints, keyStyle.Render("esc")+helpStyle.Render(" cancel"))
	b.WriteString(strings.Join(hints, helpStyle.Render(" · ")))

	out := b.String()
	// Render hardening (ported from internal/tui): bound every line to the
	// popup width so nothing wraps into ghost rows.
	if m.width > 0 {
		out = lipgloss.NewStyle().MaxWidth(m.width).Render(out)
	}
	return out
}

// window returns the [start, end) range of visible rows to render, keeping
// the cursor in view inside the popup height. Detail lines make row heights
// uneven; budgeting every row at its worst case (2 lines) keeps the math
// simple and always fits.
func (m model) window() (int, int) {
	rows := len(m.visible)
	if m.height <= 0 {
		return 0, rows
	}
	chrome := 5 // title + filter + blank + blank + hints
	avail := (m.height - chrome) / 2
	if avail < 1 {
		avail = 1
	}
	if rows <= avail {
		return 0, rows
	}
	start := m.cursor - avail/2
	if start < 0 {
		start = 0
	}
	end := start + avail
	if end > rows {
		end = rows
		start = end - avail
	}
	return start, end
}

// Confirm shows a one-line Y/n prompt as its own tiny program (the palette's
// kill-workspace guard). Enter accepts the default; an explicit n/N or escape
// cancels.
func Confirm(prompt string) (bool, error) {
	final, err := tea.NewProgram(confirmModel{prompt: prompt}).Run()
	if err != nil {
		return false, err
	}
	cm, ok := final.(confirmModel)
	return ok && cm.yes, nil
}

type confirmModel struct {
	prompt string
	yes    bool
}

func (c confirmModel) Init() tea.Cmd { return nil }

func (c confirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "enter", "y", "Y":
			c.yes = true
			return c, tea.Quit
		case "esc", "n", "N":
			return c, tea.Quit
		}
	}
	return c, nil
}

func (c confirmModel) View() string {
	return c.prompt + " " + keyStyle.Render("Y") + helpStyle.Render("/n ")
}
