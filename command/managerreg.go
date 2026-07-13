package command

import (
	"fmt"

	"github.com/DigiBugCat/duck/internal/paths"
)

func managerLaunchCmd(name, line string) string {
	q := paths.Quote(name)
	return fmt.Sprintf("tmux send-keys -t %s %s Enter && tmux set-option -t %s @duck_manager \"$(tmux display-message -p -t %s '#{pane_id}')\"", q, paths.Quote(line), q, q)
}
