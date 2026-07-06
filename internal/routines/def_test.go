package routines

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeRoutine writes <root>/.duck/routines/<ws>/<name>.toml (and, unless
// md=="", a sibling <name>.md) for test setup. root is a project sync-root
// (an absolute t.TempDir() works — paths.Expand returns it unchanged). Hub-
// local state/index still land under DUCK_HOME, so callers isolating those
// point DUCK_HOME at a scratch dir (t.Setenv) first.
func writeRoutine(t *testing.T, root, ws, name, tomlBody, md string) {
	t.Helper()
	dir, err := WorkspaceDir(root, ws)
	if err != nil {
		t.Fatal(err)
	}
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

func TestLoadWorkspace_MissingDir(t *testing.T) {
	defs, err := LoadWorkspace(t.TempDir(), "nowhere")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if defs != nil {
		t.Fatalf("expected nil defs, got %v", defs)
	}
}

func TestLoadWorkspace_HappyPath(t *testing.T) {
	root := t.TempDir()
	writeRoutine(t, root, "work", "b-heartbeat", `
trigger = "heartbeat"
interval = "15m"
target = "run"
report = "none"
`, "beat the drum")
	writeRoutine(t, root, "work", "a-cron", `
trigger = "cron"
schedule = "0 9 * * *"
`, "good morning")
	writeRoutine(t, root, "work", "c-manual", `
trigger = "manual"
target = "manager"
`, "manual job")
	// Another workspace's routine must not leak into work's list.
	writeRoutine(t, root, "elsewhere", "z-other", `trigger = "manual"`, "other")

	defs, err := LoadWorkspace(root, "work")
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
	if a.Workspace != "work" {
		t.Fatalf("Workspace = %q, want work", a.Workspace)
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

func TestAllWorkspaces(t *testing.T) {
	t.Setenv("DUCK_HOME", t.TempDir()) // isolate the hub-local index
	// Empty index => (nil, nil): AllWorkspaces is index-driven, not a scan.
	if refs, err := AllWorkspaces(); err != nil || refs != nil {
		t.Fatalf("empty index should be (nil, nil), got %v %v", refs, err)
	}
	root := t.TempDir()
	writeRoutine(t, root, "beta", "r", `trigger = "manual"`, "p")
	writeRoutine(t, root, "alpha", "r", `trigger = "manual"`, "p")
	// Defs exist on disk but the root is not indexed yet — still invisible.
	if refs, err := AllWorkspaces(); err != nil || refs != nil {
		t.Fatalf("unindexed root must stay invisible, got %v %v", refs, err)
	}
	if err := IndexAdd(root); err != nil {
		t.Fatal(err)
	}
	refs, err := AllWorkspaces()
	if err != nil {
		t.Fatal(err)
	}
	// One WSRef per workspace subdir under the indexed root, sorted.
	if len(refs) != 2 ||
		refs[0].Root != root || refs[0].Workspace != "alpha" ||
		refs[1].Root != root || refs[1].Workspace != "beta" {
		t.Fatalf("got %+v want alpha,beta under %q", refs, root)
	}
}

func TestLoadWorkspace_PromptTrimmed(t *testing.T) {
	root := t.TempDir()
	writeRoutine(t, root, "w", "r", `trigger = "manual"`, "\n\n  hello world  \n\n")
	defs, err := LoadWorkspace(root, "w")
	if err != nil {
		t.Fatal(err)
	}
	if defs[0].Prompt != "hello world" {
		t.Fatalf("Prompt = %q", defs[0].Prompt)
	}
}

func TestLoadWorkspace_MissingMd(t *testing.T) {
	root := t.TempDir()
	writeRoutine(t, root, "w", "r", `trigger = "manual"`, "")
	_, err := LoadWorkspace(root, "w")
	if err == nil {
		t.Fatal("expected error for missing .md")
	}
	if !strings.Contains(err.Error(), "r.toml") {
		t.Fatalf("error should name the file: %v", err)
	}
}

func TestLoadWorkspace_ValidationErrors(t *testing.T) {
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
			root := t.TempDir()
			writeRoutine(t, root, "w", "r", tc.toml, "prompt")
			_, err := LoadWorkspace(root, "w")
			if err == nil {
				t.Fatalf("expected error for case %q", tc.name)
			}
			if !strings.Contains(err.Error(), "r.toml") {
				t.Fatalf("error should name the file, got: %v", err)
			}
		})
	}
}

func TestLoadWorkspace_MalformedToml(t *testing.T) {
	root := t.TempDir()
	writeRoutine(t, root, "w", "r", `trigger = "manual`, "prompt") // unterminated string
	_, err := LoadWorkspace(root, "w")
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "r.toml") {
		t.Fatalf("error should name the file: %v", err)
	}
}

func TestLoadWorkspace_IgnoresNonTomlFiles(t *testing.T) {
	root := t.TempDir()
	writeRoutine(t, root, "w", "r", `trigger = "manual"`, "prompt")
	dir, _ := WorkspaceDir(root, "w")
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "subdir.toml"), 0o755); err != nil {
		t.Fatal(err)
	}

	defs, err := LoadWorkspace(root, "w")
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
	root := t.TempDir()
	// every day at 09:00
	writeRoutine(t, root, "w", "r", `trigger = "cron"
schedule = "0 9 * * *"`, "prompt")
	defs, err := LoadWorkspace(root, "w")
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
