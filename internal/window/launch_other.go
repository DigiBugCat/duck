//go:build !darwin

package window

import (
	"context"
	"fmt"
)

// launchDarwinTab is unreachable on non-darwin (launchTab only calls it when
// runtime.GOOS == "darwin"), but the symbol must exist so window.go builds
// on every platform (notably the linux hub, per CLAUDE.md: keep `go build
// ./... && go test ./...` green on linux).
func launchDarwinTab(_ context.Context, _ string) (context.Context, []context.CancelFunc, error) {
	return nil, nil, fmt.Errorf("duck window: darwin-only launch path called on a non-darwin platform")
}
