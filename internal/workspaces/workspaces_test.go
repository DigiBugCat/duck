package workspaces

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DigiBugCat/duck/internal/claude"
	"github.com/DigiBugCat/duck/internal/paths"
)

// realStore returns a Store backed by the LOCAL sh runner rooted at a scratch
// dir, so save/load/list roundtrips exercise the ACTUAL shell command strings
// (mkdir/cat/mv/glob) against a real filesystem rather than a mock of them.
func realStore(t *testing.T) *Store {
	t.Helper()
	s := NewStore(LocalRunner{})
	s.SetBase(t.TempDir())
	return s
}

func TestEncodeDirMatchesClaudeSlug(t *testing.T) {
	// EncodeDir must equal Claude Code's own slug for the same dir: the tilde is
	// expanded to abs first, then every "/" and "." becomes "-". Assert against
	// claude.Slug(abs) rather than a hardcoded home so the test is home-agnostic.
	for _, in := range []string{"~/Obsidian/aviary/duck", "~", "/abs/path", "~/a.b_c-d"} {
		abs, err := paths.Expand(in)
		if err != nil {
			t.Fatal(err)
		}
		want := claude.Slug(abs)
		if got := EncodeDir(in); got != want {
			t.Errorf("EncodeDir(%q) = %q, want claude.Slug(%q) = %q", in, got, abs, want)
		}
	}
	// A concrete example (abs path shape depends on $HOME so only assert the tail).
	if got := EncodeDir("/home/andrew/Obsidian/aviary/duck"); got != "-home-andrew-Obsidian-aviary-duck" {
		t.Errorf("EncodeDir abs example = %q", got)
	}
	// Determinism: same input, same output, twice.
	if EncodeDir("~/x/y") != EncodeDir("~/x/y") {
		t.Fatal("EncodeDir is not deterministic")
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	s := realStore(t)
	in := Record{Name: "duck-2", Dir: "~/repo", Parent: "motherduck", Title: "builder", Persistent: true, Channels: true}
	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := s.Load("~/repo", "duck-2")
	if err != nil || !ok {
		t.Fatalf("Load: ok=%v err=%v", ok, err)
	}
	if got.Name != in.Name || got.Dir != in.Dir || got.Parent != in.Parent ||
		got.Title != in.Title || !got.Persistent || !got.Channels {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if got.Created.IsZero() || got.Updated.IsZero() {
		t.Fatalf("Save must stamp Created and Updated: %+v", got)
	}
}

func TestSavePreservesCreatedRestampsUpdated(t *testing.T) {
	s := realStore(t)
	if err := s.Save(Record{Name: "w", Dir: "~/d"}); err != nil {
		t.Fatal(err)
	}
	first, _, _ := s.Load("~/d", "w")
	// The realistic update flow is load-modify-save: a record carrying a non-zero
	// Created keeps it, and Updated moves forward (or at least not backwards).
	upd := first
	upd.Title = "renamed"
	if err := s.Save(upd); err != nil {
		t.Fatal(err)
	}
	second, _, _ := s.Load("~/d", "w")
	if !second.Created.Equal(first.Created) {
		t.Fatalf("Created must be preserved across Save: %v vs %v", first.Created, second.Created)
	}
	if second.Updated.Before(first.Updated) {
		t.Fatalf("Updated must not go backwards: %v before %v", second.Updated, first.Updated)
	}
	if second.Title != "renamed" {
		t.Fatalf("Save should overwrite the record: %+v", second)
	}
}

func TestLoadMissingIsNotFound(t *testing.T) {
	s := realStore(t)
	_, ok, err := s.Load("~/nope", "ghost")
	if err != nil {
		t.Fatalf("missing record must not error: %v", err)
	}
	if ok {
		t.Fatal("missing record must report ok=false")
	}
}

func TestListDirTwoWorkspacesOneDir(t *testing.T) {
	s := realStore(t)
	// duck-2 and duck-4 both live in the duck repo — the whole point.
	if err := s.Save(Record{Name: "duck-2", Dir: "~/duck"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(Record{Name: "duck-4", Dir: "~/duck"}); err != nil {
		t.Fatal(err)
	}
	// A record in a DIFFERENT dir must not leak into this listing.
	if err := s.Save(Record{Name: "other", Dir: "~/elsewhere"}); err != nil {
		t.Fatal(err)
	}
	recs, err := s.ListDir("~/duck")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, r := range recs {
		names[r.Name] = true
	}
	if len(recs) != 2 || !names["duck-2"] || !names["duck-4"] {
		t.Fatalf("ListDir(~/duck) = %+v, want duck-2 + duck-4", recs)
	}
}

func TestListDirMissingIsEmpty(t *testing.T) {
	s := realStore(t)
	recs, err := s.ListDir("~/never-created")
	if err != nil {
		t.Fatalf("missing dir must not error: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("missing dir must list empty, got %+v", recs)
	}
}

func TestAllAcrossDirs(t *testing.T) {
	s := realStore(t)
	must := func(r Record) {
		if err := s.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	must(Record{Name: "a", Dir: "~/one"})
	must(Record{Name: "b", Dir: "~/one"})
	must(Record{Name: "c", Dir: "~/two"})
	recs, err := s.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("All() = %d records, want 3: %+v", len(recs), recs)
	}
}

func TestAllEmptyBaseIsEmpty(t *testing.T) {
	s := realStore(t) // base exists (t.TempDir) but has no group dirs yet
	recs, err := s.All()
	if err != nil {
		t.Fatalf("empty base must not error: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("empty base must yield no records, got %+v", recs)
	}
}

func TestDeleteIdempotent(t *testing.T) {
	s := realStore(t)
	if err := s.Save(Record{Name: "gone", Dir: "~/d"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("~/d", "gone"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := s.Load("~/d", "gone"); ok {
		t.Fatal("record should be gone after Delete")
	}
	// Deleting again (now missing) must still succeed.
	if err := s.Delete("~/d", "gone"); err != nil {
		t.Fatalf("Delete on missing record must be idempotent: %v", err)
	}
}

func TestCorruptFileSkippedNotFatal(t *testing.T) {
	s := realStore(t)
	if err := s.Save(Record{Name: "good", Dir: "~/d"}); err != nil {
		t.Fatal(err)
	}
	// Drop a corrupt file and a foreign (well-formed but non-workspace) file into
	// the same group dir; neither may break All()/ListDir().
	group := filepath.Join(s.base, EncodeDir("~/d"), "duck")
	if err := os.WriteFile(filepath.Join(group, "corrupt.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(group, "foreign.json"), []byte(`{"unrelated":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	recs, err := s.ListDir("~/d")
	if err != nil {
		t.Fatalf("ListDir must skip bad files, not error: %v", err)
	}
	if len(recs) != 1 || recs[0].Name != "good" {
		t.Fatalf("ListDir should return only the good record, got %+v", recs)
	}
	all, err := s.All()
	if err != nil || len(all) != 1 {
		t.Fatalf("All must skip bad files: err=%v recs=%+v", err, all)
	}
	// Loading a corrupt record by name reports not-found rather than erroring.
	if _, ok, err := s.Load("~/d", "corrupt"); err != nil || ok {
		t.Fatalf("Load of corrupt record: ok=%v err=%v (want ok=false, nil)", ok, err)
	}
}

// captureRunner records command strings (and stdin) so the atomic-write shape
// can be asserted without a filesystem.
type captureRunner struct {
	cmds   []string
	inputs []string
}

func (c *captureRunner) Run(cmd string) (string, error) { c.cmds = append(c.cmds, cmd); return "", nil }
func (c *captureRunner) RunInput(cmd string, stdin io.Reader) (string, error) {
	c.cmds = append(c.cmds, cmd)
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		c.inputs = append(c.inputs, string(b))
	}
	return "", nil
}

func TestSaveUsesUniqueTempAndAtomicRename(t *testing.T) {
	c := &captureRunner{}
	s := NewStore(c)
	if err := s.Save(Record{Name: "w", Dir: "~/repo"}); err != nil {
		t.Fatal(err)
	}
	if len(c.cmds) != 1 {
		t.Fatalf("Save should be one Runner call, got %d: %v", len(c.cmds), c.cmds)
	}
	cmd := c.cmds[0]
	// Paths are shell-quoted with the leading ~/ left outside the quotes so
	// the remote shell still tilde-expands (see shq).
	group := shq(DefaultBase + "/" + EncodeDir("~/repo") + "/duck")
	rec := shq(DefaultBase + "/" + EncodeDir("~/repo") + "/duck/w.json")
	// Per-record unique temp ($$) + mkdir -p + atomic mv into the duck/ subdir.
	if !strings.Contains(cmd, "mkdir -p "+group) {
		t.Errorf("Save must mkdir the group dir: %q", cmd)
	}
	if !strings.Contains(cmd, ".tmp.$$") {
		t.Errorf("Save must use a unique ($$) temp name, got: %q", cmd)
	}
	if !strings.Contains(cmd, "mv "+rec+".tmp.$$ "+rec) {
		t.Errorf("Save must mv the temp over the record atomically: %q", cmd)
	}
	if len(c.inputs) != 1 || !strings.Contains(c.inputs[0], `"name": "w"`) {
		t.Errorf("Save must stream the record JSON on stdin, got: %v", c.inputs)
	}
}
