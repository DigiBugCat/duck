package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubCodex writes a fake codex binary: emits a plausible --json event stream
// on stdout, and writes a final message to the -o path. The message echoes
// the prompt's REPLY:<...> directive so tests control worker output; a
// prompt asking for JSON gets JSON. Exits 1 when the prompt says FAIL.
func stubCodex(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "codex")
	script := `#!/bin/sh
out=""
prompt=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    *) prompt="$1"; shift ;;
  esac
done
echo '{"type":"thread.started","thread_id":"th-test"}'
echo '{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":10}}'
case "$prompt" in
  *FAIL*) exit 1 ;;
esac
reply="${prompt##*REPLY:}"
# Tests put the whole directive on one line; the schema suffix the engine
# appends starts on a new line — drop it.
reply=$(printf '%s' "$reply" | head -1)
printf '%s' "$reply" > "$out"
`
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func testRun(t *testing.T, script string, o Opts) (*Run, error) {
	t.Helper()
	t.Setenv("DUCK_HOME", t.TempDir())
	old := codexBin
	codexBin = stubCodex(t)
	t.Cleanup(func() { codexBin = old })
	r, err := Prepare(script, o)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return r, r.Execute(context.Background())
}

func readResult(t *testing.T, r *Run) any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.Dir, "result.json"))
	if err != nil {
		t.Fatalf("result.json: %v", err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("result.json parse: %v", err)
	}
	return v
}

const metaHeader = "export const meta = {name: 'test-wf', description: 'test'}\n"

func TestSimpleAgent(t *testing.T) {
	r, err := testRun(t, metaHeader+`
		phase('go')
		const a = await agent('say REPLY:hello')
		return {got: a}
	`, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	v := readResult(t, r).(map[string]any)
	if v["got"] != "hello" {
		t.Fatalf("got %v", v)
	}
	st, err := ReadStatus(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateDone || st.AgentsDone != 1 || st.Tokens != 110 {
		t.Fatalf("status: %+v", st)
	}
}

func TestParallelAndPipeline(t *testing.T) {
	r, err := testRun(t, metaHeader+`
		const both = await parallel([
			() => agent('a REPLY:one'),
			() => agent('b REPLY:two'),
		])
		const piped = await pipeline([1, 2],
			(item) => agent('stage1 for ' + item + ' REPLY:s1-' + item),
			(prev, item, i) => agent('stage2 saw ' + prev + ' REPLY:' + prev + '+s2@' + i),
		)
		return {both, piped}
	`, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	v := readResult(t, r).(map[string]any)
	both := v["both"].([]any)
	if both[0] != "one" || both[1] != "two" {
		t.Fatalf("parallel: %v", both)
	}
	piped := v["piped"].([]any)
	if piped[0] != "s1-1+s2@0" || piped[1] != "s1-2+s2@1" {
		t.Fatalf("pipeline: %v", piped)
	}
}

func TestFailedAgentResolvesNull(t *testing.T) {
	r, err := testRun(t, metaHeader+`
		const rs = await parallel([() => agent('FAIL now'), () => agent('ok REPLY:fine')])
		return rs.filter(Boolean)
	`, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	v := readResult(t, r).([]any)
	if len(v) != 1 || v[0] != "fine" {
		t.Fatalf("got %v", v)
	}
	st, _ := ReadStatus(r.ID)
	if st.AgentsFailed != 1 || st.AgentsDone != 2 {
		t.Fatalf("status: %+v", st)
	}
}

func TestSchemaParsing(t *testing.T) {
	r, err := testRun(t, metaHeader+`
		const v = await agent('REPLY:{"answer": 42}', {schema: {type: 'object'}})
		return v.answer
	`, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if got := readResult(t, r); got != float64(42) {
		t.Fatalf("got %v", got)
	}
}

func TestArgsAndBudget(t *testing.T) {
	r, err := testRun(t, metaHeader+`
		if (args.q !== 'hi') throw new Error('args missing')
		if (budget.total !== 1000) throw new Error('budget total')
		const before = budget.remaining()
		await agent('x REPLY:y')
		if (budget.spent() !== 110) throw new Error('spent ' + budget.spent())
		if (budget.remaining() >= before) throw new Error('remaining did not drop')
		return 'ok'
	`, Opts{Args: json.RawMessage(`{"q":"hi"}`), Budget: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if readResult(t, r) != "ok" {
		t.Fatal("bad result")
	}
}

func TestBudgetExhaustionThrows(t *testing.T) {
	_, err := testRun(t, metaHeader+`
		await agent('one REPLY:a')
		await agent('two REPLY:b')
		return 'unreachable'
	`, Opts{Budget: 50})
	if err == nil || !strings.Contains(err.Error(), "budget exhausted") {
		t.Fatalf("want budget error, got %v", err)
	}
}

func TestResumeReplaysJournal(t *testing.T) {
	t.Setenv("DUCK_HOME", t.TempDir())
	old := codexBin
	codexBin = stubCodex(t)
	t.Cleanup(func() { codexBin = old })

	script := metaHeader + `
		const a = await agent('first REPLY:one')
		const b = await agent('second REPLY:two')
		return [a, b]
	`
	r1, err := Prepare(script, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r1.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Point the stub away: a resume that re-runs anything would now fail.
	codexBin = "/nonexistent-codex"
	r2, err := Prepare(script, Opts{ResumeFrom: r1.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := r2.Execute(context.Background()); err != nil {
		t.Fatalf("resume should be fully cached: %v", err)
	}
	st, _ := ReadStatus(r2.ID)
	if st.AgentsCached != 2 {
		t.Fatalf("want 2 cached, got %+v", st)
	}
	v := readResult(t, r2).([]any)
	if v[0] != "one" || v[1] != "two" {
		t.Fatalf("cached values: %v", v)
	}
}

func TestDeadlockDetected(t *testing.T) {
	_, err := testRun(t, metaHeader+`
		await new Promise(() => {})
	`, Opts{})
	if err == nil || !strings.Contains(err.Error(), "never resolve") {
		t.Fatalf("want deadlock error, got %v", err)
	}
}

func TestExtractMeta(t *testing.T) {
	m, err := ExtractMeta("export const meta = {\n  name: 'x-1', // a comment with }\n  description: \"d{\"\n}\nrest")
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "x-1" || m.Description != "d{" {
		t.Fatalf("%+v", m)
	}
	if _, err := ExtractMeta("const notmeta = 1"); err == nil {
		t.Fatal("want error for missing meta")
	}
	if _, err := ExtractMeta("export const meta = {name: '', description: 'd'}"); err == nil {
		t.Fatal("want error for empty name")
	}
}

func TestListAndStatus(t *testing.T) {
	r, err := testRun(t, metaHeader+`return 1`, Opts{Workspace: "ws-a"})
	if err != nil {
		t.Fatal(err)
	}
	ls, err := List("ws-a")
	if err != nil || len(ls) != 1 || ls[0].RunID != r.ID {
		t.Fatalf("list: %v %v", ls, err)
	}
	if ls2, _ := List("other"); len(ls2) != 0 {
		t.Fatal("workspace filter leaked")
	}
}

func TestConcurrencyRespected(t *testing.T) {
	// 8 agents, concurrency 2 — just asserts completion and counts (the stub
	// is instant; this exercises the semaphore path, not timing).
	r, err := testRun(t, metaHeader+fmt.Sprintf(`
		const rs = await parallel(Array.from({length: 8}, (_, i) => () => agent('n' + i + ' REPLY:r' + i)))
		return rs.length
	`), Opts{Concurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := readResult(t, r); got != float64(8) {
		t.Fatalf("got %v", got)
	}
	st, _ := ReadStatus(r.ID)
	if st.AgentsDone != 8 || st.Tokens != 880 {
		t.Fatalf("status: %+v", st)
	}
}

func TestLastActivity(t *testing.T) {
	if got := lastActivity("agent_message", "hello\n  world", "", ""); got != "hello world" {
		t.Fatalf("message: %q", got)
	}
	if got := lastActivity("command_execution", "", "rg -n foo", ""); got != "$ rg -n foo" {
		t.Fatalf("command: %q", got)
	}
	if got := lastActivity("reasoning", "x", "", ""); got != "" {
		t.Fatalf("reasoning should be dropped: %q", got)
	}
	long := strings.Repeat("a", 200)
	if got := lastActivity("agent_message", long, "", ""); len(got) > 170 {
		t.Fatalf("not truncated: %d", len(got))
	}
}
