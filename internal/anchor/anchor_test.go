package anchor

import (
	"io"
	"strings"
	"testing"
)

// fakeRunner records every command string and streams back canned output,
// mirroring the fake used by internal/names' tests.
type fakeRunner struct {
	cmds      []string
	out       map[string]string
	lastInput string
}

func (f *fakeRunner) Run(cmd string) (string, error) {
	f.cmds = append(f.cmds, cmd)
	if f.out != nil {
		if v, ok := f.out[cmd]; ok {
			return v, nil
		}
	}
	return "", nil
}

func (f *fakeRunner) RunInput(cmd string, stdin io.Reader) (string, error) {
	f.cmds = append(f.cmds, cmd)
	if stdin != nil {
		if b, err := io.ReadAll(stdin); err == nil {
			f.lastInput = string(b)
		}
	}
	return "", nil
}

func TestLoadMissingFileReturnsZeroState(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		"cat " + remotePath + " 2>/dev/null || echo '{}'": "{}\n",
	}}
	s := NewStore(r)
	st, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if st.Hub != "" || st.HubName != "" || len(st.Config) != 0 {
		t.Fatalf("Load on a missing file must yield a zero State, got %+v", st)
	}
}

func TestLoadParsesExistingState(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		"cat " + remotePath + " 2>/dev/null || echo '{}'": `{"hub":"me@box","hubName":"box","config":{"codex_model":"gpt"}}`,
	}}
	s := NewStore(r)
	st, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if st.Hub != "me@box" || st.HubName != "box" || st.Config["codex_model"] != "gpt" {
		t.Fatalf("Load = %+v, want the parsed document", st)
	}
}

func TestSaveWritesAtomicTempThenRename(t *testing.T) {
	r := &fakeRunner{}
	s := NewStore(r)
	if err := s.Save(State{Hub: "me@box", Config: map[string]string{"attach_transport": "auto"}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(r.cmds) != 1 {
		t.Fatalf("Save must issue exactly one SSH call, got %d: %v", len(r.cmds), r.cmds)
	}
	wantCmd := "mkdir -p ~/.duck && cat > " + tmpPath + " && mv " + tmpPath + " " + remotePath
	if r.cmds[0] != wantCmd {
		t.Fatalf("cmd = %q, want %q", r.cmds[0], wantCmd)
	}
	if !strings.Contains(r.lastInput, `"hub": "me@box"`) {
		t.Fatalf("streamed JSON = %q, want it to contain the hub field", r.lastInput)
	}
}
