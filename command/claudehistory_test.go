package command

import (
	"reflect"
	"testing"
)

func TestDedupHomes(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"/Users/me", "/home/me", "/Users/me"}, []string{"/Users/me", "/home/me"}},
		{[]string{" /Users/me ", "", "/home/me", "   "}, []string{"/Users/me", "/home/me"}},
		{nil, nil},
		{[]string{"/only"}, []string{"/only"}},
	}
	for _, tc := range cases {
		if got := dedupHomes(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("dedupHomes(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
