// meta.go extracts a script's `meta` block WITHOUT running the script. The
// block must be a pure object literal (`export const meta = {...}` — no
// variables, calls, or interpolation), which is what makes it safe to
// evaluate standalone: the run dir needs meta.name before any script side
// effect happens, and the MCP tool validates a script at submit time.
package workflow

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/dop251/goja"
)

// Meta is the declared identity of a workflow script.
type Meta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

var metaDeclRe = regexp.MustCompile(`(?m)^\s*(?:export\s+)?const\s+meta\s*=\s*`)

// ExtractMeta finds the leading meta literal, evaluates just that literal,
// and validates the required fields.
func ExtractMeta(script string) (Meta, error) {
	loc := metaDeclRe.FindStringIndex(script)
	if loc == nil {
		return Meta{}, fmt.Errorf("script must begin with `export const meta = {name, description, ...}`")
	}
	lit, err := balancedObject(script[loc[1]:])
	if err != nil {
		return Meta{}, fmt.Errorf("meta block: %w", err)
	}
	vm := goja.New()
	v, err := vm.RunString("(" + lit + ")")
	if err != nil {
		return Meta{}, fmt.Errorf("meta must be a pure object literal: %w", err)
	}
	// goja's default export maps Go field names, not json tags — go through a
	// plain map so the JS keys (lowercase) land where they should.
	obj, ok := v.Export().(map[string]any)
	if !ok {
		return Meta{}, fmt.Errorf("meta must be an object literal")
	}
	var m Meta
	m.Name, _ = obj["name"].(string)
	m.Description, _ = obj["description"].(string)
	m.Name = sanitizeName(m.Name)
	if m.Name == "" || m.Description == "" {
		return Meta{}, fmt.Errorf("meta requires non-empty name and description")
	}
	return m, nil
}

// balancedObject returns the {...} literal at the start of s, tracking
// strings (with escapes) and comments so braces inside them don't count.
func balancedObject(s string) (string, error) {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	if i >= len(s) || s[i] != '{' {
		return "", fmt.Errorf("expected '{' after `const meta =`")
	}
	depth := 0
	for j := i; j < len(s); j++ {
		switch c := s[j]; c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[i : j+1], nil
			}
		case '\'', '"', '`':
			end, err := skipString(s, j)
			if err != nil {
				return "", err
			}
			j = end
		case '/':
			if j+1 < len(s) {
				switch s[j+1] {
				case '/':
					for j < len(s) && s[j] != '\n' {
						j++
					}
				case '*':
					k := strings.Index(s[j+2:], "*/")
					if k < 0 {
						return "", fmt.Errorf("unterminated comment")
					}
					j += 2 + k + 1
				}
			}
		}
	}
	return "", fmt.Errorf("unbalanced braces")
}

// skipString returns the index of the closing quote matching s[start].
func skipString(s string, start int) (int, error) {
	q := s[start]
	for j := start + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case q:
			return j, nil
		}
	}
	return 0, fmt.Errorf("unterminated string in meta block")
}

var nameSanitizeRe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// sanitizeName keeps meta.name filesystem- and roster-safe.
func sanitizeName(n string) string {
	n = nameSanitizeRe.ReplaceAllString(strings.TrimSpace(n), "-")
	return strings.Trim(n, "-.")
}
