// Package tmuxdb holds duck's tmux-is-the-database primitives: the exec
// Runner seam, session/pane context helpers, and the pane user options that
// carry each agent's identity. Agents are ordinary panes of the workspace
// session itself — the first one splits the current window, later ones get
// background windows — addressed by pane id and discovered by their stamps.
// There is no sidebar, no hidden companion session, and no asserted geometry:
// switching between agents is native tmux (select-window / last-window).
//
// Everything here drives the LOCAL tmux server via exec (it only makes sense
// on the machine where the session lives — the hub, when attached through
// duck). The Runner seam is injectable so tests record the tmux argv instead
// of needing a live server.
package tmuxdb

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DigiBugCat/duck/internal/paths"
)

// Runner executes one local tmux invocation (argv AFTER the "tmux") and
// returns its combined output. Production is ExecRunner; tests fake it.
type Runner func(args ...string) (string, error)

// ExecRunner runs the local tmux binary.
func ExecRunner(args ...string) (string, error) {
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// InsideTmux reports whether this process is running inside a tmux pane.
func InsideTmux() bool { return os.Getenv("TMUX") != "" }

// SocketPath returns the tmux server socket this process's pane belongs to
// ($TMUX is "socket-path,pid,pane-index"). Kept exported for callers that
// need to address this exact server. Empty when not in tmux.
func SocketPath() string {
	env := os.Getenv("TMUX")
	if i := strings.IndexByte(env, ','); i >= 0 {
		return env[:i]
	}
	return env
}

// PanelOfOption marks a companion session from the RETIRED sidebar design.
// No new companions are created; the constant survives so listings keep
// hiding stale ones on live workspaces until `duck clean` reaps them.
const PanelOfOption = "@duck_panel_of"

// Pane user options carrying each agent's identity (they travel with the
// pane wherever it sits):
const (
	NameOption      = "@duck_name"        // agent label
	kindOption      = "@duck_kind"        // agent grouping (see Kinds)
	SpawnedAtOption = "@duck_spawned_at"  // unix epoch of spawn (channel pairing)
	RolloutOption   = "@duck_rollout"     // cached codex rollout path
	SessionOption   = "@duck_session"     // codex session id (durable resume/fork handle)
	PromptOption    = "@duck_last_prompt" // codex turn id of the last submitted prompt (Send submit-confirm)
	CmdOption       = "@duck_cmd"         // spawn cmdline (channel pairing eligibility)
	anchorOption    = "@duck_anchor"      // legacy: the retired lot's keep-alive pane (skip on reads)
)

// Kinds group agents, stored per pane in kindOption.
const (
	KindAgent = "agents" // runners you supervise (default)
	KindShell = "shells" // plain interactive shells
)

// normalizeKind maps stamps to kind names: empty → agents, singular legacy
// stamps → their group.
func normalizeKind(k string) string {
	switch k {
	case "", "agent":
		return KindAgent
	case "shell":
		return KindShell
	}
	return k
}

// legacyCompanion is the retired sidebar design's hidden session name for a
// workspace. Agents reads still sweep it so live pre-teardown agents stay
// addressable until their workspace is recycled.
func legacyCompanion(outer string) string { return outer + "-agents" }

// CurrentSession returns the name of the tmux session this process's pane
// belongs to.
func CurrentSession(run Runner) (string, error) {
	out, err := run(displayArgs("#{session_name}")...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CurrentPanePath returns the current pane's working directory.
func CurrentPanePath(run Runner) (string, error) {
	out, err := run(displayArgs("#{pane_current_path}")...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// displayArgs targets display-message at THIS process's pane when known.
// Without -t, tmux answers for the most recent CLIENT's context — which is
// whatever terminal the human last touched, not necessarily where this
// process runs (a real mis-resolution we hit across sessions).
func displayArgs(format string) []string {
	if p := os.Getenv("TMUX_PANE"); p != "" {
		return []string{"display-message", "-p", "-t", p, format}
	}
	return []string{"display-message", "-p", format}
}

// SessionPath returns the working directory of a session's active pane —
// the cwd spawns targeting the session from OUTSIDE it should adopt.
func SessionPath(run Runner, outer string) (string, error) {
	out, err := run("display-message", "-p", "-t", outer+":", "#{pane_current_path}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Agent is one launched runner: a stamped pane, wherever it currently sits.
type Agent struct {
	PaneID  string // tmux pane id, e.g. "%42" — the dispatch key
	Name    string // agent label (@duck_name)
	Command string // pane_current_command, e.g. "codex", "node", "zsh"
	Kind    string // agent group (@duck_kind)
	RawKind string // literal @duck_kind (normalizeKind folds legacy stamps)
	Title   string // pane_title — agents like Claude Code write status here
}

// agentsFormat lists panes with their identity options. pane_title is free
// text, so it is LAST and the split is bounded.
const agentsFormat = "#{pane_id}\t#{" + NameOption + "}\t#{" + kindOption + "}\t#{" + anchorOption + "}\t#{pane_current_command}\t#{pane_title}"

// parseAgents turns list-panes output into Agents, skipping unstamped panes
// (the manager and whatever else lives in the session) and legacy anchors.
func parseAgents(out string) []Agent {
	var agents []Agent
	// TrimRight newlines ONLY (TrimSpace eats the last line's trailing tab).
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.SplitN(line, "\t", 6)
		if len(f) < 6 || strings.TrimSpace(f[1]) == "" || strings.TrimSpace(f[3]) != "" {
			continue
		}
		agents = append(agents, Agent{
			PaneID:  f[0],
			Name:    strings.TrimSpace(f[1]),
			Kind:    normalizeKind(strings.TrimSpace(f[2])),
			RawKind: strings.TrimSpace(f[2]),
			Command: f[4],
			Title:   f[5],
		})
	}
	// Creation order (numeric pane id): stable regardless of window layout.
	sort.SliceStable(agents, func(i, j int) bool {
		return paneNum(agents[i].PaneID) < paneNum(agents[j].PaneID)
	})
	return agents
}

// paneNum extracts the numeric part of a "%42" pane id (fallback: keep order).
func paneNum(id string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "%"))
	if err != nil {
		return int(^uint(0) >> 1)
	}
	return n
}

// Agents lists every stamped pane belonging to a workspace: the session's own
// panes, plus (legacy, read-only) any still parked in the retired sidebar's
// hidden companion session so pre-teardown agents stay addressable.
func Agents(run Runner, outer string) ([]Agent, error) {
	out, err := run("list-panes", "-s", "-t", outer, "-F", agentsFormat)
	if err != nil {
		return nil, err
	}
	all := parseAgents(out)
	if lout, lerr := run("list-panes", "-s", "-t", legacyCompanion(outer), "-F", agentsFormat); lerr == nil {
		all = append(all, parseAgents(lout)...)
	}
	return all, nil
}

// Spawn launches cmdline (or an interactive shell when empty) as a new
// stamped pane of the outer session itself. The FIRST agent splits the
// session's current window (below the manager, 40% tall); later agents get
// their own background windows named after them — native tmux windows the
// status bar lists and select-window / last-window flip between.
func Spawn(run Runner, outer, name, dir, cmdline, kind string) (paneID string, err error) {
	target := outer + ":"
	args := []string{"split-window", "-d", "-v", "-l", "40%", "-t", target, "-P", "-F", "#{pane_id}"}
	if out, cerr := run("list-panes", "-t", target, "-F", "#{pane_id}"); cerr == nil {
		if len(strings.Split(strings.TrimSpace(out), "\n")) > 1 {
			// Current window already holds a split — park this one in its own
			// background window instead of shredding the layout further.
			args = []string{"new-window", "-d", "-t", target, "-n", name, "-P", "-F", "#{pane_id}"}
		}
	}
	if dir != "" {
		args = append(args, "-c", dir)
	}
	if cmdline != "" {
		// Hold the pane open when the command exits non-zero or near-
		// instantly: otherwise `spawn test` (a builtin that exits in 1ms)
		// lives and dies before anyone can see it — reads as "nothing
		// happened".
		script := cmdline + `; ec=$?; if [ $ec -ne 0 ]; then printf '\n[exited %d — enter to close] ' "$ec"; read -r _; fi`
		args = append(args, "sh -c "+paths.Quote(script))
	}
	id, err := run(args...)
	if err != nil {
		return "", err
	}
	paneID = strings.TrimSpace(id)
	if kind == "" {
		kind = KindAgent
	}
	for _, opt := range [][2]string{
		{NameOption, name},
		{kindOption, kind},
		{SpawnedAtOption, strconv.FormatInt(time.Now().Unix(), 10)},
		{CmdOption, cmdline},
	} {
		_, _ = run("set-option", "-p", "-t", paneID, opt[0], opt[1])
	}
	return paneID, nil
}

// Kill terminates an agent pane. A background window dies with its last pane;
// a split just returns its rows to the manager — nothing needs healing.
func Kill(run Runner, paneID string) error {
	_, err := run("kill-pane", "-t", paneID)
	return err
}
