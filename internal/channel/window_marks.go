package channel

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/DigiBugCat/duck/internal/config"
	"github.com/DigiBugCat/duck/internal/window"
)

var (
	windowMarksHost       = defaultWindowMarksHost
	windowMarksHTTPClient = &http.Client{Timeout: 500 * time.Millisecond}
)

func defaultWindowMarksHost() string {
	if spoolHome != "" {
		return ""
	}
	if cfg, err := config.Load(); err == nil && cfg.WindowHost != "" {
		return cfg.WindowHost
	}
	return ""
}

func windowMarkCursorPath(workspace string) (string, error) {
	if err := validWorkspace(workspace); err != nil {
		return "", err
	}
	d, err := spoolDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "window-marks", workspace+".cursor"), nil
}

func readWindowMarkCursor(workspace string) int {
	p, err := windowMarkCursorPath(workspace)
	if err != nil {
		return 0
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func writeWindowMarkCursor(workspace string, n int) {
	p, err := windowMarkCursorPath(workspace)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(p, []byte(strconv.Itoa(n)+"\n"), 0o644)
}

func fetchWindowMarks(workspace string) ([]json.RawMessage, error) {
	host := strings.TrimSpace(windowMarksHost())
	if host == "" {
		return nil, nil
	}
	u := url.URL{Scheme: "http", Host: host, Path: "/marks"}
	q := u.Query()
	q.Set("workspace", workspace)
	u.RawQuery = q.Encode()
	resp, err := windowMarksHTTPClient.Get(u.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("window marks: %s", resp.Status)
	}
	var marks []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&marks); err != nil {
		return nil, err
	}
	return marks, nil
}

func (s *server) drainWindowMarks(workspace string) {
	if workspace == "" {
		return
	}
	marks, err := fetchWindowMarks(workspace)
	if err != nil || len(marks) == 0 {
		return
	}
	cursor := readWindowMarkCursor(workspace)
	if cursor > len(marks) {
		cursor = len(marks)
	}
	for i := cursor; i < len(marks); i++ {
		var m window.Mark
		if err := json.Unmarshal(marks[i], &m); err != nil {
			continue
		}
		s.emitWindowMark(workspace, m, marks[i])
	}
	writeWindowMarkCursor(workspace, len(marks))
}

func (s *server) emitWindowMark(workspace string, m window.Mark, raw json.RawMessage) {
	meta := map[string]any{
		"session": workspace,
		"source":  "duck-window",
		"type":    "mark",
	}
	s.write(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/claude/channel",
		"params": map[string]any{
			"source":  "duck-window",
			"content": windowMarkMessage(m),
			"meta":    meta,
			"attachments": []any{map[string]any{
				"type":    "json",
				"name":    "mark",
				"content": json.RawMessage(raw),
			}},
		},
	})
}

func windowMarkMessage(m window.Mark) string {
	typ := m.Type
	if typ == "" {
		typ = "highlight"
	}
	parts := []string{"mark " + typ}
	if m.Comment != "" {
		parts = append(parts, "note: "+m.Comment)
	}
	if m.Text != "" {
		parts = append(parts, "text: "+quoteShort(m.Text, 180))
	}
	if m.Rect != nil {
		parts = append(parts, fmt.Sprintf("rect: %.0f,%.0f %.0fx%.0f", m.Rect.X, m.Rect.Y, m.Rect.W, m.Rect.H))
	}
	if m.Shot != "" {
		parts = append(parts, "shot: "+m.Shot)
	}
	if m.URL != "" {
		parts = append(parts, "url: "+m.URL)
	}
	return strings.Join(parts, "; ")
}

func quoteShort(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = strings.ToValidUTF8(s[:max], "") + "..."
	}
	return strconv.Quote(s)
}
