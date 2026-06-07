// `duck update`: self-update from the GitHub Release, mirroring how the sibling
// `cass` tool ships (cassandra-stack/cass/internal/cmd/update.go). duck installs
// as a raw binary on PATH (e.g. ~/.local/bin/duck via install.sh) — NOT a brew
// cask — so update hits the releases API directly, downloads the matching
// duck-<os>-<arch> asset, and atomically replaces its own binary. No brew, no
// tap, no CI throttle.
package command

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// releasesAPI is duck's GitHub releases endpoint. The repo is public, so the
// unauthenticated API works without a token (60 req/hr is plenty).
const releasesAPI = "https://api.github.com/repos/DigiBugCat/duck/releases"

// ghRelease is the subset of the GitHub release JSON we read.
type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

var updateCheckOnly bool

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update duck to the latest release",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		rel, err := fetchLatestRelease()
		if err != nil {
			return err
		}
		latest := strings.TrimPrefix(rel.TagName, "v")
		fmt.Printf("Current version: %s\n", version)
		fmt.Printf("Latest version:  %s\n", latest)
		if updateCheckOnly {
			if latest == version {
				fmt.Println("duck is up to date.")
			} else {
				fmt.Printf("Update available: %s → %s\n", version, latest)
			}
			return nil
		}
		if latest == version {
			fmt.Println("Already up to date.")
			return nil
		}
		return installRelease(rel)
	},
}

func init() {
	updateCmd.Flags().BoolVar(&updateCheckOnly, "check", false,
		"only report whether an update is available, don't install")
}

// fetchLatestRelease returns the newest published release. Shared by `duck
// update` and the picker's background update check.
func fetchLatestRelease() (*ghRelease, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(releasesAPI + "/latest")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("github %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// updateAvailable reports the latest version (tag minus the leading "v") and
// whether it is worth offering an update: never for a "dev" build, never when
// already on it. Used by the picker's background check (resume.go) to decide
// whether to show the ^u banner.
func updateAvailable(rel *ghRelease) (latest string, newer bool) {
	latest = strings.TrimPrefix(rel.TagName, "v")
	if version == "dev" || latest == "" || latest == version {
		return latest, false
	}
	return latest, true
}

// selfUpdateNow fetches the latest release and installs it. It is the body the
// picker's ^u runs (after the TUI tears down) so a chosen update self-replaces
// the binary just like `duck update`.
func selfUpdateNow() error {
	rel, err := fetchLatestRelease()
	if err != nil {
		return err
	}
	return installRelease(rel)
}

// detectTarget returns the os-arch asset suffix for this machine, matching the
// goreleaser name_template (duck-{{.Os}}-{{.Arch}}).
func detectTarget() (string, error) {
	var arch string
	switch runtime.GOARCH {
	case "amd64", "x86_64":
		arch = "amd64"
	case "arm64", "aarch64":
		arch = "arm64"
	default:
		return "", fmt.Errorf("unsupported architecture: %s", runtime.GOARCH)
	}
	switch runtime.GOOS {
	case "darwin", "linux":
		return runtime.GOOS + "-" + arch, nil
	default:
		return "", fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// installRelease downloads the matching binary asset for this machine and
// atomically replaces the running binary. On Unix, replacing the in-use binary
// is safe: open descriptors keep pointing at the old inode until the process
// exits. Mirrors cass's installRelease.
func installRelease(rel *ghRelease) error {
	target, err := detectTarget()
	if err != nil {
		return err
	}
	assetName := "duck-" + target
	var url string
	for _, a := range rel.Assets {
		if a.Name == assetName {
			url = a.BrowserDownloadURL
			break
		}
	}
	if url == "" {
		return fmt.Errorf("no binary named %q in release %s (assets may still be uploading)", assetName, rel.TagName)
	}

	dest, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current binary: %w", err)
	}
	dest, _ = filepath.EvalSymlinks(dest)

	// Stream to a sibling temp file so the final rename is atomic and on the same
	// filesystem as dest.
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".duck-update-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmpPath)
		}
	}()

	fmt.Printf("Downloading %s %s…\n", assetName, rel.TagName)
	httpc := &http.Client{Timeout: 120 * time.Second}
	resp, err := httpc.Get(url)
	if err != nil {
		tmp.Close()
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		tmp.Close()
		return fmt.Errorf("download: %s", resp.Status)
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return fmt.Errorf("install (is %s writable?): %w", dest, err)
	}
	cleanup = false
	fmt.Printf("Updated duck → %s (%s)\n", rel.TagName, dest)
	return nil
}
