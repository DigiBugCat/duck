package command

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeRunner mirrors internal/channel's test fake: scripted argv → output.
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

// agentsLine builds one list-panes agents-format line (pane_id, name, kind,
// anchor, command, title).
func agentsLine(pane, name, cmd string) string {
	return pane + "\t" + name + "\t\t\t" + cmd + "\t"
}

func TestPaletteEntries(t *testing.T) {
	f := &fakeRunner{out: map[string]string{
		// Window names are free text; the last one exercises a name with tabs'
		// worth of trouble (spaces + colon) and the trailing-field tolerance.
		"list-windows -t work -F #{window_index}\t#{window_name}": "0\tmain\n1\tcodex-1\n2\tbuild: watch\n",
		"list-panes -s -t work -F " + "#{pane_id}\t#{@duck_name}\t#{@duck_kind}\t#{@duck_anchor}\t#{pane_current_command}\t#{pane_title}": agentsLine("%3", "codex-1", "codex") + "\n" + agentsLine("%5", "build", "cargo"),
	}}
	entries, err := paletteEntries(f.run, "work")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.kind+"|"+e.label+"|"+e.arg)
	}
	want := []string{
		"jump|jump: main|0",
		"jump|jump: codex-1|1",
		"jump|jump: build: watch|2",
		"kill|kill: codex-1|%3",
		"kill|kill: build|%5",
		"killws|kill workspace|",
		"detach|detach — duck -c resumes|",
		"fleet|fleet — every agent|",
	}
	if len(got) != len(want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPaletteEntriesNoAgents(t *testing.T) {
	// A fresh workspace: one window, zero agents — only jump + session verbs.
	f := &fakeRunner{out: map[string]string{
		"list-windows -t work -F #{window_index}\t#{window_name}": "0\tmain\n",
	}}
	entries, err := paletteEntries(f.run, "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("want jump + 3 session verbs, got %d: %+v", len(entries), entries)
	}
	if entries[0].kind != "jump" || entries[1].kind != "killws" {
		t.Fatalf("unexpected order: %+v", entries)
	}
}

func TestPaneMetasTolerantParse(t *testing.T) {
	spawned := time.Now().Add(-90 * time.Second).Unix()
	f := &fakeRunner{out: map[string]string{
		"list-panes -s -t work -F " + fleetMetaFormat: "%1\t0\t\t\n" + // manager: no stamps
			"%3\t1\t" + strconv.FormatInt(spawned, 10) + "\tcodex --model gpt-5.5\n" +
			"%5\t2\t\t", // trailing line, EMPTY trailing field (the TrimSpace trap)
	}}
	metas, err := paneMetas(f.run, "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 3 {
		t.Fatalf("want 3 panes, got %d: %+v", len(metas), metas)
	}
	if m := metas["%3"]; m.windowIndex != "1" || m.cmd != "codex --model gpt-5.5" || m.spawnedAt.IsZero() {
		t.Fatalf("%%3 meta = %+v", m)
	}
	if m := metas["%5"]; m.windowIndex != "2" || m.cmd != "" || !m.spawnedAt.IsZero() {
		t.Fatalf("%%5 (empty trailing fields) meta = %+v", m)
	}
}

func TestFmtAge(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{12 * time.Second, "12s"},
		{3 * time.Minute, "3m"},
		{2 * time.Hour, "2h"},
		{5 * 24 * time.Hour, "5d"},
	}
	for _, c := range cases {
		if got := fmtAge(now.Add(-c.ago), now); got != c.want {
			t.Errorf("fmtAge(-%v) = %q, want %q", c.ago, got, c.want)
		}
	}
	if got := fmtAge(time.Time{}, now); got != "?" {
		t.Errorf("zero spawnedAt = %q, want ?", got)
	}
}
