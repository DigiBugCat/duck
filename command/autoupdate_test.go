package command

import (
	"testing"
	"time"
)

// TestBackgroundUpdateEnabled pins the three hard gates that disable auto-update
// regardless of the throttle: a dev build, the env escape hatch, and the config
// opt-out. Only a released build with neither opt-out set enables it.
func TestBackgroundUpdateEnabled(t *testing.T) {
	cases := []struct {
		ver, env string
		cfgOn    bool
		want     bool
	}{
		{"dev", "", true, false},     // from-source build never auto-updates
		{"0.2.21", "1", true, false}, // DUCK_NO_AUTO_UPDATE set
		{"0.2.21", "", false, false}, // config opt-out
		{"0.2.21", "", true, true},   // released + enabled → on
	}
	for _, c := range cases {
		if got := backgroundUpdateEnabled(c.ver, c.env, c.cfgOn); got != c.want {
			t.Errorf("backgroundUpdateEnabled(%q,%q,%v) = %v, want %v", c.ver, c.env, c.cfgOn, got, c.want)
		}
	}
}

// TestDueForBackgroundUpdate pins the throttle: a missing stamp is always due
// (first run), a fresh stamp is not, and a stamp older than the interval is due
// again.
func TestDueForBackgroundUpdate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()

	// No stamp yet → due.
	if !dueForBackgroundUpdate(now) {
		t.Fatalf("missing stamp must be due")
	}

	// Just checked → not due.
	touchUpdateStamp(now)
	if dueForBackgroundUpdate(now) {
		t.Fatalf("fresh stamp must NOT be due")
	}

	// Checked longer ago than the interval → due again.
	old := now.Add(-2 * backgroundUpdateInterval)
	touchUpdateStamp(old)
	if !dueForBackgroundUpdate(now) {
		t.Fatalf("stamp older than the interval must be due")
	}
}
