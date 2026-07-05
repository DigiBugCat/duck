package window

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marks.json")

	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	s.Add(Mark{URL: "http://a", Text: "first"})
	s.Add(Mark{URL: "http://b", Text: "second"})
	s.Add(Mark{URL: "http://a", Text: "third"})

	// Reload from disk: Add must persist.
	s2, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore reload: %v", err)
	}

	all := s2.Marks("")
	if len(all) != 3 {
		t.Fatalf("Marks(\"\") = %d marks, want 3", len(all))
	}

	aMarks := s2.Marks("http://a")
	if len(aMarks) != 2 {
		t.Fatalf("Marks(http://a) = %d marks, want 2", len(aMarks))
	}
	// newest-last: "first" was added before "third" for the same URL.
	if aMarks[0].Text != "first" || aMarks[1].Text != "third" {
		t.Fatalf("Marks(http://a) order = %+v, want [first, third]", aMarks)
	}

	bMarks := s2.Marks("http://b")
	if len(bMarks) != 1 || bMarks[0].Text != "second" {
		t.Fatalf("Marks(http://b) = %+v, want [second]", bMarks)
	}

	if len(s2.Marks("http://nope")) != 0 {
		t.Fatalf("Marks(http://nope) should be empty")
	}
}

func TestMarkJSONRoundTripNewFields(t *testing.T) {
	in := Mark{
		Type:    "drawing",
		URL:     "http://example.test/chart",
		Comment: "circle this",
		Strokes: [][]Point{{
			{X: 10, Y: 20},
			{X: 30, Y: 45},
		}},
		Rect:  &Rect{X: 10, Y: 20, W: 20, H: 25},
		Shot:  "/home/andrew/.duck/window-shots/shot.png",
		Stamp: "2026-07-04T12:00:00Z",
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out Mark
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.Type != "drawing" || out.Shot != in.Shot || len(out.Strokes) != 1 || out.Rect == nil || out.Rect.W != 20 {
		t.Fatalf("round trip = %+v, want drawing fields preserved", out)
	}

	var old Mark
	if err := json.Unmarshal([]byte(`{"url":"u","text":"legacy","stamp":"2026-07-04T12:00:00Z"}`), &old); err != nil {
		t.Fatalf("Unmarshal old mark: %v", err)
	}
	if old.Type != "highlight" {
		t.Fatalf("old mark Type = %q, want highlight", old.Type)
	}
}

func TestHostTagsMarksWithCurrentWorkspace(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "marks.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := &Host{Store: store}
	h.curURL = "http://example.test/report"
	h.curWork = "work"

	h.addMark(Mark{Text: "revenue"})

	marks := store.MarksFor("", "work")
	if len(marks) != 1 {
		t.Fatalf("MarksFor workspace = %d, want 1", len(marks))
	}
	if marks[0].Workspace != "work" || marks[0].URL != "http://example.test/report" {
		t.Fatalf("mark attribution wrong: %+v", marks[0])
	}
	if got := store.MarksFor("", "other"); len(got) != 0 {
		t.Fatalf("wrong workspace should not match: %+v", got)
	}
}
