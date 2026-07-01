package claude

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ReconcileOptions configures a single reconcile pass. Everything is injected
// (no os.UserHomeDir, no hard-coded ~/.claude) so tests run fully in a temp dir.
type ReconcileOptions struct {
	// Root is the absolute ~/.claude/projects directory to scan.
	Root string
	// LocalHome is this machine's absolute home (the target path form).
	LocalHome string
	// Homes is the set of absolute home directories across the fleet (this
	// machine's + the hub's + any other laptops'). A slug dir whose sessions run
	// under one of these homes is "ours" and gets mapped onto LocalHome; anything
	// else is left untouched.
	Homes []string
	// DryRun reports what WOULD happen without copying files or registering.
	DryRun bool
	// Register adds ~/.claude.json entries for the given local paths, returning
	// the ones newly added. nil skips registration (e.g. dry runs, or callers
	// that only want the file union). It is called once, after the file union.
	Register func(absPaths ...string) ([]string, error)
}

// ReconcileResult summarizes a pass for logging.
type ReconcileResult struct {
	Scanned     int      // slug dirs that held at least one transcript
	Mapped      int      // foreign slug dirs mapped onto a local target
	CopiedFiles int      // transcript files copied into a local slug dir
	Registered  []string // local abs paths newly added to the registry
}

// Reconcile makes conversations started on other machines resumable here. For
// each slug dir under Root whose sessions ran under a FOREIGN fleet home, it
// computes this machine's equivalent path/slug and:
//   - copies any transcript the local slug dir doesn't already have (copy-if-
//     absent via O_EXCL — it never overwrites or deletes), and
//   - registers the local absolute path so `claude --resume` will find it.
//
// It relies on the verified facts that resume is keyed on the slug directory
// plus the ~/.claude.json entry, and that the transcript's internal "cwd" field
// is cosmetic — so no transcript contents are ever rewritten. The pass is
// idempotent: a second run copies nothing new (the local dir already has the
// files) and registers nothing new.
func Reconcile(opt ReconcileOptions) (ReconcileResult, error) {
	var res ReconcileResult
	entries, err := os.ReadDir(opt.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return res, nil // corpus not present yet — nothing to do
		}
		return res, err
	}

	var toRegister []string
	seen := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		srcDir := filepath.Join(opt.Root, e.Name())
		files, err := transcripts(srcDir)
		if err != nil || len(files) == 0 {
			continue
		}
		res.Scanned++

		cwd := firstCwd(srcDir, files)
		if cwd == "" {
			continue // can't tell where it ran — leave it alone
		}
		home, ok := homeOf(cwd, opt.Homes)
		if !ok || home == opt.LocalHome {
			continue // not a fleet path, or already this machine's — nothing to map
		}
		localAbs := opt.LocalHome + cwd[len(home):]
		localSlug := Slug(localAbs)
		if localSlug == e.Name() {
			continue // maps to itself (defensive) — already local
		}
		res.Mapped++
		dstDir := filepath.Join(opt.Root, localSlug)

		for _, f := range files {
			copied, err := copyIfAbsent(filepath.Join(srcDir, f), filepath.Join(dstDir, f), opt.DryRun)
			if err != nil {
				return res, err
			}
			if copied {
				res.CopiedFiles++
			}
		}
		if !seen[localAbs] {
			seen[localAbs] = true
			toRegister = append(toRegister, localAbs)
		}
	}

	if opt.DryRun || opt.Register == nil || len(toRegister) == 0 {
		res.Registered = nil
		return res, nil
	}
	added, err := opt.Register(toRegister...)
	if err != nil {
		return res, err
	}
	sort.Strings(added)
	res.Registered = added
	return res, nil
}

// transcripts returns the *.jsonl file names directly in dir (not recursive).
func transcripts(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// firstCwd returns the first non-empty "cwd" field found across dir's transcript
// files (all sessions in one slug dir share the same cwd, so the first hit is
// representative). files are the *.jsonl basenames in dir. A file with no cwd is
// skipped; "" means none of them recorded one.
func firstCwd(dir string, files []string) string {
	for _, f := range files {
		if cwd := scanCwd(filepath.Join(dir, f)); cwd != "" {
			return cwd
		}
	}
	return ""
}

// homeOf returns the fleet home that contains cwd (segment-aware: cwd equals the
// home or is nested under it), preferring the LONGEST match so nested homes
// don't shadow each other.
func homeOf(cwd string, homes []string) (string, bool) {
	best := ""
	for _, h := range homes {
		if h == "" {
			continue
		}
		if cwd == h || strings.HasPrefix(cwd, h+"/") {
			if len(h) > len(best) {
				best = h
			}
		}
	}
	return best, best != ""
}

// copyIfAbsent copies src→dst only when dst does not already exist, creating the
// destination directory as needed. It opens dst with O_EXCL so it can NEVER
// overwrite an existing transcript (the non-destructive guarantee). Returns
// whether a copy happened. In dry-run it reports what it would copy without
// writing. A dst that already exists is a no-op (returns false).
func copyIfAbsent(src, dst string, dryRun bool) (bool, error) {
	if _, err := os.Stat(dst); err == nil {
		return false, nil // already have it — never touch it
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if dryRun {
		return true, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return false, err
	}
	in, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return false, nil // lost a race — the file is there, leave it
		}
		return false, err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst) // clean up the partial WE just created (not pre-existing data)
		return false, err
	}
	if err := out.Close(); err != nil {
		return false, err
	}
	return true, nil
}

// scanCwd reads a single transcript and returns the first non-empty "cwd" field,
// scanning at most maxCwdScanLines lines (cwd appears within the first handful of
// message lines). Errors and cwds-not-found yield "".
func scanCwd(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // tolerate long JSONL lines
	var rec struct {
		Cwd string `json:"cwd"`
	}
	for i := 0; sc.Scan() && i < maxCwdScanLines; i++ {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		rec.Cwd = ""
		if json.Unmarshal(line, &rec) == nil && rec.Cwd != "" {
			return rec.Cwd
		}
	}
	return ""
}

const maxCwdScanLines = 500
