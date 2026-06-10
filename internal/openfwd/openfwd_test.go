package openfwd

import (
	"fmt"
	"testing"
)

// recordingDeps captures what Handle decided to do so tests assert on the
// action rather than touching real sockets/files.
type recordingDeps struct {
	opened       []string
	forwardedHub int
	localPort    int
	fetched      []string
	existing     map[string]bool
}

func (d *recordingDeps) deps() Deps {
	return Deps{
		LocalHome: "/Users/me",
		Open:      func(t string) error { d.opened = append(d.opened, t); return nil },
		LocalForward: func(hubPort int) (int, error) {
			d.forwardedHub = hubPort
			if d.localPort == 0 {
				d.localPort = 55000
			}
			return d.localPort, nil
		},
		Exists: func(p string) bool { return d.existing[p] },
		Fetch:  func(p string) (string, error) { d.fetched = append(d.fetched, p); return "/tmp/duck/" + p, nil },
	}
}

func TestPublicURLOpensVerbatim(t *testing.T) {
	d := &recordingDeps{}
	note, err := Handle(Request{Target: "https://anthropic.com/login?x=1"}, d.deps())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.opened) != 1 || d.opened[0] != "https://anthropic.com/login?x=1" {
		t.Fatalf("opened = %v", d.opened)
	}
	if d.forwardedHub != 0 {
		t.Fatalf("public URL must not forward; got hub port %d", d.forwardedHub)
	}
	_ = note
}

func TestLoopbackURLTunnelsAndRewrites(t *testing.T) {
	d := &recordingDeps{localPort: 55001}
	_, err := Handle(Request{Target: "http://localhost:5173/app"}, d.deps())
	if err != nil {
		t.Fatal(err)
	}
	if d.forwardedHub != 5173 {
		t.Fatalf("forwarded hub port = %d, want 5173", d.forwardedHub)
	}
	want := "http://127.0.0.1:55001/app"
	if len(d.opened) != 1 || d.opened[0] != want {
		t.Fatalf("opened = %v, want [%s]", d.opened, want)
	}
}

func TestLoopbackURLDefaultPort(t *testing.T) {
	d := &recordingDeps{}
	if _, err := Handle(Request{Target: "http://127.0.0.1/"}, d.deps()); err != nil {
		t.Fatal(err)
	}
	if d.forwardedHub != 80 {
		t.Fatalf("forwarded hub port = %d, want 80 (scheme default)", d.forwardedHub)
	}
}

func TestSyncedFileOpensLocalTwin(t *testing.T) {
	d := &recordingDeps{existing: map[string]bool{"/Users/me/dev/foo/out.html": true}}
	note, err := Handle(Request{
		Target: "/home/hub/dev/foo/out.html",
		Home:   "/home/hub",
	}, d.deps())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.opened) != 1 || d.opened[0] != "/Users/me/dev/foo/out.html" {
		t.Fatalf("opened = %v, want the translated local twin", d.opened)
	}
	if len(d.fetched) != 0 {
		t.Fatalf("a synced file must not be fetched; fetched = %v", d.fetched)
	}
	_ = note
}

func TestRelativeFileResolvedAgainstCwd(t *testing.T) {
	d := &recordingDeps{existing: map[string]bool{"/Users/me/dev/foo/out.html": true}}
	_, err := Handle(Request{
		Target: "out.html",
		Cwd:    "/home/hub/dev/foo",
		Home:   "/home/hub",
	}, d.deps())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.opened) != 1 || d.opened[0] != "/Users/me/dev/foo/out.html" {
		t.Fatalf("opened = %v", d.opened)
	}
}

func TestUnsyncedFileIsFetched(t *testing.T) {
	d := &recordingDeps{} // nothing exists locally
	_, err := Handle(Request{Target: "/tmp/chart.png", Home: "/home/hub"}, d.deps())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.fetched) != 1 || d.fetched[0] != "/tmp/chart.png" {
		t.Fatalf("fetched = %v, want [/tmp/chart.png]", d.fetched)
	}
	if len(d.opened) != 1 || d.opened[0] != "/tmp/duck//tmp/chart.png" {
		t.Fatalf("opened = %v (the fetched copy)", d.opened)
	}
}

func TestHomeFileOutsideHomeIsFetched(t *testing.T) {
	// A path NOT under the hub home has no synced twin to try, so it fetches even
	// though Exists would say false anyway — this pins translate's ok=false path.
	d := &recordingDeps{existing: map[string]bool{"/etc/hosts": true}}
	_, err := Handle(Request{Target: "/etc/hosts", Home: "/home/hub"}, d.deps())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.fetched) != 1 {
		t.Fatalf("path outside hub home should fetch, not open a coincidental local path; fetched=%v opened=%v", d.fetched, d.opened)
	}
}

func TestEmptyTargetErrors(t *testing.T) {
	d := &recordingDeps{}
	if _, err := Handle(Request{Target: "  "}, d.deps()); err == nil {
		t.Fatal("expected error on empty target")
	}
}

func TestLoopbackURLWithoutForwarderErrors(t *testing.T) {
	d := Deps{LocalHome: "/Users/me", Open: func(string) error { return nil }} // no LocalForward
	if _, err := Handle(Request{Target: "http://localhost:3000"}, d); err == nil {
		t.Fatal("expected error opening loopback URL with no tunnel")
	}
}

func TestOpenErrorPropagates(t *testing.T) {
	d := Deps{Open: func(string) error { return fmt.Errorf("boom") }}
	if _, err := Handle(Request{Target: "https://example.com"}, d); err == nil {
		t.Fatal("expected Open error to propagate")
	}
}
