// Model/effort selection for spawned codex agents. Callers pass a duck-level
// model ALIAS (e.g. "deepseek", "gpt-5.4") plus an optional reasoning effort;
// this file maps that to the right codex mechanism and injects it into the argv.
//
// Two distinct codex levers hide behind one alias, which is the whole point of
// the map — a caller shouldn't have to know which one an alias needs:
//
//   - cross-provider (reach a NON-default provider like DeepSeek via Moon Bridge):
//     needs a whole config bundle → `--profile <name>`, optionally with a model
//     override to pick a variant within that provider's catalog.
//   - same-provider (a different model on the default OpenAI provider):
//     just `-c model="<id>"`; no profile.
//
// The default (empty alias, or "gpt-5.5") injects nothing — the agent inherits
// ~/.codex/config.toml, so the existing fleet is untouched.
package agent

import (
	"fmt"
	"sort"
	"strings"
)

// modelSpec is how one alias maps onto codex flags. Exactly one of Profile /
// Model is the primary lever; Model may ALSO be set alongside Profile to pick a
// variant inside that profile's provider catalog (e.g. DeepSeek Flash).
type modelSpec struct {
	Profile string // → --profile <Profile> (empty = default provider)
	Model   string // → -c model="<Model>"  (empty = profile/config default)
}

// models is the curated alias table. Keep entries in sync with the codex
// profiles that actually exist in ~/.codex (deepseek.config.toml) and the gpt
// models the default provider serves. Adding a provider = add a profile file +
// a row here.
var models = map[string]modelSpec{
	// DeepSeek V4 via the Moon Bridge proxy (see deepseek.config.toml).
	"deepseek":       {Profile: "deepseek", Model: "deepseek-v4-pro"},
	"deepseek-pro":   {Profile: "deepseek", Model: "deepseek-v4-pro"},
	"deepseek-flash": {Profile: "deepseek", Model: "deepseek-v4-flash"},

	// Default OpenAI provider — variant models via a plain model override.
	// "gpt-5.5" is the config default → no override (empty modelSpec).
	"gpt-5.5":            {},
	"gpt-5.4":            {Model: "gpt-5.4"},
	"gpt-5.4-mini":       {Model: "gpt-5.4-mini"},
	"gpt-5.3-codex-spark": {Model: "gpt-5.3-codex-spark"},
}

// KnownModels returns the sorted alias list — for flag help and error messages.
func KnownModels() []string {
	out := make([]string, 0, len(models))
	for k := range models {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// resolveModel maps an alias to its spec. An empty alias is the default (no
// injection). An unknown alias is an error — callers surface it rather than
// silently launching the wrong model.
func resolveModel(alias string) (modelSpec, error) {
	if alias == "" {
		return modelSpec{}, nil
	}
	spec, ok := models[strings.ToLower(alias)]
	if !ok {
		return modelSpec{}, fmt.Errorf("unknown model %q (known: %s)", alias, strings.Join(KnownModels(), ", "))
	}
	return spec, nil
}

// defaultArgs routes a command-less spawn to codex when a model or effort was
// requested. Those knobs only mean anything to codex (a shell has no model), so
// their presence is the signal the caller wanted a codex agent — we infer that
// rather than spawning a bare shell that silently ignores the flags. An empty
// spawn with no model/effort stays a shell (unchanged).
func defaultArgs(args []string, model, effort string) []string {
	if len(args) == 0 && (model != "" || effort != "") {
		return []string{"codex"}
	}
	return args
}

// WithModel injects the model/effort selection for a codex argv. A non-codex
// argv, or an empty model+effort, is returned unchanged. Idempotent in the same
// sense as the other injectors: if the caller already set --profile / -c model /
// -c model_reasoning_effort by hand, that flag is left alone.
//
// alias is a duck model alias (see the models table); effort is passed straight
// to codex as model_reasoning_effort (low|medium|high|none — codex validates).
func WithModel(args []string, alias, effort string) ([]string, error) {
	if !isCodex(args) {
		return args, nil
	}
	spec, err := resolveModel(alias)
	if err != nil {
		return nil, err
	}

	// Detect caller-set flags so we never double-inject (mirrors the other
	// injectors' "user stated a preference → back off" rule).
	var hasProfile, hasModel, hasEffort bool
	for i, a := range args {
		switch {
		case a == "--profile" || strings.HasPrefix(a, "--profile="):
			hasProfile = true
		case a == "-c" && i+1 < len(args) && strings.HasPrefix(args[i+1], "model="):
			hasModel = true
		case a == "-c" && i+1 < len(args) && strings.HasPrefix(args[i+1], "model_reasoning_effort="):
			hasEffort = true
		}
	}

	at := codexInsertAt(args)
	var inject []string
	if spec.Profile != "" && !hasProfile {
		inject = append(inject, "--profile", spec.Profile)
	}
	if spec.Model != "" && !hasModel {
		inject = append(inject, "-c", fmt.Sprintf("model=%q", spec.Model))
	}
	if effort != "" && !hasEffort {
		inject = append(inject, "-c", fmt.Sprintf("model_reasoning_effort=%q", effort))
	}
	if len(inject) == 0 {
		return args, nil
	}

	out := append([]string{}, args[:at]...)
	out = append(out, inject...)
	return append(out, args[at:]...), nil
}
