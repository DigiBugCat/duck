package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	rowmodel "github.com/DigiBugCat/duck/internal/model"
)

// ---- fake Service (ported from flok's fakeService seam) ----

type fakeService struct {
	rows      []rowmodel.Row
	rename    []struct{ tmux, display string }
	nameNow   []string
	kill      []string
	revive    []string
	refreshN  int
	nameTitle string
}

func (f *fakeService) Sessions() []rowmodel.Row { return f.rows }
func (f *fakeService) Refresh() ([]rowmodel.Row, error) {
	f.refreshN++
	return f.rows, nil
}
func (f *fakeService) Attach(string) error { return nil }
func (f *fakeService) Rename(tmux, display string) error {
	f.rename = append(f.rename, struct{ tmux, display string }{tmux, display})
	return nil
}
func (f *fakeService) NameNow(tmux string) (string, error) {
	f.nameNow = append(f.nameNow, tmux)
	if f.nameTitle == "" {
		return "Generated", nil
	}
	return f.nameTitle, nil
}
func (f *fakeService) Kill(tmux string) error {
	f.kill = append(f.kill, tmux)
	return nil
}
func (f *fakeService) Revive(tmux string) error {
	f.revive = append(f.revive, tmux)
	return nil
}

// ---- helpers (ported runeKey / upd / drain driver) ----

func runeKey(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

var keyEnter = tea.KeyMsg{Type: tea.KeyEnter}

func ctrl(s string) tea.KeyMsg {
	switch s {
	case "r":
		return tea.KeyMsg{Type: tea.KeyCtrlR}
	case "n":
		return tea.KeyMsg{Type: tea.KeyCtrlN}
	case "k":
		return tea.KeyMsg{Type: tea.KeyCtrlK}
	case "a":
		return tea.KeyMsg{Type: tea.KeyCtrlA}
	case "c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	}
	panic("unknown ctrl key " + s)
}

func upd(m model, msg tea.Msg) (model, tea.Cmd) {
	nm, cmd := m.Update(msg)
	return nm.(model), cmd
}

func drain(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, drain(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// drainHasQuit reports whether running cmd (recursively, through batches) emits
// a tea.QuitMsg — the headless equivalent of "this keypress quit the picker".
func drainHasQuit(cmd tea.Cmd) bool {
	for _, msg := range drain(cmd) {
		if _, ok := msg.(tea.QuitMsg); ok {
			return true
		}
	}
	return false
}

func actionResult(cmd tea.Cmd) (actionDoneMsg, bool) {
	for _, msg := range drain(cmd) {
		if a, ok := msg.(actionDoneMsg); ok {
			return a, true
		}
	}
	return actionDoneMsg{}, false
}

func sampleRows() []rowmodel.Row {
	return []rowmodel.Row{
		{Display: "Auth Refactor", Dir: "~/dev/auth", Age: "2m", TmuxName: "auth", Attached: true},
		{Display: "Billing API", Dir: "~/dev/billing", Age: "1h", TmuxName: "billing"},
		{Display: "Web", Dir: "~/dev/web", Age: "3d", TmuxName: "web"},
	}
}

// loadedModel returns a model in stateLoaded backed by the fake service.
func loadedModel(svc *fakeService) model {
	m := initialModel(svc)
	m.cwdDir = "~/dev/auth"
	m, _ = upd(m, loadedMsg{rows: svc.rows})
	return m
}

// ---- tests ----

func TestLoadCmdCallsRefresh(t *testing.T) {
	f := &fakeService{rows: sampleRows()}
	msgs := drain(loadCmd(f))
	if f.refreshN != 1 {
		t.Fatalf("loadCmd must call Refresh, got %d calls", f.refreshN)
	}
	if len(msgs) != 1 {
		t.Fatalf("loadCmd should emit one msg, got %d", len(msgs))
	}
	if lm, ok := msgs[0].(loadedMsg); !ok || len(lm.rows) != 3 {
		t.Fatalf("loadCmd should emit loadedMsg with the refreshed rows, got %T", msgs[0])
	}
}

func TestVisibleRowsFiltersAndRanks(t *testing.T) {
	f := &fakeService{rows: sampleRows()}
	m := loadedModel(f)
	// All rows visible, attached-first (auth is attached).
	vis := m.visibleRows()
	if len(vis) != 3 || vis[0].TmuxName != "auth" {
		t.Fatalf("unfiltered visibleRows should rank attached first, got %+v", names(vis))
	}
	// Fuzzy filter narrows to the matching row.
	m.filter = "bill"
	vis = m.visibleRows()
	if len(vis) != 1 || vis[0].TmuxName != "billing" {
		t.Fatalf("filter 'bill' should show only billing, got %+v", names(vis))
	}
}

// TestScopedPickerViews pins the current-folder default UX: the footer offers
// the toggle that does something from the active scope, the header names the
// folder when scoped, and an empty current folder points at ^a instead of
// reading as an empty hub.
func TestScopedPickerViews(t *testing.T) {
	f := &fakeService{rows: sampleRows()} // auth(~/dev/auth), billing, web
	m := loadedModel(f)                   // cwdDir=~/dev/auth, default scopeAll
	m.width = 100

	if got := m.footerView(); !strings.Contains(got, "this dir") {
		t.Fatalf("all-scope footer should offer ^s this dir:\n%s", got)
	}

	m.scope = scopeThisDir
	if got := m.footerView(); !strings.Contains(got, "all dirs") {
		t.Fatalf("this-dir footer should offer ^a all dirs:\n%s", got)
	}
	if got := m.headerView(); !strings.Contains(got, "auth") {
		t.Fatalf("scoped header should name the folder:\n%s", got)
	}

	// Current folder empty but other folders have sessions → guide to ^a.
	m.cwdDir = "~/dev/empty"
	if got := m.bodyView(); !strings.Contains(got, "^a") {
		t.Fatalf("empty-folder body should point at ^a:\n%s", got)
	}
}

// TestUpdateBannerAndCtrlU pins the in-picker updater: ^u is inert until the
// background check reports a newer release, after which the banner shows and ^u
// sets doUpdate and quits (the caller self-updates after teardown).
func TestUpdateBannerAndCtrlU(t *testing.T) {
	f := &fakeService{rows: sampleRows()}
	m := loadedModel(f)
	m.width = 100

	if strings.Contains(m.footerView(), "available") {
		t.Fatalf("no banner before an update msg:\n%s", m.footerView())
	}
	m2, cmd := upd(m, ctrl("u"))
	if m2.doUpdate {
		t.Fatalf("^u with no update available must not set doUpdate")
	}
	if drainHasQuit(cmd) {
		t.Fatalf("^u with no update available must not quit")
	}

	m, _ = upd(m, UpdateAvailableMsg{Latest: "v0.9.9"})
	if m.updateLatest != "v0.9.9" {
		t.Fatalf("UpdateAvailableMsg should set updateLatest, got %q", m.updateLatest)
	}
	if !strings.Contains(m.footerView(), "v0.9.9") {
		t.Fatalf("footer should show the update banner:\n%s", m.footerView())
	}
	m, cmd = upd(m, ctrl("u"))
	if !m.doUpdate {
		t.Fatalf("^u with an update available should set doUpdate")
	}
	if !drainHasQuit(cmd) {
		t.Fatalf("^u with an update available should quit")
	}
}

func TestBaseName(t *testing.T) {
	for in, want := range map[string]string{
		"~/dev/foo":         "foo",
		"~/cassandra-stack": "cassandra-stack",
		"~":                 "~",
		"~/":                "~",
	} {
		if got := baseName(in); got != want {
			t.Errorf("baseName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScopeThisDirAndAll(t *testing.T) {
	f := &fakeService{rows: sampleRows()}
	m := loadedModel(f) // cwdDir = ~/dev/auth
	// ^s scopes to this dir (^c was rebound to quit, gap #6).
	m, _ = upd(m, ctrl("s"))
	if m.scope != scopeThisDir {
		t.Fatalf("^s should set this-dir scope, got %d", m.scope)
	}
	vis := m.visibleRows()
	if len(vis) != 1 || vis[0].Dir != "~/dev/auth" {
		t.Fatalf("this-dir scope should show only ~/dev/auth rows, got %+v", names(vis))
	}
	// ^a returns to all.
	m, _ = upd(m, ctrl("a"))
	if m.scope != scopeAll {
		t.Fatalf("^a should set all scope, got %d", m.scope)
	}
	if len(m.visibleRows()) != 3 {
		t.Fatalf("all scope should show all rows, got %d", len(m.visibleRows()))
	}
}

// TestCtrlCQuitsBrowse pins gap #6: ^c is the hard abort in browse mode (it was
// previously rebound to the this-dir scope, which violated the ^c-aborts
// convention). It must quit even with an active filter, since ^c is a control
// key that never hits the rune-append path.
func TestCtrlCQuitsBrowse(t *testing.T) {
	f := &fakeService{rows: sampleRows()}
	m := loadedModel(f)
	_, cmd := upd(m, ctrl("c"))
	if !drainHasQuit(cmd) {
		t.Fatalf("^c in browse should quit the picker")
	}
	// And it still quits mid-filter (where bare q would just be a rune).
	m2 := loadedModel(f)
	m2, _ = upd(m2, runeKey("que"))
	_, cmd2 := upd(m2, ctrl("c"))
	if !drainHasQuit(cmd2) {
		t.Fatalf("^c should quit even with an active filter")
	}
}

// TestErrorScreenRetryReloads pins that `r` on the error screen re-issues the
// load — the footer advertises {r retry · q quit}, and since session.List now
// PROPAGATES transport errors (instead of swallowing them as empty), the error
// screen is reachable on a dead hub, so its retry affordance must actually work.
// The guard is scoped to stateError so `r` stays a plain filter rune while
// browsing.
func TestErrorScreenRetryReloads(t *testing.T) {
	f := &fakeService{rows: sampleRows()}
	m := initialModel(f)
	// Drive the picker into the error state as a propagated hub failure would.
	m, _ = upd(m, errMsg{err: errInjected{}})
	if m.state != stateError {
		t.Fatalf("setup: model should be in stateError, got %d", m.state)
	}
	m, cmd := upd(m, runeKey("r"))
	if m.state != stateLoading {
		t.Fatalf("`r` on the error screen should re-enter stateLoading, got %d", m.state)
	}
	if m.err != nil {
		t.Fatalf("retry should clear the prior error, got %v", m.err)
	}
	// The emitted command must reload — draining it yields a fresh loadedMsg.
	var reloaded bool
	for _, msg := range drain(cmd) {
		if _, ok := msg.(loadedMsg); ok {
			reloaded = true
		}
	}
	if !reloaded {
		t.Fatalf("`r` retry must re-issue the load (expected a loadedMsg)")
	}
	// `r` must NOT shadow the filter rune during normal browsing.
	mb := loadedModel(f)
	mb, _ = upd(mb, runeKey("r"))
	if mb.filter != "r" {
		t.Fatalf("`r` while browsing should append to the filter, got %q", mb.filter)
	}
}

// errInjected is a stand-in hub error for the error-screen retry test.
type errInjected struct{}

func (errInjected) Error() string { return "hub unreachable" }

// TestEscClearsActiveFilterElseQuits pins the esc semantics that make the
// always-on filter undoable (gap #5/#6): esc clears a non-empty filter (staying
// in the picker), but with no filter it quits.
func TestEscClearsActiveFilterElseQuits(t *testing.T) {
	f := &fakeService{rows: sampleRows()}
	// Active filter: esc clears it and stays.
	m := loadedModel(f)
	m, _ = upd(m, runeKey("bill"))
	if m.filter != "bill" {
		t.Fatalf("setup: filter should be 'bill', got %q", m.filter)
	}
	m, cmd := upd(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.filter != "" {
		t.Fatalf("esc should clear an active filter, got %q", m.filter)
	}
	if drainHasQuit(cmd) {
		t.Fatalf("esc must NOT quit while a filter is active (it clears instead)")
	}
	// Empty filter: esc quits.
	m2 := loadedModel(f)
	_, cmd2 := upd(m2, tea.KeyMsg{Type: tea.KeyEsc})
	if !drainHasQuit(cmd2) {
		t.Fatalf("esc with an empty filter should quit")
	}
}

// TestFilterLineRendersWhenNonEmpty pins gap #5: the active filter is shown in
// the view when non-empty (so typing has a visible cause) and is absent when
// empty (no chrome for a no-op filter).
func TestSearchBoxRendersFilter(t *testing.T) {
	f := &fakeService{rows: sampleRows()}
	m := loadedModel(f)
	m.width = 100
	m.height = 24
	// The search box is always shown while browsing: placeholder when empty.
	if got := m.View(); !strings.Contains(got, "type to filter") {
		t.Fatalf("empty filter should show the search-box placeholder\n%s", got)
	}
	// Typing shows the query inside the box.
	m, _ = upd(m, runeKey("bill"))
	if got := m.View(); !strings.Contains(got, "bill") {
		t.Fatalf("non-empty filter must show the query in the search box\n%s", got)
	}
}

func TestEnterStashesSelectedAndQuits(t *testing.T) {
	f := &fakeService{rows: sampleRows()}
	m := loadedModel(f)
	m.cursor = 0 // auth (ranked first)
	m, cmd := upd(m, keyEnter)
	if m.selected != "auth" {
		t.Fatalf("enter should stash the cursor row's tmux name, got %q", m.selected)
	}
	// It must quit so the caller can exec the attach after teardown.
	var quit bool
	for _, msg := range drain(cmd) {
		if _, ok := msg.(tea.QuitMsg); ok {
			quit = true
		}
	}
	if !quit {
		t.Fatalf("enter should emit tea.Quit so Run can exec the attach")
	}
}

func TestRenameOverlayWritesNamesJSON(t *testing.T) {
	f := &fakeService{rows: sampleRows()}
	m := loadedModel(f)
	m.cursor = 0
	m, _ = upd(m, ctrl("r"))
	if m.mode != modeInput || m.renameFor != "auth" {
		t.Fatalf("^r should open rename for the cursor row, got mode=%d for=%q", m.mode, m.renameFor)
	}
	// Replace with a raw-UTF-8 name and submit.
	m.input.SetValue("My Cool Name 🚀")
	m, cmd := upd(m, keyEnter)
	if !m.busy {
		t.Fatalf("submitting rename should mark busy")
	}
	actionResult(cmd)
	if len(f.rename) != 1 || f.rename[0].tmux != "auth" || f.rename[0].display != "My Cool Name 🚀" {
		t.Fatalf("rename should call Rename(auth, raw name), got %+v", f.rename)
	}
}

func TestRenameEmptyRejected(t *testing.T) {
	f := &fakeService{rows: sampleRows()}
	m := loadedModel(f)
	m, _ = upd(m, ctrl("r"))
	m.input.SetValue("   ")
	m, _ = upd(m, keyEnter)
	if m.mode != modeInput || m.inputErr == "" {
		t.Fatalf("empty rename should keep the overlay open with an error")
	}
	if len(f.rename) != 0 {
		t.Fatalf("empty rename must not call Rename")
	}
}

func TestNameNowTriggersCodexReName(t *testing.T) {
	f := &fakeService{rows: sampleRows(), nameTitle: "Codex Title"}
	m := loadedModel(f)
	m.cursor = 1 // billing
	m, cmd := upd(m, ctrl("n"))
	if !m.busy {
		t.Fatalf("^n should mark busy")
	}
	res, ok := actionResult(cmd)
	if !ok || res.err != nil {
		t.Fatalf("^n should produce a successful actionDoneMsg, got %+v", res)
	}
	if len(f.nameNow) != 1 || f.nameNow[0] != "billing" {
		t.Fatalf("^n should call NameNow(billing), got %+v", f.nameNow)
	}
}

func TestKillConfirmFlow(t *testing.T) {
	f := &fakeService{rows: sampleRows()}
	m := loadedModel(f)
	m.cursor = 2 // web
	m, _ = upd(m, ctrl("k"))
	if m.mode != modeConfirm || m.killFor != "web" {
		t.Fatalf("^k should open kill confirm for the cursor row, got mode=%d for=%q", m.mode, m.killFor)
	}
	// Cancel: no kill.
	mc, _ := upd(m, runeKey("n"))
	if mc.mode != modeBrowse || len(f.kill) != 0 {
		t.Fatalf("n should cancel the kill, calls=%+v", f.kill)
	}
	// Confirm: kills.
	my, cmd := upd(m, runeKey("y"))
	if my.mode != modeBrowse {
		t.Fatalf("y should return to browse")
	}
	actionResult(cmd)
	if len(f.kill) != 1 || f.kill[0] != "web" {
		t.Fatalf("y should call Kill(web), got %+v", f.kill)
	}
}

func TestFilterTypingAndBackspace(t *testing.T) {
	f := &fakeService{rows: sampleRows()}
	m := loadedModel(f)
	m, _ = upd(m, runeKey("au"))
	if m.filter != "au" {
		t.Fatalf("typing should build the filter, got %q", m.filter)
	}
	m, _ = upd(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.filter != "a" {
		t.Fatalf("backspace should trim the filter, got %q", m.filter)
	}
}

func TestBareQQuitsOnlyWhenFilterEmpty(t *testing.T) {
	f := &fakeService{rows: sampleRows()}
	// Empty filter: bare q quits.
	m := loadedModel(f)
	_, cmd := upd(m, runeKey("q"))
	var quit bool
	for _, msg := range drain(cmd) {
		if _, ok := msg.(tea.QuitMsg); ok {
			quit = true
		}
	}
	if !quit {
		t.Fatalf("bare q with an empty filter should quit")
	}
	// Active filter: q is a filter rune, not a quit.
	m2 := loadedModel(f)
	m2, _ = upd(m2, runeKey("que")) // start a filter
	m2, cmd2 := upd(m2, runeKey("q"))
	if m2.filter != "queq" {
		t.Fatalf("q should extend an active filter, got %q", m2.filter)
	}
	for _, msg := range drain(cmd2) {
		if _, ok := msg.(tea.QuitMsg); ok {
			t.Fatalf("q must not quit while a filter is active")
		}
	}
}

func TestActionRefreshTriggersReload(t *testing.T) {
	f := &fakeService{rows: sampleRows()}
	m := loadedModel(f)
	m, cmd := upd(m, actionDoneMsg{summary: "done", refresh: true})
	if m.state != stateLoading {
		t.Fatalf("refresh should move to loading, got %d", m.state)
	}
	msgs := drain(cmd)
	if len(msgs) != 1 {
		t.Fatalf("reload should emit one msg, got %d", len(msgs))
	}
	if _, ok := msgs[0].(loadedMsg); !ok {
		t.Fatalf("reload cmd should produce loadedMsg, got %T", msgs[0])
	}
}

func TestBusyIgnoresActionKeys(t *testing.T) {
	f := &fakeService{rows: sampleRows()}
	m := loadedModel(f)
	m.busy = true
	m, _ = upd(m, ctrl("k"))
	if m.mode == modeConfirm {
		t.Fatalf("keys should be ignored while busy")
	}
}

// TestThreeSessionsSameDirRenderDistinctRawNames is the milestone's headline
// guarantee: N sessions in ONE dir render as N distinct rows by their RAW
// display names, and the internal tmux disambiguation suffixes (-2/-3/-4) never
// surface anywhere in the rendered output. This is the whole reason duck exists
// (DESIGN §3): the internal tmux id (foo/foo-2/foo-3) is decoupled from the
// display label. Driven through the real render path (bodyView uses r.Display,
// never r.TmuxName) so it proves what the user actually sees.
func TestThreeSessionsSameDirRenderDistinctRawNames(t *testing.T) {
	rows := []rowmodel.Row{
		{Display: "Auth flow debugging", Dir: "~/dev/foo", Age: "2m", TmuxName: "foo", Attached: true},
		{Display: "Refactor cookie refresh", Dir: "~/dev/foo", Age: "14m", TmuxName: "foo-2"},
		{Display: "Stock thesis alerts", Dir: "~/dev/foo", Age: "1h", TmuxName: "foo-3"},
	}
	f := &fakeService{rows: rows}
	m := loadedModel(f)
	m.cwdDir = "~/dev/foo"
	m.width = 100
	m.height = 24

	// Three distinct rows are visible (all in the same dir).
	if vis := m.visibleRows(); len(vis) != 3 {
		t.Fatalf("3 sessions in one dir should yield 3 visible rows, got %d (%v)", len(vis), names(vis))
	}

	out := m.bodyView()
	// Every RAW display name renders.
	for _, want := range []string{"Auth flow debugging", "Refactor cookie refresh", "Stock thesis alerts"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered body must contain raw display name %q\n%s", want, out)
		}
	}
	// The internal tmux suffixes must NEVER surface as a label.
	for _, slug := range []string{"foo-2", "foo-3", "foo-4"} {
		if strings.Contains(out, slug) {
			t.Fatalf("rendered body leaked internal tmux slug %q (the -2/-3/-4 ugliness must not surface)\n%s", slug, out)
		}
	}
}


// TestRenderRowWiresAttachedGlyph locks renderRow's call into glyphFor: an
// attached row must render the attached glyph regardless of age (a swapped
// argument would otherwise slip through, since glyphFor is only tested in
// isolation). Deterministic — no wall clock dependence for the attached case.
func TestRenderRowWiresAttachedGlyph(t *testing.T) {
	m := loadedModel(&fakeService{rows: sampleRows()})
	m.width = 80
	out := m.renderRow(rowmodel.Row{Display: "x", Dir: "~/dev/x", Age: "9h", Attached: true}, false)
	if !strings.Contains(out, "●") {
		t.Fatalf("renderRow of an attached row must show the attached glyph ●, got %q", out)
	}
}

// TestRenderRowSpansFullWidth pins the full-screen layout: even a row with short
// content fills the terminal width (metadata right-aligned to the edge), so the
// picker reads as a full-screen app rather than a narrow left column.
func TestRenderRowSpansFullWidth(t *testing.T) {
	m := loadedModel(&fakeService{rows: sampleRows()})
	m.width = 120
	out := m.renderRow(rowmodel.Row{Display: "x", Dir: "~/dev/x", Age: "1m"}, false)
	if w := lineWidth(out); w != m.width {
		t.Fatalf("short row should span the full width %d, got %d", m.width, w)
	}
}

func TestRenderRowTruncatesLongName(t *testing.T) {
	m := loadedModel(&fakeService{rows: sampleRows()})
	m.width = 80
	long := rowmodel.Row{Display: "this is an extremely long session display name that overflows", Dir: "~/dev/x", Age: "1m"}
	out := m.renderRow(long, false)
	if out == "" {
		t.Fatalf("renderRow should produce output")
	}
	// The hardening: a single rendered row must not exceed the terminal width.
	if w := lineWidth(out); w > m.width {
		t.Fatalf("rendered row width %d exceeds terminal width %d", w, m.width)
	}
}

// ---- small helpers ----

func names(rows []rowmodel.Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.TmuxName
	}
	return out
}

// lineWidth returns the widest visible line in s (ANSI-aware).
func lineWidth(s string) int {
	w := 0
	for _, line := range strings.Split(s, "\n") {
		if lw := lipgloss.Width(line); lw > w {
			w = lw
		}
	}
	return w
}


// TestEnterOnEvictedRowRevivesThenSelects pins the revive handoff: enter on an
// evicted row must NOT quit directly — it revives via the Service first, and
// only on success quits with the row selected so the caller attaches normally.
func TestEnterOnEvictedRowRevivesThenSelects(t *testing.T) {
	svc := &fakeService{rows: []rowmodel.Row{
		{Display: "gone", TmuxName: "gone", Dir: "~/dev/gone", Evicted: true},
	}}
	m := initialModel(svc)
	m, _ = upd(m, loadedMsg{rows: svc.rows})
	m, cmd := upd(m, keyEnter)
	if m.selected != "" {
		t.Fatal("enter on evicted row must not select before the revive completes")
	}
	done, ok := actionResult(cmd)
	if !ok {
		t.Fatal("enter on evicted row should dispatch the revive action")
	}
	if len(svc.revive) != 1 || svc.revive[0] != "gone" {
		t.Fatalf("revive calls = %v", svc.revive)
	}
	var quit tea.Cmd
	m, quit = upd(m, done)
	if m.selected != "gone" {
		t.Fatalf("after revive the row should be selected for attach, got %q", m.selected)
	}
	if !drainHasQuit(quit) {
		t.Fatal("revive completion should quit the picker into attach")
	}
}
