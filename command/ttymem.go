// ttymem.go is the laptop-local per-terminal session memory: a JSON map at
// ~/.duck/tty-last.json from a controlling-TTY device path to the tmux session
// name last attached from that terminal. `duck -c` consults it FIRST so a
// terminal whose session was abandoned via ^c (a transport drop the user gave up
// on) reattaches THAT session, not merely the most-recent one for the dir.
//
// ~/.duck is laptop-side (the same dir sshx uses for its control sockets); the
// store mkdirs it 0700 as needed and writes atomically (temp + rename). When
// stdin is not a TTY there is no controlling terminal to key on, so CurrentTTY
// returns "" and the whole mechanism is skipped (no reads, no writes).
package command

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/mattn/go-isatty"
)

// ttyMemFile is the basename of the per-terminal memory store under ~/.duck.
const ttyMemFile = "tty-last.json"

// ttyMem is the on-disk document: tty device path → tmux session name.
type ttyMem struct {
	Sessions map[string]string `json:"sessions"`
}

// ttyMemPath returns <HOME>/.duck/tty-last.json. It honors $HOME (so tests drive
// a temp HOME) via os.UserHomeDir.
func ttyMemPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".duck", ttyMemFile), nil
}

// CurrentTTY returns a stable identifier for stdin's controlling terminal, or ""
// when stdin is not a TTY — the signal to skip per-terminal memory entirely.
//
// The key is the terminal device number (Fstat.Rdev of fd 0), which uniquely
// and stably identifies the controlling tty across `duck` invocations in the
// same window and differs per window — the same property a /dev/ttysNNN path
// would give. (golang.org/x/sys/unix has no Ttyname wrapper, so we read the
// device identity directly; a recycled device after a reboot is harmless because
// `duck -c` prunes a remembered session whose hub session is gone.)
func CurrentTTY() string {
	fd := os.Stdin.Fd()
	if !isatty.IsTerminal(fd) {
		return ""
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(int(fd), &st); err != nil {
		return ""
	}
	return fmt.Sprintf("tty:%d", st.Rdev)
}

// ttyMemLoad reads the store, returning an empty (non-nil-map) doc when the file
// is absent or unreadable so callers can treat a miss and an empty store alike.
func ttyMemLoad() (ttyMem, error) {
	p, err := ttyMemPath()
	if err != nil {
		return ttyMem{Sessions: map[string]string{}}, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ttyMem{Sessions: map[string]string{}}, nil // absent → empty store
	}
	var m ttyMem
	if err := json.Unmarshal(b, &m); err != nil || m.Sessions == nil {
		m.Sessions = map[string]string{}
	}
	return m, nil
}

// ttyMemGet returns the remembered session name for tty (ok=false on a miss or
// when tty is "").
func ttyMemGet(tty string) (string, bool) {
	if tty == "" {
		return "", false
	}
	m, _ := ttyMemLoad()
	name, ok := m.Sessions[tty]
	return name, ok && name != ""
}

// ttyMemSet records tty → name atomically (temp file in the same dir + rename),
// mkdir'ing ~/.duck 0700 as needed. A "" tty is a no-op (no controlling TTY).
func ttyMemSet(tty, name string) error {
	if tty == "" {
		return nil
	}
	p, err := ttyMemPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	m, _ := ttyMemLoad()
	m.Sessions[tty] = name
	return writeTTYMem(p, m)
}

// ttyMemPrune removes tty's entry (a stale remembered session whose hub session
// is gone). A "" tty or absent entry is a no-op.
func ttyMemPrune(tty string) error {
	if tty == "" {
		return nil
	}
	p, err := ttyMemPath()
	if err != nil {
		return err
	}
	m, _ := ttyMemLoad()
	if _, ok := m.Sessions[tty]; !ok {
		return nil
	}
	delete(m.Sessions, tty)
	return writeTTYMem(p, m)
}

// writeTTYMem serializes m to path atomically: write a temp file in the same
// directory, then rename over the target so a crash never leaves a half doc.
func writeTTYMem(path string, m ttyMem) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ttyMemFile+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
