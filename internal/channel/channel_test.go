package channel

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRunner mirrors panel's test fake: scripted argv → output.
type fakeRunner struct {
	calls []string
	out   map[string]string
	errs  map[string]error
}

func (f *fakeRunner) run(args ...string) (string, error) {
	key := strings.Join(args, " ")
	f.calls = append(f.calls, key)
	if err, ok := f.errs[key]; ok {
		return "", err
	}
	return f.out[key], nil
}

func metaLine(ts time.Time, cwd string) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"session_id":"x","cwd":%q}}`,
		ts.UTC().Format(time.RFC3339Nano), cwd)
}

func TestParseEventFiltersSignal(t *testing.T) {
	if _, ok := ParseEvent([]byte(`{"type":"event_msg","payload":{"type":"token_count"}}`)); ok {
		t.Fatal("token_count is noise")
	}
	if _, ok := ParseEvent([]byte(`{"type":"response_item","payload":{"type":"message"}}`)); ok {
		t.Fatal("response_item is noise")
	}
	ev, ok := ParseEvent([]byte(`{"timestamp":"T","type":"event_msg","payload":{"type":"agent_message","message":"hi"}}`))
	if !ok || ev.Type != "agent_message" || ev.Message != "hi" {
		t.Fatalf("agent_message: %+v ok=%v", ev, ok)
	}
	ev, ok = ParseEvent([]byte(`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"done"}}`))
	if !ok || ev.Message != "done" {
		t.Fatalf("task_complete should carry last_agent_message: %+v", ev)
	}
}

func TestMatchRolloutPairsByCwdAndTime(t *testing.T) {
	root := t.TempDir()
	day := filepath.Join(root, "2026", "07", "03")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	spawn := time.Now().Add(-time.Minute)
	write := func(name, cwd string, ts time.Time) string {
		p := filepath.Join(day, name)
		if err := os.WriteFile(p, []byte(metaLine(ts, cwd)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("rollout-old.jsonl", "/work", spawn.Add(-time.Hour))           // too old
	write("rollout-other.jsonl", "/elsewhere", spawn.Add(2*time.Second)) // wrong dir
	want := write("rollout-mine.jsonl", "/work", spawn.Add(1*time.Second))
	write("rollout-later.jsonl", "/work", spawn.Add(30*time.Second)) // later spawn wins only if first is absent

	// The old file's mtime is fresh (just written) — matchRollout must still
	// reject it on the meta TIMESTAMP, not just mtime.
	got, err := matchRollout(root, "/work", spawn)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
	// No candidates → empty, no error (codex still starting).
	got, err = matchRollout(root, "/nowhere", spawn)
	if err != nil || got != "" {
		t.Fatalf("no-match should be empty+nil, got %q %v", got, err)
	}
}

func TestResolveCachesInWindowOption(t *testing.T) {
	f := &fakeRunner{out: map[string]string{
		"show-options -p -t %5 -v @duck_rollout": "/cached/rollout.jsonl\n",
	}}
	ref := AgentRef{WindowID: "%5"}
	if err := Resolve(f.run, &ref); err != nil {
		t.Fatal(err)
	}
	if ref.Rollout != "/cached/rollout.jsonl" {
		t.Fatalf("cached path not used: %q", ref.Rollout)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "display-message") {
			t.Fatal("cached resolve must not re-scan")
		}
	}
}

func TestTailFiltersAndReturnsOffset(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.jsonl")
	lines := `{"type":"event_msg","payload":{"type":"task_started"}}
{"type":"event_msg","payload":{"type":"token_count"}}
{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"done"}}
`
	if err := os.WriteFile(p, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	off, err := Tail(&buf, p, 0, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if off != int64(len(lines)) {
		t.Fatalf("offset %d != file size %d", off, len(lines))
	}
	var got []Event
	sc := bufio.NewScanner(&buf)
	for sc.Scan() {
		var ev Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatal(err)
		}
		got = append(got, ev)
	}
	if len(got) != 2 || got[0].Type != "task_started" || got[1].Message != "done" {
		t.Fatalf("filtered stream wrong: %+v", got)
	}
}

func TestSendTypesThenEnter(t *testing.T) {
	f := &fakeRunner{out: map[string]string{}}
	if err := Send(f.run, AgentRef{WindowID: "%3"}, "fix the tests"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 || f.calls[0] != "send-keys -t %3 -l -- fix the tests" || f.calls[1] != "send-keys -t %3 Enter" {
		t.Fatalf("calls: %v", f.calls)
	}
}

func TestCompanionsNoServerIsQuietNoop(t *testing.T) {
	f := &fakeRunner{errs: map[string]error{
		"list-sessions -F #{session_name}\t#{@duck_panel_of}": fmt.Errorf("no server running on /tmp/tmux"),
	}, out: map[string]string{}}
	owners, err := Companions(f.run)
	if err != nil || len(owners) != 0 {
		t.Fatalf("no tmux must be a quiet no-op, got %v %v", owners, err)
	}
}

// TestServeHandshakeAndReply drives the MCP loop end-to-end over pipes:
// initialize → tools/list → tools/call reply (routed to send-keys).
func TestServeHandshakeAndReply(t *testing.T) {
	f := &fakeRunner{out: map[string]string{
		"list-panes -s -t work -F #{pane_id}\t#{@duck_panel_role}": "%5\tviewport\n",
		"list-panes -s -t work -F #{pane_id}\t#{@duck_name}\t#{@duck_kind}\t#{@duck_anchor}\t#{@duck_panel_role}\t#{pane_current_command}\t#{pane_title}":        "%5\tterminal\tshells\t\tviewport\tzsh\t\n",
		"list-panes -s -t work-agents -F #{pane_id}\t#{@duck_name}\t#{@duck_kind}\t#{@duck_anchor}\t#{@duck_panel_role}\t#{pane_current_command}\t#{pane_title}": "%7\tcodex\tagents\t\t\tcodex\t\n",
	}}
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n" +
			`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"reply","arguments":{"session":"work","agent":"codex","message":"go on"}}}` + "\n")
	var out bytes.Buffer
	if err := Serve(f.run, in, &out); err != nil {
		t.Fatal(err)
	}
	var replies []map[string]any
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("non-JSON line: %s", sc.Text())
		}
		replies = append(replies, m)
	}
	if len(replies) != 3 {
		t.Fatalf("want 3 responses, got %d: %v", len(replies), replies)
	}
	init := replies[0]["result"].(map[string]any)
	caps := init["capabilities"].(map[string]any)["experimental"].(map[string]any)
	if _, ok := caps["claude/channel"]; !ok {
		t.Fatal("must declare claude/channel capability")
	}
	sent := false
	for _, c := range f.calls {
		if c == "send-keys -t %7 -l -- go on" {
			sent = true
		}
	}
	if !sent {
		t.Fatalf("reply tool must send-keys into the agent window: %v", f.calls)
	}
}
