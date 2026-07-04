package names

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

type fakeRunner struct {
	cmds   []string
	inputs []string // stdin content per RunInput call
	out    map[string]string
	err    error
}

func (f *fakeRunner) Run(cmd string) (string, error) {
	f.cmds = append(f.cmds, cmd)
	if f.err != nil {
		return "", f.err
	}
	if f.out != nil {
		if v, ok := f.out[cmd]; ok {
			return v, nil
		}
	}
	return "", nil
}

func (f *fakeRunner) RunInput(cmd string, stdin io.Reader) (string, error) {
	f.cmds = append(f.cmds, cmd)
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		f.inputs = append(f.inputs, string(b))
	}
	return "", f.err
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	f := &fakeRunner{}
	// Default fake returns "" for the cat → empty document.
	s := NewStore(f)
	n, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(n.Names) != 0 {
		t.Fatalf("missing file should yield empty Names, got %+v", n.Names)
	}
	// The read must tolerate a missing file (cat … || echo '{}').
	if len(f.cmds) != 1 || !strings.Contains(f.cmds[0], "cat "+remotePath) {
		t.Fatalf("Load should cat the names file, got %v", f.cmds)
	}
	if !strings.Contains(f.cmds[0], "{}") {
		t.Fatalf("Load must fall back to '{}' on a missing file: %q", f.cmds[0])
	}
}

func TestLoadParsesDocument(t *testing.T) {
	f := &fakeRunner{out: map[string]string{}}
	cmd := "cat " + remotePath + " 2>/dev/null || echo '{}'"
	f.out[cmd] = `{"names":{"foo":{"userName":"My Session"}}}`
	s := NewStore(f)
	n, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if n.Names["foo"].UserName != "My Session" {
		t.Fatalf("parsed entry wrong: %+v", n.Names["foo"])
	}
}

func TestSaveIsAtomicTempThenRenameInOneCall(t *testing.T) {
	f := &fakeRunner{}
	s := NewStore(f)
	n := Names{Names: map[string]Entry{"foo": {UserName: "Hi 👋"}}}
	if err := s.Save(n); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Atomic = exactly ONE ssh call that writes the temp then renames over the
	// target. Two calls would not be atomic.
	if len(f.cmds) != 1 {
		t.Fatalf("Save must be a single atomic ssh call, got %d: %v", len(f.cmds), f.cmds)
	}
	cmd := f.cmds[0]
	if !strings.Contains(cmd, "cat > "+tmpPath) {
		t.Fatalf("Save must stream JSON into the temp sibling: %q", cmd)
	}
	if !strings.Contains(cmd, "mv "+tmpPath+" "+remotePath) {
		t.Fatalf("Save must mv the temp over the target (atomic rename): %q", cmd)
	}
	// The temp write must precede the rename within the one command.
	if strings.Index(cmd, "cat > "+tmpPath) > strings.Index(cmd, "mv "+tmpPath) {
		t.Fatalf("temp write must come before the rename: %q", cmd)
	}
	// The JSON streamed on stdin must round-trip (raw UTF-8 preserved).
	if len(f.inputs) != 1 {
		t.Fatalf("Save should stream JSON on stdin once, got %d", len(f.inputs))
	}
	var got Names
	if err := json.Unmarshal([]byte(f.inputs[0]), &got); err != nil {
		t.Fatalf("streamed JSON is invalid: %v", err)
	}
	if got.Names["foo"].UserName != "Hi 👋" {
		t.Fatalf("raw UTF-8 display name not preserved: %+v", got.Names["foo"])
	}
}

func TestResolvePrecedenceUserCodexDir(t *testing.T) {
	// user-set wins over codex over dir (no pane title in play).
	n := Names{Names: map[string]Entry{
		"a": {UserName: "Manual", CodexName: "Codex Title", Dir: "~/dev/a"},
		"b": {CodexName: "Codex Title", Dir: "~/dev/b"},
		"c": {Dir: "~/dev/c"},
	}}
	if got := Resolve(n, "a", "~/dev/a", ""); got != "Manual" {
		t.Errorf("user-set should win, got %q", got)
	}
	if got := Resolve(n, "b", "~/dev/b", ""); got != "Codex Title" {
		t.Errorf("codex should win over dir, got %q", got)
	}
	if got := Resolve(n, "c", "~/dev/c", ""); got != "c" {
		t.Errorf("dir-derived floor, got %q", got)
	}
	// An unknown session falls to the dir-derived floor from the live dir.
	if got := Resolve(n, "unknown", "~/dev/widget", ""); got != "widget" {
		t.Errorf("unknown session should derive from live dir, got %q", got)
	}
}

// TestResolvePaneTitlePrecedence pins the pane-title rules: a Claude-set pane
// title (status glyph + summary) wins over a frozen codex name and the dir floor,
// but never over a user rename; a generic/non-Claude pane title is ignored.
func TestResolvePaneTitlePrecedence(t *testing.T) {
	n := Names{Names: map[string]Entry{
		"a": {UserName: "Manual", CodexName: "Codex Title", Dir: "~/dev/a"},
		"b": {CodexName: "Codex Title", Dir: "~/dev/b"},
		"c": {Dir: "~/dev/c"},
	}}
	// pane title beats a frozen codex name (it's the live name for the current task).
	if got := Resolve(n, "b", "~/dev/b", "✳ Analyze market performance"); got != "Analyze market performance" {
		t.Errorf("pane title should beat codex name, got %q", got)
	}
	// pane title beats the dir floor.
	if got := Resolve(n, "c", "~/dev/c", "⠂ Extract and organize transcripts"); got != "Extract and organize transcripts" {
		t.Errorf("pane title should beat dir floor, got %q", got)
	}
	// a user rename still outranks the pane title.
	if got := Resolve(n, "a", "~/dev/a", "✳ Some task"); got != "Manual" {
		t.Errorf("user-set should outrank pane title, got %q", got)
	}
	// a generic (non-Claude) pane title is ignored — falls to the floor.
	if got := Resolve(n, "c", "~/dev/c", "Duck.local"); got != "c" {
		t.Errorf("generic pane title should be ignored, got %q", got)
	}
}

func TestResolveForeignSessionShowsTmuxName(t *testing.T) {
	// A legacy/foreign session (not created by duck) has no entry and no
	// @duck_dir (empty live dir). Rather than a meaningless "~", show its tmux
	// session name so old sessions remain identifiable in the list.
	n := Names{Names: map[string]Entry{}}
	if got := Resolve(n, "fix-claudeai-cookies-sync-authentication-error-2", "", ""); got != "fix-claudeai-cookies-sync-authentication-error-2" {
		t.Errorf("foreign session should fall back to tmux name, got %q", got)
	}
	// An entry that exists but has no name and no dir also falls back to the id.
	n2 := Names{Names: map[string]Entry{"sess": {}}}
	if got := Resolve(n2, "sess", "", ""); got != "sess" {
		t.Errorf("entry with no name/dir should fall back to tmux name, got %q", got)
	}
}

// TestCleanTitle locks the pane-title parsing to the exact strings tmux returns
// (captured live from the hub): Claude-set titles (status glyph + summary) are
// accepted with the glyph stripped; everything else returns "".
func TestCleanTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"✳ Set up new Obsidian vault for todo tracking", "Set up new Obsidian vault for todo tracking"},
		{"⠂ Extract and organize Plaud recording transcripts", "Extract and organize Plaud recording transcripts"},
		{"✳ Investigate stack", "Investigate stack"},
		{"✳ Claude Code", ""},       // placeholder before a summary exists
		{"Duck.local", ""},          // bare shell — hostname, no glyph
		{"zsh", ""},                 // bare shell — command name, no glyph
		{"~/Obsidian/Business", ""}, // cwd, no glyph
		{"", ""},
	}
	for _, c := range cases {
		if got := CleanTitle(c.in); got != c.want {
			t.Errorf("CleanTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestResolveNeverSlugifiesDisplay(t *testing.T) {
	// The display name is RAW: spaces/caps/emoji survive (no -2/-3 suffix, no
	// slug). That distinction is the whole point of the names layer.
	n := Names{Names: map[string]Entry{"x": {UserName: "Auth Refactor 🚀"}}}
	if got := Resolve(n, "x", "~/dev/x", ""); got != "Auth Refactor 🚀" {
		t.Fatalf("display name must be raw, got %q", got)
	}
}

func TestDeriveTildeBase(t *testing.T) {
	cases := map[string]string{
		"~/dev/foo": "foo",
		"~/dev/a/b": "b",
		"~":         "~",
		"~/":        "~",
	}
	for dir, want := range cases {
		if got := Derive(dir); got != want {
			t.Errorf("Derive(%q) = %q, want %q", dir, got, want)
		}
	}
}
