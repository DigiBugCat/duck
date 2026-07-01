package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
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
