package panel

import (
	"errors"
	"strings"
	"testing"
)

// fakeRunner scripts tmux responses by argv prefix (joined with spaces) and
// records every call.
type fakeRunner struct {
	calls []string
	out   map[string]string // exact joined argv → stdout
	errs  map[string]error  // exact joined argv → error
}

func (f *fakeRunner) run(args ...string) (string, error) {
	key := strings.Join(args, " ")
	f.calls = append(f.calls, key)
	if err, ok := f.errs[key]; ok {
		return "", err
	}
	return f.out[key], nil
}

func (f *fakeRunner) called(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func TestEnsureCompanionCreatesOnceAndStamps(t *testing.T) {
	f := &fakeRunner{
		out:  map[string]string{},
		errs: map[string]error{"has-session -t =work-agents": errors.New("can't find session")},
	}
	comp, err := EnsureCompanion(f.run, "work", "/home/u/dev")
	if err != nil {
		t.Fatal(err)
	}
	if comp != "work-agents" {
		t.Fatalf("companion name = %q", comp)
	}
	if !f.called("new-session -d -s work-agents -c /home/u/dev -n terminal -P -F #{window_id}") {
		t.Fatalf("companion not created: %v", f.calls)
	}
	for _, opt := range []string{
		"set-option -t work-agents @duck_panel_of work",
		"set-option -t work-agents status off",
		"set-option -t work-agents detach-on-destroy off",
	} {
		if !f.called(opt) {
			t.Errorf("missing %q", opt)
		}
	}

	// Already exists → no create.
	f2 := &fakeRunner{out: map[string]string{}}
	if _, err := EnsureCompanion(f2.run, "work", "/x"); err != nil {
		t.Fatal(err)
	}
	if f2.called("new-session") {
		t.Fatal("recreated an existing companion")
	}
}

func TestAgentsParsesAndSkipsPlaceholder(t *testing.T) {
	f := &fakeRunner{out: map[string]string{
		"list-windows -t work-agents -F #{window_id}\t#{window_index}\t#{window_name}\t#{window_active}\t#{pane_current_command}\t#{@duck_placeholder}\t#{@duck_kind}\t#{pane_title}": "@1\t0\tterminal\t0\tsh\t1\t\t\n" +
			"@2\t1\tcodex\t1\tcodex\t\tagents\tfixing tests\n" +
			"@3\t2\tbuild\t0\tcargo\t\t\t\n",
	}}
	agents, err := Agents(f.run, "work-agents")
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 {
		t.Fatalf("want 2 agents (placeholder skipped), got %d: %+v", len(agents), agents)
	}
	if agents[0].WindowID != "@2" || !agents[0].Active || agents[0].Title != "fixing tests" {
		t.Errorf("agent[0] = %+v", agents[0])
	}
	if agents[1].Name != "build" || agents[1].Index != 2 || agents[1].Active {
		t.Errorf("agent[1] = %+v", agents[1])
	}
}

func TestSpawnRetiresPlaceholderAndPinsName(t *testing.T) {
	listKey := "list-windows -t work-agents -F #{window_id}\t#{window_index}\t#{window_name}\t#{window_active}\t#{pane_current_command}\t#{@duck_placeholder}\t#{@duck_kind}\t#{pane_title}"
	f := &fakeRunner{out: map[string]string{
		"new-window -t work-agents: -P -F #{window_id} -n codex -c /d codex": "@7\n",
		listKey: "@1\t0\tterminal\t0\tsh\t1\t\t\n@7\t1\tcodex\t1\tcodex\t\tagents\t\n",
	}}
	id, err := Spawn(f.run, "work-agents", "codex", "/d", "codex", KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	if id != "@7" {
		t.Fatalf("window id = %q", id)
	}
	if !f.called("set-option -w -t @7 automatic-rename off") {
		t.Error("named window should pin automatic-rename off")
	}
	if f.called("kill-window") {
		t.Errorf("placeholder must NOT be retired (it keeps the companion alive): %v", f.calls)
	}
	if f.called("new-window -d") {
		t.Errorf("placeholder already exists; must not create another: %v", f.calls)
	}
}

func TestOpenIsIdempotentWhenPanesExist(t *testing.T) {
	f := &fakeRunner{out: map[string]string{
		"list-panes -s -t work -F #{pane_id}\t#{@duck_panel_role}": "%1\t\n%2\tviewport\n%3\tlist\n",
	}}
	if err := Open(f.run, "work", "work-agents", "/bin/duck"); err != nil {
		t.Fatal(err)
	}
	if f.called("split-window") {
		t.Fatalf("existing panel must not re-split: %v", f.calls)
	}
}

func TestOpenCreatesViewportAndList(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-500/default,1234,0")
	f := &fakeRunner{out: map[string]string{
		"list-panes -s -t work -F #{pane_id}\t#{@duck_panel_role}":                                                                          "%1\t\n",
		"split-window -h -f -d -l 34% -t work: -P -F #{pane_id} TMUX= exec tmux -S '/tmp/tmux-500/default' attach-session -t 'work-agents'": "%8\n",
		"split-window -v -d -l 10 -t %8 -P -F #{pane_id} '/bin/duck' panel watch 'work'":                                                    "%9\n",
	}}
	if err := Open(f.run, "work", "work-agents", "/bin/duck"); err != nil {
		t.Fatal(err)
	}
	if !f.called("set-option -t work mouse on") {
		t.Error("mouse should be enabled on the outer session")
	}
	if !f.called("set-option -p -t %8 @duck_panel_role viewport") {
		t.Errorf("viewport pane not stamped: %v", f.calls)
	}
	if !f.called("set-option -p -t %9 @duck_panel_role list") {
		t.Errorf("list pane not stamped: %v", f.calls)
	}
}

// TestSpawnHealsMissingPlaceholder pins the keep-alive invariant: a companion
// without a placeholder (legacy state) gets one back on the next spawn, so
// the companion can never die with its last agent.
func TestSpawnHealsMissingPlaceholder(t *testing.T) {
	listKey := "list-windows -t work-agents -F " + agentsFormat
	f := &fakeRunner{out: map[string]string{
		"new-window -t work-agents: -P -F #{window_id} -n codex -c /d codex": "@7\n",
		listKey: "@7\t1\tcodex\t1\tcodex\t\tagents\t\n", // no placeholder anywhere
		"new-window -d -t work-agents: -n terminal -P -F #{window_id} " + placeholderCmd: "@9\n",
	}}
	if _, err := Spawn(f.run, "work-agents", "codex", "/d", "codex", KindAgent); err != nil {
		t.Fatal(err)
	}
	if !f.called("set-option -w -t @9 @duck_placeholder 1") {
		t.Errorf("healed placeholder must be marker-stamped: %v", f.calls)
	}
}
