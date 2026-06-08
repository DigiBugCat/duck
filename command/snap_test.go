package command

import "testing"

func TestCaptureArgs(t *testing.T) {
	for _, tt := range []struct {
		name string
		full bool
		want []string
	}{
		{"interactive selection", false, []string{"-i", "-x", "/tmp/x.png"}},
		{"full screen", true, []string{"-x", "/tmp/x.png"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := captureArgs(tt.full, "/tmp/x.png")
			if len(got) != len(tt.want) {
				t.Fatalf("captureArgs(%v) = %v, want %v", tt.full, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("captureArgs(%v)[%d] = %q, want %q", tt.full, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestUploadCommand(t *testing.T) {
	name := "shot-20260608-150405.png"
	cmd, path := uploadCommand(name)

	wantPath := "/tmp/duck-shots/" + name
	if path != wantPath {
		t.Errorf("remotePath = %q, want %q", path, wantPath)
	}
	// The dir is created before the file is written so the first snap succeeds,
	// and the file is piped via stdin (cat) so its bytes never touch the argv.
	wantCmd := "mkdir -p /tmp/duck-shots && cat > " + wantPath
	if cmd != wantCmd {
		t.Errorf("remoteCmd = %q, want %q", cmd, wantCmd)
	}
}
