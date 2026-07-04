//go:build darwin

package window

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureDuckWindowBundle(t *testing.T) {
	home := t.TempDir()

	bundle, err := ensureDuckWindowBundle(home, "/fake/chrome", 7335)
	if err != nil {
		t.Fatalf("ensureDuckWindowBundle: %v", err)
	}
	if bundle != filepath.Join(home, ".duck", "DuckWindow.app") {
		t.Fatalf("unexpected bundle path: %s", bundle)
	}

	plist, err := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist"))
	if err != nil {
		t.Fatalf("reading Info.plist: %v", err)
	}
	if !strings.Contains(string(plist), "Duck Window") {
		t.Fatalf("Info.plist missing bundle name: %s", plist)
	}
	if !strings.Contains(string(plist), "cat.digibug.duck.window") {
		t.Fatalf("Info.plist missing bundle identifier: %s", plist)
	}

	scriptPath := filepath.Join(bundle, "Contents", "MacOS", "duck-window")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("reading launcher script: %v", err)
	}
	if !strings.Contains(string(script), "7335") {
		t.Fatalf("launcher script missing port: %s", script)
	}
	if !strings.Contains(string(script), "/fake/chrome") {
		t.Fatalf("launcher script missing chrome path: %s", script)
	}

	fi, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("stat launcher script: %v", err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Fatalf("launcher script is not executable: mode=%v", fi.Mode())
	}

	// Idempotent regeneration: same inputs should not error and should
	// leave equivalent content (content-diff gated, like the agent-notes
	// pattern in command/agentdoc.go).
	if _, err := ensureDuckWindowBundle(home, "/fake/chrome", 7335); err != nil {
		t.Fatalf("second ensureDuckWindowBundle: %v", err)
	}
	script2, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("reading launcher script after regen: %v", err)
	}
	if string(script) != string(script2) {
		t.Fatalf("regeneration changed content unexpectedly")
	}

	// Changed inputs (different port) should regenerate with new content.
	if _, err := ensureDuckWindowBundle(home, "/fake/chrome", 9999); err != nil {
		t.Fatalf("ensureDuckWindowBundle with new port: %v", err)
	}
	script3, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("reading launcher script after port change: %v", err)
	}
	if !strings.Contains(string(script3), "9999") {
		t.Fatalf("launcher script did not pick up new port: %s", script3)
	}
}
