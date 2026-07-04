package routines

import (
	"testing"
	"time"
)

func TestLoadStateMissing(t *testing.T) {
	t.Setenv("DUCK_HOME", t.TempDir())
	s, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if s.LastFire == nil {
		t.Fatal("LastFire map is nil, want non-nil empty map")
	}
	if len(s.LastFire) != 0 {
		t.Fatalf("LastFire = %v, want empty", s.LastFire)
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	t.Setenv("DUCK_HOME", t.TempDir())
	now := time.Now().UTC().Truncate(time.Second)
	s := State{LastFire: map[string]time.Time{
		Key("/proj/a", "daily"): now,
	}}
	if err := SaveState(s); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	key := Key("/proj/a", "daily")
	gt, ok := got.LastFire[key]
	if !ok {
		t.Fatalf("LastFire missing key %q, got %v", key, got.LastFire)
	}
	if !gt.Equal(now) {
		t.Fatalf("LastFire[%q] = %v, want %v", key, gt, now)
	}
}

func TestSaveStateAtomicOverwrite(t *testing.T) {
	t.Setenv("DUCK_HOME", t.TempDir())
	t1 := time.Now().UTC().Truncate(time.Second)
	if err := SaveState(State{LastFire: map[string]time.Time{
		Key("/proj/a", "r1"): t1,
	}}); err != nil {
		t.Fatalf("SaveState 1: %v", err)
	}
	t2 := t1.Add(time.Hour)
	if err := SaveState(State{LastFire: map[string]time.Time{
		Key("/proj/b", "r2"): t2,
	}}); err != nil {
		t.Fatalf("SaveState 2: %v", err)
	}
	got, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if _, ok := got.LastFire[Key("/proj/a", "r1")]; ok {
		t.Fatal("stale entry from first save survived overwrite")
	}
	if gt, ok := got.LastFire[Key("/proj/b", "r2")]; !ok || !gt.Equal(t2) {
		t.Fatalf("LastFire[/proj/b|r2] = %v, ok=%v, want %v", gt, ok, t2)
	}
}

func TestProjectsMissing(t *testing.T) {
	t.Setenv("DUCK_HOME", t.TempDir())
	p, err := Projects()
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if p != nil {
		t.Fatalf("Projects = %v, want nil", p)
	}
}

func TestEnableIdempotencyAndSorting(t *testing.T) {
	t.Setenv("DUCK_HOME", t.TempDir())
	if err := Enable("/proj/zeta"); err != nil {
		t.Fatalf("Enable zeta: %v", err)
	}
	if err := Enable("/proj/alpha"); err != nil {
		t.Fatalf("Enable alpha: %v", err)
	}
	if err := Enable("/proj/alpha/"); err != nil { // cleaned form is a duplicate
		t.Fatalf("Enable alpha/: %v", err)
	}
	if err := Enable("/proj/alpha"); err != nil { // exact duplicate
		t.Fatalf("Enable alpha again: %v", err)
	}
	got, err := Projects()
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	want := []string{"/proj/alpha", "/proj/zeta"}
	if len(got) != len(want) {
		t.Fatalf("Projects = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Projects = %v, want %v", got, want)
		}
	}
}

func TestDisableAbsent(t *testing.T) {
	t.Setenv("DUCK_HOME", t.TempDir())
	if err := Enable("/proj/a"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if err := Disable("/proj/nonexistent"); err != nil {
		t.Fatalf("Disable absent: %v", err)
	}
	got, err := Projects()
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(got) != 1 || got[0] != "/proj/a" {
		t.Fatalf("Projects = %v, want [/proj/a]", got)
	}
	if err := Disable("/proj/a"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	got, err = Projects()
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Projects = %v, want empty after disable", got)
	}
}

func TestKeyCollisionFree(t *testing.T) {
	// Tricky dirs: trailing slash cleaned, dirs that look like they could
	// concatenate ambiguously without the tab separator.
	cases := []struct {
		dirA, nameA string
		dirB, nameB string
	}{
		{"/proj/a", "b/c", "/proj/a/b", "c"},
		{"/proj", "a", "/proj/a", ""},
	}
	for _, c := range cases {
		ka := Key(c.dirA, c.nameA)
		kb := Key(c.dirB, c.nameB)
		if ka == kb {
			t.Fatalf("Key collision: Key(%q,%q) == Key(%q,%q) == %q", c.dirA, c.nameA, c.dirB, c.nameB, ka)
		}
	}

	if k1, k2 := Key("/proj/a/", "r"), Key("/proj/a", "r"); k1 != k2 {
		t.Fatalf("Key(%q) = %q, Key(%q) = %q; want equal (filepath.Clean)", "/proj/a/", k1, "/proj/a", k2)
	}
}
