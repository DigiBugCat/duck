// `duck ls`: list remote sessions without attaching (DESIGN §1). It reads live
// sessions + resolved names (user ▸ codex ▸ dir-derived) and prints them. The
// internal tmux name is shown last so `duck kill`/`duck rename` have a handle.
//
// `duck ls --json` is the agent-facing form: one JSON array on stdout, each
// session carrying the resolved name, dir, recency, a computed status
// (attached/active/idle/evicted), and the raw pane title — Claude Code writes
// its current task summary there, so an agent can scan what every session is
// doing without attaching to any of them.
package command

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/DigiBugCat/duck/internal/model"
	"github.com/spf13/cobra"
)

// lsIdleThreshold splits "active" (produced output recently) from "idle" in the
// --json status field. It mirrors the picker's idleThreshold (internal/tui): a
// detached session whose pane printed something within the window is treated as
// still working.
const lsIdleThreshold = 2 * time.Hour

var lsJSON bool

// lsRow is the JSON shape of one session for `duck ls --json`. Field names are
// the stable contract with agent consumers; add fields, don't rename.
type lsRow struct {
	Name       string `json:"name"`              // resolved display name
	Title      string `json:"title,omitempty"`   // raw pane title — Claude Code's live task summary
	Dir        string `json:"dir,omitempty"`     // tilde-form working dir ("" for foreign sessions)
	Status     string `json:"status"`            // attached | active | idle | evicted
	Age        string `json:"age"`               // humanized, e.g. "2m", "1h"
	AgeSeconds int64  `json:"age_seconds"`       // seconds since last pane activity (or eviction)
	LastActive string `json:"last_active"`       // RFC3339
	Attached   bool   `json:"attached"`
	Looped     bool   `json:"looped"`  // running a /loop (@duck_loop)
	Windows    int    `json:"windows"`
	Evicted    bool   `json:"evicted"` // not live; revivable via the picker (claude --resume)
	TmuxName   string `json:"tmux"`    // handle for duck kill/rename
}

// lsStatus collapses a row's state into the one-word status agents branch on:
// evicted (not live), attached (a human is in it), active (detached but the
// pane produced output within lsIdleThreshold — likely still working), idle.
func lsStatus(r model.Row, now time.Time) string {
	switch {
	case r.Evicted:
		return "evicted"
	case r.Attached:
		return "attached"
	case now.Sub(r.LastSeen) < lsIdleThreshold:
		return "active"
	default:
		return "idle"
	}
}

var lsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List remote sessions without attaching",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, args []string) error {
		w, err := build()
		if err != nil {
			return err
		}
		// List() is the read-only path: it resolves rows without auto-naming, so a
		// read-only-sounding `ls` never spends codex quota or writes names.json on
		// first sight of an unnamed session (auto-naming stays on the picker).
		rows, err := w.app.List()
		if err != nil {
			return err
		}
		if lsJSON {
			now := time.Now()
			out := make([]lsRow, 0, len(rows))
			for _, r := range rows {
				out = append(out, lsRow{
					Name:       r.Display,
					Title:      r.Title,
					Dir:        r.Dir,
					Status:     lsStatus(r, now),
					Age:        r.Age,
					AgeSeconds: int64(now.Sub(r.LastSeen).Seconds()),
					LastActive: r.LastSeen.Format(time.RFC3339),
					Attached:   r.Attached,
					Looped:     r.Looped,
					Windows:    r.Windows,
					Evicted:    r.Evicted,
					TmuxName:   r.TmuxName,
				})
			}
			enc := json.NewEncoder(c.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}
		if len(rows) == 0 {
			fmt.Println("(no sessions)")
			return nil
		}
		tw := tabwriter.NewWriter(c.OutOrStdout(), 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tDIR\tAGE\tWIN\tATTACHED\tTMUX")
		for _, r := range rows {
			attached := ""
			if r.Attached {
				attached = "●"
			}
			dir := r.Dir
			if dir == "" {
				dir = "—" // foreign/legacy session: no @duck_dir recorded
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%dw\t%s\t%s\n", r.Display, dir, r.Age, r.Windows, attached, r.TmuxName)
		}
		return tw.Flush()
	},
}

func init() {
	lsCmd.Flags().BoolVar(&lsJSON, "json", false,
		"emit sessions as a JSON array (machine-readable; includes status and pane title)")
}
