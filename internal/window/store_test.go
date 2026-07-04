package window

import (
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
