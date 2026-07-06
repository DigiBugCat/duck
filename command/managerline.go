// managerline.go: command-layer thin wrappers over internal/manager, which owns
// the ONE implementation of the workspace-manager launch line (shared with
// session heal/revive — see internal/manager for the full rationale, incl. why
// the line invokes the bare word `claude`).
package command

import "github.com/DigiBugCat/duck/internal/manager"

// managerLine builds the in-pane launch line for the workspace manager.
func managerLine(extraArgs []string) string { return manager.Line(extraArgs) }

// channelsWired reports whether channel flags are already opted into / out of.
func channelsWired(extraArgs []string) bool { return manager.ChannelsWired(extraArgs) }
