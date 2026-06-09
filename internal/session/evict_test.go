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
		"AGE_SECS=43200 sh -s": "evicted foo\nevicted bar-2\n",
	}}
	m := NewManager(f, &fakeAttacher{})
	names, err := m.Evict(12 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "foo" || names[1] != "bar-2" {
		t.Fatalf("parsed names = %v", names)
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
			"foo\t~/dev/foo\t300\tcid-new\t--model opus", // re-evicted: this line wins
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
	if ev[0].Name != "foo" || ev[0].ClaudeID != "cid-new" || ev[0].ResumeArgs != "--model opus" || !ev[0].EvictedAt.Equal(time.Unix(300, 0)) {
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
	f := &evictRunner{out: map[string]string{}} // empty list-sessions → not live
	m := NewManager(f, &fakeAttacher{})
	err := m.Revive(Evicted{Name: "foo", Dir: "~/dev/foo", ClaudeID: "abc-123", ResumeArgs: "--model opus --dangerously-skip-permissions"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(f.cmds, "\n")
	for _, want := range []string{
		"tmux new-session -d -s 'foo' -c \"$HOME\"/'dev/foo'",
		"tmux send-keys -t 'foo' 'claude --resume abc-123 --model opus --dangerously-skip-permissions' Enter",
		"awk -F '\\t' -v n='foo' '$1 != n'",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing command %q in:\n%s", want, joined)
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
