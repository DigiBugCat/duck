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
	// macOS hubs install everything via Homebrew.
	if !strings.Contains(tool, "brew install tsshd") {
		t.Errorf("tsshd must be installed under the brew branch:\n%s", tool)
	}
	// Linux hubs: apt installs tmux+rsync, but mutagen and tsshd have no apt
	// package, so the apt branch fetches each one's binary from its GitHub release
	// (tssh's --install-tsshd auto-deploy is NOT reliable, so we install tsshd
	// outright — otherwise the attach fails with "tsshd: command not found").
	if !strings.Contains(tool, "apt-get install -y tmux rsync") {
		t.Errorf("apt branch must install tmux+rsync via apt:\n%s", tool)
	}
	if !strings.Contains(tool, "mutagen-io/mutagen/releases") {
		t.Errorf("apt branch must fetch mutagen from its GitHub release (no apt package):\n%s", tool)
	}
	if !strings.Contains(tool, "trzsz/tsshd/releases") {
		t.Errorf("apt branch must fetch tsshd from its GitHub release (no apt package; tssh auto-deploy unreliable):\n%s", tool)
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

// TestTsshdPathProbe pins the hub tsshd path probe used after the toolchain
// install: it `command -v tsshd` so the detected absolute path can be stored and
// passed to tssh via --tsshd-path. It must tolerate absence (|| true) so an empty
// result on a Linux hub is not a hard failure.
func TestTsshdPathProbe(t *testing.T) {
	probe := tsshdPathProbeCmd()
	if !strings.Contains(probe, "tsshd") || !strings.Contains(probe, "command -v") {
		t.Errorf("tsshd path probe must `command -v tsshd`: %s", probe)
	}
	if !strings.Contains(probe, "|| true") {
		t.Errorf("tsshd path probe must tolerate absence with `|| true`: %s", probe)
	}
}

// TestOpenInterceptorInstall pins the hub-side open-interceptor install: the
// shim lands in ~/.duck/bin, open/xdg-open symlink to it, the runtime dir for
// per-session sockets is created, the rc block is idempotent (marker-guarded)
// and exports the env Claude Code and bare openers both need, and the embedded
// shim routes to a per-session unix socket (no shared port).
func TestOpenInterceptorInstall(t *testing.T) {
	if !strings.Contains(writeDuckOpenCmd(), "~/.duck/bin/duck-open") {
		t.Errorf("shim must be written to ~/.duck/bin/duck-open: %s", writeDuckOpenCmd())
	}
	inst := installOpenInterceptorCmd()
	for _, want := range []string{
		"mkdir -p ~/.duck/run", // per-session opener sockets live here
		"chmod +x ~/.duck/bin/duck-open",
		"ln -sf ~/.duck/bin/duck-open ~/.duck/bin/open",
		"ln -sf ~/.duck/bin/duck-open ~/.duck/bin/xdg-open",
		`export BROWSER="$HOME/.duck/bin/duck-open"`,
		`export PATH="$HOME/.duck/bin:$HOME/.local/bin:$PATH"`, // ~/.local/bin so claude/cass/mutagen resolve on a Linux hub
		openInterceptorMarker,                                  // idempotency guard present
		"grep -q",                                              // skip-if-present check makes it idempotent
	} {
		if !strings.Contains(inst, want) {
			t.Errorf("install script missing %q:\n%s", want, inst)
		}
	}
	// The fixed shared port is gone — the rc block must NOT reintroduce it.
	if strings.Contains(inst, "DUCK_OPEN_PORT") {
		t.Errorf("install script must not export the removed DUCK_OPEN_PORT:\n%s", inst)
	}
	// The rc block must target both zsh rc files so non-login (interactive) and
	// login panes both pick it up.
	if !strings.Contains(inst, "~/.zshrc") || !strings.Contains(inst, "~/.zprofile") {
		t.Errorf("install script must write both ~/.zshrc and ~/.zprofile:\n%s", inst)
	}
	// The shim asset must resolve the per-session socket and POST /open to it via
	// a unix socket — the whole point of the multiplexed design.
	for _, want := range []string{"DUCK_OPEN_SOCK", "--unix-socket", "/open"} {
		if !strings.Contains(assets.DuckOpen, want) {
			t.Errorf("duck-open shim must reference %q (per-session socket /open):\n%s", want, assets.DuckOpen)
		}
	}
	if strings.Contains(assets.DuckOpen, "DUCK_OPEN_PORT") {
		t.Errorf("duck-open shim must not reference the removed DUCK_OPEN_PORT:\n%s", assets.DuckOpen)
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
