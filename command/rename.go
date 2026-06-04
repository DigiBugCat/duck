// `duck rename <session> <name…>`: set a raw-UTF-8 display name for a session,
// writing names.json (DESIGN §1). It never touches the tmux name — the slug
// constraint lives only on the internal id. The trailing words are joined with
// spaces and stored verbatim (spaces, caps, emoji all fine).
package command

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var renameCmd = &cobra.Command{
	Use:   "rename <session> <name...>",
	Short: "Set a display name for a session (raw UTF-8)",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(c *cobra.Command, args []string) error {
		tmuxName := args[0]
		display := strings.Join(args[1:], " ")
		w, err := build()
		if err != nil {
			return err
		}
		if err := w.app.Rename(tmuxName, display); err != nil {
			return err
		}
		fmt.Printf("renamed %s → %s\n", tmuxName, display)
		return nil
	},
}
