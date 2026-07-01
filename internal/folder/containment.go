package folder

import (
	"path/filepath"
	"strings"

	"github.com/DigiBugCat/duck/internal/mutagen"
	"github.com/DigiBugCat/duck/internal/paths"
)

// ContainmentKind classifies how localAbs relates to the local paths of
// already-active duck Mutagen sessions.
type ContainmentKind string

const (
	// ContainmentNone means localAbs neither overlaps nor is overlapped by any
	// active session — the normal, common case.
	ContainmentNone ContainmentKind = "none"
	// ContainmentInside means localAbs is the same as, or nested under, an
	// already-synced session root: that folder's files are already covered by
	// an existing sync, so a second session would double-sync the same data.
	ContainmentInside ContainmentKind = "inside"
	// ContainmentEncloses means one or more already-synced session roots are
	// nested under localAbs: syncing localAbs would create a parent session
	// that overlaps the existing child session(s).
	ContainmentEncloses ContainmentKind = "encloses"
)

// Containment is the verdict CheckContainment returns for a candidate local
// directory against the set of currently active duck Mutagen sessions.
type Containment struct {
	Kind ContainmentKind

	// Inside is set when Kind == ContainmentInside: the existing session that
	// already covers localAbs.
	Inside *mutagen.Session

	// Enclosed is set when Kind == ContainmentEncloses: the existing
	// session(s) nested under localAbs that a new parent sync would overlap.
	Enclosed []mutagen.Session
}

// CheckContainment classifies localAbs (an absolute, cleaned local path)
// against sessions' local (Alpha) paths. A session whose Alpha.Path is not a
// local path (e.g. the session was inspected from the hub side) is skipped —
// containment is only meaningful between two local roots on this machine.
//
// "Inside" takes priority over "encloses": if localAbs is itself covered by
// an existing session, that's the only fact that matters, even if localAbs
// also happens to enclose some unrelated session elsewhere (it can't, since
// nesting both ways would mean the two session roots are identical, which the
// inside check already catches).
func CheckContainment(localAbs string, sessions []mutagen.Session) Containment {
	clean := filepath.Clean(localAbs)

	for _, s := range sessions {
		other := localSessionPath(s)
		if other == "" {
			continue
		}
		if clean == other || isWithin(clean, other) {
			sCopy := s
			return Containment{Kind: ContainmentInside, Inside: &sCopy}
		}
	}

	var enclosed []mutagen.Session
	for _, s := range sessions {
		other := localSessionPath(s)
		if other == "" {
			continue
		}
		if isWithin(other, clean) {
			enclosed = append(enclosed, s)
		}
	}
	if len(enclosed) > 0 {
		return Containment{Kind: ContainmentEncloses, Enclosed: enclosed}
	}

	return Containment{Kind: ContainmentNone}
}

// localSessionPath returns the cleaned local-machine path for a session, or
// "" if the session's Alpha endpoint isn't a local path (Host set means it's
// remote, which shouldn't happen for Alpha but is checked defensively).
func localSessionPath(s mutagen.Session) string {
	if s.Alpha.Path == "" || s.Alpha.Host != "" {
		return ""
	}
	return filepath.Clean(s.Alpha.Path)
}

// isWithin reports whether child is a strict descendant of parent (both
// already filepath.Clean-ed absolute paths).
func isWithin(child, parent string) bool {
	if child == parent {
		return false
	}
	return strings.HasPrefix(child+string(filepath.Separator), parent+string(filepath.Separator))
}

// Display renders a Containment for a confirmation prompt, using tilde-form
// paths so it matches the rest of duck's user-facing output.
func (c Containment) Display() string {
	switch c.Kind {
	case ContainmentInside:
		return paths.Contract(c.Inside.Alpha.Path)
	case ContainmentEncloses:
		names := make([]string, len(c.Enclosed))
		for i, s := range c.Enclosed {
			names[i] = paths.Contract(s.Alpha.Path)
		}
		return strings.Join(names, ", ")
	default:
		return ""
	}
}
