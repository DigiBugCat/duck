package model

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// TestGlyphForLiveness pins the glyph semantics: ↻ looped (outranks everything),
// ● attached, ◐ live-detached (active within idleThreshold), ○ idle/old.
func TestGlyphForLiveness(t *testing.T) {
	if g := GlyphFor(false, true, true, 10*time.Hour); g != loopGlyph {
		t.Fatalf("looped should be the loop glyph even when attached, got %q", g)
	}
	if g := GlyphFor(false, true, false, idleThreshold+time.Minute); g != loopGlyph {
		t.Fatalf("looped should be the loop glyph regardless of age, got %q", g)
	}
	if g := GlyphFor(false, false, true, 10*time.Hour); g != attachedGlyph {
		t.Fatalf("attached should be the attached glyph regardless of age, got %q", g)
	}
	if g := GlyphFor(false, false, false, 5*time.Minute); g != liveGlyph {
		t.Fatalf("recently-active detached should be the live glyph, got %q", g)
	}
	if g := GlyphFor(false, false, false, idleThreshold+time.Minute); g != idleGlyph {
		t.Fatalf("stale detached should be the idle glyph, got %q", g)
	}
	// The split is exclusive (age < idleThreshold): AT the threshold, idle.
	if g := GlyphFor(false, false, false, idleThreshold); g != idleGlyph {
		t.Fatalf("at exactly idleThreshold a detached session should be idle, got %q", g)
	}
}

// TestGlyphForEvicted: evicted wins over everything — the tmux session is gone.
func TestGlyphForEvicted(t *testing.T) {
	if g := GlyphFor(true, true, true, 0); g != evictedGlyph {
		t.Fatalf("evicted should be the evicted glyph regardless of other flags, got %q", g)
	}
}

// TestRenderRowFullWidthSpans: at full width a short row fills the terminal
// (metadata right-aligned to the edge).
func TestRenderRowFullWidthSpans(t *testing.T) {
	out := RenderRow(Row{Display: "x", Dir: "~/dev/x", Age: "1m", Windows: 1}, false, 120)
	if w := lineWidth(out); w != 120 {
		t.Fatalf("short row should span the full width 120, got %d", w)
	}
}

// TestRenderRowNarrowFits: below narrowWidth the row drops dir/windows and must
// not exceed the (small) pane width.
func TestRenderRowNarrowFits(t *testing.T) {
	out := RenderRow(Row{Display: "some-workspace-name", Dir: "~/dev/x", Age: "2h", Windows: 3}, true, 44)
	if w := lineWidth(out); w > 44 {
		t.Fatalf("narrow row width %d exceeds pane width 44", w)
	}
	if strings.Contains(out, "3w") {
		t.Fatalf("narrow row should drop the window count, got %q", out)
	}
}

// TestRenderRowWiresGlyph: an attached row shows ● regardless of age.
func TestRenderRowWiresGlyph(t *testing.T) {
	out := RenderRow(Row{Display: "x", Dir: "~/dev/x", Age: "9h", Attached: true}, false, 80)
	if !strings.Contains(out, "●") {
		t.Fatalf("attached row must show the attached glyph ●, got %q", out)
	}
}

func lineWidth(s string) int {
	w := 0
	for _, line := range strings.Split(s, "\n") {
		if lw := lipgloss.Width(line); lw > w {
			w = lw
		}
	}
	return w
}
