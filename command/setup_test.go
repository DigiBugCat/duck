package command

import (
	"bytes"
	"strings"
	"testing"

	"github.com/DigiBugCat/duck/internal/config"
)

// These tests pin the NON-INTERACTIVE safety of the interactive first-run
// surface (the load-bearing property): under `go test` stdin is not a TTY, so
// isInteractive() is false. Every prompt is gated behind that, so each command
// below must return its instructive error WITHOUT reading stdin — and the test
// completing at all proves it never hangs.

// TestHubSetNoArgNonInteractive: `duck hub set` with no arg and no TTY returns
// the instructive "pass <user@host>" error and does not read stdin.
func TestHubSetNoArgNonInteractive(t *testing.T) {
	err := hubSetCmd.RunE(hubSetCmd, nil)
	if err == nil {
		t.Fatal("hub set with no arg: want error, got nil")
	}
	if !strings.Contains(err.Error(), "<user@host>") {
		t.Errorf("hub set no-arg error = %q, want it to instruct passing <user@host>", err)
	}
}

// TestHubSetupNoArgNonInteractive: same for `duck hub setup`.
func TestHubSetupNoArgNonInteractive(t *testing.T) {
	err := hubSetupCmd.RunE(hubSetupCmd, nil)
	if err == nil {
		t.Fatal("hub setup with no arg: want error, got nil")
	}
	if !strings.Contains(err.Error(), "<user@host>") {
		t.Errorf("hub setup no-arg error = %q, want it to instruct passing <user@host>", err)
	}
}

// TestSetupCmdNonInteractive: `duck setup` is interactive-only; with no TTY it
// returns the "interactive" error and never reads stdin.
func TestSetupCmdNonInteractive(t *testing.T) {
	err := setupCmd.RunE(setupCmd, nil)
	if err == nil {
		t.Fatal("duck setup with no TTY: want error, got nil")
	}
	if !strings.Contains(err.Error(), "interactive") {
		t.Errorf("duck setup error = %q, want it to say it is interactive only", err)
	}
}

// TestBareDuckNoHubNonInteractive: bare `duck` with no hub configured returns
// the `run: duck setup` error without hanging. Hermetic temp HOME → empty
// config; no TTY → no stdin read.
func TestBareDuckNoHubNonInteractive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	err := ensureHubOrOfferSetup()
	if err == nil {
		t.Fatal("bare duck with no hub: want error, got nil")
	}
	if !strings.Contains(err.Error(), "duck hub setup") {
		t.Errorf("no-hub error = %q, want it to point at `duck hub setup`", err)
	}
}

// TestEnsureHubNoopWhenConfigured: when a hub IS configured, the bare-duck gate
// is a no-op (no error, no prompt).
func TestEnsureHubNoopWhenConfigured(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := config.Save(&config.Config{Hub: "duck", HubName: "hub.local"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := ensureHubOrOfferSetup(); err != nil {
		t.Fatalf("ensureHubOrOfferSetup with hub set: %v", err)
	}
}

// TestRootRegistersSetup: the top-level `duck setup` wizard is wired on rootCmd
// (distinct from `duck hub setup`).
func TestRootRegistersSetup(t *testing.T) {
	found := false
	for _, c := range rootCmd.Commands() {
		if c.Name() == "setup" {
			found = true
			break
		}
	}
	if !found {
		t.Error("rootCmd missing the top-level `setup` wizard command")
	}
}

// ── pure parsers (no TTY) ───────────────────────────────────────────────────

// TestReadLineTrimsAndEmpties pins readLine: it trims surrounding whitespace
// and returns "" for a blank/whitespace-only line (the empty-input case the
// callers treat as "no address given").
func TestReadLineTrimsAndEmpties(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"user@host\n", "user@host"},
		{"  user@host  \n", "user@host"},
		{"\tssh-alias\t\n", "ssh-alias"},
		{"\n", ""},
		{"   \n", ""},
		{"no-newline", "no-newline"},
		{"", ""},
	}
	for _, tc := range cases {
		got, err := readLine(strings.NewReader(tc.in))
		if err != nil {
			t.Errorf("readLine(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("readLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestParseYesNo pins the `[Y/n]` parser: default (empty) is YES, unlike the
// sync prompt's parseChoice whose default is No. y/yes/empty→true,
// n/no/anything-else→false; case- and whitespace-insensitive.
func TestParseYesNo(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", true},
		{"\n", true},
		{"  ", true},
		{"y", true},
		{"Y", true},
		{"yes", true},
		{"YES", true},
		{" y \n", true},
		{"n", false},
		{"no", false},
		{"NO", false},
		{"garbage", false},
	}
	for _, tc := range cases {
		if got := parseYesNo(tc.in); got != tc.want {
			t.Errorf("parseYesNo(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestSetupCmdHasNoArgs asserts the wizard rejects positional args (Args:
// NoArgs) — it prompts, it does not take <user@host>.
func TestSetupCmdHasNoArgs(t *testing.T) {
	if err := setupCmd.Args(setupCmd, []string{"user@host"}); err == nil {
		t.Error("duck setup accepted a positional arg; want NoArgs rejection")
	}
	if err := setupCmd.Args(setupCmd, nil); err != nil {
		t.Errorf("duck setup rejected 0 args: %v", err)
	}
}

// TestHubSetAcceptsAtMostOneArg pins the relaxed Args on hub set/setup: zero or
// one positional, never two.
func TestHubSetAcceptsAtMostOneArg(t *testing.T) {
	for _, c := range []struct {
		name string
		cmd  interface {
			ValidateArgs(args []string) error
		}
	}{
		{"hub set", hubSetCmd},
		{"hub setup", hubSetupCmd},
	} {
		if err := c.cmd.ValidateArgs([]string{"a", "b"}); err == nil {
			t.Errorf("%s accepted 2 args; want at-most-one rejection", c.name)
		}
		if err := c.cmd.ValidateArgs([]string{"a"}); err != nil {
			t.Errorf("%s rejected 1 arg: %v", c.name, err)
		}
		if err := c.cmd.ValidateArgs(nil); err != nil {
			t.Errorf("%s rejected 0 args: %v", c.name, err)
		}
	}
}

// TestBareDuckExecuteNoHubNoArgs drives the full root command via Execute with a
// captured buffer + temp HOME to prove the whole bare-`duck` path returns the
// `duck setup` error without hanging (no stdin read in the non-interactive
// harness).
func TestBareDuckExecuteNoHubNoArgs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	defer func() { flagContinue, flagResume = false, false }()
	flagContinue, flagResume = false, false

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{})
	defer rootCmd.SetArgs(nil)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("bare duck with no hub: want error, got nil")
	}
	if !strings.Contains(err.Error(), "duck hub setup") {
		t.Errorf("bare duck no-hub error = %q, want it to point at `duck hub setup`", err)
	}
}
