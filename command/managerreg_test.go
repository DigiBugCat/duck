package command

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func hasServer(t *testing.T, dir, name string) bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".claude.json"))
	if err != nil {
		return false
	}
	var top struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	_, ok := top.MCPServers[name]
	return ok
}
func TestCleanupDuckAgentsRegistrationDefaultAndProfiles(t *testing.T) {
	home := t.TempDir()
	profile := filepath.Join(home, ".claude-ben")
	_ = os.MkdirAll(profile, 0755)
	seed := []byte(`{"mcpServers":{"duck-agents":{"command":"duck"},"other":{"command":"x"}},"keep":true}`)
	for _, d := range []string{home, profile} {
		if err := os.WriteFile(filepath.Join(d, ".claude.json"), seed, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CLAUDE_CONFIG_DIR", profile)
	cleanupDuckAgentsRegistration(home)
	cleanupDuckAgentsRegistration(home)
	for _, d := range []string{home, profile} {
		if hasServer(t, d, "duck-agents") {
			t.Errorf("retired server remains in %s", d)
		}
		if !hasServer(t, d, "other") {
			t.Errorf("unrelated server removed in %s", d)
		}
	}
}
func TestManagerLaunchCmdIsOneBatchedRoundtrip(t *testing.T) {
	got := managerLaunchCmd("foo", "claude --duck")
	want := `tmux send-keys -t 'foo' 'claude --duck' Enter && tmux set-option -t 'foo' @duck_manager "$(tmux display-message -p -t 'foo' '#{pane_id}')"`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
