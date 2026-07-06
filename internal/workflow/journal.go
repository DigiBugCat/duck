// journal.go is the run's resume cache: one JSON line per completed agent()
// call, keyed by a hash of (prompt, opts). Resuming a run seeds a cache from
// the prior journal; an agent() call whose key matches pops a cached result
// instead of running a worker. Keys are content hashes (not call order), so
// nondeterministic scheduling or an edited script just degrades to cache
// misses — never wrong results. Duplicate identical calls are served FIFO
// from a per-key queue, matching how many times the prior run made them.
package workflow

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// journalEntry records one completed agent() call. Result is the worker's
// value (JSON); Err is set instead when the call failed (failed calls are
// journaled for post-mortems but never served from cache — a resume retries
// them live).
type journalEntry struct {
	Seq       int64           `json:"seq"`
	Key       string          `json:"key"`
	Label     string          `json:"label,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Err       string          `json:"err,omitempty"`
	Tokens    int64           `json:"tokens"`
	SessionID string          `json:"session_id,omitempty"`
	ElapsedMs int64           `json:"elapsed_ms"`
}

// callKey hashes what identifies an agent() call for caching.
func callKey(prompt string, opts workerOpts) string {
	b, _ := json.Marshal(struct {
		Prompt string
		Opts   workerOpts
	}{prompt, opts})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}

// journal is the append side, owned by one run.
type journal struct {
	mu sync.Mutex
	f  *os.File
}

func openJournal(runDir string) (*journal, error) {
	f, err := os.OpenFile(filepath.Join(runDir, "journal.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &journal{f: f}, nil
}

func (j *journal) append(e journalEntry) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	_, _ = j.f.Write(append(b, '\n'))
}

func (j *journal) close() { _ = j.f.Close() }

// replayCache holds a prior run's successful results, popped per key.
type replayCache struct {
	mu sync.Mutex
	m  map[string][]json.RawMessage
}

// loadReplayCache reads a prior run's journal. A missing journal is an empty
// cache, not an error (the prior run may have died before its first call).
func loadReplayCache(priorRunID string) (*replayCache, error) {
	c := &replayCache{m: map[string][]json.RawMessage{}}
	dir, err := RunDir(priorRunID)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(dir, "journal.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		var e journalEntry
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Err != "" || e.Key == "" {
			continue
		}
		c.m[e.Key] = append(c.m[e.Key], e.Result)
	}
	return c, sc.Err()
}

// pop takes one cached result for key, FIFO. ok=false on miss.
func (c *replayCache) pop(key string) (json.RawMessage, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	q := c.m[key]
	if len(q) == 0 {
		return nil, false
	}
	c.m[key] = q[1:]
	return q[0], true
}
