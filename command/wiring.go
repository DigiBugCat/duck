// wiring.go builds the M2–M4 collaborators (sshx client → session.Manager,
// names.Store, namer, flow.Flow, app.App) from the configured hub. It is the
// single place the commands assemble the layered services so each verb stays a
// thin dispatch. Nothing here contacts the hub until a method is actually
// called; constructing the wiring is pure.
package command

import (
	"context"
	"io"
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
}

// build assembles the wiring from the configured hub, warming the SSH
// control-master so the subsequent calls reuse it.
func build() (*wiring, error) {
	cfg, err := config.RequireHub()
	if err != nil {
		return nil, err
	}
	client := sshx.New(cfg.Hub)
	// Best-effort warm-up; failures surface on the first real call with a
	// clearer message.
	_ = client.WarmUp()

	model := cfg.CodexModel
	if model == "" {
		model = defaultCodexModel
	}

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
		return runAttachLoop(sess, tmuxName) == CleanLeave, nil
	})
	// Per-folder Claude history co-sync (OFF by default): when on, a bare `duck`
	// that mirrors a folder also co-syncs that folder's ~/.claude/projects/<slug>.
	fl.SetClaudeHistory(cfg.SyncClaudeHistory)
	ap := app.New(sess, store, nm)
	// Gate lazy auto-naming on the per-dir toggle (OFF by default): Refresh only
	// sends pane content to the model for dirs the user opted in (DESIGN §5).
	ap.SetAutoName(cfg.AutoNameEnabled)

	return &wiring{
		addr:     cfg.Hub,
		client:   client,
		sessions: sess,
		names:    store,
		namer:    nm,
		flow:     fl,
		app:      ap,
	}, nil
}

// codexLocal is the production namer.LocalExec: it runs the laptop-side codex
// binary with the snapshot piped on stdin. codex runs LOCALLY (DESIGN §5) so
// names.json stays single-writer.
//
// Privacy: the snapshot piped on stdin is up to ~8KB of the remote terminal's
// pane content, which codex transmits to its configured model/provider. This is
// why auto-naming is opt-in per folder via `duck config` (the AutoName toggle).
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
