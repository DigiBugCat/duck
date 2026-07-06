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

func TestEnsureExecutorNotes(t *testing.T) {
	home := t.TempDir()

	// Fresh install: file created with the managed block.
	ensureExecutorNotes(home)
	agentsMd := filepath.Join(home, ".codex", "AGENTS.md")
	cur, err := os.ReadFile(agentsMd)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cur), "duck:executor-notes begin") || !strings.Contains(string(cur), "you are an executor") {
		t.Fatalf("managed block missing:\n%s", cur)
	}

	// Idempotent: second run leaves it byte-identical.
	ensureExecutorNotes(home)
	again, _ := os.ReadFile(agentsMd)
	if string(again) != string(cur) {
		t.Fatal("second run changed the file")
	}

	// User content outside the markers survives a re-paste of a stale block.
	stale := "# my own codex notes\n\n" +
		executorNotesBegin + "\nOLD CONTENT\n" + executorNotesEnd + "\n\n# more of mine\n"
	if err := os.WriteFile(agentsMd, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	ensureExecutorNotes(home)
	got, _ := os.ReadFile(agentsMd)
	s := string(got)
	if !strings.Contains(s, "# my own codex notes") || !strings.Contains(s, "# more of mine") {
		t.Fatalf("user content lost:\n%s", s)
	}
	if strings.Contains(s, "OLD CONTENT") || !strings.Contains(s, "you are an executor") {
		t.Fatalf("stale block not replaced:\n%s", s)
	}
}
