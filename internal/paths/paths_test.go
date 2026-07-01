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
