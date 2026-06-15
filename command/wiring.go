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
	// moshAttach is the EFFECTIVE interactive-attach transport for this process:
	// true only when the config selects mosh AND a local `mosh` client exists
	// (else build() warns and falls back to ssh). The attach call sites pass it to
	// runAttachLoop so the reconnect loop knows mosh self-heals (no ssh-255 backoff).
	moshAttach bool
}

// effectiveMoshAttach resolves the EFFECTIVE interactive-attach transport: mosh
// only when the config selects it AND a local `mosh` client is on PATH;
// otherwise ssh. When mosh is selected but the client is absent it returns
// (false, <warning>) so the caller warns once and falls back to ssh — the user
// is never locked out by a missing client. The hub side (mosh-server) and UDP
// reachability surface later via mosh's own connect-time stderr. lookPath is
// exec.LookPath in production (injected so the three branches are unit-testable).
func effectiveMoshAttach(transport string, lookPath func(string) (string, error)) (useMosh bool, warn string) {
	if transport != "mosh" {
		return false, ""
	}
	if _, err := lookPath("mosh"); err != nil {
		return false, "duck: attach-transport is mosh but the `mosh` client isn't on your PATH; using ssh. install it with: brew install mosh"
	}
	return true, ""
}

// build assembles the wiring from the configured hub, warming the SSH
// control-master so the subsequent calls reuse it.
func build() (*wiring, error) {
	cfg, err := config.RequireHub()
	if err != nil {
		return nil, err
	}
	// Resolve the EFFECTIVE attach transport once: mosh only when the user opted
	// in AND the local `mosh` client is installed; otherwise fall back to ssh with
	// a one-line warning so the user is never locked out by a missing client. The
	// hub side (mosh-server) and UDP reachability surface at connect time via
	// mosh's own stderr. Only the interactive attach is affected — see Client.Mosh.
	useMosh, warn := effectiveMoshAttach(cfg.Transport(), exec.LookPath)
	if warn != "" {
		fmt.Fprintln(os.Stderr, warn)
	}
	client := sshx.NewWithTransport(cfg.Hub, useMosh)
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
	// Route flow's interactive attach through the command-layer reconnect loop so
	// a transport drop reconnects (capped backoff) and a ^c give-up returns
	// cleanLeave=false (the session is KEPT for `duck -c`). cleanLeave is true only
	// on a CleanLeave outcome — the sole outcome that permits flow's existing
	// fresh-untouched cleanup.
	fl.SetInteractiveAttach(func(tmuxName string, _ bool) (bool, error) {
		return runAttachLoop(sess, tmuxName, useMosh) == CleanLeave, nil
	})
	// Per-folder Claude history co-sync (OFF by default): when on, a bare `duck`
	// that mirrors a folder also co-syncs that folder's ~/.claude/projects/<slug>.
	fl.SetClaudeHistory(cfg.SyncClaudeHistory)
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
		moshAttach: useMosh,
	}, nil
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
