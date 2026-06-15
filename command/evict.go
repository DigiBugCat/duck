// `duck evict`: kill stale (detached, non-looped, idle past --age) tmux
// sessions on the hub to reclaim RAM, leaving a breadcrumb so the picker can
// revive each one later — same tmux name, same dir, and `claude --resume` of
// the conversation that was running there. `duck evict --install` writes the
// same sweep as a launchd agent ON the hub so the mac mini evicts on its own
// timer with no duck daemon; `--uninstall` removes it.
package command

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/DigiBugCat/duck/internal/session"
)

// evictLabel is the hub launchd agent's label and plist basename.
const evictLabel = "com.duckcli.evict"

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

--install puts the same sweep on the hub as a launchd agent (runs every
--every), so the hub evicts on its own; --uninstall removes the agent.`,
	Args: cobra.NoArgs,
	RunE: func(c *cobra.Command, args []string) error {
		w, err := build()
		if err != nil {
			return err
		}
		switch {
		case evictInstall:
			return installEvictAgent(w, evictAge, evictEvery, evictRenameIdle)
		case evictUninstall:
			return uninstallEvictAgent(w)
		}
		// Best-effort hook install on manual sweeps too — otherwise a user who
		// never runs --install gets no @claude_session_id stamps and every
		// eviction falls back to the newest-jsonl heuristic. Idempotent.
		if err := installClaudeIDHook(w); err != nil {
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
	evictCmd.Flags().DurationVar(&evictAge, "age", 12*time.Hour, "idle threshold; detached sessions older than this are evicted")
	evictCmd.Flags().DurationVar(&evictEvery, "every", 30*time.Minute, "sweep interval for the installed launchd agent (with --install)")
	evictCmd.Flags().DurationVar(&evictRenameIdle, "rename-idle", 15*time.Minute, "idle threshold after which a detached Claude session gets a /rename title refresh (0 disables)")
	evictCmd.Flags().BoolVar(&evictInstall, "install", false, "install the sweep as a launchd agent on the hub")
	evictCmd.Flags().BoolVar(&evictUninstall, "uninstall", false, "remove the hub launchd agent")
}

// installEvictAgent writes the eviction script and a launchd plist onto the hub
// and (re)loads the agent, so the hub sweeps itself every `every` with the
// given idle threshold. Re-running with new flags overwrites and reloads.
func installEvictAgent(w *wiring, age, every, renameIdle time.Duration) error {
	if err := installClaudeIDHook(w); err != nil {
		// The hook is an accuracy upgrade, not a requirement: without it eviction
		// falls back to the newest-conversation-in-dir heuristic. Warn and continue.
		fmt.Printf("warning: could not install Claude session-id hook: %v\n", err)
	}
	if _, err := w.client.RunInput(
		"mkdir -p ~/.duck && cat > ~/.duck/evict.sh", strings.NewReader(session.EvictScript)); err != nil {
		return fmt.Errorf("write evict.sh: %w", err)
	}
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
	if _, err := w.client.RunInput(
		"mkdir -p ~/Library/LaunchAgents && cat > "+plistPath, strings.NewReader(plist)); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}
	// bootout tolerates "not loaded"; bootstrap (modern) falls back to load
	// (legacy / non-GUI ssh contexts).
	reload := fmt.Sprintf(
		"launchctl bootout gui/$(id -u)/%s 2>/dev/null; "+
			"launchctl bootstrap gui/$(id -u) %s 2>/dev/null || launchctl load %s",
		evictLabel, plistPath, plistPath)
	if _, err := w.client.Run(reload); err != nil {
		return fmt.Errorf("load launchd agent: %w", err)
	}
	fmt.Printf("installed: hub evicts sessions idle >%s every %s (%s)\n", age, every, evictLabel)
	if renameIdle > 0 {
		fmt.Printf("each sweep also /rename-refreshes Claude titles on sessions idle >%s\n", renameIdle)
	}
	return nil
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
func installClaudeIDHook(w *wiring) error {
	if _, err := w.client.RunInput(
		"mkdir -p ~/.duck && cat > ~/.duck/claude-hook.sh && chmod +x ~/.duck/claude-hook.sh",
		strings.NewReader(claudeHookScript)); err != nil {
		return fmt.Errorf("write claude-hook.sh: %w", err)
	}
	out, err := w.client.Run(`cat ~/.claude/settings.json 2>/dev/null || echo '{}'`)
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
	if _, err := w.client.RunInput(cmd, strings.NewReader(string(data))); err != nil {
		return err
	}
	fmt.Println("installed Claude SessionStart hook (stamps session id + launch flags for exact resume)")
	return nil
}

// uninstallEvictAgent unloads and removes the hub's eviction launchd agent,
// the sweep script, the Claude SessionStart hook (file + settings entry).
// Breadcrumbs (and the ability to revive already-evicted sessions) are left
// intact.
func uninstallEvictAgent(w *wiring) error {
	plistPath := "~/Library/LaunchAgents/" + evictLabel + ".plist"
	cmd := fmt.Sprintf(
		"launchctl bootout gui/$(id -u)/%s 2>/dev/null; launchctl unload %s 2>/dev/null; rm -f %s ~/.duck/evict.sh ~/.duck/claude-hook.sh",
		evictLabel, plistPath, plistPath)
	if _, err := w.client.Run(cmd); err != nil {
		return err
	}
	if err := removeClaudeIDHook(w); err != nil {
		fmt.Printf("warning: could not remove Claude SessionStart hook from settings: %v\n", err)
	}
	fmt.Println("uninstalled hub eviction agent")
	return nil
}

// removeClaudeIDHook drops every duck-installed SessionStart entry from the
// hub's ~/.claude/settings.json (matched on the hook command/option marker),
// leaving all other settings and hooks untouched. Missing file or no matching
// entry is a no-op.
func removeClaudeIDHook(w *wiring) error {
	out, err := w.client.Run(`cat ~/.claude/settings.json 2>/dev/null || echo '{}'`)
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
	_, err = w.client.RunInput(cmd, strings.NewReader(string(data)))
	return err
}
