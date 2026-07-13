package tmuxdb

import (
	"strings"
	"testing"
)

func TestCurrentClientAnchorsToCurrentPane(t *testing.T) {
	t.Setenv("TMUX_PANE", "%42")
	var got string
	run := func(args ...string) (string, error) {
		got = strings.Join(args, " ")
		return "/dev/pts/7\n", nil
	}

	client, err := CurrentClient(run)
	if err != nil {
		t.Fatal(err)
	}
	if client != "/dev/pts/7" {
		t.Fatalf("client = %q, want /dev/pts/7", client)
	}
	want := "display-message -p -t %42 #{client_name}"
	if got != want {
		t.Fatalf("tmux call = %q, want %q", got, want)
	}
}

func TestCurrentClientRejectsEmptyResult(t *testing.T) {
	run := func(args ...string) (string, error) { return "\n", nil }
	if _, err := CurrentClient(run); err == nil {
		t.Fatal("CurrentClient accepted an empty client name")
	}
}
