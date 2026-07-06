package panel

import (
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func syncModel(agents []Agent, routines []routineRow) watchModel {
	return watchModel{
		tabKind:   KindAgent,
		tabCursor: map[string]int{},
		agents:    agents,
		routines:  routines,
	}
}

func TestSyncToOccupantFollowsNewOccupant(t *testing.T) {
	m := syncModel([]Agent{
		{PaneID: "%1", Name: "a", Kind: KindAgent},
		{PaneID: "%2", Name: "chart", Kind: KindArtifact, Active: true},
		{PaneID: "%3", Name: "table", Kind: KindArtifact},
	}, nil)
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

func TestSyncToOccupantRoutineRunFoldsUnderSchedTab(t *testing.T) {
	m := syncModel([]Agent{
		{PaneID: "%4", Name: "daily", Kind: KindRun, Active: true},
	}, []routineRow{{Name: "hourly"}, {Name: "daily"}})
	m.syncToOccupant()
	if m.tabKind != schedTab {
		t.Errorf("routine-backed run should land on the ⏰ tab, got %q", m.tabKind)
	}
	if m.cursor != 1 {
		t.Errorf("cursor should be on the matching routine row, got %d", m.cursor)
	}
}

func TestSyncToOccupantIgnoresPlaceholder(t *testing.T) {
	m := syncModel([]Agent{
		{PaneID: "%5", Kind: "", Active: true}, // filler pane (empty kind)
	}, nil)
	m.tabKind, m.cursor = schedTab, 2
	m.syncToOccupant()
	if m.tabKind != schedTab || m.cursor != 2 {
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
