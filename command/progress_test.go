package command

import (
	"bytes"
	"strings"
	"testing"
)

// TestProgressTTYWritesCarriageReturns verifies the reporter redraws in place
// (writes \r) and prints the dir + live status when isTTY is true.
func TestProgressTTYWritesCarriageReturns(t *testing.T) {
	var buf bytes.Buffer
	p := &ttyProgress{w: &buf, isTTY: true}

	p.Start("syncing", "~/dev/foo")
	p.Update("Staging files on beta")
	p.Stop(true)

	out := buf.String()
	if !strings.Contains(out, "\r") {
		t.Fatalf("TTY reporter wrote no \\r; got %q", out)
	}
	if !strings.Contains(out, "~/dev/foo") {
		t.Fatalf("output missing target dir; got %q", out)
	}
	if !strings.Contains(out, "Staging files on beta") {
		t.Fatalf("output missing live status; got %q", out)
	}
	if !strings.Contains(out, "✓ synced") {
		t.Fatalf("Stop(true) missing ✓ note; got %q", out)
	}
}

// TestProgressNonTTYWritesNothing asserts the load-bearing property: when
// stdout is NOT a terminal the reporter emits NOTHING (no \r spam, no note), so
// a piped/redirected `duck` stays clean.
func TestProgressNonTTYWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	p := &ttyProgress{w: &buf, isTTY: false}

	p.Start("syncing", "~/dev/foo")
	p.Update("Staging files on beta")
	p.Update("Watching for changes")
	p.Stop(true)

	if buf.Len() != 0 {
		t.Fatalf("non-TTY reporter wrote %q, want nothing", buf.String())
	}
	if strings.Contains(buf.String(), "\r") {
		t.Fatalf("non-TTY reporter wrote a \\r")
	}
}

// TestProgressStartIdempotent confirms a second Start (e.g. reconcile then
// waitSteady on the same reporter) does not begin a second line — the lifecycle
// stays one continuous line with a single ✓.
func TestProgressStartIdempotent(t *testing.T) {
	var buf bytes.Buffer
	p := &ttyProgress{w: &buf, isTTY: true}

	p.Start("syncing", "~/dev/foo")           // reconcile begins the line
	p.Update("reconciling")                   // reconcile status
	p.Start("syncing", "~/dev/should-ignore") // waitSteady re-Start: no-op
	p.Update("Watching for changes")
	p.Stop(true)

	out := buf.String()
	if strings.Contains(out, "should-ignore") {
		t.Fatalf("second Start changed the target; got %q", out)
	}
	if n := strings.Count(out, "✓ synced"); n != 1 {
		t.Fatalf("✓ synced count = %d, want exactly 1", n)
	}
}
