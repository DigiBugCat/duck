// `duck claude-history`: cross-machine Claude conversation history. The corpus
// (~/.claude/projects) is already Mutagen-synced to the hub, but because Claude
// keys a session's on-disk directory AND its discoverability on the absolute cwd
// — which differs between a macOS laptop (/Users/me) and a Linux hub (/home/me)
// — a conversation started on one machine isn't resumable on the other until its
// transcripts are mapped onto the local path form and the local path is
// registered. `duck claude-history reconcile` does exactly that, non-destructively.
package command

import (
	"fmt"
	"io"
	"os"
	"strings"

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

var claudeHistoryDryRun bool

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

Idempotent: run it as often as you like.`,
	Args: cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		cfg, err := config.RequireHub()
		if err != nil {
			return err
		}
		out := c.OutOrStdout()

		localHome, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		homes := []string{localHome}

		// The hub's home is the other end of the near-universal case. Detect it
		// over SSH (cheap, one round-trip) so the user never has to configure it.
		if hubHome := detectHubHome(cfg.Hub); hubHome != "" {
			homes = append(homes, hubHome)
		} else {
			fmt.Fprintln(c.ErrOrStderr(), "warning: could not detect the hub's home dir; reconciling with local homes only")
		}

		// Fold in the anchor: it carries the whole fleet's home set so every laptop
		// reconciles consistently. Union what we know, push it back so other
		// machines learn this laptop's home too. Best-effort — a down anchor never
		// blocks the reconcile.
		if cfg.AnchorHost != "" {
			homes = mergeAnchorHomes(cfg.AnchorHost, homes, c.ErrOrStderr())
		}

		root, err := paths.Expand(claude.ProjectsRoot())
		if err != nil {
			return err
		}
		reg := claude.NewRegistry(localHome)
		res, err := claude.Reconcile(claude.ReconcileOptions{
			Root:      root,
			LocalHome: localHome,
			Homes:     homes,
			DryRun:    claudeHistoryDryRun,
			Register:  reg.Register,
		})
		if err != nil {
			return err
		}

		verb := "reconciled"
		if claudeHistoryDryRun {
			verb = "would reconcile"
		}
		fmt.Fprintf(out, "%s across homes: %s\n", verb, strings.Join(homes, ", "))
		fmt.Fprintf(out, "  %d project(s) mapped from another machine, %d transcript(s) %s in\n",
			res.Mapped, res.CopiedFiles, map[bool]string{true: "to copy", false: "copied"}[claudeHistoryDryRun])
		if len(res.Registered) > 0 {
			fmt.Fprintf(out, "  registered %d local path(s):\n", len(res.Registered))
			for _, p := range res.Registered {
				fmt.Fprintf(out, "    %s\n", p)
			}
		}
		if res.Mapped == 0 {
			fmt.Fprintln(out, "  nothing to do — everything already lines up on this machine")
		}
		return nil
	},
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
	// Union both directions: reconcile against what the anchor knows plus what we
	// just contributed.
	union, _ := anchor.State{Homes: homes}.AddHomes(next.Homes...)
	return union.Homes
}

func init() {
	claudeHistoryReconcileCmd.Flags().BoolVar(&claudeHistoryDryRun, "dry-run", false, "report what would change without copying or registering")
	claudeHistoryCmd.AddCommand(claudeHistoryReconcileCmd)
	rootCmd.AddCommand(claudeHistoryCmd)
}
