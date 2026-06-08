// Package assets embeds duck's static files (the hub tmux.conf, the laptop
// Hammerspoon snap binding) so the single `duck` binary can ship them without a
// runtime filesystem dependency.
package assets

import _ "embed"

// TmuxConf is the hub-side ~/.tmux.conf written by `duck hub setup`. It is the
// flok-era duck.tmux.conf minus the client-detached rename hook, `bind R`, and
// `bind-key T` (PLAN §4 / fix d1).
//
//go:embed duck.tmux.conf
var TmuxConf string

// HammerspoonSnap is the laptop-side Hammerspoon binding that maps Cmd+Shift+3
// to `duck snap`. `duck snap install-hotkey` writes it to ~/.hammerspoon so the
// binding lives in the binary (from git) and installs identically on every Mac,
// instead of being hand-copied per machine.
//
//go:embed hammerspoon-snap.lua
var HammerspoonSnap string
