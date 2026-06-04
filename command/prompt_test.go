package command

import (
	"os"
	"testing"

	"github.com/DigiBugCat/duck/internal/flow"
)

// TestAskSyncNoTTYReturnsNo pins the production short-circuit (item 3): when
// stdin is not a terminal, ttyPrompter.AskSync returns flow.ChoiceNo WITHOUT
// reading, so bare `duck`/-c/--resume in a non-interactive context never blocks
// and never silently starts a multi-GB mirror. We point os.Stdin at /dev/null —
// not a TTY (→ the isatty short-circuit) and, even if read, an immediate EOF —
// so the test can never hang regardless of the ambient test harness's stdin.
func TestAskSyncNoTTYReturnsNo(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer f.Close()
	old := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = old }()

	got, err := ttyPrompter{}.AskSync("~", "home directory")
	if err != nil {
		t.Fatalf("AskSync: %v", err)
	}
	if got != flow.ChoiceNo {
		t.Fatalf("no-TTY AskSync = %v, want ChoiceNo", got)
	}
}

// TestParseChoice pins the sync-prompt answer parsing: y/yes→Yes, e/never→Never,
// n/empty/anything-else→No, case- and whitespace-insensitive.
func TestParseChoice(t *testing.T) {
	cases := []struct {
		in   string
		want flow.Choice
	}{
		{"y", flow.ChoiceYes},
		{"yes", flow.ChoiceYes},
		{"YES", flow.ChoiceYes},
		{" y \n", flow.ChoiceYes},
		{"e", flow.ChoiceNever},
		{"never", flow.ChoiceNever},
		{"NEVER", flow.ChoiceNever},
		{"n", flow.ChoiceNo},
		{"no", flow.ChoiceNo},
		{"", flow.ChoiceNo},
		{"\n", flow.ChoiceNo},
		{"garbage", flow.ChoiceNo},
	}
	for _, tc := range cases {
		if got := parseChoice(tc.in); got != tc.want {
			t.Fatalf("parseChoice(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
