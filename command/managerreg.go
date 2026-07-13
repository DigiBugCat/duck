package command

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/DigiBugCat/duck/internal/claude"
	"github.com/DigiBugCat/duck/internal/paths"
)

// cleanupDuckAgentsRegistration removes the retired product registration from
// every Claude profile. It is idempotent and preserves all unrelated registry data.
func cleanupDuckAgentsRegistration(home string) {
	if home == "" {
		return
	}
	// Always clean the default registry, even when this invocation selected a
	// named CLAUDE_CONFIG_DIR profile.
	_, _ = claude.NewRegistry(home).RemoveMCPServer("duck-agents")
	if profile := claudeConfigHome(); profile != "" && profile != home {
		_, _ = claude.NewRegistry(profile).RemoveMCPServer("duck-agents")
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".claude-*"))
	for _, dir := range matches {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			_, _ = claude.NewRegistry(dir).RemoveMCPServer("duck-agents")
		}
	}
}

func claudeConfigHome() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func managerLaunchCmd(name, line string) string {
	q := paths.Quote(name)
	return fmt.Sprintf("tmux send-keys -t %s %s Enter && tmux set-option -t %s @duck_manager \"$(tmux display-message -p -t %s '#{pane_id}')\"", q, paths.Quote(line), q, q)
}
