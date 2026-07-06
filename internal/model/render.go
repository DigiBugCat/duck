// render.go holds the SHARED row look for every duck surface that lists remote
// tmux sessions: the `--resume` picker (internal/tui) and the ⌂ ws roster tab
// (internal/panel). Both call RenderRow so a session row reads identically —
// same caret, liveness glyph, palette, and column alignment — whether you meet
// it full-screen (picker) or in the narrow sidebar (roster). model is a leaf
// package (no session/names/app deps), so both importers stay cycle-free.
//
// The palette uses lipgloss.AdaptiveColor so rows are readable on BOTH light-
// and dark-background terminals; lipgloss detects the background once (pin it
// with lipgloss.SetHasDarkBackground before the render loop grabs the TTY, else
// a detection miss defaults to the Dark variant).
package model

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// The row palette — duck's purple accent (#7D56F4 / #A78BFA) plus adaptive
// neutrals. Shared by picker and roster so the two never drift.
var (
	DisplayStyle    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#1F2937", Dark: "#E5E7EB"})
	DisplaySelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#F9FAFB", Dark: "#1F2937"}).
			Background(lipgloss.AdaptiveColor{Light: "#374151", Dark: "#E5E7EB"})
	DirStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#0369A1", Dark: "#7DD3FC"})
	AgeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	CaretStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"}).Bold(true)
	KeyStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"}).Bold(true)
	// Accent is duck's brand purple, for tab bars / titles that want the tie-in.
	Accent = lipgloss.Color("#7D56F4")

	attachedGlyph = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#059669", Dark: "#34D399"}).Bold(true).Render("●")
	liveGlyph     = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#D97706", Dark: "#FBBF24"}).Render("◐")
	idleGlyph     = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Render("○")
	// loopGlyph marks a session running a /loop; the recycle arrow in duck's accent.
	loopGlyph = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"}).Bold(true).Render("↻")
	// evictedGlyph marks a session whose tmux process was evicted to save RAM;
	// reviving it (recreate + claude --resume) precedes entering.
	evictedGlyph = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Render("⊘")
)

// idleThreshold splits "live-detached" (◐) from "idle/old" (○) by recency.
const idleThreshold = 2 * time.Hour

// GlyphFor maps state to the shared status glyph: ↻ looped (running a /loop —
// outranks everything), ⊘ evicted, ● attached, ◐ live-detached (active within
// idleThreshold), ○ idle/old. Pure function of the flags + last-active age so
// it is unit-testable without a wall clock.
func GlyphFor(evicted, looped, attached bool, age time.Duration) string {
	switch {
	case evicted:
		return evictedGlyph
	case looped:
		return loopGlyph
	case attached:
		return attachedGlyph
	case age < idleThreshold:
		return liveGlyph
	default:
		return idleGlyph
	}
}

// narrowWidth is the cutoff below which RenderRow drops the dir + window
// columns: the ⌂ ws roster tab lives in a ~34%-width sidebar (often 40–55
// cols), where the picker's full name/dir/age/windows layout would ghost-wrap.
// Below it we keep caret + glyph + name + right-aligned age — the *look*
// (glyph, caret, palette, alignment) without the columns that don't fit.
const narrowWidth = 70

// RenderRow renders one session row spanning the given width. At width >=
// narrowWidth it is the full picker layout (caret + glyph + name + dir +
// right-aligned age + window count); below it degrades to caret + glyph + name
// + right-aligned age. Every text column is width-truncated so nothing wraps.
func RenderRow(r Row, selected bool, width int) string {
	w := width
	if w <= 0 {
		w = 80
	}
	caret := "  "
	if selected {
		caret = CaretStyle.Render("› ")
	}
	glyph := GlyphFor(r.Evicted, r.Looped, r.Attached, time.Since(r.LastSeen))

	renderName := func(nameW int) string {
		n := padTrunc(r.Display, nameW)
		if selected {
			return DisplaySelStyle.Render(n)
		}
		return DisplayStyle.Render(n)
	}

	// Narrow (sidebar): caret + glyph + name + right-aligned age. No dir/windows.
	if w < narrowWidth {
		ageStr := r.Age
		rightW := lipgloss.Width(ageStr)
		nameW := w - 4 - rightW - 1 // caret(2)+glyph(1)+space(1), gap(1) before age
		if nameW < 6 {
			nameW = 6
		}
		name := renderName(nameW)
		left := caret + glyph + " " + name
		pad := w - (4 + nameW) - rightW
		if pad < 1 {
			pad = 1
		}
		return left + strings.Repeat(" ", pad) + AgeStyle.Render(ageStr)
	}

	// Full (picker): caret + glyph + name + dir + right-aligned age + windows.
	ageStr := r.Age
	winStr := itoa(r.Windows) + "w"
	rightW := lipgloss.Width(ageStr) + 2 + lipgloss.Width(winStr)

	avail := w - 8 - rightW
	if avail < 20 {
		avail = 20
	}
	nameW := avail * 9 / 20 // ~45% to the name, the rest to the dir
	if nameW < 10 {
		nameW = 10
	}
	dirW := avail - nameW
	if dirW < 6 {
		dirW = 6
	}

	name := renderName(nameW)
	dir := DirStyle.Render(padTrunc(r.Dir, dirW))
	left := caret + glyph + " " + name + "  " + dir
	leftW := 4 + nameW + 2 + dirW
	pad := w - leftW - rightW
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + AgeStyle.Render(ageStr) + "  " + AgeStyle.Render(winStr)
}

// padTrunc fits s into exactly w display columns: truncating with an ellipsis
// on overflow, padding with spaces on underflow. Uses lipgloss.Width so wide/
// multi-byte runes are measured correctly.
func padTrunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s + strings.Repeat(" ", w-lipgloss.Width(s))
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > w {
		runes = runes[:len(runes)-1]
	}
	out := string(runes) + "…"
	if pad := w - lipgloss.Width(out); pad > 0 {
		out += strings.Repeat(" ", pad)
	}
	return out
}

// itoa is a tiny strconv.Itoa without the import churn (matches the picker's).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
