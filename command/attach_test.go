package command

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// exitErr fabricates an *exec.ExitError carrying the given exit code, so the
// classifier can be tested without launching a real process. It runs a tiny
// `sh -c "exit N"` — the only portable way to get a real *exec.ExitError with a
// chosen code.
func exitErr(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+itoa(code)).Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("could not fabricate exit %d: %v", code, err)
	}
	if ee.ExitCode() != code {
		t.Fatalf("fabricated exit code = %d, want %d", ee.ExitCode(), code)
	}
	return err
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func TestClassifyAttach(t *testing.T) {
	if got := classifyAttach(nil); got != CleanLeave {
		t.Fatalf("nil → %v, want CleanLeave", got)
	}
	if got := classifyAttach(exitErr(t, 255)); got != TransportDrop {
		t.Fatalf("255 → %v, want TransportDrop", got)
	}
	for _, code := range []int{1, 2} {
		if got := classifyAttach(exitErr(t, code)); got != SessionGone {
			t.Fatalf("%d → %v, want SessionGone", code, got)
		}
	}
	// A plain non-*exec.ExitError (ssh never launched: binary missing, control-dir
	// failure) is a permanent fatal condition → AttachFailed, NOT TransportDrop.
	if got := classifyAttach(errors.New("ssh: executable file not found in $PATH")); got != AttachFailed {
		t.Fatalf("non-exit error → %v, want AttachFailed", got)
	}
}

// TestAttachFailedStopsNoReconnect: a non-launchable ssh (plain non-exit error)
// is terminal — the loop returns AttachFailed on the FIRST attach, surfaces the
// underlying error, and never retries (otherwise a permanent failure would loop
// forever and swallow the real error).
func TestAttachFailedStopsNoReconnect(t *testing.T) {
	boom := errors.New("ssh: executable file not found in $PATH")
	a := &fakeLoopAttacher{errs: []error{boom}}
	var msgs []string
	rc := newTestReconnector(nil, &msgs, nil)

	if got := rc.attachWithReconnect(a, "foo"); got != AttachFailed {
		t.Fatalf("outcome = %v, want AttachFailed", got)
	}
	if a.calls != 1 {
		t.Fatalf("attach calls = %d, want 1 (no reconnect on a permanent failure)", a.calls)
	}
	var surfaced bool
	for _, m := range msgs {
		if strings.Contains(m, "could not start ssh attach") {
			surfaced = true
		}
	}
	if !surfaced {
		t.Fatalf("AttachFailed must surface the error; messages = %v", msgs)
	}
}

// fakeAttacher returns a scripted sequence of errors, one per AttachAndWait call.
type fakeLoopAttacher struct {
	errs  []error
	calls int
}

func (f *fakeLoopAttacher) AttachAndWait(string) error {
	i := f.calls
	f.calls++
	if i < len(f.errs) {
		return f.errs[i]
	}
	return nil // past the script → clean exit
}

// newTestReconnector builds a reconnector with no real time or signals: sleep
// fires immediately (so the schedule advances without delay), interrupts never
// fires unless the test injects it, printf records the messages.
func newTestReconnector(slept *[]time.Duration, msgs *[]string, interrupts <-chan struct{}) *reconnector {
	return &reconnector{
		sleep: func(d time.Duration) <-chan time.Time {
			if slept != nil {
				*slept = append(*slept, d)
			}
			ch := make(chan time.Time, 1)
			ch <- time.Now()
			return ch
		},
		interrupts: interrupts,
		printf:     func(format string, a ...any) { *msgs = append(*msgs, format) },
		maxBackoff: 30 * time.Second,
	}
}

// TestReconnectLoopRetriesThenCleanLeave: 255 twice then 0 → loops, prints the
// reconnecting notice each drop, then returns CleanLeave.
func TestReconnectLoopRetriesThenCleanLeave(t *testing.T) {
	a := &fakeLoopAttacher{errs: []error{exitErr(t, 255), exitErr(t, 255), nil}}
	var msgs []string
	rc := newTestReconnector(nil, &msgs, nil)

	if got := rc.attachWithReconnect(a, "foo"); got != CleanLeave {
		t.Fatalf("outcome = %v, want CleanLeave", got)
	}
	if a.calls != 3 {
		t.Fatalf("attach calls = %d, want 3 (2 drops + 1 clean)", a.calls)
	}
	var reconnecting int
	for _, m := range msgs {
		if m == "connection lost — reconnecting…\n" {
			reconnecting++
		}
	}
	if reconnecting != 2 {
		t.Fatalf("reconnecting notices = %d, want 2", reconnecting)
	}
}

// TestReconnectBackoffSchedule pins the exponential-capped schedule via the
// injected sleeper: 1,2,4,8,16,30,30 for seven drops before a clean exit.
func TestReconnectBackoffSchedule(t *testing.T) {
	errs := make([]error, 7)
	for i := range errs {
		errs[i] = exitErr(t, 255)
	}
	errs = append(errs, nil) // clean exit after 7 drops
	a := &fakeLoopAttacher{errs: errs}
	var slept []time.Duration
	var msgs []string
	rc := newTestReconnector(&slept, &msgs, nil)

	if got := rc.attachWithReconnect(a, "foo"); got != CleanLeave {
		t.Fatalf("outcome = %v, want CleanLeave", got)
	}
	want := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	if len(slept) != len(want) {
		t.Fatalf("sleep count = %d, want %d (%v)", len(slept), len(want), slept)
	}
	for i := range want {
		if slept[i] != want[i] {
			t.Fatalf("backoff[%d] = %v, want %v (schedule %v)", i, slept[i], want[i], slept)
		}
	}
}

// TestSessionGoneStopsNoReconnect: a non-255 non-zero exit (ssh connected but
// tmux attach failed) → SessionGone, the no-longer-on-hub message, and NO retry.
func TestSessionGoneStopsNoReconnect(t *testing.T) {
	a := &fakeLoopAttacher{errs: []error{exitErr(t, 1)}}
	var msgs []string
	rc := newTestReconnector(nil, &msgs, nil)

	if got := rc.attachWithReconnect(a, "foo"); got != SessionGone {
		t.Fatalf("outcome = %v, want SessionGone", got)
	}
	if a.calls != 1 {
		t.Fatalf("SessionGone must not retry; attach calls = %d, want 1", a.calls)
	}
	if len(msgs) != 1 || msgs[0] != "session %s is no longer on the hub\n" {
		t.Fatalf("expected the no-longer-on-hub message, got %v", msgs)
	}
}

// TestSigintDuringBackoffGivesUpAndRemembers: a 255 drop, then ^c arrives during
// the backoff → GaveUp, and run() Sets the per-terminal memory with the session
// name. The sleeper never fires (so the interrupt wins the select).
func TestSigintDuringBackoffGivesUpAndRemembers(t *testing.T) {
	interrupts := make(chan struct{}, 1)
	interrupts <- struct{}{} // ^c is already pending when the backoff begins

	a := &fakeLoopAttacher{errs: []error{exitErr(t, 255)}}
	var msgs []string
	rc := &reconnector{
		// A never-firing sleeper guarantees the interrupt branch wins.
		sleep:      func(time.Duration) <-chan time.Time { return make(chan time.Time) },
		interrupts: interrupts,
		printf:     func(format string, a ...any) { msgs = append(msgs, format) },
		maxBackoff: 30 * time.Second,
	}
	var remembered string
	rc.remember = func(name string) { remembered = name }

	if got := rc.run(a, "foo"); got != GaveUp {
		t.Fatalf("outcome = %v, want GaveUp", got)
	}
	if remembered != "foo" {
		t.Fatalf("GaveUp must Set the per-terminal memory with the session name, got %q", remembered)
	}
}
