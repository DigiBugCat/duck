// Package config holds the laptop-side client configuration: the hub address
// and its friendly display name. Ported from flok/internal/config/config.go;
// the config directory is renamed flok -> duck and laptop-side cache/registry
// path helpers are added for later milestones (M2/M3).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/BurntSushi/toml"

	"github.com/DigiBugCat/duck/internal/anchor"
	"github.com/DigiBugCat/duck/internal/hub"
)

type Config struct {
	Hub     string `toml:"hub"`                // user@host used to SSH
	HubName string `toml:"hub_name,omitempty"` // remote `hostname`, captured at registration time

	// HubHome is the hub's absolute $HOME (e.g. /home/andrew), captured over SSH
	// at registration time alongside HubName. It is the cache that lets the
	// cross-machine Claude-history reconciler know the hub's path form without an
	// SSH round-trip on every duck run. Empty on configs predating this field —
	// the reconciler detects it lazily and writes it back. See command/claudehistory.go.
	HubHome string `toml:"hub_home,omitempty"`

	// AnchorHost is the anchor host address (user@host), independently
	// configurable from Hub: it holds ~/.duck/anchor.json, a small shared-state
	// file (the hub address + a subset of user-level config) that every laptop
	// pointed at it reads on each hub-touching command (see ResolveAnchor,
	// wired into RequireHub) and writes to on change (see PushAnchor). It can be
	// the same box as Hub (in which case a hub move needs the same manual
	// carry-over names.json already requires) or a separate always-on box (in
	// which case a hub move is zero-touch on every other laptop). Empty means
	// the feature is off — opt-in, like SyncClaudeHistory and AutoName. No
	// token/secret: auth is whatever SSH access already reaches that host.
	AnchorHost string `toml:"anchor_host,omitempty"`

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

// RequireHub loads the config, resolves it against the anchor (best-effort —
// see ResolveAnchor), and fails with a clear message if no hub is set. This is
// the single insertion point that makes anchor resolution automatic: every
// command that needs a hub gets a chance to pick up a hub move made from
// another laptop before checking whether one is configured at all.
func RequireHub() (*Config, error) {
	c, err := Load()
	if err != nil {
		return nil, err
	}
	c = ResolveAnchor(c)
	if c.Hub == "" {
		return nil, fmt.Errorf("no hub configured. run: duck hub setup <user@host>")
	}
	return c, nil
}

// anchorConfigKeys is the shared config subset synced through the anchor:
// fields that are genuinely user-level preferences, not per-machine state
// (Folders, AutoName, AutoUpdate, HubTsshdPath stay local-only).
const (
	anchorKeyCodexModel        = "codex_model"
	anchorKeyAttachTransport   = "attach_transport"
	anchorKeySyncClaudeHistory = "sync_claude_history"
)

// ResolveAnchor is a best-effort pull: when c.AnchorHost is set, it loads the
// anchor state and, for each field the anchor knows about, overwrites the
// in-memory value AND persists it locally via Save (so an offline laptop
// still has the last-known-good value on its next run). Any failure (host
// unreachable, bad JSON, a local Save error) is swallowed — the anchor is
// advisory, never a hard dependency — and c is returned unchanged in that
// case. c must be non-nil.
func ResolveAnchor(c *Config) *Config {
	if c.AnchorHost == "" {
		return c
	}
	st, err := anchor.NewStore(hub.New(c.AnchorHost)).Load()
	if err != nil {
		return c
	}
	changed := false
	if st.Hub != "" && st.Hub != c.Hub {
		c.Hub = st.Hub
		c.HubName = st.HubName
		changed = true
	}
	if v, ok := st.Config[anchorKeyCodexModel]; ok && v != c.CodexModel {
		c.CodexModel = v
		changed = true
	}
	if v, ok := st.Config[anchorKeyAttachTransport]; ok && v != c.AttachTransport {
		c.AttachTransport = v
		changed = true
	}
	if v, ok := st.Config[anchorKeySyncClaudeHistory]; ok {
		if b, perr := strconv.ParseBool(v); perr == nil && (c.SyncClaudeHistory == nil || *c.SyncClaudeHistory != b) {
			c.SyncClaudeHistory = &b
			changed = true
		}
	}
	if changed {
		_ = Save(c) // best-effort local cache; a write failure must not block resolution.
	}
	return c
}

// PushAnchor is the write-through counterpart to ResolveAnchor: when
// c.AnchorHost is set, it pushes c's current Hub/HubName and the shared
// config subset to the anchor host. Best-effort — callers (the `duck hub set`
// and shared `duck config` setters) call this after a successful local Save
// and log a warning on failure, but never fail the command over it.
func PushAnchor(c *Config) error {
	if c.AnchorHost == "" {
		return nil
	}
	st := State{
		Hub:     c.Hub,
		HubName: c.HubName,
		Config: map[string]string{
			anchorKeyAttachTransport:   c.AttachTransport,
			anchorKeySyncClaudeHistory: strconv.FormatBool(c.SyncClaudeHistoryEnabled()),
		},
	}
	if c.CodexModel != "" {
		st.Config[anchorKeyCodexModel] = c.CodexModel
	}
	return anchor.NewStore(hub.New(c.AnchorHost)).Save(st)
}

// State is a local alias for anchor.State so command/ never needs to import
// internal/anchor directly — config is already the layer command/ talks to.
type State = anchor.State
