package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DigiBugCat/duck/assets"
)

func TestEnsureAgentNotesFreshHome(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	ensureAgentNotes(home)
	got, err := os.ReadFile(filepath.Join(home, ".duck", "AGENT.md"))
	if err != nil || string(got) != assets.AgentNotes {
		t.Fatalf("AGENT.md not installed: %v", err)
	}
	md, err := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md"))
	if err != nil || !strings.Contains(string(md), agentNotesImport) {
		t.Fatalf("CLAUDE.md missing import: %q err=%v", md, err)
	}
}

func TestEnsureAgentNotesIdempotentAndPreserving(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	claudeMd := filepath.Join(home, ".claude", "CLAUDE.md")
	if err := os.MkdirAll(filepath.Dir(claudeMd), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudeMd, []byte("# my precious notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	ensureAgentNotes(home)
	ensureAgentNotes(home)
	md, _ := os.ReadFile(claudeMd)
	if !strings.HasPrefix(string(md), "# my precious notes") {
		t.Fatalf("existing content clobbered: %q", md)
	}
	if strings.Count(string(md), agentNotesImport) != 1 {
		t.Fatalf("import not exactly once: %q", md)
	}
}

func TestEnsureAgentNotesRefreshesStaleNotes(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	notes := filepath.Join(home, ".duck", "AGENT.md")
	if err := os.MkdirAll(filepath.Dir(notes), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notes, []byte("old version"), 0o644); err != nil {
		t.Fatal(err)
	}
	ensureAgentNotes(home)
	got, _ := os.ReadFile(notes)
	if string(got) != assets.AgentNotes {
		t.Fatalf("stale AGENT.md not refreshed")
	}
}

func TestEnsureAgentNotesHonorsConfigDir(t *testing.T) {
	home := t.TempDir()
	conf := filepath.Join(home, ".claude-custom")
	t.Setenv("CLAUDE_CONFIG_DIR", conf)
	ensureAgentNotes(home)
	md, err := os.ReadFile(filepath.Join(conf, "CLAUDE.md"))
	if err != nil || !strings.Contains(string(md), agentNotesImport) {
		t.Fatalf("import not in CLAUDE_CONFIG_DIR: %v", err)
	}
}
