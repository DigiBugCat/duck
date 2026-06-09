// Package reconcile seeds this machine and the hub to a coherent state right
// before a force-merge of a folder the hub already has with files, so the
// mutagen two-way-resolved session that follows finds no conflicts to resolve
// and simply maintains the seeded state. The caller picks the DIRECTION:
//
//   - Merge — per-file NEWEST-WINS UNION. Two `rsync -a -u` passes (PUSH then
//     PULL), NO --delete: each side ends holding the newest copy of every file,
//     nothing is deleted. Safe across machines (no newer edit is lost). This is
//     the default and the only multi-pass direction.
//   - Push — local CLOBBERS the hub. ONE `rsync -a --delete` pass local→hub:
//     the hub becomes an exact mirror of local (hub-only files are DELETED).
//   - Pull — hub CLOBBERS local. ONE `rsync -a --delete` pass hub→local: local
//     becomes an exact mirror of the hub (local-only files are DELETED).
//
// Push/Pull are a single pass — half the scan of Merge — and are the fast,
// authoritative choice when one side is the source of truth. They intentionally
// carry --delete (the losing side is overwritten WHOLESALE); Merge intentionally
// does NOT (a union must never delete).
//
// duck REQUIRES GNU rsync 3.x (resolved via rsyncBin): the macOS built-in
// openrsync (protocol 29) has no incremental file-list streaming and no
// --info=progress2, so a 20k-file tree scans slowly and shows no progress.
//
// The rsync invocation goes through an injectable package-level runner (var
// run), mirroring the internal/mutagen (runVar) and internal/hub (runSSH)
// seams, so the safety-critical command construction is unit-tested without
// invoking real rsync/ssh.
package reconcile

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"

	"github.com/DigiBugCat/duck/internal/hub"
	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/DigiBugCat/duck/internal/sshx"
)

// Direction selects how a conflicting folder is seeded before the mutagen merge.
type Direction int

const (
	Merge Direction = iota // newest-of-each-file wins, union, no deletions (2 passes)
	Push                   // local clobbers hub: mirror local→hub with --delete (1 pass)
	Pull                   // hub clobbers local: mirror hub→local with --delete (1 pass)
)

// run is the seam tests swap to record the constructed rsync argv (and call
// order) and to inject failures without invoking the real rsync binary.
// Production runs via exec.Command and FOLDS rsync's stderr into the returned
// error so a partial transfer (e.g. exit status 23: a destination it could not
// write, a vanished file, no space on the hub) names the actual cause instead
// of failing silently. Mirrors internal/mutagen.runVar's stderr capture.
// onProgress receives the live rsync `--info=progress2` stream so the caller's
// spinner shows transfer progress; nil streams nothing. Some rsync diagnostics
// land on STDOUT, so production also keeps the last diagnostic-looking stdout
// line and folds it into the error when stderr is empty.
var run = func(onProgress func(string), name string, args ...string) error {
	cmd := exec.Command(name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pw := &progressWriter{report: onProgress}
	cmd.Stdout = pw
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		if msg := strings.TrimSpace(pw.errTail); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// rsyncBin resolves a GNU rsync 3.x binary, REQUIRED by duck. The macOS default
// (/usr/bin/rsync) is openrsync (protocol 29): no incremental file-list, no
// --info=progress2, slow on big trees — so it is rejected. It tries `rsync` on
// PATH then the Homebrew locations, returning the first whose `--version` is a
// GNU 3.x build. A seam so tests stub it without shelling out.
var rsyncBin = func() (string, error) {
	for _, cand := range []string{"rsync", "/opt/homebrew/bin/rsync", "/usr/local/bin/rsync"} {
		out, err := exec.Command(cand, "--version").CombinedOutput()
		if err != nil {
			continue
		}
		v := strings.ToLower(string(out))
		// Reject openrsync; accept a GNU rsync v3 (the version line reads
		// "rsync  version 3.x.x" / "protocol version 3x").
		if strings.Contains(v, "openrsync") {
			continue
		}
		if strings.Contains(v, "version 3") || strings.Contains(v, "protocol version 3") {
			return cand, nil
		}
	}
	return "", fmt.Errorf("duck requires GNU rsync 3.x (the macOS built-in openrsync is too slow); install it: brew install rsync")
}

// progressWriter turns rsync's `--info=progress2` output — a carriage-return /
// newline stream of progress lines — into discrete status strings for the live
// spinner. rsync redraws the line in place with \r, so a single Write may carry
// several fragments; only the LAST complete one is reported. A diagnostic-looking
// line ("error"/"failed"/an "rsync:" prefix) is stashed in errTail instead of
// reported, so a failed run whose cause rsync printed to stdout (stderr empty)
// can still name it.
type progressWriter struct {
	report  func(string)
	buf     []byte
	errTail string
}

func (w *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.buf = append(w.buf, p...)
	last := bytes.LastIndexAny(w.buf, "\r\n")
	if last < 0 {
		return n, nil // no complete fragment yet — keep buffering.
	}
	complete := w.buf[:last]
	w.buf = append(w.buf[:0:0], w.buf[last+1:]...) // retain only the partial remainder.
	frags := bytes.FieldsFunc(complete, func(r rune) bool { return r == '\r' || r == '\n' })
	var lastProgress string
	for _, f := range frags {
		line := strings.TrimSpace(string(f))
		if line == "" {
			continue
		}
		if looksLikeError(line) {
			w.errTail = line
			continue
		}
		lastProgress = line
	}
	if lastProgress != "" && w.report != nil {
		w.report(lastProgress)
	}
	return n, nil
}

// looksLikeError reports whether an rsync stdout line is a diagnostic rather than
// progress, so it is folded into the error instead of flashed in the spinner.
func looksLikeError(line string) bool {
	l := strings.ToLower(line)
	return strings.HasPrefix(l, "rsync:") || strings.HasPrefix(l, "rsync(") ||
		strings.Contains(l, "error") || strings.Contains(l, "failed")
}

// labeled wraps the caller's report fn so each streamed rsync line is prefixed
// with the pass DIRECTION. A nil report yields a nil fn so run streams nothing.
func labeled(report func(string), prefix string) func(string) {
	if report == nil {
		return nil
	}
	return func(line string) { report(prefix + " · " + line) }
}

// mkdirRemote is a SEPARATE seam from run: the pre-PUSH `mkdir -p` on the hub
// must NOT count as one of the rsync calls the safety tests pin, and it must be
// stubbable so tests never contact a real host. Production routes it through the
// hub package (login-shell-wrapped + multiplexed ssh), so tests swap it (or
// hub.SetRunner) to assert the command string.
var mkdirRemote = func(addr, rel string) error {
	_, err := hub.New(addr).Run("mkdir -p " + paths.Quote(rel))
	return err
}

// commonFlags are the attribute-relaxation + progress flags shared by every
// direction, and WHY each is safe:
//
//	--no-owner --no-group  do NOT chown on the receiver. -a (= -rlptgoD) tries
//	  to set owner/group, but a non-root hub cannot chown a file to a foreign
//	  uid/gid — and a large corpus routinely holds container-/archive-/sudo-owned
//	  files — so rsync failed those with exit status 23 (partial transfer) and
//	  ABORTED the seed. mutagen syncs CONTENT (and the exec bit), never
//	  ownership, so dropping owner/group changes nothing the seed needs.
//	--omit-dir-times  skip setting directory mtimes — another benign exit-23
//	  source over ssh, irrelevant to mutagen's content compare.
//	--info=progress2  stream an aggregate transfer progress line (GNU rsync 3.x).
//
// -p (perms) is intentionally KEPT (own-file chmod never fails) so the exec bit
// still seeds and mutagen has no exec-bit conflict to resolve.
var commonFlags = []string{"-a", "--no-owner", "--no-group", "--omit-dir-times", "--info=progress2"}

// Reconcile seeds tildeDir between this machine and the hub per dir, so the
// force-merge that follows has nothing for mutagen to resolve. addr is the hub
// user@host; tildeDir is the tilde-form path (e.g. "~/dev/foo"). report, when
// non-nil, receives the live per-pass rsync progress (labeled with direction).
//
// On any rsync failure the error is returned and the caller MUST NOT proceed to
// the force-add: a partial seed must not be treated as complete (mutagen then
// runs two-way-resolved, where LOCAL wins every conflict — an un-reconciled file
// would be silently clobbered with the local copy).
func Reconcile(addr, tildeDir string, dir Direction, report func(string)) error {
	bin, err := rsyncBin()
	if err != nil {
		return err
	}
	local, err := paths.Expand(tildeDir)
	if err != nil {
		return err
	}
	rel := hub.RemoteSyncPath(tildeDir)

	// rsync's transport is duck's OWN multiplexed, non-interactive ssh: BatchMode
	// (never hang on a host-key/password prompt), the warmed ControlPath master,
	// and ServerAlive keepalive. Passed as a single -e argv element.
	opts, err := sshx.Options()
	if err != nil {
		return err
	}
	sshTransport := "ssh " + strings.Join(opts, " ")

	localContents := local + "/"
	// SINGLE-QUOTE the remote path (paths.Quote — the shared single-quote helper)
	// so the hub shell treats it as ONE literal word: a space/$/;/`/* can neither
	// be interpreted by the remote shell nor corrupt the transfer. The trailing
	// slash stays OUTSIDE the quotes so rsync still sees CONTENTS form.
	hubContents := addr + ":" + paths.Quote(rel) + "/"

	// pass runs one rsync direction. extra adds per-direction flags (-u for Merge,
	// --delete for Push/Pull). src/dst stay LAST (the safety test reads them there).
	pass := func(prefix string, extra []string, src, dst string) error {
		args := append([]string{}, commonFlags...)
		args = append(args, extra...)
		args = append(args, "-e", sshTransport, src, dst)
		return run(labeled(report, prefix), bin, args...)
	}

	// Any pass that WRITES to the hub needs its parent dirs to exist: rsync creates
	// the final sync dir but not its parents. Idempotent + harmless if present.
	mkHub := func() error {
		if err := mkdirRemote(addr, rel); err != nil {
			return fmt.Errorf("reconcile mkdir %s on hub: %w", tildeDir, err)
		}
		return nil
	}

	switch dir {
	case Push:
		// local CLOBBERS hub: one mirror pass with --delete.
		if err := mkHub(); err != nil {
			return err
		}
		if err := pass("pushing local → hub (local wins)", []string{"--delete"}, localContents, hubContents); err != nil {
			return fmt.Errorf("push %s → hub: %w", tildeDir, err)
		}
		return nil
	case Pull:
		// hub CLOBBERS local: one mirror pass with --delete.
		if err := pass("pulling hub → local (hub wins)", []string{"--delete"}, hubContents, localContents); err != nil {
			return fmt.Errorf("pull hub → %s: %w", tildeDir, err)
		}
		return nil
	default: // Merge: newest-wins union, two -u passes, NO --delete.
		if err := mkHub(); err != nil {
			return err
		}
		if err := pass("merging: pushing newest local files", []string{"-u"}, localContents, hubContents); err != nil {
			return fmt.Errorf("reconcile push %s → hub: %w", tildeDir, err)
		}
		if err := pass("merging: pulling newest hub files", []string{"-u"}, hubContents, localContents); err != nil {
			return fmt.Errorf("reconcile pull hub → %s: %w", tildeDir, err)
		}
		return nil
	}
}
