package session

import (
	"io"
	"strings"
	"testing"
	"time"
)

// evictRunner extends the fake-runner seam with RunInput output (Evict streams
// the script on stdin and parses the sweep's stdout).
type evictRunner struct {
	cmds  []string
	out   map[string]string // exact-cmd → stdout (Run and RunInput)
	stdin string            // last RunInput body
}

func (f *evictRunner) Run(cmd string) (string, error) {
	f.cmds = append(f.cmds, cmd)
	return f.out[cmd], nil
}

func (f *evictRunner) RunInput(cmd string, stdin io.Reader) (string, error) {
	f.cmds = append(f.cmds, cmd)
	b, _ := io.ReadAll(stdin)
	f.stdin = string(b)
	return f.out[cmd], nil
}

func TestEvictStreamsScriptAndParsesNames(t *testing.T) {
	f := &evictRunner{out: map[string]string{
		"AGE_SECS=43200 RENAME_SECS=900 sh -s": "renamed baz\nevicted foo\nevicted bar-2\n",
	}}
	m := NewManager(f, &fakeAttacher{})
	names, renamed, err := m.Evict(12*time.Hour, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "foo" || names[1] != "bar-2" {
		t.Fatalf("parsed names = %v", names)
	}
	if len(renamed) != 1 || renamed[0] != "baz" {
		t.Fatalf("parsed renamed = %v", renamed)
	}
	if f.stdin != EvictScript {
		t.Fatalf("Evict must stream EvictScript on stdin (got %d bytes)", len(f.stdin))
	}
}

func TestListEvictedDedupesKeepLast(t *testing.T) {
	f := &evictRunner{out: map[string]string{
		"cat ~/.duck/evicted.tsv 2>/dev/null || true": strings.Join([]string{
			"foo\t~/dev/foo\t100\tcid-old\t",
			"bar\t~/dev/bar\t200\t",
			"foo\t~/dev/foo\t300\tcid-new\t--model opus\t✳ Fix login flow", // re-evicted: this line wins
			"",
		}, "\n"),
	}}
	m := NewManager(f, &fakeAttacher{})
	ev, err := m.ListEvicted()
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) != 2 {
		t.Fatalf("want 2 deduped entries, got %v", ev)
	}
	if ev[0].Name != "foo" || ev[0].ClaudeID != "cid-new" || ev[0].ResumeArgs != "--model opus" || ev[0].Title != "✳ Fix login flow" || !ev[0].EvictedAt.Equal(time.Unix(300, 0)) {
		t.Fatalf("keep-last dedupe failed: %+v", ev[0])
	}
	if ev[1].Name != "bar" || ev[1].ClaudeID != "" || ev[1].Dir != "~/dev/bar" {
		t.Fatalf("bar entry wrong: %+v", ev[1])
	}
}

func TestListEvictedMissingFileIsEmpty(t *testing.T) {
	m := NewManager(&evictRunner{out: map[string]string{}}, &fakeAttacher{})
	ev, err := m.ListEvicted()
	if err != nil || len(ev) != 0 {
		t.Fatalf("missing file should be empty list, got %v, %v", ev, err)
	}
}

func TestReviveRecreatesAndResumesClaude(t *testing.T) {
	f := &evictRunner{out: map[string]string{
		"tmux display-message -p -t 'foo' '#{pane_id}'": "%7\n",
	}} // empty list-sessions → not live
	m := NewManager(f, &fakeAttacher{})
	err := m.Revive(Evicted{Name: "foo", Dir: "~/dev/foo", ClaudeID: "abc-123", ResumeArgs: "--model opus --dangerously-skip-permissions"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(f.cmds, "\n")
	for _, want := range []string{
		"tmux new-session -d -s 'foo' -c \"$HOME\"/'dev/foo'",
		"tmux set-option -t 'foo' '@duck_manager' '%7'",
		"awk -F '\\t' -v n='foo' '$1 != n'",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing command %q in:\n%s", want, joined)
		}
	}
	for _, want := range []string{"tmux send-keys -t 'foo'", "claude", "--resume", "abc-123", "--model", "opus", "--dangerously-skip-permissions", "--dangerously-load-development-channels", "server:duck-agents"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("revive send line missing %q in:\n%s", want, joined)
		}
	}
}

func TestReviveWithoutClaudeIDSkipsResume(t *testing.T) {
	f := &evictRunner{out: map[string]string{}}
	m := NewManager(f, &fakeAttacher{})
	if err := m.Revive(Evicted{Name: "foo", Dir: "~/dev/foo"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(f.cmds, "\n"), "send-keys") {
		t.Fatal("no claude id → no send-keys")
	}
}

func TestEvictScriptNeverTouchesAttachedOrLooped(t *testing.T) {
	// The script's safety contract lives in its guard lines; pin them so an edit
	// that drops the attached/looped skip fails loudly.
	for _, guard := range []string{
		`[ -n "$attached" ] && [ "$attached" != "0" ] && continue`,
		`[ -n "$loop" ] && [ "$loop" != "0" ] && continue`,
	} {
		if !strings.Contains(EvictScript, guard) {
			t.Fatalf("EvictScript lost its safety guard %q", guard)
		}
	}
}

func TestEvictScriptSkipsPersistentWorkspaceRecords(t *testing.T) {
	for _, want := range []string{
		"persistent_workspace()",
		`"$HOME/.claude/projects/$pslug/duck/$2.json"`,
		`"persistent"[[:space:]]*:[[:space:]]*true`,
		`persistent_workspace "$dir" "$name" && continue`,
	} {
		if !strings.Contains(EvictScript, want) {
			t.Fatalf("EvictScript persistent exemption missing %q", want)
		}
	}
}

func TestEvictScriptPrefersStampedClaudeID(t *testing.T) {
	// The SessionStart hook stamps the exact conversation id on the tmux session
	// (@claude_session_id); the sweep must read it and only fall back to the
	// newest-jsonl-in-dir heuristic when the stamp is absent.
	if !strings.Contains(EvictScript, "#{@claude_session_id}") {
		t.Fatal("EvictScript must read the @claude_session_id option")
	}
	if !strings.Contains(EvictScript, `if [ -z "$cid" ] && [ -n "$dir" ]; then`) {
		t.Fatal("newest-jsonl heuristic must be gated on the stamp being absent")
	}
}

func TestEvictScriptCapturesResumeArgs(t *testing.T) {
	// The breadcrumb must carry the hook-captured launch flags so revive can
	// replay them (capture-for-exact-revival, per tmux-assistant-resurrect).
	if !strings.Contains(EvictScript, "#{@claude_resume_args}") {
		t.Fatal("EvictScript must read the @claude_resume_args option")
	}
	if !strings.Contains(EvictScript, `"$cid" "$rargs"`) {
		t.Fatal("EvictScript must write the resume args into the breadcrumb")
	}
}

func TestEvictScriptRenamesAndCapturesTitle(t *testing.T) {
	// Before killing a Claude pane the sweep types `/rename` (no args — Claude
	// regenerates the session name from the conversation history), waits, then
	// captures the pane title into the breadcrumb's 6th field so the picker can
	// show a meaningful name for the dead session.
	for _, want := range []string{
		`tmux send-keys -t "$1" -l '/rename'`,
		`'#{pane_title}'`,
		`"$rargs" "$title"`,
	} {
		if !strings.Contains(EvictScript, want) {
			t.Fatalf("EvictScript missing rename/title capture step %q", want)
		}
	}
	// The /rename nudge must be gated on the pane actually running Claude, so a
	// bare shell never gets a junk command typed into it.
	if !strings.Contains(EvictScript, `'#{pane_current_command}'`) {
		t.Fatal("rename nudge must be gated on pane_current_command")
	}
}

func TestEvictScriptPeriodicRenamePass(t *testing.T) {
	// The periodic title-refresh pass must be disable-able, never type into an
	// attached pane, and only re-rename after NEW activity since the last nudge
	// (@duck_renamed_at), so an idle session isn't renamed every sweep.
	for _, want := range []string{
		`: "${RENAME_SECS:=900}"`,
		`if [ "$RENAME_SECS" -gt 0 ] 2>/dev/null; then`,
		`[ $((now - activity)) -lt "$RENAME_SECS" ] && continue`,
		`[ -n "$renamed" ] && [ "$renamed" -ge "$activity" ] 2>/dev/null && continue`,
		`tmux set-option -t "$1" @duck_renamed_at "$now"`,
		`echo "renamed $name"`,
		// A hook-stamped session id makes a pane count as Claude regardless of its
		// process title (newer Claude builds retitle to their version string).
		`[ -n "$2" ] && return 0`,
		// Non-whitespace field separator so empty @duck_loop/@duck_renamed_at fields
		// don't collapse and shift the cid into $loop (which wrongly skips eviction).
		`SEP=$(printf '\037')`,
		`while IFS="$SEP" read -r name attached activity loop renamed cid; do`,
	} {
		if !strings.Contains(EvictScript, want) {
			t.Fatalf("EvictScript rename pass missing %q", want)
		}
	}
}
