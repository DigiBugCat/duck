// Package model holds the presentation-layer row type the picker TUI renders,
// plus the pure Filter/Rank helpers over it. It is a leaf package: it imports
// nothing from duck's session/names/namer layers, so both internal/app (which
// produces Rows) and internal/tui (which renders them) can depend on it without
// a cycle.
//
// A Row is the fully-resolved, display-ready view of one remote tmux session:
// the raw display name (user ▸ codex ▸ dir-derived, resolved upstream in
// internal/names), the dir, a humanized age, attached/window counts, and the
// internal tmux name kept only so the TUI can dispatch attach/kill/rename
// against the right session. The "-2/-3" slug ugliness never reaches here — it
// stays an implementation detail of the tmux id.
//
// Filter and Rank carry the fuzzy-match + activity-ranking behavior.
package model

import (
	"sort"
	"strings"
	"time"
)

// Row is the display-ready view of one remote tmux session rendered by the
// picker. Display is the resolved raw name; TmuxName is the internal id used to
// dispatch tmux operations and must never be shown as the label.
type Row struct {
	Display  string    // resolved raw display name (user ▸ codex ▸ dir-derived)
	Title    string    // raw pane title — Claude Code's live task summary (empty for evicted/foreign sessions)
	Dir      string    // tilde-form working directory, e.g. ~/dev/foo
	Age      string    // humanized last-active age, e.g. "2m", "1h", "3d"
	Attached bool      // a client is currently attached
	Looped   bool      // the session is running a /loop (@duck_loop set) — ranked to the top
	Windows  int       // tmux window count
	TmuxName string    // internal tmux session id (dispatch key; never displayed)
	LastSeen time.Time // last-active timestamp, used by Rank (not displayed)
	Evicted  bool      // not live on the hub — evicted to save RAM; enter revives it (recreate + claude --resume)
}

// Filter returns the subset of rows whose display name or dir matches the
// query. An empty query returns rows unchanged. Matching is always-on fuzzy
// (subsequence, case-insensitive) over Display+Dir.
func Filter(rows []Row, query string) []Row {
	q := strings.TrimSpace(strings.ToLower(query))
	if q == "" {
		return rows
	}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		hay := strings.ToLower(r.Display + " " + r.Dir)
		if fuzzyMatch(hay, q) {
			out = append(out, r)
		}
	}
	return out
}

// fuzzyMatch reports whether every rune of needle appears in haystack in order
// (a subsequence match). Both are expected lower-cased by the caller.
func fuzzyMatch(haystack, needle string) bool {
	i := 0
	nr := []rune(needle)
	for _, hc := range haystack {
		if i < len(nr) && hc == nr[i] {
			i++
		}
	}
	return i == len(nr)
}

// Rank orders rows for the picker: looped (/loop-running) first, then attached,
// then by recency (most-recent LastSeen first), stable within a group; evicted
// rows sink below every live one. It returns a new slice; the input is not
// mutated. Looped outranks attached so a running loop you are NOT attached to
// still surfaces at the very top — the whole point of the pin is to keep
// autonomous loops from being buried.
func Rank(rows []Row) []Row {
	out := make([]Row, len(rows))
	copy(out, rows)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Evicted != out[j].Evicted {
			return !out[i].Evicted // live sorts above evicted
		}
		if out[i].Looped != out[j].Looped {
			return out[i].Looped // looped sorts first
		}
		if out[i].Attached != out[j].Attached {
			return out[i].Attached // then attached
		}
		return out[i].LastSeen.After(out[j].LastSeen) // most recent first
	})
	return out
}
