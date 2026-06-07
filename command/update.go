// `duck update`: upgrade duck to the latest release. duck ships as a Homebrew
// cask (digibugcat/tap), so update wraps `brew upgrade --cask duck`. brew's
// auto-update is THROTTLED (HOMEBREW_AUTO_UPDATE_SECS, ~24h by default), so a
// plain upgrade run minutes after a release can refresh only the core API and
// miss the freshly-pushed cask in the custom tap — reporting "already latest".
// We force the throttle to 0 so the tap git clone is always pulled first and a
// new release is picked up immediately. A non-Homebrew install gets a clear
// pointer instead of a cryptic brew error.
package command

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update duck to the latest release (via Homebrew)",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, args []string) error {
		return runUpdate()
	},
}

// runUpdate execs `brew upgrade --cask duck`, streaming brew's output straight to
// the user's terminal so the upgrade reads exactly as if they ran brew. A missing
// brew means duck was not installed via the cask, so it returns install guidance
// rather than a raw exec error.
func runUpdate() error {
	brew, err := exec.LookPath("brew")
	if err != nil {
		return fmt.Errorf("`brew` not found on PATH: `duck update` upgrades the Homebrew cask. " +
			"If you installed duck another way, update it the same way. " +
			"Homebrew install: brew install digibugcat/tap/duck")
	}
	fmt.Println("Updating duck via Homebrew…")
	cmd := exec.Command(brew, "upgrade", "--cask", "duck")
	// HOMEBREW_AUTO_UPDATE_SECS=0 forces brew to refresh taps before upgrading, so
	// `duck update` reliably sees a release pushed moments ago (brew's default
	// throttle would otherwise skip the tap pull and report "already latest").
	cmd.Env = append(os.Environ(), "HOMEBREW_AUTO_UPDATE_SECS=0")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("brew upgrade --cask duck failed: %w", err)
	}
	return nil
}
