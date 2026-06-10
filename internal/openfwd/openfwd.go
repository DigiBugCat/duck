// Package openfwd is the laptop-side half of duck's open-interceptor: it turns
// a hub-side "open this" request (an http(s) URL or a file path, sent by the
// ~/.duck/bin/duck-open shim over the reverse-forwarded port) into the right
// LOCAL action — open the URL in the laptop browser, tunnel a hub-localhost URL
// first, or open a synced file's local twin (fetching it when there is none).
//
// The interception itself lives on the hub (the shim + $BROWSER + the
// open/xdg-open symlinks duck installs). This package is what the shim phones
// home to: Serve runs a tiny localhost HTTP listener that RemoteForward exposes
// to the hub, and Handle is the pure decision it makes per request.
//
// The keystone is duck's same-$HOME guarantee: a hub path under the hub's home
// maps to the byte-identical tilde path under the laptop's home, so a synced
// file is already sitting at the translated local path and opens with zero
// copying. Only an unsynced file needs a fetch.
package openfwd

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// Request is one decoded open-interceptor call from the hub shim: the literal
// target it was asked to open, plus the hub's cwd and home so a relative or
// home-rooted path can be resolved and translated to the laptop.
type Request struct {
	Target string // the single argument the shim received (URL or path)
	Cwd    string // the hub shell's $PWD, for resolving a relative file target
	Home   string // the hub's $HOME, for translating a home-rooted path to the laptop
}

// Deps are the injected seams Handle needs, so the decision logic is pure and
// unit-testable with no sockets, ssh, or filesystem.
type Deps struct {
	// LocalHome is the laptop's $HOME, the translation target for a hub path that
	// sits under the hub's home.
	LocalHome string
	// Open performs the actual local open (production: `open`/`xdg-open` the
	// laptop's default handler). target is a URL or a local file path.
	Open func(target string) error
	// LocalForward exposes the hub's 127.0.0.1:hubPort on the laptop and returns
	// the laptop port to use in the rewritten URL (production: an ssh -L forward).
	LocalForward func(hubPort int) (localPort int, err error)
	// Exists reports whether a translated local path is present (the synced twin).
	Exists func(localPath string) bool
	// Fetch pulls an unsynced hub file to a local temp path and returns that path
	// (production: ssh cat into a temp file).
	Fetch func(hubAbsPath string) (localPath string, err error)
}

// Handle decides what to do with one request and performs it via d, returning a
// short human-readable note describing the action (echoed back so the hub shim,
// and thus Claude's Bash-tool output, sees what happened). The four branches:
//
//	http(s) to a hub localhost:port → LocalForward, rewrite host, Open
//	http(s) anywhere else           → Open as-is
//	file under the hub home (synced) → Open its local twin
//	file with no local twin          → Fetch to temp, Open the copy
func Handle(req Request, d Deps) (string, error) {
	t := strings.TrimSpace(req.Target)
	if t == "" {
		return "", fmt.Errorf("empty open target")
	}
	if isHTTP(t) {
		return handleURL(t, d)
	}
	return handleFile(t, req, d)
}

// isHTTP reports whether t is an http or https URL (the only schemes the shim
// forwards; it validates this hub-side too).
func isHTTP(t string) bool {
	return strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://")
}

// handleURL opens a URL locally, tunneling first when it points at the hub's
// own loopback (a dev server) so the laptop browser reaches it.
func handleURL(raw string, d Deps) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse url %q: %w", raw, err)
	}
	if !isLoopbackHost(u.Hostname()) {
		if err := d.Open(raw); err != nil {
			return "", err
		}
		return "opened " + raw, nil
	}
	// Hub-loopback URL: tunnel the hub port to the laptop, then open the
	// rewritten URL. A URL with no explicit port defaults to the scheme's port.
	hubPort, err := portOf(u)
	if err != nil {
		return "", err
	}
	if d.LocalForward == nil {
		// No tunnel available (e.g. no hub connection): opening the loopback URL
		// verbatim would hit the LAPTOP's own port, which is misleading — refuse.
		return "", fmt.Errorf("cannot open hub-local URL %q: no tunnel available", raw)
	}
	localPort, err := d.LocalForward(hubPort)
	if err != nil {
		return "", fmt.Errorf("forwarding hub port %d: %w", hubPort, err)
	}
	u.Host = "127.0.0.1:" + strconv.Itoa(localPort)
	rewritten := u.String()
	if err := d.Open(rewritten); err != nil {
		return "", err
	}
	return fmt.Sprintf("opened %s (tunneled hub:%d→laptop:%d)", rewritten, hubPort, localPort), nil
}

// handleFile opens a hub file locally: its synced twin when one exists, else a
// fetched copy.
func handleFile(target string, req Request, d Deps) (string, error) {
	hubAbs := target
	if !filepath.IsAbs(hubAbs) {
		hubAbs = filepath.Join(req.Cwd, hubAbs)
	}
	hubAbs = filepath.Clean(hubAbs)
	if local, ok := translate(hubAbs, req.Home, d.LocalHome); ok && d.Exists != nil && d.Exists(local) {
		if err := d.Open(local); err != nil {
			return "", err
		}
		return "opened synced file " + local, nil
	}
	if d.Fetch == nil {
		return "", fmt.Errorf("file %q is not synced and no fetch is available", hubAbs)
	}
	local, err := d.Fetch(hubAbs)
	if err != nil {
		return "", fmt.Errorf("fetching %q from hub: %w", hubAbs, err)
	}
	if err := d.Open(local); err != nil {
		return "", err
	}
	return "opened fetched copy of " + hubAbs, nil
}

// translate maps a hub absolute path under hubHome to the byte-identical path
// under localHome, relying on duck's same-$HOME-shape guarantee. ok is false
// when the path is not under the hub home (so there is no synced twin to try)
// or when either home is empty.
func translate(hubAbs, hubHome, localHome string) (string, bool) {
	if hubHome == "" || localHome == "" {
		return "", false
	}
	hubHome = strings.TrimRight(hubHome, "/")
	if hubAbs == hubHome {
		return localHome, true
	}
	prefix := hubHome + "/"
	if !strings.HasPrefix(hubAbs, prefix) {
		return "", false
	}
	return filepath.Join(localHome, strings.TrimPrefix(hubAbs, prefix)), true
}

// isLoopbackHost reports whether host is one of the loopback names a hub dev
// server binds, meaning the URL must be tunneled to reach it from the laptop.
func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "0.0.0.0", "::1":
		return true
	}
	return false
}

// portOf returns the explicit port of u, or the scheme default (80/443) when
// none is given.
func portOf(u *url.URL) (int, error) {
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, fmt.Errorf("bad port %q", p)
		}
		return n, nil
	}
	if u.Scheme == "https" {
		return 443, nil
	}
	return 80, nil
}
