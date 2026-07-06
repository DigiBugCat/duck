// Firing: turning due routines into panes. This is the hub-side half of the
// package (LoadWorkspace/Due compute WHAT should fire; here we make it
// happen). A tick sweeps every workspace with routine definitions, fires the
// due ones into THAT workspace, and persists last-fire once. Everything
// routes through duck's own verbs (EnsureCompanion, Spawn, channel send) —
// tmux stays the only database.
package routines

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/DigiBugCat/duck/internal/channel"
	"github.com/DigiBugCat/duck/internal/manager"
	"github.com/DigiBugCat/duck/internal/panel"
	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/DigiBugCat/duck/internal/workspaces"
)

// wsStore constructs a workspaces.Store over a LOCAL shell runner. The tick
// always runs hub-local (panel.ExecRunner drives the local tmux server), and
// the workspace records live on the hub filesystem, so a local sh runner is the
// right seam here — NOT the SSH client the laptop-side flow uses. panel.
// ExecRunner itself prepends "tmux" and so cannot double as the store's shell.
// It is a package var so tests can point the ledger at a scratch base.
var wsStore = func() *workspaces.Store {
	return workspaces.NewStore(workspaces.LocalRunner{})
}

// codexBin is the executor binary tick launches per fire. Overridable via
// DUCK_CODEX_BIN (a test seam, and genuinely useful when codex isn't named
// "codex" on PATH). The full argv is <bin> exec
// --dangerously-bypass-approvals-and-sandbox <prompt>.
func codexBin() string {
	if b := strings.TrimSpace(getenv("DUCK_CODEX_BIN")); b != "" {
		return b
	}
	return "codex"
}

// getenv is a package var so tests can stub environment lookups without
// touching the real process environment. Defaults to os.Getenv.
var getenv = os.Getenv

// notifyArg wires codex's end-of-turn notify hook to `duck channel notify`
// (same treatment `duck spawn` gives interactive agents): the hook pins the
// executor pane's rollout by thread id the moment its turn ends, so channel
// attribution is exact instead of cwd+time-correlated. Empty on any error —
// the fallback pairing still works, just slower.
func notifyArg() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	return " -c " + paths.Quote(fmt.Sprintf(`notify=[%q,"channel","notify"]`, self))
}

// Tick is the entrypoint the systemd/launchd timer calls once a minute. It is
// robust by construction: a broken project or routine logs to logw and the
// sweep continues — one bad definition never stops the others. State is loaded
// once, mutated in memory as routines fire (or drop their beat), and saved
// once at the end.
//
// run is the LOCAL tmux runner (tick always runs on the hub). now is injected
// for tests. logw receives human-readable progress/skip/error lines.
func Tick(run panel.Runner, now time.Time, logw io.Writer) error {
	// Heal first: bring every Persistent workspace back to life before due-ness is
	// computed, so a routine whose workspace died in a reboot fires into the healed
	// session this same tick rather than waiting for the next one.
	healPersistent(run, logw)

	refs, err := AllWorkspaces()
	if err != nil {
		return fmt.Errorf("enumerate routine workspaces: %w", err)
	}

	state, err := LoadState()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	live := liveSessionNames(run)
	changed := false
	for _, ref := range refs {
		if !live[ref.Workspace] {
			// Not live and not healed above (no Persistent record): the
			// employee's office is gone — its duties sleep until the workspace
			// is back (or its routines dir is removed).
			fmt.Fprintf(logw, "routines: workspace %s gone — its routines are dormant\n", ref.Workspace)
			continue
		}
		defs, err := LoadWorkspace(ref.Root, ref.Workspace)
		if err != nil {
			fmt.Fprintf(logw, "routines: skip workspace %s: %v\n", ref.Workspace, err)
			continue
		}
		for _, d := range defs {
			if d.Trigger == TriggerManual {
				continue // manual never auto-fires
			}
			last := state.LastFire[Key(ref.Root, ref.Workspace, d.Name)]
			if d.Trigger == TriggerCron && last.IsZero() {
				// First sight of a cron routine: seed last-fire so it waits for
				// its next cron slot. Without the seed a zero last is never due
				// (Due can't distinguish "brand new" from "fired long ago" in a
				// way that wouldn't refire forever). Heartbeats skip the seed —
				// a fresh heartbeat is due NOW (Due treats zero-last as due), so
				// its persistent pane exists from the first tick.
				state.LastFire[Key(ref.Root, ref.Workspace, d.Name)] = now
				changed = true
				continue
			}
			if !d.Due(last, now) {
				continue
			}
			if Fire(run, d, now, logw) {
				// Record the beat regardless of whether a fresh pane was spawned
				// or the guard skipped it: a skipped beat is DROPPED, not queued
				// (docs/ROUTINES.md: "Missed beats are dropped, not replayed").
				// Recording last-fire here is what makes that true — otherwise a
				// routine skipped for concurrency would re-evaluate as due every
				// minute and pile up the instant its predecessor finished.
				state.LastFire[Key(ref.Root, ref.Workspace, d.Name)] = now
				changed = true
			}
		}
	}

	if changed {
		if err := SaveState(state); err != nil {
			return fmt.Errorf("save state: %w", err)
		}
	}

	// Report courier LAST: runs that completed since the previous tick (their
	// breadcrumbs left by the notify hook) become one digest per workspace.
	courier(run, logw)
	return nil
}

// Fire triggers one routine immediately (the `duck routines fire` path and the
// tick's per-due call share this). The routine's OWNING workspace is where
// everything lands: the run pane, the heartbeat thread, the manager turn.
// Returns true if the beat should be recorded (delivered OR the guard dropped
// the beat); false only on a hard error that left nothing done, so the caller
// can leave last-fire untouched and retry next minute.
func Fire(run panel.Runner, d Def, now time.Time, logw io.Writer) bool {
	outer := d.Workspace
	dir, err := workspaceCwd(run, outer)
	if err != nil {
		fmt.Fprintf(logw, "routines: %s/%s: resolve workspace dir: %v\n", outer, d.Name, err)
		return false
	}
	if _, err := panel.EnsureCompanion(run, outer, dir); err != nil {
		fmt.Fprintf(logw, "routines: %s/%s: ensure companion: %v\n", outer, d.Name, err)
		return false
	}
	if d.Target == TargetManager {
		return fireManager(run, d, outer, logw)
	}
	if d.Trigger == TriggerHeartbeat {
		return fireHeartbeat(run, d, outer, dir, logw)
	}

	// Concurrency guard: if a pane by this routine's name already exists AND its
	// last codex turn is still open, a previous fire is still running. Drop this
	// beat (return true so we record last-fire and don't refire next minute).
	if ref, err := channel.FindAgent(run, outer, d.Name); err == nil {
		if channel.StatusByWindow(run, ref.WindowID) == "working" {
			fmt.Fprintf(logw, "routines: %s/%s still working — dropping this beat\n", outer, d.Name)
			return true
		}
	}

	cmdline := codexBin() + " exec --dangerously-bypass-approvals-and-sandbox" + notifyArg() + " " + paths.Quote(d.Prompt)
	if _, err := panel.Spawn(run, outer, d.Name, dir, cmdline, panel.KindRun); err != nil {
		fmt.Fprintf(logw, "routines: %s/%s: spawn: %v\n", outer, d.Name, err)
		return false
	}
	fmt.Fprintf(logw, "routines: fired %s/%s\n", outer, d.Name)
	return true
}

// rootForWorkspace reverse-resolves which indexed project root owns a
// workspace's routine defs, by finding the indexed root whose
// <root>/.duck/routines/<ws>/ dir exists. Returns tilde-form root. Used by the
// courier, which enumerates report breadcrumbs by workspace name only and needs
// the root to load each workspace's report policy. A workspace with no indexed
// routine dir (an ad-hoc run) yields (,"" false) — the courier then reports its
// completions as digest, matching today's tolerance.
func rootForWorkspace(ws string) (string, bool) {
	roots, err := LoadIndex()
	if err != nil {
		return "", false
	}
	for _, root := range roots {
		dir, err := WorkspaceDir(root, ws)
		if err != nil {
			continue
		}
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return root, true
		}
	}
	return "", false
}

// workspaceCwd resolves a workspace's working directory: its @duck_dir
// (tilde-form, expanded), falling back to the first pane's current path for
// sessions that predate the stamp.
func workspaceCwd(run panel.Runner, ws string) (string, error) {
	if out, err := run("show-options", "-t", ws, "-v", "@duck_dir"); err == nil {
		if d := strings.TrimSpace(out); d != "" {
			if abs, err := paths.Expand(d); err == nil && abs != "" {
				return abs, nil
			}
		}
	}
	out, err := run("display-message", "-p", "-t", ws+":", "#{pane_current_path}")
	if err != nil {
		return "", err
	}
	if d := strings.TrimSpace(out); d != "" {
		return d, nil
	}
	return "", fmt.Errorf("workspace %s has no resolvable directory", ws)
}

// fireHeartbeat delivers one beat of a heartbeat routine: ONE persistent
// codex TUI pane per routine, each beat a channel send of the prompt into it
// — recurring turns in a single thread, watchable in the viewport
// (docs/ROUTINES.md "target=run, heartbeat"). The pane is created lazily on
// the first beat; a beat that lands while the previous turn is still open is
// DROPPED, not queued.
func fireHeartbeat(run panel.Runner, d Def, outer, dir string, logw io.Writer) bool {
	ref, err := channel.FindAgent(run, outer, d.Name)
	if err != nil {
		// No pane yet — spawn the persistent TUI and wait for its composer
		// before typing (keys sent during TUI startup are eaten).
		cmdline := codexBin() + " --dangerously-bypass-approvals-and-sandbox" + notifyArg()
		paneID, serr := panel.Spawn(run, outer, d.Name, dir, cmdline, panel.KindRun)
		if serr != nil {
			fmt.Fprintf(logw, "routines: %s/%s: spawn heartbeat pane: %v\n", outer, d.Name, serr)
			return false
		}
		ref = channel.AgentRef{Session: outer, Name: d.Name, WindowID: paneID}
		if !awaitComposer(run, paneID, 15*time.Second) {
			fmt.Fprintf(logw, "routines: %s/%s: composer not ready after spawn — sending anyway\n", outer, d.Name)
		}
	} else if channel.StatusByWindow(run, ref.WindowID) == "working" {
		fmt.Fprintf(logw, "routines: %s/%s heartbeat still working — dropping this beat\n", outer, d.Name)
		return true
	}
	if err := channel.Send(run, ref, d.Prompt); err != nil {
		fmt.Fprintf(logw, "routines: %s/%s: send beat: %v\n", outer, d.Name, err)
		return false
	}
	fmt.Fprintf(logw, "routines: heartbeat %s/%s beat delivered\n", outer, d.Name)
	return true
}

// courier is the manager's inbox (docs/ROUTINES.md "Reporting upward"): per
// workspace with pending run-completion breadcrumbs (left by the codex notify
// hook at the instant each run finished), deliver ONE batched digest turn to
// the manager — the publish lane when a channel sidecar is alive, send-keys
// into the main claude pane otherwise. Routines with report="none" are
// filtered out; breadcrumbs with no matching definition (ad-hoc runs) are
// included. Undeliverable digests are dropped, not replayed.
func courier(run panel.Runner, logw io.Writer) {
	wss, err := channel.ReportWorkspaces()
	if err != nil {
		fmt.Fprintf(logw, "routines: courier: %v\n", err)
		return
	}
	for _, ws := range wss {
		reports, err := channel.DrainReports(ws)
		if err != nil {
			fmt.Fprintf(logw, "routines: courier %s: %v\n", ws, err)
			continue
		}
		if len(reports) == 0 {
			continue
		}
		policy := map[string]string{}
		if root, ok := rootForWorkspace(ws); ok {
			if defs, lerr := LoadWorkspace(root, ws); lerr == nil {
				for _, d := range defs {
					policy[d.Name] = d.Report
				}
			}
		}
		var lines []string
		for _, r := range reports {
			if policy[r.Routine] == "none" {
				continue
			}
			first := strings.SplitN(strings.TrimSpace(r.Message), "\n", 2)[0]
			if first == "" {
				first = "(no message)"
			}
			lines = append(lines, fmt.Sprintf("routine %s completed: %s — `duck channel tail %s` for detail", r.Routine, first, r.Routine))
		}
		if len(lines) == 0 {
			continue
		}
		digest := strings.Join(lines, "\n")
		if channel.AliveWithin(ws, 10*time.Second) {
			if err := channel.Publish(ws, digest, map[string]string{"source": "routines", "type": "digest"}); err == nil {
				fmt.Fprintf(logw, "routines: courier: digest (%d) published to %s\n", len(lines), ws)
				continue
			}
		}
		if pane, ok := managerPane(run, ws); ok {
			if err := channel.Send(run, channel.AgentRef{Session: ws, Name: "manager", WindowID: pane}, digest); err == nil {
				fmt.Fprintf(logw, "routines: courier: digest (%d) typed into %s manager\n", len(lines), ws)
				continue
			}
		}
		fmt.Fprintf(logw, "routines: courier: %s has no reachable manager — digest (%d) dropped\n", ws, len(lines))
	}
}

// fireManager delivers a scheduled turn to the workspace's MANAGER — the
// main claude pane (docs/ROUTINES.md "target=manager"). Preferred delivery is
// the publish lane: a live channel sidecar injects it as a structured event
// without touching the composer. Fallback is send-keys into a main pane
// actually running claude. No manager present → the beat is dropped with a
// log line (missed beats are dropped, not replayed).
func fireManager(run panel.Runner, d Def, outer string, logw io.Writer) bool {
	if channel.AliveWithin(outer, 10*time.Second) {
		if err := channel.Publish(outer, d.Prompt, map[string]string{"source": "routines", "type": "routine", "routine": d.Name}); err == nil {
			fmt.Fprintf(logw, "routines: manager turn %s/%s published\n", outer, d.Name)
			return true
		}
	}
	pane, ok := managerPane(run, outer)
	if !ok {
		fmt.Fprintf(logw, "routines: %s/%s: no manager claude — dropping this beat\n", outer, d.Name)
		return true
	}
	if err := channel.Send(run, channel.AgentRef{Session: outer, Name: "manager", WindowID: pane}, d.Prompt); err != nil {
		fmt.Fprintf(logw, "routines: %s/%s: send to manager: %v\n", outer, d.Name, err)
		return false
	}
	fmt.Fprintf(logw, "routines: manager turn %s/%s typed into %s\n", outer, d.Name, pane)
	return true
}

const managerOption = "@duck_manager"

// stampManagerPane records the workspace manager pane as a session-scoped tmux
// option. The target session's active pane is the manager immediately after duck
// sends the launch line into a newly created/revived session. The stamp is
// PROVISIONAL: the pane id is correct at once, but the pane runs a shell until
// `claude` execs (~1s), so validateManagerPane won't confirm it as claude right
// away — managerPane's sniff fallback covers that gap and restamps if needed.
func stampManagerPane(run panel.Runner, outer string) (string, error) {
	out, err := run("display-message", "-p", "-t", outer, "#{pane_id}")
	if err != nil {
		return "", err
	}
	pane := strings.TrimSpace(out)
	if pane == "" {
		return "", fmt.Errorf("no pane id for %s", outer)
	}
	if _, err := run("set-option", "-t", outer, managerOption, pane); err != nil {
		return "", err
	}
	return pane, nil
}

func validateManagerPane(run panel.Runner, pane string) bool {
	if strings.TrimSpace(pane) == "" {
		return false
	}
	out, err := run("display-message", "-p", "-t", pane, "#{pane_id}\t#{pane_current_command}")
	if err != nil {
		return false
	}
	f := strings.SplitN(strings.TrimRight(out, "\n"), "\t", 2)
	return len(f) == 2 && strings.TrimSpace(f[0]) == pane && strings.TrimSpace(f[1]) == "claude"
}

func sniffManagerPane(run panel.Runner, outer string) (string, bool) {
	out, err := run("list-panes", "-t", outer+":", "-F", "#{pane_id}\t#{@duck_panel_role}\t#{pane_current_command}")
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) == 3 && strings.TrimSpace(f[1]) == "" && strings.TrimSpace(f[2]) == "claude" {
			return f[0], true
		}
	}
	return "", false
}

// managerPane finds the workspace's main claude pane. @duck_manager is the
// authority when it points at a live claude pane; the old role-less claude scan
// remains as a fallback/healer and restamps the option when it finds a match.
// `manager` is the reserved name for this endpoint in the org model.
func managerPane(run panel.Runner, outer string) (string, bool) {
	if out, err := run("show-options", "-t", outer, "-v", managerOption); err == nil {
		if pane := strings.TrimSpace(out); validateManagerPane(run, pane) {
			return pane, true
		}
	}
	pane, ok := sniffManagerPane(run, outer)
	if !ok {
		return "", false
	}
	_, _ = run("set-option", "-t", outer, managerOption, pane)
	return pane, true
}

// awaitComposer delegates to channel.AwaitComposer — one composer-readiness
// check shared by the routines fire path and one-call spawn+send.
func awaitComposer(run panel.Runner, paneID string, timeout time.Duration) bool {
	return channel.AwaitComposer(run, paneID, timeout)
}

// healPersistent recreates every Persistent workspace record whose tmux session
// is no longer live, so persistent workspaces survive a hub reboot. Each healed
// session is minted headless under the record's OWN name (not a fresh derived
// id) so its identity is stable across reboots, and its @duck_dir (and
// @duck_parent, when the record names one) are restamped. Best-effort
// throughout: a ledger-read or tmux failure logs and is skipped — healing must
// never stop the tick's firing pass.
func healPersistent(run panel.Runner, logw io.Writer) {
	recs, err := wsStore().All()
	if err != nil {
		return // no ledger / read error: nothing to heal, and the sweep still fires.
	}
	live := liveSessionNames(run)
	for _, r := range recs {
		if live[r.Name] {
			if _, ok := managerPane(run, r.Name); ok {
				_, _ = run("set-option", "-u", "-t", r.Name, "@duck_manager_down")
				continue
			}
			if !r.Persistent {
				_, _ = run("set-option", "-t", r.Name, "@duck_manager_down", "1")
				continue
			}
			if _, err := run("send-keys", "-t", r.Name, manager.Line(nil), "Enter"); err != nil {
				fmt.Fprintf(logw, "routines: heal %s: relaunch manager: %v\n", r.Name, err)
				continue
			}
			if _, err := stampManagerPane(run, r.Name); err != nil {
				fmt.Fprintf(logw, "routines: heal %s: stamp @duck_manager: %v\n", r.Name, err)
			}
			_, _ = run("set-option", "-u", "-t", r.Name, "@duck_manager_down")
			fmt.Fprintf(logw, "routines: healed persistent workspace %s manager\n", r.Name)
			continue
		}
		if !r.Persistent {
			continue
		}
		abs, err := paths.Expand(r.Dir)
		if err != nil {
			fmt.Fprintf(logw, "routines: heal %s: bad dir %q: %v\n", r.Name, r.Dir, err)
			continue
		}
		tilde := paths.Contract(abs)
		if _, err := run("new-session", "-d", "-s", r.Name, "-c", abs); err != nil {
			fmt.Fprintf(logw, "routines: heal %s: new-session: %v\n", r.Name, err)
			continue
		}
		if _, err := run("set-option", "-t", r.Name, "@duck_dir", tilde); err != nil {
			fmt.Fprintf(logw, "routines: heal %s: set @duck_dir: %v\n", r.Name, err)
		}
		if r.Parent != "" {
			if _, err := run("set-option", "-t", r.Name, "@duck_parent", r.Parent); err != nil {
				fmt.Fprintf(logw, "routines: heal %s: set @duck_parent: %v\n", r.Name, err)
			}
		}
		// A healed workspace is an employee whose manager is claude in the main
		// pane, so the org self-restarts: launch the manager into the fresh
		// session's pane the same way bare `duck` does (bare `claude`, so the pane
		// shell's function owns profiles; channel flags unless DUCK_NO_CHANNELS).
		// Best-effort — a send failure logs but never stops the heal/tick.
		launched := false
		if _, err := run("send-keys", "-t", r.Name, manager.Line(nil), "Enter"); err != nil {
			fmt.Fprintf(logw, "routines: heal %s: launch manager: %v\n", r.Name, err)
		} else if !manager.ChannelsWired(nil) && !r.Channels {
			launched = true
			// Stamp the record channel-aware once the manager launched with channel
			// flags, so the ledger reflects reality. Best-effort.
			r.Channels = true
			if err := wsStore().Save(r); err != nil {
				fmt.Fprintf(logw, "routines: heal %s: stamp channels: %v\n", r.Name, err)
			}
		} else {
			launched = true
		}
		if launched {
			if _, err := stampManagerPane(run, r.Name); err != nil {
				fmt.Fprintf(logw, "routines: heal %s: stamp @duck_manager: %v\n", r.Name, err)
			}
		}
		fmt.Fprintf(logw, "routines: healed persistent workspace %s (%s)\n", r.Name, tilde)
	}
}

// liveSessionNames returns the set of live tmux session names on the hub. An
// empty/dead server yields an empty set (not an error) so heal simply treats
// every record as needing revival — which is correct after a reboot.
func liveSessionNames(run panel.Runner) map[string]bool {
	names := map[string]bool{}
	out, err := run("list-sessions", "-F", "#{session_name}")
	if err != nil {
		return names
	}
	for _, n := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if n = strings.TrimSpace(n); n != "" {
			names[n] = true
		}
	}
	return names
}
