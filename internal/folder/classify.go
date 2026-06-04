package folder

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Sync-safety thresholds for the bounded walk. A folder is risky once its tree
// crosses EITHER cap, so bare `duck` never silently starts a multi-GB or
// hundred-thousand-file mirror. Named consts so they can be made configurable
// later; the walk early-exits the instant a threshold is crossed.
const (
	sizeThreshold int64 = 2 << 30 // 2 GiB
	fileCountCap        = 20000   // file entries
	timeBudget          = 1500 * time.Millisecond
)

// Result is the classifier's verdict for a local directory. Risky means the
// folder should NOT auto-mirror without confirmation; Reason is a short
// human string for the prompt. ApproxBytes/FileCount are what the walk had
// counted when it stopped (lower bounds once a threshold trips).
type Result struct {
	Risky       bool
	Reason      string
	ApproxBytes int64
	FileCount   int
}

// errBudget is the sentinel WalkDir returns to early-exit once a size/count cap
// is crossed or the wall-clock budget is spent. It is internal; callers see the
// Result, not the sentinel.
var errBudget = errors.New("folder: walk budget exceeded")

// Classify reports whether localAbs is risky to auto-mirror. It is cheap: the
// home/ancestor/root checks need no walk, and the bounded WalkDir early-exits
// the instant the tree crosses sizeThreshold or fileCountCap, and respects a
// wall-clock budget, so it never slogs through a huge tree. localAbs must be an
// absolute local path.
func Classify(localAbs string) Result {
	return classify(localAbs, sizeThreshold, fileCountCap, timeBudget)
}

// classify is Classify with injectable thresholds so tests can drive the
// count-based early-exit deterministically without crafting 20k files.
func classify(localAbs string, sizeCap int64, countCap int, budget time.Duration) Result {
	// Cheap structural checks first — no walk needed.
	if reason, ok := riskyLocation(localAbs); ok {
		return Result{Risky: true, Reason: reason}
	}

	start := time.Now()
	var total int64
	var files int
	timedOut := false

	err := filepath.WalkDir(localAbs, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// A failure on the ROOT entry (e.g. permission-denied on localAbs
			// itself) means we cannot scan the folder at all: counts stay 0 and the
			// dir would otherwise be classified NOT risky and auto-synced with no
			// prompt — brushing invariant (a). Propagate it so the switch below flags
			// the folder risky and decideSync prompts. This must come first: when the
			// root's Lstat fails, d is nil, so any d.IsDir() check would panic.
			if path == localAbs {
				return walkErr
			}
			// An unreadable SUBTREE is not, by itself, a reason to flag the folder;
			// skip it and keep the budget honest.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		// Respect the wall-clock budget so a pathological tree can't stall startup.
		if time.Since(start) > budget {
			timedOut = true
			return errBudget
		}
		if d.IsDir() {
			return nil
		}
		files++
		if info, ierr := d.Info(); ierr == nil {
			total += info.Size()
		}
		if total > sizeCap || files > countCap {
			return errBudget
		}
		return nil
	})

	r := Result{ApproxBytes: total, FileCount: files}
	switch {
	case err != nil && !errors.Is(err, errBudget):
		// A walk error we couldn't attribute to a single entry — treat as risky so
		// we err on the side of asking rather than mirroring blindly.
		r.Risky = true
		r.Reason = fmt.Sprintf("could not scan folder: %v", err)
	case timedOut:
		r.Risky = true
		r.Reason = fmt.Sprintf("%s / time budget exceeded", humanCount(files))
	case total > sizeCap:
		r.Risky = true
		r.Reason = "~" + humanBytes(total)
	case files > countCap:
		r.Risky = true
		r.Reason = humanCount(files)
	}
	return r
}

// riskyLocation flags structurally dangerous mirror roots that need no walk:
// the user's home dir, any ANCESTOR of home, and the filesystem root.
func riskyLocation(localAbs string) (reason string, risky bool) {
	clean := filepath.Clean(localAbs)
	if clean == string(filepath.Separator) {
		return "filesystem root", true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	home = filepath.Clean(home)
	if clean == home {
		return "home directory", true
	}
	// An ancestor of home (e.g. /Users, /home, or /) would mirror everyone's
	// data. home has clean+sep as a prefix iff clean is a strict ancestor.
	if strings.HasPrefix(home+string(filepath.Separator), clean+string(filepath.Separator)) && clean != home {
		return "ancestor of home directory", true
	}
	return "", false
}

// humanBytes renders a byte count as a short GB/MB/KB string for the prompt.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// humanCount renders a file count as a short "32k files" / "812 files" string.
func humanCount(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%dk files", n/1000)
	}
	return fmt.Sprintf("%d files", n)
}
