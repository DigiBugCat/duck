package folder

import (
	"testing"

	"github.com/DigiBugCat/duck/internal/config"
)

// TestStoreSetGetRoundTrip pins that Set then Get round-trips a policy through a
// temp config dir (HOME), and that an unknown dir reports ok=false.
func TestStoreSetGetRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := NewStore()

	if _, ok := s.Get("~/dev/unknown"); ok {
		t.Fatalf("an unknown dir must report ok=false")
	}

	if err := s.Set("~/dev/foo", PolicySync); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Set("~/dev/bar", PolicyNever); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if p, ok := s.Get("~/dev/foo"); !ok || p != PolicySync {
		t.Fatalf("Get(~/dev/foo) = %q,%v want sync,true", p, ok)
	}
	if p, ok := s.Get("~/dev/bar"); !ok || p != PolicyNever {
		t.Fatalf("Get(~/dev/bar) = %q,%v want never,true", p, ok)
	}
}

// TestStorePersistsAcrossLoad pins that policies survive a fresh load (written
// to the config TOML, not just held in memory).
func TestStorePersistsAcrossLoad(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := NewStore().Set("~/dev/foo", PolicySync); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// A brand-new store reads the persisted value.
	if p, ok := NewStore().Get("~/dev/foo"); !ok || p != PolicySync {
		t.Fatalf("persisted Get = %q,%v want sync,true", p, ok)
	}
}

// TestStoreSetPreservesOtherConfig pins that Set does NOT clobber unrelated
// config fields (Hub, AutoName): it loads, mutates Folders, and re-saves.
func TestStoreSetPreservesOtherConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := config.Save(&config.Config{
		Hub:      "me@host",
		AutoName: map[string]bool{"~/dev/on": true},
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if err := NewStore().Set("~/dev/foo", PolicyNever); err != nil {
		t.Fatalf("Set: %v", err)
	}
	c, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Hub != "me@host" {
		t.Fatalf("Set clobbered Hub: %q", c.Hub)
	}
	if !c.AutoNameEnabled("~/dev/on") {
		t.Fatalf("Set clobbered AutoName")
	}
	if c.Folders["~/dev/foo"] != PolicyNever {
		t.Fatalf("Folders not persisted: %+v", c.Folders)
	}
}

// TestStoreForget drops a remembered policy.
func TestStoreForget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := NewStore()
	if err := s.Set("~/dev/foo", PolicySync); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Forget("~/dev/foo"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if _, ok := s.Get("~/dev/foo"); ok {
		t.Fatalf("Forget did not drop the policy")
	}
}
