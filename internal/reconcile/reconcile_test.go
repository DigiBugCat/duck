package reconcile

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/DigiBugCat/duck/internal/hub"
	"github.com/DigiBugCat/duck/internal/paths"
)

// recordedCmd is one captured rsync invocation: the binary name and its argv.
type recordedCmd struct {
	name string
	args []string
}

// swapRun points the package run seam at a recorder for the duration of the
// returned restore func, so the constructed rsync argv is asserted without
// invoking real rsync. The optional fail func injects per-call failures. It also
// stubs the SEPARATE mkdirRemote seam to a no-op recorder so ReconcileNewest's
// pre-PUSH hub mkdir never contacts a real host (the mkdir is asserted in its
// own dedicated test); mkdir failures are not injected here.
func swapRun(t *testing.T, fail func(call int) error) (*[]recordedCmd, func()) {
	t.Helper()
	var got []recordedCmd
	orig := run
	run = func(_ func(string), name string, args ...string) error {
		got = append(got, recordedCmd{name: name, args: append([]string(nil), args...)})
		if fail != nil {
			return fail(len(got) - 1)
		}
		return nil
	}
	origMkdir := mkdirRemote
	mkdirRemote = func(addr, rel string) error { return nil }
	// Stub the GNU-rsync resolver so tests never shell out to `rsync --version`
	// and the recorded binary name stays "rsync".
	origBin := rsyncBin
	rsyncBin = func() (string, error) { return "rsync", nil }
	return &got, func() { run = orig; mkdirRemote = origMkdir; rsyncBin = origBin }
}

// TestReconcileNewestBuildsTwoNewestWinsRsyncs is the SAFETY-CRITICAL test. It
// pins, for the newest-wins seed, that ReconcileNewest builds EXACTLY two rsync
// commands, that BOTH are `rsync -a -u` with NO --delete and trailing-slash
// CONTENTS operands, that the FIRST is PUSH local→hub and the SECOND is PULL
// hub→local, and that the operands use the ABSOLUTE local path and the hub's
// RemoteSyncPath rel path. A regression here (a stray --delete, a dropped -u, a
// missing trailing slash, or a reversed direction) would risk data loss, so
// every property is asserted explicitly.
func TestReconcileNewestBuildsTwoNewestWinsRsyncs(t *testing.T) {
	const (
		addr     = "me@hub"
		tildeDir = "~/dev/foo"
	)
	wantLocal, err := paths.Expand(tildeDir)
	if err != nil {
		t.Fatalf("paths.Expand: %v", err)
	}
	wantLocalContents := wantLocal + "/"
	// The remote path is SINGLE-QUOTED (one literal shell word) with the trailing
	// slash OUTSIDE the quotes so rsync still parses CONTENTS form: me@hub:'dev/foo'/
	wantHubContents := addr + ":" + paths.Quote(hub.RemoteSyncPath(tildeDir)) + "/"

	got, restore := swapRun(t, nil)
	defer restore()

	if err := Reconcile(addr, tildeDir, Merge, nil); err != nil {
		t.Fatalf("ReconcileNewest: %v", err)
	}

	if len(*got) != 2 {
		t.Fatalf("expected EXACTLY 2 rsync commands, got %d: %v", len(*got), *got)
	}

	for i, c := range *got {
		if c.name != "rsync" {
			t.Fatalf("cmd %d: binary = %q, want rsync", i, c.name)
		}
		joined := strings.Join(c.args, " ")
		// -a and -u must both be present (as -a -u or folded -au).
		hasA := contains(c.args, "-a") || strings.Contains(joined, "-au") || strings.Contains(joined, "-ua")
		hasU := contains(c.args, "-u") || strings.Contains(joined, "-au") || strings.Contains(joined, "-ua")
		if !hasA || !hasU {
			t.Fatalf("cmd %d must be archive+update (-a -u / -au); got args=%v", i, c.args)
		}
		// NEVER --delete: a deletion would break the union/no-deletions guarantee.
		for _, a := range c.args {
			if strings.Contains(a, "--delete") {
				t.Fatalf("cmd %d must NOT contain --delete; got args=%v", i, c.args)
			}
		}
		// Both operands must be trailing-slash CONTENTS form.
		src, dst := c.args[len(c.args)-2], c.args[len(c.args)-1]
		if !strings.HasSuffix(src, "/") || !strings.HasSuffix(dst, "/") {
			t.Fatalf("cmd %d operands must have trailing slashes (contents form); got src=%q dst=%q", i, src, dst)
		}
		// The REMOTE operand (the one carrying addr:) must be single-quoted so the
		// hub shell treats the path as one literal word (no injection / space-split).
		var remote string
		if strings.HasPrefix(src, addr+":") {
			remote = src
		} else {
			remote = dst
		}
		if !strings.Contains(remote, ":'") || !strings.Contains(remote, "'/") {
			t.Fatalf("cmd %d remote operand must be single-quoted (addr:'rel'/); got %q", i, remote)
		}
		// Transport must be duck's multiplexed, non-interactive ssh passed via -e:
		// an "ssh …" string carrying BatchMode (never hang on a prompt) and the
		// warmed ControlPath master.
		eIdx := indexOf(c.args, "-e")
		if eIdx < 0 || eIdx+1 >= len(c.args) {
			t.Fatalf("cmd %d must carry -e <ssh transport>; got args=%v", i, c.args)
		}
		transport := c.args[eIdx+1]
		if !strings.HasPrefix(transport, "ssh ") {
			t.Fatalf("cmd %d -e value must be an ssh transport string; got %q", i, transport)
		}
		if !strings.Contains(transport, "BatchMode=yes") {
			t.Fatalf("cmd %d -e ssh transport must set BatchMode=yes; got %q", i, transport)
		}
		if !strings.Contains(transport, "ControlPath=") {
			t.Fatalf("cmd %d -e ssh transport must carry the warmed ControlPath; got %q", i, transport)
		}
	}

	// FIRST = PUSH local → hub.
	push := (*got)[0].args
	if push[len(push)-2] != wantLocalContents || push[len(push)-1] != wantHubContents {
		t.Fatalf("first command must be PUSH local→hub: got src=%q dst=%q, want src=%q dst=%q",
			push[len(push)-2], push[len(push)-1], wantLocalContents, wantHubContents)
	}
	// SECOND = PULL hub → local.
	pull := (*got)[1].args
	if pull[len(pull)-2] != wantHubContents || pull[len(pull)-1] != wantLocalContents {
		t.Fatalf("second command must be PULL hub→local: got src=%q dst=%q, want src=%q dst=%q",
			pull[len(pull)-2], pull[len(pull)-1], wantHubContents, wantLocalContents)
	}
}

// TestReconcileNewestPushFailureStops: a failing PUSH returns an error and does
// NOT run the PULL — a partial seed must never be treated as a finished merge,
// so the caller does not proceed to force-add.
func TestReconcileNewestPushFailureStops(t *testing.T) {
	boom := errors.New("rsync push failed")
	got, restore := swapRun(t, func(call int) error {
		if call == 0 {
			return boom
		}
		return nil
	})
	defer restore()

	err := Reconcile("me@hub", "~/dev/foo", Merge, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("push failure must surface, got %v", err)
	}
	if len(*got) != 1 {
		t.Fatalf("a failed PUSH must NOT run the PULL; ran %d commands", len(*got))
	}
}

// TestReconcileNewestPullFailureSurfaces: a failing PULL (second pass) surfaces
// as an error so the caller does not proceed to force-add on a half-done seed.
func TestReconcileNewestPullFailureSurfaces(t *testing.T) {
	boom := errors.New("rsync pull failed")
	_, restore := swapRun(t, func(call int) error {
		if call == 1 {
			return boom
		}
		return nil
	})
	defer restore()

	if err := Reconcile("me@hub", "~/dev/foo", Merge, nil); !errors.Is(err, boom) {
		t.Fatalf("pull failure must surface, got %v", err)
	}
}

// TestReconcileNewestRelaxesOwnershipAndStreamsProgress pins the exit-23 fix: a
// non-root hub cannot chown to a foreign uid/gid, so BOTH passes must disable
// owner/group/dir-time preservation (else a large corpus's container/sudo-owned
// files fail with exit 23 and ABORT the merge), and both must carry --progress
// so the seed streams live progress.
func TestReconcileNewestRelaxesOwnershipAndStreamsProgress(t *testing.T) {
	got, restore := swapRun(t, nil)
	defer restore()
	if err := Reconcile("me@hub", "~/dev/foo", Merge, nil); err != nil {
		t.Fatalf("ReconcileNewest: %v", err)
	}
	want := []string{"--no-owner", "--no-group", "--omit-dir-times", "--info=progress2"}
	for i, c := range *got {
		for _, w := range want {
			if !contains(c.args, w) {
				t.Fatalf("cmd %d missing %s; got args=%v", i, w, c.args)
			}
		}
	}
}

// TestReconcilePushMirrors pins push = local CLOBBERS hub: EXACTLY ONE pass,
// local→hub, carrying --delete (mirror) — and never -u.
func TestReconcilePushMirrors(t *testing.T) {
	const addr, tildeDir = "me@hub", "~/dev/foo"
	local, _ := paths.Expand(tildeDir)
	got, restore := swapRun(t, nil)
	defer restore()
	if err := Reconcile(addr, tildeDir, Push, nil); err != nil {
		t.Fatalf("Reconcile push: %v", err)
	}
	if len(*got) != 1 {
		t.Fatalf("push must be ONE pass, got %d: %v", len(*got), *got)
	}
	c := (*got)[0].args
	if !contains(c, "--delete") {
		t.Fatalf("push must carry --delete (mirror); got %v", c)
	}
	if contains(c, "-u") {
		t.Fatalf("push must NOT carry -u (local always wins); got %v", c)
	}
	if c[len(c)-2] != local+"/" || c[len(c)-1] != addr+":"+paths.Quote(hub.RemoteSyncPath(tildeDir))+"/" {
		t.Fatalf("push must be local→hub; got src=%q dst=%q", c[len(c)-2], c[len(c)-1])
	}
}

// TestReconcilePullMirrors pins pull = hub CLOBBERS local: EXACTLY ONE pass,
// hub→local, carrying --delete.
func TestReconcilePullMirrors(t *testing.T) {
	const addr, tildeDir = "me@hub", "~/dev/foo"
	local, _ := paths.Expand(tildeDir)
	got, restore := swapRun(t, nil)
	defer restore()
	if err := Reconcile(addr, tildeDir, Pull, nil); err != nil {
		t.Fatalf("Reconcile pull: %v", err)
	}
	if len(*got) != 1 {
		t.Fatalf("pull must be ONE pass, got %d: %v", len(*got), *got)
	}
	c := (*got)[0].args
	if !contains(c, "--delete") {
		t.Fatalf("pull must carry --delete (mirror); got %v", c)
	}
	if c[len(c)-2] != addr+":"+paths.Quote(hub.RemoteSyncPath(tildeDir))+"/" || c[len(c)-1] != local+"/" {
		t.Fatalf("pull must be hub→local; got src=%q dst=%q", c[len(c)-2], c[len(c)-1])
	}
}

// TestProgressWriterReportsLatestAndCapturesError feeds a CR/LF rsync stream and
// asserts the writer reports the LAST progress fragment and stashes a
// diagnostic-looking line in errTail instead of reporting it.
func TestProgressWriterReportsLatestAndCapturesError(t *testing.T) {
	var last string
	w := &progressWriter{report: func(s string) { last = s }}
	w.Write([]byte("big.bin\r   100  50%  1MB/s\r   200 100%  2MB/s\n"))
	if last != "200 100%  2MB/s" {
		t.Fatalf("reported %q, want the latest progress fragment", last)
	}
	w.Write([]byte("rsync: chown failed: Operation not permitted\n"))
	if w.errTail == "" || !strings.Contains(w.errTail, "chown failed") {
		t.Fatalf("errTail = %q, want the rsync diagnostic", w.errTail)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func indexOf(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return -1
}

// TestReconcileNewestMkdirsHubBeforePush pins that ReconcileNewest issues the
// remote `mkdir -p` for the hub path BEFORE the PUSH (so a fresh deep path whose
// parents the hub lacks does not fail) and that the mkdir is NOT counted as one
// of the two pinned rsync calls. The mkdirRemote seam is captured directly.
func TestReconcileNewestMkdirsHubBeforePush(t *testing.T) {
	const (
		addr     = "me@hub"
		tildeDir = "~/a/b/c"
	)
	got, restoreRun := swapRun(t, nil) // stubs mkdirRemote to a no-op; we override below
	defer restoreRun()

	var mkdirArgs *[2]string
	mkdirRemote = func(a, r string) error {
		if len(*got) != 0 {
			t.Fatalf("mkdir must run BEFORE any rsync; %d rsync call(s) already issued", len(*got))
		}
		mkdirArgs = &[2]string{a, r}
		return nil
	}

	if err := Reconcile(addr, tildeDir, Merge, nil); err != nil {
		t.Fatalf("ReconcileNewest: %v", err)
	}
	if mkdirArgs == nil {
		t.Fatal("ReconcileNewest did not issue a remote mkdir before the PUSH")
	}
	if mkdirArgs[0] != addr || mkdirArgs[1] != hub.RemoteSyncPath(tildeDir) {
		t.Fatalf("mkdir got (addr=%q rel=%q), want (addr=%q rel=%q)",
			mkdirArgs[0], mkdirArgs[1], addr, hub.RemoteSyncPath(tildeDir))
	}
	if len(*got) != 2 {
		t.Fatalf("mkdir must NOT be one of the rsync calls; want exactly 2 rsync, got %d", len(*got))
	}
}

// TestMkdirRemoteCommandString pins the actual remote command the production
// mkdirRemote builds: a login-shell-wrapped, single-quoted `mkdir -p '<rel>'`
// over duck's multiplexed ssh. It drives the real seam through hub.SetRunner so
// the ssh command string is asserted without contacting a real host.
func TestMkdirRemoteCommandString(t *testing.T) {
	var remoteCmd string
	restore := hub.SetRunner(func(argv []string, _ io.Reader) (string, error) {
		// The last argv element is the (login-shell-wrapped) remote command.
		remoteCmd = argv[len(argv)-1]
		return "", nil
	})
	defer restore()

	if err := mkdirRemote("me@hub", "a/b/c"); err != nil {
		t.Fatalf("mkdirRemote: %v", err)
	}
	// remoteCmd is LOGIN-SHELL WRAPPED (zsh -lc '…'), which is what gives the hub
	// the Homebrew PATH and matches the rest of duck's ssh. So the outer wrapping
	// single-quotes the whole command and escapes the inner single-quotes around
	// the path as '\''. After the hub's zsh un-wraps that outer layer, the command
	// it runs is exactly `mkdir -p 'a/b/c'`. Assert both layers are present.
	if !strings.HasPrefix(remoteCmd, "zsh -lc ") {
		t.Fatalf("mkdirRemote must be login-shell wrapped (zsh -lc …); got %q", remoteCmd)
	}
	if !strings.Contains(remoteCmd, "mkdir -p ") {
		t.Fatalf("mkdirRemote command must be `mkdir -p`; got %q", remoteCmd)
	}
	// The single-quoted path survives the outer wrapping as the standard
	// '\''a/b/c'\'' escape sequence — i.e. the inner quoting is preserved, not
	// dropped, so the hub shell treats the path as one literal word.
	if !strings.Contains(remoteCmd, `'\''a/b/c'\''`) {
		t.Fatalf("mkdirRemote must single-quote the path (preserved through login wrap); got %q", remoteCmd)
	}
}
