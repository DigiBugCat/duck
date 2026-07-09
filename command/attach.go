// attach.go is the command-layer interactive-attach engine: it runs ONE attach
// as a subprocess, classifies the ssh exit into an Outcome, and wraps the whole
// thing in a reconnect loop that survives a transport drop (ssh -t connection
// lost mid-session) by reconnecting with capped exponential backoff, while a ^c
// during the backoff is the give-up signal. Every interactive attach path —
// bare `duck` (created and reused), `duck connect`, `duck --resume`, the picker,
// and `duck -c` — routes through attachWithReconnect so reconnect applies
// uniformly (we stopped using syscall.Exec for interactive attach for exactly
// this reason).
package command

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"time"
)

// Outcome is the classified result of one (or a loop of) interactive attach(es).
type Outcome int

const (
	// CleanLeave: the attach exited 0 / nil — the user detached or exited the
	// shell normally. The fresh-untouched cleanup is allowed only on this outcome.
	CleanLeave Outcome = iota
	// TransportDrop: ssh exit 255 — ssh could not connect or the connection was
	// lost mid-session. The reconnect loop retries this indefinitely with backoff.
	TransportDrop
	// SessionGone: ssh CONNECTED but `tmux attach` failed (any other non-zero) —
	// no tmux server or the named session is gone (e.g. after a hub reboot). Stop.
	SessionGone
	// GaveUp: the user pressed ^c during a reconnect backoff. The session is KEPT
	// (a dropped/abandoned session must survive so `duck -c` can resume it).
	GaveUp
	// AttachFailed: ssh never LAUNCHED — the error is not an *exec.ExitError (ssh
	// binary missing, EnsureControlDir failure, etc.). This is a permanent, fatal
	// condition: retrying would loop FOREVER and swallow the real error, so the
	// loop surfaces the underlying error and STOPS. Distinct from TransportDrop
	// (ssh launched but exited 255), which IS retried.
	AttachFailed
)

// classifyAttach maps an attach error (the *exec.ExitError from RunAttach) to an
// Outcome. nil → exit 0 → CleanLeave. 255 → TransportDrop. any other non-zero →
// SessionGone. A non-ExitError (e.g. ssh binary not found, or EnsureControlDir
// failed so ssh never launched) → AttachFailed: a PERMANENT fatal condition that
// must STOP and surface the real error, not be retried forever as a transport
// drop.
func classifyAttach(err error) Outcome {
	if err == nil {
		return CleanLeave
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		switch ee.ExitCode() {
		case 0:
			return CleanLeave
		case 255:
			return TransportDrop
		default:
			return SessionGone
		}
	}
	// ssh never launched (binary missing, control-dir failure, …): permanent and
	// fatal — surface it and stop, never retry.
	return AttachFailed
}

// SessionAttacher is the subset of *session.Manager the reconnect loop needs:
// one blocking subprocess attach returning the ssh exit error.
type SessionAttacher interface {
	AttachAndWait(id string) error
}

// reconnector holds the injectable seams the reconnect loop uses so the
// indefinite-backoff and signal behavior is unit-testable with no real time or
// signals. Production values are wired by newReconnector.
type reconnector struct {
	// sleep returns a channel that fires after d (the backoff elapsed). Tests
	// inject a controllable channel; production uses a real timer.
	sleep func(d time.Duration) <-chan time.Time
	// interrupts delivers a value when the user presses ^c DURING a backoff. In
	// production it is fed by signal.Notify(os.Interrupt) armed only across the
	// backoff window (the attach child owns the TTY so ^c during the ATTACH goes
	// to the remote). Tests inject a channel they fire directly.
	interrupts <-chan struct{}
	// armInterrupts/disarmInterrupts install and remove the SIGINT handler around
	// the backoff window. nil in tests (the injected channel needs no arming).
	armInterrupts    func()
	disarmInterrupts func()
	// printf is the user-facing notice writer (overridable in tests).
	printf func(format string, a ...any)
	// maxBackoff caps the exponential schedule (≈30s in production).
	maxBackoff time.Duration
	// remember records a GaveUp session per-terminal (productionRemember in prod;
	// a recording fake in tests).
	remember rememberFunc
	// selfHealing is set when the transport reconnects on its OWN (tssh/QUIC): a
	// network drop never makes tssh exit, so the only non-zero exits are terminal
	// (clean detach is 0). With it set, a would-be TransportDrop is treated as
	// SessionGone — the ssh-255 backoff loop is never entered (retrying a
	// tsshd/bootstrap failure forever would be wrong).
	selfHealing bool
}

// attachWithReconnect runs the attach loop for tmuxName and returns the terminal
// Outcome. Loop: attach (subprocess, child holds the TTY) → classify →
//
//	CleanLeave  → return CleanLeave.
//	SessionGone → print "session <name> is no longer on the hub", return SessionGone.
//	TransportDrop → print "connection lost — reconnecting…", BACKOFF (1s,2s,4s,…
//	  capped at maxBackoff, indefinitely); a ^c during the backoff → return GaveUp.
//
// Default signal handling is restored across the actual attach (the child's ssh
// -t raw mode means ^c is a byte to the remote there); the SIGINT handler is
// armed only during the backoff sleep.
func (rc *reconnector) attachWithReconnect(sessions SessionAttacher, tmuxName string) Outcome {
	backoff := time.Second
	for {
		err := sessions.AttachAndWait(tmuxName)
		outcome := classifyAttach(err)
		if rc.selfHealing && outcome == TransportDrop {
			// tssh self-roams: a dropped link never makes tssh exit, so a non-zero
			// exit is terminal (clean detach is 0). Treat it as SessionGone so the
			// backoff loop below is never entered.
			outcome = SessionGone
		}
		switch outcome {
		case CleanLeave:
			return CleanLeave
		case AttachFailed:
			// the attach binary never launched: permanent and fatal. Surface the
			// underlying error and STOP — retrying would loop forever and swallow it.
			rc.printf("could not start %s attach: %v\n", rc.transportName(), err)
			return AttachFailed
		case SessionGone:
			if rc.selfHealing {
				// tssh exited non-zero: gone session or a bootstrap/tsshd
				// failure (tssh prints its own diagnostic to stderr). Don't retry.
				rc.printf("tssh attach for %s ended\n", tmuxName)
			} else {
				rc.printf("session %s is no longer on the hub\n", tmuxName)
			}
			return SessionGone
		case TransportDrop:
			rc.printf("connection lost — reconnecting…\n")
			if rc.backoffInterrupted(backoff) {
				return GaveUp
			}
			if backoff < rc.maxBackoff {
				backoff *= 2
				if backoff > rc.maxBackoff {
					backoff = rc.maxBackoff
				}
			}
		}
	}
}

// backoffInterrupted sleeps for d while watching for a ^c. It returns true if an
// interrupt arrived (give up), false if the sleep completed (retry). The SIGINT
// handler is armed only for this window so a ^c during the ATTACH (not here)
// reaches the remote instead.
func (rc *reconnector) backoffInterrupted(d time.Duration) bool {
	if rc.armInterrupts != nil {
		rc.armInterrupts()
		defer rc.disarmInterrupts()
	}
	select {
	case <-rc.interrupts:
		return true
	case <-rc.sleep(d):
		return false
	}
}

// transportName labels the attach transport in user-facing notices.
func (rc *reconnector) transportName() string {
	if rc.selfHealing {
		return "tssh"
	}
	return "ssh"
}

// realPrintf is the production user-notice writer.
func realPrintf(format string, a ...any) { fmt.Printf(format, a...) }

// newReconnector builds the production reconnector: a real timer for the
// backoff sleep and a SIGINT channel armed/disarmed around each backoff window.
// The handler is installed only during backoff so a ^c during the ATTACH (ssh -t
// raw mode) reaches the remote; only a ^c during backoff is the give-up signal.
func newReconnector(selfHealing bool) *reconnector {
	sigCh := make(chan os.Signal, 1)
	interrupts := make(chan struct{}, 1)
	rc := &reconnector{
		sleep: func(d time.Duration) <-chan time.Time {
			return time.After(d)
		},
		interrupts:  interrupts,
		printf:      realPrintf,
		maxBackoff:  30 * time.Second,
		selfHealing: selfHealing,
	}
	var done chan struct{}
	rc.armInterrupts = func() {
		done = make(chan struct{})
		signal.Notify(sigCh, os.Interrupt)
		// Forward at most one signal per window into the loop's interrupt channel.
		// The done channel lets disarm unblock this goroutine so it does not linger.
		go func() {
			select {
			case <-sigCh:
				select {
				case interrupts <- struct{}{}:
				default:
				}
			case <-done:
			}
		}()
	}
	rc.disarmInterrupts = func() {
		signal.Stop(sigCh)
		close(done) // unblock the forwarding goroutine
		// Drain any pending signal so it does not leak into the next window.
		select {
		case <-sigCh:
		default:
		}
		// Drain the shared interrupts channel too: a ^C landing as the backoff
		// timer fires can leave a stale buffered value that later reads as GaveUp
		// and silently abandons a session the user did not intend to drop.
		select {
		case <-interrupts:
		default:
		}
	}
	return rc
}

// rememberOnGiveUp records tmuxName in the per-terminal memory when the outcome
// is GaveUp (the user ^c'd a dropped session) so `duck -c` from this same
// terminal resumes THAT session. Injectable as a field so the loop's give-up
// behavior is observable in tests; production uses the real tty-memory store.
type rememberFunc func(tmuxName string)

// productionRemember is the real GaveUp recorder: it Sets the current terminal's
// remembered session in ~/.duck/tty-last.json.
func productionRemember(tmuxName string) { _ = ttyMemSet(CurrentTTY(), tmuxName) }

// runAttachLoop is the single entry point every interactive attach path calls.
// It runs the reconnect loop and, on a GaveUp, records the session per-terminal.
// Returns the terminal Outcome. selfHealing is true for the tssh transport (tssh
// roams on its own, so duck skips the ssh-255 backoff retry — see attachWithReconnect).
// title labels the terminal tab for the duration of the attach; empty falls back
// to the internal tmux name (callers that know the session's display name — the
// picker — pass it, the direct-attach paths don't).
func runAttachLoop(sessions SessionAttacher, tmuxName, title string, selfHealing bool) Outcome {
	if title == "" {
		title = tmuxName
	}
	setTerminalTitle(title)
	// Wrap the whole interactive attach in the open-interceptor: while attached,
	// the hub's open attempts route to this laptop. A no-op when the hook is unset
	// (tests / no hub wired). It spans the reconnect loop so a transport drop and
	// reconnect keeps the same forwarding session. The opener forwards ride the ssh
	// control master regardless of the attach transport, so this works under tssh too.
	return withOpenForwarding(tmuxName, func() Outcome {
		rc := newReconnector(selfHealing)
		rc.remember = productionRemember
		return rc.run(sessions, tmuxName)
	})
}

// run executes the reconnect loop and applies the GaveUp side effect (per-
// terminal memory). Split from attachWithReconnect so the pure classification/
// backoff loop stays independently testable.
func (rc *reconnector) run(sessions SessionAttacher, tmuxName string) Outcome {
	outcome := rc.attachWithReconnect(sessions, tmuxName)
	if outcome == GaveUp && rc.remember != nil {
		rc.remember(tmuxName)
	}
	return outcome
}
