// Package reconcile implements a per-file NEWEST-WINS seed between this machine
// and the hub, used right before a force-merge of a folder the hub already has
// with files. One user across 3+ machines wants the newest version of each
// file to win — not "this machine wins" — and mutagen has no native
// newest-wins mode. So duck seeds both sides to identical, newest-per-file
// content with two `rsync -au` passes (one each direction), after which the
// subsequent mutagen two-way-resolved session has no conflicts to resolve and
// simply maintains the already-reconciled state.
//
// `rsync -a -u SRC/ DST/` in BOTH directions gives per-file newest-wins +
// UNION with NO deletions:
//   - -a (archive) carries mtimes (-t), which -u needs to compare.
//   - -u (update) skips any file that is newer on the receiver, so a pass only
//     ever overwrites an OLDER destination file with a NEWER source file.
//   - running it both ways makes each side hold the newest copy of every file.
//   - the ABSENCE of --delete makes it a union: a file present on only one side
//     is copied, never removed. (mutagen handles deletions AFTER this seed.)
//
// The rsync invocation goes through an injectable package-level runner (var
// run), mirroring the internal/mutagen (runVar) and internal/hub (runSSH)
// seams, so the safety-critical command construction is unit-tested without
// invoking real rsync/ssh.
package reconcile

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/DigiBugCat/duck/internal/hub"
	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/DigiBugCat/duck/internal/sshx"
)

// run is the seam tests swap to record the constructed rsync argv (and call
// order) and to inject failures without invoking the real rsync binary.
// Production runs via exec.Command. Mirrors internal/mutagen.runVar.
var run = func(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

// mkdirRemote is a SEPARATE seam from run: the pre-PUSH `mkdir -p` on the hub
// must NOT count as one of the two rsync calls the safety tests pin, and it must
// be stubbable so tests never contact a real host. Production routes it through
// the hub package (login-shell-wrapped + multiplexed ssh), so tests swap it (or
// hub.SetRunner) to assert the command string. It returns the remote command
// that was issued so callers/tests can verify quoting.
var mkdirRemote = func(addr, rel string) error {
	_, err := hub.New(addr).Run("mkdir -p " + paths.Quote(rel))
	return err
}

// ReconcileNewest seeds the hub and this machine to identical, newest-per-file
// content for tildeDir with two `rsync -au` passes — PUSH newer local files to
// the hub, then PULL newer hub files back — so the force-merge that follows has
// no conflicts left for mutagen to resolve. addr is the hub user@host;
// tildeDir is the tilde-form path (e.g. "~/dev/foo").
//
// Both commands use trailing-slash CONTENTS form (sync directory contents, not
// the directory itself), are `-a -u` (newest-wins, no clobber of newer files),
// and carry NO --delete (union, no deletions). If either rsync fails the error
// is returned and the caller MUST NOT proceed to the force-add: a partial seed
// must not be treated as a completed newest-wins merge.
func ReconcileNewest(addr, tildeDir string) error {
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

	// Create any MISSING INTERMEDIATE PARENTS on the hub before the PUSH: rsync
	// creates the final sync dir but not its parents, so `duck --sync ~/a/b/c`
	// where the hub lacks ~/a would otherwise fail. `mkdir -p` is idempotent and
	// harmless in the conflict case where the dir already exists. This runs on a
	// SEPARATE seam (not the rsync `run`), so it is not one of the two pinned
	// rsync calls. Seed-before-merge ordering is unchanged: this is purely a
	// remote-dir precondition, not a reconcile of contents.
	if err := mkdirRemote(addr, rel); err != nil {
		return fmt.Errorf("reconcile mkdir %s on hub: %w", tildeDir, err)
	}

	// PUSH: newer local → hub.
	if err := run("rsync", "-a", "-u", "-e", sshTransport, localContents, hubContents); err != nil {
		return fmt.Errorf("reconcile push %s → hub: %w", tildeDir, err)
	}
	// PULL: newer hub → local.
	if err := run("rsync", "-a", "-u", "-e", sshTransport, hubContents, localContents); err != nil {
		return fmt.Errorf("reconcile pull hub → %s: %w", tildeDir, err)
	}
	return nil
}
