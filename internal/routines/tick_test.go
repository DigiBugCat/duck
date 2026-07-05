package routines

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/DigiBugCat/duck/internal/channel"
	"github.com/DigiBugCat/duck/internal/workspaces"
)

// fakeRunner scripts tmux responses by argv-prefix and records calls, so the
// tmux-free logic (workspace sweep, firing) is unit-testable.
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

// TestTickIgnoresManual: manual routines never auto-fire — no spawn, no
// state change.
func TestTickIgnoresManual(t *testing.T) {
	t.Setenv("DUCK_HOME", t.TempDir())
	writeRoutine(t, "work", "manualjob", "trigger = \"manual\"\n", "do a thing")

	f := &fakeRunner{responder: func(args []string) (string, bool) {
		if args[0] == "list-sessions" {
			return "work\n", true // the workspace is live
		}
		return "", false
	}}
	var log bytes.Buffer
	if err := Tick(f.run, time.Now(), &log); err != nil {
		t.Fatal(err)
	}
	if f.called("split-window") {
		t.Fatalf("tick must not fire manual routines; calls=%v", f.calls)
	}
	st, _ := LoadState()
	if len(st.LastFire) != 0 {
		t.Fatalf("no beats should be recorded, got %v", st.LastFire)
	}
}

// TestTickSkipsDormantWorkspace: a routine whose workspace session is gone
// (and not healable) fires nothing — its duties sleep, logged.
func TestTickSkipsDormantWorkspace(t *testing.T) {
	t.Setenv("DUCK_HOME", t.TempDir())
	writeRoutine(t, "ghost", "beat", "trigger = \"heartbeat\"\ninterval = \"5m\"\n", "p")

	f := &fakeRunner{responder: func(args []string) (string, bool) {
		if args[0] == "list-sessions" {
			return "", true // nothing live
		}
		return "", false
	}}
	var log bytes.Buffer
	if err := Tick(f.run, time.Now(), &log); err != nil {
		t.Fatal(err)
	}
	if f.called("split-window") {
		t.Fatalf("dormant workspace must not fire; calls=%v", f.calls)
	}
	if !strings.Contains(log.String(), "ghost gone") {
		t.Fatalf("dormancy must be logged: %s", log.String())
	}
}

// heartbeatResponder scripts a live workspace "work" with an empty lot and
// captures the spawn cmdline.
func heartbeatResponder(spawnCmd *string) func(args []string) (string, bool) {
	return func(args []string) (string, bool) {
		switch args[0] {
		case "list-sessions":
			return "work\n", true
		case "show-options":
			return "~/dev/work\n", true // @duck_dir
		case "has-session":
			return "", true
		case "list-windows":
			return "lot\n", true
		case "list-panes":
			return "", true // no existing pane by this name
		case "split-window":
			*spawnCmd = strings.Join(args, " ")
			return "%42\n", true
		case "new-window":
			return "%2\n", true
		case "capture-pane":
			return "codex ready\n› \n", true // composer on screen
		}
		return "", false
	}
}

// TestTickFiresHeartbeat: a fresh heartbeat is due on the FIRST tick (no cron
// seed) — its persistent codex TUI pane spawns IN ITS OWN WORKSPACE, the
// composer is awaited, and the prompt is typed in. The spawned cmdline is the
// TUI (no `exec`) with the notify hook wired.
func TestTickFiresHeartbeat(t *testing.T) {
	t.Setenv("DUCK_HOME", t.TempDir())
	t.Setenv("DUCK_CODEX_BIN", "echo-codex")
	defer channel.SetSleepFn(func(time.Duration) {})()
	writeRoutine(t, "work", "beat", "trigger = \"heartbeat\"\ninterval = \"5m\"\n", "report status")

	var spawnCmd string
	f := &fakeRunner{responder: heartbeatResponder(&spawnCmd)}
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
	// The pane lands in the routine's OWN workspace's lot.
	if !strings.Contains(spawnCmd, "-t work-agents:lot") {
		t.Fatalf("heartbeat pane must land in the owning workspace; spawnCmd=%q", spawnCmd)
	}
	// The beat itself was typed into the new pane.
	sent := false
	for _, c := range f.calls {
		if strings.Contains(c, "send-keys -t %42 -l -- report status") {
			sent = true
		}
	}
	if !sent {
		t.Fatalf("prompt not delivered to the heartbeat pane; calls=%v", f.calls)
	}
	// And the beat was recorded under the workspace key.
	st, _ := LoadState()
	if st.LastFire[Key("work", "beat")].IsZero() {
		t.Fatalf("heartbeat beat not recorded: %v", st.LastFire)
	}
}

// TestCourierDeliversBatchedDigest: breadcrumbs for a workspace become ONE
// send-keys digest into the main claude pane (no sidecar alive in this test),
// with report="none" routines filtered out.
func TestCourierDeliversBatchedDigest(t *testing.T) {
	t.Setenv("DUCK_HOME", t.TempDir())
	writeRoutine(t, "work", "loud", "trigger = \"manual\"\n", "p")
	writeRoutine(t, "work", "quiet", "trigger = \"manual\"\nreport = \"none\"\n", "p")

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

func TestManagerPaneUsesValidDuckManagerWithoutSniff(t *testing.T) {
	f := &fakeRunner{responder: func(args []string) (string, bool) {
		switch args[0] {
		case "show-options":
			return "%9\n", true
		case "display-message":
			if args[len(args)-1] != "#{pane_id}\t#{pane_current_command}" {
				t.Fatalf("unexpected display-message args: %v", args)
			}
			return "%9\tclaude\n", true
		case "list-panes":
			t.Fatal("valid @duck_manager must avoid sniffing list-panes")
		}
		return "", false
	}}
	pane, ok := managerPane(f.run, "work")
	if !ok || pane != "%9" {
		t.Fatalf("managerPane = %q, %v; calls=%v", pane, ok, f.calls)
	}
}

func TestManagerPaneFallsBackAndRestampsStaleDuckManager(t *testing.T) {
	f := &fakeRunner{responder: func(args []string) (string, bool) {
		switch args[0] {
		case "show-options":
			return "%1\n", true
		case "display-message":
			return "%1\tzsh\n", true
		case "list-panes":
			return "%2\tviewport\tzsh\n%3\t\tclaude\n", true
		case "set-option":
			return "", true
		}
		return "", false
	}}
	pane, ok := managerPane(f.run, "work")
	if !ok || pane != "%3" {
		t.Fatalf("managerPane = %q, %v; calls=%v", pane, ok, f.calls)
	}
	if !f.called("set-option -t work @duck_manager %3") {
		t.Fatalf("fallback must restamp @duck_manager; calls=%v", f.calls)
	}
}

// TestFireManagerPaths: target=manager delivers via the publish spool when
// the sidecar is alive, else types into the main claude pane, else drops the
// beat (recorded, logged).
func TestFireManagerPaths(t *testing.T) {
	t.Setenv("DUCK_HOME", t.TempDir())
	d := Def{Name: "digest", Workspace: "work", Trigger: TriggerCron, Target: TargetManager, Prompt: "daily digest please"}

	// Path 1: no sidecar, main pane runs claude → send-keys.
	f := &fakeRunner{responder: func(args []string) (string, bool) {
		switch args[0] {
		case "list-sessions":
			return "work\n", true
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
// runner and asserts the executor pane spawn + last-fire recording — all in
// the routine's owning workspace.
func TestTickFiresDueCron(t *testing.T) {
	t.Setenv("DUCK_HOME", t.TempDir())
	// No codex on the test box; the cmdline is inspected, never run.
	t.Setenv("DUCK_CODEX_BIN", "echo-codex")
	writeRoutine(t, "work", "nightly", "trigger = \"cron\"\nschedule = \"* * * * *\"\n", "run the nightly job")

	// Seed a last-fire well in the past so "* * * * *" is due now.
	st, _ := LoadState()
	st.LastFire[Key("work", "nightly")] = time.Now().Add(-time.Hour)
	if err := SaveState(st); err != nil {
		t.Fatal(err)
	}

	var spawnCmd string
	f := &fakeRunner{responder: func(args []string) (string, bool) {
		switch args[0] {
		case "list-sessions":
			return "work\n", true // the workspace is live
		case "show-options":
			return "~/dev/work\n", true // @duck_dir
		case "has-session":
			return "", true // companion "exists" so EnsureCompanion is cheap
		case "list-windows":
			return "lot\n", true // companion already has a lot window
		case "list-panes":
			return "", true // no existing run pane, no viewport
		case "split-window":
			spawnCmd = strings.Join(args, " ")
			return "%99\n", true
		case "new-window":
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
	// last-fire advanced to ~now, under the workspace key.
	st2, _ := LoadState()
	if got := st2.LastFire[Key("work", "nightly")]; time.Since(got) > time.Minute {
		t.Fatalf("last-fire not advanced: %v", got)
	}
}

// TestTickHealsPersistentWorkspace verifies the heal path: a Persistent record
// whose session is not live is recreated headless (new-session under the
// record's own name) with @duck_dir and @duck_parent restamped, before any
// firing. Uses a scratch ledger over the local sh runner so the real
// ~/.claude/projects is never touched.
func TestTickHealsPersistentWorkspace(t *testing.T) {
	t.Setenv("DUCK_HOME", t.TempDir()) // isolate routines state/defs

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

func TestTickHealsDeadManagerInLivePersistentWorkspace(t *testing.T) {
	t.Setenv("DUCK_HOME", t.TempDir())

	base := t.TempDir()
	prev := wsStore
	wsStore = func() *workspaces.Store {
		s := workspaces.NewStore(workspaces.LocalRunner{})
		s.SetBase(base)
		return s
	}
	defer func() { wsStore = prev }()

	rec := workspaces.Record{Name: "duck-9", Dir: "~/dev/persistent", Persistent: true}
	if err := wsStore().Save(rec); err != nil {
		t.Fatalf("seed record: %v", err)
	}

	f := &fakeRunner{responder: func(args []string) (string, bool) {
		switch args[0] {
		case "list-sessions":
			return "duck-9\n", true
		case "show-options":
			return "%1\n", true
		case "display-message":
			if args[len(args)-1] == "#{pane_id}\t#{pane_current_command}" {
				return "%1\tzsh\n", true
			}
			return "%8\n", true
		case "list-panes":
			return "%1\t\tzsh\n", true
		case "send-keys", "set-option":
			return "", true
		}
		return "", false
	}}
	var log bytes.Buffer
	if err := Tick(f.run, time.Now(), &log); err != nil {
		t.Fatalf("tick: %v\nlog=%s", err, log.String())
	}
	joined := strings.Join(f.calls, "\n")
	for _, want := range []string{"send-keys -t duck-9", "claude", "--dangerously-load-development-channels", "server:duck-agents"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("persistent dead manager relaunch missing %q; calls=%v\nlog=%s", want, f.calls, log.String())
		}
	}
	if !f.called("set-option -t duck-9 @duck_manager %8") {
		t.Fatalf("relaunched manager must restamp @duck_manager; calls=%v", f.calls)
	}
}
