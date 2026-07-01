// `duck claude-history`: cross-machine Claude conversation history. The corpus
// (~/.claude/projects) is already Mutagen-synced to the hub, but because Claude
// keys a session's on-disk directory AND its discoverability on the absolute cwd
// — which differs between a macOS laptop (/Users/me) and a Linux hub (/home/me)
// — a conversation started on one machine isn't resumable on the other until its
// transcripts are mapped onto the local path form and the local path is
// registered. `duck claude-history reconcile` does exactly that, non-destructively,
// on this machine AND (best-effort, over SSH) on the hub so it works both ways.
package command

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/DigiBugCat/duck/internal/anchor"
	"github.com/DigiBugCat/duck/internal/claude"
	"github.com/DigiBugCat/duck/internal/config"
	"github.com/DigiBugCat/duck/internal/hub"
	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/spf13/cobra"
)

var claudeHistoryCmd = &cobra.Command{
	Use:   "claude-history",
	Short: "Cross-machine Claude conversation history (make hub/laptop sessions resumable everywhere)",
}

var (
	claudeHistoryDryRun bool
	claudeHistoryLocal  bool
	claudeHistoryHomes  []string
)

var claudeHistoryReconcileCmd = &cobra.Command{
	Use:   "reconcile",
	Short: "Make sessions from other machines resumable here (non-destructive)",
	Long: `Map Claude transcripts that were recorded on another machine (the hub, or
another laptop) onto THIS machine's path form, and register them so
'claude --resume' finds them.

The corpus is synced verbatim, so a session started on the Linux hub lives under
a '-home-you-...' slug with cwd /home/you/...; on your Mac (/Users/you) Claude
looks under '-Users-you-...'. This copies each foreign transcript into the local
slug directory (only if absent — it never overwrites or deletes) and adds a
~/.claude.json entry for the local path (never touching existing entries).

By default it reconciles this machine and then, best-effort over SSH, the hub too
(so laptop-started sessions become resumable on the hub). Idempotent.

Flags --local and --homes are for the hub-side pass duck runs over SSH; you
rarely type them by hand.`,
	Args: cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		return runClaudeReconcile(c.OutOrStdout(), c.ErrOrStderr(), reconcileParams{
			dryRun:     claudeHistoryDryRun,
			local:      claudeHistoryLocal,
			extraHomes: claudeHistoryHomes,
		})
	},
}

// reconcileParams are the inputs to a reconcile run, threaded explicitly so the
// explicit command, the hidden background re-exec, and the --local hub-side pass
// all share one implementation without leaning on package-global flag state.
type reconcileParams struct {
	dryRun     bool
	local      bool     // hub-side / self-contained: no hub config, no SSH out
	extraHomes []string // additional fleet home dirs (--homes), joined into the set
}

// runClaudeReconcile is the single implementation behind `duck claude-history
// reconcile`, the detached background re-exec, and the --local hub-side pass.
func runClaudeReconcile(out, errw io.Writer, p reconcileParams) error {
	localHome, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	if p.local {
		// Self-contained mode (the hub runs this over SSH). No hub config, no SSH
		// out — using RequireHub/detectHubHome here would be wrong (the hub has no
		// `duck hub set`) or circular (the hub SSHing back to a laptop).
		homes := dedupHomes(append([]string{localHome}, p.extraHomes...))
		return reconcileHere(out, localHome, homes, p.dryRun)
	}

	cfg, err := config.RequireHub()
	if err != nil {
		return err
	}
	homes := []string{localHome}
	if hubHome := detectedHubHome(cfg); hubHome != "" {
		homes = append(homes, hubHome)
	} else {
		fmt.Fprintln(errw, "warning: could not detect the hub's home dir; reconciling with local homes only")
	}
	if cfg.AnchorHost != "" {
		homes = mergeAnchorHomes(cfg.AnchorHost, homes, errw)
	}
	homes = dedupHomes(homes)

	// This machine first.
	if err := reconcileHere(out, localHome, homes, p.dryRun); err != nil {
		return err
	}
	// Then the hub, best-effort (point 2): forward the SAME home set so the hub
	// maps laptop-origin sessions into its own path form. A down/absent hub-side
	// duck is a warning, never a failure.
	reconcileOnHub(cfg, homes, p.dryRun, out, errw)
	// Update the shared throttle stamp (unless this was a dry run) so a bare `duck`
	// moments later doesn't fire a redundant background reconcile right on top of
	// this one — the explicit command and the auto-wired path share one throttle.
	if !p.dryRun {
		touchClaudeReconcileStamp(time.Now())
	}
	return nil
}

// reconcileHere runs a reconcile pass against THIS machine's ~/.claude/projects
// and prints a summary.
func reconcileHere(out io.Writer, localHome string, homes []string, dryRun bool) error {
	root, err := paths.Expand(claude.ProjectsRoot())
	if err != nil {
		return err
	}
	reg := claude.NewRegistry(localHome)
	res, err := claude.Reconcile(claude.ReconcileOptions{
		Root:      root,
		LocalHome: localHome,
		Homes:     homes,
		DryRun:    dryRun,
		Register:  reg.Register,
	})
	if err != nil {
		return err
	}
	verb, tense := "reconciled", "copied"
	if dryRun {
		verb, tense = "would reconcile", "to copy"
	}
	fmt.Fprintf(out, "%s here (%s) across homes: %s\n", verb, localHome, strings.Join(homes, ", "))
	fmt.Fprintf(out, "  %d project(s) mapped from another machine, %d transcript(s) %s in\n", res.Mapped, res.CopiedFiles, tense)
	if len(res.Registered) > 0 {
		fmt.Fprintf(out, "  registered %d local path(s)\n", len(res.Registered))
	}
	if res.Mapped == 0 {
		fmt.Fprintln(out, "  nothing to do — everything already lines up here")
	}
	return nil
}

// reconcileOnHub runs the reconcile natively on the hub over SSH, forwarding the
// fleet home set so the hub maps laptop-origin sessions into its own form. It
// needs a `duck` on the hub's PATH (installed by `duck hub setup`); a missing
// binary or SSH error is a warning, never fatal.
func reconcileOnHub(cfg *config.Config, homes []string, dryRun bool, out, errw io.Writer) {
	remote := "duck claude-history reconcile --local --homes " + paths.Quote(strings.Join(homes, ","))
	if dryRun {
		remote += " --dry-run"
	}
	res, err := hub.New(cfg.Hub).Run(remote)
	if err != nil {
		fmt.Fprintf(errw, "warning: could not reconcile on the hub (%v); is duck installed there? (re-run `duck hub setup`)\n", err)
		return
	}
	if s := strings.TrimSpace(res); s != "" {
		fmt.Fprintln(out, "on the hub:")
		for _, line := range strings.Split(s, "\n") {
			fmt.Fprintf(out, "  %s\n", line)
		}
	}
}

// detectedHubHome returns the hub's absolute home, preferring the cfg.HubHome
// cache and falling back to a one-time SSH detect that it writes back so every
// later run is SSH-free.
func detectedHubHome(cfg *config.Config) string {
	if cfg.HubHome != "" {
		return cfg.HubHome
	}
	home := detectHubHome(cfg.Hub)
	if home != "" {
		cfg.HubHome = home
		_ = config.Save(cfg) // best-effort write-through cache
	}
	return home
}

// detectHubHome returns the hub's absolute $HOME over SSH, or "" on any failure.
func detectHubHome(addr string) string {
	out, err := hub.New(addr).Run(`printf %s "$HOME"`)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// mergeAnchorHomes unions the anchor's fleet home set with homes, pushes any new
// ones back (so the fleet learns this machine's home), and returns the union.
// Best-effort: an unreachable/broken anchor logs a warning and returns homes.
func mergeAnchorHomes(anchorHost string, homes []string, errw io.Writer) []string {
	store := anchor.NewStore(hub.New(anchorHost))
	st, err := store.Load()
	if err != nil {
		fmt.Fprintf(errw, "warning: could not read the anchor's home set: %v\n", err)
		return homes
	}
	next, changed := st.AddHomes(homes...)
	if changed {
		if err := store.Save(next); err != nil {
			fmt.Fprintf(errw, "warning: could not update the anchor's home set: %v\n", err)
		}
	}
	union, _ := anchor.State{Homes: homes}.AddHomes(next.Homes...)
	return union.Homes
}

// dedupHomes returns homes with blanks dropped and duplicates removed, order
// preserved.
func dedupHomes(homes []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range homes {
		h = strings.TrimSpace(h)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}

func init() {
	f := claudeHistoryReconcileCmd.Flags()
	f.BoolVar(&claudeHistoryDryRun, "dry-run", false, "report what would change without copying or registering")
	f.BoolVar(&claudeHistoryLocal, "local", false, "reconcile only this machine (no hub config/SSH); used by the hub-side pass")
	f.StringSliceVar(&claudeHistoryHomes, "homes", nil, "extra fleet home dirs to map from (comma-separated); used by the hub-side pass")
	claudeHistoryCmd.AddCommand(claudeHistoryReconcileCmd)
	rootCmd.AddCommand(claudeHistoryCmd)
}
