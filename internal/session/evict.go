// evict.go is the session manager's stale-session eviction engine: kill old,
// detached, non-looped tmux sessions on the hub to reclaim RAM, while leaving a
// breadcrumb (~/.duck/evicted.tsv) that lets the picker offer them back as
// resumable rows. Reviving recreates the tmux session in the same directory and
// — when the breadcrumb captured a Claude Code session id — re-runs
// `claude --resume <id>` so the conversation itself comes back.
//
// The eviction logic lives in ONE portable POSIX-sh script (EvictScript):
// `duck evict` pipes it over SSH for a manual sweep, and `duck evict --install`
// writes the SAME script to ~/.duck/evict.sh under a hub launchd agent so the
// hub sweeps itself on a timer with no duck daemon. names.json is NEVER touched
// by the script (it stays laptop-single-writer); the breadcrumb is a separate
// append-only TSV the laptop reads and prunes.
package session

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/DigiBugCat/duck/internal/paths"
)

// evictedPath is the hub-side breadcrumb file: one tab-separated line per
// evicted session — name \t @duck_dir \t evicted-epoch \t claude-session-id
// \t resume-args \t pane-title. Append-only from the script; the laptop
// dedupes keep-last and prunes lines on revive/kill.
const evictedPath = "~/.duck/evicted.tsv"

// claudeIDOption is the tmux user option carrying the EXACT Claude Code session
// id running in a session. Duck does not stamp it itself — a hub-side Claude
// Code SessionStart hook does (`tmux set-option @claude_session_id <id>`,
// installed by `duck evict --install`), the same pattern as @duck_loop. When
// set, eviction records the precise conversation to `claude --resume`; when
// unset (pre-hook sessions), the sweep falls back to the newest-jsonl heuristic
// for the session's dir.
const claudeIDOption = "@claude_session_id"

// EvictScript is the hub-side sweep, parameterized by $AGE_SECS (eviction idle
// threshold in seconds) and $RENAME_SECS (title-refresh idle threshold; 0
// disables the rename pass).
//
// PASS 1 (rename): for every detached, non-looped session idle past
// $RENAME_SECS whose pane is still running Claude (claude_pane: an exact
// 'claude' process, or node/bun with a hook-stamped @claude_session_id), it
// types `/rename` (no args
// — Claude regenerates the session name from the conversation history) so the
// pane title tracks the session's overall work instead of freezing on the
// first task. @duck_renamed_at (stamped after each nudge) gates the pass: a
// session is only re-renamed when there has been activity SINCE the last
// nudge, so an idle session gets exactly one rename per burst of work, not one
// per sweep. Attached sessions are never typed into (the user may be mid-
// composition); looped sessions are never touched.
//
// PASS 2 (evict): for every tmux session that is detached, not running a
// /loop (@duck_loop), and idle past the threshold, it:
//
//  1. resolves the Claude Code conversation to resume: the exact id from the
//     @claude_session_id option when the SessionStart hook stamped one, else
//     the newest jsonl for the session's @duck_dir (~/.claude/projects/<slug>/,
//     slug = dir with non-alphanumerics collapsed to '-'),
//  2. if the pane is still running Claude, types `/rename` (no args — Claude
//     auto-generates a fresh name from the conversation history) and waits,
//     so the title captured next reflects the whole session, not just the
//     last task; then captures the pane title into the breadcrumb so the
//     picker can show a meaningful name for the dead session,
//  3. kills the tmux session,
//  4. appends a breadcrumb line to ~/.duck/evicted.tsv,
//  5. prints "evicted <name>" so callers can count/report.
//
// Attached and looped sessions are NEVER evicted (same safety contract as
// `duck clean`). The newest-jsonl fallback can pick a sibling session's
// conversation when several un-stamped sessions share one dir; in that case
// revive still lands in the right directory and `claude --resume` opens the
// most recent conversation there — the same thing `claude --continue` would do.
const EvictScript = `# Ensure tmux is reachable: a launchd agent runs with a bare PATH
# (/usr/bin:/bin:/usr/sbin:/sbin) that excludes Homebrew, so a bare 'tmux'
# silently fails (every call redirects stderr to /dev/null) and the sweep
# becomes a no-op. Prepend the Homebrew bin dirs (Apple Silicon + Intel).
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
: "${AGE_SECS:=43200}"
: "${RENAME_SECS:=900}"
now=$(date +%s)
# Field separator for the list-sessions reads below. MUST be non-whitespace: a
# whitespace IFS (like a tab) makes 'read' COLLAPSE runs of the separator and
# drop EMPTY fields, so a session with an empty @duck_loop/@duck_renamed_at
# shifts every later field left — the @claude_session_id lands in $loop, the
# "is it looping?" test sees non-empty, and the session is wrongly skipped (the
# sweep then evicts almost nothing). US (unit separator, \037) never appears in
# a session name, dir, cid, flag list, or title, and preserves empty fields.
SEP=$(printf '\037')
mkdir -p "$HOME/.duck"
# claude_pane <session> <stamped-cid>: is the session's pane running Claude?
# A hook-stamped session id ($2) means this pane IS a Claude session regardless
# of pane_current_command — newer Claude builds set their process title to the
# version string (e.g. "2.1.177"), which defeats a plain name match. Without a
# stamp, only an exact 'claude' command counts; a bare node/bun REPL never does
# (so the sweep never types /rename into it).
claude_pane() {
  [ -n "$2" ] && return 0
  pcmd=$(tmux display-message -p -t "$1" '#{pane_current_command}' 2>/dev/null)
  case "$pcmd" in
    claude) return 0 ;;
  esac
  return 1
}
# nudge_rename <session>: type /rename (no args — Claude regenerates the name
# from the conversation history) and stamp @duck_renamed_at so the session is
# not re-nudged until it sees new activity.
nudge_rename() {
  tmux send-keys -t "$1" -l '/rename' 2>/dev/null
  sleep 1
  tmux send-keys -t "$1" Enter 2>/dev/null
  tmux set-option -t "$1" @duck_renamed_at "$now" 2>/dev/null
}
if [ "$RENAME_SECS" -gt 0 ] 2>/dev/null; then
tmux list-sessions -F "#{session_name}${SEP}#{session_attached}${SEP}#{session_activity}${SEP}#{@duck_loop}${SEP}#{@duck_renamed_at}${SEP}#{@claude_session_id}" 2>/dev/null |
while IFS="$SEP" read -r name attached activity loop renamed cid; do
  [ -z "$name" ] && continue
  [ -n "$attached" ] && [ "$attached" != "0" ] && continue
  [ -n "$loop" ] && [ "$loop" != "0" ] && continue
  [ -z "$activity" ] && continue
  [ $((now - activity)) -lt "$RENAME_SECS" ] && continue
  [ -n "$renamed" ] && [ "$renamed" -ge "$activity" ] 2>/dev/null && continue
  claude_pane "$name" "$cid" || continue
  nudge_rename "$name"
  echo "renamed $name"
done
fi
tmux list-sessions -F "#{session_name}${SEP}#{@duck_dir}${SEP}#{session_attached}${SEP}#{session_activity}${SEP}#{@duck_loop}${SEP}#{@claude_session_id}${SEP}#{@claude_resume_args}${SEP}#{@duck_renamed_at}" 2>/dev/null |
while IFS="$SEP" read -r name dir attached activity loop cid rargs renamed; do
  [ -z "$name" ] && continue
  [ -n "$attached" ] && [ "$attached" != "0" ] && continue
  [ -n "$loop" ] && [ "$loop" != "0" ] && continue
  [ -z "$activity" ] && continue
  [ $((now - activity)) -lt "$AGE_SECS" ] && continue
  stamped=$cid
  if [ -z "$cid" ] && [ -n "$dir" ]; then
    case "$dir" in
      "~") rdir="$HOME" ;;
      "~/"*) rdir="$HOME/${dir#\~/}" ;;
      *) rdir="$dir" ;;
    esac
    slug=$(printf '%s' "$rdir" | sed 's/[^a-zA-Z0-9]/-/g')
    pdir="$HOME/.claude/projects/$slug"
    if [ -d "$pdir" ]; then
      newest=$(ls -t "$pdir"/*.jsonl 2>/dev/null | head -1)
      [ -n "$newest" ] && cid=$(basename "$newest" .jsonl)
    fi
  fi
  # Skip the pre-evict nudge (and its wait) when the rename pass already
  # refreshed the title since the session's last activity.
  if ! { [ -n "$renamed" ] && [ "$renamed" -ge "$activity" ] 2>/dev/null; }; then
    if claude_pane "$name" "$stamped"; then
      nudge_rename "$name"
      sleep 8
    fi
  fi
  title=$(tmux display-message -p -t "$name" '#{pane_title}' 2>/dev/null | tr -d '\t\r\n' | head -c 200)
  tmux kill-session -t "$name" 2>/dev/null || continue
  printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$name" "$dir" "$now" "$cid" "$rargs" "$title" >> "$HOME/.duck/evicted.tsv"
  echo "evicted $name"
done`

// Evicted is one breadcrumb row from ~/.duck/evicted.tsv: a session that was
// killed to save RAM but can be revived (same tmux name, same dir, and — when
// captured — the same Claude Code conversation).
type Evicted struct {
	Name       string    // the tmux session id it had (and gets back on revive)
	Dir        string    // tilde-form working dir from @duck_dir at eviction time
	EvictedAt  time.Time // when the sweep killed it
	ClaudeID   string    // Claude Code session id to `claude --resume`; "" = none found
	ResumeArgs string    // allowlisted launch flags captured by the hook (e.g. "--model opus"); replayed on revive
	Title      string    // pane title at eviction time, freshly re-generated via /rename when Claude was running; "" = none captured
}

// Evict runs one sweep on the hub NOW: the title-refresh pass with renameIdle
// (0 disables it) and the eviction pass with maxAge. It returns the names of
// the sessions evicted and the names whose titles were nudged via /rename. It
// streams EvictScript over SSH so the manual sweep and the installed launchd
// timer execute identical logic.
func (m *Manager) Evict(maxAge, renameIdle time.Duration) (evicted, renamed []string, err error) {
	cmd := fmt.Sprintf("AGE_SECS=%d RENAME_SECS=%d sh -s",
		int64(maxAge/time.Second), int64(renameIdle/time.Second))
	out, err := m.run.RunInput(cmd, strings.NewReader(EvictScript))
	if err != nil {
		return nil, nil, err
	}
	for _, line := range strings.Split(out, "\n") {
		if n, ok := strings.CutPrefix(strings.TrimSpace(line), "evicted "); ok && n != "" {
			evicted = append(evicted, n)
		}
		if n, ok := strings.CutPrefix(strings.TrimSpace(line), "renamed "); ok && n != "" {
			renamed = append(renamed, n)
		}
	}
	return evicted, renamed, nil
}

// ListEvicted reads the hub's eviction breadcrumbs, deduped keep-last per name
// (a name can be evicted, revived, and evicted again — the newest line wins).
// A missing file is an empty list, not an error.
func (m *Manager) ListEvicted() ([]Evicted, error) {
	out, err := m.run.Run("cat " + evictedPath + " 2>/dev/null || true")
	if err != nil {
		return nil, err
	}
	byName := map[string]int{} // name → index in order; later lines overwrite
	var order []Evicted
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(fields) < 3 || fields[0] == "" {
			continue
		}
		e := Evicted{Name: fields[0], Dir: fields[1]}
		if secs, err := strconv.ParseInt(strings.TrimSpace(fields[2]), 10, 64); err == nil {
			e.EvictedAt = time.Unix(secs, 0)
		}
		if len(fields) >= 4 {
			e.ClaudeID = strings.TrimSpace(fields[3])
		}
		if len(fields) >= 5 {
			e.ResumeArgs = strings.TrimSpace(fields[4])
		}
		if len(fields) >= 6 {
			// The script caps the title with `head -c` (bytes, not runes), which can
			// split a trailing multibyte char; drop any invalid remainder.
			e.Title = strings.TrimSpace(strings.ToValidUTF8(fields[5], ""))
		}
		if i, ok := byName[e.Name]; ok {
			order[i] = e
			continue
		}
		byName[e.Name] = len(order)
		order = append(order, e)
	}
	return order, nil
}

// ForgetEvicted removes every breadcrumb line for name (after a revive, a kill,
// or a clean) so the picker stops offering it. A missing file is a no-op.
func (m *Manager) ForgetEvicted(name string) error {
	f := "$HOME/.duck/evicted.tsv"
	// awk -F'\t' keeps every line whose first field isn't the name; rewrite via a
	// temp sibling so a failure never truncates the file.
	cmd := fmt.Sprintf(
		`[ -f %s ] && { awk -F '\t' -v n=%s '$1 != n' %s > %s.tmp && mv %s.tmp %s; } || true`,
		f, paths.Quote(name), f, f, f, f)
	_, err := m.run.Run(cmd)
	return err
}

// Revive recreates an evicted session: a fresh tmux session under the SAME name
// in the same dir (so names.json's entry and any per-TTY memory still match),
// then — when the breadcrumb captured a Claude Code session id — types
// `claude --resume <id>` into it so the conversation resumes. Finally the
// breadcrumb is dropped. If a live session already reclaimed the name, Revive
// just forgets the breadcrumb and succeeds — attach will land on the live one.
func (m *Manager) Revive(e Evicted) error {
	exists, err := m.HasSession(e.Name)
	if err != nil {
		return err
	}
	if !exists {
		dir := e.Dir
		if dir == "" {
			dir = "~"
		}
		if err := m.New(e.Name, dir); err != nil {
			return err
		}
		if e.ClaudeID != "" {
			resume := "claude --resume " + e.ClaudeID
			if e.ResumeArgs != "" {
				// Replay the allowlisted launch flags the hook captured (--model,
				// --permission-mode, --dangerously-skip-permissions) so the revived
				// session runs the way it was originally launched.
				resume += " " + e.ResumeArgs
			}
			cmd := fmt.Sprintf("tmux send-keys -t %s %s Enter",
				paths.Quote(e.Name), paths.Quote(resume))
			if _, err := m.run.Run(cmd); err != nil {
				return err
			}
		}
	}
	return m.ForgetEvicted(e.Name)
}

// IsNoSessionErr reports whether err is tmux's "can't find session" failure —
// the signature App.Kill tolerates when killing an already-evicted session.
func IsNoSessionErr(err error) bool {
	return err != nil && isNoSession(err.Error())
}
