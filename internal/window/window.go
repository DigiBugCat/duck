// Package window is the duck window host: a client-side process that owns a
// browser surface (native WKWebView on macOS headful, chromium/CDP elsewhere),
// injects an annotation runtime into every page, and stores the human's marks
// so agents can query them.
// See docs/WINDOW.md.
package window

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultPort is the window host's HTTP port on the client machine
// (loopback+tailnet; 73xx per fleet convention, hub never uses it).
const DefaultPort = 7334

// Point is a viewport-coordinate drawing point in CSS pixels.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Rect is a viewport-coordinate rectangle in CSS pixels.
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Mark is one human annotation. Highlights carry text and optional context;
// drawings carry freehand strokes plus a screenshot crop path when available.
type Mark struct {
	Type    string    `json:"type,omitempty"`
	URL     string    `json:"url"`
	Text    string    `json:"text,omitempty"`
	Comment string    `json:"comment,omitempty"`
	Before  string    `json:"before,omitempty"`
	After   string    `json:"after,omitempty"`
	Strokes [][]Point `json:"strokes,omitempty"`
	Rect    *Rect     `json:"rect,omitempty"`
	Shot    string    `json:"shot,omitempty"`
	Stamp   string    `json:"stamp"` // RFC3339, host-side arrival time
}

func (m *Mark) UnmarshalJSON(b []byte) error {
	type markAlias Mark
	var a markAlias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*m = Mark(a)
	m.defaultType()
	return nil
}

func (m *Mark) defaultType() {
	if m.Type == "" {
		m.Type = "highlight"
	}
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
	m.defaultType()
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

	mu      sync.Mutex
	backend browserBackend
	curURL  string
}

type browserBackend interface {
	Navigate(url string) error
	Eval(js string) error
	Snapshot(rect Rect) ([]byte, error)
	Close()
}

// startBrowser launches the selected backend and prepares the annotation
// machinery. Backend selection lives behind launchBackend so the HTTP host and
// store do not care whether the surface is native WKWebView or CDP.
func (h *Host) startBrowser(parent context.Context) error {
	backend, err := launchBackend(parent, h.Headless, h.addMark)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.backend = backend
	h.mu.Unlock()
	return nil
}

func (h *Host) addMark(m Mark) {
	m.defaultType()
	if m.Type == "highlight" && m.Text == "" {
		return
	}
	h.mu.Lock()
	if m.URL == "" {
		m.URL = h.curURL
	}
	backend := h.backend
	h.mu.Unlock()
	if m.Type == "drawing" && m.Rect != nil && backend != nil {
		if shot, err := saveSnapshot(backend, paddedRect(*m.Rect, 20)); err == nil {
			m.Shot = shot
		}
	}
	h.Store.Add(m)
}

func paddedRect(r Rect, pad float64) Rect {
	x := r.X - pad
	y := r.Y - pad
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return Rect{X: x, Y: y, W: r.W + pad*2, H: r.H + pad*2}
}

func saveSnapshot(backend browserBackend, rect Rect) (string, error) {
	if rect.W <= 0 || rect.H <= 0 {
		return "", fmt.Errorf("empty snapshot rect")
	}
	png, err := backend.Snapshot(rect)
	if err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".duck", "window-shots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, time.Now().UTC().Format(time.RFC3339Nano)+".png")
	if err := os.WriteFile(path, png, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Show navigates the window to url and re-applies any stored marks for it.
func (h *Host) Show(url string) error {
	h.mu.Lock()
	backend := h.backend
	h.curURL = url
	h.mu.Unlock()
	if backend == nil {
		return fmt.Errorf("browser not started")
	}
	if err := backend.Navigate(url); err != nil {
		return err
	}
	marks := h.Store.Marks(url)
	if len(marks) == 0 {
		return nil
	}
	b, _ := json.Marshal(marks)
	return backend.Eval("window.__duckApplyMarks && window.__duckApplyMarks(" + string(b) + ")")
}

// Eval runs js in the current tab and discards the result. Exported for
// tests: it lets an e2e test drive the page the way the real annotation
// runtime would (calling window.duckMark) without a human doing the
// select-and-click.
func (h *Host) Eval(js string) error {
	h.mu.Lock()
	backend := h.backend
	h.mu.Unlock()
	if backend == nil {
		return fmt.Errorf("browser not started")
	}
	return backend.Eval(js)
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
		backend := h.backend
		h.backend = nil
		h.mu.Unlock()
		if backend != nil {
			backend.Close()
		}
	}()
	err := srv.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
