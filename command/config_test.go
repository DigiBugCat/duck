package command

import (
	"bytes"
	"strings"
	"testing"

	"github.com/DigiBugCat/duck/internal/config"
)

func TestConfigCmdShowsHubAndFolders(t *testing.T) {
	// Hermetic: redirect HOME so config.Load/Save use a temp file the framework
	// auto-cleans, never the real machine.
	t.Setenv("HOME", t.TempDir())
	if err := config.Save(&config.Config{
		Hub:     "duck",
		HubName: "hub.local",
		Folders: map[string]string{"~/dev/foo": "sync", "~/Downloads": "never"},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Invoke RunE directly: configCmd.Execute() would resolve to the root command
	// (bare `duck`) and try to hit the hub. We want just the config printer.
	var buf bytes.Buffer
	configCmd.SetOut(&buf)
	if err := configCmd.RunE(configCmd, []string{}); err != nil {
		t.Fatalf("config: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"duck", "hub.local", "~/dev/foo", "sync", "~/Downloads", "never"} {
		if !strings.Contains(out, want) {
			t.Errorf("config output missing %q:\n%s", want, out)
		}
	}
}
