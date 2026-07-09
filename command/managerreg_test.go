package command

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// hasDuckAgents reports whether the .claude.json at dir registers the
// duck-agents MCP server.
func hasDuckAgents(t *testing.T, dir string) bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".claude.json"))
	if err != nil {
		return false
	}
	var top struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatalf("bad json in %s: %v", dir, err)
	}
	_, ok := top.MCPServers["duck-agents"]
	return ok
}

func TestEnsureChannelRegistrationDefaultAndProfiles(t *testing.T) {
	home := t.TempDir()
	// A default config and one profile dir, both pre-existing (empty JSON).
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	benDir := filepath.Join(home, ".claude-ben")
	if err := os.MkdirAll(benDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(benDir, ".claude.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A non-dir match must be ignored, not crash.
	if err := os.WriteFile(filepath.Join(home, ".claude-stray"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}

	// claudeConfigHome resolves the DEFAULT config dir; point it at our temp home
	// (CLAUDE_CONFIG_DIR wins in claudeConfigHome).
	t.Setenv("CLAUDE_CONFIG_DIR", home)

	ensureChannelRegistration(home)

	if !hasDuckAgents(t, home) {
		t.Error("default ~/.claude.json missing duck-agents entry")
	}
	if !hasDuckAgents(t, benDir) {
		t.Error("profile ~/.claude-ben/.claude.json missing duck-agents entry")
	}
}

func TestEnsureChannelRegistrationIsAddOnlyAndBestEffort(t *testing.T) {
	home := t.TempDir()
	// No files exist at all: must not panic; NewRegistry writes a fresh file.
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	ensureChannelRegistration(home)
	if !hasDuckAgents(t, home) {
		t.Error("registration should create the default config and add the entry")
	}

	// A profile dir with a pre-existing UNRELATED server: registration is add-only
	// and must preserve it.
	benDir := filepath.Join(home, ".claude-will")
	if err := os.MkdirAll(benDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pre := `{"mcpServers":{"other":{"command":"x"}}}`
	if err := os.WriteFile(filepath.Join(benDir, ".claude.json"), []byte(pre), 0o644); err != nil {
		t.Fatal(err)
	}
	ensureChannelRegistration(home)

	data, _ := os.ReadFile(filepath.Join(benDir, ".claude.json"))
	var top struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	if _, ok := top.MCPServers["other"]; !ok {
		t.Error("pre-existing 'other' server was clobbered")
	}
	if _, ok := top.MCPServers["duck-agents"]; !ok {
		t.Error("duck-agents not added alongside existing server")
	}
}

// TestManagerLaunchCmdIsOneBatchedRoundtrip pins the latency batching: the
// manager launch (send-keys) and the @duck_manager pane stamp travel as ONE
// `&&`-chained remote command, with the pane id resolved remotely via
// $(tmux display-message …) instead of a separate roundtrip.
func TestManagerLaunchCmdIsOneBatchedRoundtrip(t *testing.T) {
	got := managerLaunchCmd("foo", "claude --duck")
	want := `tmux send-keys -t 'foo' 'claude --duck' Enter` +
		` && tmux set-option -t 'foo' @duck_manager "$(tmux display-message -p -t 'foo' '#{pane_id}')"`
	if got != want {
		t.Fatalf("managerLaunchCmd =\n  %q\nwant\n  %q", got, want)
	}
}
