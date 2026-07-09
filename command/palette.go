// `duck palette` — the session-manager popup (bound to prefix-Space via
// duck.tmux.conf, run inside `display-popup -E`). It offers exactly the verbs
// a human managing a workspace needs — jump to a window, kill an agent, kill
// or detach the workspace, open the fleet — and executes ONE tmux verb for
// the choice, then exits. Spawning is deliberately absent: launching agents
// is the manager Claude's job (duck spawn / MCP), not a popup's.
package command

import (
	"fmt"
	"strings"

	"github.com/DigiBugCat/duck/internal/picker"
	"github.com/DigiBugCat/duck/internal/tmuxdb"
	"github.com/spf13/cobra"
)

// paletteEntry is one palette candidate: a label for the picker plus the verb
// (and its argument) the command layer runs when it is chosen.
type paletteEntry struct {
	label string
	kind  string // jump | kill | killws | detach | fleet
	arg   string // jump: window index; kill: pane id
}

// paletteEntries builds the candidate list fresh at open: one jump per window
// of the session, one kill per agent, then the session verbs. Nothing is
// cached — the popup is one-shot, so the list is always live.
func paletteEntries(run tmuxdb.Runner, outer string) ([]paletteEntry, error) {
	var entries []paletteEntry
	out, err := run("list-windows", "-t", outer, "-F", "#{window_index}\t#{window_name}")
	if err != nil {
		return nil, err
	}
	// TrimRight newlines ONLY; window_name is free text so it goes last with a
	// bounded split, and a missing trailing field is tolerated.
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.SplitN(line, "\t", 2)
		if len(f) < 1 || strings.TrimSpace(f[0]) == "" {
			continue
		}
		name := ""
		if len(f) == 2 {
			name = f[1]
		}
		entries = append(entries, paletteEntry{
			label: "jump: " + name,
			kind:  "jump",
			arg:   strings.TrimSpace(f[0]),
		})
	}
	agents, err := tmuxdb.Agents(run, outer)
	if err != nil {
		return nil, err
	}
	for _, a := range agents {
		entries = append(entries, paletteEntry{
			label: "kill: " + a.Name,
			kind:  "kill",
			arg:   a.PaneID,
		})
	}
	entries = append(entries,
		paletteEntry{label: "kill workspace", kind: "killws"},
		paletteEntry{label: "detach — duck -c resumes", kind: "detach"},
		paletteEntry{label: "fleet — every agent", kind: "fleet"},
	)
	return entries, nil
}

var paletteCmd = &cobra.Command{
	Use:   "palette",
	Short: "Session-manager popup: jump / kill / detach (bind: prefix-Space)",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, args []string) error {
		run := tmuxdb.ExecRunner
		if !tmuxdb.InsideTmux() {
			return fmt.Errorf("duck palette only works inside a duck workspace — run `duck` first")
		}
		outer, err := tmuxdb.CurrentSession(run)
		if err != nil {
			return err
		}
		entries, err := paletteEntries(run, outer)
		if err != nil {
			return err
		}
		items := make([]picker.Item, len(entries))
		for i, e := range entries {
			items[i] = picker.Item{Label: e.label}
		}
		res, err := picker.Run(picker.Options{Title: "duck · " + outer, Items: items})
		if err != nil || res.Index < 0 {
			return err
		}
		// Exactly ONE tmux verb per choice — the picker itself never wrote.
		switch e := entries[res.Index]; e.kind {
		case "jump":
			_, err = run("select-window", "-t", outer+":"+e.arg)
			return err
		case "kill":
			return tmuxdb.Kill(run, e.arg)
		case "killws":
			yes, cerr := picker.Confirm("kill workspace " + outer + " (every pane dies)?")
			if cerr != nil || !yes {
				return cerr
			}
			_, err = run("kill-session", "-t", outer)
			return err
		case "detach":
			_, err = run("detach-client", "-s", outer)
			return err
		case "fleet":
			// The fleet is its own picker loop — run it in-process rather than
			// re-execing through another popup.
			return runFleet(run, outer)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(paletteCmd)
}
