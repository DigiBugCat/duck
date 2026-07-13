// run.go is the engine: one goroutine owns a goja VM running the script
// (wrapped in an async IIFE — goja has no top-level await), worker goroutines
// run codex processes, and a completion channel marshals their results back
// onto the VM goroutine as promise resolutions. goja Runtimes are not
// goroutine-safe, so EVERY VM touch (bindings, resolve/reject, export)
// happens on the engine goroutine; workers only send closures.
package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
)

// prelude defines the script-side orchestration helpers on top of the
// Go-bound agent(). pipeline() has NO barrier between stages (item A can be
// in stage 3 while item B is in stage 1); parallel() is the barrier. A stage
// that throws — or an agent that errored (null) — drops its item to null and
// skips the rest of its chain.
const prelude = `
globalThis.parallel = (thunks) => Promise.all(thunks.map(t =>
	Promise.resolve().then(t).then(v => v === undefined ? null : v)
		.catch(e => { __scriptError(String(e && e.stack || e)); return null; })));
globalThis.pipeline = (items, ...stages) => Promise.all(items.map(async (item, i) => {
	let r = item;
	for (const s of stages) {
		try { r = await s(r, item, i); } catch (e) { __scriptError(String(e && e.stack || e)); return null; }
		if (r === null || r === undefined) return null;
	}
	return r;
}));
`

// Run is one prepared workflow run: everything the engine needs, all on disk
// so a detached executor process can Load and Execute it.
type Run struct {
	ID     string
	Dir    string
	Script string
	Meta   Meta
	Opts   Opts
}

// Prepare validates the script, mints the run dir, and persists the run's
// inputs. The run is now visible (state=starting) but nothing executes until
// Execute — the caller picks foreground or detached.
func Prepare(script string, o Opts) (*Run, error) {
	meta, err := ExtractMeta(script)
	if err != nil {
		return nil, err
	}
	if o.Name != "" {
		if o.Name = sanitizeName(o.Name); o.Name == "" {
			return nil, fmt.Errorf("invalid run name")
		}
		meta.Name = o.Name
	}
	if o.ResumeFrom != "" {
		if err := validRunID(o.ResumeFrom); err != nil {
			return nil, err
		}
	}
	id := NewRunID()
	dir, err := RunDir(id)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	optsJSON, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "script.js"), []byte(script), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "opts.json"), optsJSON, 0o644); err != nil {
		return nil, err
	}
	now := time.Now()
	st := Status{RunID: id, Name: meta.Name, Description: meta.Description, Workspace: o.Workspace, State: StateStarting, Started: now, Updated: now}
	if err := writeStatus(dir, st); err != nil {
		return nil, err
	}
	return &Run{ID: id, Dir: dir, Script: script, Meta: meta, Opts: o}, nil
}

// Load reconstructs a prepared run from its dir (the detached executor's
// entry point).
func Load(runID string) (*Run, error) {
	dir, err := RunDir(runID)
	if err != nil {
		return nil, err
	}
	script, err := os.ReadFile(filepath.Join(dir, "script.js"))
	if err != nil {
		return nil, err
	}
	optsB, err := os.ReadFile(filepath.Join(dir, "opts.json"))
	if err != nil {
		return nil, err
	}
	var o Opts
	if err := json.Unmarshal(optsB, &o); err != nil {
		return nil, err
	}
	meta, err := ExtractMeta(string(script))
	if err != nil {
		return nil, err
	}
	if o.Name != "" {
		meta.Name = o.Name
	}
	return &Run{ID: runID, Dir: dir, Script: string(script), Meta: meta, Opts: o}, nil
}

// engine is the live state of an executing run.
type engine struct {
	run    *Run
	vm     *goja.Runtime
	done   chan func()  // worker completions → VM-thread closures
	inWork atomic.Int64 // outstanding completions the loop still owes the VM
	tokens atomic.Int64 // live usage across workers
	seq    atomic.Int64 // agent counter (file names, journal seq, maxAgents)
	cache  *replayCache // nil unless resuming
	jr     *journal
	logf   *os.File
	ctx    context.Context
	cancel context.CancelFunc

	st      Status // engine-goroutine + status closures; guarded by stMu
	stMu    chMutex
	stDirty atomic.Bool
	slots   chan struct{} // worker slot pool (lazily sized in sem)
}

// chMutex is a tiny channel-based mutex so worker goroutines can bump status
// counters without a sync.Mutex sprawl.
type chMutex chan struct{}

func (m chMutex) lock()   { m <- struct{}{} }
func (m chMutex) unlock() { <-m }

// Execute runs the script to completion, maintaining status.json/journal/
// run.log throughout, then writes result.json and publishes the completion
// digest. Blocking; the caller decides fore/background.
func (r *Run) Execute(ctx context.Context) error {
	e := &engine{run: r, done: make(chan func(), 64), stMu: make(chMutex, 1)}
	e.ctx, e.cancel = context.WithCancel(ctx)
	defer e.cancel()

	var err error
	if e.jr, err = openJournal(r.Dir); err != nil {
		return err
	}
	defer e.jr.close()
	if e.logf, err = os.OpenFile(filepath.Join(r.Dir, "run.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err != nil {
		return err
	}
	defer e.logf.Close()
	if r.Opts.ResumeFrom != "" {
		if e.cache, err = loadReplayCache(r.Opts.ResumeFrom); err != nil {
			return e.fail(fmt.Errorf("load resume journal %s: %w", r.Opts.ResumeFrom, err))
		}
		e.logline("resuming from %s (%d cached keys)", r.Opts.ResumeFrom, len(e.cache.m))
	}

	prior, _ := ReadStatus(r.ID)
	e.st = prior
	if e.st.RunID == "" {
		e.st = Status{RunID: r.ID, Name: r.Meta.Name, Workspace: r.Opts.Workspace, Started: time.Now()}
	}
	e.st.Description = r.Meta.Description
	e.st.State = StateRunning
	e.st.PID = os.Getpid()
	e.flushStatus()

	value, runErr := e.runScript()
	switch {
	case runErr == nil:
		b, merr := json.MarshalIndent(value, "", "  ")
		if merr != nil {
			b = []byte(fmt.Sprintf("%q", fmt.Sprint(value)))
		}
		if werr := os.WriteFile(filepath.Join(r.Dir, "result.json"), b, 0o644); werr != nil {
			return e.fail(werr)
		}
		e.setState(StateDone, "")
		return nil
	case e.ctx.Err() != nil && ctx.Err() != nil:
		e.setState(StateStopped, "stopped")
		return e.ctx.Err()
	default:
		return e.fail(runErr)
	}
}

func (e *engine) fail(err error) error {
	e.logline("FATAL: %v", err)
	e.setState(StateError, err.Error())
	return err
}

// runScript sets up the VM, kicks off the async IIFE, and pumps completions
// until the script's promise settles.
func (e *engine) runScript() (any, error) {
	e.vm = goja.New()
	if err := e.bind(); err != nil {
		return nil, err
	}
	src := strings.Replace(e.run.Script, "export const meta", "const meta", 1)
	wrapped := "(async () => {\n" + src + "\n})()"
	v, err := e.vm.RunScript(e.run.ID+".js", wrapped)
	if err != nil {
		return nil, fmt.Errorf("script error: %w", err)
	}
	p, ok := v.Export().(*goja.Promise)
	if !ok {
		return nil, fmt.Errorf("internal: script wrapper did not yield a promise")
	}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for p.State() == goja.PromiseStatePending {
		if e.inWork.Load() == 0 {
			select {
			case f := <-e.done:
				f()
				continue
			default:
				return nil, fmt.Errorf("script is awaiting something that can never resolve (no workers in flight)")
			}
		}
		select {
		case f := <-e.done:
			f()
		case <-tick.C:
			e.flushStatus()
		case <-e.ctx.Done():
			return nil, e.ctx.Err()
		}
	}
	if p.State() == goja.PromiseStateRejected {
		return nil, fmt.Errorf("script rejected: %s", stringify(p.Result()))
	}
	return p.Result().Export(), nil
}

// bind installs the script surface: agent, phase, log, args, budget, and the
// JS prelude (parallel/pipeline).
func (e *engine) bind() error {
	vm := e.vm
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	must(vm.Set("agent", e.jsAgent))
	must(vm.Set("log", func(msg string) {
		e.logline("log: %s", msg)
	}))
	must(vm.Set("phase", func(title string) {
		e.logline("phase: %s", title)
		e.mutate(func(s *Status) { s.Phase = title })
		e.flushStatus()
	}))
	must(vm.Set("__scriptError", func(msg string) {
		e.logline("stage error: %s", msg)
	}))

	var args any
	if len(e.run.Opts.Args) > 0 {
		if err := json.Unmarshal(e.run.Opts.Args, &args); err != nil {
			return fmt.Errorf("opts.args is not valid JSON: %w", err)
		}
	}
	must(vm.Set("args", args))

	budget := vm.NewObject()
	total := e.run.Opts.Budget
	if total > 0 {
		must(budget.Set("total", total))
	} else {
		must(budget.Set("total", goja.Null()))
	}
	must(budget.Set("spent", func() int64 { return e.tokens.Load() }))
	must(budget.Set("remaining", func() goja.Value {
		if total <= 0 {
			return vm.ToValue(float64(1e308)) // effectively Infinity
		}
		r := total - e.tokens.Load()
		if r < 0 {
			r = 0
		}
		return vm.ToValue(r)
	}))
	must(vm.Set("budget", budget))

	_, err := vm.RunString(prelude)
	return err
}

// jsAgent is the agent(prompt, opts?) binding — VM goroutine only. It
// returns a promise and hands the work to a goroutine; resolution comes back
// through e.done. Terminal worker errors resolve to null (callers filter),
// matching the design; only budget exhaustion and the agent cap throw.
func (e *engine) jsAgent(call goja.FunctionCall) goja.Value {
	vm := e.vm
	prompt := call.Argument(0).String()
	var o workerOpts
	if arg := call.Argument(1); !goja.IsUndefined(arg) && !goja.IsNull(arg) {
		raw, err := json.Marshal(arg.Export())
		if err == nil {
			err = json.Unmarshal(raw, &o)
		}
		if err != nil {
			panic(vm.ToValue("agent(): bad opts: " + err.Error()))
		}
	}
	if o.Cwd == "" {
		o.Cwd = e.run.Opts.Dir
	}
	if total := e.run.Opts.Budget; total > 0 && e.tokens.Load() >= total {
		panic(vm.ToValue(fmt.Sprintf("token budget exhausted (%d spent of %d)", e.tokens.Load(), total)))
	}
	seq := e.seq.Add(1)
	if seq > maxAgents {
		panic(vm.ToValue(fmt.Sprintf("agent cap reached (%d) — runaway loop?", maxAgents)))
	}

	promise, resolve, _ := vm.NewPromise()
	key := callKey(prompt, o)
	label := o.Label
	if label == "" {
		label = fmt.Sprintf("agent-%d", seq)
	}

	if cached, ok := e.cache.pop(key); ok {
		var v any
		_ = json.Unmarshal(cached, &v)
		e.mutate(func(s *Status) { s.AgentsTotal++; s.AgentsDone++; s.AgentsCached++ })
		e.logline("%s: cached (resume)", label)
		e.inWork.Add(1)
		go func() { e.done <- func() { e.inWork.Add(-1); _ = resolve(vm.ToValue(v)) } }()
		return vm.ToValue(promise)
	}

	e.mutate(func(s *Status) { s.AgentsTotal++; s.AgentsRunning++ })
	e.inWork.Add(1)
	go func() {
		e.sem() <- struct{}{}
		defer func() { <-e.sem() }()
		start := time.Now()
		// Register this worker's live line (visible in tail/roster progress);
		// dropped again when it finishes. Registration happens here — after
		// the semaphore — so Agents shows workers actually executing, not the
		// whole queued backlog.
		e.mutate(func(s *Status) {
			s.Agents = append(s.Agents, AgentLive{Seq: seq, Label: label, Started: start})
		})
		defer e.mutate(func(s *Status) {
			for i := range s.Agents {
				if s.Agents[i].Seq == seq {
					s.Agents = append(s.Agents[:i], s.Agents[i+1:]...)
					break
				}
			}
		})
		res, err := runWorker(e.ctx, e.run.Dir, seq, prompt, o, func(d int64, last string) {
			if d != 0 {
				e.tokens.Add(d)
			}
			e.mutate(func(s *Status) {
				s.Tokens = e.tokens.Load()
				for i := range s.Agents {
					if s.Agents[i].Seq == seq {
						s.Agents[i].Tokens += d
						if last != "" {
							s.Agents[i].Last = last
						}
						break
					}
				}
			})
		})
		entry := journalEntry{Seq: seq, Key: key, Label: label, Tokens: res.Tokens, SessionID: res.SessionID, ElapsedMs: time.Since(start).Milliseconds()}
		if err != nil {
			entry.Err = err.Error()
		} else if b, merr := json.Marshal(res.Value); merr == nil {
			entry.Result = b
		}
		e.jr.append(entry)
		e.mutate(func(s *Status) {
			s.AgentsRunning--
			s.AgentsDone++
			if err != nil {
				s.AgentsFailed++
			}
		})
		value := res.Value
		e.done <- func() {
			e.inWork.Add(-1)
			if err != nil {
				e.logline("%s: FAILED: %v", label, err)
				_ = resolve(goja.Null())
				return
			}
			e.logline("%s: done (%s tokens, %s)", label, HumanTokens(res.Tokens), time.Since(start).Round(time.Second))
			_ = resolve(vm.ToValue(value))
		}
	}()
	return vm.ToValue(promise)
}

// sem lazily builds the worker slot pool.
func (e *engine) sem() chan struct{} {
	e.stMu.lock()
	defer e.stMu.unlock()
	if e.slots == nil {
		n := e.run.Opts.Concurrency
		if n <= 0 {
			n = DefaultConcurrency
		}
		e.slots = make(chan struct{}, n)
	}
	return e.slots
}

// mutate applies a status change and marks it dirty; the engine loop's tick
// (or an explicit flushStatus) persists it.
func (e *engine) mutate(f func(*Status)) {
	e.stMu.lock()
	f(&e.st)
	e.st.Updated = time.Now()
	e.stMu.unlock()
	e.stDirty.Store(true)
}

func (e *engine) flushStatus() {
	e.stMu.lock()
	s := e.st
	s.Updated = time.Now()
	e.stMu.unlock()
	e.stDirty.Store(false)
	_ = writeStatus(e.run.Dir, s)
}

func (e *engine) setState(state, errMsg string) {
	e.mutate(func(s *Status) {
		s.State = state
		s.Error = errMsg
		s.Phase = ""
		s.Agents = nil // terminal: no live workers, whatever the race
		s.Tokens = e.tokens.Load()
	})
	e.flushStatus()
}

// logline appends a timestamped line to run.log.
func (e *engine) logline(format string, a ...any) {
	fmt.Fprintf(e.logf, "%s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, a...))
}

func stringify(v goja.Value) string {
	if v == nil {
		return "<nil>"
	}
	return v.String()
}
