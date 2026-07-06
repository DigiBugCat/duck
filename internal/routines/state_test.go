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
		Key("~/proj", "a", "daily"): now,
	}}
	if err := SaveState(s); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	key := Key("~/proj", "a", "daily")
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
		Key("~/proj", "a", "r1"): t1,
	}}); err != nil {
		t.Fatalf("SaveState 1: %v", err)
	}
	t2 := t1.Add(time.Hour)
	if err := SaveState(State{LastFire: map[string]time.Time{
		Key("~/proj", "b", "r2"): t2,
	}}); err != nil {
		t.Fatalf("SaveState 2: %v", err)
	}
	got, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if _, ok := got.LastFire[Key("~/proj", "a", "r1")]; ok {
		t.Fatal("stale entry from first save survived overwrite")
	}
	if gt, ok := got.LastFire[Key("~/proj", "b", "r2")]; !ok || !gt.Equal(t2) {
		t.Fatalf("LastFire[/proj/b|r2] = %v, ok=%v, want %v", gt, ok, t2)
	}
}

func TestKeyCollisionFree(t *testing.T) {
	// The tab separators keep (root, ws, name) parts from concatenating
	// ambiguously — moving a boundary between ws and name (or root and ws)
	// must produce a distinct key. All under one fixed root here; root's own
	// disambiguation is the same tab mechanism.
	const root = "~/proj"
	cases := []struct {
		wsA, nameA string
		wsB, nameB string
	}{
		{"a", "b/c", "a/b", "c"}, // ws/name boundary shift
		{"a", "b", "a/b", ""},    // name folds into ws
	}
	for _, c := range cases {
		ka := Key(root, c.wsA, c.nameA)
		kb := Key(root, c.wsB, c.nameB)
		if ka == kb {
			t.Fatalf("Key collision: Key(%q,%q,%q) == Key(%q,%q,%q) == %q", root, c.wsA, c.nameA, root, c.wsB, c.nameB, ka)
		}
	}

	// Trailing slash on the ws (a Cleaned part) collapses.
	if k1, k2 := Key(root, "work/", "r"), Key(root, "work", "r"); k1 != k2 {
		t.Fatalf("Key ws %q = %q, %q = %q; want equal (filepath.Clean)", "work/", k1, "work", k2)
	}
	// Trailing slash on the root collapses too.
	if k1, k2 := Key("~/proj/", "work", "r"), Key("~/proj", "work", "r"); k1 != k2 {
		t.Fatalf("Key root %q = %q, %q = %q; want equal (filepath.Clean)", "~/proj/", k1, "~/proj", k2)
	}
}
