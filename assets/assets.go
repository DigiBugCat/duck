// Package assets embeds duck's static files (the hub tmux.conf) so the single
// `duck` binary can ship them without a runtime filesystem dependency.
package assets

import _ "embed"

// TmuxConf is the hub-side ~/.tmux.conf written by `duck hub setup`. It is the
// flok-era duck.tmux.conf minus the client-detached rename hook, `bind R`, and
// `bind-key T` (PLAN §4 / fix d1).
//
//go:embed duck.tmux.conf
var TmuxConf string
