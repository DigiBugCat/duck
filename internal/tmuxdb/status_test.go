package tmuxdb

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// statusOut builds list-panes output in statusFmt order:
// pane_id, window_activity, @duck_state, @duck_spawned_at, cmd, anchor, name.
func statusOut(rows ...string) string { return strings.Join(rows, "\n") + "\n" }

func TestRenderStatusLine(t *testing.T) {
	now := time.Unix(10_000, 0)
	out := statusOut(
		"%1\t9000\tbusy\t9700\tcodex\t\talpha",   // 5m old, busy
		"%2\t9500\t\t9940\tzsh\t\tbeta",          // 60s old, idle (unset state)
		"%0\t9999\t\t\t\t\t",                     // unstamped manager: skipped
		"%3\t9400\tidle\t9990\tnode\t1\tanchor",  // legacy anchor: skipped
	)
	panes := parseStatusPanes(out)
	if len(panes) != 2 {
		t.Fatalf("parsed %d panes, want 2", len(panes))
	}
	// Sorted by window_activity desc: beta (9500) before alpha (9000).
	if got := renderStatusLine(panes, 0, now); got != "● beta 1m" {
		t.Errorf("line 0 = %q, want %q", got, "● beta 1m")
	}
	if got := renderStatusLine(panes, 1, now); got != "◐ alpha codex 5m" {
		t.Errorf("line 1 = %q, want %q", got, "◐ alpha codex 5m")
	}
	// Out-of-range lines are empty.
	if got := renderStatusLine(panes, 2, now); got != "" {
		t.Errorf("line 2 = %q, want empty", got)
	}
	if got := renderStatusLine(panes, -1, now); got != "" {
		t.Errorf("line -1 = %q, want empty", got)
	}
}

func TestRenderStatusLineAggregate(t *testing.T) {
	now := time.Unix(10_000, 0)
	var rows []string
	for i := 0; i < 6; i++ {
		state := "busy"
		if i%2 == 1 {
			state = ""
		}
		rows = append(rows, fmt.Sprintf("%%%d\t%d\t%s\t9990\tcodex\t\tagent-%d", i, 9000-i, state, i))
	}
	panes := parseStatusPanes(statusOut(rows...))
	// Lines 0..2 are the 3 most recently active agents.
	if got := renderStatusLine(panes, 0, now); !strings.Contains(got, "agent-0") {
		t.Errorf("line 0 = %q, want agent-0", got)
	}
	// Line 3 aggregates: 3 busy, 3 idle, 3 not shown individually.
	want := "◐3 ●3 +3 more — fleet"
	if got := renderStatusLine(panes, 3, now); got != want {
		t.Errorf("aggregate = %q, want %q", got, want)
	}
	if got := renderStatusLine(panes, 4, now); got != "" {
		t.Errorf("line 4 = %q, want empty", got)
	}
}

func TestStatusHeight(t *testing.T) {
	for _, tc := range []struct{ agents, want int }{
		{0, 1}, {1, 2}, {4, 5}, {9, 5},
	} {
		if got := statusHeight(tc.agents); got != tc.want {
			t.Errorf("statusHeight(%d) = %d, want %d", tc.agents, got, tc.want)
		}
	}
}

// fakeStatusRunner records set-option calls and serves canned reads.
func fakeStatusRunner(panesOut string, calls *[]string) Runner {
	return func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		*calls = append(*calls, joined)
		switch args[0] {
		case "list-panes":
			return panesOut, nil
		case "show-options":
			return "DEFAULT-WINDOW-LIST\n", nil
		}
		return "", nil
	}
}

func TestSyncStatusHeight(t *testing.T) {
	var calls []string
	run := fakeStatusRunner(statusOut(
		"%1\t9000\tbusy\t9700\tcodex\t\talpha",
		"%2\t9500\t\t9940\tzsh\t\tbeta",
	), &calls)
	SyncStatusHeight(run, "work")

	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		"set-option -t work status-interval 2",
		"set-option -t work status 3",
		"statusline 'work' 0",
		"statusline 'work' 1",
		"set-option -t work status-format[2] DEFAULT-WINDOW-LIST",
		"set-option -u -t work status-format[3]",
		"set-option -u -t work status-format[4]",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("SyncStatusHeight calls missing %q in:\n%s", want, joined)
		}
	}
	// The agent lines must NOT clobber the window-list line's index.
	if strings.Contains(joined, "status-format[2] #[align=left]") {
		t.Errorf("agent format overwrote the window-list line:\n%s", joined)
	}
}

func TestSyncStatusHeightNoAgents(t *testing.T) {
	var calls []string
	run := fakeStatusRunner("%0\t9000\t\t\tzsh\t\t\n", &calls)
	SyncStatusHeight(run, "work")
	joined := strings.Join(calls, "\n")
	// tmux rejects `status 1` — a single line is spelled "on".
	if !strings.Contains(joined, "set-option -t work status on") {
		t.Errorf("want status on with no agents, got:\n%s", joined)
	}
	if !strings.Contains(joined, "set-option -t work status-format[0] DEFAULT-WINDOW-LIST") {
		t.Errorf("want window list restored at index 0, got:\n%s", joined)
	}
}
