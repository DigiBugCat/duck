// spool.go is the PUBLISH lane onto the Claude Code channel sidecar: a
// per-workspace, hub-local file that anything duck-side can append events to
// (the routines tick, `duck channel publish`, …) so the manager Claude hears
// them WITHOUT tmux send-keys. The sidecar (serve.go) drains each workspace's
// spool every sweep and emits the events as notifications/claude/channel.
//
// Both publishers and the sidecar run on the hub, so this is plain local file
// I/O (contrast internal/names, which goes over SSH). One JSON line per event;
// appends are single Write calls under O_APPEND (atomic for small writes), and
// draining atomically renames the file aside so a concurrent publisher's
// appends land in a fresh spool and are never lost.
package channel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// spoolHome overrides the duck home dir for tests. Empty => $DUCK_HOME (test
// seam, matching internal/routines) else ~/.duck.
var spoolHome string

func duckHome() (string, error) {
	if spoolHome != "" {
		return spoolHome, nil
	}
	if d := os.Getenv("DUCK_HOME"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".duck"), nil
}

// spoolDir is <home>/channel-spool, holding <workspace>.jsonl and
// <workspace>.alive per workspace.
func spoolDir() (string, error) {
	d, err := duckHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "channel-spool"), nil
}

// validWorkspace rejects names that would escape the spool dir. Workspace
// names are tmux session names; a '/' (or empty) has no business here.
func validWorkspace(ws string) error {
	if ws == "" || strings.ContainsAny(ws, "/\\") || ws == "." || ws == ".." {
		return fmt.Errorf("invalid workspace name %q", ws)
	}
	return nil
}

func spoolPath(ws string) (string, error) {
	if err := validWorkspace(ws); err != nil {
		return "", err
	}
	d, err := spoolDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, ws+".jsonl"), nil
}

func alivePath(ws string) (string, error) {
	if err := validWorkspace(ws); err != nil {
		return "", err
	}
	d, err := spoolDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, ws+".alive"), nil
}

// SpoolEvent is one published event: when it was spooled, its content, and
// free-form meta (source, type, …) that the sidecar merges into the channel
// notification's _meta.
type SpoolEvent struct {
	Time    time.Time         `json:"time"`
	Content string            `json:"content"`
	Meta    map[string]string `json:"meta,omitempty"`
}

// Publish appends one event to the workspace's spool. Content is truncated to
// stay well under a line's worth (same cap as serve.go's maxPush) so a huge
// message can't blow up the manager's context or split an atomic append.
func Publish(workspace, content string, meta map[string]string) error {
	p, err := spoolPath(workspace)
	if err != nil {
		return err
	}
	if len(content) > maxPush {
		content = strings.ToValidUTF8(content[:maxPush], "") + " …[truncated]"
	}
	line, err := json.Marshal(SpoolEvent{Time: time.Now().UTC(), Content: content, Meta: meta})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	// One Write of a well-under-4KB payload: atomic under O_APPEND, so
	// concurrent publishers never interleave a line.
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

// DrainSpool atomically claims and returns the workspace's spooled events. It
// renames the spool to a unique sibling (so publishers racing in mid-drain
// recreate a fresh spool and lose nothing), reads+parses it, and removes it.
// A missing spool yields (nil, nil); corrupt lines are skipped.
func DrainSpool(workspace string) ([]SpoolEvent, error) {
	p, err := spoolPath(workspace)
	if err != nil {
		return nil, err
	}
	// Unique aside name (pid + nanos) so overlapping drains never collide.
	aside := fmt.Sprintf("%s.draining.%d.%d", p, os.Getpid(), time.Now().UnixNano())
	if err := os.Rename(p, aside); err != nil {
		if os.IsNotExist(err) {
			return nil, nil // nothing spooled
		}
		return nil, err
	}
	data, err := os.ReadFile(aside)
	if err != nil {
		os.Remove(aside)
		return nil, err
	}
	os.Remove(aside)
	var events []SpoolEvent
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev SpoolEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue // skip corrupt line
		}
		events = append(events, ev)
	}
	return events, nil
}

// TouchAlive refreshes the sidecar's liveness marker for a workspace (an empty
// file whose mtime publishers check via AliveWithin to pick spool vs
// send-keys fallback).
func TouchAlive(workspace string) error {
	p, err := alivePath(workspace)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	now := time.Now()
	if err := os.Chtimes(p, now, now); err != nil {
		if os.IsNotExist(err) {
			f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			return f.Close()
		}
		return err
	}
	return nil
}

// AliveWithin reports whether a sidecar has touched the workspace's marker
// within d (its mtime is fresher than now-d). A missing marker => not alive.
func AliveWithin(workspace string, d time.Duration) bool {
	p, err := alivePath(workspace)
	if err != nil {
		return false
	}
	fi, err := os.Stat(p)
	if err != nil {
		return false
	}
	return time.Since(fi.ModTime()) <= d
}
