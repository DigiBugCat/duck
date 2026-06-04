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
	Dir      string    // tilde-form working directory, e.g. ~/dev/foo
	Age      string    // humanized last-active age, e.g. "2m", "1h", "3d"
	Attached bool      // a client is currently attached
	Windows  int       // tmux window count
	TmuxName string    // internal tmux session id (dispatch key; never displayed)
	LastSeen time.Time // last-active timestamp, used by Rank (not displayed)
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

// Rank orders rows for the picker: attached first, then by recency (most-recent
// LastSeen first), stable within a group. It returns a new slice; the input is
// not mutated.
func Rank(rows []Row) []Row {
	out := make([]Row, len(rows))
	copy(out, rows)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Attached != out[j].Attached {
			return out[i].Attached // attached sorts first
		}
		return out[i].LastSeen.After(out[j].LastSeen) // most recent first
	})
	return out
}
