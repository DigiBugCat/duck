package folder

import (
	"path/filepath"
	"testing"

	"github.com/DigiBugCat/duck/internal/mutagen"
)

func session(name, localPath string) mutagen.Session {
	return mutagen.Session{
		Name:  name,
		Alpha: mutagen.Endpoint{Path: localPath},
	}
}

func TestCheckContainmentNoOverlap(t *testing.T) {
	sessions := []mutagen.Session{session("duck-default-abc", "/Users/a/dev/other")}
	got := CheckContainment("/Users/a/dev/project", sessions)
	if got.Kind != ContainmentNone {
		t.Fatalf("Kind = %v, want none", got.Kind)
	}
}

func TestCheckContainmentSamePathIsInside(t *testing.T) {
	sessions := []mutagen.Session{session("duck-default-abc", "/Users/a/dev/project")}
	got := CheckContainment("/Users/a/dev/project", sessions)
	if got.Kind != ContainmentInside {
		t.Fatalf("Kind = %v, want inside", got.Kind)
	}
	if got.Inside == nil || got.Inside.Name != "duck-default-abc" {
		t.Fatalf("Inside = %+v, want the matching session", got.Inside)
	}
}

func TestCheckContainmentNestedUnderExistingIsInside(t *testing.T) {
	sessions := []mutagen.Session{session("duck-default-abc", "/Users/a/dev")}
	got := CheckContainment("/Users/a/dev/project", sessions)
	if got.Kind != ContainmentInside {
		t.Fatalf("Kind = %v, want inside", got.Kind)
	}
}

func TestCheckContainmentParentOfExistingEncloses(t *testing.T) {
	sessions := []mutagen.Session{
		session("duck-default-abc", "/Users/a/dev/project"),
		session("duck-default-def", "/Users/a/dev/other"),
	}
	got := CheckContainment("/Users/a/dev", sessions)
	if got.Kind != ContainmentEncloses {
		t.Fatalf("Kind = %v, want encloses", got.Kind)
	}
	if len(got.Enclosed) != 2 {
		t.Fatalf("Enclosed = %+v, want both nested sessions", got.Enclosed)
	}
}

// TestCheckContainmentSiblingPrefixIsNotInside pins that a path which merely
// shares a string prefix with a session root (e.g. "/Users/a/dev2" vs
// "/Users/a/dev") is NOT treated as nested — the separator-aware check must
// not be fooled by prefix matching without a path boundary.
func TestCheckContainmentSiblingPrefixIsNotInside(t *testing.T) {
	sessions := []mutagen.Session{session("duck-default-abc", "/Users/a/dev")}
	got := CheckContainment("/Users/a/dev2", sessions)
	if got.Kind != ContainmentNone {
		t.Fatalf("Kind = %v, want none (sibling with shared prefix must not match)", got.Kind)
	}
}

// TestCheckContainmentRemoteAlphaIsSkipped pins that a session whose Alpha
// endpoint has a Host (not actually local to this machine) is ignored —
// containment is only meaningful between two local roots.
func TestCheckContainmentRemoteAlphaIsSkipped(t *testing.T) {
	sessions := []mutagen.Session{{
		Name:  "duck-default-abc",
		Alpha: mutagen.Endpoint{Host: "otherhost", Path: "/Users/a/dev/project"},
	}}
	got := CheckContainment("/Users/a/dev/project", sessions)
	if got.Kind != ContainmentNone {
		t.Fatalf("Kind = %v, want none (remote Alpha must be skipped)", got.Kind)
	}
}

func TestIsWithinUsesPathBoundary(t *testing.T) {
	if isWithin("/a/b", "/a/b") {
		t.Fatalf("a path is not within itself")
	}
	if !isWithin(filepath.Clean("/a/b/c"), filepath.Clean("/a/b")) {
		t.Fatalf("/a/b/c must be within /a/b")
	}
	if isWithin(filepath.Clean("/a/bc"), filepath.Clean("/a/b")) {
		t.Fatalf("/a/bc must NOT be within /a/b")
	}
}
