package sync

import "testing"

func TestBundleFromSessionName(t *testing.T) {
	cases := map[string]string{
		"duck-duck-0123456789ab":      "duck",
		"duck-my-bundle-0123456789ab": "my-bundle", // dashes inside the bundle survive
		"duck-x-0123456789ab":         "x",
		"duck-0123456789ab":           "", // no bundle at all
		"flok-duck-0123456789ab":      "",
		"duck-":                       "",
		"":                            "",
	}
	for name, want := range cases {
		if got := bundleFromSessionName(name); got != want {
			t.Errorf("bundleFromSessionName(%q) = %q, want %q", name, got, want)
		}
	}
}
