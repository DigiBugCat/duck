// `duck workflows` — deterministic multi-agent runs (DESIGN: docs/WORKFLOWS.md).
// A workflow is a JS script fanning out over disposable headless codex
// executors; the run — not its workers — is the
// visible, addressable thing.
//
//	duck workflows                     list this workspace's runs (--all: every run)
//	duck workflows run <script.js> …   start a run (detached by default; --fg to watch)
//	duck workflows tail <run-id>       the run's progress log
//	duck workflows stop <run-id>       terminate a live run (workers die with it)
//	duck workflows exec <run-id>       hidden: the detached engine's entrypoint
package command

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/DigiBugCat/duck/internal/panel"
	"github.com/DigiBugCat/duck/internal/workflow"
)

var (
	workflowsAll bool
	workflowsTSV bool

	wfRunName        string
	wfRunArgs        string
	wfRunBudget      int64
	wfRunConcurrency int
	wfRunResume      string
	wfRunFg          bool
)

var workflowsCmd = &cobra.Command{
	Use:   "workflows",
	Short: "Deterministic multi-agent runs over headless codex fleets",
	RunE: func(c *cobra.Command, args []string) error {
		return listWorkflows(c)
	},
}

// workflowWorkspace resolves the enclosing workspace, "" when outside tmux —
// a bare-shell run is legal, it just gets no completion digest.
func workflowWorkspace(run panel.Runner) string {
	if !panel.InsideTmux() {
		return ""
	}
	ws, err := panel.CurrentSession(run)
	if err != nil {
		return ""
	}
	return ws
}

func listWorkflows(c *cobra.Command) error {
	ws := ""
	if !workflowsAll {
		ws = workflowWorkspace(panel.ExecRunner)
	}
	runs, err := workflow.List(ws)
	if err != nil {
		return err
	}
	if workflowsTSV {
		for _, s := range runs {
			state := s.State
			if s.State == workflow.StateRunning && !s.Alive() {
				state = "orphaned"
			}
			// Machine row for the roster: staleness (seconds since the last
			// status write) lets it age terminal runs out of the sidebar
			// without parsing times. Free-text (name) LAST, per the house rule.
			fmt.Fprintf(c.OutOrStdout(), "%s\t%s\t%s\t%s\t%d/%d\t%s\t%d\t%s\n",
				s.RunID, s.Workspace, state, s.Elapsed(), s.AgentsDone, s.AgentsTotal,
				workflow.HumanTokens(s.Tokens), int(time.Since(s.Updated).Seconds()), s.Name)
		}
		return nil
	}
	if len(runs) == 0 {
		fmt.Fprintln(c.OutOrStdout(), "no workflow runs here (start one: duck workflows run <script.js>)")
		return nil
	}
	tw := tabwriter.NewWriter(c.OutOrStdout(), 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tWORKSPACE\tNAME\tSTATE\tPHASE\tELAPSED\tAGENTS\tTOKENS")
	for _, s := range runs {
		state := s.State
		if s.State == workflow.StateRunning && !s.Alive() {
			state = "orphaned"
		}
		phase := s.Phase
		if phase == "" {
			phase = "—"
		}
		wsCol := s.Workspace
		if wsCol == "" {
			wsCol = "—"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d/%d\t%s\n",
			s.RunID, wsCol, s.Name, state, phase, s.Elapsed(), s.AgentsDone, s.AgentsTotal, workflow.HumanTokens(s.Tokens))
	}
	return tw.Flush()
}

var workflowsRunCmd = &cobra.Command{
	Use:   "run <script.js>",
	Short: "Start a workflow run from a script file",
	Args:  cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		script, err := os.ReadFile(args[0])
		if err != nil {
			return err
		}
		cwd, _ := os.Getwd()
		o := workflow.Opts{
			Name:        wfRunName,
			Workspace:   workflowWorkspace(panel.ExecRunner),
			Dir:         cwd,
			Budget:      wfRunBudget,
			Concurrency: wfRunConcurrency,
			ResumeFrom:  wfRunResume,
		}
		if wfRunArgs != "" {
			o.Args = []byte(wfRunArgs)
		}
		r, err := workflow.Prepare(string(script), o)
		if err != nil {
			return err
		}
		if wfRunFg {
			fmt.Fprintf(c.OutOrStdout(), "%s running in the foreground (^C stops it)\n", r.ID)
			return execRunForeground(r.ID)
		}
		bin, err := os.Executable()
		if err != nil {
			bin = "duck"
		}
		pid, err := workflow.StartDetached(bin, r.ID)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.OutOrStdout(), "started %s (%s, pid %d) — watch: duck workflows tail %s\n", r.ID, r.Meta.Name, pid, r.ID)
		return nil
	},
}

// execRunForeground runs the engine in this process, SIGINT/SIGTERM stopping
// it cleanly (state=stopped, workers killed via context).
func execRunForeground(runID string) error {
	r, err := workflow.Load(runID)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return r.Execute(ctx)
}

// workflowsExecCmd is the detached engine's entrypoint; not for humans.
var workflowsExecCmd = &cobra.Command{
	Use:    "exec <run-id>",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		return execRunForeground(args[0])
	},
}

var workflowsTailCmd = &cobra.Command{
	Use:   "tail <run-id>",
	Short: "Show a run's progress log (phases, per-agent completions, errors)",
	Args:  cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		dir, err := workflow.RunDir(args[0])
		if err != nil {
			return err
		}
		s, err := workflow.ReadStatus(args[0])
		if err != nil {
			return fmt.Errorf("no such run %s", args[0])
		}
		fmt.Fprintf(c.OutOrStdout(), "%s  %s  %s  agents %d/%d (%d failed)  %s tokens  %s\n",
			s.RunID, s.Name, s.State, s.AgentsDone, s.AgentsTotal, s.AgentsFailed, workflow.HumanTokens(s.Tokens), s.Elapsed())
		if len(s.Agents) > 0 {
			fmt.Fprintln(c.OutOrStdout())
			for _, a := range s.Agents {
				last := a.Last
				if last == "" {
					last = "(starting)"
				}
				fmt.Fprintf(c.OutOrStdout(), "  ● %-18s %6s  %5s  %s\n",
					a.Label, time.Since(a.Started).Round(time.Second), workflow.HumanTokens(a.Tokens), last)
			}
		}
		fmt.Fprintln(c.OutOrStdout())
		data, err := os.ReadFile(dir + "/run.log")
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		fmt.Fprint(c.OutOrStdout(), string(data))
		if s.State == workflow.StateDone {
			fmt.Fprintf(c.OutOrStdout(), "\nresult: %s/result.json\n", dir)
		}
		return nil
	},
}

var workflowsStopCmd = &cobra.Command{
	Use:   "stop <run-id>",
	Short: "Terminate a live run (its workers die with it)",
	Args:  cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		s, err := workflow.ReadStatus(args[0])
		if err != nil {
			return fmt.Errorf("no such run %s", args[0])
		}
		if s.State != workflow.StateRunning && s.State != workflow.StateStarting {
			return fmt.Errorf("%s is not running (state: %s)", args[0], s.State)
		}
		if !s.Alive() {
			return fmt.Errorf("%s looks orphaned (engine pid %d is gone) — its status is stale, nothing to stop", args[0], s.PID)
		}
		if err := workflow.Stop(s.PID); err != nil {
			return err
		}
		fmt.Fprintf(c.OutOrStdout(), "stopped %s\n", args[0])
		return nil
	},
}

// StartWorkflowRun is the shared start path for the sidecar's `workflow` MCP
// tool: prepare from an inline script + start detached, returning the run id.
// Lives here (not internal/workflow) so the workspace/binary resolution stays
// with the command layer.
func StartWorkflowRun(workspace, script, argsJSON, resumeFrom string, budget int64) (string, error) {
	cwd, _ := os.Getwd()
	o := workflow.Opts{Workspace: workspace, Dir: cwd, ResumeFrom: resumeFrom, Budget: budget}
	if strings.TrimSpace(argsJSON) != "" {
		o.Args = []byte(argsJSON)
	}
	r, err := workflow.Prepare(script, o)
	if err != nil {
		return "", err
	}
	bin, err := os.Executable()
	if err != nil {
		bin = "duck"
	}
	if _, err := workflow.StartDetached(bin, r.ID); err != nil {
		return "", err
	}
	return r.ID, nil
}

func init() {
	workflowsCmd.Flags().BoolVar(&workflowsAll, "all", false, "list every run, not just this workspace's")
	workflowsCmd.Flags().BoolVar(&workflowsTSV, "tsv", false, "machine-readable tab-separated output (no header)")
	_ = workflowsCmd.Flags().MarkHidden("tsv")
	workflowsRunCmd.Flags().StringVar(&wfRunName, "name", "", "run name override (default: the script's meta.name)")
	workflowsRunCmd.Flags().StringVar(&wfRunArgs, "args", "", "JSON value exposed to the script as `args`")
	workflowsRunCmd.Flags().Int64Var(&wfRunBudget, "budget", 0, "token cap across all workers (0 = uncapped)")
	workflowsRunCmd.Flags().IntVar(&wfRunConcurrency, "concurrency", 0, "max simultaneous workers (default 64)")
	workflowsRunCmd.Flags().StringVar(&wfRunResume, "resume", "", "prior run id whose journal seeds the replay cache")
	workflowsRunCmd.Flags().BoolVar(&wfRunFg, "fg", false, "run in the foreground instead of detaching")
	workflowsCmd.AddCommand(workflowsRunCmd, workflowsExecCmd, workflowsTailCmd, workflowsStopCmd)
	rootCmd.AddCommand(workflowsCmd)
}
