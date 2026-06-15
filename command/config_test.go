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

// TestConfigCmdShowsAttachTransport pins that the printer surfaces the
// interactive-attach transport line with its set value.
func TestConfigCmdShowsAttachTransport(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := config.Save(&config.Config{Hub: "duck", AttachTransport: "mosh"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	var buf bytes.Buffer
	configCmd.SetOut(&buf)
	if err := configCmd.RunE(configCmd, []string{}); err != nil {
		t.Fatalf("config: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"attach transport", "mosh"} {
		if !strings.Contains(out, want) {
			t.Errorf("config output missing %q:\n%s", want, out)
		}
	}
}

// TestConfigAttachTransportSetter pins the setter: mosh persists, ssh clears the
// stored value (empty == ssh default, keeping config.toml clean), and an unknown
// value errors (ValidArgs alone does not enforce — the RunE switch does).
func TestConfigAttachTransportSetter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var buf bytes.Buffer
	configAttachTransportCmd.SetOut(&buf)

	if err := configAttachTransportCmd.RunE(configAttachTransportCmd, []string{"mosh"}); err != nil {
		t.Fatalf("set mosh: %v", err)
	}
	if cfg, _ := config.Load(); cfg.Transport() != "mosh" {
		t.Fatalf("after set mosh, Transport() = %q, want mosh", cfg.Transport())
	}

	if err := configAttachTransportCmd.RunE(configAttachTransportCmd, []string{"ssh"}); err != nil {
		t.Fatalf("set ssh: %v", err)
	}
	cfg, _ := config.Load()
	if cfg.AttachTransport != "" {
		t.Fatalf("after set ssh, AttachTransport = %q, want empty (clean default)", cfg.AttachTransport)
	}
	if cfg.Transport() != "ssh" {
		t.Fatalf("after set ssh, Transport() = %q, want ssh", cfg.Transport())
	}

	if err := configAttachTransportCmd.RunE(configAttachTransportCmd, []string{"telnet"}); err == nil {
		t.Fatalf("an invalid transport must error")
	}
}
