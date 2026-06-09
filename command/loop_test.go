package command

import "testing"

// TestParseLoopState pins the on/off arg mapping: on/1/true → true, off/0/false
// → false, anything else is an error (so a typo never silently no-ops).
func TestParseLoopState(t *testing.T) {
	on := []string{"on", "1", "true"}
	for _, a := range on {
		got, err := parseLoopState(a)
		if err != nil || !got {
			t.Fatalf("parseLoopState(%q) = %v, %v; want true, nil", a, got, err)
		}
	}
	off := []string{"off", "0", "false"}
	for _, a := range off {
		got, err := parseLoopState(a)
		if err != nil || got {
			t.Fatalf("parseLoopState(%q) = %v, %v; want false, nil", a, got, err)
		}
	}
	for _, a := range []string{"", "yes", "toggle"} {
		if _, err := parseLoopState(a); err == nil {
			t.Fatalf("parseLoopState(%q) should error", a)
		}
	}
}

// TestLoopMarkerOptionMatchesSession guards the hand-synced constant: the command
// layer's marker name must equal the option the session layer reads, or `duck
// loop on` would set an option the picker never pins on.
func TestLoopMarkerOptionMatchesSession(t *testing.T) {
	if loopMarkerOption != "@duck_loop" {
		t.Fatalf("loopMarkerOption = %q, want @duck_loop (must match internal/session.loopOption)", loopMarkerOption)
	}
}
