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

	"github.com/DigiBugCat/duck/internal/channel"
	"github.com/DigiBugCat/duck/internal/panel"
	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/spf13/cobra"
)

var (
	spawnName   string
	spawnTab    string
	spawnPrompt string
)

var spawnCmd = &cobra.Command{
	Use:   "spawn [flags] [--] [cmd args...]",
	Short: "Launch an agent (codex/claude/anything) into the sidebar",
	Long: `Run a command as a new agent in the current duck session's sidebar. With no
command, spawns an interactive shell. Opens the sidebar if it isn't already.

Prints "spawned <name>\t<pane-id>"; the pane id is the stable handle for
channel send/tail/reply (it never collides, even when agents share a cwd or
label). With --prompt, delivers the first turn in the same call.

Examples:
  duck spawn codex                          # codex TUI as an agent
  duck spawn codex -p "fix the tests"       # spawn AND send the first turn
  duck spawn -- codex exec "fix the tests"
  duck spawn -n build -- cargo watch -x test
  duck spawn                                # plain shell agent`,
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
		bin, err := os.Executable()
		if err != nil {
			bin = "duck"
		}
		// Open BEFORE spawn: Spawn selects the newcomer into the viewport slot,
		// which must exist.
		if err := panel.Open(run, outer, comp, bin); err != nil {
			return err
		}
		args = withCodexFullAccess(args)
		args = withCodexNotify(args)
		args = withCodexSessionHook(args)
		args = withCodexHookTrust(args)
		quoted := make([]string, len(args))
		for i, a := range args {
			quoted[i] = paths.Quote(a)
		}
		line := strings.Join(quoted, " ")
		name := spawnName
		kind := spawnTab
		if name == "" {
			// An OMITTED name defaults to the command, but made UNIQUE: bare
			// `duck spawn codex` × N otherwise stamps N agents all named "codex"
			// — the collision that makes a fan-out unaddressable by name (the
			// pane id is always the unambiguous handle, but the label should not
			// actively mislead). An EXPLICIT -n is honored verbatim: routines and
			// the manager depend on deterministic, predictable names.
			base := "shell"
			if len(args) > 0 {
				base = filepath.Base(args[0])
			}
			name = uniqueAgentName(run, outer, base)
		}
		if kind == "" {
			// Default tab by shape: a bare `duck spawn` is a shell; anything
			// running a command is an agent. --tab overrides (and mints new
			// tabs on the fly — any name becomes a tab while windows carry it).
			kind = panel.KindAgent
			if len(args) == 0 {
				kind = panel.KindShell
			}
		}
		paneID, err := panel.Spawn(run, outer, name, dir, line, kind)
		if err != nil {
			return err
		}
		// The pane id is the HANDLE — the true identity (tmux-as-db), stable
		// through swaps and unambiguous when several agents share a cwd or label.
		// The name is a human alias; address the agent by either, but the id is
		// what never collides. Print both so a caller can grab whichever it needs.
		fmt.Printf("spawned %s\t%s\n", name, paneID)

		// One-call spawn+send: deliver the first turn now instead of a separate
		// `channel send`. SendWhenReady awaits the agent's composer first (a fresh
		// TUI eats keys in its first seconds). Best-effort — a send failure never
		// unspawns the agent; the pane is up and addressable regardless.
		if spawnPrompt != "" {
			ref := channel.AgentRef{Session: outer, Name: name, WindowID: paneID}
			if err := channel.SendWhenReady(run, ref, spawnPrompt); err != nil {
				fmt.Fprintf(os.Stderr, "spawned, but first turn not delivered: %v\n", err)
			}
		}
		return nil
	},
}

// uniqueAgentName returns base if no agent in outer already carries it, else the
// first free base-2, base-3, … A best-effort listing failure falls back to base
// (spawn must never be blocked by a naming nicety; the pane id stays unique
// regardless). Only used for OMITTED names — an explicit -n is never rewritten.
func uniqueAgentName(run panel.Runner, outer, base string) string {
	agents, err := panel.Agents(run, outer)
	if err != nil {
		return base
	}
	taken := map[string]bool{}
	for _, a := range agents {
		taken[a.Name] = true
	}
	if !taken[base] {
		return base
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if !taken[cand] {
			return cand
		}
	}
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

// withCodexNotify wires codex's end-of-turn notify hook to `duck channel
// notify`, which pins the pane's rollout from the payload's thread id —
// exact, instant channel attribution instead of cwd+time correlation.
// Skipped when the user configured their own notify (an explicit -c wins).
func withCodexNotify(args []string) []string {
	if len(args) == 0 || filepath.Base(args[0]) != "codex" {
		return args
	}
	for _, a := range args {
		if strings.HasPrefix(a, "notify=") {
			return args
		}
	}
	self, err := os.Executable()
	if err != nil {
		return args
	}
	at := 1
	if len(args) > 1 && (args[1] == "exec" || args[1] == "e" || args[1] == "review") {
		at = 2
	}
	out := append([]string{}, args[:at]...)
	out = append(out, "-c", fmt.Sprintf(`notify=[%q,"channel","notify"]`, self))
	return append(out, args[at:]...)
}

// withCodexSessionHook wires codex's SessionStart hook to `duck channel hook`,
// which binds the pane's EXACT session id + rollout at the first turn (payload
// carries both) — race-free even under fan-out, since each hook fires in its own
// pane's process. This is the precise fast-path over notify/matchRollout. Wired
// as an inline `-c hooks.SessionStart=[...]` override (verified: fires without
// touching config.toml). Requires the trust bypass (see withCodexHookTrust) or
// codex silently skips it. Skipped if the user wired their own hooks.
func withCodexSessionHook(args []string) []string {
	if len(args) == 0 || filepath.Base(args[0]) != "codex" {
		return args
	}
	for _, a := range args {
		if strings.HasPrefix(a, "hooks.") || strings.HasPrefix(a, "hooks=") {
			return args
		}
	}
	self, err := os.Executable()
	if err != nil {
		return args
	}
	at := 1
	if len(args) > 1 && (args[1] == "exec" || args[1] == "e" || args[1] == "review") {
		at = 2
	}
	out := append([]string{}, args[:at]...)
	// The hook command must be a single string codex runs via the shell; it reads
	// the payload from stdin (duck channel hook), so no arg is appended.
	hook := fmt.Sprintf(`hooks.SessionStart=[{hooks=[{type="command",command=%q}]}]`,
		self+" channel hook")
	out = append(out, "-c", hook)
	return append(out, args[at:]...)
}

// withCodexHookTrust injects --dangerously-bypass-hook-trust for codex spawns.
// VERIFIED load-bearing: without it, codex SILENTLY skips duck's SessionStart
// hook (no error, no binding — the "flaky no-fire" we chased). duck vets its own
// hook, so the bypass is safe here. The flag is GLOBAL (before any subcommand),
// so it goes right after `codex`.
func withCodexHookTrust(args []string) []string {
	if len(args) == 0 || filepath.Base(args[0]) != "codex" {
		return args
	}
	for _, a := range args {
		if a == "--dangerously-bypass-hook-trust" {
			return args
		}
	}
	out := append([]string{}, args[:1]...)
	out = append(out, "--dangerously-bypass-hook-trust")
	return append(out, args[1:]...)
}

func init() {
	spawnCmd.Flags().StringVarP(&spawnName, "name", "n", "", "agent label in the sidebar (default: command name; the printed pane id is the stable handle)")
	spawnCmd.Flags().StringVar(&spawnTab, "tab", "", "sidebar tab to file this under (default: agents, or shells for a bare spawn; new names create new tabs)")
	spawnCmd.Flags().StringVarP(&spawnPrompt, "prompt", "p", "", "first turn to deliver once the agent is ready (one-call spawn+send)")
	rootCmd.AddCommand(spawnCmd)
}
