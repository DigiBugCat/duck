// managerreg.go: hub-side registration of the duck-agents channel MCP server,
// and the durable-record stamp marking a workspace as manager-launched with
// channels. Registration must land where Claude actually reads its config — the
// HUB (the pane's shell runs on the hub; the manager line's `duck panel` runs
// hub-side, so this self-installs on every hub duck invocation via the
// PersistentPreRun hook). It writes the DEFAULT ~/.claude.json AND every
// ~/.claude-*/.claude.json profile dir that exists (glob), add-only,
// best-effort, swallowing errors like ensureAgentNotes does.
package command

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DigiBugCat/duck/internal/claude"
	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/DigiBugCat/duck/internal/workspaces"
)

// duckAgentsServer is the MCP server spec duck registers so the manager Claude
// can reach its sidebar agents over the `duck channel serve` fabric.
var duckAgentsServer = map[string]any{
	"command": "duck",
	"args":    []any{"channel", "serve"},
}

// ensureChannelRegistration registers "duck-agents" in the default Claude config
// (<home>/.claude.json) and in every profile config dir (<home>/.claude-*/.
// claude.json) that already exists. Add-only and best-effort: a missing file,
// read-only home, or malformed JSON in one location never blocks the others or
// the user's command. Called from the hub-side PersistentPreRun hook, so it runs
// where the config files live regardless of where the human typed `duck`.
func ensureChannelRegistration(home string) {
	if home == "" {
		return
	}
	// The DEFAULT config dir is <home> itself for ~/.claude.json (NewRegistry
	// appends ".claude.json"), honoring a relocated CLAUDE_CONFIG_DIR when set.
	if def := claudeConfigHome(); def != "" {
		_, _ = claude.NewRegistry(def).EnsureMCPServer("duck-agents", duckAgentsServer)
	}
	// Every profile dir ~/.claude-<who> holds its own .claude.json (the shell
	// wrapper's --ben/--will point CLAUDE_CONFIG_DIR there). Register into each so
	// a manager launched under any profile hears its agents. NewRegistry appends
	// ".claude.json" to the dir it is given, so pass the profile dir itself.
	matches, _ := filepath.Glob(filepath.Join(home, ".claude-*"))
	for _, dir := range matches {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		_, _ = claude.NewRegistry(dir).EnsureMCPServer("duck-agents", duckAgentsServer)
	}
}

// claudeConfigHome resolves the dir holding Claude's DEFAULT config
// (~/.claude.json), honoring CLAUDE_CONFIG_DIR the way command/agentdoc.go does.
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

// stampManagerLaunched records the freshly launched manager pane in tmux and
// marks the workspace record channel-aware whenever duck launched a manager WITH
// channel flags (i.e. not opted out via DUCK_NO_CHANNELS / explicit --channels).
// Best-effort: neither a tmux option nor ledger write may block the launch. The
// ledger write uses the same ssh seam as the names store (records live in the
// hub's Claude projects corpus), loading the existing record first so a
// re-stamp preserves Parent/Title/Persistent.
func stampManagerLaunched(w *wiring, tildeDir, name string, extraArgs []string) {
	if out, err := w.client.Run(fmt.Sprintf("tmux display-message -p -t %s '#{pane_id}'", paths.Quote(name))); err == nil {
		if pane := strings.TrimSpace(out); pane != "" {
			_, _ = w.client.Run(fmt.Sprintf("tmux set-option -t %s @duck_manager %s", paths.Quote(name), paths.Quote(pane)))
		}
	}
	if channelsWired(extraArgs) {
		return // manager launched without channel flags — nothing to stamp.
	}
	store := workspaces.NewStore(w.client)
	rec, ok, err := store.Load(tildeDir, name)
	if err != nil {
		return
	}
	if !ok {
		rec = workspaces.Record{Name: name, Dir: tildeDir}
	}
	if rec.Channels {
		return // already stamped; skip the write.
	}
	rec.Channels = true
	_ = store.Save(rec)
}
