// `duck routines` — the workspace's job description (DESIGN: docs/ROUTINES.md).
// A workspace is a manager (claude in the main pane) with a flock of
// executors; routines are what those employees do on a schedule. Definitions
// are pairs of files under <project-sync-root>/.duck/routines/<workspace>/
// (<name>.toml + <name>.md) — project content, synced+versioned alongside pads,
// but owned by the WORKSPACE. The hub fires the due ones on a systemd/launchd
// timer (finding them via a hub-local project index), each fire landing in that
// workspace's `runs` tab.
//
//	duck routines                 list this workspace's routines (--all: every workspace)
//	duck routines add <name> …    create a routine here (writes the files, marks the workspace persistent)
//	duck routines rm <name>       delete a routine here
//	duck routines fire <name>     manually trigger a routine of this workspace
//	duck routines install         put the tick on the hub's native timer
//	duck routines tick            hidden: the timer's entrypoint (one sweep)
package command

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/DigiBugCat/duck/internal/channel"
	"github.com/DigiBugCat/duck/internal/panel"
	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/DigiBugCat/duck/internal/routines"
	"github.com/DigiBugCat/duck/internal/workspaces"
)

const routinesSystemdUnit = "duck-routines"

var (
	routinesAll bool
	routinesTSV bool
)

var routinesCmd = &cobra.Command{
	Use:   "routines",
	Short: "The workspace's job description: scheduled + manual codex runs",
	RunE: func(c *cobra.Command, args []string) error {
		return listRoutines(c)
	},
}

// currentWorkspace resolves the workspace whose routines we operate on: the
// enclosing tmux session. Routines are workspace-owned, so outside tmux there
// is no answer.
func currentWorkspace(run panel.Runner) (string, error) {
	if !panel.InsideTmux() {
		return "", fmt.Errorf("not inside a duck workspace — routines belong to a workspace; run this inside one")
	}
	return panel.CurrentSession(run)
}

func listRoutines(c *cobra.Command) error {
	run := panel.ExecRunner

	// Enumerate (root, workspace) pairs. --all is a cross-project roll-up from
	// the index + on-disk dirs (never touches tmux); the single case resolves
	// the current workspace's root via the sync resolver.
	var refs []routines.WSRef
	if routinesAll {
		all, err := routines.AllWorkspaces()
		if err != nil {
			return err
		}
		refs = all
	} else {
		ws, err := currentWorkspace(run)
		if err != nil {
			return err
		}
		root, err := routines.SyncRoot(run, ws)
		if err != nil {
			return err
		}
		refs = []routines.WSRef{{Root: root, Workspace: ws}}
	}

	state, err := routines.LoadState()
	if err != nil {
		return err
	}

	var tw *tabwriter.Writer
	if !routinesTSV {
		tw = tabwriter.NewWriter(c.OutOrStdout(), 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "WORKSPACE\tROUTINE\tTRIGGER\tSCHEDULE\tMODEL\tLAST FIRE\tNEXT FIRE\tSTATUS")
	}
	any := false
	for _, ref := range refs {
		ws := ref.Workspace
		defs, lerr := routines.LoadWorkspace(ref.Root, ws)
		if lerr != nil {
			fmt.Fprintf(c.ErrOrStderr(), "routines: skip %s: %v\n", ws, lerr)
			continue
		}
		for _, d := range defs {
			any = true
			sched := d.Schedule
			if d.Trigger == routines.TriggerHeartbeat {
				sched = d.Interval.String()
			}
			if sched == "" {
				sched = "—"
			}
			model := d.Model
			if model == "" {
				model = "—"
			}
			now := time.Now()
			lastT := state.LastFire[routines.Key(ref.Root, ws, d.Name)]
			last := "never"
			if !lastT.IsZero() {
				last = lastT.In(routines.Location).Format("Jan 2 15:04")
			}
			next := "—"
			if t := d.NextFire(lastT, now); !t.IsZero() {
				next = t.In(routines.Location).Format("Jan 2 15:04")
			}
			status := routineStatus(run, ws, d.Name)
			if routinesTSV {
				// Machine row carries one extra trailing field: the prompt .md
				// path (free-text LAST, per the tmux-parsing house rule) — the
				// roster's card/edit affordances need it.
				mdPath := ""
				if dir, derr := routines.WorkspaceDir(ref.Root, ws); derr == nil {
					mdPath = filepath.Join(dir, d.Name+".md")
				}
				fmt.Fprintf(c.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", ws, d.Name, d.Trigger, sched, model, last, next, status, mdPath)
			} else {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", ws, d.Name, d.Trigger, sched, model, last, next, status)
			}
		}
	}
	if tw != nil {
		if err := tw.Flush(); err != nil {
			return err
		}
		if !any {
			fmt.Fprintln(c.OutOrStdout(), "no routines here (create one: duck routines add <name> --cron '0 9 * * *' <prompt…>)")
		}
	}
	return nil
}

// routineStatus reports the live state of a routine's most recent run pane:
// the channel status ("working"/"done"/"idle") if a pane by that name exists
// in the workspace, else "—" (no run yet / workspace closed).
func routineStatus(run panel.Runner, ws, name string) string {
	ref, err := channel.FindAgent(run, ws, name)
	if err != nil {
		return "—"
	}
	return channel.StatusByWindow(run, ref.WindowID)
}

var (
	addCron    string
	addEvery   time.Duration
	addManual  bool
	addManager bool
	addReport  string
	addModel   string
	addEffort  string
)

var routinesAddCmd = &cobra.Command{
	Use:   "add <name> [flags] <prompt…>",
	Short: "Create a routine in this workspace (a standing duty for its executor flock)",
	Long: `Create a routine owned by the current workspace: writes
<project-sync-root>/.duck/routines/<workspace>/<name>.toml + <name>.md (project
content, synced alongside pads), records the project in the hub-local index, and
marks the workspace persistent in the ledger so its schedule survives hub reboots.

Pick exactly one trigger (default --manual):
  --cron "0 9 * * *"   fire on a cron schedule (waits for its next slot)
  --every 15m          heartbeat: ONE persistent codex thread, a beat per interval
  --manual             fire only on demand (duck routines fire <name>)

  --manager            deliver the prompt to this workspace's manager claude
                       instead of spawning a codex executor
  --report none        don't include this routine's completions in digests`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(c *cobra.Command, args []string) error {
		run := panel.ExecRunner
		ws, err := currentWorkspace(run)
		if err != nil {
			return err
		}
		name := args[0]
		prompt := strings.TrimSpace(strings.Join(args[1:], " "))
		if prompt == "" {
			return fmt.Errorf("empty prompt — the .md is the job description")
		}

		triggers := 0
		var body strings.Builder
		switch {
		case addCron != "":
			triggers++
			fmt.Fprintf(&body, "trigger = %q\nschedule = %q\n", "cron", addCron)
		}
		if addEvery > 0 {
			triggers++
			fmt.Fprintf(&body, "trigger = %q\ninterval = %q\n", "heartbeat", addEvery.String())
		}
		if addManual {
			triggers++
			fmt.Fprintf(&body, "trigger = %q\n", "manual")
		}
		if triggers == 0 {
			fmt.Fprintf(&body, "trigger = %q\n", "manual")
		}
		if triggers > 1 {
			return fmt.Errorf("pick ONE trigger: --cron, --every, or --manual")
		}
		if addManager {
			fmt.Fprintf(&body, "target = %q\n", "manager")
		}
		if addReport != "" {
			fmt.Fprintf(&body, "report = %q\n", addReport)
		}
		if addModel != "" {
			fmt.Fprintf(&body, "model = %q\n", addModel)
		}
		if addEffort != "" {
			fmt.Fprintf(&body, "effort = %q\n", addEffort)
		}

		// Resolve the project sync-root this workspace's defs live under BEFORE
		// writing — a routine with no owning location cannot be written (unlike
		// the best-effort markWorkspacePersistent below, this is a hard error).
		root, err := routines.SyncRoot(run, ws)
		if err != nil {
			return fmt.Errorf("resolve project root for %s: %w", ws, err)
		}
		if root == "" {
			return fmt.Errorf("workspace %s has no resolvable project root — cannot place routine", ws)
		}
		dir, err := routines.WorkspaceDir(root, ws)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		tomlPath := filepath.Join(dir, name+".toml")
		mdPath := filepath.Join(dir, name+".md")
		if _, err := os.Stat(tomlPath); err == nil {
			return fmt.Errorf("routine %q already exists here (duck routines rm %s first)", name, name)
		}
		if err := os.WriteFile(tomlPath, []byte(body.String()), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(mdPath, []byte(prompt+"\n"), 0o644); err != nil {
			os.Remove(tomlPath)
			return err
		}
		// Validate by parsing exactly as the tick will; a bad definition must
		// not survive on disk.
		if _, err := routines.LoadWorkspace(root, ws); err != nil {
			os.Remove(tomlPath)
			os.Remove(mdPath)
			return err
		}
		// Record the project in the hub-local index so the tick finds it (and
		// the future machine-wide view can roll it up). A failed IndexAdd means
		// the tick never sees the routine — worse than a clean failure, so roll
		// back the files.
		if err := routines.IndexAdd(root); err != nil {
			os.Remove(tomlPath)
			os.Remove(mdPath)
			return fmt.Errorf("index project %s: %w", root, err)
		}

		markWorkspacePersistent(c, run, ws)

		switch {
		case addCron != "":
			fmt.Fprintf(c.OutOrStdout(), "added %s (cron %q) — first fire at its next slot\n", name, addCron)
		case addEvery > 0:
			fmt.Fprintf(c.OutOrStdout(), "added %s (heartbeat every %s) — its executor thread starts within a minute\n", name, addEvery)
		default:
			fmt.Fprintf(c.OutOrStdout(), "added %s (manual) — fires only on: duck routines fire %s\n", name, name)
		}
		return nil
	},
}

// markWorkspacePersistent stamps Persistent on the workspace's ledger record
// (creating one if needed) so the tick heals it back after a reboot — a
// workspace with standing duties must outlive the tmux server. Best-effort
// and reported, never fatal: the routine files are already written.
func markWorkspacePersistent(c *cobra.Command, run panel.Runner, ws string) {
	dirOut, err := run("show-options", "-t", ws, "-v", "@duck_dir")
	if err != nil {
		fmt.Fprintf(c.ErrOrStderr(), "routines: could not read @duck_dir: %v\n", err)
		return
	}
	tilde := strings.TrimSpace(dirOut)
	if tilde == "" {
		if out, derr := run("display-message", "-p", "-t", ws+":", "#{pane_current_path}"); derr == nil {
			tilde = paths.Contract(strings.TrimSpace(out))
		}
	}
	if tilde == "" {
		fmt.Fprintf(c.ErrOrStderr(), "routines: workspace %s has no directory — not marked persistent\n", ws)
		return
	}
	store := workspaces.NewStore(workspaces.LocalRunner{})
	rec, found, err := store.Load(tilde, ws)
	if err != nil {
		fmt.Fprintf(c.ErrOrStderr(), "routines: could not read workspace record: %v\n", err)
		return
	}
	if !found {
		rec = workspaces.Record{Name: ws, Dir: tilde, Created: time.Now()}
	}
	if rec.Persistent {
		return
	}
	rec.Persistent = true
	rec.Updated = time.Now()
	if err := store.Save(rec); err != nil {
		fmt.Fprintf(c.ErrOrStderr(), "routines: could not update workspace record: %v\n", err)
	}
}

// rmRootForWorkspace resolves the project sync-root for rm. The live path is
// routines.SyncRoot (reads @duck_dir). If the workspace's tmux session is gone
// — SyncRoot fails — it falls back to the workspaces ledger record's Dir (the
// same authoritative source healPersistent trusts), running it through the sync
// resolver so it lands on the identical root add used. Errors out rather than
// guessing when nothing resolves.
func rmRootForWorkspace(run panel.Runner, ws string) (string, error) {
	if root, err := routines.SyncRoot(run, ws); err == nil && root != "" {
		return root, nil
	}
	// Dead session: recover the dir from the ledger by workspace NAME (we have
	// no dir to key Load on — that's precisely what's missing). Match across all
	// records; a name is unique per hub.
	store := workspaces.NewStore(workspaces.LocalRunner{})
	recs, err := store.All()
	if err != nil {
		return "", fmt.Errorf("workspace %s is not live and the ledger is unreadable: %w", ws, err)
	}
	for _, rec := range recs {
		if rec.Name == ws && rec.Dir != "" {
			if r := routines.SyncRootFn(rec.Dir); r != "" {
				return r, nil
			}
			return rec.Dir, nil
		}
	}
	return "", fmt.Errorf("workspace %s is not live and has no ledger record — cannot locate its routines", ws)
}

var routinesRmCmd = &cobra.Command{
	Use:   "rm <name>",
	Short: "Delete a routine of this workspace",
	Args:  cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		run := panel.ExecRunner
		ws, err := currentWorkspace(run)
		if err != nil {
			return err
		}
		root, err := rmRootForWorkspace(run, ws)
		if err != nil {
			return err
		}
		dir, err := routines.WorkspaceDir(root, ws)
		if err != nil {
			return err
		}
		tomlPath := filepath.Join(dir, args[0]+".toml")
		if _, err := os.Stat(tomlPath); err != nil {
			return fmt.Errorf("no routine %q in workspace %s", args[0], ws)
		}
		if err := os.Remove(tomlPath); err != nil {
			return err
		}
		_ = os.Remove(filepath.Join(dir, args[0]+".md"))
		// Tidy the now-possibly-empty workspace subdir (best-effort — os.Remove
		// only succeeds if it's empty, which is exactly what we want).
		_ = os.Remove(dir)
		// Drop the project from the index ONLY when its LAST routine — across
		// EVERY workspace under this root — is gone. Another workspace on the
		// same root (the aviary umbrella case) may still have live routines;
		// dropping the entry then would make the tick silently skip that project.
		if has, herr := routines.RootHasRoutines(root); herr == nil && !has {
			if ierr := routines.IndexRemove(root); ierr != nil {
				fmt.Fprintf(c.ErrOrStderr(), "routines: removed %s but could not prune index for %s: %v\n", args[0], root, ierr)
			}
		}
		fmt.Fprintf(c.OutOrStdout(), "removed %s from %s\n", args[0], ws)
		return nil
	},
}

// routinesEditCmd opens a routine's job description (.md) as a buffer pane —
// the same live viewer/editor pads get. The next fire reads the file fresh,
// so an edit takes effect without any re-registration.
var routinesEditCmd = &cobra.Command{
	Use:   "edit <name>",
	Short: "Open a routine's prompt (.md) in a buffer pane; next fire picks it up",
	Args:  cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		run := panel.ExecRunner
		ws, err := currentWorkspace(run)
		if err != nil {
			return err
		}
		root, err := routines.SyncRoot(run, ws)
		if err != nil {
			return err
		}
		dir, err := routines.WorkspaceDir(root, ws)
		if err != nil {
			return err
		}
		mdPath := filepath.Join(dir, args[0]+".md")
		if _, err := os.Stat(mdPath); err != nil {
			return fmt.Errorf("no routine %q in workspace %s (see: duck routines)", args[0], ws)
		}
		return openBufferPath(c, mdPath)
	},
}

// openBufferPath opens a file with the PAD treatment (glow viewer, e-to-edit,
// live reload on disk writes) as a buffer pane in the current workspace —
// mirrors `duck edit <path>` but keeps the pad-style viewer, which suits
// routine prompts (an agent edit repaints an open card/pad instantly).
func openBufferPath(c *cobra.Command, path string) error {
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
	if err := panel.Open(run, outer, comp, bin); err != nil {
		return err
	}
	name := strings.TrimSuffix(filepath.Base(path), ".md")
	_, err = panel.Spawn(run, outer, name, dir, panel.PadCmd(path), panel.KindBuffer)
	return err
}

var routinesFireCmd = &cobra.Command{
	Use:   "fire <name>",
	Short: "Manually trigger a routine of this workspace (also forces a cron one)",
	Args:  cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		run := panel.ExecRunner
		ws, err := currentWorkspace(run)
		if err != nil {
			return err
		}
		root, err := routines.SyncRoot(run, ws)
		if err != nil {
			return err
		}
		defs, err := routines.LoadWorkspace(root, ws)
		if err != nil {
			return err
		}
		for _, d := range defs {
			if d.Name == args[0] {
				now := time.Now()
				if !routines.Fire(run, d, now, c.OutOrStdout()) {
					return fmt.Errorf("fire %s failed (see output above)", d.Name)
				}
				// A forced fire is a real beat: record it so a cron routine
				// doesn't double-fire at its next slot.
				state, serr := routines.LoadState()
				if serr == nil {
					state.LastFire[routines.Key(root, ws, d.Name)] = now
					serr = routines.SaveState(state)
				}
				if serr != nil {
					fmt.Fprintf(c.ErrOrStderr(), "routines: fired but could not record last-fire: %v\n", serr)
				}
				return nil
			}
		}
		return fmt.Errorf("no routine %q in workspace %s (see: duck routines)", args[0], ws)
	},
}

// routinesTickCmd is the timer's entrypoint; not for humans.
var routinesTickCmd = &cobra.Command{
	Use:    "tick",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(c *cobra.Command, args []string) error {
		return routines.Tick(panel.ExecRunner, time.Now(), c.ErrOrStderr())
	},
}

var (
	routinesInstall   bool
	routinesUninstall bool
	routinesEvery     time.Duration
)

var routinesInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Put the tick on the hub's native timer (systemd on Linux, launchd on macOS)",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, args []string) error {
		w, err := build()
		if err != nil {
			return err
		}
		if routinesUninstall {
			return uninstallRoutinesTimer(w.client)
		}
		return installRoutinesTimer(w.client, routinesEvery)
	},
}

// routinesLabel is the hub launchd agent's label (macOS hub).
const routinesLabel = "com.duckcli.routines"

// hubDuckBin resolves the absolute path of the duck binary on the hub, so the
// timer's ExecStart is absolute (systemd/launchd require it). Falls back to the
// install.sh location when `command -v` finds nothing.
func hubDuckBin(r evictRunner) string {
	if out, err := r.Run("command -v duck 2>/dev/null"); err == nil {
		if p := strings.TrimSpace(out); p != "" {
			return p
		}
	}
	return "$HOME/.local/bin/duck"
}

// installRoutinesTimer schedules `duck routines tick` on the hub's native timer
// (systemd --user on Linux, launchd on macOS), running every `every`. Cloned
// from installEvictAgent; re-running overwrites and reloads.
func installRoutinesTimer(r evictRunner, every time.Duration) error {
	goos, err := hubGOOS(r)
	if err != nil {
		return fmt.Errorf("detect hub OS: %w", err)
	}
	bin := hubDuckBin(r)
	switch goos {
	case "darwin":
		return installRoutinesLaunchd(r, bin, every)
	case "linux":
		return installRoutinesSystemd(r, bin, every)
	default:
		return fmt.Errorf("duck routines install: unsupported hub OS %q (want macOS or Linux)", goos)
	}
}

func installRoutinesSystemd(r evictRunner, bin string, every time.Duration) error {
	// systemd does no shell expansion in ExecStart; the hubDuckBin fallback
	// uses $HOME, which systemd spells %h.
	bin = strings.ReplaceAll(bin, "$HOME", "%h")
	service := fmt.Sprintf(`[Unit]
Description=duck: fire due routines (see: duck routines)

[Service]
Type=oneshot
Environment=PATH=/usr/local/bin:/usr/bin:/bin:%%h/.local/bin
ExecStart=%s routines tick
`, bin)
	timer := fmt.Sprintf(`[Unit]
Description=duck: periodic routines tick (see: duck routines)

[Timer]
OnBootSec=1min
OnUnitActiveSec=%d
Persistent=true

[Install]
WantedBy=timers.target
`, int64(every/time.Second))
	unitDir := "~/.config/systemd/user/"
	if _, err := r.RunInput(
		"mkdir -p ~/.config/systemd/user && cat > "+unitDir+routinesSystemdUnit+".service",
		strings.NewReader(service)); err != nil {
		return fmt.Errorf("write systemd service: %w", err)
	}
	if _, err := r.RunInput(
		"cat > "+unitDir+routinesSystemdUnit+".timer", strings.NewReader(timer)); err != nil {
		return fmt.Errorf("write systemd timer: %w", err)
	}
	activate := "export XDG_RUNTIME_DIR=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}; " +
		`loginctl enable-linger "$(id -un)" 2>/dev/null || sudo -n loginctl enable-linger "$(id -un)" 2>/dev/null || true; ` +
		"systemctl --user daemon-reload && " +
		"systemctl --user enable --now " + routinesSystemdUnit + ".timer"
	if _, err := r.Run(activate); err != nil {
		return fmt.Errorf("enable systemd timer: %w", err)
	}
	fmt.Printf("installed: hub fires due routines every %s (systemd --user timer %s)\n", every, routinesSystemdUnit)
	return nil
}

func installRoutinesLaunchd(r evictRunner, bin string, every time.Duration) error {
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>/bin/sh</string>
		<string>-c</string>
		<string>%s routines tick</string>
	</array>
	<key>StartInterval</key><integer>%d</integer>
	<key>StandardOutPath</key><string>/tmp/duck-routines.log</string>
	<key>StandardErrorPath</key><string>/tmp/duck-routines.log</string>
</dict>
</plist>
`, routinesLabel, bin, int64(every/time.Second))
	plistPath := "~/Library/LaunchAgents/" + routinesLabel + ".plist"
	if _, err := r.RunInput(
		"mkdir -p ~/Library/LaunchAgents && cat > "+plistPath, strings.NewReader(plist)); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}
	reload := fmt.Sprintf(
		"launchctl bootout gui/$(id -u)/%s 2>/dev/null; "+
			"launchctl bootstrap gui/$(id -u) %s 2>/dev/null || launchctl load %s",
		routinesLabel, plistPath, plistPath)
	if _, err := r.Run(reload); err != nil {
		return fmt.Errorf("load launchd agent: %w", err)
	}
	fmt.Printf("installed: hub fires due routines every %s (launchd agent %s)\n", every, routinesLabel)
	return nil
}

// uninstallRoutinesTimer removes the hub routines timer (units/plist);
// definitions and state are left intact.
func uninstallRoutinesTimer(r evictRunner) error {
	goos, err := hubGOOS(r)
	if err != nil {
		return fmt.Errorf("detect hub OS: %w", err)
	}
	var cmd string
	switch goos {
	case "darwin":
		plistPath := "~/Library/LaunchAgents/" + routinesLabel + ".plist"
		cmd = fmt.Sprintf(
			"launchctl bootout gui/$(id -u)/%s 2>/dev/null; launchctl unload %s 2>/dev/null; rm -f %s",
			routinesLabel, plistPath, plistPath)
	case "linux":
		unitDir := "~/.config/systemd/user/"
		cmd = "export XDG_RUNTIME_DIR=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}; " +
			"systemctl --user disable --now " + routinesSystemdUnit + ".timer 2>/dev/null; " +
			"rm -f " + unitDir + routinesSystemdUnit + ".service " + unitDir + routinesSystemdUnit + ".timer; " +
			"systemctl --user daemon-reload 2>/dev/null || true"
	default:
		return fmt.Errorf("duck routines install --uninstall: unsupported hub OS %q (want macOS or Linux)", goos)
	}
	if _, err := r.Run(cmd); err != nil {
		return err
	}
	fmt.Println("uninstalled hub routines timer")
	return nil
}

func init() {
	routinesCmd.Flags().BoolVar(&routinesAll, "all", false, "list every workspace's routines, not just this one's")
	routinesCmd.Flags().BoolVar(&routinesTSV, "tsv", false, "machine-readable tab-separated output (no header)")
	_ = routinesCmd.Flags().MarkHidden("tsv")
	routinesAddCmd.Flags().StringVar(&addCron, "cron", "", "cron schedule (standard 5-field expression)")
	routinesAddCmd.Flags().DurationVar(&addEvery, "every", 0, "heartbeat interval (e.g. 15m)")
	routinesAddCmd.Flags().BoolVar(&addManual, "manual", false, "fire only on demand (the default)")
	routinesAddCmd.Flags().BoolVar(&addManager, "manager", false, "deliver to this workspace's manager claude instead of a codex executor")
	routinesAddCmd.Flags().StringVar(&addReport, "report", "", `completion reporting: "digest" (default) or "none"`)
	routinesAddCmd.Flags().StringVar(&addModel, "model", "", "executor model alias — codex-native only (e.g. gpt-5.4-mini, gpt-5.3-codex-spark); default = codex config default")
	routinesAddCmd.Flags().StringVar(&addEffort, "effort", "", "executor reasoning effort: low|medium|high; default = codex config default")
	routinesInstallCmd.Flags().BoolVar(&routinesUninstall, "uninstall", false, "remove the hub routines timer")
	routinesInstallCmd.Flags().DurationVar(&routinesEvery, "every", time.Minute, "tick interval for the installed hub timer")
	routinesCmd.AddCommand(routinesAddCmd, routinesRmCmd, routinesFireCmd, routinesEditCmd, routinesTickCmd, routinesInstallCmd)
	rootCmd.AddCommand(routinesCmd)
}
