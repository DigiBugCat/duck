package claude

import (
	"os"
	"path/filepath"
	"testing"
)

// seedSession writes a minimal transcript with the given cwd into
// root/<slug>/<id>.jsonl.
func seedSession(t *testing.T, root, slug, id, cwd string) {
	t.Helper()
	dir := filepath.Join(root, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A couple of leading lines with no cwd, then a message line carrying it —
	// mirrors real transcripts (first lines are mode/permission/meta records).
	body := `{"type":"mode","sessionId":"` + id + `"}` + "\n" +
		`{"type":"user","cwd":"` + cwd + `","sessionId":"` + id + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// The core scenario: a hub session (Linux /home/andrew) synced onto a Mac
// (/Users/andrew.sulistio) becomes resumable — its transcript lands under the
// Mac's slug and the Mac path is registered — with the hub's original copy and
// the transcript contents left untouched.
func TestReconcileMapsForeignHubSessionOntoLocalMac(t *testing.T) {
	root := t.TempDir()
	macHome := "/Users/andrew.sulistio"
	hubHome := "/home/andrew"
	// A hub-origin session for ~/Obsidian/aviary/duck.
	seedSession(t, root, "-home-andrew-Obsidian-aviary-duck", "sess-1", hubHome+"/Obsidian/aviary/duck")

	var registered []string
	res, err := Reconcile(ReconcileOptions{
		Root:      root,
		LocalHome: macHome,
		Homes:     []string{macHome, hubHome},
		Register:  func(p ...string) ([]string, error) { registered = append(registered, p...); return p, nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	// Transcript copied into the Mac's slug dir…
	macFile := filepath.Join(root, "-Users-andrew-sulistio-Obsidian-aviary-duck", "sess-1.jsonl")
	if !exists(macFile) {
		t.Fatal("session not copied into the local Mac slug dir")
	}
	// …the hub original is still there (non-destructive)…
	if !exists(filepath.Join(root, "-home-andrew-Obsidian-aviary-duck", "sess-1.jsonl")) {
		t.Fatal("hub original was removed — must be non-destructive")
	}
	// …and the Mac path was registered.
	want := macHome + "/Obsidian/aviary/duck"
	if len(registered) != 1 || registered[0] != want {
		t.Fatalf("registered = %v, want [%s]", registered, want)
	}
	if res.CopiedFiles != 1 || res.Mapped != 1 {
		t.Fatalf("result = %+v, want 1 copied / 1 mapped", res)
	}
}

func TestReconcileIsIdempotentAndNonDestructive(t *testing.T) {
	root := t.TempDir()
	macHome, hubHome := "/Users/andrew.sulistio", "/home/andrew"
	seedSession(t, root, "-home-andrew-dev", "s1", hubHome+"/dev")

	opt := ReconcileOptions{Root: root, LocalHome: macHome, Homes: []string{macHome, hubHome},
		Register: func(p ...string) ([]string, error) { return p, nil }}

	if _, err := Reconcile(opt); err != nil {
		t.Fatal(err)
	}
	// Mutate the local copy to prove a second pass never overwrites it.
	localCopy := filepath.Join(root, "-Users-andrew-sulistio-dev", "s1.jsonl")
	if err := os.WriteFile(localCopy, []byte("LOCAL-EDIT"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Reconcile(opt)
	if err != nil {
		t.Fatal(err)
	}
	if res.CopiedFiles != 0 {
		t.Fatalf("second pass copied %d files, want 0 (idempotent)", res.CopiedFiles)
	}
	b, _ := os.ReadFile(localCopy)
	if string(b) != "LOCAL-EDIT" {
		t.Fatalf("second pass overwrote an existing local transcript: %q", b)
	}
}

func TestReconcileLeavesNativeAndUnknownDirsAlone(t *testing.T) {
	root := t.TempDir()
	macHome, hubHome := "/Users/andrew.sulistio", "/home/andrew"
	// A native Mac session (already local) and a stranger's path not in the fleet.
	seedSession(t, root, "-Users-andrew-sulistio-dev", "n1", macHome+"/dev")
	seedSession(t, root, "-Users-someone-else-proj", "x1", "/Users/someone/else/proj")

	dirsBefore, _ := os.ReadDir(root)
	res, err := Reconcile(ReconcileOptions{Root: root, LocalHome: macHome, Homes: []string{macHome, hubHome},
		Register: func(p ...string) ([]string, error) { return p, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mapped != 0 || res.CopiedFiles != 0 {
		t.Fatalf("native/unknown dirs must not map: %+v", res)
	}
	dirsAfter, _ := os.ReadDir(root)
	if len(dirsAfter) != len(dirsBefore) {
		t.Fatalf("reconcile created dirs for native/unknown sessions: %d → %d", len(dirsBefore), len(dirsAfter))
	}
}

func TestReconcileDryRunCopiesNothing(t *testing.T) {
	root := t.TempDir()
	macHome, hubHome := "/Users/andrew.sulistio", "/home/andrew"
	seedSession(t, root, "-home-andrew-dev", "s1", hubHome+"/dev")

	res, err := Reconcile(ReconcileOptions{Root: root, LocalHome: macHome, Homes: []string{macHome, hubHome}, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mapped != 1 || res.CopiedFiles != 1 {
		t.Fatalf("dry run should REPORT 1 mapped/1 copyable, got %+v", res)
	}
	if exists(filepath.Join(root, "-Users-andrew-sulistio-dev", "s1.jsonl")) {
		t.Fatal("dry run actually wrote a file")
	}
}
