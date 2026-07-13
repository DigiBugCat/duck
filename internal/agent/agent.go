// Package agent owns duck's transport-neutral spawn pipeline: Codex launch
// defaults plus Spawn, Resume, and Fork orchestration over tmuxdb.
package agent

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/DigiBugCat/duck/internal/tmuxdb"
)

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

// WithFullAccess injects --dangerously-bypass-approvals-and-sandbox for Codex
// spawns unless the user stated their own approval or sandbox preference.
// Spawned processes are operator-visible tmux panes; interactive approval
// prompts would otherwise leave unattended jobs stalled.
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

// Wire applies transport-neutral codex launch defaults.
func Wire(args []string) []string { return WithFullAccess(args) }

// UniqueName returns base if no agent in outer already carries it, else the
// first free base-2, base-3, … so bare `spawn codex` × N don't all become
// "codex". A listing failure falls back to base (never block a spawn).
func UniqueName(run tmuxdb.Runner, outer, base string) string {
	agents, err := tmuxdb.Agents(run, outer)
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

// Spec is a request to launch a workspace agent.
type Spec struct {
	Args   []string // the command argv (e.g. ["codex"] or a Resume/Fork build)
	Name   string   // agent label; "" → derived + made unique
	Tab    string   // agent group; "" → agents (or shells for a bare arg-less spawn)
	Prompt string   // optional first turn, delivered after the composer is ready
	Model  string   // optional model alias (see model.go); "" → codex config default
	Effort string   // optional reasoning effort (low|medium|high); "" → codex default
}

// Result is the receipt of a launch.
type Result struct {
	Name   string // the resolved agent label (after unique-name derivation)
	PaneID string // instant, stable tmux handle
}

// Launch applies launch defaults, spawns the pane in the workspace session,
// and optionally delivers its first prompt.
func Launch(run tmuxdb.Runner, outer, dir string, spec Spec) (Result, error) {
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
		kind = tmuxdb.KindAgent
		if len(spec.Args) == 0 {
			kind = tmuxdb.KindShell
		}
	}
	paneID, err := tmuxdb.Spawn(run, outer, name, dir, line, kind)
	if err != nil {
		return Result{}, err
	}
	res := Result{Name: name, PaneID: paneID}
	if spec.Prompt != "" {
		if isCodex(args) {
			awaitComposer(run, paneID, 15*time.Second)
		}
		// Best-effort: a prompt delivery failure never destroys the spawned pane.
		_ = SendPrompt(run, paneID, spec.Prompt)
	}
	return res, nil
}

// awaitComposer waits until Codex displays its input prompt. Timeout is
// best-effort: a slow or differently themed client still receives the text.
func awaitComposer(run tmuxdb.Runner, paneID string, timeout time.Duration) {
	const every = 500 * time.Millisecond
	for elapsed := time.Duration(0); elapsed <= timeout; elapsed += every {
		if out, err := run("capture-pane", "-p", "-t", paneID); err == nil && strings.Contains(out, "›") {
			return
		}
		time.Sleep(every)
	}
}

// SendPrompt delivers text to a pane without rollout or session hooks.
// The short delay separates the literal paste from Enter for terminal UIs.
func SendPrompt(run tmuxdb.Runner, paneID, prompt string) error {
	if _, err := run("send-keys", "-t", paneID, "-l", "--", prompt); err != nil {
		return err
	}
	beat := 250 * time.Millisecond
	if len(prompt) > 256 {
		beat = 750 * time.Millisecond
	}
	time.Sleep(beat)
	_, err := run("send-keys", "-t", paneID, "Enter")
	return err
}

// ResumeArgs / ForkArgs build the codex argv for continuing or branching a
// session by id. Both flow through Launch → Wire like any spawn.
func ResumeArgs(sessionID string) []string { return []string{"codex", "resume", sessionID} }
func ForkArgs(sessionID string) []string   { return []string{"codex", "fork", sessionID} }
