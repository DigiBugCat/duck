// Package command is duck's cobra command surface (renamed from flok's cmd/ to
// avoid clashing with the cmd/ binaries directory). Bare `duck` mirrors `claude`
// (DESIGN §1): it syncs cwd to the hub, creates a NEW remote tmux session in the
// synced dir, and attaches — composed idempotently by internal/flow. The
// continue/resume behaviours are FLAGS on bare `duck` (-c/--continue and
// --resume, mirroring claude); the verbs ls/rename/kill/clean and the
// `duck sync` group hang off it as subcommands.
//
// Bare `duck` and each verb wire to the layered services assembled in wiring.go
// (flow.Run, session.Manager, names.Store, app.App, the picker tui). The sync
// bundle commands and hub setup are the kept M1 layer.
package command

import (
	"errors"
	"fmt"
	"os"

	syncpkg "github.com/DigiBugCat/duck/command/sync"
	"github.com/DigiBugCat/duck/internal/actions"
	"github.com/DigiBugCat/duck/internal/config"
	"github.com/DigiBugCat/duck/internal/flow"
	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/spf13/cobra"
)

// Flag-backed mirrors of claude's session selectors. They live on bare `duck`
// (not as subcommands) so `duck -c` / `duck --resume [name]` match DESIGN §1.
// flagSync/flagNoSync override the sync-awareness decision for bare `duck`.
var (
	flagContinue bool
	flagResume   bool
	flagSync     bool
	flagNoSync   bool
)

var rootCmd = &cobra.Command{
	Use:   "duck",
	Short: "Make a remote hub feel local: sync cwd, open a remote tmux session, attach",
	Long: `duck is a thin laptop client that mirrors cwd to a canonical "hub" host
over SSH (Mutagen), opens a remote tmux session in the synced directory, and
drops you in. It mirrors claude's ergonomics but is tool-agnostic.

Running 'duck' with no arguments opens a remote session in cwd. It is
sync-aware: a small, already-synced, or remembered folder is mirrored and you
drop straight in; a large/home/root folder it hasn't seen prompts first
(default no) so it never starts a multi-GB mirror by accident.
  duck [folder]          target a folder instead of cwd (same sync-aware flow)
  duck --sync/--no-sync  force syncing (or not) for this folder, and remember it
  duck -c / --continue   reattach the most recent session for this dir
  duck --resume [name]   pick from existing sessions / attach a named one
  duck ls                list remote sessions without attaching

duck also bundles 'duck sync', a Mutagen-backed multi-machine path sync.`,
	SilenceUsage: true,
	// Args is flag-aware: --resume takes an optional session NAME; -c takes none;
	// bare `duck` takes an optional positional FOLDER (`duck ~/dev/foo`) that the
	// sync-aware flow targets in place of cwd. (The old claude-aware
	// "duck <session-name>" form is gone; this positional is a folder path.)
	Args: func(c *cobra.Command, args []string) error {
		if flagContinue {
			return cobra.NoArgs(c, args)
		}
		return cobra.MaximumNArgs(1)(c, args)
	},
	RunE: func(c *cobra.Command, args []string) error {
		switch {
		case flagContinue:
			// EnsureSyncedGated(cwd) → session.Recent(dir) → Attach.
			return runContinue()
		case flagResume:
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			// picker TUI (no name) / direct Attach(name).
			return runResume(name)
		}
		// First-run front door: if NO hub is configured yet, offer to set one up
		// (interactively) instead of failing in build()/RequireHub. This must
		// NEVER read stdin without a TTY — a non-interactive run gets the
		// instructive error immediately. Scoped to the bare-`duck` fall-through so
		// -c/--resume are unaffected.
		if err := ensureHubOrOfferSetup(); err != nil {
			return err
		}
		// Bare `duck` targets cwd by default, or the positional FOLDER if given
		// (expanded to an absolute path; ~, relative, and absolute all work).
		target, err := os.Getwd()
		if err != nil {
			return err
		}
		if len(args) == 1 {
			target, err = paths.Expand(args[0])
			if err != nil {
				return err
			}
		}
		w, err := build()
		if err != nil {
			return err
		}
		// decide-sync target → create a NEW session → attach. --sync/--no-sync
		// force the sync decision (bypassing the prompt). A hub-conflict
		// (actions.ErrHubNonEmpty) is resolved interactively rather than being a
		// dead-end error.
		return runWithHubConflict(w.flow, target, syncOverride())
	},
}

// overrideRunner is the seam runWithHubConflict drives: the bare-`duck`
// sync-aware composition keyed by a flow.Override. *flow.Flow satisfies it; the
// command test injects a stub so the conflict resolution is unit-testable
// without real ssh/mutagen.
type overrideRunner interface {
	RunWithOverride(cwd string, override flow.Override) error
}

// runWithHubConflict runs the bare-`duck` flow and, when it dead-ends on a
// hub-already-has-this-dir conflict (actions.ErrHubNonEmpty), turns that into an
// interactive choice instead of a hard error:
//
//   - interactive (TTY): print the merge prompt — for one user across machines
//     the merge keeps the NEWEST version of each file (a per-file rsync seed,
//     then mutagen on the now-identical sides; nothing is deleted). The DEFAULT
//     is YES because newest-wins is non-destructive of newer data: [y]/yes/blank
//     Enter re-runs the flow with OverrideSync (the newest-wins merge); [n]/no
//     re-runs with OverrideNoSyncOnce — a ONE-TIME no-sync that opens a session
//     in the hub's existing copy WITHOUT syncing and does NOT persist "never", so
//     duck asks again next time. Either way the session+attach then proceeds.
//   - non-interactive (no TTY): returns the ErrHubNonEmpty error UNCHANGED and
//     never reads stdin (invariant b: no prompt reads stdin when stdin is not a
//     TTY). `duck --sync` resolves the same conflict newest-wins WITHOUT a prompt
//     (the OverrideSync re-run forces the merge on the first pass).
//
// Re-running is safe: actions.AddPath returns ErrHubNonEmpty BEFORE it records
// the path or creates a session, so the forced re-run cleanly reconciles then
// re-attempts AddPath(force=true) then session+attach with no double-create.
func runWithHubConflict(r overrideRunner, target string, override flow.Override) error {
	err := r.RunWithOverride(target, override)
	var e actions.ErrHubNonEmpty
	if !errors.As(err, &e) || !isInteractive() {
		// nil, an unrelated error, or a conflict in a non-interactive context all
		// pass through unchanged — the non-interactive branch NEVER reads stdin.
		return err
	}
	fmt.Printf("Hub already has %s with files.\n"+
		"  [y] sync  — one user across machines: the NEWEST version of each file wins (nothing is deleted)\n"+
		"  [n] no sync — just open a remote session in the hub’s copy\n"+
		"Sync? [Y/n]: ", e.Path)
	line, lerr := readLine(os.Stdin)
	if lerr != nil {
		return err // surface the original conflict if we cannot read an answer.
	}
	return r.RunWithOverride(target, conflictOverride(line))
}

// conflictOverride maps a raw answer line at the hub-conflict prompt to the
// override to re-run the flow with. It uses parseYesNo (NOT parseChoice) so a
// blank Enter is YES, matching the printed [Y/n] default: empty/y/yes →
// OverrideSync (the newest-wins merge — reconcile then force-add); n/no/anything
// else → OverrideNoSyncOnce (open a session in the hub's existing copy WITHOUT
// syncing, and do NOT persist "never", so duck asks again next time). Pure, so
// the answer→override mapping is unit-tested without a TTY.
func conflictOverride(line string) flow.Override {
	if parseYesNo(line) {
		return flow.OverrideSync
	}
	return flow.OverrideNoSyncOnce
}

// ensureHubOrOfferSetup is the bare-`duck` first-run gate. If a hub is already
// configured it is a no-op. If none is configured and stdin is a TTY, it offers
// to run the setup wizard now (default YES); declining returns a friendly error
// pointing at `duck setup`. When NOT interactive it returns that friendly error
// immediately WITHOUT reading stdin — the load-bearing non-interactive safety
// property, mirroring the sync prompt's no-TTY behavior.
func ensureHubOrOfferSetup() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Hub != "" {
		return nil
	}
	if !isInteractive() {
		return fmt.Errorf("no hub configured. run: duck hub setup <user@host>")
	}
	fmt.Print("No hub configured. Set one up now? [Y/n] ")
	line, err := readLine(os.Stdin)
	if err != nil {
		return fmt.Errorf("no hub configured. run: duck setup")
	}
	if !parseYesNo(line) {
		return fmt.Errorf("no hub configured. run: duck setup")
	}
	addr, err := promptHubAddr()
	if err != nil {
		return err
	}
	if addr == "" {
		return fmt.Errorf("no hub address given")
	}
	return provisionHub(addr)
}

// syncOverride maps the --sync/--no-sync flags to a flow.Override. They are
// mutually exclusive (enforced in init), so at most one is set.
func syncOverride() flow.Override {
	switch {
	case flagSync:
		return flow.OverrideSync
	case flagNoSync:
		return flow.OverrideNoSync
	default:
		return flow.OverrideNone
	}
}

func init() {
	rootCmd.Flags().BoolVarP(&flagContinue, "continue", "c", false,
		"reattach the most recent session for this dir")
	rootCmd.Flags().BoolVar(&flagResume, "resume", false,
		"pick from existing sessions (no arg) or attach a named one")
	rootCmd.Flags().BoolVar(&flagSync, "sync", false,
		"force syncing cwd to the hub (remembered for this folder)")
	rootCmd.Flags().BoolVar(&flagNoSync, "no-sync", false,
		"open a session without syncing cwd (remembered for this folder)")
	rootCmd.MarkFlagsMutuallyExclusive("continue", "resume")
	rootCmd.MarkFlagsMutuallyExclusive("sync", "no-sync")

	rootCmd.AddCommand(syncpkg.Cmd)
	rootCmd.AddCommand(lsCmd)
	rootCmd.AddCommand(renameCmd)
	rootCmd.AddCommand(killCmd)
	rootCmd.AddCommand(cleanCmd)
	rootCmd.AddCommand(configCmd)
}

// Execute runs the root command. It is the single entrypoint called by
// cmd/duck/main.go.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
