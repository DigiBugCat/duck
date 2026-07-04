package window

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	duckwindow "github.com/DigiBugCat/duck/native/duck-window"
)

const nativeDuckWindowBundleName = "duck-window.app"

var compileNativeDuckWindowApp = defaultCompileNativeDuckWindowApp

func nativeDuckWindowInfoPlist() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key>
	<string>Duck Window</string>
	<key>CFBundleDisplayName</key>
	<string>Duck Window</string>
	<key>CFBundleIdentifier</key>
	<string>cat.digibug.duck.window</string>
	<key>CFBundleExecutable</key>
	<string>duck-window</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>1.0</string>
	<key>LSMinimumSystemVersion</key>
	<string>11.0</string>
	<key>LSUIElement</key>
	<false/>
</dict>
</plist>
`
}

func nativeSwiftSource() string {
	return duckwindow.MainSwift
}

func nativeSwiftSourceHash() string {
	sum := sha256.Sum256([]byte(nativeSwiftSource()))
	return hex.EncodeToString(sum[:])
}

func ensureNativeDuckWindowBundle(home string) (string, error) {
	if home == "" {
		return "", fmt.Errorf("no home directory")
	}
	bundle := filepath.Join(home, ".duck", nativeDuckWindowBundleName)
	contentsDir := filepath.Join(bundle, "Contents")
	macosDir := filepath.Join(contentsDir, "MacOS")
	resourcesDir := filepath.Join(contentsDir, "Resources")
	if err := os.MkdirAll(macosDir, 0o755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(resourcesDir, 0o755); err != nil {
		return "", err
	}

	plistPath := filepath.Join(contentsDir, "Info.plist")
	if err := writeFileIfChanged(plistPath, []byte(nativeDuckWindowInfoPlist()), 0o644); err != nil {
		return "", err
	}

	sourcePath := filepath.Join(resourcesDir, "main.swift")
	source := nativeSwiftSource()
	if err := writeFileIfChanged(sourcePath, []byte(source), 0o644); err != nil {
		return "", err
	}

	exePath := filepath.Join(macosDir, "duck-window")
	hashPath := filepath.Join(resourcesDir, "main.swift.sha256")
	wantHash := nativeSwiftSourceHash()
	if bundleNeedsCompile(exePath, hashPath, wantHash) {
		if err := compileNativeDuckWindowApp(sourcePath, exePath); err != nil {
			return "", err
		}
		if err := os.WriteFile(hashPath, []byte(wantHash+"\n"), 0o644); err != nil {
			return "", err
		}
	}
	return bundle, nil
}

func defaultCompileNativeDuckWindowApp(sourcePath, exePath string) error {
	if _, err := exec.LookPath("swiftc"); err != nil {
		return fmt.Errorf("swiftc not found; install command line tools with: xcode-select --install")
	}
	cmd := exec.Command("swiftc", "-O", sourcePath, "-o", exePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("swiftc duck-window: %w\n%s", err, string(out))
	}
	return os.Chmod(exePath, 0o755)
}

func bundleNeedsCompile(exePath, hashPath, wantHash string) bool {
	if fi, err := os.Stat(exePath); err != nil || fi.Mode()&0o111 == 0 {
		return true
	}
	cur, err := os.ReadFile(hashPath)
	return err != nil || string(cur) != wantHash+"\n"
}

func writeFileIfChanged(path string, content []byte, mode os.FileMode) error {
	if cur, err := os.ReadFile(path); err == nil && string(cur) == string(content) {
		return nil
	}
	return os.WriteFile(path, content, mode)
}
