// worker.go runs ONE disposable executor: a headless `codex exec` process.
// Workers never touch tmux — stdout (--json JSONL) streams to the run dir,
// the final message lands via -o, and token usage is summed live from
// turn.completed events so the status file counts while workers run.
//
// Structured output: --output-schema is passed when a schema is set (the
// default OpenAI provider enforces it), but cross-provider profiles (DeepSeek
// via Moon Bridge) silently ignore it — validated empirically. So the schema
// is ALSO inlined into the prompt, the reply is parsed+validated here, and a
// bad reply gets up to two `codex exec resume` nudges before giving up.
package workflow

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/DigiBugCat/duck/internal/agent"
)

// codexBin is the executor binary — a var so tests substitute a stub.
var codexBin = "codex"

// DefaultModel is the worker tier when an agent() call names none: the codex
// gpt default. Scripts opt down to cheaper gpt tiers (e.g. gpt-5.4-mini) per
// call when mechanical work warrants it.
const DefaultModel = "gpt-5.5"

// schemaRetries is how many resume-nudges a schema mismatch gets.
const schemaRetries = 2

// workerOpts are the per-agent() knobs (JSON tags double as the cache-key
// canonical form — see callKey).
type workerOpts struct {
	Label  string          `json:"label,omitempty"`
	Phase  string          `json:"phase,omitempty"`
	Model  string          `json:"model,omitempty"`
	Effort string          `json:"effort,omitempty"`
	Cwd    string          `json:"cwd,omitempty"`
	Write  bool            `json:"write,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
}

// workerResult is one finished worker: the value agent() resolves to (a
// parsed object with a schema, the final message text without), plus
// accounting.
type workerResult struct {
	Value     any
	Tokens    int64
	SessionID string
}

// runWorker executes one worker to completion. seq names its files under
// <runDir>/agents/. progress receives live signal as the event stream drains:
// token deltas as turns complete, and "last activity" snippets (a message, or
// the command being run) for the status file's per-agent lines.
func runWorker(ctx context.Context, runDir string, seq int64, prompt string, o workerOpts, progress func(tokens int64, last string)) (workerResult, error) {
	agentsDir := filepath.Join(runDir, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return workerResult{}, err
	}
	base := filepath.Join(agentsDir, fmt.Sprintf("%d", seq))

	schemaPath := ""
	if len(o.Schema) > 0 {
		schemaPath = base + ".schema.json"
		if err := os.WriteFile(schemaPath, o.Schema, 0o644); err != nil {
			return workerResult{}, err
		}
		prompt += "\n\nRespond with ONLY a single JSON object matching this JSON Schema — no prose, no code fences:\n" + string(o.Schema)
	}

	res := workerResult{}
	lastMsg, sessionID, err := execOnce(ctx, base, schemaPath, o, nil, prompt, &res, progress)
	if err != nil {
		return res, err
	}
	res.SessionID = sessionID

	if schemaPath == "" {
		res.Value = strings.TrimSpace(lastMsg)
		return res, nil
	}
	// Schema path: parse, nudge on failure. The nudge resumes the SAME codex
	// session so the model sees its own bad reply.
	for attempt := 0; ; attempt++ {
		if v, perr := parseJSONReply(lastMsg); perr == nil {
			res.Value = v
			return res, nil
		}
		if attempt >= schemaRetries || sessionID == "" {
			return res, fmt.Errorf("reply did not match the schema after %d attempts", attempt+1)
		}
		nudge := "Your last reply was not a valid JSON object for the required schema. Respond again with ONLY the JSON object — no prose, no code fences.\nSchema:\n" + string(o.Schema)
		lastMsg, _, err = execOnce(ctx, fmt.Sprintf("%s.retry%d", base, attempt+1), schemaPath, o, &sessionID, nudge, &res, progress)
		if err != nil {
			return res, err
		}
	}
}

// execOnce runs a single codex exec (fresh, or resuming resume's session) and
// returns the final message. Token usage accumulates into res and addTokens.
func execOnce(ctx context.Context, base, schemaPath string, o workerOpts, resume *string, prompt string, res *workerResult, progress func(int64, string)) (lastMsg, sessionID string, err error) {
	lastPath := base + ".last.txt"
	argv := []string{codexBin, "exec", "--json", "--skip-git-repo-check", "--color", "never", "-o", lastPath}
	sandbox := "read-only"
	if o.Write {
		sandbox = "workspace-write"
	}
	argv = append(argv, "-s", sandbox)
	if schemaPath != "" {
		argv = append(argv, "--output-schema", schemaPath)
	}
	model := o.Model
	if model == "" {
		model = DefaultModel
	}
	// WithModel keys on argv[0]=="codex"; inject against the canonical name,
	// then restore the (possibly stubbed) binary.
	injected, err := agent.WithModel(append([]string{"codex"}, argv[1:]...), model, o.Effort)
	if err != nil {
		return "", "", err
	}
	argv = append([]string{codexBin}, injected[1:]...)
	if resume != nil {
		argv = append(argv, "resume", *resume)
	}
	argv = append(argv, prompt)

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if o.Cwd != "" {
		cmd.Dir = o.Cwd
	}
	// Own process group: Stop kills the ENGINE's group, and the engine puts
	// workers in it by default — but CommandContext's kill only reaches the
	// direct child, so group the worker with any grandchildren it spawns.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

	errLog, err := os.OpenFile(base+".err.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", "", err
	}
	defer errLog.Close()
	cmd.Stderr = errLog

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", err
	}
	if err := cmd.Start(); err != nil {
		return "", "", err
	}

	stream, err := os.OpenFile(base+".jsonl", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		defer stream.Close()
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	start := time.Now()
	for sc.Scan() {
		line := sc.Bytes()
		if stream != nil {
			_, _ = stream.Write(append(append([]byte{}, line...), '\n'))
		}
		var ev struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Usage    struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
			Item struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Command string `json:"command"`
				Query   string `json:"query"`
			} `json:"item"`
		}
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		switch ev.Type {
		case "thread.started":
			sessionID = ev.ThreadID
		case "turn.completed":
			d := ev.Usage.InputTokens + ev.Usage.OutputTokens
			res.Tokens += d
			if progress != nil {
				progress(d, "")
			}
		case "item.completed", "item.started":
			if last := lastActivity(ev.Item.Type, ev.Item.Text, ev.Item.Command, ev.Item.Query); last != "" && progress != nil {
				progress(0, last)
			}
		}
	}
	werr := cmd.Wait()
	if ctx.Err() != nil {
		return "", sessionID, ctx.Err()
	}
	if werr != nil {
		return "", sessionID, fmt.Errorf("codex exec failed after %s: %w", time.Since(start).Round(time.Second), werr)
	}
	data, rerr := os.ReadFile(lastPath)
	if rerr != nil {
		return "", sessionID, fmt.Errorf("worker finished but wrote no final message: %w", rerr)
	}
	return string(data), sessionID, nil
}

// lastActivity renders one worker event as a one-line "what it's doing now"
// snippet for the status file. Messages beat commands beat nothing; reasoning
// and file plumbing are noise at this altitude.
func lastActivity(itemType, text, command, query string) string {
	var s string
	switch itemType {
	case "agent_message":
		s = text
	case "command_execution":
		s = "$ " + command
	case "web_search":
		if query == "" {
			return ""
		}
		s = "🔎 " + query
	default:
		return ""
	}
	s = strings.Join(strings.Fields(s), " ") // collapse newlines/runs
	if len(s) > 160 {
		s = strings.ToValidUTF8(s[:160], "") + "…"
	}
	return s
}

// parseJSONReply extracts a JSON value from a model reply, tolerating code
// fences and surrounding prose (first { or [ to last } or ]).
func parseJSONReply(s string) (any, error) {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "{["); i >= 0 {
		if j := strings.LastIndexAny(s, "}]"); j > i {
			s = s[i : j+1]
		}
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, err
	}
	return v, nil
}
