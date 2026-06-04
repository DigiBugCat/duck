// Package paths handles tilde-form path normalization and deterministic
// per-path identifiers shared by the hub and mutagen layers.
//
// Ported from flok/internal/paths/paths.go. The only behavioral change is the
// mutagen session-name prefix flok- -> duck- so the two tools' sessions never
// collide on a shared mutagen daemon (see mutagen.List filter, which is renamed
// to match).
package paths

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Contract replaces the user's home directory prefix with "~".
// Always returns a tilde-form path when the input is under $HOME.
func Contract(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if abs == home {
		return "~"
	}
	if strings.HasPrefix(abs, home+string(os.PathSeparator)) {
		return "~/" + abs[len(home)+1:]
	}
	return abs
}

// Expand replaces a leading "~" with the user's home directory and resolves
// relative paths against the current working directory.
func Expand(p string) (string, error) {
	if p == "~" {
		return os.UserHomeDir()
	}
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, p[2:]), nil
	}
	return filepath.Abs(p)
}

// ID returns a deterministic short hex identifier for a tilde-form path.
// Used as the on-disk directory name on the hub and as part of mutagen session names.
func ID(tildePath string) string {
	sum := sha256.Sum256([]byte(tildePath))
	return hex.EncodeToString(sum[:6]) // 12 hex chars
}

// SessionName returns the mutagen sync session name for a bundle/path pair.
func SessionName(bundle, tildePath string) string {
	return fmt.Sprintf("duck-%s-%s", bundle, ID(tildePath))
}

// claudeSlugReplacer mirrors Claude Code's project-directory slug rule: an
// absolute path becomes a single token with every "/" and "." turned into "-"
// (case preserved). Verified empirically: /Users/jane.doe/dev maps to
// the on-disk slug -Users-jane-doe-dev (note the "." in the username).
var claudeSlugReplacer = strings.NewReplacer("/", "-", ".", "-")

// ClaudeProjectDir returns the tilde-form path of the ~/.claude/projects/<slug>
// directory Claude Code uses for sessions started in absCwd, deriving <slug>
// with the same "/"→"-", "."→"-" rule Claude itself applies. Because duck
// guarantees the same $HOME/username on both machines, the slug computed here is
// byte-identical to the one Claude wrote and to the one the hub would compute —
// so the per-folder transcript+memory corpus lines up across machines. absCwd
// must be absolute (the caller passes the resolved cwd).
func ClaudeProjectDir(absCwd string) string {
	slug := claudeSlugReplacer.Replace(absCwd)
	return "~/.claude/projects/" + slug
}

// Quote single-quotes s for safe interpolation into a remote /bin/sh command,
// escaping any embedded single quotes. Shared by the session, namer, and hub
// layers (all of which build remote tmux/shell command strings) so the quoting
// rule lives in exactly one place.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// IsEmptyDir returns true if the path does not exist or is an empty directory.
// Returns an error if the path exists but is not a directory.
func IsEmptyDir(p string) (bool, error) {
	info, err := os.Stat(p)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("%s exists and is not a directory", p)
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}
