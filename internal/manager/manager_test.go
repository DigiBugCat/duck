package manager

import (
	"strings"
	"testing"
)

const quotedHookSettings = " --settings '" + hookSettings + "'"

func TestLineAddsActivityHook(t *testing.T) {
	got := Line(nil)
	if got != "claude"+quotedHookSettings {
		t.Fatalf("Line(nil) = %q", got)
	}
	if !strings.Contains(got, ActivityOption) {
		t.Fatalf("activity option missing: %q", got)
	}
}
func TestLineForwardsAndQuotesArgs(t *testing.T) {
	got := Line([]string{"--model", "opus", "--append-system-prompt", "be nice"})
	if !strings.HasPrefix(got, "claude '--model' 'opus' '--append-system-prompt' 'be nice'") {
		t.Fatalf("args not preserved: %q", got)
	}
}
func TestLineHonorsExplicitSettings(t *testing.T) {
	for _, args := range [][]string{{"--settings", "/tmp/mine.json"}, {"--settings={}"}} {
		got := Line(args)
		if strings.Count(got, "--settings") != 1 || strings.Contains(got, ActivityOption) {
			t.Fatalf("settings duplicated for %v: %q", args, got)
		}
	}
}
