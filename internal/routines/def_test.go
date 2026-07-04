package routines

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeRoutine writes <dir>/.duck/routines/<name>.toml (and, unless md=="",
// a sibling <name>.md) for test setup.
func writeRoutine(t *testing.T, projectDir, name, tomlBody, md string) {
	t.Helper()
	dir := filepath.Join(projectDir, ".duck", "routines")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(tomlBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if md != "" {
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(md), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoad_MissingDir(t *testing.T) {
	dir := t.TempDir()
	defs, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if defs != nil {
		t.Fatalf("expected nil defs, got %v", defs)
	}
}

func TestLoad_HappyPath(t *testing.T) {
	dir := t.TempDir()
	writeRoutine(t, dir, "b-heartbeat", `
trigger = "heartbeat"
interval = "15m"
target = "run"
report = "none"
`, "beat the drum")
	writeRoutine(t, dir, "a-cron", `
trigger = "cron"
schedule = "0 9 * * *"
`, "good morning")
	writeRoutine(t, dir, "c-manual", `
trigger = "manual"
target = "manager"
`, "manual job")

	defs, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(defs) != 3 {
		t.Fatalf("expected 3 defs, got %d: %+v", len(defs), defs)
	}

	// sorted by name
	wantNames := []string{"a-cron", "b-heartbeat", "c-manual"}
	for i, want := range wantNames {
		if defs[i].Name != want {
			t.Fatalf("defs[%d].Name = %q, want %q", i, defs[i].Name, want)
		}
	}

	a := defs[0]
	if a.Trigger != TriggerCron || a.Schedule != "0 9 * * *" || a.Target != TargetRun || a.Report != "digest" {
		t.Fatalf("unexpected a-cron def: %+v", a)
	}
	if a.Dir != dir {
		t.Fatalf("Dir = %q, want %q", a.Dir, dir)
	}
	if a.Prompt != "good morning" {
		t.Fatalf("Prompt = %q", a.Prompt)
	}

	b := defs[1]
	if b.Trigger != TriggerHeartbeat || b.Interval != 15*time.Minute || b.Target != TargetRun || b.Report != "none" {
		t.Fatalf("unexpected b-heartbeat def: %+v", b)
	}

	c := defs[2]
	if c.Trigger != TriggerManual || c.Target != TargetManager || c.Report != "digest" {
		t.Fatalf("unexpected c-manual def: %+v", c)
	}
}

func TestLoad_PromptTrimmed(t *testing.T) {
	dir := t.TempDir()
	writeRoutine(t, dir, "r", `trigger = "manual"`, "\n\n  hello world  \n\n")
	defs, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if defs[0].Prompt != "hello world" {
		t.Fatalf("Prompt = %q", defs[0].Prompt)
	}
}

func TestLoad_MissingMd(t *testing.T) {
	dir := t.TempDir()
	writeRoutine(t, dir, "r", `trigger = "manual"`, "")
	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error for missing .md")
	}
	if !strings.Contains(err.Error(), "r.toml") {
		t.Fatalf("error should name the file: %v", err)
	}
}

func TestLoad_ValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		toml string
	}{
		{"missing trigger", ``},
		{"unknown trigger", `trigger = "nope"`},
		{"cron missing schedule", `trigger = "cron"`},
		{"cron bad schedule", `trigger = "cron"
schedule = "not a cron expr"`},
		{"heartbeat missing interval", `trigger = "heartbeat"`},
		{"heartbeat bad interval", `trigger = "heartbeat"
interval = "not a duration"`},
		{"heartbeat zero interval", `trigger = "heartbeat"
interval = "0s"`},
		{"heartbeat negative interval", `trigger = "heartbeat"
interval = "-5m"`},
		{"unknown target", `trigger = "manual"
target = "bogus"`},
		{"unknown report", `trigger = "manual"
report = "bogus"`},
		{"unknown key", `trigger = "manual"
frobnicate = true`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeRoutine(t, dir, "r", tc.toml, "prompt")
			_, err := Load(dir)
			if err == nil {
				t.Fatalf("expected error for case %q", tc.name)
			}
			if !strings.Contains(err.Error(), "r.toml") {
				t.Fatalf("error should name the file, got: %v", err)
			}
		})
	}
}

func TestLoad_MalformedToml(t *testing.T) {
	dir := t.TempDir()
	writeRoutine(t, dir, "r", `trigger = "manual`, "prompt") // unterminated string
	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "r.toml") {
		t.Fatalf("error should name the file: %v", err)
	}
}

func TestLoad_IgnoresNonTomlFiles(t *testing.T) {
	dir := t.TempDir()
	writeRoutine(t, dir, "r", `trigger = "manual"`, "prompt")
	routinesDir := filepath.Join(dir, ".duck", "routines")
	if err := os.WriteFile(filepath.Join(routinesDir, "README.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(routinesDir, "subdir.toml"), 0o755); err != nil {
		t.Fatal(err)
	}

	defs, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs[0].Name != "r" {
		t.Fatalf("expected only 'r', got %+v", defs)
	}
}

func TestDue_Manual(t *testing.T) {
	d := Def{Trigger: TriggerManual}
	now := time.Now()
	if d.Due(time.Time{}, now) {
		t.Fatal("manual should never be due (zero last)")
	}
	if d.Due(now.Add(-time.Hour), now) {
		t.Fatal("manual should never be due")
	}
}

func TestDue_Heartbeat(t *testing.T) {
	d := Def{Trigger: TriggerHeartbeat, Interval: 10 * time.Minute}
	now := time.Now()

	if !d.Due(time.Time{}, now) {
		t.Fatal("heartbeat should be due when never fired")
	}
	if d.Due(now.Add(-5*time.Minute), now) {
		t.Fatal("heartbeat should not be due before interval elapses")
	}
	if !d.Due(now.Add(-10*time.Minute), now) {
		t.Fatal("heartbeat should be due exactly at interval")
	}
	if !d.Due(now.Add(-time.Hour), now) {
		t.Fatal("heartbeat should be due well past interval")
	}
}

func TestDue_Cron(t *testing.T) {
	dir := t.TempDir()
	// every day at 09:00
	writeRoutine(t, dir, "r", `trigger = "cron"
schedule = "0 9 * * *"`, "prompt")
	defs, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	d := defs[0]

	// Zero last: freshly registered routine waits for its NEXT slot, does not
	// fire immediately even if "now" looks like it's past some slot.
	now := time.Date(2026, 7, 4, 9, 0, 0, 0, time.Local) // exactly a fire instant
	if d.Due(time.Time{}, now) {
		t.Fatal("cron with zero last should not fire immediately (waits for next slot after now)")
	}

	// A zero last is never due, whatever "now" is — the tick seeds
	// LastFire=now on first sight, and the routine fires at its next slot
	// after that seed.
	if d.Due(time.Time{}, now) {
		t.Fatal("cron with zero last should never be due (tick seeds last-fire on first sight)")
	}
	if d.Due(time.Time{}, now.Add(24*time.Hour)) {
		t.Fatal("cron with zero last should never be due even a day later (seeding is the tick's job)")
	}

	// last = a previous fire; not yet due before the next 09:00.
	last := time.Date(2026, 7, 4, 9, 0, 0, 0, time.Local)
	beforeNext := time.Date(2026, 7, 5, 8, 0, 0, 0, time.Local)
	if d.Due(last, beforeNext) {
		t.Fatal("should not be due before next scheduled time")
	}

	// Exactly at / after the next scheduled time: due.
	atNext := time.Date(2026, 7, 5, 9, 0, 0, 0, time.Local)
	if !d.Due(last, atNext) {
		t.Fatal("should be due at the next scheduled time")
	}

	// Missed beats collapse to one fire: last is far in the past, many
	// scheduled instants have been skipped, but Due only asks "is the NEXT
	// slot after last <= now" — true regardless of how many were missed, and
	// firing once resets last to now via the caller so it doesn't replay.
	longAgo := time.Date(2026, 1, 1, 9, 0, 0, 0, time.Local)
	if !d.Due(longAgo, atNext) {
		t.Fatal("should be due after many missed beats")
	}
}
