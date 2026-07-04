package workspaces

import "testing"

func TestSpacedDirRoundtrip(t *testing.T) {
	s := NewStore(LocalRunner{})
	s.SetBase(t.TempDir())
	if err := s.Save(Record{Name: "w1", Dir: "~/My Vault/sub dir"}); err != nil {
		t.Fatal(err)
	}
	r, ok, err := s.Load("~/My Vault/sub dir", "w1")
	if err != nil || !ok || r.Dir != "~/My Vault/sub dir" {
		t.Fatalf("roundtrip failed: %v %v %+v", err, ok, r)
	}
	all, err := s.All()
	if err != nil || len(all) != 1 {
		t.Fatalf("All: %v %v", err, all)
	}
}
