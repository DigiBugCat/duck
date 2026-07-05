package channel

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DigiBugCat/duck/internal/window"
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

func TestThreadID(t *testing.T) {
	cases := map[string]string{
		"/a/b/2026/07/04/rollout-2026-07-04T14-09-56-019f2ef7-7345-7e13-a443-651cf28b427e.jsonl": "019f2ef7-7345-7e13-a443-651cf28b427e",
		"rollout-2026-07-04T14-09-56-019f2ef7-7345-7e13-a443-651cf28b427e.jsonl":                 "019f2ef7-7345-7e13-a443-651cf28b427e",
		"/some/other/file.jsonl": "",
		"":                       "",
		"rollout-short.jsonl":    "", // fewer than 5 trailing groups
	}
	for in, want := range cases {
		if got := threadID(in); got != want {
			t.Errorf("threadID(%q) = %q, want %q", in, got, want)
		}
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
	mine := write("rollout-mine.jsonl", "/work", spawn.Add(1*time.Second))
	later := write("rollout-later.jsonl", "/work", spawn.Add(30*time.Second))

	// TWO unclaimed /work candidates (mine + later) = ambiguous. matchRollout
	// must refuse to guess (empty, no error) rather than adopt the earliest —
	// guessing is the fan-out scramble. HandleNotify's exact thread-id pin
	// resolves it later. The old/other files are correctly filtered (age, cwd).
	got, err := matchRollout(root, "/work", spawn, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("two same-cwd candidates must be ambiguous → empty, got %s", got)
	}
	// No candidates → empty, no error (codex still starting).
	got, err = matchRollout(root, "/nowhere", spawn, nil)
	if err != nil || got != "" {
		t.Fatalf("no-match should be empty+nil, got %q %v", got, err)
	}
	// Claiming one candidate disambiguates: exactly ONE unclaimed /work rollout
	// remains, so it pairs. This is the legit sequential case — one agent already
	// paired, the next takes the remaining stream.
	got, err = matchRollout(root, "/work", spawn, map[string]bool{mine: true})
	if err != nil || got != later {
		t.Fatalf("one unclaimed candidate must pair; want %s, got %q %v", later, got, err)
	}
	// And with the single genuine candidate (later removed from the picture),
	// the lone match pairs immediately — the common one-agent case.
	got, err = matchRollout(root, "/work", spawn, map[string]bool{later: true})
	if err != nil || got != mine {
		t.Fatalf("lone candidate must pair; want %s, got %q %v", mine, got, err)
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

// TestResolveRefusesAmbiguousConcurrentSpawns is the fan-out scramble repro:
// two codex panes launched in the SAME cwd near-simultaneously, two rollout
// files present, neither claimed. Correlation-pairing must NOT guess — both
// panes defer (empty Rollout) rather than adopt the same earliest stream and
// cross-attribute. Then, when one pane's stream is claimed (as HandleNotify's
// exact thread-id pin would do), the other pane pairs to the REMAINING stream.
func TestResolveRefusesAmbiguousConcurrentSpawns(t *testing.T) {
	root := t.TempDir()
	day := filepath.Join(root, "2026", "07", "03")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	spawn := time.Now().Add(-time.Minute)
	rollA := filepath.Join(day, "rollout-a.jsonl")
	rollB := filepath.Join(day, "rollout-b.jsonl")
	if err := os.WriteFile(rollA, []byte(metaLine(spawn.Add(1*time.Second), "/work")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rollB, []byte(metaLine(spawn.Add(2*time.Second), "/work")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DUCK_CODEX_SESSIONS", root)

	// Two panes, both codex, both in /work. No @duck_rollout pinned on either,
	// and list-panes -a reports no claims yet (both fresh).
	newFake := func(pane string) *fakeRunner {
		return &fakeRunner{out: map[string]string{
			"show-options -p -t " + pane + " -v @duck_rollout":     "\n",
			"show-options -p -t " + pane + " -v @duck_cmd":         "codex --model gpt-5\n",
			"show-options -p -t " + pane + " -v @duck_spawned_at":  fmt.Sprintf("%d\n", spawn.Unix()),
			"display-message -p -t " + pane + " #{pane_current_path}": "/work\n",
			"list-panes -a -F #{@duck_rollout}":                     "\n", // nothing claimed
		}}
	}
	// Pane %1: ambiguous (two unclaimed /work candidates) → must NOT pair.
	f1 := newFake("%1")
	ref1 := AgentRef{WindowID: "%1"}
	if err := Resolve(f1.run, &ref1); err != nil {
		t.Fatal(err)
	}
	if ref1.Rollout != "" {
		t.Fatalf("ambiguous concurrent spawn must defer, got %q", ref1.Rollout)
	}
	// Pane %2: same ambiguity → also defers.
	f2 := newFake("%2")
	ref2 := AgentRef{WindowID: "%2"}
	if err := Resolve(f2.run, &ref2); err != nil {
		t.Fatal(err)
	}
	if ref2.Rollout != "" {
		t.Fatalf("ambiguous concurrent spawn must defer, got %q", ref2.Rollout)
	}
	// Now rollA is claimed (e.g. HandleNotify pinned it to %1). Pane %2 re-resolves
	// and pairs to the REMAINING stream, rollB — never rollA.
	f2b := newFake("%2")
	f2b.out["list-panes -a -F #{@duck_rollout}"] = rollA + "\n"
	ref2 = AgentRef{WindowID: "%2"}
	if err := Resolve(f2b.run, &ref2); err != nil {
		t.Fatal(err)
	}
	if ref2.Rollout != rollB {
		t.Fatalf("with rollA claimed, %%2 must pair to rollB; got %q", ref2.Rollout)
	}
}

// TestHandleNotifyPinsRolloutByThreadID: the codex notify hook carries the
// thread id (a UUIDv7 whose first 48 bits are unix-ms), which names the date
// partition directly — the rollout is located and pinned with no tree walk.
func TestHandleNotifyPinsRolloutByThreadID(t *testing.T) {
	root := t.TempDir()
	// Thread id minted from a known time so the date dir is derived, not
	// hardcoded to "today".
	ts := time.Date(2026, 7, 4, 12, 0, 0, 0, time.Local)
	hexMS := fmt.Sprintf("%012x", ts.UnixMilli())
	threadID := hexMS[:8] + "-" + hexMS[8:12] + "-7000-8000-000000000001"
	day := filepath.Join(root, ts.Format("2006/01/02"))
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(day, "rollout-2026-07-04T12-00-00-"+threadID+".jsonl")
	if err := os.WriteFile(rollout, []byte(metaLine(ts, "/work")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DUCK_CODEX_SESSIONS", root)

	f := &fakeRunner{out: map[string]string{}}
	payload := fmt.Sprintf(`{"type":"agent-turn-complete","thread-id":%q,"last-assistant-message":"done"}`, threadID)
	if err := HandleNotify(f.run, "%7", payload); err != nil {
		t.Fatal(err)
	}
	want := "set-option -p -t %7 @duck_rollout " + rollout
	found := false
	for _, c := range f.calls {
		if c == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("notify must pin the rollout option; calls=%v", f.calls)
	}
	// Outside tmux → quiet no-op.
	if err := HandleNotify(f.run, "", payload); err != nil {
		t.Fatal(err)
	}
	// Unknown thread id → no pin (but the breadcrumb check may still probe).
	n := len(f.calls)
	if err := HandleNotify(f.run, "%7", `{"type":"agent-turn-complete","thread-id":"019f0000-0000-7000-8000-00000000dead"}`); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls[n:] {
		if strings.HasPrefix(c, "set-option") {
			t.Fatalf("unknown thread id must not pin: %v", f.calls[n:])
		}
	}
}

// TestHandleNotifyLeavesRunBreadcrumb: a completing routine executor
// (kind=runs) leaves a courier breadcrumb for its OWNER workspace, carrying
// the routine name and last message — even though its pane dies immediately.
func TestHandleNotifyLeavesRunBreadcrumb(t *testing.T) {
	old := spoolHome
	spoolHome = t.TempDir()
	defer func() { spoolHome = old }()
	t.Setenv("DUCK_CODEX_SESSIONS", t.TempDir()) // no rollout on disk — pin skipped

	f := &fakeRunner{out: map[string]string{
		"display-message -p -t %9 #{@duck_kind}\t#{@duck_name}\t#{session_name}": "runs\tnightly\twork-agents\n",
		"show-options -t work-agents -v @duck_panel_of":                          "work\n",
	}}
	payload := `{"type":"agent-turn-complete","thread-id":"019f2f69-33a2-7d02-909d-b8f0d1328621","last-assistant-message":"all green\ndetails below"}`
	if err := HandleNotify(f.run, "%9", payload); err != nil {
		t.Fatal(err)
	}
	got, err := DrainReports("work")
	if err != nil || len(got) != 1 {
		t.Fatalf("want 1 breadcrumb for owner workspace, got %v err=%v", got, err)
	}
	if got[0].Routine != "nightly" || !strings.HasPrefix(got[0].Message, "all green") {
		t.Fatalf("breadcrumb wrong: %+v", got[0])
	}
	// Drained means gone.
	if again, _ := DrainReports("work"); len(again) != 0 {
		t.Fatalf("drain must consume: %v", again)
	}
	// Non-run panes leave nothing.
	f.out["display-message -p -t %9 #{@duck_kind}\t#{@duck_name}\t#{session_name}"] = "agents\tchat\twork-agents\n"
	if err := HandleNotify(f.run, "%9", payload); err != nil {
		t.Fatal(err)
	}
	if crumbs, _ := DrainReports("work"); len(crumbs) != 0 {
		t.Fatalf("non-run pane must not leave breadcrumbs: %v", crumbs)
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
	// No rollout pairing + empty capture → composer clear (double-checked)
	// → exactly one Enter.
	f := &fakeRunner{out: map[string]string{}}
	if err := Send(f.run, AgentRef{WindowID: "%3"}, "fix the tests"); err != nil {
		t.Fatal(err)
	}
	enters, captures := 0, 0
	for _, c := range f.calls {
		if c == "send-keys -t %3 Enter" {
			enters++
		}
		if strings.HasPrefix(c, "capture-pane") {
			captures++
		}
	}
	if enters != 1 || captures != 2 {
		t.Fatalf("want 1 Enter + 2 confirming captures, calls: %v", f.calls)
	}
}

func TestSendRetriesEnterWhilePasteSitsInComposer(t *testing.T) {
	// The composer NEVER clears in this fake — Send must keep retrying and
	// then report failure honestly instead of claiming success.
	f := &fakeRunner{out: map[string]string{
		"capture-pane -p -t %3": "transcript above\n› [Pasted Content 1018 chars]\nfooter",
	}}
	err := Send(f.run, AgentRef{WindowID: "%3"}, "a long prompt")
	if err == nil {
		t.Fatal("a stuck composer must surface as an error")
	}
	enters := 0
	for _, c := range f.calls {
		if c == "send-keys -t %3 Enter" {
			enters++
		}
	}
	if enters != 6 {
		t.Fatalf("want 6 Enter attempts, got %d", enters)
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

func TestServeSweepsWindowMarksOnceWithWorkspaceAttribution(t *testing.T) {
	oldHome := spoolHome
	spoolHome = t.TempDir()
	defer func() { spoolHome = oldHome }()
	oldSweep := sweepEvery
	sweepEvery = 5 * time.Millisecond
	defer func() { sweepEvery = oldSweep }()
	oldHost := windowMarksHost
	oldClient := windowMarksHTTPClient
	defer func() {
		windowMarksHost = oldHost
		windowMarksHTTPClient = oldClient
	}()

	var queried []string
	marks := []map[string]any{{
		"type":      "highlight",
		"workspace": "work",
		"url":       "http://artifact.test/report",
		"text":      "Q3 revenue",
		"comment":   "off by 10x",
		"stamp":     "2026-07-05T12:00:00Z",
	}}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/marks" {
			http.NotFound(w, r)
			return
		}
		queried = append(queried, r.URL.Query().Get("workspace"))
		_ = json.NewEncoder(w).Encode(marks)
	}))
	defer host.Close()
	windowMarksHost = func() string { return strings.TrimPrefix(host.URL, "http://") }
	windowMarksHTTPClient = host.Client()

	f := &fakeRunner{out: map[string]string{}}
	pr, pw := io.Pipe()
	var out lockedBuffer
	done := make(chan error, 1)
	go func() { done <- Serve(f.run, "work", pr, &out) }()
	io.WriteString(pw, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n")
	io.WriteString(pw, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")

	deadline := time.Now().Add(3 * time.Second)
	var got []map[string]any
	for time.Now().Before(deadline) {
		got = got[:0]
		for _, m := range parseLines(out.String()) {
			if m["method"] == "notifications/claude/channel" {
				got = append(got, m)
			}
		}
		if len(got) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(40 * time.Millisecond) // allow extra sweeps that would double-deliver
	pw.Close()
	<-done

	if len(queried) == 0 || queried[0] != "work" {
		t.Fatalf("host queried with workspace %v, want work", queried)
	}
	if len(got) != 1 {
		t.Fatalf("want exactly one mark notification, got %d; output:\n%s", len(got), out.String())
	}
	params := got[0]["params"].(map[string]any)
	if params["source"] != "duck-window" {
		t.Fatalf("source = %v, want duck-window", params["source"])
	}
	if content := params["content"].(string); !strings.Contains(content, "mark highlight") || !strings.Contains(content, "off by 10x") || !strings.Contains(content, "Q3 revenue") {
		t.Fatalf("content did not summarize mark: %q", content)
	}
	meta := params["meta"].(map[string]any)
	if meta["session"] != "work" || meta["source"] != "duck-window" || meta["type"] != "mark" {
		t.Fatalf("meta wrong: %v", meta)
	}
	attachments := params["attachments"].([]any)
	attached := attachments[0].(map[string]any)
	if attached["type"] != "json" || attached["name"] != "mark" {
		t.Fatalf("attachment header wrong: %v", attached)
	}
	full := attached["content"].(map[string]any)
	if full["workspace"] != "work" || full["text"] != "Q3 revenue" {
		t.Fatalf("full mark JSON not attached: %v", full)
	}
	if cursor := readWindowMarkCursor("work"); cursor != 1 {
		t.Fatalf("cursor = %d, want 1", cursor)
	}

	var out2 lockedBuffer
	pr2, pw2 := io.Pipe()
	done2 := make(chan error, 1)
	go func() { done2 <- Serve(f.run, "work", pr2, &out2) }()
	io.WriteString(pw2, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n")
	io.WriteString(pw2, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")
	time.Sleep(40 * time.Millisecond)
	pw2.Close()
	<-done2
	for _, m := range parseLines(out2.String()) {
		if m["method"] == "notifications/claude/channel" {
			t.Fatalf("persisted cursor must suppress replay after restart: %v", m)
		}
	}
}

func TestWindowMarkMessageIncludesRectShotAndFullJSON(t *testing.T) {
	raw := json.RawMessage(`{"type":"drawing","workspace":"work","url":"http://artifact.test/chart","comment":"look here","rect":{"x":10,"y":20,"w":30,"h":40},"shot":"/tmp/shot.png","stamp":"2026-07-05T12:00:00Z"}`)
	var m struct {
		Params struct {
			Source      string `json:"source"`
			Content     string `json:"content"`
			Attachments []struct {
				Content map[string]any `json:"content"`
			} `json:"attachments"`
		} `json:"params"`
	}
	var out bytes.Buffer
	s := &server{out: &out}
	var mark window.Mark
	if err := json.Unmarshal(raw, &mark); err != nil {
		t.Fatal(err)
	}
	s.emitWindowMark("work", mark, raw)
	line := strings.TrimSpace(out.String())
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("event is not JSON: %v\n%s", err, line)
	}
	if m.Params.Source != "duck-window" {
		t.Fatalf("source = %q", m.Params.Source)
	}
	if !strings.Contains(m.Params.Content, "rect: 10,20 30x40") || !strings.Contains(m.Params.Content, "shot: /tmp/shot.png") {
		t.Fatalf("drawing summary missing rect/shot: %q", m.Params.Content)
	}
	if m.Params.Attachments[0].Content["type"] != "drawing" || m.Params.Attachments[0].Content["shot"] != "/tmp/shot.png" {
		t.Fatalf("full mark JSON missing: %+v", m.Params.Attachments[0].Content)
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

// TestSendConfirmsViaRollout: with a paired rollout, Send trusts ONLY the
// rollout — it keeps pressing Enter until a task_started event appears past
// the pre-send offset, ignoring the composer heuristic entirely.
func TestSendConfirmsViaRollout(t *testing.T) {
	rollout := filepath.Join(t.TempDir(), "r.jsonl")
	if err := os.WriteFile(rollout, []byte(`{"type":"event_msg","payload":{"type":"task_started"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err) // pre-existing event BEFORE the send offset — must not count
	}
	enters := 0
	run := func(args ...string) (string, error) {
		key := strings.Join(args, " ")
		if key == "send-keys -t %3 Enter" {
			enters++
			if enters == 2 { // paste ingested on the second Enter: turn begins
				f, _ := os.OpenFile(rollout, os.O_APPEND|os.O_WRONLY, 0o644)
				fmt.Fprintln(f, `{"type":"event_msg","payload":{"type":"task_started"}}`)
				f.Close()
			}
		}
		if strings.HasPrefix(key, "show-options") {
			return rollout + "\n", nil // cached pairing
		}
		if strings.HasPrefix(key, "capture-pane") {
			t.Fatal("rollout is authoritative — composer must not be consulted")
		}
		return "", nil
	}
	if err := Send(run, AgentRef{WindowID: "%3"}, "a long brief"); err != nil {
		t.Fatal(err)
	}
	if enters != 2 {
		t.Fatalf("want 2 Enters (retry until rollout confirms), got %d", enters)
	}
}
