package window

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/chromedp/cdproto/page"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

type markSink func(Mark)

// launchBackend is the browser backend selector. Darwin headful prefers the
// native WKWebView app unless DUCK_WINDOW_BACKEND=cdp forces the legacy CDP
// path; Linux and headless tests stay on chromedp.
func launchBackend(parent context.Context, headless bool, sink markSink) (browserBackend, error) {
	if runtime.GOOS == "darwin" && !headless && os.Getenv("DUCK_WINDOW_BACKEND") != "cdp" {
		return launchWebviewBackend(parent, sink)
	}
	return launchCDPBackend(parent, headless, sink)
}

// findChrome resolves a chromium binary: $DUCK_WINDOW_CHROME, the well-known
// system paths, then the puppeteer cache (present wherever gosling ran).
func findChrome() string {
	if p := os.Getenv("DUCK_WINDOW_CHROME"); p != "" {
		return p
	}
	for _, p := range []string{
		"/usr/bin/chromium", "/usr/bin/chromium-browser", "/usr/bin/google-chrome",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	home, _ := os.UserHomeDir()
	hits, _ := filepath.Glob(filepath.Join(home, ".cache/puppeteer/chrome/*/chrome-linux64/chrome"))
	if len(hits) > 0 {
		sort.Strings(hits)
		return hits[len(hits)-1]
	}
	return ""
}

type cdpBackend struct {
	tab     context.Context
	cancels []context.CancelFunc
}

func launchCDPBackend(parent context.Context, headless bool, sink markSink) (browserBackend, error) {
	chrome := findChrome()
	if chrome == "" {
		return nil, fmt.Errorf("no chromium found (set DUCK_WINDOW_CHROME)")
	}
	tab, cancels, err := launchCDPTab(parent, chrome, headless)
	if err != nil {
		return nil, err
	}

	// Marks arrive here: the page calls duckMark(json) and CDP delivers it.
	chromedp.ListenTarget(tab, func(ev interface{}) {
		if e, ok := ev.(*cdpruntime.EventBindingCalled); ok && e.Name == "duckMark" {
			if m, err := DecodeDuckMark(e.Payload); err == nil {
				sink(m)
			}
		}
	})
	if err := chromedp.Run(tab,
		chromedp.ActionFunc(func(ctx context.Context) error {
			if err := cdpruntime.AddBinding("duckMark").Do(ctx); err != nil {
				return err
			}
			_, err := page.AddScriptToEvaluateOnNewDocument(AnnotationJS).Do(ctx)
			return err
		}),
	); err != nil {
		for i := len(cancels) - 1; i >= 0; i-- {
			cancels[i]()
		}
		return nil, err
	}
	return &cdpBackend{tab: tab, cancels: cancels}, nil
}

func launchCDPTab(parent context.Context, chrome string, headless bool) (context.Context, []context.CancelFunc, error) {
	if runtime.GOOS == "darwin" && !headless {
		return launchDarwinCDPTab(parent, chrome)
	}
	home, _ := os.UserHomeDir()
	opts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(chrome),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.UserDataDir(filepath.Join(home, ".duck", "window-profile")),
	}
	if headless {
		opts = append(opts, chromedp.Headless, chromedp.DisableGPU,
			chromedp.NoSandbox) // hub containers lack userns; headless is test-only
	} else {
		// Headful: chromeless app window — a duck surface, not a browser.
		opts = append(opts, chromedp.Flag("app", "about:blank"))
	}
	actx, acancel := chromedp.NewExecAllocator(parent, opts...)
	tab, tcancel := chromedp.NewContext(actx)
	return tab, []context.CancelFunc{tcancel, acancel}, nil
}

func (b *cdpBackend) Navigate(url string) error {
	return chromedp.Run(b.tab, chromedp.Navigate(url), chromedp.WaitReady("body"))
}

func (b *cdpBackend) Eval(js string) error {
	return chromedp.Run(b.tab, chromedp.Evaluate(js, nil))
}

func (b *cdpBackend) Close() {
	for i := len(b.cancels) - 1; i >= 0; i-- {
		b.cancels[i]()
	}
}
