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

	"github.com/DigiBugCat/duck/internal/panel"
	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/spf13/cobra"
)

var editCmd = &cobra.Command{
	Use:   "edit [file]",
	Short: "Open a file as a buffer in the sidebar (no file: the scratch pad)",
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
		abs, err := filepath.Abs(args[0])
		if err != nil {
			return err
		}
		// A regular buffer: plain editor, pane closes when the editor exits.
		line := fmt.Sprintf(`sh -c '"${EDITOR:-vim}" %s'`, paths.Quote(abs))
		_, err = panel.Spawn(run, outer, filepath.Base(abs), dir, line, panel.KindBuffer)
		return err
	},
}

func init() {
	rootCmd.AddCommand(editCmd)
}
