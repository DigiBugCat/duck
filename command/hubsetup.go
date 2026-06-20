package command

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/DigiBugCat/duck/assets"
	"github.com/DigiBugCat/duck/internal/config"
	"github.com/DigiBugCat/duck/internal/hub"
	"github.com/DigiBugCat/duck/internal/openfwd"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

// hubsetup.go ports flok/cmd/hub.go (the ssh-key bootstrap for `set`) and adds
// the `setup` subcommand that provisions an unprovisioned hub: ssh keys, tmux +
// mutagen + TPM install, and the de-hooked ~/.tmux.conf. Naming runs laptop-side
// (codex via internal/namer), so there is no hub daemon to deploy — DESIGN §7
// drops the old duckd/SQLite path entirely. Per the milestone's hard safety
// rules NOTHING in here is executed against a live hub by the test suite; the
// command-string builders are pure and unit-tested, and `duck hub setup` is a
// manual step the human runs later.

var hubCmd = &cobra.Command{
	Use:   "hub",
	Short: "Configure, inspect, or provision the canonical hub host",
}

var hubSetCmd = &cobra.Command{
	Use:   "set <user@host>",
	Short: "Set the hub address and verify SSH connectivity",
	Long: `set verifies SSH connectivity to <user@host> and saves it as the hub.

If the SSH ping fails because of an auth error and you are running interactively,
duck offers to set up key-based auth using your existing key (or to generate one)
and runs ssh-copy-id on your behalf.

With no argument, duck prompts for the hub address when run interactively.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		addr, err := resolveAddrArg(args, "set")
		if err != nil {
			return err
		}
		if err := hub.ValidateAddr(addr); err != nil {
			return err
		}
		h := hub.New(addr)

		if err := h.Ping(); err != nil {
			if looksLikeAuthError(err) && isInteractive() {
				fmt.Printf("hub unreachable: %v\n\n", err)
				if setupErr := interactiveKeySetup(addr); setupErr != nil {
					return fmt.Errorf("key setup: %w", setupErr)
				}
				if err := h.Ping(); err != nil {
					return fmt.Errorf("still unreachable after key setup: %w", err)
				}
			} else {
				return fmt.Errorf("hub unreachable: %w", err)
			}
		}

		cfg, err := config.Load()
		if err != nil {
			return err
		}
		cfg.Hub = addr
		cfg.HubName, _ = h.Hostname() // best-effort; not fatal if it fails
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Printf("hub registered: %s\n", hub.DisplayName(cfg.Hub, cfg.HubName))
		return nil
	},
}

var hubShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the configured hub address",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if cfg.Hub == "" {
			fmt.Println("(no hub configured)")
			return nil
		}
		fmt.Println(hub.DisplayName(cfg.Hub, cfg.HubName))
		return nil
	},
}

var hubSetupCmd = &cobra.Command{
	Use:   "setup <user@host>",
	Short: "Provision an unprovisioned hub (keys, tmux/mutagen/TPM, tmux.conf)",
	Long: `setup brings a fresh hub up to duck's requirements:

  1. verify SSH connectivity (offering key setup if it fails interactively),
  2. install tmux + mutagen + TPM on the hub,
  3. write the de-hooked ~/.tmux.conf and run TPM plugin install so
     resurrect/continuum actually loads.

With no argument, duck prompts for the hub address when run interactively.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		addr, err := resolveAddrArg(args, "setup")
		if err != nil {
			return err
		}
		return provisionHub(addr)
	},
}

// setupCmd is duck's friendly interactive front door: with no hub configured a
// user can run `duck setup` and be walked through provisioning. It is
// INTERACTIVE-ONLY — without a TTY it returns an instructive error and never
// reads stdin (the load-bearing non-interactive safety property).
var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Interactively set up and provision your duck hub",
	Long: `setup is duck's interactive first-run wizard: it prompts for the hub
address (user@host or an ssh alias) and provisions it end to end — verifying
connectivity (offering key setup), installing tmux + mutagen + TPM, and writing
the de-hooked ~/.tmux.conf.

It is interactive-only. In a non-interactive context (no TTY) run
` + "`duck hub setup <user@host>`" + ` instead.`,
	Args: cobra.NoArgs,
	RunE: func(c *cobra.Command, args []string) error {
		if !isInteractive() {
			return fmt.Errorf("duck setup is interactive — run `duck hub setup <user@host>` instead")
		}
		addr, err := promptHubAddr()
		if err != nil {
			return err
		}
		if addr == "" {
			return fmt.Errorf("no hub address given")
		}
		if err := provisionHub(addr); err != nil {
			return err
		}
		cfg, _ := config.Load()
		fmt.Printf("✓ Hub ready: %s. Run `duck` in a project to start.\n",
			hub.DisplayName(cfg.Hub, cfg.HubName))
		return nil
	},
}

// provisionHub runs `hub setup` end to end for addr: ping (offering interactive
// key setup on an auth error), save the hub to config, then runHubSetup. It is
// the shared core behind `duck hub setup <addr>`, `duck setup`, and the bare-
// `duck` first-run offer. runHubSetup already saves the hub and prints the
// `hub provisioned:` confirmation, so this keeps the `hub setup <addr>` output
// identical to before — callers that want a different success line print their
// own after this returns.
func provisionHub(addr string) error {
	if err := hub.ValidateAddr(addr); err != nil {
		return err
	}
	h := hub.New(addr)

	if err := h.Ping(); err != nil {
		if looksLikeAuthError(err) && isInteractive() {
			fmt.Printf("hub unreachable: %v\n\n", err)
			if setupErr := interactiveKeySetup(addr); setupErr != nil {
				return fmt.Errorf("key setup: %w", setupErr)
			}
			if err := h.Ping(); err != nil {
				return fmt.Errorf("still unreachable after key setup: %w", err)
			}
		} else {
			return fmt.Errorf("hub unreachable: %w", err)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.Hub = addr
	cfg.HubName, _ = h.Hostname() // best-effort; not fatal if it fails
	if err := config.Save(cfg); err != nil {
		return err
	}

	return runHubSetup(addr, h)
}

// resolveAddrArg picks the hub address for the `hub set`/`hub setup` commands:
// the positional arg when given, else an interactive prompt (only when stdin is
// a TTY), else an instructive error. It NEVER reads stdin when not interactive
// (the load-bearing non-interactive safety property), so these commands never
// hang in a pipe/CI.
func resolveAddrArg(args []string, verb string) (string, error) {
	if len(args) == 1 {
		return args[0], nil
	}
	if !isInteractive() {
		return "", fmt.Errorf("no hub address given; pass <user@host>: duck hub %s <user@host>", verb)
	}
	addr, err := promptHubAddr()
	if err != nil {
		return "", err
	}
	if addr == "" {
		return "", fmt.Errorf("no hub address given")
	}
	return addr, nil
}

// runHubSetup performs the live provisioning steps. It is deliberately kept out
// of the unit tests (it shells out to the hub); the pure command-string
// builders it calls (below) carry the testable logic.
func runHubSetup(addr string, h *hub.Hub) error {
	// tsshdPath is the hub's detected tsshd location (step 2), persisted to config
	// at the end so the tssh attach can pass it via --tsshd-path. Empty stays empty
	// (Linux auto-deploy path).
	var tsshdPath string

	// 1. detect the hub's platform.
	unameOut, err := h.Run(remoteUnameCmd())
	if err != nil {
		return fmt.Errorf("probing hub platform: %w", err)
	}
	goos, goarch, err := parseUname(unameOut)
	if err != nil {
		return err
	}
	fmt.Printf("hub platform: %s/%s\n", goos, goarch)

	// 2. install the toolchain (tmux + mutagen + rsync + tsshd + TPM).
	fmt.Println("installing tmux + mutagen + tsshd + TPM ...")
	if _, err := h.Run(installToolchainScript()); err != nil {
		return fmt.Errorf("installing toolchain: %w", err)
	}
	if _, err := h.Run(installTPMScript()); err != nil {
		return fmt.Errorf("installing TPM: %w", err)
	}
	// tsshd backs the tssh interactive-attach transport (the default when the
	// local `tssh` client is installed). Detect its absolute path on the hub now
	// and store it so the attach can pass --tsshd-path (tssh launches tsshd over a
	// non-login ssh shell that may not have Homebrew on PATH). Best-effort: an
	// empty result is fine — on a Linux hub tssh auto-deploys tsshd itself, and the
	// attach simply omits the flag. A probe failure is non-fatal.
	if out, err := h.Run(tsshdPathProbeCmd()); err == nil {
		tsshdPath = strings.TrimSpace(out)
	}
	if tsshdPath == "" {
		fmt.Println("note: tsshd not found on the hub PATH; tssh will deploy it on first connect (Linux) or `brew install tsshd` on the hub.")
	}

	// 3. write the de-hooked tmux.conf and run TPM plugin install.
	fmt.Println("writing ~/.tmux.conf and installing tmux plugins ...")
	if _, err := h.RunInput(writeTmuxConfCmd(), strings.NewReader(assets.TmuxConf)); err != nil {
		return fmt.Errorf("writing ~/.tmux.conf: %w", err)
	}
	if _, err := h.Run(tpmInstallPluginsCmd()); err != nil {
		return fmt.Errorf("installing tmux plugins: %w", err)
	}

	// 4. install the open-interceptor: the duck-open shim, its open/xdg-open
	// symlinks, and the shell-rc env block ($BROWSER + PATH + DUCK_OPEN_PORT) so
	// URLs/files anything in a hub session tries to open get routed to the
	// attached laptop.
	fmt.Println("installing open-interceptor (duck-open) ...")
	if _, err := h.RunInput(writeDuckOpenCmd(), strings.NewReader(assets.DuckOpen)); err != nil {
		return fmt.Errorf("writing duck-open shim: %w", err)
	}
	if _, err := h.Run(installOpenInterceptorCmd()); err != nil {
		return fmt.Errorf("installing open-interceptor: %w", err)
	}

	// persist the hub for subsequent commands.
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.Hub = addr
	cfg.HubName, _ = h.Hostname()
	cfg.HubTsshdPath = tsshdPath
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Printf("hub provisioned: %s\n", hub.DisplayName(cfg.Hub, cfg.HubName))

	// Naming runs LAPTOP-side (DESIGN §5): the pane is captured on the hub over
	// ssh and piped into a local `codex exec`, which keeps names.json
	// single-writer. codex is therefore NOT needed on the hub — we verify it
	// here on the laptop and only WARN (never fail) if it is missing, since duck
	// degrades cleanly to the dir-derived name floor without it.
	warnIfNoLocalCodex()
	return nil
}

// warnIfNoLocalCodex prints a non-fatal warning when the laptop-local `codex`
// binary is absent. Naming degrades to the dir-derived floor without it, so
// this is advisory only — hub setup still succeeds.
func warnIfNoLocalCodex() {
	if _, err := exec.LookPath("codex"); err != nil {
		fmt.Println("note: `codex` was not found on your laptop's PATH.")
		fmt.Println("      duck names sessions LOCALLY via codex (codex is not needed on the hub);")
		fmt.Println("      without it, sessions fall back to their directory-derived names.")
		fmt.Println("      install codex to enable codex-powered naming.")
		return
	}
	fmt.Println("codex found locally: codex-powered session naming is available.")
}

// ── Pure command-string builders (unit-tested, never executed in tests) ─────

// remoteUnameCmd prints the hub's OS and machine on two lines for platform
// detection.
func remoteUnameCmd() string {
	return `uname -s && uname -m`
}

// parseUname maps `uname -s` / `uname -m` output to Go's GOOS/GOARCH so setup
// can report the hub's platform. It fails loudly on an unsupported target
// rather than proceeding against a host duck cannot support (risk #3).
func parseUname(out string) (goos, goarch string, err error) {
	lines := strings.Fields(strings.TrimSpace(out))
	if len(lines) < 2 {
		return "", "", fmt.Errorf("unexpected uname output: %q", out)
	}
	switch lines[0] {
	case "Linux":
		goos = "linux"
	case "Darwin":
		goos = "darwin"
	default:
		return "", "", fmt.Errorf("unsupported hub OS %q (want Linux or Darwin)", lines[0])
	}
	switch lines[1] {
	case "x86_64", "amd64":
		goarch = "amd64"
	case "aarch64", "arm64":
		goarch = "arm64"
	default:
		return "", "", fmt.Errorf("unsupported hub arch %q (want x86_64/amd64 or aarch64/arm64)", lines[1])
	}
	return goos, goarch, nil
}

// installToolchainScript is the remote shell snippet that installs tmux and
// mutagen. It prefers Homebrew (the hub is macOS in the verified setup) and
// falls back to apt-get on Debian/Ubuntu. Idempotent: re-running is a no-op if
// the tools are already present.
//
// tsshd (the UDP/QUIC attach server) is installed via Homebrew on macOS; on
// Linux it is NOT installed here because tssh deploys tsshd itself over ssh on
// first connect (`--install-tsshd` supports Linux targets), so apt only needs
// tmux + rsync.
func installToolchainScript() string {
	return strings.Join([]string{
		`set -e`,
		`if command -v brew >/dev/null 2>&1; then`,
		`  brew list tmux >/dev/null 2>&1 || brew install tmux`,
		`  brew list mutagen-io/mutagen/mutagen >/dev/null 2>&1 || brew install mutagen-io/mutagen/mutagen`,
		`  brew list rsync >/dev/null 2>&1 || brew install rsync`,
		`  brew list tsshd >/dev/null 2>&1 || brew install tsshd`,
		`elif command -v apt-get >/dev/null 2>&1; then`,
		`  sudo apt-get update && sudo apt-get install -y tmux rsync`,
		`  command -v mutagen >/dev/null 2>&1 || echo "install mutagen manually: https://mutagen.io/documentation/introduction/installation"`,
		`else`,
		`  echo "no supported package manager (brew/apt-get) found on hub" >&2; exit 1`,
		`fi`,
	}, "\n")
}

// tsshdPathProbeCmd prints the absolute path to tsshd on the hub's login-shell
// PATH, or nothing when it is absent. Run via hub.Run (login-wrapped) so the
// Homebrew bin dir where `brew install tsshd` lands the binary is on PATH. The
// detected path is stored in config and passed to tssh via --tsshd-path so the
// attach finds tsshd even off the hub's non-login PATH. An empty result (e.g. a
// Linux hub where tssh auto-installs tsshd) is fine — the attach omits the flag.
func tsshdPathProbeCmd() string {
	return `command -v tsshd 2>/dev/null || true`
}

// installTPMScript clones the Tmux Plugin Manager if it is not already present.
// Idempotent.
func installTPMScript() string {
	return `[ -d ~/.tmux/plugins/tpm ] || git clone https://github.com/tmux-plugins/tpm ~/.tmux/plugins/tpm`
}

// tpmInstallPluginsCmd runs TPM's non-interactive plugin installer so that
// resurrect/continuum are actually loaded (gap#4).
func tpmInstallPluginsCmd() string {
	return `~/.tmux/plugins/tpm/bin/install_plugins`
}

// writeTmuxConfCmd writes the tmux.conf streamed over stdin to ~/.tmux.conf.
// The content is fed via stdin (not the command text) so it never needs shell
// escaping.
func writeTmuxConfCmd() string {
	// Back up any existing ~/.tmux.conf (e.g. a pre-duck config) before
	// overwriting it, so the user can always restore their previous setup. The
	// ~ is left unquoted so the shell expands it to the hub's $HOME.
	return `[ -f ~/.tmux.conf ] && cp ~/.tmux.conf ~/.tmux.conf.pre-duck-$(date +%Y%m%d%H%M%S).bak; cat > ~/.tmux.conf`
}

// writeDuckOpenCmd writes the duck-open shim (streamed over stdin, so its own
// quoting/`$` never needs escaping) to ~/.duck/bin/duck-open.
func writeDuckOpenCmd() string {
	return `mkdir -p ~/.duck/bin && cat > ~/.duck/bin/duck-open`
}

// openInterceptorMarker brackets the duck-managed block in the hub's shell rc
// files so installOpenInterceptorCmd can stay idempotent (skip if already
// present) and a user can find/remove it.
const openInterceptorMarker = "duck open-interceptor"

// installOpenInterceptorCmd makes the shim executable, symlinks the platform
// opener names (open, xdg-open) to it so a bare `open foo` / `xdg-open url` from
// a hub shell hits duck even without $BROWSER, and appends the env block to the
// hub's zsh rc files (idempotently, guarded by openInterceptorMarker). The
// block prepends ~/.duck/bin to PATH (so the symlinks win over the system
// opener), points $BROWSER at the shim (so Claude Code's own opener and
// $BROWSER-respecting tools route through it), and sets DUCK_OPEN_PORT. The
// heredoc body is single-quoted ('EOF') so $HOME/$PATH stay LITERAL in the rc
// file and expand at the hub shell's startup, not now.
func installOpenInterceptorCmd() string {
	return strings.Join([]string{
		`set -e`,
		`chmod +x ~/.duck/bin/duck-open`,
		`ln -sf ~/.duck/bin/duck-open ~/.duck/bin/open`,
		`ln -sf ~/.duck/bin/duck-open ~/.duck/bin/xdg-open`,
		`for rc in ~/.zshrc ~/.zprofile; do`,
		`  if ! grep -q '` + openInterceptorMarker + `' "$rc" 2>/dev/null; then`,
		`    cat >> "$rc" <<'EOF'`,
		``,
		`# >>> ` + openInterceptorMarker + ` >>>`,
		`export PATH="$HOME/.duck/bin:$PATH"`,
		`export BROWSER="$HOME/.duck/bin/duck-open"`,
		fmt.Sprintf(`export DUCK_OPEN_PORT=%d`, openfwd.HubPort),
		`# <<< ` + openInterceptorMarker + ` <<<`,
		`EOF`,
		`  fi`,
		`done`,
	}, "\n")
}

// ── ssh-key bootstrap (ported verbatim from flok/cmd/hub.go) ────────────────

// looksLikeAuthError reports whether an SSH failure was likely due to missing
// or wrong credentials, as opposed to a network/host problem. Used to decide
// whether offering key setup makes sense.
func looksLikeAuthError(err error) bool {
	msg := err.Error()
	for _, marker := range []string{
		"Permission denied",
		"publickey",
		"password",
		"authentication failed",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// isInteractive reports whether stdin is connected to a real terminal. It is
// the single gate behind EVERY stdin prompt (host address, the Y/n offer, the
// key-setup menu) — when it is false NO path reads stdin, so duck never blocks
// in a pipe/CI/redirected-stdin context (the load-bearing safety property).
//
// It uses isatty (the SAME check as the sync prompt in prompt.go) rather than
// os.ModeCharDevice: a char device like /dev/null is NOT a terminal, yet
// passes the ModeCharDevice test — so the weaker check would prompt (and on an
// open-but-empty pipe, BLOCK) under `duck </dev/null`. isatty distinguishes a
// real TTY from such char devices, which is exactly what the safety property
// requires.
func isInteractive() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())
}

// interactiveKeySetup walks the user through picking or generating an SSH key
// and copying it to the remote with ssh-copy-id.
func interactiveKeySetup(addr string) error {
	if _, err := exec.LookPath("ssh-copy-id"); err != nil {
		return fmt.Errorf("ssh-copy-id not on PATH; install it or set up keys manually")
	}

	candidates, err := findPublicKeys()
	if err != nil {
		return err
	}

	fmt.Println("Set up SSH key auth?")
	for i, k := range candidates {
		fmt.Printf("  [%d] Use existing key: %s\n", i+1, k)
	}
	genIdx := len(candidates) + 1
	cancelIdx := genIdx + 1
	fmt.Printf("  [%d] Generate a new ed25519 key\n", genIdx)
	fmt.Printf("  [%d] Cancel\n", cancelIdx)

	choice, err := readChoice(cancelIdx)
	if err != nil {
		return err
	}
	if choice == cancelIdx {
		return fmt.Errorf("cancelled by user")
	}

	var pubKey string
	if choice == genIdx {
		pubKey, err = generateEd25519Key()
		if err != nil {
			return err
		}
	} else {
		pubKey = candidates[choice-1]
	}

	fmt.Printf("\nCopying %s to %s\n", pubKey, addr)
	fmt.Println("(you'll be prompted for the remote password once)")
	cmd := exec.Command("ssh-copy-id", "-i", pubKey, addr)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh-copy-id failed: %w", err)
	}
	return nil
}

// findPublicKeys returns the standard SSH public key paths that exist for the
// current user, in the order ssh prefers them (ed25519 first).
func findPublicKeys() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	var found []string
	for _, name := range []string{"id_ed25519.pub", "id_ecdsa.pub", "id_rsa.pub"} {
		p := filepath.Join(home, ".ssh", name)
		if _, err := os.Stat(p); err == nil {
			found = append(found, p)
		}
	}
	return found, nil
}

// generateEd25519Key creates ~/.ssh/id_ed25519 if it does not already exist
// and returns the path to its public key.
func generateEd25519Key() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	priv := filepath.Join(home, ".ssh", "id_ed25519")
	pub := priv + ".pub"
	if _, err := os.Stat(priv); err == nil {
		return "", fmt.Errorf("%s already exists; pick it from the menu instead of generating", priv)
	}
	if err := os.MkdirAll(filepath.Dir(priv), 0o700); err != nil {
		return "", err
	}
	user := os.Getenv("USER")
	cmd := exec.Command("ssh-keygen",
		"-t", "ed25519",
		"-f", priv,
		"-N", "",
		"-C", fmt.Sprintf("%s@duck", user),
	)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return pub, nil
}

func readChoice(max int) (int, error) {
	fmt.Print("> ")
	line, err := readLine(os.Stdin)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > max {
		return 0, fmt.Errorf("invalid choice %q (expected 1-%d)", line, max)
	}
	return n, nil
}

// readLine reads a single line from r and returns it trimmed of surrounding
// whitespace. An EOF after some bytes is not an error (the line is returned);
// it is the shared line-reader behind the host prompt, the Y/n offer, and the
// key-setup choice. Taking an io.Reader keeps it unit-testable via
// strings.NewReader without a TTY.
func readLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	line = strings.TrimSpace(line)
	// EOF (e.g. an empty/closed stream) is not an error: the trimmed line so far
	// is returned, and callers treat a blank result uniformly as "no input".
	if err != nil && err != io.EOF {
		return "", err
	}
	return line, nil
}

// promptHubAddr prints the hub-address prompt and reads a trimmed line of
// stdin. It is ONLY ever called behind an isInteractive() check — callers must
// guard it so a non-interactive run never blocks on stdin.
func promptHubAddr() (string, error) {
	fmt.Print("Hub address (user@host or ssh alias): ")
	return readLine(os.Stdin)
}

// parseYesNo maps a raw answer line to a yes/no decision for a `[Y/n]` prompt
// where the default (empty input) is YES: empty/y/yes → true, n/no/anything
// else → false. Case- and whitespace-insensitive. Pure so it is unit-tested
// without a TTY. NOTE the default differs from parseChoice (sync prompt), whose
// default is No.
func parseYesNo(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes":
		return true
	default:
		return false
	}
}

func init() {
	hubCmd.AddCommand(hubSetCmd, hubShowCmd, hubSetupCmd)
	rootCmd.AddCommand(hubCmd)
	rootCmd.AddCommand(setupCmd)
}
