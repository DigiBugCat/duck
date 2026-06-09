package command

import (
	"strings"
	"testing"

	"github.com/DigiBugCat/duck/assets"
)

// ── root wiring ─────────────────────────────────────────────────────────────

func TestRootRegistersSyncAndHub(t *testing.T) {
	names := map[string]bool{}
	for _, c := range rootCmd.Commands() {
		names[c.Name()] = true
	}
	if !names["sync"] {
		t.Error("rootCmd missing the `sync` parent command")
	}
	if !names["hub"] {
		t.Error("rootCmd missing the `hub` command")
	}
}

func TestRootTakesOptionalFolderArg(t *testing.T) {
	// Bare `duck` operates on cwd by default but accepts ONE optional positional
	// FOLDER (`duck ~/dev/foo`) the sync-aware flow targets in place of cwd. The
	// dropped claude-aware "duck <session-name>" form was a different semantic.
	// A second positional is still rejected.
	defer func() { flagContinue, flagResume = false, false }()
	flagContinue, flagResume = false, false

	if err := rootCmd.Args(rootCmd, []string{"a", "b"}); err == nil {
		t.Error("rootCmd accepted 2 args; want at-most-one rejection")
	}
	if err := rootCmd.Args(rootCmd, []string{"a"}); err != nil {
		t.Errorf("rootCmd rejected 1 folder arg: %v", err)
	}
	if err := rootCmd.Args(rootCmd, nil); err != nil {
		t.Errorf("rootCmd rejected 0 args: %v", err)
	}

	// -c takes no positional args.
	flagContinue = true
	if err := rootCmd.Args(rootCmd, []string{"a"}); err == nil {
		t.Error("`duck -c a` should be rejected (-c takes no args)")
	}

	// --resume allows one optional folder/name arg (`duck --resume foo`) but
	// still rejects a second positional. -c is off here.
	flagContinue, flagResume = false, true
	if err := rootCmd.Args(rootCmd, []string{"foo"}); err != nil {
		t.Errorf("`duck --resume foo` should be allowed (one folder/name arg): %v", err)
	}
	if err := rootCmd.Args(rootCmd, []string{"a", "b"}); err == nil {
		t.Error("`duck --resume a b` should be rejected (at most one arg)")
	}
}

func TestHubHasSetShowSetup(t *testing.T) {
	var hubC = hubCmd
	names := map[string]bool{}
	for _, c := range hubC.Commands() {
		names[c.Name()] = true
	}
	for _, want := range []string{"set", "show", "setup"} {
		if !names[want] {
			t.Errorf("hub command missing %q subcommand", want)
		}
	}
}

// ── pure command-string builders (no exec ever runs) ────────────────────────

func TestParseUname(t *testing.T) {
	ok := []struct {
		out, goos, goarch string
	}{
		{"Darwin\narm64\n", "darwin", "arm64"},
		{"Linux\nx86_64\n", "linux", "amd64"},
		{"Linux\naarch64\n", "linux", "arm64"},
		{"Darwin amd64", "darwin", "amd64"},
	}
	for _, c := range ok {
		goos, goarch, err := parseUname(c.out)
		if err != nil {
			t.Errorf("parseUname(%q) error: %v", c.out, err)
			continue
		}
		if goos != c.goos || goarch != c.goarch {
			t.Errorf("parseUname(%q) = %s/%s, want %s/%s", c.out, goos, goarch, c.goos, c.goarch)
		}
	}
	// Unsupported targets fail loudly (risk #3).
	bad := []string{"Windows\nx86_64", "Linux\nmips", "garbage", ""}
	for _, c := range bad {
		if _, _, err := parseUname(c); err == nil {
			t.Errorf("parseUname(%q) = nil error, want failure on unsupported target", c)
		}
	}
}

func TestInstallScriptsAndTmuxConf(t *testing.T) {
	tool := installToolchainScript()
	if !strings.Contains(tool, "tmux") || !strings.Contains(tool, "mutagen") || !strings.Contains(tool, "rsync") {
		t.Errorf("toolchain script must install tmux, mutagen, and rsync:\n%s", tool)
	}
	tpm := installTPMScript()
	if !strings.Contains(tpm, "tmux-plugins/tpm") || !strings.Contains(tpm, "~/.tmux/plugins/tpm") {
		t.Errorf("TPM script must clone tpm into ~/.tmux/plugins/tpm:\n%s", tpm)
	}
	if !strings.Contains(tpmInstallPluginsCmd(), "install_plugins") {
		t.Errorf("must run TPM install_plugins so continuum loads (gap#4): %s", tpmInstallPluginsCmd())
	}
	if !strings.Contains(writeTmuxConfCmd(), "~/.tmux.conf") {
		t.Errorf("tmux.conf must be written to ~/.tmux.conf: %s", writeTmuxConfCmd())
	}
}

// TestEmbeddedTmuxConfDropsHooks asserts the shipped conf is the flok-era one
// MINUS the client-detached rename hook, `bind R`, and `bind-key T` (d1),
// while KEEPING resurrect/continuum.
func TestEmbeddedTmuxConfDropsHooks(t *testing.T) {
	conf := assets.TmuxConf
	for _, banned := range []string{
		"client-detached",
		"bind-key R",
		"bind-key T",
		"tmux-claude-name",
		"duck-pick",
	} {
		if strings.Contains(conf, banned) {
			t.Errorf("shipped tmux.conf must NOT contain %q (d1)", banned)
		}
	}
	for _, kept := range []string{
		"tmux-plugins/tmux-resurrect",
		"tmux-plugins/tmux-continuum",
		"@continuum-restore 'on'",
		"run '~/.tmux/plugins/tpm/tpm'",
	} {
		if !strings.Contains(conf, kept) {
			t.Errorf("shipped tmux.conf must KEEP %q", kept)
		}
	}
}
