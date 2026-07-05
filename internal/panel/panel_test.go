package panel

import (
	"errors"
	"strings"
	"testing"
)

// fakeRunner scripts tmux responses by exact joined argv and records calls.
type fakeRunner struct {
	calls []string
	out   map[string]string
	errs  map[string]error
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

const rolesKey = "list-panes -s -t work -F #{pane_id}\t#{@duck_panel_role}"

func agentsKey(target string) string {
	return "list-panes -s -t " + target + " -F " + agentsFormat
}

func TestEnsureCompanionCreatesLotWithAnchor(t *testing.T) {
	f := &fakeRunner{
		out:  map[string]string{"new-session -d -s work-agents -c /d -n lot -P -F #{pane_id} " + anchorCmd: "%1\n"},
		errs: map[string]error{"has-session -t =work-agents": errors.New("can't find session")},
	}
	comp, err := EnsureCompanion(f.run, "work", "/d")
	if err != nil || comp != "work-agents" {
		t.Fatalf("comp=%q err=%v", comp, err)
	}
	for _, want := range []string{
		"set-option -p -t %1 @duck_anchor 1",
		"set-option -t work-agents @duck_panel_of work",
		"set-option -t work-agents status off",
	} {
		if !f.called(want) {
			t.Errorf("missing %q in %v", want, f.calls)
		}
	}
	// Idempotent: existing companion → no create.
	f2 := &fakeRunner{out: map[string]string{}}
	if _, err := EnsureCompanion(f2.run, "work", "/d"); err != nil || f2.called("new-session") {
		t.Fatal("must not recreate an existing companion")
	}
}

func TestAgentsMergesSlotOccupantAndParkedPanes(t *testing.T) {
	f := &fakeRunner{out: map[string]string{
		rolesKey: "%0\t\n%5\tviewport\n%6\tlist\n",
		// outer: slot occupant is a stamped terminal; %0 (user's main pane) unstamped.
		agentsKey("work"): "%0\t\t\t\t\tzsh\t\n%5\tterminal\tshells\t\tviewport\tzsh\t\n%6\t\t\t\tlist\tduck\t\n",
		// lot: anchor (hidden) + parked codex.
		agentsKey("work-agents"): "%1\t\t\t1\t\tsh\t\n%7\tfixer\tagents\t\t\tcodex\tworking away\n",
	}}
	agents, err := Agents(f.run, "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 {
		t.Fatalf("want terminal+fixer, got %+v", agents)
	}
	if agents[0].Name != "terminal" || !agents[0].Active || agents[0].Kind != KindShell {
		t.Errorf("slot occupant wrong: %+v", agents[0])
	}
	if agents[1].PaneID != "%7" || agents[1].Active || agents[1].Title != "working away" {
		t.Errorf("parked agent wrong: %+v", agents[1])
	}
}

func TestWorkspacesUsesManagerClaudeTitle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	outer := "duck-workspace-title"
	f := &fakeRunner{out: map[string]string{
		"list-sessions -F #{session_name}\t#{session_attached}\t#{@duck_dir}\t#{@duck_panel_of}": outer + "\t0\t/home/andrew/Obsidian/aviary/duck\t\n",
		"list-panes -a -F #{session_name}\t#{pane_id}\t#{@duck_panel_role}\t#{pane_current_command}\t#{pane_title}": outer + "\t%1\tviewport\tzsh\tpelican\n" +
			outer + "\t%2\t\tclaude\t\u2733 Fix workspace names\n",
		"list-panes -t " + outer + ": -F #{pane_current_path}\t#{@duck_panel_role}": "/home/andrew/Obsidian/aviary/duck\t\n",
	}}

	ws, err := Workspaces(f.run, outer)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 1 {
		t.Fatalf("want one workspace, got %+v", ws)
	}
	if ws[0].Display != "Fix workspace names" {
		t.Fatalf("display = %q, want manager Claude summary", ws[0].Display)
	}
}

func TestSpawnParksStampsAndSelects(t *testing.T) {
	f := &fakeRunner{out: map[string]string{
		"split-window -d -t work-agents:lot -P -F #{pane_id} -c /d sh -c 'codex; ec=$?; if [ $ec -ne 0 ]; then printf '\\''\\n[exited %d — enter to close] '\\'' \"$ec\"; read -r _; fi'": "%9\n",
		rolesKey: "%5\tviewport\n",
	}}
	id, err := Spawn(f.run, "work", "fixer", "/d", "codex", KindAgent)
	if err != nil || id != "%9" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	for _, want := range []string{
		"set-option -p -t %9 @duck_name fixer",
		"set-option -p -t %9 @duck_kind agents",
		"set-option -p -t %9 @duck_spawned_at ",
		"swap-pane -d -s %9 -t %5",                      // Select: newcomer goes on display
		"set-option -p -t %9 @duck_panel_role viewport", // role re-stamped onto slot occupant
	} {
		if !f.called(want) {
			t.Errorf("missing %q in %v", want, f.calls)
		}
	}
}

func TestSelectSwapsAndRestampsRole(t *testing.T) {
	f := &fakeRunner{out: map[string]string{rolesKey: "%5\tviewport\n"}}
	if err := Select(f.run, "work", "%7"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"swap-pane -d -s %7 -t %5",
		"set-option -p -t %5 -u @duck_panel_role", // old occupant (now parked) loses the role
		"set-option -p -t %7 @duck_panel_role viewport",
	} {
		if !f.called(want) {
			t.Errorf("missing %q in %v", want, f.calls)
		}
	}
	// Selecting the pane already on display is a no-op.
	f2 := &fakeRunner{out: map[string]string{rolesKey: "%7\tviewport\n"}}
	if err := Select(f2.run, "work", "%7"); err != nil || f2.called("swap-pane") {
		t.Fatal("selecting the displayed pane must not swap")
	}
}

func TestOpenCreatesTerminalSlotAndRoster(t *testing.T) {
	f := &fakeRunner{out: map[string]string{
		rolesKey: "%0\t\n",
		"split-window -h -f -d -l 34% -t work: -P -F #{pane_id} " + terminalCmd:          "%5\n",
		"split-window -v -d -l 15 -t %5 -P -F #{pane_id} '/bin/duck' panel watch 'work'": "%6\n",
	}}
	if err := Open(f.run, "work", "work-agents", "/bin/duck"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"set-option -t work mouse on",
		"set-option -t work allow-passthrough on",
		"set-option -p -t %5 @duck_panel_role viewport",
		"set-option -p -t %5 @duck_name terminal",
		"set-option -p -t %6 @duck_panel_role list",
	} {
		if !f.called(want) {
			t.Errorf("missing %q in %v", want, f.calls)
		}
	}
	// Idempotent when both panes exist.
	f2 := &fakeRunner{out: map[string]string{rolesKey: "%5\tviewport\n%6\tlist\n"}}
	if err := Open(f2.run, "work", "work-agents", "/bin/duck"); err != nil || f2.called("split-window") {
		t.Fatal("existing panel must not re-split")
	}
}

func TestCloseParksOccupantNeverKillsIt(t *testing.T) {
	f := &fakeRunner{out: map[string]string{rolesKey: "%5\tviewport\n%6\tlist\n"}}
	if err := Close(f.run, "work"); err != nil {
		t.Fatal(err)
	}
	if !f.called("break-pane -d -s %5 -t work-agents:lot") {
		t.Errorf("occupant must be parked, not killed: %v", f.calls)
	}
	if f.called("kill-pane -t %5") {
		t.Error("close must never kill the occupant")
	}
	if !f.called("kill-pane -t %6") {
		t.Error("roster pane should be killed")
	}
}

// TestEnsureCompanionMigratesOldDesign pins the upgrade path: a pre-swap
// companion (no 'lot' window) gets one retrofitted instead of wedging every
// comp:lot operation with "can't find window".
func TestEnsureCompanionMigratesOldDesign(t *testing.T) {
	f := &fakeRunner{out: map[string]string{
		"list-windows -t work-agents -F #{window_name}":                      "codex\npreview\nterminal\n", // old windows design
		"new-window -d -t work-agents: -n lot -P -F #{pane_id} " + anchorCmd: "%9\n",
	}}
	if _, err := EnsureCompanion(f.run, "work", "/d"); err != nil {
		t.Fatal(err)
	}
	if !f.called("new-window -d -t work-agents: -n lot") {
		t.Fatalf("old companion must get a lot window: %v", f.calls)
	}
	if !f.called("set-option -p -t %9 @duck_anchor 1") {
		t.Error("retrofitted lot must have a stamped anchor")
	}
	// Modern companion (lot present) → untouched.
	f2 := &fakeRunner{out: map[string]string{
		"list-windows -t work-agents -F #{window_name}": "lot\n",
	}}
	if _, err := EnsureCompanion(f2.run, "work", "/d"); err != nil || f2.called("new-window") {
		t.Fatal("modern companion must not be modified")
	}
}
