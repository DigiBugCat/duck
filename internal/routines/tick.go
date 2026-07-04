// Firing: turning due routines into panes. This is the hub-side half of the
// package (Load/Due compute WHAT should fire; here we make it happen). A tick
// sweeps every registered/live project, fires the cron routines that are due,
// and persists last-fire once. Everything routes through duck's own verbs
// (session create, EnsureCompanion, Spawn) — tmux stays the only database.
//
// Phase 1 scope: trigger=cron/manual, target=run only. Heartbeats and
// target=manager are recognized but not yet fired (they log and are skipped);
// see docs/ROUTINES.md phases.
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
	"github.com/DigiBugCat/duck/internal/session"
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

	projects, err := SweepProjects(run)
	if err != nil {
		return fmt.Errorf("enumerate projects: %w", err)
	}

	state, err := LoadState()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	changed := false
	for _, proj := range projects {
		defs, err := Load(proj)
		if err != nil {
			fmt.Fprintf(logw, "routines: skip project %s: %v\n", proj, err)
			continue
		}
		for _, d := range defs {
			if d.Trigger != TriggerCron {
				continue // manual never auto-fires; heartbeat is Phase 2
			}
			last := state.LastFire[Key(d.Dir, d.Name)]
			if last.IsZero() {
				// First sight of this routine: seed last-fire so it waits for
				// its next cron slot. Without the seed a zero last is never due
				// (Due can't distinguish "brand new" from "fired long ago" in a
				// way that wouldn't refire forever).
				state.LastFire[Key(d.Dir, d.Name)] = now
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
				state.LastFire[Key(d.Dir, d.Name)] = now
				changed = true
			}
		}
	}

	if changed {
		if err := SaveState(state); err != nil {
			return fmt.Errorf("save state: %w", err)
		}
	}
	return nil
}

// Fire triggers one routine immediately (the `duck routines fire` path and the
// tick's per-due call share this). It ensures the project workspace exists,
// applies the concurrency guard, and — when clear — spawns the executor pane.
// Returns true if the beat should be recorded (a pane was spawned OR the guard
// dropped the beat); false only on a hard error that left nothing done, so the
// caller can leave last-fire untouched and retry next minute.
func Fire(run panel.Runner, d Def, now time.Time, logw io.Writer) bool {
	if d.Target != TargetRun {
		fmt.Fprintf(logw, "routines: %s/%s target=%s not yet implemented (Phase 2) — skipping\n", d.Dir, d.Name, d.Target)
		return false
	}

	outer, err := ensureWorkspace(run, d.Dir)
	if err != nil {
		fmt.Fprintf(logw, "routines: %s/%s: ensure workspace: %v\n", d.Dir, d.Name, err)
		return false
	}
	if _, err := panel.EnsureCompanion(run, outer, d.Dir); err != nil {
		fmt.Fprintf(logw, "routines: %s/%s: ensure companion: %v\n", d.Dir, d.Name, err)
		return false
	}

	// Concurrency guard: if a pane by this routine's name already exists AND its
	// last codex turn is still open, a previous fire is still running. Drop this
	// beat (return true so we record last-fire and don't refire next minute).
	if ref, err := channel.FindAgent(run, outer, d.Name); err == nil {
		if channel.StatusByWindow(run, ref.WindowID) == "working" {
			fmt.Fprintf(logw, "routines: %s/%s still working — dropping this beat\n", d.Dir, d.Name)
			return true
		}
	}

	cmdline := codexBin() + " exec --dangerously-bypass-approvals-and-sandbox" + notifyArg() + " " + paths.Quote(d.Prompt)
	if _, err := panel.Spawn(run, outer, d.Name, d.Dir, cmdline, panel.KindRun); err != nil {
		fmt.Fprintf(logw, "routines: %s/%s: spawn: %v\n", d.Dir, d.Name, err)
		return false
	}
	fmt.Fprintf(logw, "routines: fired %s/%s (workspace %s)\n", d.Dir, d.Name, outer)
	return true
}

// sweepProjects is the union of three sources, all as absolute deduped paths:
// (a) live workspaces' @duck_dir, (b) the registered projects file, and (c)
// dirs that have at least one Persistent workspace record. Registered projects
// and persistent records both survive all workspaces being closed, so
// automation keeps running across a reboot.
//
// NOTE: the routines-projects file (source b) is now LEGACY — `duck routines
// enable` sets Persistent on the live workspace's record (source c) when one
// exists, which is the durable form. The file union is kept working for
// migration and for the enable-with-no-live-workspace fallback; it is not
// removed here.
func SweepProjects(run panel.Runner) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		abs, err := paths.Expand(p)
		if err != nil || abs == "" || seen[abs] {
			return
		}
		seen[abs] = true
		out = append(out, abs)
	}

	dirs, err := workspaceDirs(run)
	if err != nil {
		return nil, err
	}
	for _, d := range dirs {
		add(d)
	}

	registered, err := Projects()
	if err != nil {
		return nil, err
	}
	for _, p := range registered {
		add(p)
	}

	// Persistent records: a dir with one keeps automation alive even with every
	// workspace closed. Best-effort — a ledger read failure (or empty ledger) must
	// not stop the sweep, which still fires for live + registered projects.
	if recs, err := wsStore().All(); err == nil {
		for _, r := range recs {
			if r.Persistent {
				add(r.Dir)
			}
		}
	}
	return out, nil
}

// workspaceDirs returns the @duck_dir (tilde-form) of every live duck
// workspace, companions excluded. Unlike panel.Workspaces it does NOT filter
// to the current project — the tick must see every project on the hub.
func workspaceDirs(run panel.Runner) ([]string, error) {
	out, err := run("list-sessions", "-F", "#{@duck_dir}\t#{@duck_panel_of}")
	if err != nil {
		// No server / no sessions: not an error for the sweep — registered
		// projects still fire.
		return nil, nil
	}
	var dirs []string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.SplitN(line, "\t", 2)
		dir := strings.TrimSpace(f[0])
		panelOf := ""
		if len(f) == 2 {
			panelOf = strings.TrimSpace(f[1])
		}
		if panelOf != "" || dir == "" {
			continue // companion (plumbing) or a non-duck session
		}
		dirs = append(dirs, dir)
	}
	return dirs, nil
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
		if !r.Persistent || live[r.Name] {
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
		if _, err := run("send-keys", "-t", r.Name, manager.Line(nil), "Enter"); err != nil {
			fmt.Fprintf(logw, "routines: heal %s: launch manager: %v\n", r.Name, err)
		} else if !manager.ChannelsWired(nil) && !r.Channels {
			// Stamp the record channel-aware once the manager launched with channel
			// flags, so the ledger reflects reality. Best-effort.
			r.Channels = true
			if err := wsStore().Save(r); err != nil {
				fmt.Fprintf(logw, "routines: heal %s: stamp channels: %v\n", r.Name, err)
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

// ensureWorkspace returns the tmux session name for absDir, creating a headless
// one when none exists (so a routine fires even with every workspace closed and
// the run stays inspectable later). Reuse is by @duck_dir match, mirroring the
// Recent semantics the attach path uses. absDir is an absolute (expanded) path;
// @duck_dir is stamped tilde-form (the display/Recent key) and -c gets the real
// path (tmux -c does not expand ~).
func ensureWorkspace(run panel.Runner, absDir string) (string, error) {
	tilde := paths.Contract(absDir)
	sessions, err := run("list-sessions", "-F", "#{session_name}\t#{@duck_dir}\t#{@duck_panel_of}")
	if err == nil {
		for _, line := range strings.Split(strings.TrimRight(sessions, "\n"), "\n") {
			f := strings.SplitN(line, "\t", 3)
			if len(f) < 2 {
				continue
			}
			name := strings.TrimSpace(f[0])
			dir := strings.TrimSpace(f[1])
			panelOf := ""
			if len(f) == 3 {
				panelOf = strings.TrimSpace(f[2])
			}
			if panelOf != "" {
				continue
			}
			if dir == tilde || dir == absDir {
				return name, nil
			}
		}
	}

	// None live: mint a fresh one. Derive a tmux-legal id from the dir and add
	// a numeric suffix until it doesn't collide with a live session.
	id := freshSessionID(run, absDir)
	if _, err := run("new-session", "-d", "-s", id, "-c", absDir); err != nil {
		return "", err
	}
	if _, err := run("set-option", "-t", id, "@duck_dir", tilde); err != nil {
		return "", err
	}
	return id, nil
}

// freshSessionID derives a tmux-legal id from absDir and appends -<n> until it
// doesn't collide with a live session name.
func freshSessionID(run panel.Runner, absDir string) string {
	base := session.DeriveID(absDir)
	taken := map[string]bool{}
	if out, err := run("list-sessions", "-F", "#{session_name}"); err == nil {
		for _, n := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			if n = strings.TrimSpace(n); n != "" {
				taken[n] = true
			}
		}
	}
	if !taken[base] {
		return base
	}
	for n := 2; ; n++ {
		cand := fmt.Sprintf("%s-%d", base, n)
		if !taken[cand] {
			return cand
		}
	}
}
