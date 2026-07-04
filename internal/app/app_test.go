package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/DigiBugCat/duck/internal/namer"
	"github.com/DigiBugCat/duck/internal/names"
	"github.com/DigiBugCat/duck/internal/session"
)

// fakeRunner backs both the session.Manager and the names.Store, recording
// every command string and returning canned output. No real host is touched.
type fakeRunner struct {
	cmds   []string
	inputs []string
	out    map[string]string
}

func (f *fakeRunner) Run(cmd string) (string, error) {
	f.cmds = append(f.cmds, cmd)
	if f.out != nil {
		if v, ok := f.out[cmd]; ok {
			return v, nil
		}
	}
	return "", nil
}
func (f *fakeRunner) RunInput(cmd string, stdin io.Reader) (string, error) {
	f.cmds = append(f.cmds, cmd)
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		f.inputs = append(f.inputs, string(b))
	}
	return "", nil
}

type fakeAttacher struct{ attached string }

func (f *fakeAttacher) ExecAttach(id string) error { f.attached = id; return nil }
func (f *fakeAttacher) RunAttach(id string) error  { f.attached = id; return nil }

const listCmd = "tmux list-sessions -F '#{session_name}\t#{@duck_dir}\t#{session_attached}\t#{session_activity}\t#{session_windows}\t#{@duck_loop}\t#{@duck_panel_of}\t#{pane_title}'"

func newApp(r *fakeRunner, n namer.Namer) *App {
	mgr := session.NewManager(r, &fakeAttacher{})
	store := names.NewStore(r)
	return New(mgr, store, n)
}

func TestRefreshResolvesRowsLiveAndRanks(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		listCmd: "auth\t~/dev/auth\t1\t9999999999\t2\n" +
			"web\t~/dev/web\t0\t100\t1\n",
		// names.json: a user-set name for auth.
		"cat ~/.duck/names.json 2>/dev/null || echo '{}'": `{"names":{"auth":{"userName":"My Auth"}}}`,
	}}
	a := newApp(r, namer.DirDerived{})
	rows, err := a.Refresh()
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	// Attached row ranks first; its display is the resolved user name (raw).
	if rows[0].TmuxName != "auth" || rows[0].Display != "My Auth" || !rows[0].Attached {
		t.Fatalf("row 0 wrong: %+v", rows[0])
	}
	// The detached row's display falls to the dir-derived floor.
	if rows[1].TmuxName != "web" || rows[1].Display != "web" {
		t.Fatalf("row 1 should derive 'web', got %+v", rows[1])
	}
	// Sessions() returns the cached set.
	if len(a.Sessions()) != 2 {
		t.Fatalf("cache not populated")
	}
}

func TestRenameWritesAtomicNamesJSON(t *testing.T) {
	r := &fakeRunner{}
	a := newApp(r, namer.DirDerived{})
	if err := a.Rename("auth", "Cool Name 🚀"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	// The write is the atomic temp+rename (single ssh call streaming JSON).
	var wrote string
	for _, c := range r.cmds {
		if strings.Contains(c, "mv") && strings.Contains(c, "names.json") {
			wrote = c
		}
	}
	if wrote == "" {
		t.Fatalf("Rename must perform the atomic names.json write; cmds=%v", r.cmds)
	}
	if len(r.inputs) == 0 || !strings.Contains(r.inputs[len(r.inputs)-1], "Cool Name 🚀") {
		t.Fatalf("the raw display name must be streamed into names.json: %v", r.inputs)
	}
}

// stubNamer returns a fixed title and a fixed captured head, counting the
// codex Name calls so a test can assert the cache skips the (quota-costing)
// call. A pointer receiver on Name lets the counter mutate.
type stubNamer struct {
	title     string
	head      string
	err       error // when non-nil, Name fails (exercises the dir-floor fallback)
	nameCalls int
}

func (s *stubNamer) Name(context.Context, string) (string, error) {
	s.nameCalls++
	if s.err != nil {
		return "", s.err
	}
	return s.title, nil
}
func (s *stubNamer) CaptureHead(string) (string, error) { return s.head, nil }

func TestNameNowPinsGeneratedNameAsUserName(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		"tmux show-options -t 'auth' -v '@duck_dir'": "~/dev/auth\n",
	}}
	sn := &stubNamer{title: "Codex Title", head: "pane head content"}
	a := newApp(r, sn)
	title, err := a.NameNow("auth")
	if err != nil {
		t.Fatalf("NameNow: %v", err)
	}
	if title != "Codex Title" {
		t.Fatalf("NameNow returned %q", title)
	}
	// It must PIN the generated name as the user name so it outranks the live pane
	// title in Resolve (otherwise ^n would be a silent no-op on Claude sessions).
	if len(r.inputs) == 0 {
		t.Fatalf("NameNow must write names.json")
	}
	written := r.inputs[len(r.inputs)-1]
	var got names.Names
	if err := json.Unmarshal([]byte(written), &got); err != nil {
		t.Fatalf("streamed JSON is invalid: %v", err)
	}
	if got.Names["auth"].UserName != "Codex Title" {
		t.Fatalf("generated name must be pinned as userName, got %+v", got.Names["auth"])
	}
	// And it must NOT land in the codex slot (which loses to the pane title).
	if got.Names["auth"].CodexName != "" {
		t.Fatalf("NameNow must not write codexName, got %q", got.Names["auth"].CodexName)
	}
}

// TestNameNowFallsToFloorWithoutPoisoningCacheOnError is the M1 failure-branch
// guard: when codex errors, NameNow returns the dir-derived floor for display
// but must NOT write the floor into the codex slots. Writing it (with a hash of
// the current head) would make namer.CacheHit report a hit on the next Refresh,
// silently freezing the floor and disabling auto-naming until the head changed.
// So: the floor is returned, and names.json is NOT rewritten.
func TestNameNowFallsToFloorWithoutPoisoningCacheOnError(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		"tmux show-options -t 'auth' -v '@duck_dir'": "~/dev/auth\n",
	}}
	sn := &stubNamer{head: "pane head content", err: errors.New("codex unavailable")}
	a := newApp(r, sn)

	title, err := a.NameNow("auth")
	if err != nil {
		t.Fatalf("NameNow must not fail on a codex error: %v", err)
	}
	// Display falls to the dir-derived floor (~/dev/auth ⇒ "auth").
	if title != "auth" {
		t.Fatalf("NameNow should return the dir-derived floor on codex error, got %q", title)
	}
	// The codex slots must be left untouched: no names.json write at all, so a
	// later refresh/NameNow retries naming instead of seeing a poisoned cache hit.
	for _, c := range r.cmds {
		if strings.Contains(c, "mv") && strings.Contains(c, "names.json") {
			t.Fatalf("NameNow must NOT write names.json on a codex error (cache poisoning): %s", c)
		}
	}
	if len(r.inputs) != 0 {
		t.Fatalf("NameNow must not stream any names.json on a codex error: %v", r.inputs)
	}
}

func TestKillForgetsNamesEntry(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		"cat ~/.duck/names.json 2>/dev/null || echo '{}'": `{"names":{"web":{"userName":"Web"}}}`,
	}}
	a := newApp(r, namer.DirDerived{})
	if err := a.Kill("web"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	// It kills the tmux session …
	var killed bool
	for _, c := range r.cmds {
		if c == "tmux kill-session -t 'web'" {
			killed = true
		}
	}
	if !killed {
		t.Fatalf("Kill must issue tmux kill-session; cmds=%v", r.cmds)
	}
	// … and drops the entry (the rewritten names.json no longer contains it).
	if len(r.inputs) == 0 {
		t.Fatalf("Kill must rewrite names.json to forget the entry")
	}
	if strings.Contains(r.inputs[len(r.inputs)-1], "Web") {
		t.Fatalf("Kill should have removed the entry, still present: %s", r.inputs[len(r.inputs)-1])
	}
}

// loadCmd is the exact names.json read string the Store issues; counting it in
// r.cmds proves CleanDetached loads names.json exactly once for the whole batch.
const loadCmd = "cat ~/.duck/names.json 2>/dev/null || echo '{}'"

// TestCleanDetachedBatchesAndSkipsAttached pins the §M3 batched-clean contract:
// given one ATTACHED and one detached session, CleanDetached must (1) NEVER kill
// the attached one (the safety contract), (2) kill exactly the detached one via a
// single `tmux kill-session`, and (3) read names.json ONCE and write it ONCE for
// the whole batch — not per session. This is what turns K×(load+save) round-trips
// into one of each.
func TestCleanDetachedBatchesAndSkipsAttached(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		// One attached ("live"), one detached ("gone"); both have names entries so
		// the killed one's entry removal is exercised.
		listCmd: "live\t~/dev/live\t1\t100\t1\ngone\t~/dev/gone\t0\t90\t1\n",
		loadCmd: `{"names":{"live":{"userName":"Live"},"gone":{"userName":"Gone"}}}`,
	}}
	a := newApp(r, namer.DirDerived{})

	killed, err := a.CleanDetached()
	if err != nil {
		t.Fatalf("CleanDetached: %v", err)
	}
	if killed != 1 {
		t.Fatalf("CleanDetached should report 1 killed (the detached session), got %d", killed)
	}

	// The ATTACHED session must NEVER be killed; the detached one must be, exactly
	// once. We count kill-session calls by target.
	var killCmds []string
	loads, kills := 0, 0
	for _, c := range r.cmds {
		if c == loadCmd {
			loads++
		}
		if strings.HasPrefix(c, "tmux kill-session") {
			kills++
			killCmds = append(killCmds, c)
		}
		if c == "tmux kill-session -t 'live'" {
			t.Fatalf("CleanDetached killed the ATTACHED session 'live' (safety violation); cmds=%v", r.cmds)
		}
	}
	if kills != 1 || killCmds[0] != "tmux kill-session -t 'gone'" {
		t.Fatalf("CleanDetached must issue exactly one kill for the detached session; killCmds=%v", killCmds)
	}

	// ONE load + ONE save for the whole batch (vs K of each via per-session Kill).
	if loads != 1 {
		t.Fatalf("CleanDetached must load names.json exactly once, loaded %d times; cmds=%v", loads, r.cmds)
	}
	if len(r.inputs) != 1 {
		t.Fatalf("CleanDetached must save names.json exactly once, saved %d times; inputs=%v", len(r.inputs), r.inputs)
	}
	// The save drops only the killed session's entry; the attached one survives.
	written := r.inputs[0]
	if strings.Contains(written, "gone") {
		t.Fatalf("killed session's names.json entry must be removed; still present: %s", written)
	}
	if !strings.Contains(written, "live") {
		t.Fatalf("the surviving attached session's entry must be kept; missing: %s", written)
	}
}

// TestCleanDetachedPropagatesListError pins that a dead/unreachable hub surfaces
// as an error from CleanDetached — never a silent empty (which would make `duck
// clean` falsely print "no detached sessions to clean").
func TestCleanDetachedPropagatesListError(t *testing.T) {
	r := &fakeErrRunner{err: errors.New("ssh: connect to host hub port 22: Connection refused")}
	mgr := session.NewManager(r, &fakeAttacher{})
	store := names.NewStore(r)
	a := New(mgr, store, namer.DirDerived{})

	if _, err := a.CleanDetached(); err == nil {
		t.Fatalf("CleanDetached must propagate a transport/List error, got nil")
	}
}

// fakeErrRunner fails every Run with err, simulating an unreachable hub. The
// error string lacks tmux's "no server running" signature, so session.List
// propagates it (the empty-server case is matched separately).
type fakeErrRunner struct{ err error }

func (f *fakeErrRunner) Run(string) (string, error)                 { return "", f.err }
func (f *fakeErrRunner) RunInput(string, io.Reader) (string, error) { return "", f.err }

// always reports a dir is enabled, for the auto-naming-on path.
func always(string) bool { return true }

// TestRefreshAutoNamesUnnamedSessionOnFirstSight is the §M3 / §5 risk#1 path:
// with the per-dir toggle ON, the first Refresh that sees an unnamed session
// captures the head, runs codex, freezes the name+hash in names.json, and the
// row's Display is the codex title (not the dir-derived floor).
func TestRefreshAutoNamesUnnamedSessionOnFirstSight(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		listCmd: "auth\t~/dev/auth\t0\t100\t1\n",
		// names.json starts empty: the session has no cached name yet.
		"cat ~/.duck/names.json 2>/dev/null || echo '{}'": `{}`,
	}}
	sn := &stubNamer{title: "Login Flow", head: "claude: implement login"}
	a := newApp(r, sn)
	a.SetAutoName(always)

	rows, err := a.Refresh()
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if sn.nameCalls != 1 {
		t.Fatalf("codex Name should fire once on first sight, fired %d times", sn.nameCalls)
	}
	if len(rows) != 1 || rows[0].Display != "Login Flow" {
		t.Fatalf("row Display should be the codex title, got %+v", rows)
	}
	// It must freeze the codex name + the content hash of the captured head.
	if len(r.inputs) == 0 {
		t.Fatalf("Refresh must persist the freshly-minted name")
	}
	written := r.inputs[len(r.inputs)-1]
	if !strings.Contains(written, "Login Flow") {
		t.Fatalf("codex name not frozen in names.json: %s", written)
	}
	if !strings.Contains(written, namer.Hash("claude: implement login")) {
		t.Fatalf("content hash of the captured head must be stored: %s", written)
	}
}

// TestRefreshSkipsCodexWhenCacheHit pins the freeze: a session whose entry
// already holds a codex name minted from the current head (CacheHit) must NOT
// re-run codex on a later Refresh — the cached name is reused.
func TestRefreshSkipsCodexWhenCacheHit(t *testing.T) {
	head := "claude: implement login"
	r := &fakeRunner{out: map[string]string{
		listCmd: "auth\t~/dev/auth\t0\t100\t1\n",
		"cat ~/.duck/names.json 2>/dev/null || echo '{}'": `{"names":{"auth":{"codexName":"Login Flow","codexHash":"` + namer.Hash(head) + `"}}}`,
	}}
	sn := &stubNamer{title: "Should Not Run", head: head}
	a := newApp(r, sn)
	a.SetAutoName(always)

	rows, err := a.Refresh()
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if sn.nameCalls != 0 {
		t.Fatalf("codex Name must be skipped on a cache hit, fired %d times", sn.nameCalls)
	}
	if rows[0].Display != "Login Flow" {
		t.Fatalf("cached codex name should be reused, got %q", rows[0].Display)
	}
	// Nothing new minted ⇒ no names.json write.
	if len(r.inputs) != 0 {
		t.Fatalf("a cache hit must not rewrite names.json: %v", r.inputs)
	}
}

// TestRefreshDoesNotAutoNameWhenToggleOff is the privacy default: with no
// per-dir toggle installed (the production OFF default), Refresh never captures
// or sends pane content to the model, and Display falls to the dir-derived floor.
func TestRefreshDoesNotAutoNameWhenToggleOff(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		listCmd: "auth\t~/dev/auth\t0\t100\t1\n",
		"cat ~/.duck/names.json 2>/dev/null || echo '{}'": `{}`,
	}}
	sn := &stubNamer{title: "Should Not Run", head: "secret content"}
	a := newApp(r, sn) // SetAutoName never called ⇒ OFF for every dir

	rows, err := a.Refresh()
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if sn.nameCalls != 0 {
		t.Fatalf("codex Name must not fire with auto-naming off, fired %d times", sn.nameCalls)
	}
	if rows[0].Display != "auth" {
		t.Fatalf("Display should be the dir-derived floor, got %q", rows[0].Display)
	}
	// No capture-pane command should have been issued either.
	for _, c := range r.cmds {
		if strings.Contains(c, "capture-pane") {
			t.Fatalf("no pane content may be captured when auto-naming is off: %s", c)
		}
	}
}

// TestListDoesNotAutoNameEvenWhenToggleOn pins the `duck ls` read-only
// invariant: List must never auto-name even with the per-dir toggle ON. Unlike
// Refresh (interactive picker), List spends no codex quota, captures no pane
// content, and writes no names.json on first sight of an unnamed session.
func TestListDoesNotAutoNameEvenWhenToggleOn(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		listCmd: "auth\t~/dev/auth\t0\t100\t1\n",
		"cat ~/.duck/names.json 2>/dev/null || echo '{}'": `{}`,
	}}
	sn := &stubNamer{title: "Should Not Run", head: "secret content"}
	a := newApp(r, sn)
	a.SetAutoName(always) // toggle ON for every dir

	rows, err := a.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Display falls to the dir-derived floor — no codex name minted.
	if rows[0].Display != "auth" {
		t.Fatalf("List Display should be the dir-derived floor, got %q", rows[0].Display)
	}
	// No codex call …
	if sn.nameCalls != 0 {
		t.Fatalf("List must not call codex even with the toggle on, fired %d times", sn.nameCalls)
	}
	// … no pane capture …
	for _, c := range r.cmds {
		if strings.Contains(c, "capture-pane") {
			t.Fatalf("List must not capture pane content: %s", c)
		}
	}
	// … and no names.json write.
	if len(r.inputs) != 0 {
		t.Fatalf("List must not write names.json: %v", r.inputs)
	}
}

// TestRefreshDegradesToFloorOnCodexError guards the Refresh-side first-sight
// path: with the toggle ON and a codex error, Refresh must still succeed, the
// row Display falls back to the dir-derived floor, and names.json is NOT written
// (so a transient codex hiccup can't poison the cache or fail the refresh).
func TestRefreshDegradesToFloorOnCodexError(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		listCmd: "auth\t~/dev/auth\t0\t100\t1\n",
		"cat ~/.duck/names.json 2>/dev/null || echo '{}'": `{}`,
	}}
	sn := &stubNamer{head: "pane head content", err: errors.New("codex unavailable")}
	a := newApp(r, sn)
	a.SetAutoName(always) // toggle ON: Refresh will attempt to name

	rows, err := a.Refresh()
	if err != nil {
		t.Fatalf("Refresh must not fail on a codex error: %v", err)
	}
	// Display degrades to the dir-derived floor (~/dev/auth ⇒ "auth").
	if rows[0].Display != "auth" {
		t.Fatalf("Refresh should fall to the dir-derived floor on codex error, got %q", rows[0].Display)
	}
	// No names.json write — the codex slots stay untouched so a later refresh
	// retries naming instead of seeing a poisoned cache hit.
	if len(r.inputs) != 0 {
		t.Fatalf("Refresh must not write names.json on a codex error (cache poisoning): %v", r.inputs)
	}
}

func TestAttachDelegates(t *testing.T) {
	r := &fakeRunner{}
	att := &fakeAttacher{}
	mgr := session.NewManager(r, att)
	a := New(mgr, names.NewStore(r), namer.DirDerived{})
	if err := a.Attach("foo"); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if att.attached != "foo" {
		t.Fatalf("Attach delegated %q, want foo", att.attached)
	}
}

// TestRefreshHidesPanelCompanions pins that a `duck panel` companion session
// (@duck_panel_of set) never becomes a picker row — its agents surface in the
// owning session's sidebar, not in `duck --resume` / `duck ls`.
func TestRefreshHidesPanelCompanions(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		listCmd: "auth\t~/dev/auth\t1\t100\t2\t\t\t\n" +
			"auth-agents\t\t0\t100\t3\t\tauth\t\n",
		"cat ~/.duck/names.json 2>/dev/null || echo '{}'": `{}`,
	}}
	a := newApp(r, namer.DirDerived{})
	rows, err := a.Refresh()
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(rows) != 1 || rows[0].TmuxName != "auth" {
		t.Fatalf("companion should be hidden; rows = %+v", rows)
	}
}
