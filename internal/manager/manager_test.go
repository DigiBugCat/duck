package manager

import (
	"strings"
	"testing"
)

// quotedHookSettings is the --settings tail Line appends (hookSettings has no
// single quotes, so paths.Quote wraps it in one pair).
const quotedHookSettings = " --settings '" + hookSettings + "'"

func TestLineAppendsChannelFlags(t *testing.T) {
	t.Setenv("DUCK_NO_CHANNELS", "")
	got := Line(nil)
	want := "claude " +
		"'--dangerously-load-development-channels' 'server:duck-agents'" +
		quotedHookSettings
	if got != want {
		t.Fatalf("Line(nil) =\n  %q\nwant\n  %q", got, want)
	}
}

func TestLineForwardsArgsVerbatimBeforeChannelFlags(t *testing.T) {
	t.Setenv("DUCK_NO_CHANNELS", "")
	got := Line([]string{"--ben", "--model", "opus"})
	// profile/claude args come first, each shell-quoted, then the channel flags.
	want := "claude '--ben' '--model' 'opus' " +
		"'--dangerously-load-development-channels' 'server:duck-agents'" +
		quotedHookSettings
	if got != want {
		t.Fatalf("Line =\n  %q\nwant\n  %q", got, want)
	}
}

func TestLineQuotesArgsWithSpaces(t *testing.T) {
	t.Setenv("DUCK_NO_CHANNELS", "")
	got := Line([]string{"--append-system-prompt", "be nice"})
	if !strings.Contains(got, "'be nice'") {
		t.Fatalf("space-bearing arg not single-quoted as one token: %q", got)
	}
	// The arg must land BEFORE the channel flags.
	if strings.Index(got, "'be nice'") > strings.Index(got, "--dangerously-load-development-channels") {
		t.Fatalf("arg came after channel flags: %q", got)
	}
}

func TestLineNoChannelsWhenEnvSet(t *testing.T) {
	t.Setenv("DUCK_NO_CHANNELS", "1")
	got := Line([]string{"--ben"})
	if strings.Contains(got, "--channels") || strings.Contains(got, "development-channels") {
		t.Fatalf("DUCK_NO_CHANNELS set but channel flags present: %q", got)
	}
	if got != "claude '--ben'"+quotedHookSettings {
		t.Fatalf("unexpected line under DUCK_NO_CHANNELS: %q", got)
	}
}

func TestLineDedupsExplicitSettings(t *testing.T) {
	t.Setenv("DUCK_NO_CHANNELS", "")
	// User already passed --settings: duck must NOT append its hook overlay
	// (claude takes one --settings; duck must not fight the user's).
	for _, args := range [][]string{
		{"--settings", "/tmp/mine.json"},
		{"--settings={}"},
	} {
		got := Line(args)
		if strings.Count(got, "--settings") != 1 {
			t.Fatalf("--settings duplicated for %v: %q", args, got)
		}
		if strings.Contains(got, "UserPromptSubmit") {
			t.Fatalf("hook overlay appended despite explicit --settings %v: %q", args, got)
		}
	}
}

func TestLineDedupsExplicitChannels(t *testing.T) {
	t.Setenv("DUCK_NO_CHANNELS", "")
	// User already passed --channels: duck must NOT append its own set again.
	got := Line([]string{"--channels", "server:mine"})
	if strings.Count(got, "--channels") != 1 {
		t.Fatalf("--channels duplicated: %q", got)
	}
	if strings.Contains(got, "server:duck-agents") {
		t.Fatalf("duck appended its channel spec despite explicit --channels: %q", got)
	}
}

func TestLineDedupsChannelsEqualsForm(t *testing.T) {
	t.Setenv("DUCK_NO_CHANNELS", "")
	got := Line([]string{"--channels=server:mine"})
	if strings.Contains(got, "server:duck-agents") {
		t.Fatalf("--channels=... form not recognized as wired: %q", got)
	}
}

func TestLineDedupsDevChannelsFlag(t *testing.T) {
	t.Setenv("DUCK_NO_CHANNELS", "")
	got := Line([]string{"--dangerously-load-development-channels"})
	if strings.Contains(got, "--channels") {
		t.Fatalf("appended --channels despite explicit dev-channels flag: %q", got)
	}
	if strings.Count(got, "--dangerously-load-development-channels") != 1 {
		t.Fatalf("dev-channels flag duplicated: %q", got)
	}
}

func TestChannelsWired(t *testing.T) {
	t.Setenv("DUCK_NO_CHANNELS", "")
	cases := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"--ben"}, false},
		{[]string{"--channels", "x"}, true},
		{[]string{"--channels=x"}, true},
		{[]string{"--dangerously-load-development-channels"}, true},
	}
	for _, c := range cases {
		if got := ChannelsWired(c.args); got != c.want {
			t.Errorf("ChannelsWired(%v) = %v, want %v", c.args, got, c.want)
		}
	}
	t.Setenv("DUCK_NO_CHANNELS", "1")
	if !ChannelsWired(nil) {
		t.Error("ChannelsWired(nil) should be true when DUCK_NO_CHANNELS is set")
	}
}
