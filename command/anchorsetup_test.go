package command

import "testing"

func TestRootRegistersAnchor(t *testing.T) {
	names := map[string]bool{}
	for _, c := range rootCmd.Commands() {
		names[c.Name()] = true
	}
	if !names["anchor"] {
		t.Error("rootCmd missing the `anchor` command")
	}
}

func TestAnchorHasSetShow(t *testing.T) {
	names := map[string]bool{}
	for _, c := range anchorCmd.Commands() {
		names[c.Name()] = true
	}
	for _, want := range []string{"set", "show"} {
		if !names[want] {
			t.Errorf("anchor command missing %q subcommand", want)
		}
	}
}

func TestAnchorSetRequiresExactlyOneArg(t *testing.T) {
	if err := anchorSetCmd.Args(anchorSetCmd, nil); err == nil {
		t.Error("anchor set with no args should be rejected")
	}
	if err := anchorSetCmd.Args(anchorSetCmd, []string{"me@host"}); err != nil {
		t.Errorf("anchor set with one arg should be accepted: %v", err)
	}
	if err := anchorSetCmd.Args(anchorSetCmd, []string{"a", "b"}); err == nil {
		t.Error("anchor set with two args should be rejected")
	}
}
