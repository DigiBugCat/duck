package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// writeJSON writes v as ~/.claude.json under home.
func writeJSON(t *testing.T, home string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// readProjects reloads the projects map from disk.
func readProjects(t *testing.T, home string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	out := map[string]json.RawMessage{}
	if raw, ok := top["projects"]; ok {
		json.Unmarshal(raw, &out)
	}
	return out
}

func TestRegisterAddsMissingAndPreservesEverythingElse(t *testing.T) {
	home := t.TempDir()
	// Seed a realistic file: an auth-bearing top-level key and an existing
	// project with a distinctive marker we must NOT clobber.
	writeJSON(t, home, map[string]any{
		"oauthAccount": map[string]any{"token": "SECRET-KEEP-ME"},
		"projects": map[string]any{
			"/home/andrew/dev": map[string]any{"marker": "ORIGINAL", "hasTrustDialogAccepted": true},
		},
	})
	r := NewRegistry(home)

	added, err := r.Register("/Users/me/dev", "/home/andrew/dev") // one new, one existing
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0] != "/Users/me/dev" {
		t.Fatalf("added = %v, want just the new path", added)
	}

	projects := readProjects(t, home)
	if _, ok := projects["/Users/me/dev"]; !ok {
		t.Fatal("new path was not registered")
	}
	// Existing entry preserved verbatim.
	var existing struct{ Marker string }
	json.Unmarshal(projects["/home/andrew/dev"], &existing)
	if existing.Marker != "ORIGINAL" {
		t.Fatalf("existing project entry was clobbered: %s", projects["/home/andrew/dev"])
	}
	// Auth-bearing top-level key preserved.
	data, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	var top map[string]json.RawMessage
	json.Unmarshal(data, &top)
	if _, ok := top["oauthAccount"]; !ok {
		t.Fatal("top-level oauthAccount key was dropped")
	}
}

func TestRegisterIsIdempotentAndSkipsWriteWhenNothingNew(t *testing.T) {
	home := t.TempDir()
	writeJSON(t, home, map[string]any{"projects": map[string]any{"/Users/me/x": map[string]any{}}})
	r := NewRegistry(home)

	added, err := r.Register("/Users/me/x")
	if err != nil {
		t.Fatal(err)
	}
	if added != nil {
		t.Fatalf("re-registering an existing path must add nothing, got %v", added)
	}
}

// TestRegisterConcurrentNoLostUpdates hammers Register from many goroutines each
// adding a distinct path; with the advisory lock + mtime CAS none may clobber
// another's addition, and the file must stay valid JSON with a preserved
// pre-existing key.
func TestRegisterConcurrentNoLostUpdates(t *testing.T) {
	home := t.TempDir()
	writeJSON(t, home, map[string]any{
		"oauthAccount": map[string]any{"token": "KEEP"},
		"projects":     map[string]any{"/seed": map[string]any{}},
	})
	r := NewRegistry(home)

	const n = 24
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := r.Register(fmt.Sprintf("/p/%d", i)); err != nil {
				t.Errorf("Register: %v", err)
			}
		}(i)
	}
	wg.Wait()

	projects := readProjects(t, home)
	for i := 0; i < n; i++ {
		if _, ok := projects[fmt.Sprintf("/p/%d", i)]; !ok {
			t.Fatalf("lost update: /p/%d missing after concurrent Register", i)
		}
	}
	if _, ok := projects["/seed"]; !ok {
		t.Fatal("pre-existing /seed entry clobbered")
	}
	// Auth key survived.
	data, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatalf("file is not valid JSON after concurrent writes: %v", err)
	}
	if _, ok := top["oauthAccount"]; !ok {
		t.Fatal("oauthAccount dropped under concurrency")
	}
}

func TestRegisterOnMissingFileCreatesIt(t *testing.T) {
	home := t.TempDir() // no ~/.claude.json yet
	r := NewRegistry(home)
	added, err := r.Register("/Users/me/new")
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 {
		t.Fatalf("added = %v", added)
	}
	if ok, _ := r.Registered("/Users/me/new"); !ok {
		t.Fatal("path not registered after Register on a missing file")
	}
}

// TestEnsureMCPServerAddsPreservesAndIsIdempotent adds a server, leaves an
// existing one and top-level auth verbatim, and refuses to overwrite.
func TestEnsureMCPServerAddsPreservesAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	writeJSON(t, home, map[string]any{
		"oauthAccount": map[string]any{"token": "SECRET-KEEP-ME"},
		"mcpServers": map[string]any{
			"other": map[string]any{"command": "keepme", "marker": "ORIGINAL"},
		},
		"projects": map[string]any{"/home/andrew/dev": map[string]any{"marker": "P"}},
	})
	r := NewRegistry(home)

	added, err := r.EnsureMCPServer("duck-agents", map[string]any{"command": "duck", "args": []any{"channel", "serve"}})
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("first EnsureMCPServer should report added")
	}

	data, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatalf("file not valid JSON: %v", err)
	}
	// New server present with our args.
	var servers map[string]json.RawMessage
	json.Unmarshal(top["mcpServers"], &servers)
	var duck struct {
		Command string
		Args    []string
	}
	json.Unmarshal(servers["duck-agents"], &duck)
	if duck.Command != "duck" || len(duck.Args) != 2 || duck.Args[0] != "channel" {
		t.Fatalf("duck-agents entry wrong: %s", servers["duck-agents"])
	}
	// Existing server preserved verbatim.
	var other struct{ Marker string }
	json.Unmarshal(servers["other"], &other)
	if other.Marker != "ORIGINAL" {
		t.Fatalf("existing mcpServer clobbered: %s", servers["other"])
	}
	// Auth + projects preserved.
	if _, ok := top["oauthAccount"]; !ok {
		t.Fatal("oauthAccount dropped")
	}
	if _, ok := top["projects"]; !ok {
		t.Fatal("projects dropped")
	}

	// Idempotent: a second call adds nothing and never overwrites.
	added, err = r.EnsureMCPServer("duck-agents", map[string]any{"command": "DIFFERENT"})
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Fatal("second EnsureMCPServer must not re-add")
	}
	data, _ = os.ReadFile(filepath.Join(home, ".claude.json"))
	json.Unmarshal(data, &top)
	json.Unmarshal(top["mcpServers"], &servers)
	json.Unmarshal(servers["duck-agents"], &duck)
	if duck.Command != "duck" {
		t.Fatalf("existing entry was overwritten: %s", servers["duck-agents"])
	}
}

// TestEnsureMCPServerOnMissingFile creates the map from scratch.
func TestEnsureMCPServerOnMissingFile(t *testing.T) {
	home := t.TempDir()
	r := NewRegistry(home)
	added, err := r.EnsureMCPServer("duck-agents", map[string]any{"command": "duck"})
	if err != nil || !added {
		t.Fatalf("add on missing file: added=%v err=%v", added, err)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if _, ok := top["mcpServers"]; !ok {
		t.Fatal("mcpServers not created")
	}
}
