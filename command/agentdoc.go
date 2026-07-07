// Agent-notes installation: every duck start ensures the agent-facing cheat
// sheet (assets/agent-notes.md) is present at ~/.duck/AGENT.md and imported
// from ~/.claude/CLAUDE.md, so any Claude launched inside a duck workspace
// carries duck's workspace-tool instructions in its system prompt. Same
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
	// Honor a relocated Claude config dir (CLAUDE_CONFIG_DIR) — the import
	// must land in the CLAUDE.md Claude actually loads.
	confDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if confDir == "" {
		confDir = filepath.Join(home, ".claude")
	}
	claudeMd := filepath.Join(confDir, "CLAUDE.md")
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

// Codex reads ~/.codex/AGENTS.md as standing instructions but has no import
// syntax, so the executor briefing is pasted IN FULL between these markers
// and re-pasted whenever the embedded copy changes. Anything the user keeps
// outside the markers is preserved verbatim.
const (
	executorNotesBegin = "<!-- duck:executor-notes begin (managed by duck; edits inside are overwritten) -->"
	executorNotesEnd   = "<!-- duck:executor-notes end -->"
)

// ensureExecutorNotes keeps the managed block in <home>/.codex/AGENTS.md in
// sync with assets.ExecutorNotes. Same contract as ensureAgentNotes: cheap,
// idempotent, errors swallowed.
func ensureExecutorNotes(home string) {
	if home == "" {
		return
	}
	block := executorNotesBegin + "\n" + assets.ExecutorNotes + executorNotesEnd + "\n"
	agentsMd := filepath.Join(home, ".codex", "AGENTS.md")
	cur, err := os.ReadFile(agentsMd)
	body := string(cur)
	if err == nil {
		if i := strings.Index(body, executorNotesBegin); i >= 0 {
			if j := strings.Index(body, executorNotesEnd); j > i {
				old := body[i : j+len(executorNotesEnd)+1]
				if old == block || body[i:j+len(executorNotesEnd)]+"\n" == block {
					return // already current
				}
				body = body[:i] + block + body[j+len(executorNotesEnd):]
				body = strings.TrimSuffix(body, "\n") + "\n"
				_ = os.WriteFile(agentsMd, []byte(body), 0o644)
				return
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(agentsMd), 0o755); err != nil {
		return
	}
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if body != "" {
		body += "\n"
	}
	_ = os.WriteFile(agentsMd, []byte(body+block), 0o644)
}
