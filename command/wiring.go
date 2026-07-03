// wiring.go builds the M2–M4 collaborators (sshx client → session.Manager,
// names.Store, namer, flow.Flow, app.App) from the configured hub. It is the
// single place the commands assemble the layered services so each verb stays a
// thin dispatch. Nothing here contacts the hub until a method is actually
// called; constructing the wiring is pure.
package command

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/DigiBugCat/duck/internal/app"
	"github.com/DigiBugCat/duck/internal/config"
	"github.com/DigiBugCat/duck/internal/flow"
	"github.com/DigiBugCat/duck/internal/namer"
	"github.com/DigiBugCat/duck/internal/names"
	"github.com/DigiBugCat/duck/internal/session"
	"github.com/DigiBugCat/duck/internal/sshx"
)

// defaultCodexModel is the fallback codex model for laptop-side naming when the
// config sets none. Kept small (mini) so titles are fast and cheap (DESIGN §5).
const defaultCodexModel = "gpt-5-mini"

// wiring bundles the assembled services for one configured hub.
type wiring struct {
	addr     string
	client   *sshx.Client
	sessions *session.Manager
	names    *names.Store
	namer    namer.Namer
	flow     *flow.Flow
	app      *app.App
	// tsshAttach is the EFFECTIVE interactive-attach transport for this process:
	// true only when the config selects tssh AND a local `tssh` client exists
	// (else build() warns and falls back to ssh). The attach call sites pass it to
	// runAttachLoop so the reconnect loop knows tssh self-heals (no ssh-255 backoff).
	tsshAttach bool
}

// effectiveTsshAttach resolves the EFFECTIVE interactive-attach transport from
// the configured value:
//
//   - "auto" (the default): use tssh when a local `tssh` client is on PATH,
//     else ssh — silently, because auto-detect is the whole point: a client
//     opts in just by installing tssh, and the hub always supports it
//     (`duck hub setup` installs tsshd). No warning on absence.
//   - "tssh" (explicit force): use tssh, but when the client is absent return
//     (false, <warning>) so the caller warns once and falls back to ssh — an
//     explicit request that can't be honored is worth a word.
//   - "ssh" (explicit force): always ssh, even if tssh is installed.
//
// The hub side (tsshd) and UDP reachability surface later via tssh's own
// connect-time stderr. lookPath is exec.LookPath in production (injected so the
// branches are unit-testable).
func effectiveTsshAttach(transport string, lookPath func(string) (string, error)) (useTssh bool, warn string) {
	switch transport {
	case "ssh":
		return false, ""
	case "tssh":
		if _, err := lookPath("tssh"); err != nil {
			return false, "duck: attach-transport is tssh but the `tssh` client isn't on your PATH; using ssh. install it with: brew install trzsz-ssh"
		}
		return true, ""
	default: // "auto": prefer tssh when present, silently fall back to ssh.
		if _, err := lookPath("tssh"); err != nil {
			return false, ""
		}
		return true, ""
	}
}

// build assembles the wiring from the configured hub, warming the SSH
// control-master so the subsequent calls reuse it.
func build() (*wiring, error) {
	cfg, err := config.RequireHub()
	if err != nil {
		return nil, err
	}
	// Resolve the EFFECTIVE attach transport once: tssh only when the user opted
	// in AND the local `tssh` client is installed; otherwise fall back to ssh with
	// a one-line warning so the user is never locked out by a missing client. The
	// hub side (tsshd) and UDP reachability surface at connect time via tssh's own
	// stderr. Only the interactive attach is affected — see Client.Tssh.
	useTssh, warn := effectiveTsshAttach(cfg.Transport(), exec.LookPath)
	if warn != "" {
		fmt.Fprintln(os.Stderr, warn)
	}
	client := sshx.NewWithTransport(cfg.Hub, useTssh, cfg.HubTsshdPath)
	// If we're running ON the hub itself (this machine's hostname matches the one
	// captured at `duck hub setup`), skip ssh entirely and run every command/attach
	// locally. Lets `duck` on the host behave like duck everywhere else instead of
	// round-tripping ssh to itself (and so the host installs its own claude-hook.sh
	// rather than only the remote one). Best-effort: any hostname error leaves it
	// off and duck ssh's as before.
	if onHub(cfg.HubName) {
		client.Local = true
	}
	// Best-effort warm-up; failures surface on the first real call with a
	// clearer message.
	_ = client.WarmUp()
	// Best-effort: teach the hub this terminal's terminfo (e.g. xterm-ghostty)
	// so the interactive attach keeps full capabilities. Idempotent; on failure
	// AttachArgv's termGuard falls back to xterm-256color.
	_ = client.EnsureTerminfo(os.Getenv("TERM"))

	model := cfg.CodexModel
	if model == "" {
		model = defaultCodexModel
	}

	// Arm the open-interceptor for every interactive attach this process makes:
	// runAttachLoop wraps its attach in withOpenForwarding, which uses this hook.
	startOpenForwarding = newOpenForwarding(client)

	sess := session.NewManager(client, client)
	store := names.NewStore(client)
	nm := namer.NewCodexExec(client, codexLocal{}, model)
	fl := flow.New(cfg.Hub, sess, store, ttyPrompter{}, newTTYProgress())
	// Running ON the hub: skip the sync-awareness gate entirely (mirroring a folder
	// to the machine it already lives on is a no-op), so bare `duck` opens a local
	// session in cwd without prompting to sync. Same hostname match as client.Local.
	fl.SetLocal(client.Local)
	// Route flow's interactive attach through the command-layer reconnect loop so
	// a transport drop reconnects (capped backoff) and a ^c give-up returns
	// cleanLeave=false (the session is KEPT for `duck -c`). cleanLeave is true only
	// on a CleanLeave outcome — the sole outcome that permits flow's existing
	// fresh-untouched cleanup.
	fl.SetInteractiveAttach(func(tmuxName string, _ bool) (bool, error) {
		return runAttachLoop(sess, tmuxName, "", useTssh) == CleanLeave, nil
	})
	// Per-folder Claude history co-sync (ON by default): when on, a bare `duck`
	// that mirrors a folder also co-syncs that folder's ~/.claude/projects/<slug>.
	fl.SetClaudeHistory(cfg.SyncClaudeHistoryEnabled())
	// Cross-machine reconcile: after co-sync, best-effort map foreign-machine
	// transcripts onto this machine's path form (and the hub's) so `claude
	// --resume` finds hub/other-laptop sessions. Throttled + detached inside.
	fl.SetClaudeReconciler(newClaudeReconciler(cfg))
	ap := app.New(sess, store, nm)
	// Gate lazy auto-naming on the per-dir toggle (OFF by default) for non-picker
	// paths: Refresh only sends pane content to the model for dirs the user opted
	// in (DESIGN §5). The `duck --resume` picker OVERRIDES this with name-all (see
	// runResume), so resuming auto-titles every session it shows; this config gate
	// still governs any other Refresh caller.
	ap.SetAutoName(cfg.AutoNameEnabled)

	return &wiring{
		addr:       cfg.Hub,
		client:     client,
		sessions:   sess,
		names:      store,
		namer:      nm,
		flow:       fl,
		app:        ap,
		tsshAttach: useTssh,
	}, nil
}

// onHub reports whether duck is running on the hub machine itself, by comparing
// this host's name to the hub name captured at registration (config.HubName,
// the remote `hostname`). The comparison is case-insensitive and tolerant of
// FQDN/short-name and ".local" differences (so "macmini" matches "macmini.local"
// and "Macmini.lan"), since `hostname` output and os.Hostname() can disagree on
// the suffix. An empty hubName (older config, never captured) or any os.Hostname
// error returns false — duck then ssh's to the hub as before.
func onHub(hubName string) bool {
	if strings.TrimSpace(hubName) == "" {
		return false
	}
	local, err := os.Hostname()
	if err != nil {
		return false
	}
	short := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.TrimSuffix(s, ".local")
		if i := strings.IndexByte(s, '.'); i >= 0 {
			s = s[:i]
		}
		return s
	}
	a, b := short(local), short(hubName)
	return a != "" && a == b
}

// codexLocal is the production namer.LocalExec: it runs the laptop-side codex
// binary with the snapshot piped on stdin. codex runs LOCALLY (DESIGN §5) so
// names.json stays single-writer.
//
// Privacy: the snapshot piped on stdin is up to ~8KB of the remote terminal's
// pane content, which codex transmits to its configured model/provider. Outside
// the picker this is opt-in per folder via `duck config` (the AutoName toggle);
// `duck --resume` auto-names EVERY session it shows by default (runResume), so
// opening the picker sends each unnamed session's pane content to codex once
// (then frozen) — a deliberate widening of the opt-in for the interactive picker.
type codexLocal struct{}

func (codexLocal) Run(ctx context.Context, args []string, stdin io.Reader) (string, error) {
	cmd := exec.CommandContext(ctx, "codex", args...)
	cmd.Stdin = stdin
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
