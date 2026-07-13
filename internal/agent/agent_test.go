package agent

import (
	"strings"
	"testing"
)

// TestWithFullAccess pins the default-full-access rule for spawned codex agents:
// injected for bare codex (and codex exec/resume/fork), never for other
// commands, and never when the user stated their own approval/sandbox preference.
func TestWithFullAccess(t *testing.T) {
	bypass := "--dangerously-bypass-approvals-and-sandbox"
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"codex"}, "codex " + bypass},
		{[]string{"codex", "exec", "fix it"}, "codex exec " + bypass + " fix it"},
		{[]string{"codex", "--full-auto"}, "codex --full-auto"},
		{[]string{"codex", "-s", "read-only"}, "codex -s read-only"},
		{[]string{"codex", "--sandbox=read-only"}, "codex --sandbox=read-only"},
		{[]string{"cargo", "watch"}, "cargo watch"},
		{nil, ""},
	}
	for _, c := range cases {
		got := strings.Join(WithFullAccess(c.in), " ")
		if got != c.want {
			t.Errorf("WithFullAccess(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCodexInsertAt: injected flags land after the SUBCOMMAND when present
// (exec/resume/fork/review), else right after "codex". Mispositioning put
// --sandbox before `resume`, which codex rejects.
func TestCodexInsertAt(t *testing.T) {
	if got := codexInsertAt([]string{"codex"}); got != 1 {
		t.Errorf("bare codex insert = %d, want 1", got)
	}
	for _, sub := range []string{"exec", "e", "review", "resume", "fork"} {
		if got := codexInsertAt([]string{"codex", sub, "x"}); got != 2 {
			t.Errorf("codexInsertAt(codex %s) = %d, want 2", sub, got)
		}
	}
	line := strings.Join(WithFullAccess([]string{"codex", "resume", "SID"}), " ")
	if strings.Index(line, "--dangerously-bypass-approvals-and-sandbox") < strings.Index(line, "resume") {
		t.Fatalf("sandbox flag must follow the subcommand: %s", line)
	}
}

// TestWire keeps codex launch defaults transport-neutral.
func TestWire(t *testing.T) {
	line := strings.Join(Wire([]string{"codex"}), " ")
	if !strings.Contains(line, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("Wire(codex) = %q", line)
	}
	for _, stale := range []string{"notify=", "hooks=", "channel", "hook-trust"} {
		if strings.Contains(line, stale) {
			t.Fatalf("Wire retained %q in %q", stale, line)
		}
	}
	if got := strings.Join(Wire([]string{"cargo", "watch"}), " "); got != "cargo watch" {
		t.Fatalf("Wire(non-codex) = %q", got)
	}
}
