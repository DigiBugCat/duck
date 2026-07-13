package command

import "testing"

func TestManagerLaunchCmdIsOneBatchedRoundtrip(t *testing.T) {
	got := managerLaunchCmd("foo", "claude --duck")
	want := `tmux send-keys -t 'foo' 'claude --duck' Enter && tmux set-option -t 'foo' @duck_manager "$(tmux display-message -p -t 'foo' '#{pane_id}')"`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
