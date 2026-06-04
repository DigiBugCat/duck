package namer

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/DigiBugCat/duck/internal/names"
)

// fakeRunner records the hub-side capture command and returns canned pane text.
type fakeRunner struct {
	cmds []string
	out  string
}

func (f *fakeRunner) Run(cmd string) (string, error) {
	f.cmds = append(f.cmds, cmd)
	return f.out, nil
}
func (f *fakeRunner) RunInput(cmd string, _ io.Reader) (string, error) {
	f.cmds = append(f.cmds, cmd)
	return f.out, nil
}

// fakeExec records the local codex args + stdin and returns a canned title. It
// stands in for the laptop-side `codex exec` so no real process runs.
type fakeExec struct {
	args  []string
	stdin string
	title string
}

func (f *fakeExec) Run(_ context.Context, args []string, stdin io.Reader) (string, error) {
	f.args = args
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		f.stdin = string(b)
	}
	return f.title, nil
}

func TestCaptureHeadPipeline(t *testing.T) {
	r := &fakeRunner{out: "hello from pane"}
	c := NewCodexExec(r, &fakeExec{}, "gpt-5-mini")
	out, err := c.CaptureHead("foo-2")
	if err != nil {
		t.Fatalf("CaptureHead: %v", err)
	}
	if out != "hello from pane" {
		t.Fatalf("CaptureHead returned %q", out)
	}
	// The EXACT capture pipeline PLAN §M3 specifies (head of the pane, bounded).
	want := "tmux capture-pane -p -S - -t 'foo-2' | head -n 200 | head -c 8000"
	if len(r.cmds) != 1 || r.cmds[0] != want {
		t.Fatalf("capture cmd =\n  %q\nwant\n  %q", r.cmds, want)
	}
}

func TestNameBuildsCodexExecArgsAndPipesSnapshot(t *testing.T) {
	fe := &fakeExec{title: "Auth Refactor\n"}
	c := NewCodexExec(&fakeRunner{}, fe, "gpt-5-mini")
	title, err := c.Name(context.Background(), "captured pane head")
	if err != nil {
		t.Fatalf("Name: %v", err)
	}
	if title != "Auth Refactor" {
		t.Fatalf("Name returned %q, want trimmed 'Auth Refactor'", title)
	}
	// codex args: exec -m <model> -c model_reasoning_effort=low <prompt>.
	args := fe.args
	if len(args) < 5 || args[0] != "exec" {
		t.Fatalf("codex args malformed: %v", args)
	}
	if !hasPair(args, "-m", "gpt-5-mini") {
		t.Fatalf("codex must pass -m <model>: %v", args)
	}
	if !hasPair(args, "-c", "model_reasoning_effort=low") {
		t.Fatalf("codex must fix reasoning effort low (DESIGN §5): %v", args)
	}
	// The snapshot is piped on STDIN, never as an argument.
	if fe.stdin != "captured pane head" {
		t.Fatalf("snapshot must be piped on stdin, got %q", fe.stdin)
	}
	for _, a := range args {
		if strings.Contains(a, "captured pane head") {
			t.Fatalf("snapshot must not appear in argv: %v", args)
		}
	}
}

func TestNameEmptyTitleIsError(t *testing.T) {
	c := NewCodexExec(&fakeRunner{}, &fakeExec{title: "   \n"}, "m")
	if _, err := c.Name(context.Background(), "x"); err == nil {
		t.Fatalf("an empty codex title must be an error so the caller falls back")
	}
}

func TestHashStableAndChangesWithContent(t *testing.T) {
	a := Hash("the pane head")
	b := Hash("the pane head")
	if a != b {
		t.Fatalf("Hash must be stable for identical content: %q vs %q", a, b)
	}
	if a == Hash("a different head") {
		t.Fatalf("Hash must change when content changes")
	}
	if a == "" {
		t.Fatalf("Hash must be non-empty")
	}
}

func TestDirDerivedIgnoresSnapshot(t *testing.T) {
	d := DirDerived{Dir: "~/dev/widget"}
	got, err := d.Name(context.Background(), "irrelevant pane content")
	if err != nil {
		t.Fatalf("DirDerived.Name: %v", err)
	}
	if got != "widget" {
		t.Fatalf("DirDerived should yield the dir floor, got %q", got)
	}
	// It also satisfies Capturer with an empty snapshot.
	var _ Capturer = d
	if snap, _ := d.CaptureHead("x"); snap != "" {
		t.Fatalf("DirDerived.CaptureHead should be empty, got %q", snap)
	}
}

func TestCacheHitSkipsOnlyOnUnchangedHead(t *testing.T) {
	head := "the captured pane head"
	cached := names.Entry{CodexName: "Auth Refactor", CodexHash: Hash(head)}

	// Same head as the name was minted from → hit (skip the codex call).
	if !CacheHit(cached, head) {
		t.Fatalf("a cached name with a matching head hash must be a cache hit (skip)")
	}
	// Materially changed head → miss (re-name).
	if CacheHit(cached, "a completely different head") {
		t.Fatalf("a changed head must be a cache miss so the name regenerates")
	}
	// No cached codex name → miss even if a (stale) hash happens to match.
	noName := names.Entry{CodexHash: Hash(head)}
	if CacheHit(noName, head) {
		t.Fatalf("an empty codex name must be a cache miss so naming is triggered")
	}
}

func hasPair(args []string, flag, val string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == val {
			return true
		}
	}
	return false
}
