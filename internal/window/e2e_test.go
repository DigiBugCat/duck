package window

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// e2eChrome mirrors findChrome's resolution for the test's skip check: we
// need to know BEFORE starting the host whether a chrome binary exists, so
// we can skip cleanly instead of failing.
func e2eChrome() string {
	return findChrome()
}

// TestWindowE2E drives the full host↔page round-trip: serve a page, tell the
// host to open it, have the page call window.duckMark (standing in for the
// annotation runtime's select-and-click), and poll /marks until it shows up.
func TestWindowE2E(t *testing.T) {
	if e2eChrome() == "" {
		t.Skip("no chromium found (set DUCK_WINDOW_CHROME); skipping e2e")
	}

	pageSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><body><p id="p">known text lives here</p></body></html>`)
	}))
	defer pageSrv.Close()

	store, err := NewStore(filepath.Join(t.TempDir(), "marks.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := &Host{Store: store, Headless: true}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	addr := ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- h.Serve(ctx, ln) }()

	client := &http.Client{Timeout: 5 * time.Second}
	hostURL := "http://" + addr

	if !waitUntil(15*time.Second, func() bool {
		resp, err := client.Get(hostURL + "/health")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}) {
		select {
		case err := <-serveErr:
			t.Fatalf("host exited early: %v", err)
		default:
		}
		t.Fatal("window host never became healthy (chrome failed to start?)")
	}

	resp, err := client.PostForm(hostURL+"/open", map[string][]string{"url": {pageSrv.URL}})
	if err != nil {
		t.Fatalf("POST /open: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /open status = %d", resp.StatusCode)
	}

	// Stand in for the human's select-and-click: call the same binding the
	// runtime uses, directly, via the test-only Eval hook.
	markJSON := fmt.Sprintf(`{"url":%q,"text":"known text","comment":"e2e"}`, pageSrv.URL)
	evalJS := "window.duckMark(JSON.stringify(" + markJSON + "))"
	if err := h.Eval(evalJS); err != nil {
		t.Fatalf("Eval duckMark: %v", err)
	}

	var got []Mark
	ok := waitUntil(15*time.Second, func() bool {
		resp, err := client.Get(hostURL + "/marks")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var marks []Mark
		if err := json.NewDecoder(resp.Body).Decode(&marks); err != nil {
			return false
		}
		if len(marks) == 0 {
			return false
		}
		got = marks
		return true
	})
	if !ok {
		t.Fatal("mark never showed up in /marks within timeout")
	}

	if got[0].Text != "known text" {
		t.Errorf("mark.Text = %q, want %q", got[0].Text, "known text")
	}
	if got[0].Comment != "e2e" {
		t.Errorf("mark.Comment = %q, want %q", got[0].Comment, "e2e")
	}

	cancel()
	select {
	case <-serveErr:
	case <-time.After(10 * time.Second):
		t.Log("host did not shut down promptly after cancel (non-fatal)")
	}
}

func waitUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return cond()
}

