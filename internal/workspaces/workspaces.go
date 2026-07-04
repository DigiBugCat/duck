// Package workspaces owns duck's durable workspace ledger: one small JSON
// record per workspace, grouped on disk by the project directory the workspace
// lives in. The records live INSIDE Claude Code's projects corpus, in a
// duck-owned subdir so we stay clear of Claude's own session .jsonl files and
// UUID subdirs:
//
//	<claude-projects-root>/<slug>/duck/<workspace-name>.json
//	e.g. ~/.claude/projects/-home-andrew-Obsidian-aviary-duck/duck/duck-4.json
//
// The point of the per-dir grouping is that ONE directory holds MANY workspaces
// (duck-2 and duck-4 both live in the duck repo), so the <slug> dir is a GROUP
// key, and the tmux session name is the record key within that group. The slug
// scheme is Claude Code's own (internal/claude) — duck does NOT invent one, so
// records land alongside the corpus duck already syncs.
//
// Records live on the HUB (that is where tmux and the routines tick run), but
// duck may run on a laptop driving the hub over SSH. So — exactly like
// internal/names — the store goes through an injected Runner (shell mkdir/cat/
// mv, streamed over the same SSH seam), never local os file I/O. The one
// deliberate difference from names.Store is per-record files with a UNIQUE
// temp name per write ($$-suffixed): a shared temp path lets two concurrent
// writers rename each other's half-written file (the bug fixed in
// internal/routines/state.go). Rename-over still means no-corruption, not
// no-lost-updates — the same single-user caveat names.json carries.
package workspaces

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/DigiBugCat/duck/internal/claude"
	"github.com/DigiBugCat/duck/internal/paths"
)

// DefaultBase is the hub-side root under which the per-project group dirs live:
// Claude Code's projects corpus. It is a Store field (not an env read) because a
// plain os.Getenv would not cross SSH to the hub. NOTE: claude.ProjectsRoot() is
// currently a hardcoded "~/.claude/projects" — there is no CLAUDE_CONFIG_DIR
// resolver in internal/claude (the CLAUDE_CONFIG_DIR handling from 3f37400 is a
// local os.Getenv in command/agentdoc.go, which cannot cross ssh to the hub
// anyway). If a config-root seam is added to internal/claude later, thread it
// here; until then the hardcoded root is correct for the Runner-remote hub path.
var DefaultBase = claude.ProjectsRoot()

// duckSubdir is the leaf under each <slug> dir that holds duck's records, kept
// separate from Claude's session .jsonl files and UUID subdirs in the same slug.
const duckSubdir = "duck"

// Record is one workspace's durable row. Name is the tmux session id and the
// unique key WITHIN a directory group; Dir (tilde-form) is the grouping key and
// is ALWAYS stored so the on-disk encoding never has to be reversed.
type Record struct {
	Name       string    `json:"name"`                 // tmux session name (unique key within the dir)
	Dir        string    `json:"dir"`                  // tilde-form project dir (the grouping key)
	Parent     string    `json:"parent,omitempty"`     // org chart: parent workspace name ("" = motherduck default)
	Title      string    `json:"title,omitempty"`      // role/title, the workspace's report line
	Persistent bool      `json:"persistent,omitempty"` // heal me back into existence after reboot
	Channels   bool      `json:"channels,omitempty"`   // manager pane launched channel-enabled
	Created    time.Time `json:"created"`
	Updated    time.Time `json:"updated"`
}

// Runner is the injectable SSH seam (the same subset of *sshx.Client that
// internal/names uses). Tests swap a fake asserting the mkdir/cat/mv command
// strings; production passes a real *sshx.Client, and the routines tick passes
// a LOCAL sh runner (see LocalRunner) since it runs hub-local.
type Runner interface {
	Run(remoteCmd string) (string, error)
	RunInput(remoteCmd string, stdin io.Reader) (string, error)
}

// Store reads and writes the workspace ledger through an injected Runner. The
// zero value is not usable; construct with NewStore.
type Store struct {
	run  Runner
	base string
}

// NewStore returns a Store backed by run, rooted at DefaultBase.
func NewStore(run Runner) *Store {
	return &Store{run: run, base: DefaultBase}
}

// SetBase overrides the ledger root (tests point it at a scratch dir). Trailing
// slashes are trimmed so path joins stay clean.
func (s *Store) SetBase(base string) {
	s.base = strings.TrimRight(base, "/")
}

// EncodeDir is the deterministic directory-group key: Claude Code's own project
// slug for the given dir. dir is tilde-form (Record.Dir's form); it is expanded
// to an absolute path first because claude.Slug is defined on the ABSOLUTE path
// (the slug embeds it — see claude/paths.go). So `~/Obsidian/aviary/duck` →
// `-home-andrew-Obsidian-aviary-duck`. This is intentionally NOT reversible;
// collisions (should any exist) are harmless because the real Dir is stored
// inside every record and is the authority. EncodeDir is a thin wrapper over
// claude.Slug so callers have one exported name for the group key.
func EncodeDir(dir string) string {
	abs, err := paths.Expand(dir)
	if err != nil || abs == "" {
		abs = dir // best-effort: slug the tilde-form as-is if expansion fails
	}
	return claude.Slug(abs)
}

// dirPath is duck's record directory for a project dir: the <slug> group dir
// plus the duck/ subdir that keeps our files out of Claude's own session tree.
func (s *Store) dirPath(dir string) string {
	return s.base + "/" + EncodeDir(dir) + "/" + duckSubdir
}

// shq shell-quotes a path for the Runner while keeping a leading ~/ unquoted so
// the remote shell still expands it (sh does tilde-expand `~/'quoted rest'`).
// Needed because claude.Slug only rewrites "/" and "." — a project dir with a
// space or quote in its name would otherwise split or inject the command.
func shq(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return "~/" + paths.Quote(rest)
	}
	return paths.Quote(p)
}

// recordPath is the JSON file for one workspace. The name is a tmux session id
// (already restricted to a tmux-legal charset by session.DeriveID), so it is a
// safe filename as-is.
func (s *Store) recordPath(dir, name string) string {
	return s.dirPath(dir) + "/" + name + ".json"
}

// Save writes r atomically as its own JSON file. It stamps Updated (always) and
// Created (only when zero), so callers may Save a bare Record without setting
// timestamps. The write mkdir -p's the group dir, streams JSON to a temp
// sibling suffixed with the shell PID ($$) so concurrent writers never share a
// temp path, then mv's it over the target in the SAME call (atomic on one
// filesystem).
func (s *Store) Save(r Record) error {
	now := time.Now().UTC()
	if r.Created.IsZero() {
		r.Created = now
	}
	r.Updated = now
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	dst := s.recordPath(r.Dir, r.Name)
	// $$ is the shell's PID: a per-writer temp name (contrast names.json's fixed
	// .tmp, which is safe there only because it is single-writer).
	// $$ must stay OUTSIDE the quoting so the shell substitutes its PID.
	cmd := "mkdir -p " + shq(s.dirPath(r.Dir)) +
		" && cat > " + shq(dst) + `.tmp.$$` +
		" && mv " + shq(dst) + `.tmp.$$` + " " + shq(dst)
	_, err = s.run.RunInput(cmd, bytes.NewReader(data))
	return err
}

// Load reads one workspace record. ok=false (nil error) means no such record —
// a missing file or missing group dir is not an error, matching the rest of
// duck's degrade-to-no-op reads.
func (s *Store) Load(dir, name string) (Record, bool, error) {
	p := s.recordPath(dir, name)
	// `cat … || echo` makes a missing file read as an empty string rather than a
	// non-zero exit the Runner would surface as an error.
	out, err := s.run.Run("cat " + shq(p) + " 2>/dev/null || echo ''")
	if err != nil {
		return Record{}, false, err
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return Record{}, false, nil
	}
	var r Record
	if err := json.Unmarshal([]byte(trimmed), &r); err != nil {
		// A corrupt/foreign file is treated as "not present" rather than an error,
		// so a single bad file never breaks a caller looking one record up.
		return Record{}, false, nil
	}
	return r, true, nil
}

// ListDir returns every workspace record for one project dir. A missing group
// dir yields an empty slice and nil error. Corrupt/foreign JSON files in the
// duck/ subdir are skipped silently (they never fail the listing).
func (s *Store) ListDir(dir string) ([]Record, error) {
	// Quote the (possibly space-bearing) group dir; the *.json glob component
	// must stay outside the quotes to keep globbing.
	return s.readGlob(shq(s.dirPath(dir)) + "/*.json")
}

// All returns every record across every project dir on the hub. It looks ONLY
// inside the */duck/ subdirs (never at Claude's own slug-level .jsonl files). A
// missing base yields an empty slice and nil error. Corrupt/foreign files
// anywhere in the tree are skipped silently — one bad file never fails All().
func (s *Store) All() ([]Record, error) {
	return s.readGlob(shq(s.base) + "/*/" + duckSubdir + "/*.json")
}

// readGlob cats every file matching glob in a single Runner call, splitting the
// concatenated files on a NUL sentinel so a record's own contents can never be
// mistaken for a delimiter. A glob that matches nothing (missing dir) prints
// nothing → empty result, no error.
func (s *Store) readGlob(glob string) ([]Record, error) {
	// Per file: print the JSON then a lone NUL byte. `2>/dev/null` and the `|| true`
	// keep a missing directory / no-match glob quiet (empty output, exit 0).
	cmd := "for f in " + glob + "; do cat \"$f\" 2>/dev/null; printf '\\0'; done 2>/dev/null || true"
	out, err := s.run.Run(cmd)
	if err != nil {
		return nil, err
	}
	var records []Record
	for _, chunk := range strings.Split(out, "\x00") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(chunk), &r); err != nil {
			continue // skip corrupt/foreign files; never fail the whole listing
		}
		if r.Name == "" {
			continue // a well-formed but non-workspace JSON object: skip it too
		}
		records = append(records, r)
	}
	return records, nil
}

// Delete removes one workspace record. It is idempotent: `rm -f` on a missing
// file is a no-op, so deleting a record that never existed succeeds.
func (s *Store) Delete(dir, name string) error {
	_, err := s.run.Run("rm -f " + shq(s.recordPath(dir, name)))
	return err
}
