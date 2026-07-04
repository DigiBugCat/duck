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
