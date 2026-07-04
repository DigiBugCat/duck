//go:build !darwin

package window

import (
	"context"
	"fmt"
)

func launchWebviewBackend(context.Context, markSink) (browserBackend, error) {
	return nil, fmt.Errorf("duck window: native WKWebView backend is only available on darwin")
}
