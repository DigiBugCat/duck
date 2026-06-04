// Package namer turns a captured terminal snapshot into a short human title for
// a session. It is the pluggable, sticky naming engine behind DESIGN §5.
//
// The default codexExec implementation captures the HEAD of a session's pane on
// the hub over SSH (`tmux capture-pane -p -S - -t <id> | head -n 200 |
// head -c 8000`) and pipes it into `codex exec` running LOCALLY on the laptop
// (the verified install; also keeps names.json single-writer). Capturing the
// head — not the tail — means the name reflects the session's opening intent
// and then sticks; a content hash gates regeneration so a frozen name is reused
// until the head changes materially or the user forces a re-name.
//
// dirDerived is the floor: duck never hard-depends on codex. Any codex error or
// empty title degrades to the dir-derived name, and naming never blocks the
// picker.
package namer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/DigiBugCat/duck/internal/names"
	"github.com/DigiBugCat/duck/internal/paths"
)

// Namer returns a short human title for a session given a captured terminal
// snapshot. It is the one interface the flow/app layer depends on, so the codex
// namer and the dir-derived fallback are interchangeable.
type Namer interface {
	Name(ctx context.Context, snapshot string) (string, error)
}

// Capturer reads the head snapshot of a session's pane. CodexExec satisfies it
// (over ssh); DirDerived returns an empty snapshot since it ignores it. The app
// layer depends on it so NameNow can capture-then-name-then-freeze.
type Capturer interface {
	CaptureHead(id string) (string, error)
}

// Runner is the injectable SSH seam (subset of *sshx.Client) used to capture
// the pane head on the hub. Tests swap a fake asserting the capture-pane
// command string.
type Runner interface {
	Run(remoteCmd string) (string, error)
	RunInput(remoteCmd string, stdin io.Reader) (string, error)
}

// LocalExec runs the laptop-side `codex exec` over a snapshot piped on stdin
// and returns its stdout (the title). Split out as a seam so a fake codex
// returns a canned title in tests without spawning a real process.
type LocalExec interface {
	Run(ctx context.Context, args []string, stdin io.Reader) (string, error)
}

// CodexExec is the default Namer: capture-pane head on the hub, pipe into a
// local `codex exec`. model selects the codex model; the reasoning effort is
// fixed low (DESIGN §5).
type CodexExec struct {
	run   Runner    // hub-side pane capture
	exec  LocalExec // laptop-side codex
	model string    // codex model id, e.g. a mini model
}

// NewCodexExec returns a CodexExec wired to the hub Runner, the local codex
// exec seam, and the codex model id.
func NewCodexExec(run Runner, exec LocalExec, model string) *CodexExec {
	return &CodexExec{run: run, exec: exec, model: model}
}

// titlePrompt is the instruction handed to codex over the captured pane head.
// It asks for a single short title and nothing else so the stdout is the name.
const titlePrompt = "Read the terminal snapshot on stdin and reply with a short (2-4 word) human title for this session. Reply with the title only, no punctuation, no quotes, no explanation."

// codexArgs builds the local codex invocation: the model, the fixed low
// reasoning effort (DESIGN §5), and the title prompt. The snapshot is fed on
// stdin, not as an argument, so it never hits the argv length limit.
func (c *CodexExec) codexArgs() []string {
	return []string{
		"exec",
		"-m", c.model,
		"-c", "model_reasoning_effort=low",
		titlePrompt,
	}
}

// Name implements Namer for the codex path. The snapshot is the already-captured
// pane head (CaptureHead); Name pipes it into the local codex and returns the
// trimmed title. An empty title is an error so callers fall back to dir-derived.
func (c *CodexExec) Name(ctx context.Context, snapshot string) (string, error) {
	out, err := c.exec.Run(ctx, c.codexArgs(), strings.NewReader(snapshot))
	if err != nil {
		return "", err
	}
	title := strings.TrimSpace(out)
	// Codex may echo a leading newline or a stray quote; collapse to one line.
	if i := strings.IndexAny(title, "\n\r"); i >= 0 {
		title = strings.TrimSpace(title[:i])
	}
	title = strings.Trim(title, `"'`)
	if title == "" {
		return "", fmt.Errorf("codex returned an empty title")
	}
	return title, nil
}

// captureHeadCmd is the hub-side pane-capture pipeline (split out so tests can
// assert the exact command string without a runner).
func captureHeadCmd(id string) string {
	return fmt.Sprintf("tmux capture-pane -p -S - -t %s | head -n 200 | head -c 8000", paths.Quote(id))
}

// CaptureHead reads the head of session id's pane on the hub:
// `tmux capture-pane -p -S - -t <id> | head -n 200 | head -c 8000`. Capturing
// early (while the session is young) sidesteps the alternate-screen scrollback
// gap (DESIGN §5). The result is fed to Name (and hashed for the cache).
func (c *CodexExec) CaptureHead(id string) (string, error) {
	return c.run.Run(captureHeadCmd(id))
}

// DirDerived is the no-codex fallback Namer: it ignores the snapshot and yields
// the dir-derived floor. Used when codex is absent/disabled so naming always
// has a value.
type DirDerived struct {
	Dir string // tilde-form working dir
}

// Name returns the dir-derived floor, ignoring the snapshot.
func (d DirDerived) Name(ctx context.Context, snapshot string) (string, error) {
	return names.Derive(d.Dir), nil
}

// CaptureHead returns an empty snapshot: the dir-derived namer ignores pane
// content. Present so DirDerived satisfies Capturer.
func (d DirDerived) CaptureHead(id string) (string, error) { return "", nil }

// Hash is the content-hash cache helper: a stable digest of a captured head
// used as names.Entry.CodexHash. A cached codex name is reused while Hash of
// the current head is unchanged; regeneration happens only on a material change
// or an explicit `^n`.
func Hash(snapshot string) string {
	sum := sha256.Sum256([]byte(snapshot))
	return hex.EncodeToString(sum[:8]) // 16 hex chars, plenty to gate regen
}

// CacheHit is the skip decision the lazy-naming caller consults BEFORE calling
// Name: it reports whether e already holds a codex name minted from the same
// head, so the (laptop-quota) codex call can be skipped and the cached name
// reused. The frozen Namer.Name(ctx, snapshot) signature is stateless and has
// no access to the prior hash, so the skip lives here as a pure helper the
// caller invokes — Name itself never decides to skip. A hit requires both a
// non-empty cached CodexName and a CodexHash matching the current head; an
// empty name or a materially changed head is a miss and triggers a re-name.
func CacheHit(e names.Entry, head string) bool {
	return e.CodexName != "" && e.CodexHash == Hash(head)
}
