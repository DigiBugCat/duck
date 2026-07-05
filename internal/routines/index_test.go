package routines

import "testing"

// TestIndex: the hub-local routines-projects index round-trips, add is
// idempotent, remove drops a root, and a missing file reads as empty.
func TestIndex(t *testing.T) {
	t.Setenv("DUCK_HOME", t.TempDir())
	if r, _ := LoadIndex(); len(r) != 0 {
		t.Fatalf("empty start, got %v", r)
	}
	_ = IndexAdd("~/a")
	_ = IndexAdd("~/b")
	_ = IndexAdd("~/a") // idempotent
	r, _ := LoadIndex()
	if len(r) != 2 || r[0] != "~/a" || r[1] != "~/b" {
		t.Fatalf("want [~/a ~/b], got %v", r)
	}
	_ = IndexRemove("~/a")
	r, _ = LoadIndex()
	if len(r) != 1 || r[0] != "~/b" {
		t.Fatalf("after remove want [~/b], got %v", r)
	}
}
