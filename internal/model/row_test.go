package model

import (
	"testing"
	"time"
)

func TestFilterFuzzySubsequence(t *testing.T) {
	rows := []Row{
		{Display: "Auth refactor", Dir: "~/dev/auth"},
		{Display: "Billing API", Dir: "~/dev/billing"},
		{Display: "Frontend", Dir: "~/dev/web"},
	}
	// Empty query returns all rows unchanged.
	if got := Filter(rows, ""); len(got) != 3 {
		t.Fatalf("empty query should return all rows, got %d", len(got))
	}
	// Subsequence match over Display: "athr" ⊆ "auth refactor".
	got := Filter(rows, "athr")
	if len(got) != 1 || got[0].Display != "Auth refactor" {
		t.Fatalf("fuzzy 'athr' should match Auth refactor only, got %+v", got)
	}
	// Match over Dir, case-insensitive.
	got = Filter(rows, "BILLING")
	if len(got) != 1 || got[0].Dir != "~/dev/billing" {
		t.Fatalf("fuzzy 'BILLING' should match billing dir, got %+v", got)
	}
	// No match yields empty.
	if got := Filter(rows, "zzzz"); len(got) != 0 {
		t.Fatalf("no-match query should be empty, got %+v", got)
	}
}

func TestRankAttachedThenRecency(t *testing.T) {
	now := time.Now()
	rows := []Row{
		{Display: "old-detached", LastSeen: now.Add(-3 * time.Hour)},
		{Display: "attached-old", Attached: true, LastSeen: now.Add(-2 * time.Hour)},
		{Display: "fresh-detached", LastSeen: now.Add(-1 * time.Minute)},
		{Display: "attached-fresh", Attached: true, LastSeen: now.Add(-30 * time.Second)},
	}
	got := Rank(rows)
	// Attached first (most-recent attached leads), then detached by recency.
	want := []string{"attached-fresh", "attached-old", "fresh-detached", "old-detached"}
	for i, w := range want {
		if got[i].Display != w {
			t.Fatalf("rank[%d] = %q, want %q (full: %v)", i, got[i].Display, w, names(got))
		}
	}
	// Rank must not mutate the input slice order.
	if rows[0].Display != "old-detached" {
		t.Fatalf("Rank mutated the input slice")
	}
}

func names(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Display
	}
	return out
}
