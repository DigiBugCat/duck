//go:build !darwin

package window

import (
	"context"
	"fmt"
)

// launchDarwinCDPTab is unreachable on non-darwin (launchCDPTab only calls it when
// runtime.GOOS == "darwin"), but the symbol must exist so window.go builds
// on every platform (notably the linux hub, per CLAUDE.md: keep `go build
// ./... && go test ./...` green on linux).
func launchDarwinCDPTab(_ context.Context, _ string) (context.Context, []context.CancelFunc, error) {
	return nil, nil, fmt.Errorf("duck window: darwin-only launch path called on a non-darwin platform")
}
