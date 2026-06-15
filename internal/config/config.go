// Package config holds the laptop-side client configuration: the hub address
// and its friendly display name. Ported from flok/internal/config/config.go;
// the config directory is renamed flok -> duck and laptop-side cache/registry
// path helpers are added for later milestones (M2/M3).
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Hub     string `toml:"hub"`                // user@host used to SSH
	HubName string `toml:"hub_name,omitempty"` // remote `hostname`, captured at registration time

	// CodexModel selects the laptop-side codex model used for auto-naming
	// (DESIGN §5). Empty falls back to the built-in default in command/wiring.go.
	CodexModel string `toml:"codex_model,omitempty"`

	// AttachTransport selects the INTERACTIVE-ATTACH transport: "ssh" (default,
	// empty) or "mosh". mosh is opt-in and only replaces the interactive
	// `tmux attach` — the SSH control plane (Run, the opener's port forwards,
	// ReadFile, naming, provisioning, terminfo) and mosh's own bootstrap always
	// stay on ssh, and ssh is the fallback when the local `mosh` client is absent.
	// Read via Transport() so the ssh default lives in exactly one place.
	AttachTransport string `toml:"attach_transport,omitempty"`

	// AutoName is the per-dir auto-naming toggle (DESIGN §5 / §M3): the key is a
	// tilde-form dir, the value whether duck may send that dir's remote terminal
	// content to the codex model when it first sees an unnamed session. A missing
	// key is OFF — auto-naming is opt-in because it is a real data flow.
	AutoName map[string]bool `toml:"auto_name,omitempty"`

	// SyncClaudeHistory is the global opt-in for per-folder Claude history sync:
	// when true, bare `duck <folder>` ALSO co-syncs that folder's
	// ~/.claude/projects/<slug> directory (its transcripts + auto-memory) to the
	// hub, bidirectionally, scoped to the folders you actually duck into. OFF by
	// default because it ships terminal transcripts to the hub — a real data flow,
	// like auto-naming. See flow.Flow.coSyncClaude.
	SyncClaudeHistory bool `toml:"sync_claude_history,omitempty"`

	// Folders is the per-folder sync policy store: the key is a tilde-form dir,
	// the value is "sync" or "never". It lets bare `duck` remember whether a
	// folder should auto-mirror so it never re-prompts (and so a known-safe folder
	// auto-syncs without a prompt). A missing key is unknown. Laptop-side only;
	// see internal/folder.Store.
	Folders map[string]string `toml:"folders,omitempty"`
}

// AutoNameEnabled reports whether auto-naming is on for the given tilde-form
// dir. A missing entry (or a nil map) is OFF: sending pane content to the model
// is opt-in per-dir. A nil receiver is treated as OFF so callers need no guard.
func (c *Config) AutoNameEnabled(dir string) bool {
	if c == nil || c.AutoName == nil {
		return false
	}
	return c.AutoName[dir]
}

// Transport returns the configured interactive-attach transport, defaulting to
// "ssh" for an empty/unset value (and a nil receiver) so callers need no guard.
// "ssh" and "mosh" are the meaningful values; the setter (`duck config
// attach-transport`) validates input, so any stored value is one of those.
func (c *Config) Transport() string {
	if c == nil || c.AttachTransport == "" {
		return "ssh"
	}
	return c.AttachTransport
}

func path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "duck", "config.toml"), nil
}

// Path returns the absolute location of the duck config file (whether or not it
// exists yet), so commands like `duck config` can show and open it.
func Path() (string, error) { return path() }

func Load() (*Config, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	c := &Config{}
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return c, nil
	} else if err != nil {
		return nil, err
	}
	if _, err := toml.DecodeFile(p, c); err != nil {
		return nil, err
	}
	return c, nil
}

func Save(c *Config) error {
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	return toml.NewEncoder(f).Encode(c)
}

// RequireHub loads the config and fails with a clear message if no hub is set.
func RequireHub() (*Config, error) {
	c, err := Load()
	if err != nil {
		return nil, err
	}
	if c.Hub == "" {
		return nil, fmt.Errorf("no hub configured. run: duck hub setup <user@host>")
	}
	return c, nil
}
