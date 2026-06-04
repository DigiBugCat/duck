package hub

import (
	"io"
	"strings"
	"testing"

	"github.com/DigiBugCat/duck/internal/paths"
)

// ── Injection-hardening tests, ported verbatim from flok/internal/hub/hub_test.go ──

func TestRemoteSyncPath(t *testing.T) {
	cases := map[string]string{
		"~/notes/vault/.codex": "notes/vault/.codex",
		"~/dev/foo":            "dev/foo",
		"~":                    ".",
		"/etc/hosts":           "/etc/hosts",
	}
	for in, want := range cases {
		if got := RemoteSyncPath(in); got != want {
			t.Errorf("RemoteSyncPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShellQuoteEscapesSingleQuotes(t *testing.T) {
	// hub's remote-shell quoting now lives in paths.Quote (deduped from the
	// byte-identical copies in session/namer/hub); this pins the same
	// injection-hardening contract the hub relies on.
	cases := map[string]string{
		"plain":       `'plain'`,
		"a b":         `'a b'`,
		"it's":        `'it'\''s'`,
		"$(rm -rf ~)": `'$(rm -rf ~)'`,
		";reboot":     `';reboot'`,
	}
	for in, want := range cases {
		if got := paths.Quote(in); got != want {
			t.Errorf("paths.Quote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidatePathRejectsControlChars(t *testing.T) {
	ok := []string{"~/notes/vault/.codex", "~/dev/foo", "/abs/path", "~/a b/c", "~"}
	for _, p := range ok {
		if err := ValidatePath(p); err != nil {
			t.Errorf("ValidatePath(%q) = %v, want nil", p, err)
		}
	}
	// Newline (heredoc-injection vector), tab (record delimiter), NUL, DEL.
	bad := []string{"~/a\nDUCK_EOF\ntouch /tmp/pwned\nb", "~/a\tb", "~/a\x00b", "~/a\x7fb", "~/a\rb"}
	for _, p := range bad {
		if err := ValidatePath(p); err == nil {
			t.Errorf("ValidatePath(%q) = nil, want error", p)
		}
	}
}

func TestRemoteShellPathQuotesUserPortion(t *testing.T) {
	cases := map[string]string{
		"~/dev/foo": `"$HOME"/'dev/foo'`,
		"~":         `"$HOME"`,
		"/abs/path": `'/abs/path'`,
		// A malicious-looking tilde path stays fully contained in single quotes.
		"~/a; rm -rf ~": `"$HOME"/'a; rm -rf ~'`,
		"~/x'y":         `"$HOME"/'x'\''y'`,
	}
	for in, want := range cases {
		if got := remoteShellPath(in); got != want {
			t.Errorf("remoteShellPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// ── duck-specific: sshInput carries the Control* multiplexing flags (gap#6) ──

// withRecordedSSH swaps the package runSSH seam for one that records the
// constructed argv and returns canned stdout, restoring the original on
// cleanup. No real ssh ever runs.
func withRecordedSSH(t *testing.T, stdout string) *[]string {
	t.Helper()
	var argv []string
	orig := runSSH
	runSSH = func(a []string, _ io.Reader) (string, error) {
		argv = append([]string(nil), a...)
		return stdout, nil
	}
	t.Cleanup(func() { runSSH = orig })
	return &argv
}

func TestSSHInputCarriesControlMasterFlags(t *testing.T) {
	argv := withRecordedSSH(t, "duck-ok\n")
	h := New("me@hub.local")
	if err := h.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	joined := strings.Join(*argv, " ")

	// gap#6: the ported hub path must carry duck's full multiplexing flag set so
	// the synchronous bind on `duck new` reuses the warmed master socket.
	for _, want := range []string{
		"-o BatchMode=yes",
		"-o ConnectTimeout=10",
		"-o ControlMaster=auto",
		"ControlPath=",
		"-o ControlPersist=10m",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sshInput argv missing %q\nargv: %v", want, *argv)
		}
	}
	// The ControlPath must be Go-expanded (absolute), never a literal "~" (c2).
	if strings.Contains(joined, "ControlPath=~") {
		t.Errorf("ControlPath used a literal ~; want Go-expanded absolute path\nargv: %v", *argv)
	}
	// argv[0] is ssh, the addr and the remote command are the last two elements.
	if (*argv)[0] != "ssh" {
		t.Errorf("argv[0] = %q, want ssh", (*argv)[0])
	}
	if (*argv)[len(*argv)-2] != "me@hub.local" {
		t.Errorf("addr not penultimate arg: %v", *argv)
	}
}

func TestPingChecksDuckToken(t *testing.T) {
	// The ping token was renamed flok-ok -> duck-ok.
	argv := withRecordedSSH(t, "duck-ok\n")
	h := New("h")
	if err := h.Ping(); err != nil {
		t.Fatalf("Ping with duck-ok: %v", err)
	}
	// The remote command is wrapped in a login shell (Homebrew PATH fix), so it
	// reads `zsh -lc 'echo duck-ok'`; assert the token is present.
	if last := (*argv)[len(*argv)-1]; !strings.Contains(last, "echo duck-ok") {
		t.Errorf("remote command = %q, want it to contain \"echo duck-ok\"", last)
	}
}

func TestRemotePathsUseDuckDir(t *testing.T) {
	// Every remote path moved from ~/.flok to ~/.duck.
	argv := withRecordedSSH(t, "yes\n")
	if _, err := New("h").BundleExists("b1"); err != nil {
		t.Fatalf("BundleExists: %v", err)
	}
	cmd := (*argv)[len(*argv)-1]
	if !strings.Contains(cmd, "~/.duck/bundles/b1") {
		t.Errorf("BundleExists command = %q, want it to reference ~/.duck/bundles/b1", cmd)
	}
	if strings.Contains(cmd, ".flok") {
		t.Errorf("BundleExists command still references .flok: %q", cmd)
	}
}

// TestValidateAddr pins the positional-safety contract: a leading dash (would be
// parsed as an ssh option, e.g. -oProxyCommand=…) and any whitespace/control
// char are rejected; legitimate user@host / bare host / ssh alias / user@ip are
// accepted.
func TestValidateAddr(t *testing.T) {
	bad := []string{
		"-oProxyCommand=x",
		"-",
		" a@b ",
		"a b",
		"host\tname",
		"host\nname",
		"",
	}
	for _, in := range bad {
		if err := ValidateAddr(in); err == nil {
			t.Errorf("ValidateAddr(%q) = nil, want rejection", in)
		}
	}
	good := []string{
		"me@10.0.0.5",
		"duck",
		"host",
		"user@host",
		"my-host.example.com",
		"user_name@host.local",
	}
	for _, in := range good {
		if err := ValidateAddr(in); err != nil {
			t.Errorf("ValidateAddr(%q) = %v, want accepted", in, err)
		}
	}
}
