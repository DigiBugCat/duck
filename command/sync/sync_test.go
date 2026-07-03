package sync

import "testing"

// TestSubcommandsRegisterOnSyncCmd asserts the cobra wiring: every bundle
// subcommand registers on syncCmd (the `sync` parent), NOT on the root — duck's
// one structural change from flok's flat cmd layout.
func TestSubcommandsRegisterOnSyncCmd(t *testing.T) {
	want := map[string]bool{
		"new": true, "add": true, "get": true, "ls": true,
		"rm": true, "drop": true, "show": true, "status": true,
		"prune": true, "migrate": true,
	}
	got := map[string]bool{}
	for _, c := range syncCmd.Commands() {
		got[c.Name()] = true
	}
	for name := range want {
		if !got[name] {
			t.Errorf("subcommand %q not registered on syncCmd", name)
		}
	}
	if len(got) != len(want) {
		t.Errorf("syncCmd has %d subcommands, want %d: got %v", len(got), len(want), got)
	}
	// hub.go is NOT under sync (it ports to top-level command/hubsetup.go).
	if got["hub"] || got["setup"] {
		t.Errorf("hub/setup must not be a sync subcommand: %v", got)
	}
}

func TestSyncCmdIsExportedAsCmd(t *testing.T) {
	if Cmd != syncCmd {
		t.Fatal("exported Cmd must be the syncCmd parent")
	}
	if Cmd.Use != "sync" {
		t.Errorf("Cmd.Use = %q, want \"sync\"", Cmd.Use)
	}
}
