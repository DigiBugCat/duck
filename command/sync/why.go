// why.go implements `duck sync why [dir]` — the sync-gate debugger. It answers
// "why did (or didn't) duck consider this folder synced?" by printing every
// input the bare-`duck` decision consumes: the ownership mode, the sessions
// fetched (hub ledger or local daemon), the per-session coverage test against
// the folder, and the remembered folder policy. Diagnostic-only: it mutates
// nothing.
package sync

import (
	"fmt"
	"os"
	"strings"

	"github.com/DigiBugCat/duck/internal/actions"
	"github.com/DigiBugCat/duck/internal/config"
	"github.com/DigiBugCat/duck/internal/folder"
	"github.com/DigiBugCat/duck/internal/mutagen"
	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/spf13/cobra"
)

var whyCmd = &cobra.Command{
	Use:   "why [dir]",
	Short: "Explain the sync-gate decision for a directory (default: cwd)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		dir := ""
		if len(args) == 1 {
			dir = args[0]
		} else {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			dir = cwd
		}
		tilde := normalizeToTilde(dir)
		local, err := paths.Expand(tilde)
		if err != nil {
			return err
		}

		cfg, err := config.Load()
		if err != nil {
			return err
		}
		fmt.Printf("dir:          %s  (expands to %s)\n", tilde, local)
		fmt.Printf("hub:          %q\n", cfg.Hub)
		fmt.Printf("machine_addr: %q\n", cfg.MachineAddr)

		hubOwned := cfg.MachineAddr != "" && cfg.Hub != ""
		var sessions []mutagen.Session
		if hubOwned {
			fmt.Println("mode:         hub-owned (sessions read from the hub's ledger over SSH)")
			sessions, err = actions.HubOwnedSessions(cfg.Hub, cfg.MachineAddr)
			if err != nil {
				fmt.Printf("ledger fetch FAILED: %v\n", err)
				fmt.Println("→ bare `duck` would abort with this error (not prompt).")
				return nil
			}
		} else {
			fmt.Println("mode:         laptop-owned (sessions read from the local mutagen daemon)")
			sessions, err = mutagen.List()
			if err != nil {
				fmt.Printf("local mutagen list FAILED: %v\n", err)
				return nil
			}
		}

		fmt.Printf("sessions for this machine: %d\n", len(sessions))
		covered := false
		for _, s := range sessions {
			cov := coveredBy(local, s.Alpha.Path)
			hubOK := hubOwned || s.Beta.MatchesHub(cfg.Hub)
			mark := "  "
			if cov && hubOK {
				mark = "✓ "
				covered = true
			}
			fmt.Printf("%s%s\n    local path: %s  covers=%v  hub-match=%v  status=%s\n",
				mark, s.Name, s.Alpha.Path, cov, hubOK, s.Status)
		}

		policy, known := folder.NewStore().Get(tilde)
		if known {
			fmt.Printf("folder policy: %q (remembered)\n", policy)
		} else {
			fmt.Println("folder policy: (none remembered)")
		}

		if covered {
			fmt.Println("verdict: SYNCED — bare `duck` will not prompt for this dir.")
		} else if known && policy == folder.PolicySync {
			fmt.Println("verdict: NOT COVERED but policy=sync — bare `duck` re-mirrors without prompting.")
		} else if known && policy == folder.PolicyNever {
			fmt.Println("verdict: NOT COVERED, policy=never — bare `duck` opens without syncing.")
		} else {
			fmt.Println("verdict: NOT COVERED, no policy — bare `duck` prompts (if the folder classifies risky) or auto-syncs (if safe).")
		}
		return nil
	},
}

// coveredBy mirrors flow's pathCoveredBy: dir equals ancestor or sits under it
// (path-segment compare, so /a/foobar is not covered by /a/foo).
func coveredBy(dir, ancestor string) bool {
	return dir == ancestor || strings.HasPrefix(dir, ancestor+"/")
}

func init() { syncCmd.AddCommand(whyCmd) }
