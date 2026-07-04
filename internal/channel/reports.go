// reports.go is the RUN-COMPLETION breadcrumb lane: the raw material for the
// routines courier (docs/ROUTINES.md "Reporting upward"). Exec executor panes
// die the instant their turn ends, so a tick-time rollout scan misses fast
// runs entirely — instead the codex notify hook (HandleNotify), which fires AT
// the completion instant with the last message in hand, appends one breadcrumb
// per completed run here. The tick drains each workspace's file and delivers
// ONE batched digest to the manager. Same file discipline as the publish
// spool: hub-local, one JSON line per event, O_APPEND writes, rename-aside
// drain.
package channel

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// RunReport is one completed routine run: the breadcrumb the notify hook
// leaves for the courier.
type RunReport struct {
	Routine string    `json:"routine"`
	Message string    `json:"message"` // last assistant message (may be multiline)
	At      time.Time `json:"at"`
}

func reportsDir() (string, error) {
	d, err := duckHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "routines-reports"), nil
}

func reportsPath(ws string) (string, error) {
	if err := validWorkspace(ws); err != nil {
		return "", err
	}
	d, err := reportsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, ws+".jsonl"), nil
}

// ReportRun appends one completion breadcrumb for the workspace.
func ReportRun(ws string, r RunReport) error {
	p, err := reportsPath(ws)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// DrainReports atomically takes all pending breadcrumbs for the workspace
// (rename-aside, so a concurrent ReportRun lands in a fresh file and is never
// lost). Missing file → nil, nil.
func DrainReports(ws string) ([]RunReport, error) {
	p, err := reportsPath(ws)
	if err != nil {
		return nil, err
	}
	tmp := p + ".draining"
	if err := os.Rename(p, tmp); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer os.Remove(tmp)
	f, err := os.Open(tmp)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []RunReport
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		var r RunReport
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.Routine != "" {
			out = append(out, r)
		}
	}
	return out, sc.Err()
}

// ReportWorkspaces lists workspaces with pending breadcrumbs.
func ReportWorkspaces() ([]string, error) {
	d, err := reportsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(d)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if name := e.Name(); filepath.Ext(name) == ".jsonl" {
			out = append(out, name[:len(name)-len(".jsonl")])
		}
	}
	return out, nil
}
