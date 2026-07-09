// `duck fleet` — the every-agent popup (bound to prefix-f via duck.tmux.conf,
// run inside `display-popup -E`). One scrollable row per agent of the current
// session: state glyph, name, running command, age, window — with the spawn
// cmdline and the agent's last rollout activity as the detail line. Everything
// is read live at open (tmux stamps + the channel's rollout parsing); the only
// writes are the chosen verb: ⏎ jumps to the agent's window, x kills it and
// the list rebuilds. One-shot, nothing standing, nothing to heal.
package command

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/DigiBugCat/duck/internal/channel"
	"github.com/DigiBugCat/duck/internal/picker"
	"github.com/DigiBugCat/duck/internal/tmuxdb"
	"github.com/spf13/cobra"
)

// stateGlyph maps the channel's status classification to the roster glyphs.
func stateGlyph(status string) string {
	switch status {
	case "working":
		return "◐"
	case "done":
		return "●"
	}
	return "○"
}

// paneMeta is the per-pane detail the fleet needs beyond tmuxdb.Agent:
// where the pane sits and the spawn stamps.
type paneMeta struct {
	windowIndex string
	spawnedAt   time.Time
	cmd         string // spawn cmdline (@duck_cmd) — the agent's task
}

// fleetMetaFormat lists pane placement + spawn stamps. @duck_cmd is free text
// (an arbitrary cmdline), so it goes LAST with a bounded split.
const fleetMetaFormat = "#{pane_id}\t#{window_index}\t#{" + tmuxdb.SpawnedAtOption + "}\t#{" + tmuxdb.CmdOption + "}"

// paneMetas fetches placement + stamps for every pane of the session in one
// tmux call, keyed by pane id.
func paneMetas(run tmuxdb.Runner, outer string) (map[string]paneMeta, error) {
	out, err := run("list-panes", "-s", "-t", outer, "-F", fleetMetaFormat)
	if err != nil {
		return nil, err
	}
	metas := map[string]paneMeta{}
	// TrimRight newlines ONLY (TrimSpace eats the last line's trailing tab
	// when @duck_cmd is empty); tolerate missing trailing fields.
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.SplitN(line, "\t", 4)
		if len(f) < 2 || strings.TrimSpace(f[0]) == "" {
			continue
		}
		m := paneMeta{windowIndex: strings.TrimSpace(f[1])}
		if len(f) > 2 {
			if secs, err := strconv.ParseInt(strings.TrimSpace(f[2]), 10, 64); err == nil {
				m.spawnedAt = time.Unix(secs, 0)
			}
		}
		if len(f) > 3 {
			m.cmd = f[3]
		}
		metas[f[0]] = m
	}
	return metas, nil
}

// fmtAge renders a spawn age compactly (12s, 3m, 2h, 5d); "?" when unstamped.
func fmtAge(spawnedAt time.Time, now time.Time) string {
	if spawnedAt.IsZero() {
		return "?"
	}
	d := now.Sub(spawnedAt)
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	}
	return strconv.Itoa(int(d.Hours()/24)) + "d"
}

// fleetRow is one agent's rendered pair of picker lines plus its verbs' args.
type fleetRow struct {
	item        picker.Item
	paneID      string
	windowIndex string
}

// fleetRows assembles the live rows: identity from tmuxdb, placement/stamps
// from paneMetas, state + last activity from the channel's rollout parsing
// (Resolve + StatusByWindow + LastMessage — never reimplemented here).
func fleetRows(run tmuxdb.Runner, outer string) ([]fleetRow, error) {
	agents, err := tmuxdb.Agents(run, outer)
	if err != nil {
		return nil, err
	}
	metas, err := paneMetas(run, outer)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	rows := make([]fleetRow, 0, len(agents))
	for _, a := range agents {
		m := metas[a.PaneID]
		label := fmt.Sprintf("%s %s · %s · %s · win %s",
			stateGlyph(channel.StatusByWindow(run, a.PaneID)),
			a.Name, a.Command, fmtAge(m.spawnedAt, now), m.windowIndex)
		task := m.cmd
		if task == "" {
			task = "(shell)"
		}
		detail := "task: " + task
		ref := channel.AgentRef{Session: outer, Name: a.Name, WindowID: a.PaneID}
		if last := channel.LastMessage(run, &ref); last != "" {
			if line, _, _ := strings.Cut(last, "\n"); line != "" {
				detail += " · " + line
			}
		}
		rows = append(rows, fleetRow{
			item:        picker.Item{Label: label, Detail: detail},
			paneID:      a.PaneID,
			windowIndex: m.windowIndex,
		})
	}
	return rows, nil
}

// runFleet loops the fleet picker: ⏎ jumps and exits; x kills the row's pane
// and reopens with a fresh list; esc/q leaves. Shared with the palette's
// "fleet" entry so both paths stay in-process.
func runFleet(run tmuxdb.Runner, outer string) error {
	for {
		rows, err := fleetRows(run, outer)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			fmt.Println("no agents in " + outer + " — spawn some (duck spawn codex)")
			return nil
		}
		items := make([]picker.Item, len(rows))
		for i, r := range rows {
			items[i] = r.item
		}
		res, err := picker.Run(picker.Options{
			Title:     "fleet · " + outer,
			Items:     items,
			ExtraKeys: []string{"x"},
			NoFilter:  true, // short action list: j/k/x are always verbs
		})
		if err != nil || res.Index < 0 {
			return err
		}
		row := rows[res.Index]
		switch res.Key {
		case "x":
			if err := tmuxdb.Kill(run, row.paneID); err != nil {
				return err
			}
			continue // rebuild the list and keep browsing
		default: // enter
			if row.windowIndex == "" {
				// Legacy agent parked in the hidden companion session: it has
				// no window in this session, and `select-window -t "outer:"`
				// would silently no-op to the current window. Keep browsing.
				continue
			}
			_, err = run("select-window", "-t", outer+":"+row.windowIndex)
			return err
		}
	}
}

var fleetCmd = &cobra.Command{
	Use:   "fleet",
	Short: "Every agent of this workspace: state, task, last activity (bind: prefix-f)",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, args []string) error {
		run := tmuxdb.ExecRunner
		if !tmuxdb.InsideTmux() {
			return fmt.Errorf("duck fleet only works inside a duck workspace — run `duck` first")
		}
		outer, err := tmuxdb.CurrentSession(run)
		if err != nil {
			return err
		}
		return runFleet(run, outer)
	},
}

func init() {
	rootCmd.AddCommand(fleetCmd)
}
