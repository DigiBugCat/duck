package claude

import "testing"

// TestSlugAndProjectDir pins the cwd→slug mapping to Claude Code's actual rule,
// verified empirically against ~/.claude/projects on disk: every "/" and "."
// becomes "-", case preserved. The "." case (a username like jane.doe) is the
// one that bites — it must collapse to "-", not survive.
func TestSlugAndProjectDir(t *testing.T) {
	cases := []struct{ abs, slug string }{
		{"/Users/jane.doe/dev", "-Users-jane-doe-dev"},
		{"/Users/jane.doe/dev/foo", "-Users-jane-doe-dev-foo"},
		{"/Users/me/Cassandra-Finance", "-Users-me-Cassandra-Finance"},
		{"/home/andrew/Obsidian/aviary", "-home-andrew-Obsidian-aviary"},
		{"/private/tmp", "-private-tmp"},
	}
	for _, tc := range cases {
		if got := Slug(tc.abs); got != tc.slug {
			t.Errorf("Slug(%q) = %q, want %q", tc.abs, got, tc.slug)
		}
		wantDir := "~/.claude/projects/" + tc.slug
		if got := ProjectDir(tc.abs); got != wantDir {
			t.Errorf("ProjectDir(%q) = %q, want %q", tc.abs, got, wantDir)
		}
	}
	if ProjectsRoot() != "~/.claude/projects" {
		t.Errorf("ProjectsRoot() = %q", ProjectsRoot())
	}
}
