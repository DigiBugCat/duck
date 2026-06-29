// `duck evict`: kill stale (detached, non-looped, idle past --age) tmux
// sessions on the hub to reclaim RAM, leaving a breadcrumb so the picker can
// revive each one later — same tmux name, same dir, and `claude --resume` of
// the conversation that was running there. `duck evict --install` schedules the
// same sweep ON the hub using its native scheduler — a launchd agent on macOS,
// a systemd --user timer on Linux — so the hub evicts on its own timer with no
// duck daemon; `--uninstall` removes it.
package command

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/DigiBugCat/duck/internal/session"
)

// evictRunner is the hub-command surface the eviction installer needs: a remote
// command runner with optional stdin. Both *sshx.Client (the `duck evict` path)
// and *hub.Hub (the `duck hub setup` path) satisfy it, so the scheduler can be
// installed from either entrypoint.
type evictRunner interface {
	Run(remoteCmd string) (string, error)
	RunInput(remoteCmd string, stdin io.Reader) (string, error)
}

// evictLabel is the hub launchd agent's label and plist basename (macOS hub).
const evictLabel = "com.duckcli.evict"

// evictSystemdUnit is the basename of the hub's systemd --user service+timer
// units (Linux hub): ~/.config/systemd/user/duck-evict.{service,timer}.
const evictSystemdUnit = "duck-evict"

// Default eviction knobs, shared by the `duck evict` flags and the
// install-by-default step in `duck hub setup`.
const (
	defaultEvictAge        = 12 * time.Hour
	defaultEvictEvery      = 30 * time.Minute
	defaultEvictRenameIdle = 15 * time.Minute
)

// claudeHookPath is where the SessionStart hook script lives on the hub; the
// settings.json hook entry just invokes it, so upgrading duck upgrades the hook
// by rewriting one file (no settings churn).
const claudeHookPath = `$HOME/.duck/claude-hook.sh`

// claudeHookCmd is the command string registered under hooks.SessionStart in
// the hub's ~/.claude/settings.json.
const claudeHookCmd = `/bin/sh "` + claudeHookPath + `"`

// claudeHookScript is the hub-side Claude Code SessionStart hook (the
// capture-for-exact-revival pattern from tmux-assistant-resurrect). On every
// session start — including resumes, where Claude mints a NEW session_id — it
// stamps onto the surrounding tmux session:
//
//   - @claude_session_id: the session_id from the hook's JSON stdin, so
//     eviction records the EXACT conversation to `claude --resume`;
//   - @claude_resume_args: the launch flags that must survive a revival,
//     read off the live claude process ($PPID — the hook runs as its child)
//     via `ps`. Allowlisted rather than replayed wholesale: --model and
//     --permission-mode (with values) and --dangerously-skip-permissions.
//     An allowlist keeps one-shot positionals (an initial prompt, a stale
//     --resume from a previous revival) from being replayed forever.
//
// Every exit path is 0 so a hook hiccup (no tmux, odd stdin, ps denied) never
// fails a Claude session start.
const claudeHookScript = `#!/bin/sh
# duck: stamp Claude session identity on the surrounding tmux session so
# eviction can revive it exactly (see: duck evict). Managed by duck — rewritten
# on every duck evict --install.
[ -n "$TMUX" ] || exit 0
sid=$(sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
[ -n "$sid" ] || exit 0
tmux set-option @claude_session_id "$sid" 2>/dev/null
keep=""
set -- $(ps -o args= -p $PPID 2>/dev/null)
while [ $# -gt 0 ]; do
  case "$1" in
    --model|--permission-mode)
      if [ $# -ge 2 ]; then keep="$keep $1 $2"; shift 2; else shift; fi ;;
    --dangerously-skip-permissions)
      keep="$keep $1"; shift ;;
    *) shift ;;
  esac
done
tmux set-option @claude_resume_args "${keep# }" 2>/dev/null
exit 0`

var (
	evictAge        time.Duration
	evictEvery      time.Duration
	evictRenameIdle time.Duration
	evictInstall    bool
	evictUninstall  bool
)

var evictCmd = &cobra.Command{
	Use:   "evict",
	Short: "Evict stale sessions to save hub RAM (resumable from the picker)",
	Long: `Kill detached, non-looped tmux sessions idle past --age. Attached
sessions and sessions running a /loop are never touched. Each evicted session
leaves a breadcrumb (~/.duck/evicted.tsv on the hub): the picker shows it as ⊘
and enter revives it — a fresh tmux session in the same directory that runs
'claude --resume' on the conversation that was active there.

Each sweep also refreshes titles: detached Claude sessions idle past
--rename-idle get '/rename' typed into them (once per burst of activity), so
their names track the conversation instead of freezing on the first task.

--install puts the same sweep on the hub as a launchd agent (macOS) or a
systemd --user timer (Linux), running every --every, so the hub evicts on its
own; --uninstall removes it.`,
	Args: cobra.NoArgs,
	RunE: func(c *cobra.Command, args []string) error {
		w, err := build()
		if err != nil {
			return err
		}
		switch {
		case evictInstall:
			return installEvictAgent(w.client, evictAge, evictEvery, evictRenameIdle)
		case evictUninstall:
			return uninstallEvictAgent(w.client)
		}
		// Best-effort hook install on manual sweeps too — otherwise a user who
		// never runs --install gets no @claude_session_id stamps and every
		// eviction falls back to the newest-jsonl heuristic. Idempotent.
		if err := installClaudeIDHook(w.client); err != nil {
			fmt.Printf("warning: could not install Claude session-id hook: %v\n", err)
		}
		evicted, renamed, err := w.sessions.Evict(evictAge, evictRenameIdle)
		if err != nil {
			return err
		}
		for _, n := range renamed {
			fmt.Printf("refreshed title of %s (/rename)\n", n)
		}
		if len(evicted) == 0 {
			fmt.Printf("no sessions idle longer than %s\n", evictAge)
			return nil
		}
		for _, n := range evicted {
			fmt.Printf("evicted %s\n", n)
		}
		fmt.Println("revive any of them from `duck --resume` (shown as ⊘)")
		return nil
	},
}

func init() {
	evictCmd.Flags().DurationVar(&evictAge, "age", defaultEvictAge, "idle threshold; detached sessions older than this are evicted")
	evictCmd.Flags().DurationVar(&evictEvery, "every", defaultEvictEvery, "sweep interval for the installed hub scheduler (launchd/systemd, with --install)")
	evictCmd.Flags().DurationVar(&evictRenameIdle, "rename-idle", defaultEvictRenameIdle, "idle threshold after which a detached Claude session gets a /rename title refresh (0 disables)")
	evictCmd.Flags().BoolVar(&evictInstall, "install", false, "install the sweep on the hub (launchd agent on macOS, systemd --user timer on Linux)")
	evictCmd.Flags().BoolVar(&evictUninstall, "uninstall", false, "remove the hub eviction scheduler")
}

// installEvictAgent writes the eviction script onto the hub and schedules it to
// run every `every` with the given idle threshold, picking the hub's native
// scheduler: a launchd agent on macOS, a systemd --user timer on Linux.
// Re-running with new flags overwrites and reloads.
func installEvictAgent(r evictRunner, age, every, renameIdle time.Duration) error {
	if err := installClaudeIDHook(r); err != nil {
		// The hook is an accuracy upgrade, not a requirement: without it eviction
		// falls back to the newest-conversation-in-dir heuristic. Warn and continue.
		fmt.Printf("warning: could not install Claude session-id hook: %v\n", err)
	}
	if _, err := r.RunInput(
		"mkdir -p ~/.duck && cat > ~/.duck/evict.sh", strings.NewReader(session.EvictScript)); err != nil {
		return fmt.Errorf("write evict.sh: %w", err)
	}
	goos, err := hubGOOS(r)
	if err != nil {
		return fmt.Errorf("detect hub OS: %w", err)
	}
	switch goos {
	case "darwin":
		return installEvictLaunchd(r, age, every, renameIdle)
	case "linux":
		return installEvictSystemd(r, age, every, renameIdle)
	default:
		return fmt.Errorf("duck evict --install: unsupported hub OS %q (want macOS or Linux)", goos)
	}
}

// hubGOOS reports the hub's operating system ("darwin"/"linux") via `uname -s`,
// so scheduler installs/uninstalls target the right mechanism. Unknown output is
// returned verbatim so callers can surface it in an error.
func hubGOOS(r evictRunner) (string, error) {
	out, err := r.Run("uname -s")
	if err != nil {
		return "", err
	}
	switch strings.TrimSpace(out) {
	case "Darwin":
		return "darwin", nil
	case "Linux":
		return "linux", nil
	default:
		return strings.TrimSpace(out), nil
	}
}

// installEvictLaunchd schedules the sweep as a macOS launchd agent (the verified
// macOS-hub path). Re-running overwrites and reloads.
func installEvictLaunchd(r evictRunner, age, every, renameIdle time.Duration) error {
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>/bin/sh</string>
		<string>-c</string>
		<string>AGE_SECS=%d RENAME_SECS=%d /bin/sh "$HOME/.duck/evict.sh"</string>
	</array>
	<key>StartInterval</key><integer>%d</integer>
	<key>StandardOutPath</key><string>/tmp/duck-evict.log</string>
	<key>StandardErrorPath</key><string>/tmp/duck-evict.log</string>
</dict>
</plist>
`, evictLabel, int64(age/time.Second), int64(renameIdle/time.Second), int64(every/time.Second))
	plistPath := "~/Library/LaunchAgents/" + evictLabel + ".plist"
	if _, err := r.RunInput(
		"mkdir -p ~/Library/LaunchAgents && cat > "+plistPath, strings.NewReader(plist)); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}
	// bootout tolerates "not loaded"; bootstrap (modern) falls back to load
	// (legacy / non-GUI ssh contexts).
	reload := fmt.Sprintf(
		"launchctl bootout gui/$(id -u)/%s 2>/dev/null; "+
			"launchctl bootstrap gui/$(id -u) %s 2>/dev/null || launchctl load %s",
		evictLabel, plistPath, plistPath)
	if _, err := r.Run(reload); err != nil {
		return fmt.Errorf("load launchd agent: %w", err)
	}
	reportEvictInstalled(age, every, renameIdle, "launchd agent "+evictLabel)
	return nil
}

// installEvictSystemd schedules the sweep as a systemd --user timer (the Linux
// hub path). It writes a oneshot service + a recurring timer under
// ~/.config/systemd/user, enables linger so the timer fires even when no one is
// logged into the hub, and enables+starts the timer. Re-running overwrites the
// units and reloads. systemd `%h` expands to the hub user's home; XDG_RUNTIME_DIR
// is set explicitly so `systemctl --user` finds the user bus over SSH.
func installEvictSystemd(r evictRunner, age, every, renameIdle time.Duration) error {
	service := fmt.Sprintf(`[Unit]
Description=duck: evict stale tmux sessions on the hub (see: duck evict)

[Service]
Type=oneshot
Environment=AGE_SECS=%d
Environment=RENAME_SECS=%d
Environment=PATH=/usr/local/bin:/usr/bin:/bin:%%h/.local/bin
ExecStart=/bin/sh %%h/.duck/evict.sh
`, int64(age/time.Second), int64(renameIdle/time.Second))
	timer := fmt.Sprintf(`[Unit]
Description=duck: periodic eviction sweep (see: duck evict)

[Timer]
OnBootSec=2min
OnUnitActiveSec=%d
Persistent=true

[Install]
WantedBy=timers.target
`, int64(every/time.Second))
	if _, err := r.RunInput(
		"mkdir -p ~/.config/systemd/user && cat > ~/.config/systemd/user/"+evictSystemdUnit+".service",
		strings.NewReader(service)); err != nil {
		return fmt.Errorf("write systemd service: %w", err)
	}
	if _, err := r.RunInput(
		"cat > ~/.config/systemd/user/"+evictSystemdUnit+".timer", strings.NewReader(timer)); err != nil {
		return fmt.Errorf("write systemd timer: %w", err)
	}
	// enable-linger is best-effort: self-linger is permitted on most distros, and
	// we fall back to passwordless sudo; without it the timer still fires whenever
	// a session is present, so a failure here is not fatal.
	activate := "export XDG_RUNTIME_DIR=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}; " +
		`loginctl enable-linger "$(id -un)" 2>/dev/null || sudo -n loginctl enable-linger "$(id -un)" 2>/dev/null || true; ` +
		"systemctl --user daemon-reload && " +
		"systemctl --user enable --now " + evictSystemdUnit + ".timer"
	if _, err := r.Run(activate); err != nil {
		return fmt.Errorf("enable systemd timer: %w", err)
	}
	reportEvictInstalled(age, every, renameIdle, "systemd --user timer "+evictSystemdUnit)
	return nil
}

// reportEvictInstalled prints the shared post-install summary for either
// scheduler (sched names the mechanism, e.g. "launchd agent com.duckcli.evict").
func reportEvictInstalled(age, every, renameIdle time.Duration, sched string) {
	fmt.Printf("installed: hub evicts sessions idle >%s every %s (%s)\n", age, every, sched)
	if renameIdle > 0 {
		fmt.Printf("each sweep also /rename-refreshes Claude titles on sessions idle >%s\n", renameIdle)
	}
}

// installClaudeIDHook writes the hook script to the hub and merges a
// SessionStart entry invoking it into the hub's ~/.claude/settings.json, so
// every Claude session stamps its id + revival flags onto its tmux session.
// Idempotent and self-upgrading: the script file is always rewritten (so a new
// duck ships a new hook body), and any prior duck-installed SessionStart entry
// (old inline variant or the script call) is replaced, never duplicated. The
// settings merge happens laptop-side in Go (read → modify → atomic temp+mv
// write) so the hub needs no jq; unrelated settings are preserved verbatim by
// the map round-trip.
func installClaudeIDHook(r evictRunner) error {
	if _, err := r.RunInput(
		"mkdir -p ~/.duck && cat > ~/.duck/claude-hook.sh && chmod +x ~/.duck/claude-hook.sh",
		strings.NewReader(claudeHookScript)); err != nil {
		return fmt.Errorf("write claude-hook.sh: %w", err)
	}
	out, err := r.Run(`cat ~/.claude/settings.json 2>/dev/null || echo '{}'`)
	if err != nil {
		return err
	}
	settings := map[string]any{}
	if s := strings.TrimSpace(out); s != "" {
		if err := json.Unmarshal([]byte(s), &settings); err != nil {
			return fmt.Errorf("hub ~/.claude/settings.json is not valid JSON: %w", err)
		}
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	starts, _ := hooks["SessionStart"].([]any)
	// Drop every prior duck-installed entry (recognized by the option name the
	// old inline hook stamped, or the script path), then append the current one.
	kept := starts[:0]
	already := false
	for _, e := range starts {
		s := fmt.Sprint(e)
		if strings.Contains(s, claudeHookCmd) {
			already = true
			kept = append(kept, e)
			continue
		}
		if strings.Contains(s, "@claude_session_id") {
			continue // stale duck variant: replace
		}
		kept = append(kept, e)
	}
	if already && len(kept) == len(starts) {
		return nil // settings already current; only the script file was refreshed
	}
	if !already {
		kept = append(kept, map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": claudeHookCmd}},
		})
	}
	hooks["SessionStart"] = kept
	settings["hooks"] = hooks
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	cmd := "mkdir -p ~/.claude && cat > ~/.claude/settings.json.tmp && mv ~/.claude/settings.json.tmp ~/.claude/settings.json"
	if _, err := r.RunInput(cmd, strings.NewReader(string(data))); err != nil {
		return err
	}
	fmt.Println("installed Claude SessionStart hook (stamps session id + launch flags for exact resume)")
	return nil
}

// uninstallEvictAgent removes the hub's eviction scheduler (launchd agent on
// macOS, systemd --user timer on Linux), the sweep script, and the Claude
// SessionStart hook (file + settings entry). Breadcrumbs (and the ability to
// revive already-evicted sessions) are left intact.
func uninstallEvictAgent(r evictRunner) error {
	goos, err := hubGOOS(r)
	if err != nil {
		return fmt.Errorf("detect hub OS: %w", err)
	}
	var cmd string
	switch goos {
	case "darwin":
		plistPath := "~/Library/LaunchAgents/" + evictLabel + ".plist"
		cmd = fmt.Sprintf(
			"launchctl bootout gui/$(id -u)/%s 2>/dev/null; launchctl unload %s 2>/dev/null; rm -f %s ~/.duck/evict.sh ~/.duck/claude-hook.sh",
			evictLabel, plistPath, plistPath)
	case "linux":
		unitDir := "~/.config/systemd/user/"
		cmd = "export XDG_RUNTIME_DIR=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}; " +
			"systemctl --user disable --now " + evictSystemdUnit + ".timer 2>/dev/null; " +
			"rm -f " + unitDir + evictSystemdUnit + ".service " + unitDir + evictSystemdUnit + ".timer ~/.duck/evict.sh ~/.duck/claude-hook.sh; " +
			"systemctl --user daemon-reload 2>/dev/null || true"
	default:
		return fmt.Errorf("duck evict --uninstall: unsupported hub OS %q (want macOS or Linux)", goos)
	}
	if _, err := r.Run(cmd); err != nil {
		return err
	}
	if err := removeClaudeIDHook(r); err != nil {
		fmt.Printf("warning: could not remove Claude SessionStart hook from settings: %v\n", err)
	}
	fmt.Println("uninstalled hub eviction agent")
	return nil
}

// removeClaudeIDHook drops every duck-installed SessionStart entry from the
// hub's ~/.claude/settings.json (matched on the hook command/option marker),
// leaving all other settings and hooks untouched. Missing file or no matching
// entry is a no-op.
func removeClaudeIDHook(r evictRunner) error {
	out, err := r.Run(`cat ~/.claude/settings.json 2>/dev/null || echo '{}'`)
	if err != nil {
		return err
	}
	settings := map[string]any{}
	if s := strings.TrimSpace(out); s != "" {
		if err := json.Unmarshal([]byte(s), &settings); err != nil {
			return fmt.Errorf("hub ~/.claude/settings.json is not valid JSON: %w", err)
		}
	}
	hooks, _ := settings["hooks"].(map[string]any)
	starts, _ := hooks["SessionStart"].([]any)
	if len(starts) == 0 {
		return nil
	}
	kept := make([]any, 0, len(starts))
	for _, e := range starts {
		s := fmt.Sprint(e)
		if strings.Contains(s, "claude-hook.sh") || strings.Contains(s, "@claude_session_id") {
			continue
		}
		kept = append(kept, e)
	}
	if len(kept) == len(starts) {
		return nil
	}
	if len(kept) == 0 {
		delete(hooks, "SessionStart")
	} else {
		hooks["SessionStart"] = kept
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	cmd := "cat > ~/.claude/settings.json.tmp && mv ~/.claude/settings.json.tmp ~/.claude/settings.json"
	_, err = r.RunInput(cmd, strings.NewReader(string(data)))
	return err
}
