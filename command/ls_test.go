package command

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/DigiBugCat/duck/internal/model"
)

// TestLsStatus pins the four-way status agents branch on: evicted beats
// everything, attached beats recency, and the active/idle split is exclusive
// at lsIdleThreshold (exactly at the threshold is already idle, matching the
// picker's glyph split).
func TestLsStatus(t *testing.T) {
	now := time.Now()
	cases := []struct {
		row  model.Row
		want string
	}{
		{model.Row{Evicted: true, Attached: true}, "evicted"},
		{model.Row{Attached: true, LastSeen: now.Add(-3 * lsIdleThreshold)}, "attached"},
		{model.Row{LastSeen: now.Add(-time.Minute)}, "active"},
		{model.Row{LastSeen: now.Add(-lsIdleThreshold)}, "idle"},
	}
	for _, c := range cases {
		if got := lsStatus(c.row, now); got != c.want {
			t.Errorf("lsStatus(%+v) = %q, want %q", c.row, got, c.want)
		}
	}
}

// TestLsRowJSONContract pins the JSON field names — they are the stable
// contract with agent consumers of `duck ls --json`.
func TestLsRowJSONContract(t *testing.T) {
	b, err := json.Marshal(lsRow{Name: "n", Title: "t", Dir: "~/d", Status: "active",
		Age: "2m", AgeSeconds: 120, LastActive: "2026-01-01T00:00:00Z", TmuxName: "n-2"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"name", "title", "dir", "status", "age", "age_seconds",
		"last_active", "attached", "looped", "windows", "evicted", "tmux"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing JSON key %q in %s", k, b)
		}
	}
}
