package picker

import (
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestMatch(t *testing.T) {
	cases := []struct {
		query, s string
		want     bool
	}{
		{"", "anything", true},
		{"abc", "abc", true},
		{"abc", "a-b-c", true},         // subsequence, not substring
		{"KW", "kill workspace", true}, // case-insensitive
		{"jmp", "jump: main", true},
		{"abc", "acb", false}, // order matters
		{"abc", "ab", false},
		{"x", "", false},
		{"kill", "jump: killdeer", true},
		{"fleet", "detach — duck -c resumes", false},
	}
	for _, c := range cases {
		if got := Match(c.query, c.s); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.query, c.s, got, c.want)
		}
	}
}

func TestVisibleIndices(t *testing.T) {
	items := []Item{
		{Label: "jump: main"},
		{Label: "jump: codex-1"},
		{Label: "kill: codex-1"},
		{Label: "kill workspace"},
		{Label: "detach — duck -c resumes"},
	}
	cases := []struct {
		filter string
		want   []int
	}{
		{"", []int{0, 1, 2, 3, 4}},
		{"jump", []int{0, 1}},
		{"kill", []int{2, 3}},
		{"cdx", []int{1, 2}}, // fuzzy subsequence
		{"zzz", nil},
	}
	for _, c := range cases {
		if got := visibleIndices(items, c.filter); !reflect.DeepEqual(got, c.want) {
			t.Errorf("visibleIndices(%q) = %v, want %v", c.filter, got, c.want)
		}
	}
}

// key drives a model with one keypress and returns the updated model.
func key(m model, k tea.KeyMsg) model {
	nm, _ := m.Update(k)
	return nm.(model)
}

func runes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestConfirmDefaultsYes(t *testing.T) {
	cases := []struct {
		name string
		key  tea.KeyMsg
		yes  bool
		quit bool
	}{
		{"enter accepts default", tea.KeyMsg{Type: tea.KeyEnter}, true, true},
		{"y confirms", runes("y"), true, true},
		{"n cancels", runes("n"), false, true},
		{"escape cancels", tea.KeyMsg{Type: tea.KeyEsc}, false, true},
		{"unrelated key waits", runes("x"), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, cmd := (confirmModel{prompt: "kill?"}).Update(tc.key)
			if got := m.(confirmModel).yes; got != tc.yes {
				t.Fatalf("yes = %v, want %v", got, tc.yes)
			}
			if got := cmd != nil; got != tc.quit {
				t.Fatalf("quit command present = %v, want %v", got, tc.quit)
			}
		})
	}
	if got := (confirmModel{prompt: "kill?"}).View(); got != "kill? "+keyStyle.Render("Y")+helpStyle.Render("/n ") {
		t.Fatalf("view = %q", got)
	}
}

func TestModelNavigationAndCommit(t *testing.T) {
	items := []Item{{Label: "alpha"}, {Label: "beta"}, {Label: "gamma"}}
	m := model{opts: Options{Items: items, ExtraKeys: []string{"x"}}, visible: visibleIndices(items, ""), chosen: -1}

	// j moves down (verb while no filter), enter commits the cursor row.
	m = key(m, runes("j"))
	if m.filter != "" || m.cursor != 1 {
		t.Fatalf("bare j should navigate: filter=%q cursor=%d", m.filter, m.cursor)
	}
	got := key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got.chosen != 1 || got.chosenKey != "enter" {
		t.Fatalf("enter commit: chosen=%d key=%q", got.chosen, got.chosenKey)
	}

	// x (ExtraKeys) commits with its own key.
	got = key(m, runes("x"))
	if got.chosen != 1 || got.chosenKey != "x" {
		t.Fatalf("x commit: chosen=%d key=%q", got.chosen, got.chosenKey)
	}

	// With a filter active, j is a plain filter rune and the chosen index maps
	// back to the ORIGINAL slice.
	m2 := model{opts: Options{Items: items}, visible: visibleIndices(items, ""), chosen: -1}
	m2 = key(m2, runes("g"))
	if len(m2.visible) != 1 || m2.visible[0] != 2 {
		t.Fatalf("filter g: visible=%v", m2.visible)
	}
	got = key(m2, tea.KeyMsg{Type: tea.KeyEnter})
	if got.chosen != 2 {
		t.Fatalf("filtered enter should map to original index 2, got %d", got.chosen)
	}

	// esc clears the filter first, then cancels.
	m2 = key(m2, tea.KeyMsg{Type: tea.KeyEsc})
	if m2.filter != "" || len(m2.visible) != 3 {
		t.Fatalf("esc should clear filter: %q %v", m2.filter, m2.visible)
	}
	got = key(m2, tea.KeyMsg{Type: tea.KeyEsc})
	if got.chosen != -1 {
		t.Fatalf("esc with no filter should cancel, chosen=%d", got.chosen)
	}
}

func TestNoFilterKeepsVerbs(t *testing.T) {
	items := []Item{{Label: "a"}, {Label: "b"}}
	m := model{opts: Options{Items: items, NoFilter: true, ExtraKeys: []string{"x"}}, visible: visibleIndices(items, ""), chosen: -1}
	m = key(m, runes("j"))
	if m.cursor != 1 {
		t.Fatalf("NoFilter j should navigate, cursor=%d", m.cursor)
	}
	m = key(m, runes("z")) // stray rune is ignored, not a filter
	if m.filter != "" || m.cursor != 1 {
		t.Fatalf("NoFilter must ignore runes: filter=%q cursor=%d", m.filter, m.cursor)
	}
	got := key(m, runes("x"))
	if got.chosen != 1 || got.chosenKey != "x" {
		t.Fatalf("NoFilter x commit: chosen=%d key=%q", got.chosen, got.chosenKey)
	}
}
