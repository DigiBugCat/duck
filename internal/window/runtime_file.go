package window

import (
	"os"
	"path/filepath"
)

const duckMarkWebkitShim = `window.duckMark = s => window.webkit.messageHandlers.duckMark.postMessage(s);
`

func WebviewRuntimeJS() string {
	return duckMarkWebkitShim + AnnotationJS
}

func writeWebviewRuntime(home string) (string, error) {
	path := filepath.Join(home, ".duck", "window-runtime.js")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	content := WebviewRuntimeJS()
	if cur, err := os.ReadFile(path); err == nil && string(cur) == content {
		return path, nil
	}
	return path, os.WriteFile(path, []byte(content), 0o644)
}
