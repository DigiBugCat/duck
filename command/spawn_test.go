package command

import (
	"strings"
	"testing"
)

// TestWithCodexFullAccess pins the default-full-access rule for spawned codex
// agents: injected for bare codex (and codex exec), never for other commands,
// and never when the user stated their own approval/sandbox preference.
func TestWithCodexFullAccess(t *testing.T) {
	bypass := "--dangerously-bypass-approvals-and-sandbox"
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"codex"}, "codex " + bypass},
		{[]string{"codex", "exec", "fix it"}, "codex exec " + bypass + " fix it"},
		{[]string{"codex", "--full-auto"}, "codex --full-auto"},                 // explicit choice wins
		{[]string{"codex", "-s", "read-only"}, "codex -s read-only"},            // explicit sandbox wins
		{[]string{"codex", "--sandbox=read-only"}, "codex --sandbox=read-only"}, // = form too
		{[]string{"cargo", "watch"}, "cargo watch"},                             // non-codex untouched
		{nil, ""}, // shell agent untouched
	}
	for _, c := range cases {
		got := strings.Join(withCodexFullAccess(c.in), " ")
		if got != c.want {
			t.Errorf("withCodexFullAccess(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCodexInsertAt: injected flags land after the SUBCOMMAND when one is
// present (exec/resume/fork/review), else right after "codex". Getting this
// wrong put --sandbox before `resume`, which codex rejects.
func TestCodexInsertAt(t *testing.T) {
	cases := map[int][]string{
		1: {"codex"},
		2: {"codex", "exec", "prompt"},
	}
	for want, args := range cases {
		if got := codexInsertAt(args); got != want {
			t.Errorf("codexInsertAt(%v) = %d, want %d", args, got, want)
		}
	}
	for _, sub := range []string{"resume", "fork", "e", "review"} {
		if got := codexInsertAt([]string{"codex", sub, "x"}); got != 2 {
			t.Errorf("codexInsertAt(codex %s) = %d, want 2", sub, got)
		}
	}
	// The injectors place the sandbox flag AFTER resume/fork, not before.
	line := strings.Join(withCodexFullAccess([]string{"codex", "resume", "SID"}), " ")
	if strings.Index(line, "--dangerously-bypass-approvals-and-sandbox") < strings.Index(line, "resume") {
		t.Fatalf("sandbox flag must follow the subcommand: %s", line)
	}
}
