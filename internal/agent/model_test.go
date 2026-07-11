package agent

import (
	"strings"
	"testing"
)

// TestWithModel pins the alias→codex-flag mapping: cross-provider aliases inject
// --profile (+ a catalog model override), same-provider aliases inject only
// -c model=, the default injects nothing, effort is orthogonal, non-codex argv
// is untouched, and a caller-set flag is never double-injected.
//
// No shipped alias is cross-provider today, so the profile mechanism is pinned
// via a test-local alias.
func TestWithModel(t *testing.T) {
	models["test-remote"] = modelSpec{Profile: "remote", Model: "remote-pro"}
	defer delete(models, "test-remote")

	cases := []struct {
		name   string
		in     []string
		model  string
		effort string
		want   string
	}{
		{"default empty", []string{"codex"}, "", "", "codex"},
		{"gpt-5.6-sol is the default → no inject", []string{"codex"}, "gpt-5.6-sol", "", "codex"},
		{"gpt-5.5 is now an explicit override", []string{"codex"}, "gpt-5.5", "",
			`codex -c model="gpt-5.5"`},
		{"cross-provider → profile + catalog model", []string{"codex"}, "test-remote", "",
			`codex --profile remote -c model="remote-pro"`},
		{"same-provider variant → model override only", []string{"codex"}, "gpt-5.4", "",
			`codex -c model="gpt-5.4"`},
		{"effort alone", []string{"codex"}, "", "high",
			`codex -c model_reasoning_effort="high"`},
		{"model + effort", []string{"codex"}, "gpt-5.4-mini", "low",
			`codex -c model="gpt-5.4-mini" -c model_reasoning_effort="low"`},
		{"exec inserts after subcommand", []string{"codex", "exec", "do it"}, "gpt-5.4", "",
			`codex exec -c model="gpt-5.4" do it`},
		{"non-codex untouched", []string{"cargo", "watch"}, "gpt-5.4", "high", "cargo watch"},
		{"caller-set --profile wins", []string{"codex", "--profile", "mine"}, "test-remote", "",
			// still injects the catalog model (only --profile was caller-set), not a second profile
			`codex -c model="remote-pro" --profile mine`},
		{"caller-set effort not doubled", []string{"codex", "-c", "model_reasoning_effort=medium"}, "", "high",
			"codex -c model_reasoning_effort=medium"},
	}
	for _, c := range cases {
		got, err := WithModel(c.in, c.model, c.effort)
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
			continue
		}
		if g := strings.Join(got, " "); g != c.want {
			t.Errorf("%s: WithModel(%v, %q, %q) = %q, want %q", c.name, c.in, c.model, c.effort, g, c.want)
		}
	}
}

// TestDefaultArgs pins the "route a command-less model spawn to codex" rule:
// a model/effort with no command becomes a codex agent; a bare spawn stays a
// shell; an explicit command is never overridden.
func TestDefaultArgs(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		model  string
		effort string
		want   []string
	}{
		{"empty + model → codex", nil, "gpt-5.4", "", []string{"codex"}},
		{"empty + effort → codex", nil, "", "high", []string{"codex"}},
		{"empty, no knobs → shell (unchanged)", nil, "", "", nil},
		{"explicit command wins over model", []string{"htop"}, "gpt-5.4", "", []string{"htop"}},
		{"explicit codex unchanged", []string{"codex", "exec", "x"}, "gpt-5.4", "", []string{"codex", "exec", "x"}},
	}
	for _, c := range cases {
		got := defaultArgs(c.args, c.model, c.effort)
		if strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("%s: defaultArgs(%v, %q, %q) = %v, want %v", c.name, c.args, c.model, c.effort, got, c.want)
		}
	}
}

// TestWithModelUnknown pins that an unknown alias fails loudly (so a spawn
// surfaces the error rather than launching the wrong model).
func TestWithModelUnknown(t *testing.T) {
	_, err := WithModel([]string{"codex"}, "gpt-9000", "")
	if err == nil {
		t.Fatal("expected error for unknown model alias")
	}
	if !strings.Contains(err.Error(), "unknown model") {
		t.Errorf("error = %v, want it to mention 'unknown model'", err)
	}
}
