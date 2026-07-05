// `duck channel` — the machine lane onto sidebar agents (internal/channel):
//
//	duck channel ls            list agents and their event streams
//	duck channel tail <agent>  stream an agent's structured events (JSONL)
//	duck channel send <agent> <msg…>  type a message into the agent's TUI
//	duck channel serve         Claude Code channel sidecar (MCP stdio)
//
// ls/tail/send default to the current tmux session's agents (--session to
// target another). serve multiplexes the enclosing workspace's agents into
// one Claude channel (--all for machine-wide, e.g. motherduck); register it
// in .mcp.json and launch Claude with `--channels server:duck-agents
// --dangerously-load-development-channels` (research preview). Without tmux
// or without any agents, everything here degrades to a quiet no-op.
package command

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	agentpkg "github.com/DigiBugCat/duck/internal/agent"
	"github.com/DigiBugCat/duck/internal/channel"
	"github.com/DigiBugCat/duck/internal/panel"
	"github.com/DigiBugCat/duck/internal/routines"
	"github.com/spf13/cobra"
)

var channelSession string

var channelCmd = &cobra.Command{
	Use:   "channel",
	Short: "Structured I/O onto sidebar agents (tail events, send prompts)",
}

// channelOuter resolves the duck session whose agents we address: --session
// wins, else the enclosing tmux session.
func channelOuter(run panel.Runner) (string, error) {
	if channelSession != "" {
		return channelSession, nil
	}
	if !panel.InsideTmux() {
		return "", fmt.Errorf("not inside tmux — pass --session <name> to pick the duck session")
	}
	return panel.CurrentSession(run)
}

var channelLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List every sidebar agent on this machine and its event stream",
	RunE: func(c *cobra.Command, args []string) error {
		refs, err := channel.AllAgents(panel.ExecRunner)
		if err != nil {
			return err
		}
		if len(refs) == 0 {
			fmt.Println("no agents (spawn one: duck spawn <cmd>)")
			return nil
		}
		for _, r := range refs {
			stream := r.Rollout
			if stream == "" {
				stream = "- (no codex stream; send-keys only)"
			}
			fmt.Printf("%s/%s\t%s\n", r.Session, r.Name, stream)
		}
		return nil
	},
}

var channelTailFollow bool
var channelTailRaw bool

var channelTailCmd = &cobra.Command{
	Use:   "tail <agent>",
	Short: "Stream an agent's structured events (JSONL on stdout)",
	Args:  cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		run := panel.ExecRunner
		outer, err := channelOuter(run)
		if err != nil {
			return err
		}
		ref, err := channel.FindAgent(run, outer, args[0])
		if err != nil {
			return err
		}
		if err := channel.Resolve(run, &ref); err != nil {
			return err
		}
		if ref.Rollout == "" {
			return fmt.Errorf("agent %q has no codex event stream (not a codex window, or codex still starting)", args[0])
		}
		_, err = channel.Tail(os.Stdout, ref.Rollout, 0, channelTailFollow, channelTailRaw)
		return err
	},
}

var channelSendCmd = &cobra.Command{
	Use:   "send <agent> <message…>",
	Short: "Type a message into the agent's TUI (visible in the viewport)",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(c *cobra.Command, args []string) error {
		run := panel.ExecRunner
		outer, err := channelOuter(run)
		if err != nil {
			return err
		}
		ref, err := channel.FindAgent(run, outer, args[0])
		if err != nil {
			return err
		}
		return channel.Send(run, ref, strings.Join(args[1:], " "))
	},
}

var channelPublishCmd = &cobra.Command{
	Use:   "publish <message…>",
	Short: "Notify the workspace's manager Claude (spooled to its channel sidecar)",
	Long: `Append a message to the workspace's channel spool. The Claude Code channel
sidecar (duck channel serve) drains it each sweep and pushes it into the
manager's context — no tmux send-keys, no interrupting a running turn. If no
sidecar is alive the event parks in the spool and is delivered when one starts.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		outer, err := channelOuter(panel.ExecRunner)
		if err != nil {
			return err
		}
		msg := strings.Join(args, " ")
		if err := channel.Publish(outer, msg, map[string]string{"source": "cli"}); err != nil {
			return err
		}
		if channel.AliveWithin(outer, 10*time.Second) {
			fmt.Printf("published to %s (sidecar alive)\n", outer)
		} else {
			fmt.Printf("spooled for %s (no sidecar alive yet — parked until one starts)\n", outer)
		}
		return nil
	},
}

var channelNotifyCmd = &cobra.Command{
	Use:    "notify <payload-json>",
	Hidden: true, // plumbing: codex's notify hook target, wired in by duck spawn
	Short:  "codex notify hook: pin this pane's rollout from the turn payload",
	Args:   cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		return channel.HandleNotify(panel.ExecRunner, os.Getenv("TMUX_PANE"), args[0])
	},
}

// mcpHost backs the serve MCP server's action tools (spawn/resume/fork agents,
// preview/render artifacts, routine control) with duck's real internal packages
// — so a tool call does EXACTLY what the equivalent CLI verb does. Lives here
// (not in internal/channel) to break the channel↔{agent,routines,command} import
// cycles: command already imports all of them.
type mcpHost struct{}

func (mcpHost) launch(workspace string, spec agentpkg.Spec) (string, string, error) {
	run := panel.ExecRunner
	dir, err := panel.SessionPath(run, workspace)
	if err != nil {
		return "", "", err
	}
	bin, err := os.Executable()
	if err != nil {
		bin = "duck"
	}
	res, err := agentpkg.Launch(run, workspace, dir, bin, spec)
	if err != nil {
		return "", "", err
	}
	return res.PaneID, res.SessionID, nil
}

func (h mcpHost) Launch(workspace string, argv []string, name, tab, prompt string) (string, string, error) {
	return h.launch(workspace, agentpkg.Spec{Args: argv, Name: name, Tab: tab, Prompt: prompt})
}
func (h mcpHost) Resume(workspace, sessionID, prompt string) (string, string, error) {
	return h.launch(workspace, agentpkg.Spec{Args: agentpkg.ResumeArgs(sessionID), Prompt: prompt})
}
func (h mcpHost) Fork(workspace, sessionID, prompt string) (string, string, error) {
	return h.launch(workspace, agentpkg.Spec{Args: agentpkg.ForkArgs(sessionID), Prompt: prompt})
}

// Preview / Render route through the same functions the CLI preview/render verbs
// use, so a tool-driven artifact behaves identically (live-watch, click-refresh).
func (mcpHost) Preview(workspace, target, name string) (string, error) {
	run := panel.ExecRunner
	dir, err := panel.SessionPath(run, workspace)
	if err != nil {
		return "", err
	}
	return runPreview(run, workspace, dir, target, name, false)
}
func (mcpHost) Render(workspace, target string) error {
	return openOnClient(target)
}

// Routines / FireRoutine drive the workspace's scheduled executors (list, or run
// one now) via internal/routines — the same paths `duck routines`/`fire` use.
func (mcpHost) Routines(workspace string) (string, error) {
	defs, err := routines.LoadWorkspace(workspace)
	if err != nil {
		return "", err
	}
	if len(defs) == 0 {
		return "no routines in this workspace", nil
	}
	var b strings.Builder
	for _, d := range defs {
		when := d.Schedule
		if d.Trigger == routines.TriggerHeartbeat {
			when = d.Interval.String()
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\n", d.Name, d.Trigger, when)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
func (mcpHost) FireRoutine(workspace, name string) (string, error) {
	defs, err := routines.LoadWorkspace(workspace)
	if err != nil {
		return "", err
	}
	for _, d := range defs {
		if d.Name == name {
			if !routines.Fire(panel.ExecRunner, d, time.Now(), io.Discard) {
				return "", fmt.Errorf("routine %q did not fire (already running?)", name)
			}
			return "fired routine " + name, nil
		}
	}
	return "", fmt.Errorf("no routine %q in this workspace", name)
}

var channelHookCmd = &cobra.Command{
	Use:    "hook",
	Hidden: true, // plumbing: codex's SessionStart hook target, wired in by duck spawn
	Short:  "codex SessionStart hook: bind this pane's session id + rollout at first turn",
	Args:   cobra.NoArgs,
	// codex hooks deliver their payload on STDIN (unlike notify, which passes it
	// as arg $1). Must be fast + non-blocking — codex blocks on the hook (60s
	// timeout), so HandleHook does only local option stamps.
	RunE: func(c *cobra.Command, args []string) error {
		payload, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		return channel.HandleHook(panel.ExecRunner, os.Getenv("TMUX_PANE"), string(payload))
	},
}

var channelServeAll bool

var channelServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Claude Code channel sidecar: this workspace's agents, one channel",
	Long: `MCP stdio server pushing sidebar-agent events (turn started/finished) into a
Claude Code session, with a reply tool routing answers back into the agent's
TUI. Scoped to the enclosing workspace's agents by default (a manager hears
its own lot, not the whole machine); --session picks another workspace, --all
sweeps every workspace (motherduck). Register in .mcp.json:

  {"mcpServers": {"duck-agents": {"command": "duck", "args": ["channel", "serve"]}}}

and launch: claude --channels server:duck-agents --dangerously-load-development-channels`,
	RunE: func(c *cobra.Command, args []string) error {
		workspace := ""
		if !channelServeAll {
			ws, err := channelOuter(panel.ExecRunner)
			if err != nil {
				// Outside tmux with no --session there is nothing to scope to:
				// sweep machine-wide rather than dying — degrade, don't demand.
				fmt.Fprintln(os.Stderr, "duck channel serve: no enclosing workspace, sweeping machine-wide (use --session or --all to be explicit)")
			} else {
				workspace = ws
			}
		}
		return channel.Serve(panel.ExecRunner, workspace, mcpHost{}, os.Stdin, os.Stdout)
	},
}

func init() {
	channelCmd.PersistentFlags().StringVar(&channelSession, "session", "", "duck session owning the agent (default: current tmux session)")
	channelServeCmd.Flags().BoolVar(&channelServeAll, "all", false, "sweep every workspace on the machine (motherduck), not just the enclosing one")
	channelTailCmd.Flags().BoolVarP(&channelTailFollow, "follow", "f", false, "keep streaming as new events arrive")
	channelTailCmd.Flags().BoolVar(&channelTailRaw, "raw", false, "raw rollout lines (unfiltered)")
	channelCmd.AddCommand(channelLsCmd, channelTailCmd, channelSendCmd, channelServeCmd, channelPublishCmd, channelNotifyCmd, channelHookCmd)
	rootCmd.AddCommand(channelCmd)
}
