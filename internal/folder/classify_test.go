package folder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestClassifySmallDirNotRisky pins that a temp dir with a few small files is
// NOT flagged: the common case auto-syncs without a prompt.
func TestClassifySmallDirNotRisky(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("hello"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	r := Classify(dir)
	if r.Risky {
		t.Fatalf("a small temp dir must NOT be risky; got reason=%q bytes=%d files=%d", r.Reason, r.ApproxBytes, r.FileCount)
	}
	if r.FileCount != 3 {
		t.Fatalf("FileCount = %d, want 3", r.FileCount)
	}
}

// TestClassifyHomeIsRisky pins the structural check: the user home dir is risky
// with no walk.
func TestClassifyHomeIsRisky(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	r := Classify(home)
	if !r.Risky {
		t.Fatalf("the home dir must be risky")
	}
	if r.Reason != "home directory" {
		t.Fatalf("Reason = %q, want %q", r.Reason, "home directory")
	}
}

// TestClassifyAncestorOfHomeIsRisky pins that a strict ancestor of home (e.g.
// /Users on macOS, /home on Linux) is risky.
func TestClassifyAncestorOfHomeIsRisky(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	parent := filepath.Dir(filepath.Clean(home))
	if parent == filepath.Clean(home) {
		t.Skip("home has no distinct parent")
	}
	r := Classify(parent)
	if !r.Risky {
		t.Fatalf("an ancestor of home (%s) must be risky", parent)
	}
}

// TestClassifyRootIsRisky pins that "/" is risky.
func TestClassifyRootIsRisky(t *testing.T) {
	r := Classify(string(filepath.Separator))
	if !r.Risky {
		t.Fatalf("filesystem root must be risky")
	}
}

// TestClassifyEarlyExitsOnCount crafts more than a low count cap of tiny files
// and verifies the walk flags the dir AND early-exits — FileCount must be just
// over the cap, not the full tree, proving the walk stopped the instant it
// crossed the threshold.
func TestClassifyEarlyExitsOnCount(t *testing.T) {
	dir := t.TempDir()
	const total = 50
	for i := 0; i < total; i++ {
		if err := os.WriteFile(filepath.Join(dir, fileName(i)), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	const cap = 10
	r := classify(dir, sizeThreshold, cap, timeBudget)
	if !r.Risky {
		t.Fatalf("a dir past the count cap must be risky")
	}
	// Early-exit: the walk must stop the instant it crosses the cap, so it sees
	// at most cap+1 files, never the full 50.
	if r.FileCount > cap+1 {
		t.Fatalf("walk did not early-exit on count: enumerated %d files (cap=%d, total=%d)", r.FileCount, cap, total)
	}
}

// TestClassifyEarlyExitsOnSize pins the size-based early-exit with a tiny cap.
func TestClassifyEarlyExitsOnSize(t *testing.T) {
	dir := t.TempDir()
	// Two 1 KB files; a 1 KB-1 cap trips on the first.
	for i := 0; i < 2; i++ {
		if err := os.WriteFile(filepath.Join(dir, fileName(i)), make([]byte, 1024), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	r := classify(dir, 1023, fileCountCap, timeBudget)
	if !r.Risky {
		t.Fatalf("a dir past the size cap must be risky")
	}
	if r.FileCount > 1 {
		t.Fatalf("walk did not early-exit on size: enumerated %d files", r.FileCount)
	}
}

// TestClassifyTimeBudget pins that a zero/near-zero budget trips the wall-clock
// guard and flags the dir as risky.
func TestClassifyTimeBudget(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(dir, fileName(i)), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	// A budget already in the past forces the time guard on the first entry.
	r := classify(dir, sizeThreshold, fileCountCap, -1*time.Second)
	if !r.Risky {
		t.Fatalf("a spent time budget must flag the dir risky")
	}
}

// TestClassifyUnreadableRootIsRisky pins invariant (a): a dir whose ROOT cannot
// be scanned (permission-denied on the walk's first entry) must be flagged risky
// — counts stay 0, so without this it would classify NOT risky and decideSync
// would auto-sync+remember with no prompt. We chmod a SUBDIR of t.TempDir to 000
// and point Classify at it; t.Cleanup restores 0o755 so TempDir's RemoveAll can
// delete it. Skipped as root (chmod is ineffective for the superuser).
func TestClassifyUnreadableRootIsRisky(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod 000 does not deny the superuser; cannot make root unreadable")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "locked")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("chmod 000: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	// Some platforms/filesystems still let the owner traverse a 000 dir; if the
	// walk can read it, the unreadable-root condition isn't reproduced and the
	// behavior under test doesn't apply.
	if _, err := os.ReadDir(dir); err == nil {
		t.Skip("root remained readable after chmod 000 (ineffective on this fs)")
	}

	r := Classify(dir)
	if !r.Risky {
		t.Fatalf("an unscannable folder must be risky so duck PROMPTS; got %+v", r)
	}
	if !strings.HasPrefix(r.Reason, "could not scan folder") {
		t.Fatalf("Reason = %q, want it to start with %q", r.Reason, "could not scan folder")
	}
}

// fileName makes a unique tiny filename for index i.
func fileName(i int) string {
	return "f" + itoa(i)
}

// itoa avoids importing strconv for a one-liner.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
