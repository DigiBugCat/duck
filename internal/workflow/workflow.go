// Package workflow is duck's deterministic multi-agent orchestration engine
// (DESIGN: docs/WORKFLOWS.md): the manager writes a small JS script whose
// control flow (loops, pipelines, barriers) is plain code, and each agent()
// call inside it runs ONE disposable headless executor (`codex exec`,
// the codex gpt default). Workers are processes, not panes — they never
// enter the sidebar lot, the ledger, or the org chart. The RUN is the
// addressable thing: it owns a run dir with the script, a journal (resume),
// a status file (roster/CLI visibility), and the result.
//
// Run dirs live under <duck-home>/workflows/<run-id>/:
//
//	script.js        the script as submitted (after `export ` normalization)
//	opts.json        run options (workspace, args, budget, …)
//	status.json      live snapshot: state, phase, agent counts, tokens (atomic rewrites)
//	journal.jsonl    one line per completed agent() call — the resume cache
//	result.json      the script's return value (state=done)
//	run.log          phase()/log() lines + engine notes
//	agents/<n>.*     per-worker: event stream (.jsonl), last message (.last.txt), stderr (.err.log)
package workflow

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// States a run moves through. starting is the window between Prepare and the
// engine's first status write (how a detached launch that never came up is
// distinguished from a live run).
const (
	StateStarting = "starting"
	StateRunning  = "running"
	StateDone     = "done"
	StateError    = "error"
	StateStopped  = "stopped"
)

// Status is the live snapshot the roster and CLI read. Rewritten atomically
// (tmp+rename) by the engine on every state change; consumers just parse the
// file — there is no daemon to ask.
type Status struct {
	RunID       string    `json:"run_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"` // meta.description: what this run is for
	Workspace   string    `json:"workspace,omitempty"`
	State       string    `json:"state"`
	Phase       string    `json:"phase,omitempty"`
	PID         int       `json:"pid,omitempty"`
	Started     time.Time `json:"started"`
	Updated     time.Time `json:"updated"`

	// Agents is the LIVE workers only (bounded by the concurrency cap, so
	// status.json stays small at any fleet size): what each is doing right
	// now, for the tail/progress views.
	Agents []AgentLive `json:"agents,omitempty"`

	AgentsTotal   int   `json:"agents_total"`   // agent() calls dispatched (incl. running)
	AgentsRunning int   `json:"agents_running"` // currently executing workers
	AgentsDone    int   `json:"agents_done"`    // finished (ok or failed)
	AgentsFailed  int   `json:"agents_failed"`  // finished with an error (resolved to null)
	AgentsCached  int   `json:"agents_cached"`  // served from a resumed run's journal
	Tokens        int64 `json:"tokens"`         // input+output across all workers

	Error string `json:"error,omitempty"` // state=error: what killed the run
}

// AgentLive is one running worker's live line in Status: its label, spend,
// and the last thing it did (a message snippet or the command it ran).
type AgentLive struct {
	Seq     int64     `json:"seq"`
	Label   string    `json:"label"`
	Tokens  int64     `json:"tokens"`
	Started time.Time `json:"started"`
	Last    string    `json:"last,omitempty"`
}

// Opts are a run's inputs, persisted to opts.json so the detached executor
// process reconstructs the exact run its parent prepared.
type Opts struct {
	Name        string          `json:"name,omitempty"` // override; else meta.name
	Workspace   string          `json:"workspace,omitempty"`
	Dir         string          `json:"dir,omitempty"`  // default worker cwd
	Args        json.RawMessage `json:"args,omitempty"` // the script's `args` global
	Budget      int64           `json:"budget,omitempty"`
	Concurrency int             `json:"concurrency,omitempty"`
	ResumeFrom  string          `json:"resume_from,omitempty"` // prior run id whose journal seeds the cache
}

// DefaultConcurrency bounds simultaneous workers. Workers are light headless
// processes — the real ceiling is provider throughput (Moon Bridge / OpenAI
// rate limits), not local cores, so this is deliberately far above the
// subagent-style min(16, cores) rule. Per-run override via Opts.Concurrency.
const DefaultConcurrency = 64

// maxAgents is the runaway-loop backstop, far above any real workflow.
const maxAgents = 1000

// home resolves the duck home dir ($DUCK_HOME test seam, else ~/.duck) —
// mirrors internal/routines and the channel spool.
func home() (string, error) {
	if d := os.Getenv("DUCK_HOME"); d != "" {
		return d, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".duck"), nil
}

// BaseDir is <duck-home>/workflows, the parent of every run dir.
func BaseDir() (string, error) {
	d, err := home()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "workflows"), nil
}

// NewRunID mints a wf_ handle: sortable timestamp + random suffix.
func NewRunID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("wf_%s-%s", time.Now().UTC().Format("20060102-150405"), hex.EncodeToString(b[:]))
}

// validRunID gates path joins the same way validWorkspace does for spools: a
// run id is ours to mint, so anything with a separator is an attack or a bug.
func validRunID(id string) error {
	if id == "" || strings.ContainsAny(id, "/\\") || id == "." || id == ".." || !strings.HasPrefix(id, "wf_") {
		return fmt.Errorf("invalid workflow run id %q", id)
	}
	return nil
}

// RunDir is the run's directory (not created).
func RunDir(runID string) (string, error) {
	if err := validRunID(runID); err != nil {
		return "", err
	}
	base, err := BaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, runID), nil
}

// ReadStatus parses a run's status.json.
func ReadStatus(runID string) (Status, error) {
	dir, err := RunDir(runID)
	if err != nil {
		return Status{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "status.json"))
	if err != nil {
		return Status{}, err
	}
	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		return Status{}, fmt.Errorf("corrupt status for %s: %w", runID, err)
	}
	return s, nil
}

// writeStatus atomically rewrites status.json (tmp+rename, so a reader never
// sees a torn file).
func writeStatus(dir string, s Status) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".status.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "status.json"))
}

// List returns every run's status, newest first (run ids sort by mint time).
// workspace filters when non-empty. Corrupt/foreign dirs are skipped.
func List(workspace string) ([]Status, error) {
	base, err := BaseDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Status
	for _, e := range entries {
		if !e.IsDir() || validRunID(e.Name()) != nil {
			continue
		}
		s, err := ReadStatus(e.Name())
		if err != nil {
			continue
		}
		if workspace != "" && s.Workspace != workspace {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RunID > out[j].RunID })
	return out, nil
}

// Alive reports whether the run's recorded engine process still exists — how
// a consumer tells a live "running" from one orphaned by a crash/reboot.
func (s Status) Alive() bool {
	if s.PID <= 0 {
		return false
	}
	// Signal 0 probes existence without touching the process.
	return syscallKill(s.PID, 0) == nil
}

// HumanTokens renders a token count the way the roster shows it (1.4M, 320k).
func HumanTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%dk", n/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// Elapsed is the run's wall clock: to now while live, frozen at the final
// status write once terminal.
func (s Status) Elapsed() time.Duration {
	end := time.Now()
	if s.State != StateRunning && s.State != StateStarting {
		end = s.Updated
	}
	return end.Sub(s.Started).Round(time.Second)
}
