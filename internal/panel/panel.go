// Package panel implements duck's agent sidebar: a right-hand column in the
// CURRENT tmux session showing (top) the selected agent ITSELF and (bottom) a
// clickable roster of everything launched.
//
// The design is swap-based ("one flat layer"): agents are PANES parked in a
// hidden companion session (the lot). The viewport is a real pane of YOUR
// session, and selecting an agent atomically `swap-pane`s it into that slot
// (the previous occupant parks back in the lot). Nothing is ever *viewed
// through* a nested client — the pane on screen IS the agent, one tmux layer
// from the terminal, so kitty-graphics pixels (casty, future renderers)
// survive. The lot never changes pane count (swap exchanges), and an
// immortal anchor pane keeps the companion session alive regardless.
//
// All identity lives in PANE user options (@duck_name/@duck_kind/…): pane
// options travel with the pane through swaps, so an agent keeps its name,
// tab, and rollout pairing wherever it currently sits. tmux is the database.
//
// Everything here drives the LOCAL tmux server via exec (the panel only makes
// sense from inside a tmux pane, i.e. on the machine where the session lives —
// the hub, when attached through duck). The Runner seam is injectable so tests
// record the tmux argv instead of needing a live server.
package panel

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/DigiBugCat/duck/internal/names"
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

// panelOfOption marks a companion session with the outer session it belongs
// to. session.Manager reads it (via list-sessions) so the picker, `duck ls`,
// `duck clean`, and the evict sweep all treat companions as plumbing.
const panelOfOption = "@duck_panel_of"

// roleOption is the PANE user option stamping the two panel panes so Open is
// idempotent and Close knows what to kill. Values: "viewport", "list". After
// every swap, Select re-stamps so the role stays on the SLOT (positional),
// not on whichever pane happens to travel.
const roleOption = "@duck_panel_role"

// Pane user options carrying each agent's identity (they travel with the
// pane through swaps):
const (
	NameOption      = "@duck_name"       // roster label
	kindOption      = "@duck_kind"       // roster tab (see Kinds)
	SpawnedAtOption = "@duck_spawned_at" // unix epoch of spawn (channel pairing)
	RolloutOption   = "@duck_rollout"    // cached codex rollout path
	anchorOption    = "@duck_anchor"     // the lot's immortal keep-alive pane
)

// Kinds are ROSTER TAB NAMES, stored per pane in kindOption. The base set
// below always shows in the tab bar; any other value creates its own tab for
// as long as a pane carries it (`duck spawn --tab <name>`), so duck — or an
// agent driving duck — can grow the tab set at runtime with zero declaration.
const (
	KindAgent    = "agents"     // runners you supervise (default)
	KindShell    = "shells"     // plain interactive shells
	KindArtifact = "artifacts"  // things you look at (previews)
	KindBuffer   = "scratchpad" // editor panes: the scratch note + duck edit files
)

// BaseKinds is the always-visible tab order; dynamic kinds append after.
var BaseKinds = []string{KindAgent, KindShell, KindArtifact, KindBuffer}

// normalizeKind maps stamps to tab names: empty → agents, singular legacy
// stamps → their tabs.
func normalizeKind(k string) string {
	switch k {
	case "", "agent":
		return KindAgent
	case "artifact":
		return KindArtifact
	case "shell":
		return KindShell
	}
	return k
}

// terminalCmd is the viewport's default occupant: a real interactive shell
// in a respawn loop (exit → fresh shell, never a dead pane). It is stamped
// name=terminal, kind=shells, so it lives in the roster like anything else.
const terminalCmd = `sh -c 'while :; do "${SHELL:-sh}" -l || true; sleep 0.5; done'`

// anchorCmd keeps the lot window alive forever; hidden from the roster.
const anchorCmd = `sh -c 'while :; do sleep 3600; done'`

// ProjectName identifies the PROJECT a workspace belongs to: the basename of
// its main pane's working directory. Every workspace rooted in the same
// folder resolves to the same project, so they share one pad set. Falls back
// to the session name when the cwd can't be read.
func ProjectName(run Runner, outer string) string {
	// Prefer the main (non-panel) pane's cwd — the viewport occupant could be
	// an agent that wandered elsewhere.
	if out, err := run("list-panes", "-t", outer+":", "-F", "#{pane_current_path}\t#{"+roleOption+"}"); err == nil {
		for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			f := strings.SplitN(line, "\t", 2)
			if len(f) == 2 && strings.TrimSpace(f[1]) == "" && strings.TrimSpace(f[0]) != "" {
				return filepath.Base(strings.TrimSpace(f[0]))
			}
		}
	}
	if p, err := SessionPath(run, outer); err == nil && p != "" {
		return filepath.Base(p)
	}
	return outer
}

// PadPath resolves a named pad. Pads are PROJECT-scoped: every workspace
// rooted in the same folder shares the pad set under
// ~/.duck/scratchpad/<project>/ (the scratchpad dir is commonly a symlink
// into the user's synced vault). A pad that already exists at the flat top
// level (shared pads like shared.md, pre-project-era pads) wins over the
// project dir, so nothing already written goes dark. Created with a header
// on first touch.
func PadPath(project, name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(home, ".duck", "scratchpad")
	if flat := filepath.Join(root, name+".md"); fileExists(flat) {
		return flat, nil
	}
	dir := root
	if project != "" {
		dir = filepath.Join(root, project)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+".md")
	if !fileExists(path) {
		header := "# " + name + "\n\n"
		if werr := os.WriteFile(path, []byte(header), 0o644); werr != nil {
			return "", werr
		}
	}
	return path, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// PadCmd builds the in-pane command for a pad editor: a respawn loop (quit
// reopens; the roster's x truly closes) around, in preference order,
// $DUCK_PAD_EDITOR, micro (standard keybindings + autosave every second +
// silent auto-reload of external edits — agents can write into a pad the
// human is watching), else $EDITOR.
func PadCmd(path string) string {
	return "sh -c " + paths.Quote(`while :; do `+editorScript(path)+`; sleep 0.3; done`)
}

// EditorCmd is the ONE-SHOT editor invocation for a file: the same editor
// preferences as pads (micro with autosave + live-reload + softwrap when
// installed, $DUCK_PAD_EDITOR override, $EDITOR fallback) but the pane
// closes when the editor exits — right for `duck edit <file>` buffers and
// markdown previews, where quit means done.
func EditorCmd(path string) string {
	return "sh -c " + paths.Quote(editorScript(path))
}

// editorScript is the shared editor-preference chain both wrappers run.
func editorScript(path string) string {
	q := paths.Quote(path)
	return `if [ -n "$DUCK_PAD_EDITOR" ]; then $DUCK_PAD_EDITOR ` + q +
		`; elif command -v micro >/dev/null 2>&1; then micro -autosave 1 -reload auto -savecursor true -softwrap true -wordwrap true ` + q +
		`; else "${EDITOR:-vim}" ` + q + `; fi`
}

// EnsureScratch guarantees the project's long-lived scratch buffer exists
// as a PARKED pane (roster tab: buffers) — present from the first glance,
// shown only when selected. The editor runs in a respawn loop so :q just
// reopens it; killing it for real is the roster's two-press x.
func EnsureScratch(run Runner, outer string) {
	agents, err := Agents(run, outer)
	if err != nil {
		return
	}
	for _, a := range agents {
		if a.Kind == KindBuffer && a.Name == "scratch" {
			return
		}
	}
	path, err := PadPath(ProjectName(run, outer), "scratch")
	if err != nil {
		return
	}
	id, err := run("split-window", "-d", "-t", Companion(outer)+":lot", "-P", "-F", "#{pane_id}", PadCmd(path))
	if err != nil {
		return
	}
	pid := strings.TrimSpace(id)
	for _, opt := range [][2]string{
		{NameOption, "scratch"},
		{kindOption, KindBuffer},
	} {
		_, _ = run("set-option", "-p", "-t", pid, opt[0], opt[1])
	}
	_, _ = run("select-layout", "-t", Companion(outer)+":lot", "tiled")
}

// rosterRows is the roster pane's height (the command box + hint need
// breathing room under a useful list).
const rosterRows = "15"

// Companion returns the companion (lot) session name for an outer session.
func Companion(outer string) string { return outer + "-agents" }

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
// the cwd a panel opened from OUTSIDE the session should adopt.
func SessionPath(run Runner, outer string) (string, error) {
	out, err := run("display-message", "-p", "-t", outer+":", "#{pane_current_path}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// EnsureCompanion creates the lot session for outer if it doesn't exist:
// one window holding the immortal anchor pane, stamped as plumbing
// (hidden from picker, skipped by clean/evict). Idempotent.
func EnsureCompanion(run Runner, outer, dir string) (string, error) {
	comp := Companion(outer)
	if _, err := run("has-session", "-t", "="+comp); err == nil {
		// MIGRATION: a companion from the pre-swap (windows-based) design has
		// no 'lot' window, and every swap-era operation targets comp:lot —
		// without this retrofit an upgraded binary hard-wedges on old fleet
		// sessions ("can't find window: lot"). Old agent windows are left
		// as-is (invisible to the roster; they predate pane identity).
		if out, lerr := run("list-windows", "-t", comp, "-F", "#{window_name}"); lerr == nil {
			hasLot := false
			for _, w := range strings.Split(strings.TrimSpace(out), "\n") {
				if w == "lot" {
					hasLot = true
				}
			}
			if !hasLot {
				if pid, cerr := run("new-window", "-d", "-t", comp+":", "-n", "lot", "-P", "-F", "#{pane_id}", anchorCmd); cerr == nil {
					_, _ = run("set-option", "-p", "-t", strings.TrimSpace(pid), anchorOption, "1")
				}
			}
		}
		return comp, nil
	}
	pid, err := run("new-session", "-d", "-s", comp, "-c", dir, "-n", "lot", "-P", "-F", "#{pane_id}", anchorCmd)
	if err != nil {
		return "", err
	}
	if _, err := run("set-option", "-p", "-t", strings.TrimSpace(pid), anchorOption, "1"); err != nil {
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

// Panes returns the panel panes of the outer session as role → pane id, by
// scanning every pane for the role option.
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
// height right column (~34%) holding the viewport SLOT on top (starting as
// the terminal occupant) and the roster below. Idempotent: existing panel
// panes are left alone. duckBin is the path of the duck binary to run the
// roster TUI with.
func Open(run Runner, outer, comp, duckBin string) error {
	roles, err := Panes(run, outer)
	if err != nil {
		return err
	}
	for _, opt := range [][2]string{
		// Clicking between panes and roster rows is the whole point.
		{"mouse", "on"},
		// Focus reporting lets the roster tell "click to focus" from "click a row".
		{"focus-events", "on"},
		// The one pixel path to the terminal (kitty graphics wrapped once) —
		// and with the swap design the viewport IS one layer from the client.
		{"allow-passthrough", "on"},
	} {
		if _, err := run("set-option", "-t", outer, opt[0], opt[1]); err != nil {
			return err
		}
	}
	if roles["viewport"] == "" {
		// -f: span the window's full height; -d: keep focus in the main pane.
		id, err := run("split-window", "-h", "-f", "-d", "-l", "34%", "-t", outer+":", "-P", "-F", "#{pane_id}", terminalCmd)
		if err != nil {
			return err
		}
		vp := strings.TrimSpace(id)
		roles["viewport"] = vp
		for _, opt := range [][2]string{
			{roleOption, "viewport"},
			{NameOption, "terminal"},
			{kindOption, KindShell},
		} {
			if _, err := run("set-option", "-p", "-t", vp, opt[0], opt[1]); err != nil {
				return err
			}
		}
	}
	if roles["list"] == "" {
		cmd := paths.Quote(duckBin) + " panel watch " + paths.Quote(outer)
		id, err := run("split-window", "-v", "-d", "-l", rosterRows, "-t", roles["viewport"], "-P", "-F", "#{pane_id}", cmd)
		if err != nil {
			return err
		}
		if _, err := run("set-option", "-p", "-t", strings.TrimSpace(id), roleOption, "list"); err != nil {
			return err
		}
	}
	// Geometry is asserted on EVERY open — spawn/edit/preview/attach all pass
	// through here, so no code path can leave a mangled layout behind.
	Heal(run, outer)
	return nil
}

// Close kills the roster pane and parks the viewport occupant back in the
// lot before killing the slot — closing the panel must never kill an agent.
func Close(run Runner, outer string) error {
	roles, err := Panes(run, outer)
	if err != nil {
		return err
	}
	if vp := roles["viewport"]; vp != "" {
		// Park the occupant: move it to the lot window (break-pane into comp).
		// The terminal occupant parks too — it's a roster citizen like any other.
		comp := Companion(outer)
		if _, err := run("has-session", "-t", "="+comp); err == nil {
			_, _ = run("break-pane", "-d", "-s", vp, "-t", comp+":lot")
		} else {
			_, _ = run("kill-pane", "-t", vp)
		}
		_, _ = run("set-option", "-p", "-t", vp, "-u", roleOption)
	}
	if id := roles["list"]; id != "" {
		_, _ = run("kill-pane", "-t", id)
	}
	return nil
}

// Agent is one launched runner or artifact: a stamped pane, wherever it
// currently sits (parked in the lot, or on display in the viewport slot).
type Agent struct {
	PaneID  string // tmux pane id, e.g. "%42" — the dispatch key
	Name    string // roster label (@duck_name)
	Active  bool   // currently in the viewport slot
	Command string // pane_current_command, e.g. "codex", "node", "zsh"
	Kind    string // roster tab (@duck_kind)
	Title   string // pane_title — agents like Claude Code write status here
}

// agentsFormat lists panes with their identity options. pane_title is free
// text, so it is LAST and the split is bounded.
const agentsFormat = "#{pane_id}\t#{" + NameOption + "}\t#{" + kindOption + "}\t#{" + anchorOption + "}\t#{" + roleOption + "}\t#{pane_current_command}\t#{pane_title}"

// parseAgents turns list-panes output into Agents, skipping unstamped panes
// (whatever else lives in the sessions) and the lot anchor. slotID marks
// which pane is on display.
func parseAgents(out, slotID string) []Agent {
	var agents []Agent
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.SplitN(line, "\t", 7)
		if len(f) < 7 || strings.TrimSpace(f[1]) == "" || strings.TrimSpace(f[3]) != "" {
			continue
		}
		agents = append(agents, Agent{
			PaneID:  f[0],
			Name:    strings.TrimSpace(f[1]),
			Kind:    normalizeKind(strings.TrimSpace(f[2])),
			Active:  f[0] == slotID,
			Command: f[5],
			Title:   f[6],
		})
	}
	return agents
}

// Agents lists every stamped pane belonging to outer's sidebar: parked in
// the lot plus the current viewport occupant.
func Agents(run Runner, outer string) ([]Agent, error) {
	roles, err := Panes(run, outer)
	if err != nil {
		return nil, err
	}
	slot := roles["viewport"]
	var all []Agent
	if slot != "" {
		if out, err := run("list-panes", "-s", "-t", outer, "-F", agentsFormat); err == nil {
			for _, a := range parseAgents(out, slot) {
				if a.PaneID == slot {
					all = append(all, a) // only the occupant; other outer panes aren't ours
				}
			}
		}
	}
	comp := Companion(outer)
	out, err := run("list-panes", "-s", "-t", comp, "-F", agentsFormat)
	if err != nil {
		// No lot yet (panel never opened / all parked panes elsewhere) — the
		// occupant alone is a valid roster.
		return all, nil
	}
	all = append(all, parseAgents(out, slot)...)
	return all, nil
}

// Spawn launches cmdline (or an interactive shell when empty) as a new
// stamped pane PARKED in the lot, then swaps it on display. kind is the
// roster tab; empty means agents.
func Spawn(run Runner, outer, name, dir, cmdline, kind string) (paneID string, err error) {
	comp, err := EnsureCompanion(run, outer, dir)
	if err != nil {
		return "", err
	}
	args := []string{"split-window", "-d", "-t", comp + ":lot", "-P", "-F", "#{pane_id}"}
	if dir != "" {
		args = append(args, "-c", dir)
	}
	if cmdline != "" {
		// Hold the pane open when the command exits non-zero or near-
		// instantly: otherwise `spawn test` (a builtin that exits in 1ms)
		// lives and dies before the viewport can even show it — reads as
		// "nothing happened".
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
	} {
		_, _ = run("set-option", "-p", "-t", paneID, opt[0], opt[1])
	}
	// Even out the lot so parked panes never shrink to zero-size (tmux
	// refuses splits on tiny panes eventually).
	_, _ = run("select-layout", "-t", comp+":lot", "tiled")
	// Show the newcomer.
	_ = Select(run, outer, paneID)
	return paneID, nil
}

// Select puts the given pane on display: an atomic swap-pane between it and
// the current viewport occupant (which parks in the agent's old lot spot),
// then re-stamps the role so "viewport" stays positional, and hard-refreshes
// clients so full-screen TUIs can't leave stale cells behind.
func Select(run Runner, outer, paneID string) error {
	roles, err := Panes(run, outer)
	if err != nil {
		return err
	}
	slot := roles["viewport"]
	if slot == "" {
		return fmt.Errorf("no viewport open (run: duck panel)")
	}
	if slot == paneID {
		return nil // already on display
	}
	if _, err := run("swap-pane", "-d", "-s", paneID, "-t", slot); err != nil {
		return err
	}
	// The role option traveled with the old occupant into the lot; move it to
	// the pane now sitting in the slot (positional role).
	_, _ = run("set-option", "-p", "-t", slot, "-u", roleOption)
	_, _ = run("set-option", "-p", "-t", paneID, roleOption, "viewport")
	if out, err := run("list-clients", "-t", outer, "-F", "#{client_name}"); err == nil {
		for _, c := range strings.Split(strings.TrimSpace(out), "\n") {
			if c != "" {
				_, _ = run("refresh-client", "-t", c)
			}
		}
	}
	_, _ = run("select-layout", "-t", Companion(outer)+":lot", "tiled")
	return nil
}

// Kill terminates an agent pane. If it is currently on display, the terminal
// (or any parked pane) is NOT auto-promoted — the slot pane dies and Open
// recreates it on the next panel/spawn; killing parked panes is invisible.
func Kill(run Runner, paneID string) error {
	_, err := run("kill-pane", "-t", paneID)
	return err
}

// Preview panes carry their render recipe in pane options so the roster can
// re-render ON DEMAND: selecting an artifact whose source file changed
// respawns the pane with the same command. Unchanged file → plain select.
const (
	PreviewCmdOption   = "@duck_preview_cmd"
	PreviewPathOption  = "@duck_preview_path"
	PreviewMtimeOption = "@duck_preview_mtime"
)

// StampPreview records the recipe on a freshly spawned preview pane.
func StampPreview(run Runner, paneID, cmd, path string, mtime int64) {
	_, _ = run("set-option", "-p", "-t", paneID, PreviewCmdOption, cmd)
	_, _ = run("set-option", "-p", "-t", paneID, PreviewPathOption, path)
	_, _ = run("set-option", "-p", "-t", paneID, PreviewMtimeOption, strconv.FormatInt(mtime, 10))
}

// RefreshIfStale re-renders a preview pane whose source file changed since
// the last render (respawn-pane with the stamped command), restamping the
// new mtime. Panes without a preview stamp are a no-op, so callers invoke it
// on every selection unconditionally.
func RefreshIfStale(run Runner, paneID string, stat func(string) (int64, bool)) {
	read := func(name string) string {
		out, err := run("show-options", "-p", "-t", paneID, "-v", name)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(out)
	}
	path := read(PreviewPathOption)
	cmd := read(PreviewCmdOption)
	if path == "" || cmd == "" {
		return
	}
	mtime, ok := stat(path)
	if !ok || strconv.FormatInt(mtime, 10) == read(PreviewMtimeOption) {
		return
	}
	if _, err := run("respawn-pane", "-k", "-t", paneID, cmd); err != nil {
		return
	}
	_, _ = run("set-option", "-p", "-t", paneID, PreviewMtimeOption, strconv.FormatInt(mtime, 10))
}

// FileMtime is the production stat for RefreshIfStale.
func FileMtime(path string) (int64, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	return info.ModTime().Unix(), true
}

// EnsureSlot heals a missing viewport (its occupant was killed on display):
// recreate the slot with a transient holder, swap the parked terminal back
// in when one exists, and retire the holder. No-op when the slot is alive.
func EnsureSlot(run Runner, outer string) {
	roles, err := Panes(run, outer)
	if err != nil || roles["viewport"] != "" {
		return
	}
	var holder string
	if roles["list"] != "" {
		// Split ABOVE the roster so the healed slot lands in the sidebar
		// column (the roster absorbed the dead viewport's space), then give
		// the roster its 10 rows back.
		holder, err = run("split-window", "-v", "-b", "-d", "-t", roles["list"], "-P", "-F", "#{pane_id}", anchorCmd)
		if err == nil {
			_, _ = run("resize-pane", "-t", roles["list"], "-y", rosterRows)
		}
	} else {
		holder, err = run("split-window", "-h", "-f", "-d", "-l", "34%", "-t", outer+":", "-P", "-F", "#{pane_id}", anchorCmd)
	}
	if err != nil {
		return
	}
	hid := strings.TrimSpace(holder)
	_, _ = run("set-option", "-p", "-t", hid, roleOption, "viewport")
	// Promote the parked terminal (or leave a fresh one) as the occupant.
	comp := Companion(outer)
	if out, lerr := run("list-panes", "-s", "-t", comp, "-F", "#{pane_id}\t#{"+NameOption+"}"); lerr == nil {
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			f := strings.SplitN(line, "\t", 2)
			if len(f) == 2 && strings.TrimSpace(f[1]) == "terminal" {
				if Select(run, outer, f[0]) == nil {
					_, _ = run("kill-pane", "-t", hid) // holder is parked now — retire it
				}
				return
			}
		}
	}
	// No parked terminal: make the holder BE the terminal.
	_, _ = run("respawn-pane", "-k", "-t", hid, terminalCmd)
	_, _ = run("set-option", "-p", "-t", hid, NameOption, "terminal")
	_, _ = run("set-option", "-p", "-t", hid, kindOption, KindShell)
}

// Heal re-asserts the canonical panel geometry — main pane(s) left, a
// full-height 34% sidebar column on the right holding the viewport over a
// 10-row roster — by MOVING the existing panes (join-pane repositions
// without touching processes). Pane surgery, resizes, and mishaps mangle
// layouts; heal makes the workspace self-asserting: every `duck panel`
// (and thus every attach) converges the geometry instead of trusting it.
func Heal(run Runner, outer string) {
	roles, err := Panes(run, outer)
	if err != nil || roles["viewport"] == "" || roles["list"] == "" {
		return // Open owns creation; heal only repositions complete panels
	}
	// Reference: the first pane in the current window that is NOT panel
	// furniture — the user's main pane.
	out, err := run("list-panes", "-t", outer+":", "-F", "#{pane_id}\t#{"+roleOption+"}")
	if err != nil {
		return
	}
	main := ""
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.SplitN(line, "\t", 2)
		if len(f) == 2 && strings.TrimSpace(f[1]) == "" {
			main = f[0]
			break
		}
	}
	if main == "" {
		return
	}
	// Viewport becomes the full-height right column of the main pane's
	// window; the roster docks under it at 10 rows. join-pane moves panes
	// even within the same window, so this is idempotent-by-outcome.
	_, _ = run("join-pane", "-d", "-f", "-h", "-l", "34%", "-s", roles["viewport"], "-t", main)
	_, _ = run("join-pane", "-d", "-v", "-l", rosterRows, "-s", roles["list"], "-t", roles["viewport"])
	_, _ = run("resize-pane", "-t", roles["list"], "-y", rosterRows)
}

// Workspace is one duck session on this hub, as the roster's ws view shows.
type Workspace struct {
	Name     string // internal tmux id (the switch target)
	Display  string // resolved display name (user ▸ pane title ▸ codex ▸ dir)
	Attached bool
	Current  bool
}

// Workspaces lists the hub's duck sessions (companions excluded), current
// first, then attached, then name order preserved from tmux.
func Workspaces(run Runner, outer string) ([]Workspace, error) {
	out, err := run("list-sessions", "-F", "#{session_name}\t#{session_attached}\t#{@duck_dir}\t#{"+panelOfOption+"}\t#{pane_title}")
	if err != nil {
		return nil, err
	}
	myProj := ProjectName(run, outer)
	doc, _ := names.NewStore(shellRunner{}).Load() // display names; empty doc on any error
	var ws []Workspace
	// TrimRight newlines ONLY (TrimSpace eats the last line's trailing tab);
	// pane_title is free text and LAST, so the split is bounded.
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.SplitN(line, "\t", 5)
		if len(f) < 3 {
			continue
		}
		panelOf, title := "", ""
		if len(f) >= 4 {
			panelOf = strings.TrimSpace(f[3])
		}
		if len(f) == 5 {
			title = f[4]
		}
		if panelOf != "" {
			continue // companions are plumbing
		}
		dir := strings.TrimSpace(f[2])
		proj := ""
		if dir != "" {
			proj = filepath.Base(dir)
		}
		if f[0] != outer && (proj == "" || proj != myProj) {
			continue
		}
		ws = append(ws, Workspace{
			Name:     f[0],
			Display:  names.Resolve(doc, f[0], dir, title),
			Attached: f[1] != "0" && f[1] != "",
			Current:  f[0] == outer,
		})
	}
	return ws, nil
}

// shellRunner adapts the names.Store Runner (shell-string commands) to
// local execution — the roster runs on the hub, next to names.json.
type shellRunner struct{}

func (shellRunner) Run(cmd string) (string, error) {
	out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
	return string(out), err
}
func (shellRunner) RunInput(cmd string, stdin io.Reader) (string, error) {
	c := exec.Command("sh", "-c", cmd)
	c.Stdin = stdin
	out, err := c.CombinedOutput()
	return string(out), err
}

// SwitchTo moves the outer session's attached client to another workspace,
// arming the target's panel first so the jump lands in a furnished cockpit.
func SwitchTo(run Runner, outer, target, duckBin string) error {
	if comp, err := EnsureCompanion(run, target, ""); err == nil {
		_ = Open(run, target, comp, duckBin)
		EnsureScratch(run, target)
	}
	out, err := run("list-clients", "-t", outer, "-F", "#{client_name}")
	if err != nil {
		return err
	}
	client := strings.TrimSpace(strings.Split(strings.TrimSpace(out), "\n")[0])
	if client == "" {
		return fmt.Errorf("no attached client to switch")
	}
	_, err = run("switch-client", "-c", client, "-t", target)
	return err
}
