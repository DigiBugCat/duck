// Package claude owns everything duck knows about Claude Code's on-disk state:
// the ~/.claude/projects transcript corpus (its slug rules and per-project
// directories), the ~/.claude.json project registry that makes a session
// discoverable, and the cross-machine history reconciler that makes a
// conversation started on one machine resumable on another despite the two
// machines having different $HOME paths (e.g. macOS /Users/me vs Linux
// /home/me).
//
// Background: Claude Code stores each session as ~/.claude/projects/<slug>/
// <id>.jsonl, where <slug> is the absolute working directory with every "/" and
// "." turned into "-". Because the slug embeds the absolute path, the SAME
// logical project gets different slugs on machines with different homes. Resume
// is keyed on (a) that slug directory and (b) the project's absolute path being
// present in ~/.claude.json's "projects" map — the internal "cwd" field of the
// transcript is cosmetic (verified empirically). The reconciler exploits exactly
// that: place foreign transcripts under this machine's slug and register the
// local path — never rewriting transcript contents, never deleting anything.
package claude

import "strings"

// slugReplacer mirrors Claude Code's project-directory slug rule: an absolute
// path becomes a single token with every "/" and "." turned into "-" (case
// preserved). Verified empirically: /Users/jane.doe/dev maps to the on-disk slug
// -Users-jane-doe-dev (note the "." in the username also becomes "-").
var slugReplacer = strings.NewReplacer("/", "-", ".", "-")

// ProjectsRoot is the tilde-form path of the whole Claude sessions corpus —
// ~/.claude/projects — the parent of every per-project <slug> directory. duck
// syncs this ONE directory so all conversation history is mirrored to the hub in
// a single Mutagen session.
func ProjectsRoot() string { return "~/.claude/projects" }

// Slug returns the on-disk slug Claude Code derives from an ABSOLUTE
// working-directory path: every "/" and "." becomes "-". This is the directory
// name under ~/.claude/projects, and the join key the reconciler uses to map a
// foreign machine's cwd onto this machine's corpus.
func Slug(absPath string) string { return slugReplacer.Replace(absPath) }

// ProjectDir returns the tilde-form path of the ~/.claude/projects/<slug>
// directory Claude Code uses for sessions started in absCwd. Because duck
// mirrors the corpus verbatim, the slug computed here is byte-identical to the
// one Claude wrote for that same absolute cwd on any machine.
func ProjectDir(absCwd string) string { return ProjectsRoot() + "/" + Slug(absCwd) }
