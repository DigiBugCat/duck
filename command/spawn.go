// `duck spawn [-n name] [cmd...]` — launch an agent runner (codex, claude, a
// build, any command; no args = interactive shell) as a window of the current
// session's hidden companion, and make sure the sidebar (`duck panel`) is up
// so it appears in the list with the viewport showing it. The runner keeps
// running when the sidebar is closed and when you detach — it's a tmux window
// like any other.
package command

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DigiBugCat/duck/internal/panel"
	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/spf13/cobra"
)

var spawnName string

var spawnCmd = &cobra.Command{
	Use:   "spawn [flags] [--] [cmd args...]",
	Short: "Launch an agent (codex/claude/anything) into the sidebar",
	Long: `Run a command as a new agent in the current duck session's sidebar. With no
command, spawns an interactive shell. Opens the sidebar if it isn't already.

Examples:
  duck spawn codex              # codex TUI as an agent
  duck spawn -- codex exec "fix the tests"
  duck spawn -n build -- cargo watch -x test
  duck spawn                    # plain shell agent`,
	RunE: func(c *cobra.Command, args []string) error {
		run := panel.ExecRunner
		outer, dir, err := panelContext(run)
		if err != nil {
			return err
		}
		comp, err := panel.EnsureCompanion(run, outer, dir)
		if err != nil {
			return err
		}
		args = withCodexFullAccess(args)
		quoted := make([]string, len(args))
		for i, a := range args {
			quoted[i] = paths.Quote(a)
		}
		line := strings.Join(quoted, " ")
		name := spawnName
		if name == "" {
			if len(args) > 0 {
				name = filepath.Base(args[0])
			} else {
				name = "shell"
			}
		}
		if _, err := panel.Spawn(run, comp, name, dir, line); err != nil {
			return err
		}
		bin, err := os.Executable()
		if err != nil {
			bin = "duck"
		}
		if err := panel.Open(run, outer, comp, bin); err != nil {
			return err
		}
		fmt.Printf("spawned %s\n", name)
		return nil
	},
}

// withCodexFullAccess makes spawned codex agents run with full access by
// default: sidebar agents exist to work autonomously under supervision (the
// channel layer + viewport ARE the oversight), so per-command approval prompts
// just stall them. Only applies when the command is codex AND the user gave no
// approval/sandbox preference of their own — an explicit flag always wins.
func withCodexFullAccess(args []string) []string {
	if len(args) == 0 || filepath.Base(args[0]) != "codex" {
		return args
	}
	for _, a := range args {
		switch {
		case a == "-a", a == "-s", a == "--full-auto",
			strings.HasPrefix(a, "--ask-for-approval"), strings.HasPrefix(a, "--sandbox"),
			strings.HasPrefix(a, "--dangerously-bypass"):
			return args
		}
	}
	// Insert after the subcommand when one is given (`codex exec …` — the flag
	// belongs to the subcommand), else right after "codex".
	at := 1
	if len(args) > 1 && (args[1] == "exec" || args[1] == "e" || args[1] == "review") {
		at = 2
	}
	out := append([]string{}, args[:at]...)
	out = append(out, "--dangerously-bypass-approvals-and-sandbox")
	return append(out, args[at:]...)
}

func init() {
	spawnCmd.Flags().StringVarP(&spawnName, "name", "n", "", "agent label in the sidebar (default: command name)")
	rootCmd.AddCommand(spawnCmd)
}
