package channel

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	got, err := matchRollout(root, "/work", spawn, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
	// No candidates → empty, no error (codex still starting).
	got, err = matchRollout(root, "/nowhere", spawn, nil)
	if err != nil || got != "" {
		t.Fatalf("no-match should be empty+nil, got %q %v", got, err)
	}
	// A rollout already pinned by another pane is off-limits: the runner-up
	// (the later spawn in the same cwd) wins instead.
	later := filepath.Join(day, "rollout-later.jsonl")
	got, err = matchRollout(root, "/work", spawn, map[string]bool{want: true})
	if err != nil || got != later {
		t.Fatalf("claimed rollout must be skipped; want %s, got %q %v", later, got, err)
	}
}

// TestResolveOnlyPairsCodexSpawns: pairing is cwd+time correlation, so a
// pane whose spawn cmdline isn't codex (previews, carbonyl, shells) must
// never adopt a neighboring agent's rollout — the duck-2 misattribution.
func TestResolveOnlyPairsCodexSpawns(t *testing.T) {
	root := t.TempDir()
	day := filepath.Join(root, "2026", "07", "03")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	spawn := time.Now().Add(-time.Minute)
	rollout := filepath.Join(day, "rollout-agent.jsonl")
	if err := os.WriteFile(rollout, []byte(metaLine(spawn.Add(time.Second), "/work")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DUCK_CODEX_SESSIONS", root)

	base := map[string]string{
		"show-options -p -t %9 -v @duck_spawned_at":     fmt.Sprintf("%d\n", spawn.Unix()),
		"display-message -p -t %9 #{pane_current_path}": "/work\n",
	}
	// carbonyl pane in the same cwd: must NOT pair.
	f := &fakeRunner{out: map[string]string{}}
	for k, v := range base {
		f.out[k] = v
	}
	f.out["show-options -p -t %9 -v @duck_cmd"] = "carbonyl http://x\n"
	ref := AgentRef{WindowID: "%9"}
	if err := Resolve(f.run, &ref); err != nil || ref.Rollout != "" {
		t.Fatalf("non-codex spawn must not pair, got %q %v", ref.Rollout, err)
	}
	// same pane launched as codex: pairs.
	f = &fakeRunner{out: map[string]string{}}
	for k, v := range base {
		f.out[k] = v
	}
	f.out["show-options -p -t %9 -v @duck_cmd"] = "codex --model gpt-5\n"
	ref = AgentRef{WindowID: "%9"}
	if err := Resolve(f.run, &ref); err != nil {
		t.Fatal(err)
	}
	if ref.Rollout == "" {
		t.Fatal("codex spawn should pair")
	}
}

func TestCmdRunsCodex(t *testing.T) {
	yes := []string{"codex", "codex --model x", "/usr/local/bin/codex resume", "env FOO=1 codex",
		// duck spawn stamps the paths.Quote'd line — tokens arrive quoted.
		"'codex' '--dangerously-bypass-approvals-and-sandbox'", `"codex" "resume"`}
	no := []string{"", "carbonyl http://x", "gosling render codex-notes.html", "claude --ben", "sh"}
	for _, c := range yes {
		if !cmdRunsCodex(c) {
			t.Errorf("want codex: %q", c)
		}
	}
	for _, c := range no {
		if cmdRunsCodex(c) {
			t.Errorf("want non-codex: %q", c)
		}
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
	// Empty capture → composer clear → exactly one Enter, verified once.
	f := &fakeRunner{out: map[string]string{}}
	if err := Send(f.run, AgentRef{WindowID: "%3"}, "fix the tests"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"send-keys -t %3 -l -- fix the tests",
		"send-keys -t %3 Enter",
		"capture-pane -p -t %3",
	}
	if len(f.calls) != 3 || f.calls[0] != want[0] || f.calls[1] != want[1] || f.calls[2] != want[2] {
		t.Fatalf("calls: %v", f.calls)
	}
}

func TestSendRetriesEnterWhilePasteSitsInComposer(t *testing.T) {
	// The composer still shows a pending paste after the first Enter (a big
	// paste eats it) — Send must press Enter again rather than give up.
	f := &fakeRunner{out: map[string]string{
		"capture-pane -p -t %3": "transcript above\n› [Pasted Content 1018 chars]\nfooter",
	}}
	if err := Send(f.run, AgentRef{WindowID: "%3"}, "a long prompt"); err != nil {
		t.Fatal(err)
	}
	enters := 0
	for _, c := range f.calls {
		if c == "send-keys -t %3 Enter" {
			enters++
		}
	}
	if enters != 3 { // initial + 2 retries, then stop guessing
		t.Fatalf("want 3 Enters, got %d (calls: %v)", enters, f.calls)
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
	if err := Serve(f.run, "", in, &out); err != nil {
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

// TestServeDrainsPublishSpool pre-spools a published event, runs Serve scoped
// to the workspace, and asserts a notifications/claude/channel carrying that
// content lands on stdout after the handshake.
func TestServeDrainsPublishSpool(t *testing.T) {
	old := spoolHome
	spoolHome = t.TempDir()
	defer func() { spoolHome = old }()
	oldSweep := sweepEvery
	sweepEvery = 5 * time.Millisecond
	defer func() { sweepEvery = oldSweep }()

	if err := Publish("work", "routine fired: standup", map[string]string{"source": "routines", "type": "digest"}); err != nil {
		t.Fatal(err)
	}

	// Scoped mode with no companions: Companions() returns whatever the fake
	// gives; an empty list is fine — drainPublish covers s.workspace directly.
	f := &fakeRunner{out: map[string]string{}}
	pr, pw := io.Pipe()
	var out lockedBuffer
	done := make(chan error, 1)
	go func() { done <- Serve(f.run, "work", pr, &out) }()

	// Handshake, then leave stdin open so watch() can sweep.
	io.WriteString(pw, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n")
	io.WriteString(pw, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")

	// Poll for the notification (generous timeout — no real-time race).
	deadline := time.Now().Add(3 * time.Second)
	var got map[string]any
	for time.Now().Before(deadline) {
		for _, m := range parseLines(out.String()) {
			if m["method"] == "notifications/claude/channel" {
				got = m
			}
		}
		if got != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	pw.Close()
	<-done
	if got == nil {
		t.Fatalf("no channel notification emitted; output was:\n%s", out.String())
	}
	params := got["params"].(map[string]any)
	if params["content"] != "routine fired: standup" {
		t.Fatalf("wrong content: %v", params["content"])
	}
	meta := params["meta"].(map[string]any)
	if meta["session"] != "work" || meta["type"] != "digest" || meta["source"] != "routines" {
		t.Fatalf("meta wrong: %v", meta)
	}
	// The sidecar marked the workspace alive.
	if !AliveWithin("work", time.Minute) {
		t.Fatal("sidecar should have touched the alive marker")
	}
}

// TestServeDrainsFirstTurnOfFreshAgent replays the live first-turn swallow:
// an agent spawned AFTER the sidecar starts pairs its rollout only after its
// first (fast) turn already finished. Those events must still push — the
// spawn stamp marks the pane as fresh, so the rollout drains from byte 0
// instead of baselining at end-of-file as "history".
func TestServeDrainsFirstTurnOfFreshAgent(t *testing.T) {
	old := spoolHome
	spoolHome = t.TempDir()
	defer func() { spoolHome = old }()
	oldSweep := sweepEvery
	sweepEvery = 5 * time.Millisecond
	defer func() { sweepEvery = oldSweep }()

	// Rollout already holds a completed first turn before the sweep ever
	// sees it (pairing lagged the turn).
	rollout := filepath.Join(t.TempDir(), "rollout-fresh.jsonl")
	lines := metaLine(time.Now(), "/work") + "\n" +
		`{"type":"event_msg","payload":{"type":"task_started"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"CHANNEL-TEST-OK"}}` + "\n"
	if err := os.WriteFile(rollout, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	agentsFmt := "#{pane_id}\t#{@duck_name}\t#{@duck_kind}\t#{@duck_anchor}\t#{@duck_panel_role}\t#{pane_current_command}\t#{pane_title}"
	f := &fakeRunner{out: map[string]string{
		"list-sessions -F #{session_name}\t#{@duck_panel_of}":      "work\t\nwork-agents\twork\n",
		"list-panes -s -t work -F #{pane_id}\t#{@duck_panel_role}": "%5\tviewport\n",
		"list-panes -s -t work -F " + agentsFmt:                    "%5\tterminal\tshells\t\tviewport\tzsh\t\n",
		"list-panes -s -t work-agents -F " + agentsFmt:             "%7\tchantest\tagents\t\t\tcodex\t\n",
		"show-options -p -t %7 -v @duck_rollout":                   rollout + "\n",
		// Spawned "now" — after the sidecar's start stamp.
		"show-options -p -t %7 -v @duck_spawned_at": fmt.Sprintf("%d\n", time.Now().Unix()),
	}}
	pr, pw := io.Pipe()
	var out lockedBuffer
	done := make(chan error, 1)
	go func() { done <- Serve(f.run, "work", pr, &out) }()
	io.WriteString(pw, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n")
	io.WriteString(pw, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")

	deadline := time.Now().Add(3 * time.Second)
	var types []string
	for time.Now().Before(deadline) {
		types = types[:0]
		for _, m := range parseLines(out.String()) {
			if m["method"] == "notifications/claude/channel" {
				meta := m["params"].(map[string]any)["meta"].(map[string]any)
				types = append(types, meta["type"].(string))
			}
		}
		if len(types) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	pw.Close()
	<-done
	// The task_started drained in the same sweep as its task_complete, so
	// only the completion pushes — a started for an already-finished turn is
	// noise.
	if len(types) != 1 || types[0] != "task_complete" {
		t.Fatalf("first turn must push exactly task_complete, got %v; output:\n%s", types, out.String())
	}
}

// TestServeBaselinesPreexistingAgentAtEnd: a pane spawned BEFORE the sidecar
// keeps the history-suppression baseline — nothing already in its rollout is
// replayed at the first sweep.
func TestServeBaselinesPreexistingAgentAtEnd(t *testing.T) {
	old := spoolHome
	spoolHome = t.TempDir()
	defer func() { spoolHome = old }()
	oldSweep := sweepEvery
	sweepEvery = 5 * time.Millisecond
	defer func() { sweepEvery = oldSweep }()

	rollout := filepath.Join(t.TempDir(), "rollout-old.jsonl")
	lines := metaLine(time.Now().Add(-time.Hour), "/work") + "\n" +
		`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"stale"}}` + "\n"
	if err := os.WriteFile(rollout, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	agentsFmt := "#{pane_id}\t#{@duck_name}\t#{@duck_kind}\t#{@duck_anchor}\t#{@duck_panel_role}\t#{pane_current_command}\t#{pane_title}"
	f := &fakeRunner{out: map[string]string{
		"list-sessions -F #{session_name}\t#{@duck_panel_of}":      "work\t\nwork-agents\twork\n",
		"list-panes -s -t work -F #{pane_id}\t#{@duck_panel_role}": "%5\tviewport\n",
		"list-panes -s -t work -F " + agentsFmt:                    "%5\tterminal\tshells\t\tviewport\tzsh\t\n",
		"list-panes -s -t work-agents -F " + agentsFmt:             "%7\toldtimer\tagents\t\t\tcodex\t\n",
		"show-options -p -t %7 -v @duck_rollout":                   rollout + "\n",
		"show-options -p -t %7 -v @duck_spawned_at":                fmt.Sprintf("%d\n", time.Now().Add(-time.Hour).Unix()),
	}}
	pr, pw := io.Pipe()
	var out lockedBuffer
	done := make(chan error, 1)
	go func() { done <- Serve(f.run, "work", pr, &out) }()
	io.WriteString(pw, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n")
	io.WriteString(pw, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")

	time.Sleep(150 * time.Millisecond) // several sweeps
	pw.Close()
	<-done
	for _, m := range parseLines(out.String()) {
		if m["method"] == "notifications/claude/channel" {
			t.Fatalf("stale history must not replay: %v", m)
		}
	}
}

// lockedBuffer is a tiny goroutine-safe buffer: Serve's watch goroutine writes
// while the test reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func parseLines(s string) []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}
