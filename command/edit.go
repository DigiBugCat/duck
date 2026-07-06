// `duck edit [file]` — buffers as workspace citizens: open a file in an
// editor PANE (roster tab: buffers), or with no argument jump to the
// workspace's immortal scratch buffer. The emacs steal, on tmux substrate:
// the editor is just another pane with identity; the scratch pad is the
// buffer that is always there.
package command

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DigiBugCat/duck/internal/flow"
	"github.com/DigiBugCat/duck/internal/panel"
	"github.com/DigiBugCat/duck/internal/routines"
	"github.com/spf13/cobra"
)

var editCmd = &cobra.Command{
	Use:   "edit [file]",
	Short: "Open a buffer: no arg = workspace pad, bare name = named pad, path = file",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		run := panel.ExecRunner
		outer, dir, err := panelContext(run)
		if err != nil {
			return err
		}
		comp, err := panel.EnsureCompanion(run, outer, dir)
		if err != nil {
			return err
		}
		bin, err := os.Executable()
		if err != nil {
			bin = "duck"
		}
		if err := panel.Open(run, outer, comp, bin); err != nil {
			return err
		}
		if len(args) == 0 {
			// The scratch pad: ensure it, show it.
			panel.EnsureScratch(run, outer)
			agents, err := panel.Agents(run, outer)
			if err != nil {
				return err
			}
			for _, a := range agents {
				if a.Kind == panel.KindBuffer && a.Name == "scratch" {
					return panel.Select(run, outer, a.PaneID)
				}
			}
			return fmt.Errorf("scratch buffer did not come up")
		}
		arg := args[0]
		// A bare name (no path separator, no extension, not an existing file)
		// is a PAD: created on demand in ~/.duck/scratchpad/, immortal like the
		// workspace scratch. Anything path-like is a regular one-shot buffer.
		if !strings.ContainsAny(arg, "/.") {
			if _, statErr := os.Stat(arg); statErr != nil {
				path, err := panel.EnsurePad(panel.PadRoot(run, outer), arg)
				if err != nil {
					return err
				}
				_, err = panel.Spawn(run, outer, arg, dir, panel.PadCmd(path), panel.KindBuffer)
				return err
			}
		}
		abs, err := filepath.Abs(arg)
		if err != nil {
			return err
		}
		// A regular buffer: plain editor, pane closes when the editor exits.
		line := panel.EditorCmd(abs)
		_, err = panel.Spawn(run, outer, filepath.Base(abs), dir, line, panel.KindBuffer)
		return err
	},
}

func init() {
	rootCmd.AddCommand(editCmd)
	// Inject the sync-root resolver so panel + routines (low-level, no
	// flow/mutagen dep) can place pads and routine defs under the covering
	// project sync root. This init() runs for EVERY subcommand, so the binding
	// is live in the `routines tick` path too.
	panel.SyncRootFn = flow.CoveringSyncRoot
	routines.SyncRootFn = flow.CoveringSyncRoot
}
