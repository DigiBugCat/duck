// Agent-notes installation: every duck start ensures the agent-facing cheat
// sheet (assets/agent-notes.md) is present at ~/.duck/AGENT.md and imported
// from ~/.claude/CLAUDE.md, so any Claude launched inside a duck workspace
// carries duck's artifact/viewport instructions in its system prompt. Same
// spirit as the auto-updater hook: cheap, idempotent, never delays or fails
// the user's command.
package command

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/DigiBugCat/duck/assets"
)

const agentNotesImport = "@~/.duck/AGENT.md"

// agentNotesMarker brackets the managed line in ~/.claude/CLAUDE.md so it can
// be found (and, if duck ever wants, rewritten) without disturbing the rest
// of the file.
const agentNotesBlock = "<!-- duck:agent-notes (managed by duck) -->\n" + agentNotesImport + "\n"

// ensureAgentNotes writes the embedded notes to <home>/.duck/AGENT.md when
// the content differs, and appends the managed import block to
// <home>/.claude/CLAUDE.md when no duck:agent-notes marker is present.
// Errors are swallowed: a read-only home must never break a duck command.
func ensureAgentNotes(home string) {
	if home == "" {
		return
	}
	notes := filepath.Join(home, ".duck", "AGENT.md")
	if cur, err := os.ReadFile(notes); err != nil || string(cur) != assets.AgentNotes {
		if err := os.MkdirAll(filepath.Dir(notes), 0o755); err != nil {
			return
		}
		if err := os.WriteFile(notes, []byte(assets.AgentNotes), 0o644); err != nil {
			return
		}
	}
	claudeMd := filepath.Join(home, ".claude", "CLAUDE.md")
	cur, err := os.ReadFile(claudeMd)
	if err == nil && strings.Contains(string(cur), "duck:agent-notes") {
		return
	}
	if err := os.MkdirAll(filepath.Dir(claudeMd), 0o755); err != nil {
		return
	}
	body := string(cur)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if body != "" {
		body += "\n"
	}
	_ = os.WriteFile(claudeMd, []byte(body+agentNotesBlock), 0o644)
}
