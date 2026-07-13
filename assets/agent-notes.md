# duck — workspace lifecycle (managed by duck; do not edit)

You may be running inside a duck workspace: a plain tmux session whose
manager occupies the main pane. Additional processes launched with `duck
spawn` are ordinary panes/windows in that session. They remain visible and
continue running across detachments; the tmux status bar is their roster.

Use Claude Code's native subagent/orchestration facilities for model delegation
and fan-out. Use `duck spawn` only when the job specifically needs a durable,
human-watchable tmux process, an arbitrary command, or an explicit Codex
resume/fork. The human operates those panes through tmux or `duck fleet`.

Duck's detached workflow CLI (`duck workflows`) is a separate operator tool
for persistent, replayable Codex runs. It does not deliver completion events
into this conversation; inspect runs explicitly with its list/tail commands.
