package paths

import "testing"

// TestQuoteEscapesSingleQuotes pins paths.Quote — the single home of the
// remote-shell quoting rule the session, namer, and hub layers share (deduped
// from three byte-identical shellQuote copies). The contract: wrap in single
// quotes and render an embedded ' as the '\” escape, so a path can never break
// out of the quoting and inject shell.
func TestQuoteEscapesSingleQuotes(t *testing.T) {
	cases := map[string]string{
		"plain":       `'plain'`,
		"a b":         `'a b'`,
		"it's":        `'it'\''s'`,
		"$(rm -rf ~)": `'$(rm -rf ~)'`,
		";reboot":     `';reboot'`,
		"":            `''`,
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestClaudeProjectDir pins the cwd→slug mapping to Claude Code's actual rule,
// verified empirically against ~/.claude/projects on disk: every "/" and "."
// becomes "-", case preserved. The "." case (a username like jane.doe)
// is the one that bites — it must collapse to "-", not survive.
func TestClaudeProjectDir(t *testing.T) {
	cases := []struct{ abs, want string }{
		{"/Users/jane.doe/dev", "~/.claude/projects/-Users-jane-doe-dev"},
		{"/Users/jane.doe/dev/foo", "~/.claude/projects/-Users-jane-doe-dev-foo"},
		{"/Users/me/Cassandra-Finance", "~/.claude/projects/-Users-me-Cassandra-Finance"},
		{"/private/tmp", "~/.claude/projects/-private-tmp"},
	}
	for _, tc := range cases {
		if got := ClaudeProjectDir(tc.abs); got != tc.want {
			t.Errorf("ClaudeProjectDir(%q) = %q, want %q", tc.abs, got, tc.want)
		}
	}
}
