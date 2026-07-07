package panel

import (
	"reflect"
	"testing"
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
		{PaneID: "%2", Name: "chart", Kind: KindShell, Active: true},
		{PaneID: "%3", Name: "table", Kind: KindShell},
	})
	m.syncToOccupant()
	if m.tabKind != KindShell {
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
	m.tabKind, m.cursor = KindShell, 2
	m.syncToOccupant()
	if m.tabKind != KindShell || m.cursor != 2 {
		t.Errorf("placeholder occupant must leave tab/cursor alone, got %q/%d", m.tabKind, m.cursor)
	}
	if m.lastActive != "%5" {
		t.Errorf("lastActive should still advance, got %q", m.lastActive)
	}
}

func TestWorkflowsSectionInAgentsTab(t *testing.T) {
	m := watchModel{
		tabKind:   KindAgent,
		tabCursor: map[string]int{},
		agents: []Agent{
			{PaneID: "%1", Name: "claude", Kind: KindAgent},
			{PaneID: "%2", Name: "chart", Kind: KindShell},
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
	m.tabKind = KindShell
	if got := m.visible(); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("shell tab visible = %v", got)
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
