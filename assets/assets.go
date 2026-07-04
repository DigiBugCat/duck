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

// DuckOpen is the hub-side open interceptor written to ~/.duck/bin/duck-open by
// `duck hub setup` (with `open` and `xdg-open` symlinked to it, and exported as
// $BROWSER). It forwards http(s) URLs and existing files to the attached
// laptop's opener listener over the reverse-forwarded port, and passes
// everything else through to the real platform opener.
//
//go:embed duck-open.sh
var DuckOpen string

// AgentNotes is the agent-facing cheat sheet (how to show things to the
// human: artifact/viewport verbs, routing rules, workspace hygiene). It is
// written to ~/.duck/AGENT.md on every duck start and imported into
// ~/.claude/CLAUDE.md via a managed @-import line, so any Claude launched in
// a duck workspace carries these instructions in its system prompt.
//
//go:embed agent-notes.md
var AgentNotes string
