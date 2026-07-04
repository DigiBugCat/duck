// `duck routines` — the workspace's job description (DESIGN: docs/ROUTINES.md).
// A routine is a pair of files under <project>/.duck/routines/ (<name>.toml +
// <name>.md); the hub fires the due ones on a systemd/launchd timer, each fire
// landing as a codex executor pane in the project workspace's `runs` tab.
//
//	duck routines                 list routines across live + registered projects
//	duck routines enable|disable  register/unregister the current project
//	duck routines fire <name>     manually trigger a routine of the current project
//	duck routines install         put the tick on the hub's native timer
//	duck routines tick            hidden: the timer's entrypoint (one sweep)
//
// Phase 1: trigger=cron/manual, target=run. Heartbeats and target=manager are
// recognized but not yet fired.
package command

import (
	"fmt"
	"os"
	"os/exec"
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

var routinesCmd = &cobra.Command{
	Use:   "routines",
	Short: "The workspace's job description: scheduled + manual codex runs",
	RunE: func(c *cobra.Command, args []string) error {
		return listRoutines(c)
	},
}

// currentProject resolves the project root for enable/disable/fire: the git
// toplevel of the CWD if inside a repo, else the cleaned CWD. Absolute.
func currentProject() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if out, gerr := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output(); gerr == nil {
		if root := strings.TrimSpace(string(out)); root != "" {
			return filepath.Clean(root), nil
		}
	}
	return filepath.Clean(cwd), nil
}

func listRoutines(c *cobra.Command) error {
	run := panel.ExecRunner

	projects, err := routines.SweepProjects(run)
	if err != nil {
		return err
	}
	// Always include the current project so `duck routines` in an unregistered
	// repo still shows its (as-yet-unscheduled) definitions.
	if proj, perr := currentProject(); perr == nil {
		projects = appendUnique(projects, proj)
	}

	state, err := routines.LoadState()
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(c.OutOrStdout(), 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "PROJECT\tROUTINE\tTRIGGER\tSCHEDULE\tLAST FIRE\tSTATUS")
	any := false
	for _, proj := range projects {
		defs, lerr := routines.Load(proj)
		if lerr != nil {
			fmt.Fprintf(c.ErrOrStderr(), "routines: skip %s: %v\n", proj, lerr)
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
			last := "never"
			if t, ok := state.LastFire[routines.Key(d.Dir, d.Name)]; ok && !t.IsZero() {
				last = t.Local().Format("Jan 2 15:04")
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
				filepath.Base(proj), d.Name, d.Trigger, sched, last, routineStatus(run, proj, d.Name))
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if !any {
		fmt.Fprintln(c.OutOrStdout(), "no routines (add .duck/routines/<name>.toml + <name>.md, then: duck routines enable)")
	}
	return nil
}

// routineStatus reports the live state of a routine's most recent run pane:
// the channel status ("working"/"done"/"idle") if a pane by that name exists in
// the project's workspace, else "—" (no run yet / workspace closed).
func routineStatus(run panel.Runner, proj, name string) string {
	outer, ok := liveWorkspace(run, proj)
	if !ok {
		return "—"
	}
	ref, err := channel.FindAgent(run, outer, name)
	if err != nil {
		return "—"
	}
	return channel.StatusByWindow(run, ref.WindowID)
}

// liveWorkspace finds the live tmux session for a project dir (by @duck_dir),
// or ok=false. Both tilde and absolute forms are matched.
func liveWorkspace(run panel.Runner, absDir string) (string, bool) {
	tilde := paths.Contract(absDir)
	out, err := run("list-sessions", "-F", "#{session_name}\t#{@duck_dir}\t#{@duck_panel_of}")
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) < 2 {
			continue
		}
		if len(f) == 3 && strings.TrimSpace(f[2]) != "" {
			continue // companion
		}
		dir := strings.TrimSpace(f[1])
		if dir == tilde || dir == absDir {
			return strings.TrimSpace(f[0]), true
		}
	}
	return "", false
}

func appendUnique(xs []string, x string) []string {
	for _, e := range xs {
		if e == x {
			return xs
		}
	}
	return append(xs, x)
}

// setPersistentForCurrentDir marks (or clears) Persistent on the live
// workspace's durable record for proj, so a persistent workspace is healed back
// after a reboot and the tick's sweep sees the dir even with no file-registry
// entry. It is the DURABLE half of enable/disable, in ADDITION to the file
// registry. When no live workspace exists in proj there is nothing to stamp — we
// fall back to the file registry alone (exactly the old behavior), so enabling
// still works with every workspace closed. Best-effort and reported, never
// fatal: a hub/ledger failure must not fail the enable/disable the user asked
// for (the file registry write has already succeeded by the time this runs).
func setPersistentForCurrentDir(c *cobra.Command, proj string, persistent bool) {
	w, err := build()
	if err != nil {
		return // no hub configured: file registry alone (the pre-ledger behavior).
	}
	tilde := paths.Contract(proj)
	s, ok, err := w.sessions.Recent(tilde)
	if err != nil || !ok {
		return // no live workspace in this dir: nothing to stamp.
	}
	store := workspaces.NewStore(w.client)
	rec, found, err := store.Load(tilde, s.Name)
	if err != nil {
		fmt.Fprintf(c.ErrOrStderr(), "routines: could not read workspace record: %v\n", err)
		return
	}
	if !found {
		rec = workspaces.Record{Name: s.Name, Dir: tilde}
	}
	if rec.Persistent == persistent {
		return // already in the desired state.
	}
	rec.Persistent = persistent
	if err := store.Save(rec); err != nil {
		fmt.Fprintf(c.ErrOrStderr(), "routines: could not update workspace record: %v\n", err)
	}
}

var routinesEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Register the current project so its routines fire even with no workspace open",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, args []string) error {
		proj, err := currentProject()
		if err != nil {
			return err
		}
		if err := routines.Enable(proj); err != nil {
			return err
		}
		// Durable form: if a live workspace exists here, mark its record Persistent
		// so it heals back after a reboot (the file registry is legacy — see
		// routines.SweepProjects).
		setPersistentForCurrentDir(c, proj, true)
		fmt.Fprintf(c.OutOrStdout(), "enabled routines for %s\n", proj)
		return nil
	},
}

var routinesDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Unregister the current project (its files stay; the tick stops sweeping it)",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, args []string) error {
		proj, err := currentProject()
		if err != nil {
			return err
		}
		if err := routines.Disable(proj); err != nil {
			return err
		}
		setPersistentForCurrentDir(c, proj, false)
		fmt.Fprintf(c.OutOrStdout(), "disabled routines for %s\n", proj)
		return nil
	},
}

var routinesFireCmd = &cobra.Command{
	Use:   "fire <name>",
	Short: "Manually trigger a routine of the current project (also forces a cron one)",
	Args:  cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		proj, err := currentProject()
		if err != nil {
			return err
		}
		defs, err := routines.Load(proj)
		if err != nil {
			return err
		}
		for _, d := range defs {
			if d.Name == args[0] {
				now := time.Now()
				if !routines.Fire(panel.ExecRunner, d, now, c.OutOrStdout()) {
					return fmt.Errorf("fire %s failed (see output above)", d.Name)
				}
				// A forced fire is a real beat: record it so a cron routine
				// doesn't double-fire at its next slot.
				state, serr := routines.LoadState()
				if serr == nil {
					state.LastFire[routines.Key(d.Dir, d.Name)] = now
					serr = routines.SaveState(state)
				}
				if serr != nil {
					fmt.Fprintf(c.ErrOrStderr(), "routines: fired but could not record last-fire: %v\n", serr)
				}
				return nil
			}
		}
		return fmt.Errorf("no routine %q in %s (looked in %s)", args[0], filepath.Base(proj), filepath.Join(proj, ".duck/routines"))
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
	routinesInstallCmd.Flags().BoolVar(&routinesUninstall, "uninstall", false, "remove the hub routines timer")
	routinesInstallCmd.Flags().DurationVar(&routinesEvery, "every", time.Minute, "tick interval for the installed hub timer")
	routinesCmd.AddCommand(routinesEnableCmd, routinesDisableCmd, routinesFireCmd, routinesTickCmd, routinesInstallCmd)
	rootCmd.AddCommand(routinesCmd)
}
