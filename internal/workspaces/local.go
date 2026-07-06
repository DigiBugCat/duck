package workspaces

import (
	"io"
	"os/exec"
)

// LocalRunner satisfies Runner by executing each command through the LOCAL
// `/bin/sh`, for callers that already run ON the hub. panel.ExecRunner itself
// is a tmux-prefixed local runner and so cannot double as the store's shell
// seam — this is the plain-shell equivalent. The base path passed to Store is
// tilde-form (`~/.claude/projects`); `/bin/sh -c` expands the leading ~, so the
// same command strings work locally and over SSH unchanged.
type LocalRunner struct{}

// Run executes cmd via `sh -c` and returns its combined output. A non-zero
// exit surfaces as an error (the store's reads defend against that with
// `|| echo ”` / `|| true`, mirroring the SSH path).
func (LocalRunner) Run(cmd string) (string, error) {
	out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
	return string(out), err
}

// RunInput executes cmd via `sh -c` with stdin piped in — the local twin of
// sshx.Client.RunInput, used by Save to stream a record's JSON into `cat`.
func (LocalRunner) RunInput(cmd string, stdin io.Reader) (string, error) {
	c := exec.Command("sh", "-c", cmd)
	c.Stdin = stdin
	out, err := c.CombinedOutput()
	return string(out), err
}
