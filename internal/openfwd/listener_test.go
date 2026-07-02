package openfwd

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"
)

// startTestListener spins up a real Listener on loopback with deps that record
// opens, and returns it plus its bound port. The caller defers Close.
func startTestListener(t *testing.T, opened *[]string) *Listener {
	t.Helper()
	ln, err := Start(Deps{
		LocalHome: "/Users/me",
		Open:      func(target string) error { *opened = append(*opened, target); return nil },
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ln
}

// TestOpenRoutePostReachesOpen confirms a POST /open drives the injected Open —
// the contract the duck-open shim depends on, now over the per-session socket
// forward (the listener itself is transport-agnostic: loopback here, a
// unix-socket reverse forward in production).
func TestOpenRoutePostReachesOpen(t *testing.T) {
	var opened []string
	ln := startTestListener(t, &opened)
	defer ln.Close()

	resp, err := http.PostForm(
		"http://127.0.0.1:"+strconv.Itoa(ln.LocalPort())+"/open",
		url.Values{"target": {"https://anthropic.com/x"}})
	if err != nil {
		t.Fatalf("POST /open: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(opened) != 1 || opened[0] != "https://anthropic.com/x" {
		t.Fatalf("opened = %v, want [https://anthropic.com/x]", opened)
	}
}
