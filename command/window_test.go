package command

import (
	"fmt"
	"testing"
	"time"

	"github.com/DigiBugCat/duck/internal/config"
	"github.com/DigiBugCat/duck/internal/window"
)

func restoreWindowTestGlobals(t *testing.T) {
	t.Helper()
	oldFlag := windowHostFlag
	oldHealthy := windowHostHealthy
	oldStart := startWindowHost
	oldSleep := windowEnsureSleep
	t.Cleanup(func() {
		windowHostFlag = oldFlag
		windowHostHealthy = oldHealthy
		startWindowHost = oldStart
		windowEnsureSleep = oldSleep
	})
	windowHostFlag = ""
}

func TestWindowHostResolutionPrecedence(t *testing.T) {
	restoreWindowTestGlobals(t)
	t.Setenv("HOME", t.TempDir())
	if err := config.Save(&config.Config{WindowHost: "studio:7334"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := windowHost(); got != "studio:7334" {
		t.Fatalf("config windowHost() = %q, want studio:7334", got)
	}

	t.Setenv("DUCK_WINDOW_HOST", "envbox:7334")
	if got := windowHost(); got != "envbox:7334" {
		t.Fatalf("env windowHost() = %q, want envbox:7334", got)
	}

	windowHostFlag = "flagbox:7334"
	if got := windowHost(); got != "flagbox:7334" {
		t.Fatalf("flag windowHost() = %q, want flagbox:7334", got)
	}
}

func TestWindowHostDefaultsToLoopback(t *testing.T) {
	restoreWindowTestGlobals(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUCK_WINDOW_HOST", "")
	want := fmt.Sprintf("127.0.0.1:%d", window.DefaultPort)
	if got := windowHost(); got != want {
		t.Fatalf("windowHost() = %q, want %q", got, want)
	}
}

func TestEnsureWindowHostStartsDetachedLocalSingleton(t *testing.T) {
	restoreWindowTestGlobals(t)
	var started int
	var probes int
	windowHostHealthy = func(host string) bool {
		probes++
		return started > 0
	}
	startWindowHost = func() error {
		started++
		return nil
	}
	windowEnsureSleep = func(time.Duration) {}

	if err := ensureWindowHost("127.0.0.1:7334"); err != nil {
		t.Fatalf("ensureWindowHost: %v", err)
	}
	if started != 1 {
		t.Fatalf("startWindowHost called %d times, want 1", started)
	}
	if probes < 2 {
		t.Fatalf("health probe called %d times, want initial probe plus poll", probes)
	}
}

func TestEnsureWindowHostDoesNotStartWhenAlreadyHealthy(t *testing.T) {
	restoreWindowTestGlobals(t)
	windowHostHealthy = func(host string) bool { return true }
	startWindowHost = func() error {
		t.Fatalf("startWindowHost must not run when host is already healthy")
		return nil
	}

	if err := ensureWindowHost("localhost:7334"); err != nil {
		t.Fatalf("ensureWindowHost: %v", err)
	}
}

func TestEnsureWindowHostDoesNotStartForRemoteConfiguredHost(t *testing.T) {
	restoreWindowTestGlobals(t)
	windowHostHealthy = func(host string) bool {
		t.Fatalf("remote hosts should not be probed for local singleton startup")
		return false
	}
	startWindowHost = func() error {
		t.Fatalf("startWindowHost must not run for remote configured host")
		return nil
	}

	if err := ensureWindowHost("studio:7334"); err != nil {
		t.Fatalf("ensureWindowHost: %v", err)
	}
}
