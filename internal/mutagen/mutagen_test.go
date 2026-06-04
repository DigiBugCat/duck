package mutagen

import (
	"strings"
	"testing"
)

// recordRun swaps runVar to capture every invocation's argv in order, without
// running the real mutagen binary.
func recordRun(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	orig := runVar
	runVar = func(args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	t.Cleanup(func() { runVar = orig })
	return &calls
}

func TestCreateUsesModeFlagNotSyncMode(t *testing.T) {
	calls := recordRun(t)
	if err := Create("duck-b1-abc", "/local", "h:dev/x", nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Two calls: daemon start, then sync create.
	if len(*calls) != 2 {
		t.Fatalf("Create made %d calls, want 2 (daemon start + sync create)", len(*calls))
	}
	create := strings.Join((*calls)[1], " ")

	// KEY FIX: the modern flag is -m / --mode two-way-resolved; the removed
	// --sync-mode flag (which mutagen 0.18.1 rejects) must NOT appear.
	if !strings.Contains(create, "-m two-way-resolved") {
		t.Errorf("sync create missing \"-m two-way-resolved\": %q", create)
	}
	if strings.Contains(create, "--sync-mode") {
		t.Errorf("sync create still uses the removed --sync-mode flag: %q", create)
	}
}

func TestCreateStartsDaemonFirst(t *testing.T) {
	calls := recordRun(t)
	if err := Create("duck-b1-abc", "/local", "h:dev/x", nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(*calls) < 2 {
		t.Fatalf("expected at least 2 calls, got %d", len(*calls))
	}
	// `mutagen daemon start` must precede the first `sync create`.
	first := strings.Join((*calls)[0], " ")
	if first != "daemon start" {
		t.Errorf("first mutagen call = %q, want \"daemon start\"", first)
	}
	second := strings.Join((*calls)[1], " ")
	if !strings.HasPrefix(second, "sync create") {
		t.Errorf("second mutagen call = %q, want it to start with \"sync create\"", second)
	}
}

func TestCreateIncludesEndpointsAndIgnores(t *testing.T) {
	calls := recordRun(t)
	if err := Create("duck-b1-abc", "/local/path", "h:dev/x", []string{"secrets"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	create := (*calls)[1]
	joined := strings.Join(create, " ")
	if !strings.Contains(joined, "--ignore-vcs") {
		t.Errorf("missing --ignore-vcs: %q", joined)
	}
	if !strings.Contains(joined, "--ignore secrets") {
		t.Errorf("missing extra ignore: %q", joined)
	}
	// alpha and beta endpoints are the final two args.
	if create[len(create)-2] != "/local/path" || create[len(create)-1] != "h:dev/x" {
		t.Errorf("endpoints not the final two args: %v", create)
	}
}

func TestListFilterUsesDuckPrefix(t *testing.T) {
	orig := outputVar
	outputVar = func(args ...string) (string, error) {
		// Two sessions: one duck-, one flok-. Only the duck- one should survive.
		return "duck-b1-abc|Watching|Local|||/x|SSH||h|dev/x\n" +
			"flok-b1-abc|Watching|Local|||/y|SSH||h|dev/y\n", nil
	}
	t.Cleanup(func() { outputVar = orig })

	sessions, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("List returned %d sessions, want 1 (duck- only)", len(sessions))
	}
	if sessions[0].Name != "duck-b1-abc" {
		t.Errorf("List returned %q, want duck-b1-abc", sessions[0].Name)
	}
}
