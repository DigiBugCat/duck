// `duck snap`: capture a screen selection, upload the PNG to the hub over duck's
// multiplexed SSH, and copy the REMOTE file path to the laptop clipboard —
// ready to paste into a Claude Code session on the hub (Claude reads the image
// by path). This ports the standalone remote-shot.sh tool into the duck binary:
// the screen capture and clipboard write happen LOCALLY (macOS), only the file
// crosses to the hub, and the path is what you paste. No daemon — it fires only
// when run, so bind it to a hotkey (Hammerspoon / macOS Shortcuts) for a one-key
// capture, exactly like the original remote-shot Cmd+Shift+3.
package command

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// remoteShotsDir is the hub directory snap uploads into. /tmp keeps screenshots
// ephemeral (cleared on reboot) and avoids assuming the hub's $HOME, matching
// remote-shot's /tmp/shots.
const remoteShotsDir = "/tmp/duck-shots"

// screencaptureBin is macOS's screenshot CLI by absolute path: when duck is
// launched from a hotkey runner (Hammerspoon), $PATH is minimal, so we don't
// rely on a lookup.
const screencaptureBin = "/usr/sbin/screencapture"

var snapFull bool

var snapCmd = &cobra.Command{
	Use:   "snap",
	Short: "Capture a screenshot, upload it to the hub, copy the remote path to your clipboard",
	Long: `snap captures a screen selection (drag a region like Cmd+Shift+4, Space for a
window, Esc to cancel), uploads the PNG to the hub over duck's multiplexed SSH,
and copies the REMOTE file path to your laptop clipboard. Paste that path into a
Claude Code session on the hub and ask about it — Claude reads the image by path.

macOS laptop only. Bind it to a hotkey (Hammerspoon or macOS Shortcuts) for a
one-key capture, mirroring the original remote-shot.`,
	Args: cobra.NoArgs,
	RunE: func(c *cobra.Command, args []string) error {
		return runSnap(snapFull)
	},
}

func init() {
	snapCmd.Flags().BoolVar(&snapFull, "full", false,
		"capture the full screen instead of an interactive selection")
}

// captureArgs builds the screencapture argv. -x silences the shutter sound; -i
// is an interactive selection (region or window) unless full-screen is asked.
func captureArgs(full bool, path string) []string {
	if full {
		return []string{"-x", path}
	}
	return []string{"-i", "-x", path}
}

// uploadCommand returns the remote shell command that creates the shots dir and
// writes the piped PNG to it, plus the absolute remote path of the written file.
// name must be a plain filename (no shell metacharacters); snap generates it.
func uploadCommand(name string) (remoteCmd, remotePath string) {
	remotePath = remoteShotsDir + "/" + name
	remoteCmd = fmt.Sprintf("mkdir -p %s && cat > %s", remoteShotsDir, remotePath)
	return remoteCmd, remotePath
}

func runSnap(full bool) error {
	w, err := build()
	if err != nil {
		return err
	}

	name := "shot-" + time.Now().Format("20060102-150405") + ".png"
	localPath := filepath.Join(os.TempDir(), name)
	defer func() { _ = os.Remove(localPath) }()

	// Capture locally (interactive selection by default; Esc cancels).
	capture := exec.Command(screencaptureBin, captureArgs(full, localPath)...)
	capture.Stdin, capture.Stdout, capture.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := capture.Run(); err != nil {
		return fmt.Errorf("screencapture: %w", err)
	}
	// Esc during an interactive capture leaves no file written.
	if fi, statErr := os.Stat(localPath); statErr != nil || fi.Size() == 0 {
		fmt.Println("snap: cancelled — no image captured")
		return nil
	}

	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	// Stream the PNG to the hub over the warmed control-master (no scp; reuses
	// the same multiplexed connection every other duck call uses).
	remoteCmd, remotePath := uploadCommand(name)
	if _, err := w.client.RunInput(remoteCmd, f); err != nil {
		return fmt.Errorf("upload to hub: %w", err)
	}

	// Put the remote path on the laptop clipboard, ready to paste into Claude.
	if err := pbcopy(remotePath); err != nil {
		return fmt.Errorf("copy path to clipboard: %w", err)
	}
	// Best-effort desktop notification (mirrors remote-shot).
	_ = exec.Command("osascript", "-e",
		fmt.Sprintf("display notification %q with title \"duck snap\"", remotePath)).Run()

	fmt.Printf("snap: %s (path copied to clipboard)\n", remotePath)
	return nil
}

// pbcopy writes s to the macOS clipboard.
func pbcopy(s string) error {
	c := exec.Command("pbcopy")
	c.Stdin = strings.NewReader(s)
	return c.Run()
}
