package command

import (
	"fmt"
	"net"
	"net/http"
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
	run := func(args ...string) (string, error) {
		if got := fmt.Sprint(args); got == "[show-environment -t work DUCK_WINDOW_SOCK]" {
			return "DUCK_WINDOW_SOCK=/tmp/window-work.sock\n", nil
		}
		return "", fmt.Errorf("unexpected tmux call: %v", args)
	}
	if got := resolveWindowTargetWithRun(run, "work").label(); got != "unix:/tmp/window-work.sock" {
		t.Fatalf("session socket target = %q, want unix:/tmp/window-work.sock", got)
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

func TestWindowHostResolutionUsesPaneAnchoredSession(t *testing.T) {
	restoreWindowTestGlobals(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMUX", "/tmp/tmux,1,0")
	t.Setenv("TMUX_PANE", "%42")
	var calls []string
	run := func(args ...string) (string, error) {
		calls = append(calls, fmt.Sprint(args))
		switch fmt.Sprint(args) {
		case "[display-message -p -t %42 #{session_name}]":
			return "work\n", nil
		case "[show-environment -t work DUCK_WINDOW_SOCK]":
			return "DUCK_WINDOW_SOCK=/tmp/window-work.sock\n", nil
		default:
			return "", fmt.Errorf("unexpected tmux call: %v", args)
		}
	}

	if got := resolveWindowTargetWithRun(run, "").label(); got != "unix:/tmp/window-work.sock" {
		t.Fatalf("target = %q, want pane-anchored session socket", got)
	}
	if len(calls) == 0 || calls[0] != "[display-message -p -t %42 #{session_name}]" {
		t.Fatalf("CurrentSession must target TMUX_PANE first, calls: %v", calls)
	}
}

func TestWindowUnixSocketClient(t *testing.T) {
	restoreWindowTestGlobals(t)
	sock := t.TempDir() + "/window.sock"
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Fatalf("path = %s, want /health", r.URL.Path)
		}
		fmt.Fprint(w, "ok")
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	client, baseURL, _ := windowClientForTarget(windowTarget{sock: sock})
	resp, err := client.Get(baseURL + "/health")
	if err != nil {
		t.Fatalf("GET over unix socket: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %s, want 200 OK", resp.Status)
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
