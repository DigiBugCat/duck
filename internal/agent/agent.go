// Package agent is duck's shared codex-agent spawn pipeline: the argv injectors
// that wire a codex launch (full access, notify + SessionStart hooks, hook
// trust) and the Spawn/Resume/Fork orchestration over the panel + channel.
//
// It exists so the CLI (command/spawn.go) and the MCP tool surface
// (internal/channel serve) share ONE pipeline — a codex agent spawned either way
// gets the exact same wiring, so it is bound + attributed identically. Neither
// caller may reimplement the injectors; that divergence is what let an MCP spawn
// skip the notify hook in the original design.
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DigiBugCat/duck/internal/channel"
	"github.com/DigiBugCat/duck/internal/panel"
	"github.com/DigiBugCat/duck/internal/paths"
)

// selfBin resolves this duck binary for wiring hooks/notify back to it. Falls
// back to "duck" on PATH — best-effort, never blocks a spawn.
func selfBin() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return "duck"
}

// isCodex reports whether argv launches codex (the injectors only touch codex).
func isCodex(args []string) bool {
	return len(args) > 0 && filepath.Base(args[0]) == "codex"
}

// codexInsertAt is where injected flags/-c overrides belong in a codex argv:
// after the subcommand when present (exec/resume/fork/review — the flag belongs
// to the subcommand), else right after "codex" (interactive TUI, no subcommand).
func codexInsertAt(args []string) int {
	if len(args) > 1 {
		switch args[1] {
		case "exec", "e", "review", "resume", "fork":
			return 2
		}
	}
	return 1
}

// WithFullAccess injects --dangerously-bypass-approvals-and-sandbox for codex
// spawns unless the user stated their own approval/sandbox preference. Sidebar
// agents run autonomously under supervision (channel + viewport are the
// oversight); per-command approval prompts just stall them.
func WithFullAccess(args []string) []string {
	if !isCodex(args) {
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
	at := codexInsertAt(args)
	out := append([]string{}, args[:at]...)
	out = append(out, "--dangerously-bypass-approvals-and-sandbox")
	return append(out, args[at:]...)
}

// WithNotify wires codex's end-of-turn notify hook to `duck channel notify` —
// the stable attribution floor (pins the rollout from the turn payload's thread
// id). Skipped when the user set their own notify.
func WithNotify(args []string) []string {
	if !isCodex(args) {
		return args
	}
	for _, a := range args {
		if strings.HasPrefix(a, "notify=") {
			return args
		}
	}
	at := codexInsertAt(args)
	out := append([]string{}, args[:at]...)
	out = append(out, "-c", fmt.Sprintf(`notify=[%q,"channel","notify"]`, selfBin()))
	return append(out, args[at:]...)
}

// WithSessionHook wires codex's SessionStart AND UserPromptSubmit hooks to `duck
// channel hook`. Two events, two jobs:
//   - SessionStart binds the pane's exact session id + rollout at the first turn
//     (race-free even under fan-out).
//   - UserPromptSubmit stamps @duck_last_prompt on EVERY submit (turn_id) — the
//     ground-truth "the prompt actually submitted" signal Send confirms against
//     (SessionStart fires only once; UserPromptSubmit fires per turn).
//
// Both go in one inline `-c hooks=...` TOML table. Requires WithHookTrust or
// codex silently skips them. Skipped when the user wired their own hooks.
func WithSessionHook(args []string) []string {
	if !isCodex(args) {
		return args
	}
	for _, a := range args {
		if strings.HasPrefix(a, "hooks.") || strings.HasPrefix(a, "hooks=") {
			return args
		}
	}
	at := codexInsertAt(args)
	out := append([]string{}, args[:at]...)
	self := selfBin() + " channel hook"
	hook := fmt.Sprintf(
		`hooks={SessionStart=[{hooks=[{type="command",command=%[1]q}]}],UserPromptSubmit=[{hooks=[{type="command",command=%[1]q}]}]}`,
		self)
	out = append(out, "-c", hook)
	return append(out, args[at:]...)
}

// WithHookTrust injects --dangerously-bypass-hook-trust (a GLOBAL flag, before
// any subcommand). VERIFIED load-bearing: without it codex SILENTLY skips duck's
// SessionStart hook. duck vets its own hook, so the bypass is safe here.
func WithHookTrust(args []string) []string {
	if !isCodex(args) {
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

// Wire applies every codex injector in order. This is THE pipeline both callers
// use — a codex agent spawned via the CLI or an MCP tool is wired identically.
func Wire(args []string) []string {
	args = WithFullAccess(args)
	args = WithNotify(args)
	args = WithSessionHook(args)
	args = WithHookTrust(args)
	return args
}

// UniqueName returns base if no agent in outer already carries it, else the
// first free base-2, base-3, … so bare `spawn codex` × N don't all become
// "codex". A listing failure falls back to base (never block a spawn).
func UniqueName(run panel.Runner, outer, base string) string {
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

// Spec is a request to launch a sidebar agent.
type Spec struct {
	Args   []string // the command argv (e.g. ["codex"] or a Resume/Fork build)
	Name   string   // roster label; "" → derived + made unique
	Tab    string   // roster tab; "" → agents (or shells for a bare arg-less spawn)
	Prompt string   // optional first turn, delivered after the composer is ready
	Model  string   // optional model alias (see model.go); "" → codex config default
	Effort string   // optional reasoning effort (low|medium|high); "" → codex default
}

// Result is the receipt of a launch: the pane id (instant, stable handle) and,
// when known, the codex session id (bound at first turn by the SessionStart
// hook — nil until then).
type Result struct {
	Name      string // the resolved roster label (after unique-name derivation)
	PaneID    string
	SessionID string // "" until the SessionStart hook has fired (first turn)
}

// Launch runs the full spawn pipeline: ensure the companion + panel, wire the
// codex argv, spawn the pane, optionally deliver the first turn, and read back
// the session id the hook stamped (best-effort — may be "" if the agent hasn't
// taken its first turn yet). Shared by the CLI and the MCP spawn tool.
func Launch(run panel.Runner, outer, dir, duckBin string, spec Spec) (Result, error) {
	comp, err := panel.EnsureCompanion(run, outer, dir)
	if err != nil {
		return Result{}, err
	}
	// Open BEFORE spawn: Spawn selects the newcomer into the viewport slot.
	if err := panel.Open(run, outer, comp, duckBin); err != nil {
		return Result{}, err
	}
	// Model/effort are codex-only concepts (see defaultArgs): setting one with no
	// command means "a codex agent on this model", not a bare shell. Runs before
	// the name/kind/injector logic below, all of which key off spec.Args.
	spec.Args = defaultArgs(spec.Args, spec.Model, spec.Effort)
	// Model/effort selection is applied BEFORE Wire so its injected -c/--profile
	// flags sit at the same subcommand insertion point as the rest. An unknown
	// alias fails the spawn loudly rather than launching the wrong model.
	selected, err := WithModel(spec.Args, spec.Model, spec.Effort)
	if err != nil {
		return Result{}, err
	}
	args := Wire(selected)
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = paths.Quote(a)
	}
	line := strings.Join(quoted, " ")

	name := spec.Name
	if name == "" {
		base := "shell"
		if len(spec.Args) > 0 {
			base = filepath.Base(spec.Args[0])
		}
		name = UniqueName(run, outer, base)
	}
	kind := spec.Tab
	if kind == "" {
		kind = panel.KindAgent
		if len(spec.Args) == 0 {
			kind = panel.KindShell
		}
	}
	paneID, err := panel.Spawn(run, outer, name, dir, line, kind)
	if err != nil {
		return Result{}, err
	}
	res := Result{Name: name, PaneID: paneID}
	if spec.Prompt != "" {
		ref := channel.AgentRef{Session: outer, Name: name, WindowID: paneID}
		// Best-effort: a send failure never unspawns the agent.
		_ = channel.SendWhenReady(run, ref, spec.Prompt)
	}
	// Read back the session id the SessionStart hook stamped (present once the
	// first turn fired — e.g. after a delivered Prompt). Best-effort.
	if out, err := run("show-options", "-p", "-t", paneID, "-v", panel.SessionOption); err == nil {
		res.SessionID = strings.TrimSpace(out)
	}
	return res, nil
}

// ResumeArgs / ForkArgs build the codex argv for continuing or branching a
// session by id. Both flow through Launch → Wire like any spawn.
func ResumeArgs(sessionID string) []string { return []string{"codex", "resume", sessionID} }
func ForkArgs(sessionID string) []string   { return []string{"codex", "fork", sessionID} }
