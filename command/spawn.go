// `duck spawn [-n name] [cmd...]` — launch an agent runner (codex, claude, a
// build, any command; no args = interactive shell) as a pane of the current
// session: the first agent splits the active window, later ones get their own
// background windows. The runner keeps running when you detach — it's a tmux
// pane like any other, and the status bar's window list is the roster.
package command

import (
	"fmt"

	agentpkg "github.com/DigiBugCat/duck/internal/agent"
	"github.com/DigiBugCat/duck/internal/tmuxdb"
	"github.com/spf13/cobra"
)

// workspaceContext resolves the enclosing tmux session and pane cwd, erroring
// with a hint when not inside tmux.
func workspaceContext(run tmuxdb.Runner) (outer, dir string, err error) {
	if !tmuxdb.InsideTmux() {
		return "", "", fmt.Errorf("duck spawn only works inside a tmux session — run `duck` first")
	}
	if outer, err = tmuxdb.CurrentSession(run); err != nil {
		return "", "", err
	}
	dir, err = tmuxdb.CurrentPanePath(run)
	return outer, dir, err
}

var (
	spawnName   string
	spawnTab    string
	spawnPrompt string
	spawnResume string
	spawnFork   string
	spawnModel  string
	spawnEffort string
)

var spawnCmd = &cobra.Command{
	Use:   "spawn [flags] [--] [cmd args...]",
	Short: "Launch an agent (codex/claude/anything) into this workspace",
	Long: `Run a command as a new agent pane of the current duck session. With no
command, spawns an interactive shell. The first agent splits the active
window; later ones get background windows (see the status bar's window list).

Prints "spawned <name>\t<pane-id>"; the pane id is the stable handle for
channel send/tail/reply (it never collides, even when agents share a cwd or
label). With --prompt, delivers the first turn in the same call.

A codex agent's session id (printed on its channel events, stamped @duck_session)
is a durable handle: --resume <id> continues that exact conversation; --fork <id>
branches a NEW session that inherits its context (cheap fan-out — prime one, fork
many). Both bind + attribute like any spawn.

Examples:
  duck spawn codex                          # codex TUI as an agent
  duck spawn codex -p "fix the tests"       # spawn AND send the first turn
  duck spawn --resume <id> -p "keep going"  # continue an existing codex session
  duck spawn --fork <id> -p "try approach B"# branch a session, inherit its context
  duck spawn -- codex exec "fix the tests"
  duck spawn -n build -- cargo watch -x test
  duck spawn                                # plain shell agent`,
	RunE: func(c *cobra.Command, args []string) error {
		run := tmuxdb.ExecRunner
		outer, dir, err := workspaceContext(run)
		if err != nil {
			return err
		}
		// --resume/--fork are shorthands that BUILD the codex argv: resume a codex
		// session by id (same conversation, same session id — a durable handle) or
		// fork it (a new session that inherits the parent's context, the cheap
		// fan-out primitive). Both flow through the shared agent.Launch pipeline,
		// so a resumed/forked agent is wired + bound + attributed like any spawn.
		if spawnResume != "" && spawnFork != "" {
			return fmt.Errorf("--resume and --fork are mutually exclusive")
		}
		if spawnResume != "" {
			args = agentpkg.ResumeArgs(spawnResume)
		} else if spawnFork != "" {
			args = agentpkg.ForkArgs(spawnFork)
		}
		res, err := agentpkg.Launch(run, outer, dir, agentpkg.Spec{
			Args: args, Name: spawnName, Tab: spawnTab, Prompt: spawnPrompt,
			Model: spawnModel, Effort: spawnEffort,
		})
		if err != nil {
			return err
		}
		// The pane id is the HANDLE — the true identity (tmux-as-db), stable
		// through swaps and unambiguous when several agents share a cwd or label.
		// Print it; add the session id once the hook has bound it (first turn).
		if res.SessionID != "" {
			fmt.Printf("spawned %s\t%s\t%s\n", res.Name, res.PaneID, res.SessionID)
		} else {
			fmt.Printf("spawned %s\t%s\n", res.Name, res.PaneID)
		}
		return nil
	},
}

func init() {
	spawnCmd.Flags().StringVarP(&spawnName, "name", "n", "", "agent label (default: command name; the printed pane id is the stable handle)")
	spawnCmd.Flags().StringVar(&spawnTab, "tab", "", "agent group stamp (default: agents, or shells for a bare spawn)")
	spawnCmd.Flags().StringVarP(&spawnPrompt, "prompt", "p", "", "first turn to deliver once the agent is ready (one-call spawn+send)")
	spawnCmd.Flags().StringVar(&spawnResume, "resume", "", "resume a codex session by id — same conversation, same session id (a durable handle)")
	spawnCmd.Flags().StringVar(&spawnFork, "fork", "", "fork a codex session by id — a new session that inherits the parent's context (cheap fan-out)")
	spawnCmd.Flags().StringVarP(&spawnModel, "model", "m", "", "model for a codex agent (e.g. gpt-5.4, gpt-5.4-mini); default = codex config default (gpt-5.5)")
	spawnCmd.Flags().StringVar(&spawnEffort, "effort", "", "reasoning effort for a codex agent (low|medium|high); default = codex config default")
	rootCmd.AddCommand(spawnCmd)
}
