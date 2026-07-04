// Package window is the duck window host: a client-side process that owns a
// chromium (app-mode when headful) via CDP, injects an annotation runtime
// into every page, and stores the human's marks so agents can query them.
// See docs/WINDOW.md — this is the spike: host + CDP window + navigate +
// highlight round-trip. Fetch interception, drawing, and screenshot crops
// come later.
package window

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/page"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// DefaultPort is the window host's HTTP port on the client machine
// (loopback+tailnet; 73xx per fleet convention, hub never uses it).
const DefaultPort = 7334

// Mark is one human annotation. The spike supports text highlights with an
// optional comment; Before/After carry surrounding context so a mark
// degrades legibly when the underlying artifact is rewritten (anchor drift —
// see docs/WINDOW.md).
type Mark struct {
	URL     string `json:"url"`
	Text    string `json:"text"`
	Comment string `json:"comment,omitempty"`
	Before  string `json:"before,omitempty"`
	After   string `json:"after,omitempty"`
	Stamp   string `json:"stamp"` // RFC3339, host-side arrival time
}

// Store is the file-backed mark store (~/.duck/window-marks.json).
type Store struct {
	mu    sync.Mutex
	path  string
	marks []Mark
}

func NewStore(path string) (*Store, error) {
	s := &Store{path: path}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s.marks) // corrupt store = start empty, never fatal
	}
	return s, nil
}

func (s *Store) Add(m Mark) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m.Stamp = time.Now().UTC().Format(time.RFC3339)
	s.marks = append(s.marks, m)
	s.persist()
}

// Marks returns annotations, newest last; url == "" means all.
func (s *Store) Marks(url string) []Mark {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Mark
	for _, m := range s.marks {
		if url == "" || m.URL == url {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Stamp < out[j].Stamp })
	return out
}

func (s *Store) persist() {
	b, err := json.MarshalIndent(s.marks, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
	_ = os.WriteFile(s.path, b, 0o644)
}

// Host owns the browser session and serves the control API.
type Host struct {
	Store    *Store
	Headless bool // headless chrome (tests / no display); headful app-mode otherwise

	mu     sync.Mutex
	cancel []context.CancelFunc
	tab    context.Context
	curURL string
}

// findChrome resolves a chromium binary: $DUCK_WINDOW_CHROME, the well-known
// system paths, then the puppeteer cache (present wherever gosling ran).
func findChrome() string {
	if p := os.Getenv("DUCK_WINDOW_CHROME"); p != "" {
		return p
	}
	for _, p := range []string{
		"/usr/bin/chromium", "/usr/bin/chromium-browser", "/usr/bin/google-chrome",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	home, _ := os.UserHomeDir()
	hits, _ := filepath.Glob(filepath.Join(home, ".cache/puppeteer/chrome/*/chrome-linux64/chrome"))
	if len(hits) > 0 {
		sort.Strings(hits)
		return hits[len(hits)-1]
	}
	return ""
}

// launchTab is the seam between "get me a driveable CDP tab" and how the
// browser actually got started. Linux headful/headless use a direct
// ExecAllocator; darwin headful launches the DuckWindow.app wrapper bundle
// via LaunchServices (for dock identity) and connects with a remote
// allocator instead (see bundle_darwin.go / launch_darwin.go). This is also
// the seam a future native WKWebView backend would slot in behind
// (docs/WINDOW.md).
func launchTab(parent context.Context, chrome string, headless bool) (context.Context, []context.CancelFunc, error) {
	if runtime.GOOS == "darwin" && !headless {
		return launchDarwinTab(parent, chrome)
	}
	home, _ := os.UserHomeDir()
	opts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(chrome),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.UserDataDir(filepath.Join(home, ".duck", "window-profile")),
	}
	if headless {
		opts = append(opts, chromedp.Headless, chromedp.DisableGPU,
			chromedp.NoSandbox) // hub containers lack userns; headless is test-only
	} else {
		// Headful: chromeless app window — a duck surface, not a browser.
		opts = append(opts, chromedp.Flag("app", "about:blank"))
	}
	actx, acancel := chromedp.NewExecAllocator(parent, opts...)
	tab, tcancel := chromedp.NewContext(actx)
	return tab, []context.CancelFunc{tcancel, acancel}, nil
}

// startBrowser launches chrome and prepares the annotation machinery on a
// fresh tab: the duckMark binding (page→host, native CDP callback) and the
// runtime injected into every future document.
func (h *Host) startBrowser(parent context.Context) error {
	chrome := findChrome()
	if chrome == "" {
		return fmt.Errorf("no chromium found (set DUCK_WINDOW_CHROME)")
	}
	tab, cancels, err := launchTab(parent, chrome, h.Headless)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.cancel = append(h.cancel, cancels...)
	h.tab = tab
	h.mu.Unlock()

	// Marks arrive here: the page calls duckMark(json) and CDP delivers it.
	chromedp.ListenTarget(tab, func(ev interface{}) {
		if e, ok := ev.(*cdpruntime.EventBindingCalled); ok && e.Name == "duckMark" {
			var m Mark
			if err := json.Unmarshal([]byte(e.Payload), &m); err == nil && m.Text != "" {
				h.mu.Lock()
				if m.URL == "" {
					m.URL = h.curURL
				}
				h.mu.Unlock()
				h.Store.Add(m)
			}
		}
	})
	return chromedp.Run(tab,
		chromedp.ActionFunc(func(ctx context.Context) error {
			if err := cdpruntime.AddBinding("duckMark").Do(ctx); err != nil {
				return err
			}
			_, err := page.AddScriptToEvaluateOnNewDocument(AnnotationJS).Do(ctx)
			return err
		}),
	)
}

// Show navigates the window to url (custody: CDP Page.navigate, same tab)
// and re-applies any stored marks for it.
func (h *Host) Show(url string) error {
	h.mu.Lock()
	tab := h.tab
	h.curURL = url
	h.mu.Unlock()
	if tab == nil {
		return fmt.Errorf("browser not started")
	}
	if err := chromedp.Run(tab, chromedp.Navigate(url), chromedp.WaitReady("body")); err != nil {
		return err
	}
	marks := h.Store.Marks(url)
	if len(marks) == 0 {
		return nil
	}
	b, _ := json.Marshal(marks)
	return chromedp.Run(tab,
		chromedp.Evaluate("window.__duckApplyMarks && window.__duckApplyMarks("+string(b)+")", nil))
}

// Eval runs js in the current tab and discards the result. Exported for
// tests: it lets an e2e test drive the page the way the real annotation
// runtime would (calling window.duckMark) without a human doing the
// select-and-click.
func (h *Host) Eval(js string) error {
	h.mu.Lock()
	tab := h.tab
	h.mu.Unlock()
	if tab == nil {
		return fmt.Errorf("browser not started")
	}
	return chromedp.Run(tab, chromedp.Evaluate(js, nil))
}

// Serve runs the control API until ctx ends. ln lets tests bind :0.
func (h *Host) Serve(ctx context.Context, ln net.Listener) error {
	if err := h.startBrowser(ctx); err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("POST /open", func(w http.ResponseWriter, r *http.Request) {
		u := strings.TrimSpace(r.FormValue("url"))
		if u == "" {
			http.Error(w, "missing url", http.StatusBadRequest)
			return
		}
		if err := h.Show(u); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		fmt.Fprintln(w, "shown")
	})
	mux.HandleFunc("GET /marks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		marks := h.Store.Marks(r.URL.Query().Get("url"))
		if marks == nil {
			marks = []Mark{}
		}
		_ = json.NewEncoder(w).Encode(marks)
	})
	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
		h.mu.Lock()
		for i := len(h.cancel) - 1; i >= 0; i-- {
			h.cancel[i]()
		}
		h.mu.Unlock()
	}()
	err := srv.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
