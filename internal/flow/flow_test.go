package flow

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/DigiBugCat/duck/internal/claude"
	"github.com/DigiBugCat/duck/internal/folder"
	"github.com/DigiBugCat/duck/internal/mutagen"
	"github.com/DigiBugCat/duck/internal/names"
	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/DigiBugCat/duck/internal/session"
)

// osUserHomeDir is a tiny indirection so the decision-tree tests build
// tilde-stable paths without importing os at every call site.
func osUserHomeDir() (string, error) { return os.UserHomeDir() }

// fakeSyncer drives the EnsureSynced branch without touching rsync/mutagen.
// lastForce records the force arg of the most recent AddAndWait so the force
// threading can be asserted; calls records the ORDER of Reconcile/AddAndWait so
// the safety-relevant "reconcile BEFORE add" ordering on the merge path is
// observable one level up (the recording-fake pattern the spec's flow test
// asks for).
type fakeSyncer struct {
	synced      bool
	addCalls    int
	reconCalls  int
	lastForce   bool
	lastDir     Direction
	calls       []string // "reconcile"/"addwait" in call order
	addPaths    []string // tildeDir of every AddAndWait, in order (co-sync target asserts)
	containment folder.Containment
	terminated  []string // session names passed to Terminate, in order
}

func (f *fakeSyncer) IsSynced(string) (bool, error) { return f.synced, nil }
func (f *fakeSyncer) Reconcile(_ string, dir Direction) error {
	f.reconCalls++
	f.lastDir = dir
	f.calls = append(f.calls, "reconcile")
	return nil
}
func (f *fakeSyncer) AddAndWait(tildeDir string, force bool) error {
	f.addCalls++
	f.lastForce = force
	f.calls = append(f.calls, "addwait")
	f.addPaths = append(f.addPaths, tildeDir)
	return nil
}
func (f *fakeSyncer) CheckContainment(string) (folder.Containment, error) {
	return f.containment, nil
}
func (f *fakeSyncer) Terminate(sessionName string) error {
	f.terminated = append(f.terminated, sessionName)
	return nil
}

// fakePolicy is an in-memory policy store for the decision-tree tests. seed is
// the remembered policy ("" = unknown); sets records every Set so a test can
// assert what was remembered.
type fakePolicy struct {
	seed map[string]string
	sets map[string]string
}

func newFakePolicy(seed map[string]string) *fakePolicy {
	return &fakePolicy{seed: seed, sets: map[string]string{}}
}

func (f *fakePolicy) Get(dir string) (string, bool) {
	if f.seed == nil {
		return "", false
	}
	p, ok := f.seed[dir]
	return p, ok && p != ""
}

func (f *fakePolicy) Set(dir, policy string) error {
	f.sets[dir] = policy
	if f.seed == nil {
		f.seed = map[string]string{}
	}
	f.seed[dir] = policy
	return nil
}

// fakeClassifier returns a canned riskiness verdict.
type fakeClassifier struct {
	risky  bool
	reason string
}

func (f fakeClassifier) IsRisky(string) (bool, string) { return f.risky, f.reason }

// fakePrompter returns a canned choice and counts how often it was asked, so a
// test can assert the prompt was (or was NOT) shown.
type fakePrompter struct {
	choice           Choice
	calls            int
	consolidate      bool
	consolidateCalls int
}

func (f *fakePrompter) AskSync(string, string) (Choice, error) {
	f.calls++
	return f.choice, nil
}

func (f *fakePrompter) AskConsolidate(string, string) (bool, error) {
	f.consolidateCalls++
	return f.consolidate, nil
}

// fakeRunner backs the session.Manager + names.Store with canned tmux/cat
// output and records every command string. Mutex-guarded because EnsureSession
// now runs its best-effort bookkeeping (names/ledger writes) on a background
// goroutine, so the fake can see calls from two goroutines.
type fakeRunner struct {
	mu        sync.Mutex
	cmds      []string
	out       map[string]string
	lastInput string // the most recent RunInput body (e.g. the JSON a names Save streamed)
}

func (f *fakeRunner) Run(cmd string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, cmd)
	if f.out != nil {
		if v, ok := f.out[cmd]; ok {
			return v, nil
		}
	}
	return "", nil
}
func (f *fakeRunner) RunInput(cmd string, stdin io.Reader) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, cmd)
	if stdin != nil {
		if b, err := io.ReadAll(stdin); err == nil {
			f.lastInput = string(b)
		}
	}
	return "", nil
}

type fakeAttacher struct {
	attached string // recorded by ExecAttach (reused-session path)
	waited   string // recorded by RunAttach (fresh-session path)
}

func (f *fakeAttacher) ExecAttach(id string) error { f.attached = id; return nil }

// RunAttach records the fresh-path subprocess attach. It sets attached too so
// the existing "every branch ends by attaching" assertions hold regardless of
// which attach variant the path used.
func (f *fakeAttacher) RunAttach(id string) error {
	f.waited = id
	f.attached = id
	return nil
}

func listCmd() string {
	// Mirror the manager's list format without importing its private const
	// (@duck_loop then pane_title are the trailing fields — pane_title last because
	// it is free text; @duck_loop is the looped-session marker the picker pins on).
	return "tmux list-sessions -F '#{session_name}\t#{@duck_dir}\t#{session_attached}\t#{session_activity}\t#{session_windows}\t#{@duck_loop}\t#{@duck_panel_of}\t#{pane_title}'"
}

// newFlow builds a Flow with default sync-awareness collaborators: an unknown
// policy store, a not-risky classifier, and an always-No prompter. The existing
// EnsureSynced/EnsureSession/Run tests use it; the decision-tree tests use
// newFlowDeps to inject specific policy/classifier/prompter fakes.
func newFlow(r *fakeRunner, a *fakeAttacher, s *fakeSyncer) *Flow {
	mgr := session.NewManager(r, a)
	store := names.NewStore(r)
	return NewWithDeps("hub", mgr, store, s, newFakePolicy(nil), fakeClassifier{}, &fakePrompter{choice: ChoiceNo})
}

// newFlowDeps builds a Flow with every sync-awareness collaborator injected.
func newFlowDeps(r *fakeRunner, a *fakeAttacher, s *fakeSyncer, p *fakePolicy, c Classifier, pr *fakePrompter) *Flow {
	mgr := session.NewManager(r, a)
	store := names.NewStore(r)
	return NewWithDeps("hub", mgr, store, s, p, c, pr)
}

func TestEnsureSyncedShortCircuitsWhenAlreadySynced(t *testing.T) {
	s := &fakeSyncer{synced: true}
	f := newFlow(&fakeRunner{}, &fakeAttacher{}, s)
	dir, err := f.EnsureSynced("/home/me/dev/foo", DirNone)
	if err != nil {
		t.Fatalf("EnsureSynced: %v", err)
	}
	if s.addCalls != 0 {
		t.Fatalf("already-synced dir must NOT call AddAndWait, got %d", s.addCalls)
	}
	_ = dir // tilde-contraction depends on $HOME; only the short-circuit matters here.
}

func TestEnsureSyncedAddsWhenUnsynced(t *testing.T) {
	s := &fakeSyncer{synced: false}
	f := newFlow(&fakeRunner{}, &fakeAttacher{}, s)
	if _, err := f.EnsureSynced("/home/me/dev/foo", DirNone); err != nil {
		t.Fatalf("EnsureSynced: %v", err)
	}
	if s.addCalls != 1 {
		t.Fatalf("unsynced dir must AddAndWait exactly once, got %d", s.addCalls)
	}
}

// TestEnsureSyncedConsolidatesEnclosedOnConsent pins the new containment
// wiring: when CheckContainment reports ContainmentEncloses and the prompter
// consents, EnsureSynced terminates every enclosed session before proceeding
// to AddAndWait.
func TestEnsureSyncedConsolidatesEnclosedOnConsent(t *testing.T) {
	s := &fakeSyncer{
		synced: false,
		containment: folder.Containment{
			Kind: folder.ContainmentEncloses,
			Enclosed: []mutagen.Session{
				{Name: "duck-default-aaa", Alpha: mutagen.Endpoint{Path: "/home/me/dev/foo/a"}},
				{Name: "duck-default-bbb", Alpha: mutagen.Endpoint{Path: "/home/me/dev/foo/b"}},
			},
		},
	}
	pr := &fakePrompter{choice: ChoiceNo, consolidate: true}
	f := newFlowDeps(&fakeRunner{}, &fakeAttacher{}, s, newFakePolicy(nil), fakeClassifier{}, pr)

	if _, err := f.EnsureSynced("/home/me/dev/foo", DirNone); err != nil {
		t.Fatalf("EnsureSynced: %v", err)
	}
	if pr.consolidateCalls != 1 {
		t.Fatalf("AskConsolidate calls = %d, want 1", pr.consolidateCalls)
	}
	if len(s.terminated) != 2 || s.terminated[0] != "duck-default-aaa" || s.terminated[1] != "duck-default-bbb" {
		t.Fatalf("terminated = %v, want both enclosed sessions", s.terminated)
	}
	if s.addCalls != 1 {
		t.Fatalf("the new parent sync must still proceed, addCalls = %d", s.addCalls)
	}
}

// TestEnsureSyncedKeepsEnclosedWhenDeclined pins the opposite consent branch:
// declining the consolidation prompt leaves every enclosed session running
// and still proceeds with the new parent sync.
func TestEnsureSyncedKeepsEnclosedWhenDeclined(t *testing.T) {
	s := &fakeSyncer{
		synced: false,
		containment: folder.Containment{
			Kind:     folder.ContainmentEncloses,
			Enclosed: []mutagen.Session{{Name: "duck-default-aaa", Alpha: mutagen.Endpoint{Path: "/home/me/dev/foo/a"}}},
		},
	}
	pr := &fakePrompter{choice: ChoiceNo, consolidate: false}
	f := newFlowDeps(&fakeRunner{}, &fakeAttacher{}, s, newFakePolicy(nil), fakeClassifier{}, pr)

	if _, err := f.EnsureSynced("/home/me/dev/foo", DirNone); err != nil {
		t.Fatalf("EnsureSynced: %v", err)
	}
	if pr.consolidateCalls != 1 {
		t.Fatalf("AskConsolidate calls = %d, want 1", pr.consolidateCalls)
	}
	if len(s.terminated) != 0 {
		t.Fatalf("declining must NOT terminate anything, got %v", s.terminated)
	}
	if s.addCalls != 1 {
		t.Fatalf("the new parent sync must still proceed, addCalls = %d", s.addCalls)
	}
}

// TestEnsureSyncedSkipsPromptWhenNoContainment pins the common case: when
// CheckContainment reports ContainmentNone, AskConsolidate is never called.
func TestEnsureSyncedSkipsPromptWhenNoContainment(t *testing.T) {
	s := &fakeSyncer{synced: false, containment: folder.Containment{Kind: folder.ContainmentNone}}
	pr := &fakePrompter{choice: ChoiceNo}
	f := newFlowDeps(&fakeRunner{}, &fakeAttacher{}, s, newFakePolicy(nil), fakeClassifier{}, pr)

	if _, err := f.EnsureSynced("/home/me/dev/foo", DirNone); err != nil {
		t.Fatalf("EnsureSynced: %v", err)
	}
	if pr.consolidateCalls != 0 {
		t.Fatalf("AskConsolidate calls = %d, want 0 when there is no containment", pr.consolidateCalls)
	}
}

// TestOverrideSyncMergeReconcilesThenForceAdds pins the newest-wins merge
// threading: OverrideSync forces the merge (force=true into AddAndWait) AND runs
// Reconcile BEFORE AddAndWait (the load-bearing order — the rsync seed makes
// both sides identical so the force-add's mutagen session has no conflicts);
// OverrideNone (the safe-unknown auto-sync) passes force=false and does NOT
// reconcile. Asserted via the fake syncer's recorded force arg and call order.
// synced:false so AddAndWait actually fires.
func TestOverrideSyncMergeReconcilesThenForceAdds(t *testing.T) {
	home, _ := osUserHomeDir()
	local := home + "/dev/foo"
	const d = "~/dev/foo"

	cases := []struct {
		name      string
		override  Override
		wantForce bool
		wantCalls []string // expected Reconcile/AddAndWait order
	}{
		{name: "OverrideSync → reconcile then force-add", override: OverrideSync, wantForce: true, wantCalls: []string{"reconcile", "addwait"}},
		{name: "OverrideNone safe-unknown → add only, no reconcile", override: OverrideNone, wantForce: false, wantCalls: []string{"addwait"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{out: map[string]string{
				listCmd():       "",
				dirExistsCmd(d): "yes\n",
			}}
			s := &fakeSyncer{synced: false}
			// Unknown policy + not-risky classifier → OverrideNone still auto-syncs.
			f := newFlowDeps(r, &fakeAttacher{}, s, newFakePolicy(nil), fakeClassifier{risky: false}, &fakePrompter{choice: ChoiceNo})
			if err := f.RunWithOverride(local, tc.override); err != nil {
				t.Fatalf("RunWithOverride: %v", err)
			}
			if s.addCalls != 1 {
				t.Fatalf("expected AddAndWait to fire once, got addCalls=%d", s.addCalls)
			}
			if s.lastForce != tc.wantForce {
				t.Fatalf("AddAndWait force = %v, want %v", s.lastForce, tc.wantForce)
			}
			if strings.Join(s.calls, ",") != strings.Join(tc.wantCalls, ",") {
				t.Fatalf("call order = %v, want %v (reconcile MUST precede addwait on the merge path)", s.calls, tc.wantCalls)
			}
		})
	}
}

func TestEnsureSessionForceNewMintsAndStampsDuckDir(t *testing.T) {
	r := &fakeRunner{out: map[string]string{listCmd(): ""}} // empty hub
	f := newFlow(r, &fakeAttacher{}, &fakeSyncer{synced: true})

	id, created, err := f.EnsureSession("~/dev/foo", true)
	if err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	f.WaitBackground() // join the async names/ledger write before reading r.cmds
	if !created {
		t.Fatalf("forceNew on an empty hub must report created=true")
	}
	if id != "foo" {
		t.Fatalf("first session id should be the slug 'foo', got %q", id)
	}
	// It must have issued a tmux new-session with -c and stamped @duck_dir —
	// batched into ONE `&&`-chained command (latency: one ssh roundtrip).
	var sawNew, sawOpt bool
	for _, c := range r.cmds {
		if strings.Contains(c, `tmux new-session -d -s 'foo' -c "$HOME"/'dev/foo'`) {
			sawNew = true
		}
		if strings.Contains(c, `tmux set-option -t 'foo' '@duck_dir' '~/dev/foo'`) {
			sawOpt = true
		}
	}
	if !sawNew {
		t.Fatalf("EnsureSession must create the session with -c hubpath; cmds=%v", r.cmds)
	}
	if !sawOpt {
		t.Fatalf("EnsureSession must stamp @duck_dir; cmds=%v", r.cmds)
	}
}

func TestEnsureSessionForceNewIsNPerDirWithSuffix(t *testing.T) {
	// A live session already named "foo" in the dir → forceNew mints foo-2.
	r := &fakeRunner{out: map[string]string{
		listCmd(): "foo\t~/dev/foo\t0\t100\t1\n",
	}}
	f := newFlow(r, &fakeAttacher{}, &fakeSyncer{synced: true})
	id, created, err := f.EnsureSession("~/dev/foo", true)
	if err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	f.WaitBackground()
	if !created {
		t.Fatalf("forceNew minting foo-2 must report created=true")
	}
	if id != "foo-2" {
		t.Fatalf("a 2nd session in the same dir should be 'foo-2' (N-per-dir), got %q", id)
	}
}

func TestEnsureSessionReuseShortCircuits(t *testing.T) {
	// forceNew=false with an existing session in the dir → reuse, no new-session.
	r := &fakeRunner{out: map[string]string{
		listCmd(): "foo\t~/dev/foo\t1\t100\t1\n",
	}}
	f := newFlow(r, &fakeAttacher{}, &fakeSyncer{synced: true})
	id, created, err := f.EnsureSession("~/dev/foo", false)
	if err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	if created {
		t.Fatalf("reuse must report created=false")
	}
	if id != "foo" {
		t.Fatalf("reuse should return the existing 'foo', got %q", id)
	}
	f.WaitBackground()
	for _, c := range r.cmds {
		if strings.Contains(c, "tmux new-session") {
			t.Fatalf("reuse must NOT create a new session; cmds=%v", r.cmds)
		}
	}
}

func TestRunComposesSyncSessionAttach(t *testing.T) {
	r := &fakeRunner{out: map[string]string{listCmd(): ""}}
	a := &fakeAttacher{}
	f := newFlow(r, a, &fakeSyncer{synced: true})
	if err := f.Run("/home/me/dev/foo"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if a.attached == "" {
		t.Fatalf("Run must end by attaching a session")
	}
}

// dirExistsCmd is the exact remote command DirExists issues for a tilde-form
// dir, so the no-sync fallback tests can seed its yes/no output.
func dirExistsCmd(tildeDir string) string {
	hp := `"$HOME"`
	if tildeDir != "~" {
		// hubPath quotes "~/x" as "$HOME"/'x'.
		rest := tildeDir[len("~/"):]
		hp = `"$HOME"/'` + rest + `'`
	}
	return "test -d " + hp + " && echo yes || echo no"
}

// TestDecideSync exercises every branch of the bare-`duck` sync decision tree
// via the RunWithOverride composition. localDir is always a real tilde-form
// path under $HOME so paths.Contract is stable.
func TestDecideSync(t *testing.T) {
	// Use a path guaranteed under $HOME so Contract yields "~/dev/foo".
	home, _ := osUserHomeDir()
	local := home + "/dev/foo"
	const d = "~/dev/foo"

	cases := []struct {
		name       string
		override   Override
		seed       map[string]string // remembered policy
		synced     bool              // syncer.IsSynced
		risky      bool
		choice     Choice
		wantSync   bool   // doSync → AddAndWait called when not already synced
		wantPrompt bool   // prompter consulted
		wantSet    string // policy remembered for d ("" = none)
	}{
		{name: "already synced", synced: true, wantSync: true},
		{name: "remembered sync no prompt", seed: map[string]string{d: "sync"}, wantSync: true},
		{name: "remembered never", seed: map[string]string{d: "never"}, wantSync: false},
		// IsSynced (flow.go:335) is checked BEFORE the known&&never branch
		// (flow.go:342), so a folder remembered "never" that already has a running
		// mirror still returns true: "never" governs starting a mirror, not tearing
		// down a running one. No policy write on this path (wantSet "").
		{name: "never but already synced", seed: map[string]string{d: "never"}, synced: true, wantSync: true},
		{name: "unknown safe auto-sync remembered", risky: false, wantSync: true, wantSet: "sync"},
		{name: "unknown risky prompt yes", risky: true, choice: ChoiceYes, wantSync: true, wantPrompt: true, wantSet: "sync"},
		{name: "unknown risky prompt no", risky: true, choice: ChoiceNo, wantSync: false, wantPrompt: true, wantSet: ""},
		{name: "unknown risky prompt never", risky: true, choice: ChoiceNever, wantSync: false, wantPrompt: true, wantSet: "never"},
		{name: "override sync", override: OverrideSync, risky: true, wantSync: true, wantSet: "sync"},
		{name: "override no-sync", override: OverrideNoSync, synced: true, wantSync: false, wantSet: "never"},
		// One-time no-sync (the hub-conflict [n] choice): no-sync AND no policy
		// persisted, so duck asks again next time. wantSet "" asserts the store is
		// untouched for d.
		{name: "override no-sync-once", override: OverrideNoSyncOnce, synced: true, wantSync: false, wantSet: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Seed list (empty hub) and, for the no-sync branches, the dir-exists
			// probe so the fallback resolves D.
			r := &fakeRunner{out: map[string]string{
				listCmd():       "",
				dirExistsCmd(d): "yes\n",
			}}
			a := &fakeAttacher{}
			s := &fakeSyncer{synced: tc.synced}
			p := newFakePolicy(tc.seed)
			pr := &fakePrompter{choice: tc.choice}
			f := newFlowDeps(r, a, s, p, fakeClassifier{risky: tc.risky}, pr)

			if err := f.RunWithOverride(local, tc.override); err != nil {
				t.Fatalf("RunWithOverride: %v", err)
			}
			// doSync ⇒ AddAndWait called (since synced=false unless tc.synced).
			gotSync := s.addCalls > 0 || tc.synced
			if tc.wantSync && !gotSync {
				t.Fatalf("expected doSync; addCalls=%d synced=%v", s.addCalls, tc.synced)
			}
			if !tc.wantSync && s.addCalls > 0 {
				t.Fatalf("expected no-sync but AddAndWait ran (%d times)", s.addCalls)
			}
			if tc.wantPrompt && pr.calls == 0 {
				t.Fatalf("expected the prompter to be consulted")
			}
			if !tc.wantPrompt && pr.calls > 0 {
				t.Fatalf("prompter consulted %d times but should not have been", pr.calls)
			}
			if got := p.sets[d]; got != tc.wantSet {
				t.Fatalf("policy remembered for %s = %q, want %q", d, got, tc.wantSet)
			}
			if a.attached == "" {
				t.Fatalf("every branch must end by attaching a session")
			}
		})
	}
}

// TestLocalSkipsSync pins that a flow marked local (running ON the hub) NEVER
// syncs and NEVER prompts, regardless of riskiness or an explicit sync override:
// mirroring a folder to the machine it already lives on is a no-op. It still
// opens (attaches) a session in cwd.
func TestLocalSkipsSync(t *testing.T) {
	home, _ := osUserHomeDir()
	local := home + "/dev/foo"
	const d = "~/dev/foo"
	for _, override := range []Override{OverrideNone, OverrideSync} {
		r := &fakeRunner{out: map[string]string{
			listCmd():       "",
			dirExistsCmd(d): "yes\n",
		}}
		a := &fakeAttacher{}
		s := &fakeSyncer{}
		p := newFakePolicy(nil)
		pr := &fakePrompter{choice: ChoiceYes}
		f := newFlowDeps(r, a, s, p, fakeClassifier{risky: true}, pr)
		f.SetLocal(true)
		if err := f.RunWithOverride(local, override); err != nil {
			t.Fatalf("override %v: RunWithOverride: %v", override, err)
		}
		if s.addCalls > 0 {
			t.Fatalf("override %v: local flow must not sync (AddAndWait ran %d times)", override, s.addCalls)
		}
		if pr.calls > 0 {
			t.Fatalf("override %v: local flow must not prompt (prompter consulted %d times)", override, pr.calls)
		}
		if a.attached == "" {
			t.Fatalf("override %v: local flow must still attach a session", override)
		}
	}
}

// TestNoSyncCwdFallback pins the no-sync session-dir choice: D when it exists on
// the hub, "~" when it does not. Asserted via the fake runner's new-session -c.
func TestNoSyncCwdFallback(t *testing.T) {
	home, _ := osUserHomeDir()
	local := home + "/dev/foo"
	const d = "~/dev/foo"

	t.Run("dir exists → session in D", func(t *testing.T) {
		r := &fakeRunner{out: map[string]string{
			listCmd():       "",
			dirExistsCmd(d): "yes\n",
		}}
		f := newFlowDeps(r, &fakeAttacher{}, &fakeSyncer{}, newFakePolicy(map[string]string{d: "never"}), fakeClassifier{}, &fakePrompter{})
		if err := f.RunWithOverride(local, OverrideNone); err != nil {
			t.Fatalf("RunWithOverride: %v", err)
		}
		if !sawNewSessionIn(r, `"$HOME"/'dev/foo'`) {
			t.Fatalf("no-sync session should open in D's hub path; cmds=%v", r.cmds)
		}
	})

	t.Run("dir missing → session in home", func(t *testing.T) {
		r := &fakeRunner{out: map[string]string{
			listCmd():       "",
			dirExistsCmd(d): "no\n",
		}}
		f := newFlowDeps(r, &fakeAttacher{}, &fakeSyncer{}, newFakePolicy(map[string]string{d: "never"}), fakeClassifier{}, &fakePrompter{})
		if err := f.RunWithOverride(local, OverrideNone); err != nil {
			t.Fatalf("RunWithOverride: %v", err)
		}
		if !sawNewSessionIn(r, `"$HOME"`) {
			t.Fatalf("a missing hub dir must fall back to home; cmds=%v", r.cmds)
		}
	})
}

// TestEnsureSyncedGatedNoMirrorForRiskyUnknown is the regression guard for the
// mirror-safety hole behind `duck -c` / `duck --resume`: an unknown risky/home
// folder with no remembered "sync" and a No prompt must NOT call AddAndWait, yet
// must still return the tilde-form dir so Recent/picker see the same dir. The
// safe-unknown case proves the gate still syncs when allowed.
func TestEnsureSyncedGatedNoMirrorForRiskyUnknown(t *testing.T) {
	home, _ := osUserHomeDir()
	local := home + "/dev/foo"
	const d = "~/dev/foo"

	t.Run("risky unknown, no consent → no mirror", func(t *testing.T) {
		s := &fakeSyncer{synced: false}
		f := newFlowDeps(&fakeRunner{}, &fakeAttacher{}, s,
			newFakePolicy(nil), fakeClassifier{risky: true, reason: "home directory"}, &fakePrompter{choice: ChoiceNo})
		got, err := f.EnsureSyncedGated(local)
		if err != nil {
			t.Fatalf("EnsureSyncedGated: %v", err)
		}
		if s.addCalls != 0 {
			t.Fatalf("risky unknown folder must NOT start a mirror, got addCalls=%d", s.addCalls)
		}
		if got != d {
			t.Fatalf("gated dir = %q, want %q (same dir as EnsureSynced)", got, d)
		}
	})

	t.Run("safe unknown → syncs", func(t *testing.T) {
		s := &fakeSyncer{synced: false}
		f := newFlowDeps(&fakeRunner{}, &fakeAttacher{}, s,
			newFakePolicy(nil), fakeClassifier{risky: false}, &fakePrompter{choice: ChoiceNo})
		got, err := f.EnsureSyncedGated(local)
		if err != nil {
			t.Fatalf("EnsureSyncedGated: %v", err)
		}
		if s.addCalls != 1 {
			t.Fatalf("safe unknown folder must sync exactly once, got addCalls=%d", s.addCalls)
		}
		if got != d {
			t.Fatalf("gated dir = %q, want %q", got, d)
		}
	})

	t.Run("already synced → no new mirror, returns dir", func(t *testing.T) {
		s := &fakeSyncer{synced: true}
		f := newFlowDeps(&fakeRunner{}, &fakeAttacher{}, s,
			newFakePolicy(nil), fakeClassifier{risky: true}, &fakePrompter{choice: ChoiceNo})
		got, err := f.EnsureSyncedGated(local)
		if err != nil {
			t.Fatalf("EnsureSyncedGated: %v", err)
		}
		if s.addCalls != 0 {
			t.Fatalf("already-synced dir must not call AddAndWait, got addCalls=%d", s.addCalls)
		}
		if got != d {
			t.Fatalf("gated dir = %q, want %q", got, d)
		}
	})
}

// sawNewSessionIn reports whether any new-session command used the given -c
// hub-path expression.
func sawNewSessionIn(r *fakeRunner, hubCExpr string) bool {
	for _, c := range r.cmds {
		if strings.Contains(c, "tmux new-session") && strings.Contains(c, "-c "+hubCExpr) {
			return true
		}
	}
	return false
}

// untouchedQueryFor is the exact display-message query IsUntouched issues for
// id, so the cleanup tests can seed its canned output (mirrors the session
// package's own format string without importing the private const).
func untouchedQueryFor(id string) string {
	return "tmux display-message -p -t '" + id + "' '#{session_windows}|#{window_panes}|#{pane_current_command}|#{history_size}|#{pane_id}|#{@duck_manager}|#{@duck_state}'"
}

// namesLoadCmd is the exact remote command names.Store.Load issues, so a test
// can seed the on-hub names.json document forgetName then mutates.
func namesLoadCmd() string {
	return "cat ~/.duck/names.json 2>/dev/null || echo '{}'"
}

// killCmdFor is the exact kill-session command for id.
func killCmdFor(id string) string {
	return "tmux kill-session -t '" + id + "'"
}

func sawCmd(r *fakeRunner, want string) bool {
	for _, c := range r.cmds {
		if c == want {
			return true
		}
	}
	return false
}

// TestRunCleansUpFreshUntouchedSession is the feature-2 happy path: bare `duck`
// FRESHLY creates a session, the user detaches WITHOUT running anything, and
// duck kills it + forgets its names entry on bail — while KEEPING the sync and
// the folder policy. Asserted via the kill-session command, a names Save, and
// the untouched sync/policy state.
func TestRunCleansUpFreshUntouchedSession(t *testing.T) {
	// Seed Load so forgetName actually SEES the "foo" entry to remove (otherwise
	// Load reads an empty doc and the delete branch never runs — a vacuous test).
	r := &fakeRunner{out: map[string]string{
		listCmd():                "", // empty hub → mints "foo"
		untouchedQueryFor("foo"): "1|1|zsh|0|%0||\n",
		namesLoadCmd():           `{"names":{"foo":{"dir":"~/dev/foo"}}}`,
	}}
	a := &fakeAttacher{}
	s := &fakeSyncer{synced: true}
	p := newFakePolicy(map[string]string{"~/dev/foo": "sync"})
	f := newFlowDeps(r, a, s, p, fakeClassifier{}, &fakePrompter{})
	home, _ := osUserHomeDir()

	if err := f.RunWithOverride(home+"/dev/foo", OverrideNone); err != nil {
		t.Fatalf("RunWithOverride: %v", err)
	}
	// Attached via the SUBPROCESS variant (so control returned for cleanup).
	if a.waited != "foo" {
		t.Fatalf("fresh path must AttachAndWait (RunAttach), got waited=%q", a.waited)
	}
	// Killed the untouched session.
	if !sawCmd(r, killCmdFor("foo")) {
		t.Fatalf("a fresh untouched session must be killed; cmds=%v", r.cmds)
	}
	// Forgot the names entry: the LAST names Save (the forget) must no longer carry
	// "foo". Unmarshal rather than substring (the dir value "~/dev/foo" contains the
	// literal "foo"), and assert the key is gone — this fails if forgetName's delete
	// did not run.
	var n names.Names
	if err := json.Unmarshal([]byte(r.lastInput), &n); err != nil {
		t.Fatalf("cleanup must write a valid names doc; lastInput=%q err=%v", r.lastInput, err)
	}
	if _, ok := n.Names["foo"]; ok {
		t.Fatalf("cleanup must REMOVE the names entry; got %s", r.lastInput)
	}
	// Sync + policy are KEPT: cleanup triggers no re-sync and changes no policy.
	if s.addCalls != 0 {
		t.Fatalf("cleanup must KEEP the sync (no AddAndWait), got addCalls=%d", s.addCalls)
	}
	if got := p.seed["~/dev/foo"]; got != "sync" {
		t.Fatalf("cleanup must KEEP the folder policy, got %q", got)
	}
}

// TestRunDoesNotCleanFreshTouchedSession: a freshly created session the user
// WORKED IN (e.g. launched a program → non-shell current command) is NOT killed.
func TestRunDoesNotCleanFreshTouchedSession(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		listCmd():                "",
		untouchedQueryFor("foo"): "1|1|vim|3|%0||\n", // a program ran, scrollback grew
	}}
	a := &fakeAttacher{}
	f := newFlowDeps(r, a, &fakeSyncer{synced: true}, newFakePolicy(map[string]string{"~/dev/foo": "sync"}), fakeClassifier{}, &fakePrompter{})
	home, _ := osUserHomeDir()

	if err := f.RunWithOverride(home+"/dev/foo", OverrideNone); err != nil {
		t.Fatalf("RunWithOverride: %v", err)
	}
	if sawCmd(r, killCmdFor("foo")) {
		t.Fatalf("a fresh TOUCHED session must NOT be killed; cmds=%v", r.cmds)
	}
}

// TestRunNeverCleansReattachedSession: the reused/reattach path (forceNew minted
// nothing — an existing session is reused) hands off via the EXEC attach with no
// kill-check. RunWithOverride always forceNew=true, so to exercise the
// reused-session guarantee we drive EnsureSession+Attach directly the way the
// continue/resume paths do: created=false ⇒ ExecAttach, never RunAttach, never a
// kill.
func TestRunNeverCleansReattachedSession(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		listCmd(): "foo\t~/dev/foo\t0\t100\t1\n", // existing session in the dir
	}}
	a := &fakeAttacher{}
	f := newFlow(r, a, &fakeSyncer{synced: true})

	id, created, err := f.EnsureSession("~/dev/foo", false)
	if err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	if created {
		t.Fatalf("reattach must report created=false")
	}
	if err := f.Attach(id); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if a.attached != "foo" || a.waited != "" {
		t.Fatalf("reattach must ExecAttach (not RunAttach); attached=%q waited=%q", a.attached, a.waited)
	}
	if sawCmd(r, killCmdFor("foo")) {
		t.Fatalf("a reattached session must NEVER be killed; cmds=%v", r.cmds)
	}
}

// TestRunKeepsFreshSessionOnGiveUp is the cleanup-coordination guard for the
// reconnect feature: a freshly-created, untouched session whose interactive
// attach ended in a GIVE-UP (cleanLeave=false, e.g. ^c during a reconnect
// backoff) must NOT be killed — it is kept so `duck -c` can resume it. Driven by
// injecting an InteractiveAttach that reports cleanLeave=false.
func TestRunKeepsFreshSessionOnGiveUp(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		listCmd():                "", // empty hub → mints "foo"
		untouchedQueryFor("foo"): "1|1|zsh|0|%0||\n",
	}}
	a := &fakeAttacher{}
	f := newFlowDeps(r, a, &fakeSyncer{synced: true}, newFakePolicy(map[string]string{"~/dev/foo": "sync"}), fakeClassifier{}, &fakePrompter{})
	// Replace the interactive-attach seam with a give-up (cleanLeave=false).
	f.SetInteractiveAttach(func(string, bool) (bool, error) { return false, nil })
	home, _ := osUserHomeDir()

	if err := f.RunWithOverride(home+"/dev/foo", OverrideNone); err != nil {
		t.Fatalf("RunWithOverride: %v", err)
	}
	if sawCmd(r, killCmdFor("foo")) {
		t.Fatalf("a fresh session abandoned via give-up must be KEPT, not killed; cmds=%v", r.cmds)
	}
}

// TestRunCleansFreshSessionOnCleanLeave: the SAME fresh untouched session, but the
// injected seam reports cleanLeave=true → killed (cleanup proceeds). Pins that the
// only difference is the cleanLeave bit.
func TestRunCleansFreshSessionOnCleanLeave(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		listCmd():                "",
		untouchedQueryFor("foo"): "1|1|zsh|0|%0||\n",
		namesLoadCmd():           `{"names":{"foo":{"dir":"~/dev/foo"}}}`,
	}}
	f := newFlowDeps(r, &fakeAttacher{}, &fakeSyncer{synced: true}, newFakePolicy(map[string]string{"~/dev/foo": "sync"}), fakeClassifier{}, &fakePrompter{})
	f.SetInteractiveAttach(func(string, bool) (bool, error) { return true, nil })
	home, _ := osUserHomeDir()

	if err := f.RunWithOverride(home+"/dev/foo", OverrideNone); err != nil {
		t.Fatalf("RunWithOverride: %v", err)
	}
	if !sawCmd(r, killCmdFor("foo")) {
		t.Fatalf("a fresh untouched session left CLEANLY must be killed; cmds=%v", r.cmds)
	}
}

// --- per-folder Claude history co-sync (SetClaudeHistory) ---------------------

// claudeCoSyncEnv sets up a hermetic HOME with a real cwd under it and (when
// makeCorpus) the ~/.claude/projects corpus ROOT, so coSyncClaude can exercise
// its os.Stat gate against a real filesystem the framework cleans. It returns the
// absolute cwd. HOME is redirected for the whole test (paths Contract/Expand and
// claude.ProjectsRoot all key off os.UserHomeDir == $HOME).
func claudeCoSyncEnv(t *testing.T, makeCorpus bool) (home, cwd string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	cwd = home + "/work/proj"
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	if makeCorpus {
		corpus, err := paths.Expand(claude.ProjectsRoot())
		if err != nil {
			t.Fatalf("expand corpus: %v", err)
		}
		if err := os.MkdirAll(corpus+"/-work-proj", 0o755); err != nil {
			t.Fatalf("mkdir corpus: %v", err)
		}
		if err := os.WriteFile(corpus+"/-work-proj/session.jsonl", []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("seed corpus file: %v", err)
		}
	}
	return home, cwd
}

func TestClaudeCoSyncOffByDefault(t *testing.T) {
	_, cwd := claudeCoSyncEnv(t, true) // corpus EXISTS, so only the toggle gates it
	r := &fakeRunner{out: map[string]string{listCmd(): ""}}
	s := &fakeSyncer{synced: false}
	f := newFlow(r, &fakeAttacher{}, s)
	// SetClaudeHistory NOT called → default OFF.

	if err := f.Run(cwd); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Exactly ONE AddAndWait (the folder). No second call for the corpus.
	if s.addCalls != 1 {
		t.Fatalf("off-by-default must co-sync nothing: addCalls=%d, paths=%v", s.addCalls, s.addPaths)
	}
}

func TestClaudeCoSyncFiresWhenEnabled(t *testing.T) {
	_, cwd := claudeCoSyncEnv(t, true)
	r := &fakeRunner{out: map[string]string{listCmd(): ""}}
	s := &fakeSyncer{synced: false}
	f := newFlow(r, &fakeAttacher{}, s)
	f.SetClaudeHistory(true)

	if err := f.Run(cwd); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Two AddAndWait calls: the folder, then the corpus.
	if s.addCalls != 2 {
		t.Fatalf("enabled co-sync must AddAndWait twice (folder + corpus): addCalls=%d, paths=%v", s.addCalls, s.addPaths)
	}
	wantCorpus := claude.ProjectsRoot()
	if s.addPaths[len(s.addPaths)-1] != wantCorpus {
		t.Fatalf("co-sync target = %q, want the corpus root %q", s.addPaths[len(s.addPaths)-1], wantCorpus)
	}
	// The corpus is a multi-machine artifact → it must merge NEWEST-WINS
	// (force=true ⇒ Reconcile runs before its AddAndWait).
	if !s.lastForce {
		t.Fatalf("corpus co-sync must force the newest-wins merge (force=true)")
	}
}

// The auto-wired cross-machine reconcile must fire at the end of coSyncClaude
// whenever history co-sync is on and the corpus exists — INCLUDING when the
// corpus is already syncing (the seed is skipped but the reconcile still runs,
// so hub/other-laptop sessions that landed via the mirror get mapped in).
func TestClaudeCoSyncRunsReconcilerEvenWhenAlreadySynced(t *testing.T) {
	_, cwd := claudeCoSyncEnv(t, true)
	r := &fakeRunner{out: map[string]string{listCmd(): ""}}
	s := &fakeSyncer{synced: true} // corpus already syncing → seed skipped
	f := newFlow(r, &fakeAttacher{}, s)
	f.SetClaudeHistory(true)
	reconciled := 0
	f.SetClaudeReconciler(func() { reconciled++ })

	if err := f.Run(cwd); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if reconciled != 1 {
		t.Fatalf("reconciler must run once from coSyncClaude even when already synced, ran %d", reconciled)
	}
}

// With co-sync OFF, neither the corpus seed nor the reconcile fires.
func TestClaudeCoSyncReconcilerSkippedWhenDisabled(t *testing.T) {
	_, cwd := claudeCoSyncEnv(t, true)
	r := &fakeRunner{out: map[string]string{listCmd(): ""}}
	f := newFlow(r, &fakeAttacher{}, &fakeSyncer{synced: true})
	// SetClaudeHistory NOT called → OFF.
	reconciled := 0
	f.SetClaudeReconciler(func() { reconciled++ })
	if err := f.Run(cwd); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if reconciled != 0 {
		t.Fatalf("reconciler must NOT run when co-sync is off, ran %d", reconciled)
	}
}

func TestClaudeCoSyncSkippedWhenCorpusAbsent(t *testing.T) {
	_, cwd := claudeCoSyncEnv(t, false) // NO corpus dir: Claude never ran here
	r := &fakeRunner{out: map[string]string{listCmd(): ""}}
	s := &fakeSyncer{synced: false}
	f := newFlow(r, &fakeAttacher{}, s)
	f.SetClaudeHistory(true) // ON, but nothing to seed yet

	if err := f.Run(cwd); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if s.addCalls != 1 {
		t.Fatalf("absent corpus must co-sync nothing even when enabled: addCalls=%d, paths=%v", s.addCalls, s.addPaths)
	}
}

func TestClaudeCoSyncFiresFromGatedPath(t *testing.T) {
	// `duck -c` / `--resume` go through EnsureSyncedGated, not Run; the corpus
	// must seed there too so history follows the user regardless of entry point.
	_, cwd := claudeCoSyncEnv(t, true)
	r := &fakeRunner{out: map[string]string{listCmd(): ""}}
	s := &fakeSyncer{synced: false}
	// not-risky classifier + unknown policy → gated decideSync auto-syncs.
	f := newFlowDeps(r, &fakeAttacher{}, s, newFakePolicy(nil), fakeClassifier{}, &fakePrompter{choice: ChoiceNo})
	f.SetClaudeHistory(true)

	if _, err := f.EnsureSyncedGated(cwd); err != nil {
		t.Fatalf("EnsureSyncedGated: %v", err)
	}
	if s.addCalls != 2 {
		t.Fatalf("gated path must co-sync the corpus too: addCalls=%d, paths=%v", s.addCalls, s.addPaths)
	}
	if s.addPaths[len(s.addPaths)-1] != claude.ProjectsRoot() {
		t.Fatalf("gated co-sync target = %q, want corpus root %q", s.addPaths[len(s.addPaths)-1], claude.ProjectsRoot())
	}
}
