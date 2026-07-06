package panel

import (
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func syncModel(agents []Agent) watchModel {
	return watchModel{
		tabKind:   KindAgent,
		tabCursor: map[string]int{},
		agents:    agents,
	}
}

func TestSyncToOccupantFollowsNewOccupant(t *testing.T) {
	m := syncModel([]Agent{
		{PaneID: "%1", Name: "a", Kind: KindAgent},
		{PaneID: "%2", Name: "chart", Kind: KindArtifact, Active: true},
		{PaneID: "%3", Name: "table", Kind: KindArtifact},
	})
	m.syncToOccupant()
	if m.tabKind != KindArtifact {
		t.Errorf("tab should follow occupant kind, got %q", m.tabKind)
	}
	if m.cursor != 0 {
		t.Errorf("cursor should land on the occupant row, got %d", m.cursor)
	}
	if m.lastActive != "%2" {
		t.Errorf("lastActive = %q, want %%2", m.lastActive)
	}

	// Same occupant on the next poll: the user navigates away and must not
	// be yanked back.
	m.tabKind, m.cursor = KindAgent, 0
	m.syncToOccupant()
	if m.tabKind != KindAgent {
		t.Errorf("unchanged occupant must not re-sync the tab, got %q", m.tabKind)
	}
}

func TestSyncToOccupantIgnoresPlaceholder(t *testing.T) {
	m := syncModel([]Agent{
		{PaneID: "%5", Kind: "", Active: true}, // filler pane (empty kind)
	})
	m.tabKind, m.cursor = KindArtifact, 2
	m.syncToOccupant()
	if m.tabKind != KindArtifact || m.cursor != 2 {
		t.Errorf("placeholder occupant must leave tab/cursor alone, got %q/%d", m.tabKind, m.cursor)
	}
	if m.lastActive != "%5" {
		t.Errorf("lastActive should still advance, got %q", m.lastActive)
	}
}

func TestWindowArtifactEnterRefocusesClientWindow(t *testing.T) {
	f := &fakeRunner{out: map[string]string{
		rolesKey:                                        "%5\tviewport\n",
		"show-options -p -t %8 -v @duck_url":            "http://hub:7327/dash\n",
		"list-clients -t work -F #{client_name}":        "",
		"select-layout -t work-agents:lot tiled":        "",
		"swap-pane -d -s %8 -t %5":                      "",
		"set-option -p -t %5 -u @duck_panel_role":       "",
		"set-option -p -t %8 @duck_panel_role viewport": "",
	}}
	var got []string
	m := watchModel{
		run:       f.run,
		outer:     "work",
		tabKind:   KindArtifact,
		tabCursor: map[string]int{},
		agents: []Agent{
			{PaneID: "%8", Name: "dashboard", Kind: KindArtifact, RawKind: KindWindow},
		},
		duckExecFn: func(args ...string) string {
			got = append([]string{}, args...)
			return "shown: http://hub:7327/dash"
		},
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(watchModel)
	if !reflect.DeepEqual(got, []string{"window", "http://hub:7327/dash", "dashboard"}) {
		t.Fatalf("duck exec args = %#v", got)
	}
	if m.lastMsg != "shown: http://hub:7327/dash" {
		t.Fatalf("lastMsg = %q", m.lastMsg)
	}
}

func TestWindowArtifactXRemovesPlaceholderRow(t *testing.T) {
	f := &fakeRunner{}
	m := watchModel{
		run:       f.run,
		outer:     "work",
		tabKind:   KindArtifact,
		tabCursor: map[string]int{},
		agents: []Agent{
			{PaneID: "%8", Name: "dashboard", Kind: KindArtifact, RawKind: KindWindow},
		},
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = next.(watchModel)
	if !f.called("kill-pane -t %8") {
		t.Fatalf("x should remove the placeholder pane: %v", f.calls)
	}
	if m.armedKill != "" {
		t.Fatalf("window removal should not use x-twice arming, got %q", m.armedKill)
	}
	if m.lastMsg != "removed dashboard" {
		t.Fatalf("lastMsg = %q", m.lastMsg)
	}
}

func TestWorkflowsSectionInAgentsTab(t *testing.T) {
	m := watchModel{
		tabKind:   KindAgent,
		tabCursor: map[string]int{},
		agents: []Agent{
			{PaneID: "%1", Name: "claude", Kind: KindAgent},
			{PaneID: "%2", Name: "chart", Kind: KindArtifact},
		},
		workflows: []workflowRow{
			{RunID: "wf_x", Name: "audit", State: "running", Agents: "3/9", Tokens: "120k"},
			{RunID: "wf_y", Name: "sweep", State: "done", Agents: "4/4", Tokens: "80k"},
		},
	}
	// Agents tab: one agent row, the divider, two workflow rows.
	want := []int{0, wfDividerRow, wfEncode(0), wfEncode(1)}
	if got := m.visible(); !reflect.DeepEqual(got, want) {
		t.Fatalf("visible = %v, want %v", got, want)
	}
	// Other tabs never grow the section.
	m.tabKind = KindArtifact
	if got := m.visible(); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("artifact tab visible = %v", got)
	}
	m.tabKind = KindAgent

	// Down from the agent row skips the divider onto the first run.
	m.cursor = 1 // as if the down key landed on the divider
	m.skipDivider(1)
	if w, ok := m.selectedWorkflow(); !ok || w.RunID != "wf_x" {
		t.Fatalf("after skip: selectedWorkflow = %v %v", w, ok)
	}
	// Up from the first run skips back onto the agent row.
	m.cursor = 1
	m.skipDivider(-1)
	if m.cursor != 0 {
		t.Fatalf("skip up: cursor = %d", m.cursor)
	}
	// Workflow rows are not agents.
	m.cursor = 2
	if _, ok := m.selectedAgent(); ok {
		t.Fatal("workflow row must not resolve to an agent")
	}
}
