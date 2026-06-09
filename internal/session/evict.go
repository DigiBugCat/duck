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
// evicted session — name \t @duck_dir \t evicted-epoch \t claude-session-id.
// Append-only from the script; the laptop dedupes keep-last and prunes lines on
// revive/kill.
const evictedPath = "~/.duck/evicted.tsv"

// claudeIDOption is the tmux user option carrying the EXACT Claude Code session
// id running in a session. Duck does not stamp it itself — a hub-side Claude
// Code SessionStart hook does (`tmux set-option @claude_session_id <id>`,
// installed by `duck evict --install`), the same pattern as @duck_loop. When
// set, eviction records the precise conversation to `claude --resume`; when
// unset (pre-hook sessions), the sweep falls back to the newest-jsonl heuristic
// for the session's dir.
const claudeIDOption = "@claude_session_id"

// EvictScript is the hub-side eviction sweep, parameterized by $AGE_SECS (idle
// threshold in seconds). For every tmux session that is detached, not running a
// /loop (@duck_loop), and idle past the threshold, it:
//
//  1. resolves the Claude Code conversation to resume: the exact id from the
//     @claude_session_id option when the SessionStart hook stamped one, else
//     the newest jsonl for the session's @duck_dir (~/.claude/projects/<slug>/,
//     slug = dir with non-alphanumerics collapsed to '-'),
//  2. kills the tmux session,
//  3. appends a breadcrumb line to ~/.duck/evicted.tsv,
//  4. prints "evicted <name>" so callers can count/report.
//
// Attached and looped sessions are NEVER evicted (same safety contract as
// `duck clean`). The newest-jsonl fallback can pick a sibling session's
// conversation when several un-stamped sessions share one dir; in that case
// revive still lands in the right directory and `claude --resume` opens the
// most recent conversation there — the same thing `claude --continue` would do.
const EvictScript = `: "${AGE_SECS:=43200}"
now=$(date +%s)
mkdir -p "$HOME/.duck"
tmux list-sessions -F '#{session_name}	#{@duck_dir}	#{session_attached}	#{session_activity}	#{@duck_loop}	#{@claude_session_id}	#{@claude_resume_args}' 2>/dev/null |
while IFS='	' read -r name dir attached activity loop cid rargs; do
  [ -z "$name" ] && continue
  [ -n "$attached" ] && [ "$attached" != "0" ] && continue
  [ -n "$loop" ] && [ "$loop" != "0" ] && continue
  [ -z "$activity" ] && continue
  [ $((now - activity)) -lt "$AGE_SECS" ] && continue
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
  tmux kill-session -t "$name" 2>/dev/null || continue
  printf '%s\t%s\t%s\t%s\t%s\n' "$name" "$dir" "$now" "$cid" "$rargs" >> "$HOME/.duck/evicted.tsv"
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
}

// Evict runs one eviction sweep on the hub NOW with the given idle threshold,
// returning the names of the sessions it evicted. It streams EvictScript over
// SSH so the manual sweep and the installed launchd timer execute identical
// logic.
func (m *Manager) Evict(maxAge time.Duration) ([]string, error) {
	secs := int64(maxAge / time.Second)
	out, err := m.run.RunInput(fmt.Sprintf("AGE_SECS=%d sh -s", secs), strings.NewReader(EvictScript))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if n, ok := strings.CutPrefix(strings.TrimSpace(line), "evicted "); ok && n != "" {
			names = append(names, n)
		}
	}
	return names, nil
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
