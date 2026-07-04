package channel

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// useTempSpool points the spool at a fresh temp home for the test.
func useTempSpool(t *testing.T) {
	t.Helper()
	old := spoolHome
	spoolHome = t.TempDir()
	t.Cleanup(func() { spoolHome = old })
}

func TestPublishDrainRoundtrip(t *testing.T) {
	useTempSpool(t)
	if err := Publish("work", "hello", map[string]string{"source": "cli"}); err != nil {
		t.Fatal(err)
	}
	if err := Publish("work", "second", nil); err != nil {
		t.Fatal(err)
	}
	evs, err := DrainSpool("work")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("want 2 events, got %d: %+v", len(evs), evs)
	}
	if evs[0].Content != "hello" || evs[0].Meta["source"] != "cli" {
		t.Fatalf("first event wrong: %+v", evs[0])
	}
	if evs[1].Content != "second" {
		t.Fatalf("second event wrong: %+v", evs[1])
	}
	// Drain is destructive: a second drain finds nothing.
	evs, err = DrainSpool("work")
	if err != nil || len(evs) != 0 {
		t.Fatalf("second drain should be empty: %v %+v", err, evs)
	}
}

func TestDrainMissing(t *testing.T) {
	useTempSpool(t)
	evs, err := DrainSpool("never")
	if err != nil {
		t.Fatalf("missing spool must not error: %v", err)
	}
	if evs != nil {
		t.Fatalf("missing spool should yield nil, got %+v", evs)
	}
}

func TestDrainSkipsCorruptLines(t *testing.T) {
	useTempSpool(t)
	if err := Publish("work", "good", nil); err != nil {
		t.Fatal(err)
	}
	// Splice a garbage line into the spool.
	p, err := spoolPath("work")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{not json\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := Publish("work", "alsogood", nil); err != nil {
		t.Fatal(err)
	}
	evs, err := DrainSpool("work")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Content != "good" || evs[1].Content != "alsogood" {
		t.Fatalf("corrupt line must be skipped, valid kept: %+v", evs)
	}
}

// TestConcurrentPublishDuringDrain proves a publish that lands after the drain
// renamed the spool aside goes into a fresh spool and is not lost.
func TestConcurrentPublishDuringDrain(t *testing.T) {
	useTempSpool(t)
	if err := Publish("work", "before", nil); err != nil {
		t.Fatal(err)
	}
	// Manually reproduce the drain's rename-then-read window, publishing in
	// between — deterministic, no timing race.
	p, _ := spoolPath("work")
	aside := p + ".aside"
	if err := os.Rename(p, aside); err != nil {
		t.Fatal(err)
	}
	if err := Publish("work", "during", nil); err != nil { // recreates the spool
		t.Fatal(err)
	}
	// The renamed-aside batch holds only "before".
	data, err := os.ReadFile(aside)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got == "" || got[:6] != "{\"time" {
		t.Fatalf("aside batch unexpected: %q", got)
	}
	// The fresh spool holds "during" — not lost.
	evs, err := DrainSpool("work")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Content != "during" {
		t.Fatalf("mid-drain publish lost: %+v", evs)
	}
}

func TestConcurrentPublishersDoNotInterleave(t *testing.T) {
	useTempSpool(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Publish("work", "x", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	evs, err := DrainSpool("work")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 20 {
		t.Fatalf("want 20 clean lines, got %d", len(evs))
	}
}

func TestAliveMarkerFreshness(t *testing.T) {
	useTempSpool(t)
	if AliveWithin("work", time.Minute) {
		t.Fatal("no marker yet — must not be alive")
	}
	if err := TouchAlive("work"); err != nil {
		t.Fatal(err)
	}
	if !AliveWithin("work", time.Minute) {
		t.Fatal("just touched — must be alive")
	}
	// Backdate the marker past the window.
	p, _ := alivePath("work")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if AliveWithin("work", time.Minute) {
		t.Fatal("stale marker must not count as alive")
	}
}

func TestInvalidWorkspaceRejected(t *testing.T) {
	useTempSpool(t)
	if err := Publish("a/b", "x", nil); err == nil {
		t.Fatal("slash in workspace must be rejected")
	}
	if err := Publish("", "x", nil); err == nil {
		t.Fatal("empty workspace must be rejected")
	}
}

func TestPublishTruncatesHugeContent(t *testing.T) {
	useTempSpool(t)
	big := make([]byte, maxPush+500)
	for i := range big {
		big[i] = 'a'
	}
	if err := Publish("work", string(big), nil); err != nil {
		t.Fatal(err)
	}
	evs, err := DrainSpool("work")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || len(evs[0].Content) > maxPush+len(" …[truncated]") {
		t.Fatalf("content not truncated: len=%d", len(evs[0].Content))
	}
	// The spool file basename is <ws>.jsonl under channel-spool.
	d, _ := spoolDir()
	if _, err := os.Stat(filepath.Join(d, "work.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("spool should be drained away, got err=%v", err)
	}
}
