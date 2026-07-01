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

	// AttachTransport selects the INTERACTIVE-ATTACH transport. The default
	// (empty) is "auto": use tssh (trzsz-ssh, UDP/QUIC roaming) when the local
	// `tssh` client is present, else ssh — so a client opts in just by installing
	// tssh, no config needed (the hub always supports it, `duck hub setup`
	// installs tsshd). The two explicit overrides are "ssh" (force ssh even if
	// tssh is installed) and "tssh" (force tssh, warning + ssh fallback when the
	// client is absent). Either way tssh only replaces the interactive `tmux
	// attach` — the SSH control plane (Run, the opener's port forwards, ReadFile,
	// naming, provisioning, terminfo) always stays on ssh. Read via Transport()
	// so the auto default lives in exactly one place.
	AttachTransport string `toml:"attach_transport,omitempty"`

	// HubTsshdPath is the absolute path to tsshd on the hub, detected by `duck hub
	// setup` over a login shell (`command -v tsshd`). The tssh attach passes it via
	// --tsshd-path so the hub's non-login ssh shell finds tsshd even when it lives
	// off the default PATH (Homebrew's /opt/homebrew/bin on Apple Silicon). Empty
	// when detection found nothing (e.g. a Linux hub where tssh auto-installs tsshd
	// itself) — the attach then omits --tsshd-path and lets tssh self-resolve.
	HubTsshdPath string `toml:"hub_tsshd_path,omitempty"`

	// AutoName is the per-dir auto-naming toggle (DESIGN §5 / §M3): the key is a
	// tilde-form dir, the value whether duck may send that dir's remote terminal
	// content to the codex model when it first sees an unnamed session. A missing
	// key is OFF — auto-naming is opt-in because it is a real data flow.
	AutoName map[string]bool `toml:"auto_name,omitempty"`

	// SyncClaudeHistory gates per-folder Claude history sync: when on, bare `duck
	// <folder>` ALSO co-syncs that folder's ~/.claude/projects/<slug> directory
	// (its transcripts + auto-memory) to the hub, bidirectionally, scoped to the
	// folders you actually duck into. ON by default — the co-sync is scoped to the
	// folder's own slug (never auth/config/locks), so it mirrors your working
	// history to the hub out of the box. A pointer so the zero value (absent key)
	// reads as ON via SyncClaudeHistoryEnabled; `false` is the explicit opt-out.
	// See flow.Flow.coSyncClaude.
	SyncClaudeHistory *bool `toml:"sync_claude_history,omitempty"`

	// AutoUpdate gates the background self-updater (on by default). When unset
	// (nil) or true, every `duck` run spawns a throttled detached check that
	// replaces the binary in place when a newer GitHub release exists; the next
	// run picks it up. A pointer so the zero value (absent key) reads as ON via
	// AutoUpdateEnabled — `false` is the explicit opt-out. Dev (from-source) builds
	// ignore this entirely and never auto-update.
	AutoUpdate *bool `toml:"auto_update,omitempty"`

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
// "auto" for an empty/unset value (and a nil receiver) so callers need no guard.
// "auto" (tssh-if-present, else ssh), "ssh", and "tssh" are the meaningful
// values; the setter (`duck config attach-transport`) validates input, so any
// stored value is one of those — and "auto" is stored as empty to keep the
// config.toml clean.
func (c *Config) Transport() string {
	if c == nil || c.AttachTransport == "" {
		return "auto"
	}
	return c.AttachTransport
}

// SyncClaudeHistoryEnabled reports whether per-folder Claude history co-sync is
// on. The default (nil pointer, or a nil receiver) is ON — the co-sync is scoped
// to each ducked folder's own ~/.claude/projects/<slug>; only an explicit
// `sync_claude_history = false` turns it off.
func (c *Config) SyncClaudeHistoryEnabled() bool {
	if c == nil || c.SyncClaudeHistory == nil {
		return true
	}
	return *c.SyncClaudeHistory
}

// AutoUpdateEnabled reports whether the background self-updater is on. The
// default (nil pointer, or a nil receiver) is ON — auto-update is opt-out; only
// an explicit `auto_update = false` turns it off.
func (c *Config) AutoUpdateEnabled() bool {
	if c == nil || c.AutoUpdate == nil {
		return true
	}
	return *c.AutoUpdate
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
