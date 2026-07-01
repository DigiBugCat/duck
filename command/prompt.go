// prompt.go is the production flow.Prompter: it asks on the controlling TTY
// whether to start a sync mirror for a risky, unknown folder. If stdin is not a
// terminal (piped, CI, no TTY) it returns ChoiceNo WITHOUT prompting, so bare
// `duck` in a non-interactive context never blocks and never silently starts a
// multi-GB mirror. The answer parsing lives in parseChoice so it is unit-tested
// without a TTY.
package command

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/DigiBugCat/duck/internal/flow"
	"github.com/mattn/go-isatty"
)

// ttyPrompter reads the sync decision from stdin when it is a terminal.
type ttyPrompter struct{}

// AskSync prints the sync prompt and reads a line. Non-TTY stdin returns
// ChoiceNo without reading. Parsing: y/yes→Yes, e/never→Never, anything else
// (incl. empty, n) →No.
func (ttyPrompter) AskSync(dir, reason string) (flow.Choice, error) {
	if !isatty.IsTerminal(os.Stdin.Fd()) && !isatty.IsCygwinTerminal(os.Stdin.Fd()) {
		return flow.ChoiceNo, nil
	}
	fmt.Fprintf(os.Stderr, "Sync %s? %s — could be a multi-GB mirror. [y]es / [n]o / n[e]ver ", dir, reason)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		// EOF/error with nothing read → treat as No (the safe default).
		return flow.ChoiceNo, nil
	}
	return parseChoice(line), nil
}

// AskConsolidate prints the consolidation prompt and reads a line. Non-TTY
// stdin returns false without reading, mirroring AskSync's safe default: a
// non-interactive run never auto-terminates an existing sync session.
func (ttyPrompter) AskConsolidate(parentDir, enclosedDisplay string) (bool, error) {
	if !isatty.IsTerminal(os.Stdin.Fd()) && !isatty.IsCygwinTerminal(os.Stdin.Fd()) {
		return false, nil
	}
	fmt.Fprintf(os.Stderr, "%s is already syncing separately under %s. Stop %s and let %s's new sync cover it? [y/N] ", enclosedDisplay, parentDir, enclosedDisplay, parentDir)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// parseChoice maps a raw answer line to a flow.Choice. y/yes→Yes, e/never→Never,
// everything else (empty, n, gibberish) →No. Case- and whitespace-insensitive.
func parseChoice(line string) flow.Choice {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return flow.ChoiceYes
	case "e", "never":
		return flow.ChoiceNever
	default:
		return flow.ChoiceNo
	}
}
