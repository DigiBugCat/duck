package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestCodexModelRoundTrips pins that the M3 codex model field decodes from the
// config TOML.
func TestCodexModelRoundTrips(t *testing.T) {
	c := &Config{}
	if _, err := toml.Decode("hub = \"me@host\"\ncodex_model = \"gpt-5-nano\"\n", c); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if c.CodexModel != "gpt-5-nano" {
		t.Fatalf("CodexModel = %q, want gpt-5-nano", c.CodexModel)
	}
}

// TestAutoNameEnabledReflectsPerDirToggle pins the per-dir naming toggle: a
// dir in the map with true is ON, a dir set false or absent is OFF, and a nil
// map / nil receiver is OFF (the opt-in default).
func TestAutoNameEnabledReflectsPerDirToggle(t *testing.T) {
	c := &Config{AutoName: map[string]bool{
		"~/dev/on":  true,
		"~/dev/off": false,
	}}
	if !c.AutoNameEnabled("~/dev/on") {
		t.Fatalf("~/dev/on should be enabled")
	}
	if c.AutoNameEnabled("~/dev/off") {
		t.Fatalf("~/dev/off should be disabled")
	}
	if c.AutoNameEnabled("~/dev/absent") {
		t.Fatalf("an absent dir should default to disabled")
	}

	// A nil map and a nil receiver are both OFF, so callers need no guard.
	if (&Config{}).AutoNameEnabled("~/dev/anything") {
		t.Fatalf("a nil AutoName map should default to disabled")
	}
	var nilCfg *Config
	if nilCfg.AutoNameEnabled("~/dev/anything") {
		t.Fatalf("a nil config should default to disabled")
	}
}

// TestAutoNameRoundTripsThroughSaveLoad pins that the per-dir toggle persists
// through Save/Load (writing into a temp HOME so no real config is touched).
func TestAutoNameRoundTripsThroughSaveLoad(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	in := &Config{
		Hub:        "me@host",
		CodexModel: "gpt-5-mini",
		AutoName:   map[string]bool{"~/dev/foo": true},
	}
	if err := Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Sanity: it wrote under the temp HOME.
	if _, err := os.Stat(filepath.Join(tmp, ".config", "duck", "config.toml")); err != nil {
		t.Fatalf("config not written under HOME: %v", err)
	}

	out, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.CodexModel != "gpt-5-mini" {
		t.Fatalf("CodexModel did not round-trip: %q", out.CodexModel)
	}
	if !out.AutoNameEnabled("~/dev/foo") {
		t.Fatalf("AutoName toggle did not round-trip: %+v", out.AutoName)
	}
}

// TestFoldersRoundTrip pins that the per-folder sync policy map persists through
// Save/Load (the bare-`duck` sync-awareness store).
func TestFoldersRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	in := &Config{
		Hub:     "me@host",
		Folders: map[string]string{"~/dev/foo": "sync", "~": "never"},
	}
	if err := Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.Folders["~/dev/foo"] != "sync" {
		t.Fatalf("Folders[~/dev/foo] = %q, want sync", out.Folders["~/dev/foo"])
	}
	if out.Folders["~"] != "never" {
		t.Fatalf("Folders[~] = %q, want never", out.Folders["~"])
	}
}
