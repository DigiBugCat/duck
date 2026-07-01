package config

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/DigiBugCat/duck/internal/hub"
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

// TestAutoUpdateEnabledDefaultsOn pins that the background self-updater is opt-
// out: a nil pointer (absent key), a nil receiver, and an explicit true all read
// as ON; only an explicit false turns it off.
func TestAutoUpdateEnabledDefaultsOn(t *testing.T) {
	if !(&Config{}).AutoUpdateEnabled() {
		t.Errorf("absent auto_update must default ON")
	}
	var nilCfg *Config
	if !nilCfg.AutoUpdateEnabled() {
		t.Errorf("nil *Config AutoUpdateEnabled() must be ON")
	}
	tru, fls := true, false
	if !(&Config{AutoUpdate: &tru}).AutoUpdateEnabled() {
		t.Errorf("explicit true must be ON")
	}
	if (&Config{AutoUpdate: &fls}).AutoUpdateEnabled() {
		t.Errorf("explicit false must be OFF")
	}
}

// TestAttachTransportRoundTrips pins that the interactive-attach transport field
// decodes from the config TOML and is reflected by Transport().
func TestAttachTransportRoundTrips(t *testing.T) {
	c := &Config{}
	if _, err := toml.Decode("hub = \"me@host\"\nattach_transport = \"tssh\"\n", c); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if c.AttachTransport != "tssh" {
		t.Fatalf("AttachTransport = %q, want tssh", c.AttachTransport)
	}
	if c.Transport() != "tssh" {
		t.Fatalf("Transport() = %q, want tssh", c.Transport())
	}
}

// TestTransportDefaultsToAuto pins the auto default: empty/unset and a nil
// receiver resolve to "auto" (tssh-if-present, else ssh — decided in the
// wiring), while explicit values pass through unchanged, all through the one
// accessor so callers need no guard and the default lives in exactly one place.
func TestTransportDefaultsToAuto(t *testing.T) {
	if got := (&Config{}).Transport(); got != "auto" {
		t.Fatalf("(&Config{}).Transport() = %q, want auto", got)
	}
	var nilCfg *Config
	if got := nilCfg.Transport(); got != "auto" {
		t.Fatalf("nil *Config Transport() = %q, want auto", got)
	}
	if got := (&Config{AttachTransport: "tssh"}).Transport(); got != "tssh" {
		t.Fatalf("explicit tssh Transport() = %q, want tssh", got)
	}
	if got := (&Config{AttachTransport: "ssh"}).Transport(); got != "ssh" {
		t.Fatalf("explicit ssh Transport() = %q, want ssh", got)
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

// TestResolveAnchorNoOpWhenUnset pins that ResolveAnchor does nothing (no SSH
// call, no mutation) when AnchorHost is empty — the opt-in default.
func TestResolveAnchorNoOpWhenUnset(t *testing.T) {
	called := false
	restore := hub.SetRunner(func(argv []string, _ io.Reader) (string, error) {
		called = true
		return "{}", nil
	})
	defer restore()

	c := &Config{Hub: "me@old"}
	got := ResolveAnchor(c)
	if called {
		t.Fatalf("ResolveAnchor must not touch SSH when AnchorHost is empty")
	}
	if got.Hub != "me@old" {
		t.Fatalf("Hub = %q, want unchanged", got.Hub)
	}
}

// TestResolveAnchorPullsNewerHubAndCachesLocally pins the core anchor-read
// path: a hub that differs from the anchor's is overwritten AND persisted to
// the local config file (writing into a temp HOME so no real config is
// touched), so an offline laptop still has the last-known-good value.
func TestResolveAnchorPullsNewerHubAndCachesLocally(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	restore := hub.SetRunner(func(argv []string, _ io.Reader) (string, error) {
		remote := argv[len(argv)-1]
		if strings.Contains(remote, "cat ~/.duck/anchor.json") {
			return `{"hub":"me@new","hubName":"new-box","config":{"codex_model":"gpt-5"}}`, nil
		}
		return "", nil
	})
	defer restore()

	c := &Config{Hub: "me@old", AnchorHost: "me@anchorbox"}
	got := ResolveAnchor(c)
	if got.Hub != "me@new" || got.HubName != "new-box" {
		t.Fatalf("Hub/HubName = %q/%q, want me@new/new-box", got.Hub, got.HubName)
	}
	if got.CodexModel != "gpt-5" {
		t.Fatalf("CodexModel = %q, want gpt-5", got.CodexModel)
	}

	// The pulled value must have been cached to the local config file.
	reloaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.Hub != "me@new" {
		t.Fatalf("reloaded Hub = %q, want the pulled value to have been cached locally", reloaded.Hub)
	}
}

// TestResolveAnchorSwallowsSSHFailure pins that an unreachable anchor host
// leaves c unchanged rather than propagating an error — the anchor is
// advisory, never a hard dependency.
func TestResolveAnchorSwallowsSSHFailure(t *testing.T) {
	restore := hub.SetRunner(func(argv []string, _ io.Reader) (string, error) {
		return "", io.ErrUnexpectedEOF
	})
	defer restore()

	c := &Config{Hub: "me@old", AnchorHost: "me@anchorbox"}
	got := ResolveAnchor(c)
	if got.Hub != "me@old" {
		t.Fatalf("Hub = %q, want unchanged on SSH failure", got.Hub)
	}
}

// TestPushAnchorNoOpWhenUnset mirrors TestResolveAnchorNoOpWhenUnset for the
// write side.
func TestPushAnchorNoOpWhenUnset(t *testing.T) {
	called := false
	restore := hub.SetRunner(func(argv []string, _ io.Reader) (string, error) {
		called = true
		return "", nil
	})
	defer restore()

	if err := PushAnchor(&Config{Hub: "me@host"}); err != nil {
		t.Fatalf("PushAnchor: %v", err)
	}
	if called {
		t.Fatalf("PushAnchor must not touch SSH when AnchorHost is empty")
	}
}

// TestPushAnchorWritesSharedSubset pins that PushAnchor streams the hub
// address and the shared config subset to the anchor's atomic-write command.
func TestPushAnchorWritesSharedSubset(t *testing.T) {
	var lastInput string
	restore := hub.SetRunner(func(argv []string, stdin io.Reader) (string, error) {
		remote := argv[len(argv)-1]
		if strings.Contains(remote, "mv") && stdin != nil {
			b, _ := io.ReadAll(stdin)
			lastInput = string(b)
		}
		return "", nil
	})
	defer restore()

	c := &Config{Hub: "me@new", HubName: "new-box", AnchorHost: "me@anchorbox", CodexModel: "gpt-5"}
	if err := PushAnchor(c); err != nil {
		t.Fatalf("PushAnchor: %v", err)
	}
	if !strings.Contains(lastInput, `"hub": "me@new"`) {
		t.Fatalf("streamed JSON = %q, want it to contain the hub field", lastInput)
	}
	if !strings.Contains(lastInput, `"codex_model": "gpt-5"`) {
		t.Fatalf("streamed JSON = %q, want the shared config subset", lastInput)
	}
}
