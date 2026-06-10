package openfwd

import (
	"fmt"
	"net"
	"net/http"
	"time"
)

// HubPort is the fixed hub-side loopback port the duck-open shim POSTs to and
// that RemoteForward exposes back to this listener. Fixed (not negotiated) so
// the hub shim stays stateless — it always curls 127.0.0.1:HubPort. The value
// is duplicated as the DUCK_OPEN_PORT default in assets/duck-open.sh; keep them
// in sync.
const HubPort = 4774

// Listener is the running laptop-side opener server. Start returns one; Close
// stops it. It serves a single route, POST /open, whose form fields
// (target/cwd/home) mirror the shim's curl --data-urlencode call.
type Listener struct {
	srv  *http.Server
	ln   net.Listener
	deps Deps
}

// LocalPort is the ephemeral laptop port the server bound. RemoteForward maps
// the hub's HubPort to this so the shim's POST reaches the server.
func (l *Listener) LocalPort() int { return l.ln.Addr().(*net.TCPAddr).Port }

// Start binds an ephemeral loopback port, starts serving POST /open against d,
// and returns the Listener. The caller wires RemoteForward(HubPort,
// l.LocalPort()) so the hub can reach it, and defers Close. Binding loopback
// only keeps the opener unreachable from the network — only the hub, via the
// reverse forward, can talk to it.
func Start(d Deps) (*Listener, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("binding opener listener: %w", err)
	}
	l := &Listener{ln: ln, deps: d}
	mux := http.NewServeMux()
	mux.HandleFunc("/open", l.handleOpen)
	l.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go l.srv.Serve(ln)
	return l, nil
}

// Close stops the server (and releases the port). Best-effort.
func (l *Listener) Close() error {
	if l.srv == nil {
		return nil
	}
	return l.srv.Close()
}

// handleOpen decodes one shim request and runs Handle. It always replies in
// plain text: the note on success (which the shim echoes, so Claude's Bash-tool
// output reflects the open) or the error with a 500 (so the shim falls through
// to the hub's real opener).
func (l *Listener) handleOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}
	note, err := Handle(Request{
		Target: r.FormValue("target"),
		Cwd:    r.FormValue("cwd"),
		Home:   r.FormValue("home"),
	}, l.deps)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintln(w, note)
}
