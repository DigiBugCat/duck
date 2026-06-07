// Package names owns the hub's ~/.duck/names.json file: the single small JSON
// map from internal tmux name to a session's naming metadata, plus the
// display-name precedence that turns a session into the raw label the picker
// renders.
//
// names.json is the ONLY persisted duck-specific state (DESIGN §4). It is read
// over SSH (`ssh duck cat ~/.duck/names.json`) and written atomically with a
// temp-file + rename over SSH so a partial write never CORRUPTS it.
//
// Concurrency (known single-user limitation): the laptop is the only writer
// (codex runs laptop-side, DESIGN §5) and there is no hub helper binary. The
// temp+rename guarantees the file is never left half-written, but it does NOT
// serialize a read-modify-write cycle: two duck processes on the same laptop
// that each Load → modify → Save concurrently can interleave so the second
// rename clobbers the first writer's change (a lost update). This is acceptable
// for a single user driving one terminal at a time; the real fix would be a
// hub-side flock (or compare-and-swap on a version) around the load+save, which
// duck deliberately does not carry. Cross-process atomicity here means
// no-corruption, not no-lost-updates.
//
// Resolve implements the display precedence: user-set ▸ codex-generated ▸
// dir-derived. The result is rendered RAW (spaces, caps, emoji) — it is not a
// tmux name, so it carries no slug and no collision suffix.
package names

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"time"
	"unicode"
)

// remotePath is the hub-side location of the names file. Reads cat it; writes
// stream to a temp sibling and rename over it.
const remotePath = "~/.duck/names.json"

// tmpPath is the temp sibling JSON is streamed to before the atomic rename.
const tmpPath = "~/.duck/names.json.tmp"

// Entry is the naming metadata for one tmux session, keyed by its internal tmux
// name in the Names map. UserName (a manual `duck rename`) wins; CodexName is
// the cached codex-generated title, valid while CodexHash matches the captured
// head; Dir is the tilde-form working directory used for the dir-derived floor.
type Entry struct {
	UserName  string    `json:"userName,omitempty"`
	CodexName string    `json:"codexName,omitempty"`
	CodexHash string    `json:"codexHash,omitempty"` // content hash of the head the codex name was minted from
	Dir       string    `json:"dir,omitempty"`
	CreatedAt time.Time `json:"createdAt,omitempty"`
}

// Names is the whole names.json document: tmux-name → Entry.
type Names struct {
	Names map[string]Entry `json:"names"`
}

// Runner is the injectable SSH seam (subset of *sshx.Client) names uses to read
// and write the hub file. Tests swap a fake asserting the cat / temp+rename
// command strings; production passes a real *sshx.Client.
type Runner interface {
	Run(remoteCmd string) (string, error)
	RunInput(remoteCmd string, stdin io.Reader) (string, error)
}

// Store reads and writes names.json on the hub over an injected Runner. The
// zero value is not usable; construct with NewStore.
type Store struct {
	run Runner
}

// NewStore returns a Store backed by the given Runner.
func NewStore(run Runner) *Store {
	return &Store{run: run}
}

// Load reads and parses ~/.duck/names.json from the hub. A missing file is not
// an error: the cat falls back to `{}` (so the first run works) and a blank/
// empty document yields an empty (zero-entry) Names.
func (s *Store) Load() (Names, error) {
	empty := Names{Names: map[string]Entry{}}
	// `cat … 2>/dev/null || echo '{}'` makes a missing file read as an empty
	// document instead of erroring.
	out, err := s.run.Run("cat " + remotePath + " 2>/dev/null || echo '{}'")
	if err != nil {
		return empty, err
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" || trimmed == "{}" {
		return empty, nil
	}
	var n Names
	if err := json.Unmarshal([]byte(trimmed), &n); err != nil {
		return empty, err
	}
	if n.Names == nil {
		n.Names = map[string]Entry{}
	}
	return n, nil
}

// Save serializes n and writes it to the hub atomically: stream JSON to a temp
// sibling over SSH, then `mv` it over names.json in the SAME ssh call so a
// partial write never leaves a CORRUPT file. The atomic rename does NOT prevent
// a lost update, though: if another laptop-side duck process Loaded the same
// document, modified it, and Saves after this one, its rename overwrites this
// change. That window is tolerated as a single-user limitation (see the package
// doc); a hub-side flock around load+save would be the real fix.
func (s *Store) Save(n Names) error {
	if n.Names == nil {
		n.Names = map[string]Entry{}
	}
	data, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return err
	}
	// One ssh call: mkdir the dir, stream JSON into the temp sibling, then mv it
	// over the target. The mv is atomic on a single filesystem.
	cmd := "mkdir -p ~/.duck && cat > " + tmpPath + " && mv " + tmpPath + " " + remotePath
	_, err = s.run.RunInput(cmd, bytes.NewReader(data))
	return err
}

// Resolve returns the raw display name for a session, applying the precedence
// user-set ▸ live pane title ▸ codex-generated ▸ derive(dir) ▸ tmux name. n is
// the loaded document; dir is the live @duck_dir (preferred over a stale
// Entry.Dir); paneTitle is the session's live #{pane_title}. The pane title wins
// over a (possibly stale, possibly low-quality) frozen CodexName because it is
// the name the running program — Claude Code — set for the CURRENT task; only an
// explicit user rename outranks it. The result is never slugified.
func Resolve(n Names, tmuxName, dir, paneTitle string) string {
	e, ok := n.Names[tmuxName]
	if ok && e.UserName != "" {
		return e.UserName
	}
	// Pull the already-made name the running program wrote (Claude Code's task
	// summary), instead of spending a codex call to regenerate one.
	if t := CleanTitle(paneTitle); t != "" {
		return t
	}
	if ok && e.CodexName != "" {
		return e.CodexName
	}
	// Prefer the live dir; fall back to the stored Entry.Dir.
	if dir != "" {
		return Derive(dir)
	}
	if ok && e.Dir != "" {
		return Derive(e.Dir)
	}
	// Foreign/legacy session: not created by duck, so no names.json entry and no
	// @duck_dir. The tmux session name is the only identifying info we have — show
	// it rather than a meaningless "~", so old sessions read sensibly in the list.
	return tmuxName
}

// CleanTitle extracts a session name from a raw tmux #{pane_title} IFF the title
// was set by Claude Code, returning "" otherwise so Resolve falls through to the
// codex/dir-derived floor. Claude Code prefixes its title with a status glyph
// (the ✳/✶/✻ "sparkle" dingbats while idle, a braille spinner frame while
// working); a bare shell leaves pane_title at the terminal default (the hostname,
// the running command, the cwd) with NO such glyph. So we GATE on the glyph:
//
//   - no leading status glyph  → not Claude's title → "" (don't clobber the floor)
//   - leading glyph(s)+spaces  → strip them; the remainder is the task summary
//   - remainder empty or the placeholder "Claude Code" (Claude started, no
//     summary yet) → "" (fall through to dir-derived)
//
// Gating on the glyph (rather than blocklisting known generic strings) means any
// non-Claude pane_title degrades safely to the dir-derived name. This is coupled
// to Claude Code's title format; if that format changes, sessions fall back to
// the floor rather than showing garbage.
func CleanTitle(raw string) string {
	runes := []rune(raw)
	if len(runes) == 0 || !isClaudeStatusGlyph(runes[0]) {
		return ""
	}
	i := 0
	for i < len(runes) && (isClaudeStatusGlyph(runes[i]) || unicode.IsSpace(runes[i])) {
		i++
	}
	t := strings.TrimSpace(string(runes[i:]))
	if t == "" || strings.EqualFold(t, "Claude Code") {
		return ""
	}
	return t
}

// isClaudeStatusGlyph reports whether r is one of the leading status glyphs
// Claude Code prepends to its terminal title: the dingbat "sparkle/asterisk"
// family (U+2700–U+27BF, e.g. ✳ U+2733) it shows when idle, and the braille
// pattern range (U+2800–U+28FF) it cycles as a spinner while working.
func isClaudeStatusGlyph(r rune) bool {
	switch {
	case r >= 0x2700 && r <= 0x27BF: // dingbats incl. ✳ ✶ ✻ ✽ ✢
		return true
	case r >= 0x2800 && r <= 0x28FF: // braille spinner frames (⠂ …)
		return true
	}
	return false
}

// Derive is the dir-derived floor: the base of a tilde-form dir (`~/dev/foo` →
// `foo`). It guarantees Resolve always has a non-empty label even with no
// entry and no codex name.
func Derive(dir string) string {
	d := strings.TrimRight(dir, "/")
	if d == "" || d == "~" {
		return "~"
	}
	if i := strings.LastIndex(d, "/"); i >= 0 {
		d = d[i+1:]
	}
	if d == "" {
		return "~"
	}
	return d
}
