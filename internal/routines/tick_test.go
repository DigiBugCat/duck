package routines

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DigiBugCat/duck/internal/channel"
	"github.com/DigiBugCat/duck/internal/workspaces"
)

// fakeRunner scripts tmux responses by argv-prefix and records calls, so the
// tmux-free logic (project sweep, workspace reuse/create) is unit-testable.
type fakeRunner struct {
	calls []string
	// responder returns (output, matched) for a joined argv; unmatched calls
	// return "" with no error.
	responder func(args []string) (string, bool)
}

func (f *fakeRunner) run(args ...string) (string, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if f.responder != nil {
		if out, ok := f.responder(args); ok {
			return out, nil
		}
	}
	return "", nil
}

func (f *fakeRunner) called(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func TestWorkspaceDirsSkipsCompanions(t *testing.T) {
	f := &fakeRunner{responder: func(args []string) (string, bool) {
		if args[0] == "list-sessions" {
			// name-less format: dir \t panel_of. A companion has panel_of set;
			// a bare non-duck session has an empty dir.
			return "~/dev/app\t\n~/dev/app\twork\n\t\n~/dev/other\t\n", true
		}
		return "", false
	}}
	dirs, err := workspaceDirs(f.run)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"~/dev/app", "~/dev/other"}
	if strings.Join(dirs, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v", dirs, want)
	}
}

func TestSweepProjectsUnionAndDedup(t *testing.T) {
	home, _ := os.UserHomeDir()
	duckHome := t.TempDir()
	t.Setenv("DUCK_HOME", duckHome)

	// Register two projects, one of which also has a live workspace.
	if err := Enable(filepath.Join(home, "dev/app")); err != nil {
		t.Fatal(err)
	}
	if err := Enable(filepath.Join(home, "dev/reg-only")); err != nil {
		t.Fatal(err)
	}

	f := &fakeRunner{responder: func(args []string) (string, bool) {
		if args[0] == "list-sessions" {
			return "~/dev/app\t\n", true
		}
		return "", false
	}}
	got, err := SweepProjects(f.run)
	if err != nil {
		t.Fatal(err)
	}
	// Expect absolute, deduped: app (live+registered → once) and reg-only.
	want := map[string]bool{
		filepath.Join(home, "dev/app"):      true,
		filepath.Join(home, "dev/reg-only"): true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v want keys %v", got, want)
	}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("unexpected project %q in %v", g, got)
		}
	}
}

func TestEnsureWorkspaceReuse(t *testing.T) {
	home, _ := os.UserHomeDir()
	absDir := filepath.Join(home, "dev/app")
	f := &fakeRunner{responder: func(args []string) (string, bool) {
		if args[0] == "list-sessions" {
			return "app\t~/dev/app\t\nother\t~/dev/other\t\n", true
		}
		return "", false
	}}
	name, err := ensureWorkspace(f.run, absDir)
	if err != nil {
		t.Fatal(err)
	}
	if name != "app" {
		t.Fatalf("got %q want app", name)
	}
	if f.called("new-session") {
		t.Fatal("reuse must not create a session")
	}
}

func TestEnsureWorkspaceCreatesWhenMissing(t *testing.T) {
	home, _ := os.UserHomeDir()
	absDir := filepath.Join(home, "dev/fresh")
	f := &fakeRunner{responder: func(args []string) (string, bool) {
		if args[0] == "list-sessions" {
			return "", true // no sessions live
		}
		return "", false
	}}
	name, err := ensureWorkspace(f.run, absDir)
	if err != nil {
		t.Fatal(err)
	}
	if name != "fresh" {
		t.Fatalf("got %q want fresh (DeriveID of dir)", name)
	}
	if !f.called("new-session -d -s fresh -c " + absDir) {
		t.Fatalf("missing new-session call; calls=%v", f.calls)
	}
	// @duck_dir stamped tilde-form.
	if !f.called("set-option -t fresh @duck_dir ~/dev/fresh") {
		t.Fatalf("missing @duck_dir stamp; calls=%v", f.calls)
	}
}

func TestFreshSessionIDAvoidsCollision(t *testing.T) {
	home, _ := os.UserHomeDir()
	f := &fakeRunner{responder: func(args []string) (string, bool) {
		if args[0] == "list-sessions" {
			return "app\napp-2\n", true
		}
		return "", false
	}}
	got := freshSessionID(f.run, filepath.Join(home, "dev/app"))
	if got != "app-3" {
		t.Fatalf("got %q want app-3", got)
	}
}

// TestTickIgnoresManual: manual routines never auto-fire — no session
// creation, no state change.
func TestTickIgnoresManual(t *testing.T) {
	duckHome := t.TempDir()
	t.Setenv("DUCK_HOME", duckHome)
	proj := t.TempDir()
	writeRoutine(t, proj, "manualjob", "trigger = \"manual\"\n", "do a thing")
	if err := Enable(proj); err != nil {
		t.Fatal(err)
	}

	f := &fakeRunner{responder: func(args []string) (string, bool) {
		if args[0] == "list-sessions" {
			return "", true
		}
		return "", false
	}}
	var log bytes.Buffer
	if err := Tick(f.run, time.Now(), &log); err != nil {
		t.Fatal(err)
	}
	if f.called("new-session") {
		t.Fatalf("tick must not fire manual routines; calls=%v", f.calls)
	}
	st, _ := LoadState()
	if len(st.LastFire) != 0 {
		t.Fatalf("no beats should be recorded, got %v", st.LastFire)
	}
}

// TestTickFiresHeartbeat: a fresh heartbeat is due on the FIRST tick (no cron
// seed) — its persistent codex TUI pane spawns, the composer is awaited, and
// the prompt is typed in. The spawned cmdline is the TUI (no `exec`) with the
// notify hook wired.
func TestTickFiresHeartbeat(t *testing.T) {
	duckHome := t.TempDir()
	t.Setenv("DUCK_HOME", duckHome)
	t.Setenv("DUCK_CODEX_BIN", "echo-codex")
	oldSleep := sleepFn
	sleepFn = func(time.Duration) {}
	defer func() { sleepFn = oldSleep }()
	proj := t.TempDir()
	writeRoutine(t, proj, "beat", "trigger = \"heartbeat\"\ninterval = \"5m\"\n", "report status")
	if err := Enable(proj); err != nil {
		t.Fatal(err)
	}

	var spawnCmd string
	f := &fakeRunner{responder: func(args []string) (string, bool) {
		switch args[0] {
		case "list-sessions":
			return "", true
		case "has-session":
			return "", true
		case "list-windows":
			return "lot\n", true
		case "list-panes":
			return "", true // no existing pane named beat
		case "split-window":
			spawnCmd = strings.Join(args, " ")
			return "%42\n", true
		case "new-window":
			return "%2\n", true
		case "capture-pane":
			return "codex ready\n› \n", true // composer on screen
		}
		return "", false
	}}
	var log bytes.Buffer
	if err := Tick(f.run, time.Now(), &log); err != nil {
		t.Fatalf("tick: %v\nlog=%s", err, log.String())
	}
	if !strings.Contains(spawnCmd, "echo-codex --dangerously-bypass-approvals-and-sandbox") || strings.Contains(spawnCmd, " exec ") {
		t.Fatalf("heartbeat must spawn the persistent TUI (no exec); spawnCmd=%q\nlog=%s", spawnCmd, log.String())
	}
	if !strings.Contains(spawnCmd, `,"channel","notify"]`) {
		t.Fatalf("notify hook not wired on heartbeat pane; spawnCmd=%q", spawnCmd)
	}
	// The beat itself was typed into the new pane.
	if !f.called("send-keys") {
		t.Fatalf("beat prompt must be sent; calls=%v", f.calls)
	}
	sent := false
	for _, c := range f.calls {
		if strings.Contains(c, "send-keys -t %42 -l -- report status") {
			sent = true
		}
	}
	if !sent {
		t.Fatalf("prompt not delivered to the heartbeat pane; calls=%v", f.calls)
	}
	// And the beat was recorded.
	st, _ := LoadState()
	if st.LastFire[Key(proj, "beat")].IsZero() {
		t.Fatalf("heartbeat beat not recorded: %v", st.LastFire)
	}
}

// TestCourierDeliversBatchedDigest: breadcrumbs for a workspace become ONE
// send-keys digest into the main claude pane (no sidecar alive in this test),
// with report="none" routines filtered out.
func TestCourierDeliversBatchedDigest(t *testing.T) {
	duckHome := t.TempDir()
	t.Setenv("DUCK_HOME", duckHome)
	proj := t.TempDir()
	writeRoutine(t, proj, "loud", "trigger = \"manual\"\n", "p")
	writeRoutine(t, proj, "quiet", "trigger = \"manual\"\nreport = \"none\"\n", "p")

	for _, r := range []channel.RunReport{
		{Routine: "loud", Message: "42 tests passed\nlong detail", At: time.Now()},
		{Routine: "quiet", Message: "secret", At: time.Now()},
		{Routine: "adhoc", Message: "did a thing", At: time.Now()},
	} {
		if err := channel.ReportRun("work", r); err != nil {
			t.Fatal(err)
		}
	}

	var sent []string
	f := &fakeRunner{responder: func(args []string) (string, bool) {
		switch args[0] {
		case "show-options":
			return proj + "\n", true
		case "list-panes":
			return "%1\t\tclaude\n", true
		case "send-keys":
			sent = append(sent, strings.Join(args, " "))
			return "", true
		}
		return "", true
	}}
	var log bytes.Buffer
	courier(f.run, &log)

	if len(sent) == 0 {
		t.Fatalf("digest not sent; log=%s", log.String())
	}
	digest := strings.Join(sent, "\n")
	if !strings.Contains(digest, "routine loud completed: 42 tests passed") ||
		!strings.Contains(digest, "duck channel tail loud") {
		t.Fatalf("digest missing loud line: %q", digest)
	}
	if !strings.Contains(digest, "adhoc") {
		t.Fatalf("ad-hoc runs must be included: %q", digest)
	}
	if strings.Contains(digest, "quiet") || strings.Contains(digest, "secret") {
		t.Fatalf("report=none must be filtered: %q", digest)
	}
	// Drained: a second courier pass delivers nothing.
	sent = nil
	courier(f.run, &log)
	if len(sent) != 0 {
		t.Fatalf("breadcrumbs must be consumed: %v", sent)
	}
}

// TestFireManagerPrefersPublishThenSendKeys: target=manager delivers via the
// publish spool when the sidecar is alive, else types into the main claude
// pane, else drops the beat (recorded, logged).
func TestFireManagerPaths(t *testing.T) {
	duckHome := t.TempDir()
	t.Setenv("DUCK_HOME", duckHome)
	d := Def{Name: "digest", Dir: "/w", Trigger: TriggerCron, Target: TargetManager, Prompt: "daily digest please"}

	// Path 1: no sidecar, main pane runs claude → send-keys.
	f := &fakeRunner{responder: func(args []string) (string, bool) {
		switch args[0] {
		case "list-sessions":
			return "work\t/w\t1\t1\t1\n", true
		case "has-session":
			return "", true
		case "list-windows":
			return "lot\n", true
		case "list-panes":
			return "%1\t\tclaude\n%2\tviewport\tzsh\n", true
		}
		return "", false
	}}
	var log bytes.Buffer
	if !fireManager(f.run, d, "work", &log) {
		t.Fatalf("fireManager failed: %s", log.String())
	}
	typed := false
	for _, c := range f.calls {
		if strings.Contains(c, "send-keys -t %1 -l -- daily digest please") {
			typed = true
		}
	}
	if !typed {
		t.Fatalf("manager turn not typed into main claude pane; calls=%v", f.calls)
	}

	// Path 2: no sidecar, no claude in the main pane → beat dropped (true), logged.
	f2 := &fakeRunner{responder: func(args []string) (string, bool) {
		if args[0] == "list-panes" {
			return "%1\t\tzsh\n", true
		}
		return "", true
	}}
	log.Reset()
	if !fireManager(f2.run, d, "work", &log) {
		t.Fatal("absent manager must drop the beat, not error")
	}
	if !strings.Contains(log.String(), "no manager claude") {
		t.Fatalf("drop must be logged: %s", log.String())
	}
}

// TestTickFiresDueCron drives a due cron routine end to end against the fake
// runner and asserts the executor pane spawn + last-fire recording.
func TestTickFiresDueCron(t *testing.T) {
	duckHome := t.TempDir()
	t.Setenv("DUCK_HOME", duckHome)
	// No codex on the test box; the cmdline is inspected, never run.
	t.Setenv("DUCK_CODEX_BIN", "echo-codex")
	proj := t.TempDir()
	writeRoutine(t, proj, "nightly", "trigger = \"cron\"\nschedule = \"* * * * *\"\n", "run the nightly job")
	if err := Enable(proj); err != nil {
		t.Fatal(err)
	}

	// Seed a last-fire well in the past so "* * * * *" is due now.
	st, _ := LoadState()
	st.LastFire[Key(proj, "nightly")] = time.Now().Add(-time.Hour)
	if err := SaveState(st); err != nil {
		t.Fatal(err)
	}

	var spawnCmd string
	f := &fakeRunner{responder: func(args []string) (string, bool) {
		joined := strings.Join(args, " ")
		switch {
		case args[0] == "list-sessions":
			return "", true // nothing live → ensureWorkspace creates
		case args[0] == "has-session":
			return "", true // companion "exists" so EnsureCompanion is cheap
		case args[0] == "list-windows":
			return "lot\n", true // companion already has a lot window
		case args[0] == "list-panes":
			return "", true // no existing run pane, no viewport
		case args[0] == "split-window":
			// capture the spawned cmdline (last arg after `sh -c`)
			for i, a := range args {
				if a == "-P" && i+2 < len(args) {
					// pane id is returned; capture full argv for cmdline check
				}
			}
			spawnCmd = joined
			return "%99\n", true
		case args[0] == "new-window":
			return "%2\n", true
		}
		return "", false
	}}
	var log bytes.Buffer
	if err := Tick(f.run, time.Now(), &log); err != nil {
		t.Fatalf("tick: %v\nlog=%s", err, log.String())
	}
	if !strings.Contains(spawnCmd, "echo-codex exec --dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("executor cmdline not spawned; spawnCmd=%q\nlog=%s\ncalls=%v", spawnCmd, log.String(), f.calls)
	}
	// Prompt must be shell-quoted, not bare.
	if !strings.Contains(spawnCmd, "'run the nightly job'") {
		t.Fatalf("prompt not shell-quoted; spawnCmd=%q", spawnCmd)
	}
	// The codex notify hook must be wired so the executor's rollout is pinned
	// exactly (same treatment duck spawn gives interactive agents).
	if !strings.Contains(spawnCmd, `,"channel","notify"]`) {
		t.Fatalf("notify hook not wired; spawnCmd=%q", spawnCmd)
	}
	// last-fire advanced to ~now.
	st2, _ := LoadState()
	if got := st2.LastFire[Key(proj, "nightly")]; time.Since(got) > time.Minute {
		t.Fatalf("last-fire not advanced: %v", got)
	}
}

// TestTickHealsPersistentWorkspace verifies the heal path: a Persistent record
// whose session is not live is recreated headless (new-session under the
// record's own name) with @duck_dir and @duck_parent restamped, before any
// firing. Uses a scratch ledger over the local sh runner so the real
// ~/.claude/projects is never touched.
func TestTickHealsPersistentWorkspace(t *testing.T) {
	duckHome := t.TempDir()
	t.Setenv("DUCK_HOME", duckHome) // isolate routines-state/-projects

	// Point the ledger at a scratch base and seed one persistent record with a
	// parent. Restore the package seam after the test.
	base := t.TempDir()
	prev := wsStore
	wsStore = func() *workspaces.Store {
		s := workspaces.NewStore(workspaces.LocalRunner{})
		s.SetBase(base)
		return s
	}
	defer func() { wsStore = prev }()

	rec := workspaces.Record{Name: "duck-9", Dir: "~/dev/persistent", Parent: "motherduck", Persistent: true}
	if err := wsStore().Save(rec); err != nil {
		t.Fatalf("seed record: %v", err)
	}

	// No live sessions anywhere → heal must revive duck-9. list-sessions returns
	// empty for every format the tick asks.
	f := &fakeRunner{responder: func(args []string) (string, bool) {
		if args[0] == "list-sessions" {
			return "", true
		}
		return "", false
	}}
	var log bytes.Buffer
	if err := Tick(f.run, time.Now(), &log); err != nil {
		t.Fatalf("tick: %v\nlog=%s", err, log.String())
	}

	if !f.called("new-session -d -s duck-9") {
		t.Fatalf("heal must new-session the persistent workspace; calls=%v\nlog=%s", f.calls, log.String())
	}
	if !f.called("set-option -t duck-9 @duck_dir ~/dev/persistent") {
		t.Fatalf("heal must restamp @duck_dir; calls=%v", f.calls)
	}
	if !f.called("set-option -t duck-9 @duck_parent motherduck") {
		t.Fatalf("heal must restamp @duck_parent from the record; calls=%v", f.calls)
	}
}
