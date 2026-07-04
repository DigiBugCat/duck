// Package panel implements duck's agent sidebar: a right-hand column in the
// CURRENT tmux session showing (top) a live, fully-interactive view of the
// selected agent and (bottom) a list of every launched agent.
//
// Mechanically it stays pure tmux ("don't change what works — containerize
// it"): agents run as windows of a hidden COMPANION tmux session named
// "<outer>-agents", and the viewport pane simply runs a NESTED tmux client
// attached to that companion (TMUX unset so tmux allows the nesting, status
// off so it looks like a bare pane). Selecting an agent in the list is just
// `tmux select-window` on the companion — the nested client follows, and
// because it is a real client the user can click into the viewport pane and
// type at the agent directly.
//
// Everything here drives the LOCAL tmux server via exec (the panel only makes
// sense from inside a tmux pane, i.e. on the machine where the session lives —
// the hub, when attached through duck). The Runner seam is injectable so tests
// record the tmux argv instead of needing a live server.
package panel

import (
	"fmt"
	"os"
	"os/exec"
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
// ($TMUX is "socket-path,pid,pane-index"). The nested viewport client must
// attach through the SAME socket — clearing TMUX alone would send it to the
// default server, where the companion doesn't exist. Empty when not in tmux.
func SocketPath() string {
	env := os.Getenv("TMUX")
	if i := strings.IndexByte(env, ','); i >= 0 {
		return env[:i]
	}
	return env
}

// panelOfOption marks a companion session with the outer session it belongs
// to. session.Manager reads it (via list-sessions) so the picker and `duck ls`
// hide companions — they are plumbing, not resumable duck sessions.
const panelOfOption = "@duck_panel_of"

// roleOption is the PANE user option stamping the two panel panes so Open is
// idempotent and Close knows what to kill. Values: "viewport", "list".
const roleOption = "@duck_panel_role"

// SpawnedAtOption is the WINDOW user option carrying the unix epoch of the
// agent's spawn; the channel layer matches it against codex rollout-file
// timestamps to pair a window with its structured event stream.
const SpawnedAtOption = "@duck_spawned_at"

// RolloutOption is the WINDOW user option caching the resolved codex rollout
// path, so the (heuristic) pairing runs once and then sticks.
const RolloutOption = "@duck_rollout"

// placeholderWindow is the companion's initial window: it keeps the companion
// alive (a tmux session dies with its last window) and gives the viewport
// something friendly to show before the first agent is spawned. Spawn retires
// it once a real agent window exists. It is IDENTIFIED by placeholderOption,
// not by this name — a user is free to name a real agent "welcome".
const placeholderWindow = "welcome"

// placeholderOption is the WINDOW user option marking the placeholder, so
// Agents/Spawn identify it structurally rather than by its display name.
const placeholderOption = "@duck_placeholder"

// placeholderCmd is what the placeholder window runs: a friendly idle screen
// that sleeps forever, keeping the companion session alive.
const placeholderCmd = `sh -c 'printf "\n\n   no agent selected\n\n   spawn one:  duck spawn <cmd>\n"; while :; do sleep 3600; done'`

// Companion returns the companion session name for an outer session.
func Companion(outer string) string { return outer + "-agents" }

// CurrentSession returns the name of the tmux session this process's pane
// belongs to.
func CurrentSession(run Runner) (string, error) {
	out, err := run("display-message", "-p", "#{session_name}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CurrentPanePath returns the current pane's working directory.
func CurrentPanePath(run Runner) (string, error) {
	out, err := run("display-message", "-p", "#{pane_current_path}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// EnsureCompanion creates the companion session for outer if it doesn't exist
// (detached, cwd dir, holding only the placeholder window), and stamps its
// options: hidden-from-picker marker, status off (it renders inside the
// viewport pane — an inner status bar would be visual noise), and
// detach-on-destroy off so the nested client survives window churn.
func EnsureCompanion(run Runner, outer, dir string) (string, error) {
	comp := Companion(outer)
	if _, err := run("has-session", "-t", "="+comp); err == nil {
		return comp, nil
	}
	wid, err := run("new-session", "-d", "-s", comp, "-c", dir, "-n", placeholderWindow, "-P", "-F", "#{window_id}", placeholderCmd)
	if err != nil {
		return "", err
	}
	if _, err := run("set-option", "-w", "-t", strings.TrimSpace(wid), placeholderOption, "1"); err != nil {
		return "", err
	}
	for _, opt := range [][2]string{
		{panelOfOption, outer},
		{"status", "off"},
		{"detach-on-destroy", "off"},
	} {
		if _, err := run("set-option", "-t", comp, opt[0], opt[1]); err != nil {
			return "", err
		}
	}
	return comp, nil
}

// Panes returns the panel panes of the outer session as role → pane id
// ("%12"), by scanning every pane for the role option.
func Panes(run Runner, outer string) (map[string]string, error) {
	out, err := run("list-panes", "-s", "-t", outer, "-F", "#{pane_id}\t#{"+roleOption+"}")
	if err != nil {
		return nil, err
	}
	roles := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.SplitN(line, "\t", 2)
		if len(f) == 2 && strings.TrimSpace(f[1]) != "" {
			roles[strings.TrimSpace(f[1])] = f[0]
		}
	}
	return roles, nil
}

// Open lays out the sidebar in the outer session's current window: a full-
// height right column (~34%) holding the nested-client viewport on top and the
// agent list below. Idempotent: existing panel panes are left alone. duckBin
// is the path of the duck binary to run the list TUI with (os.Executable of
// the calling process).
func Open(run Runner, outer, comp, duckBin string) error {
	roles, err := Panes(run, outer)
	if err != nil {
		return err
	}
	// Mouse on: clicking between main pane / viewport / list rows is the whole
	// point of the sidebar.
	if _, err := run("set-option", "-t", outer, "mouse", "on"); err != nil {
		return err
	}
	// Focus reporting lets the list TUI tell "click to focus me" apart from
	// "click a row": a click landing while the pane is unfocused only focuses.
	if _, err := run("set-option", "-t", outer, "focus-events", "on"); err != nil {
		return err
	}
	if roles["viewport"] == "" {
		// -f: span the window's full height; -d: keep focus in the main pane.
		// TMUX= lets the nested client attach from inside a pane; -S pins it to
		// this server's socket (see SocketPath).
		cmd := "TMUX= exec tmux "
		if sock := SocketPath(); sock != "" {
			cmd += "-S " + paths.Quote(sock) + " "
		}
		cmd += "attach-session -t " + paths.Quote(comp)
		id, err := run("split-window", "-h", "-f", "-d", "-l", "34%", "-t", outer+":", "-P", "-F", "#{pane_id}", cmd)
		if err != nil {
			return err
		}
		roles["viewport"] = strings.TrimSpace(id)
		if _, err := run("set-option", "-p", "-t", roles["viewport"], roleOption, "viewport"); err != nil {
			return err
		}
	}
	if roles["list"] == "" {
		// Fixed rows, not a percentage: the roster only needs a handful of lines,
		// and a % split eats half a tall terminal. The viewport gets the rest.
		cmd := paths.Quote(duckBin) + " panel watch " + paths.Quote(outer)
		id, err := run("split-window", "-v", "-d", "-l", "10", "-t", roles["viewport"], "-P", "-F", "#{pane_id}", cmd)
		if err != nil {
			return err
		}
		if _, err := run("set-option", "-p", "-t", strings.TrimSpace(id), roleOption, "list"); err != nil {
			return err
		}
	}
	return nil
}

// Close kills the panel panes of outer (viewport + list). The companion
// session — and the agents in it — keep running; the panel is just a view.
func Close(run Runner, outer string) error {
	roles, err := Panes(run, outer)
	if err != nil {
		return err
	}
	for _, id := range roles {
		_, _ = run("kill-pane", "-t", id)
	}
	return nil
}

// Agent is one launched runner: a window of the companion session.
type Agent struct {
	WindowID string // tmux global window id, e.g. "@7" — the dispatch key
	Index    int
	Name     string // window name (agent label)
	Active   bool   // currently shown in the viewport
	Command  string // pane_current_command, e.g. "codex", "node", "zsh"
	Title    string // pane_title — agents like Claude Code write status here
}

// agentsFormat is the list-windows template Agents and placeholders parse.
// pane_title is free text and NOT last, so the split is bounded (SplitN) with
// the controlled placeholder marker trailing — a tab inside a title cannot
// shift fields.
const agentsFormat = "#{window_id}\t#{window_index}\t#{window_name}\t#{window_active}\t#{pane_current_command}\t#{@duck_placeholder}\t#{pane_title}"

// Agents lists the companion's windows, excluding the placeholder (identified
// by its @duck_placeholder marker, never by name).
func Agents(run Runner, comp string) ([]Agent, error) {
	out, err := run("list-windows", "-t", comp, "-F", agentsFormat)
	if err != nil {
		return nil, err
	}
	var agents []Agent
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.SplitN(line, "\t", 7)
		if len(f) < 7 || strings.TrimSpace(f[5]) != "" {
			continue
		}
		a := Agent{WindowID: f[0], Name: f[2], Active: f[3] == "1", Command: f[4], Title: f[6]}
		fmt.Sscanf(f[1], "%d", &a.Index)
		agents = append(agents, a)
	}
	return agents, nil
}

// placeholders returns the window ids of the companion's placeholder windows.
func placeholders(run Runner, comp string) []string {
	out, err := run("list-windows", "-t", comp, "-F", agentsFormat)
	if err != nil {
		return nil
	}
	var ids []string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.SplitN(line, "\t", 7)
		if len(f) >= 7 && strings.TrimSpace(f[5]) != "" {
			ids = append(ids, f[0])
		}
	}
	return ids
}

// Spawn launches cmdline (or an interactive shell when empty) as a new named
// window of the companion, selects it (so the viewport shows the newcomer),
// and retires the placeholder window once a real agent exists.
func Spawn(run Runner, comp, name, dir, cmdline string) (windowID string, err error) {
	args := []string{"new-window", "-t", comp + ":", "-P", "-F", "#{window_id}"}
	if name != "" {
		args = append(args, "-n", name)
	}
	if dir != "" {
		args = append(args, "-c", dir)
	}
	if cmdline != "" {
		args = append(args, cmdline)
	}
	id, err := run(args...)
	if err != nil {
		return "", err
	}
	windowID = strings.TrimSpace(id)
	// Stamp the spawn instant: the channel layer uses it to map this window to
	// the codex rollout file that appears right after (internal/channel).
	_, _ = run("set-option", "-w", "-t", windowID, SpawnedAtOption, strconv.FormatInt(time.Now().Unix(), 10))
	if name != "" {
		// Keep the user/derived label; don't let tmux rename it to the running cmd.
		_, _ = run("set-option", "-w", "-t", windowID, "automatic-rename", "off")
	}
	// Make sure the placeholder EXISTS (it is never retired): a tmux session
	// dies with its last window, so the placeholder is what keeps the
	// companion — and the viewport's nested client — alive when the last
	// agent exits, falling back to the "no agent selected" screen instead of
	// tearing the pane out of the layout. It is hidden from the roster by its
	// marker, so its only cost is one sleeping sh.
	if len(placeholders(run, comp)) == 0 {
		if wid, err := run("new-window", "-d", "-t", comp+":", "-n", placeholderWindow, "-P", "-F", "#{window_id}", placeholderCmd); err == nil {
			_, _ = run("set-option", "-w", "-t", strings.TrimSpace(wid), placeholderOption, "1")
		}
	}
	return windowID, nil
}

// Select makes the viewport show the given agent (`select-window` on the
// companion; the nested client follows).
func Select(run Runner, windowID string) error {
	_, err := run("select-window", "-t", windowID)
	return err
}

// Kill terminates an agent (kills its window; the process gets SIGHUP).
func Kill(run Runner, windowID string) error {
	_, err := run("kill-window", "-t", windowID)
	return err
}
