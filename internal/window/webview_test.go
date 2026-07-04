package window

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebviewProtocolEncodeDecode(t *testing.T) {
	nav, err := EncodeNavigateCommand("http://example.test/page")
	if err != nil {
		t.Fatalf("EncodeNavigateCommand: %v", err)
	}
	if got, want := string(nav), "{\"navigate\":\"http://example.test/page\"}\n"; got != want {
		t.Fatalf("navigate command = %q, want %q", got, want)
	}

	eval, err := EncodeEvalCommand("window.duckMark('x')")
	if err != nil {
		t.Fatalf("EncodeEvalCommand: %v", err)
	}
	if !strings.HasSuffix(string(eval), "\n") || !strings.Contains(string(eval), `"eval"`) {
		t.Fatalf("eval command is not a JSON line: %q", eval)
	}

	ev, err := DecodeWebviewEvent([]byte(`{"ready":true}`))
	if err != nil {
		t.Fatalf("DecodeWebviewEvent ready: %v", err)
	}
	if !ev.Ready {
		t.Fatalf("ready event did not decode as ready")
	}

	ev, err = DecodeWebviewEvent([]byte(`{"mark":{"url":"u","text":"hello","before":"b","after":"a"}}`))
	if err != nil {
		t.Fatalf("DecodeWebviewEvent mark: %v", err)
	}
	if ev.Mark == nil {
		t.Fatalf("mark event missing raw mark payload")
	}
	m, err := DecodeWebviewMark(json.RawMessage(*ev.Mark))
	if err != nil {
		t.Fatalf("DecodeWebviewMark: %v", err)
	}
	if m.Text != "hello" || m.URL != "u" {
		t.Fatalf("mark = %+v, want text/url decoded", m)
	}
}

func TestWebviewRuntimeShim(t *testing.T) {
	js := WebviewRuntimeJS()
	for _, want := range []string{
		"window.duckMark = s => window.webkit.messageHandlers.duckMark.postMessage(s);",
		"window.__duckAnnotate",
		"window.__duckApplyMarks",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("runtime JS missing %q", want)
		}
	}

	home := t.TempDir()
	path, err := writeWebviewRuntime(home)
	if err != nil {
		t.Fatalf("writeWebviewRuntime: %v", err)
	}
	if path != filepath.Join(home, ".duck", "window-runtime.js") {
		t.Fatalf("runtime path = %s, want ~/.duck/window-runtime.js", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read runtime: %v", err)
	}
	if string(b) != js {
		t.Fatalf("runtime file content did not match WebviewRuntimeJS")
	}
}

func TestEnsureNativeDuckWindowBundleWithFakeCompiler(t *testing.T) {
	oldCompile := compileNativeDuckWindowApp
	t.Cleanup(func() { compileNativeDuckWindowApp = oldCompile })
	var compiles int
	compileNativeDuckWindowApp = func(sourcePath, exePath string) error {
		compiles++
		source, err := os.ReadFile(sourcePath)
		if err != nil {
			return err
		}
		if !strings.Contains(string(source), "WKWebView") {
			t.Fatalf("generated Swift source missing WKWebView")
		}
		return os.WriteFile(exePath, []byte("#!/bin/sh\n"), 0o755)
	}

	home := t.TempDir()
	bundle, err := ensureNativeDuckWindowBundle(home)
	if err != nil {
		t.Fatalf("ensureNativeDuckWindowBundle: %v", err)
	}
	if bundle != filepath.Join(home, ".duck", nativeDuckWindowBundleName) {
		t.Fatalf("bundle path = %s", bundle)
	}
	if compiles != 1 {
		t.Fatalf("compile count = %d, want 1", compiles)
	}

	plist, err := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist"))
	if err != nil {
		t.Fatalf("read Info.plist: %v", err)
	}
	for _, want := range []string{"Duck Window", "cat.digibug.duck.window", "duck-window"} {
		if !strings.Contains(string(plist), want) {
			t.Fatalf("Info.plist missing %q:\n%s", want, plist)
		}
	}
	hash, err := os.ReadFile(filepath.Join(bundle, "Contents", "Resources", "main.swift.sha256"))
	if err != nil {
		t.Fatalf("read source hash: %v", err)
	}
	if strings.TrimSpace(string(hash)) != nativeSwiftSourceHash() {
		t.Fatalf("stored hash = %q, want source hash", hash)
	}

	if _, err := ensureNativeDuckWindowBundle(home); err != nil {
		t.Fatalf("second ensureNativeDuckWindowBundle: %v", err)
	}
	if compiles != 1 {
		t.Fatalf("second ensure recompiled unexpectedly; compile count = %d", compiles)
	}
}
