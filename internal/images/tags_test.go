package images

import "testing"

func TestSuggestTag(t *testing.T) {
	cases := []struct{ latest, suggest, hint string }{
		{"", "1", ""},
		{"7", "8", ""},
		{"1.4.0", "1.4.1", ""},
		{"v1.4", "v1.5", ""},
		{"2026.10", "2026.11", ""},
		{"latest", "", "latest"},
		{"1.4-rc1", "", "1.4-rc1"},
	}
	for _, c := range cases {
		suggest, hint := SuggestTag(c.latest)
		if suggest != c.suggest || hint != c.hint {
			t.Errorf("SuggestTag(%q) = (%q, %q), want (%q, %q)", c.latest, suggest, hint, c.suggest, c.hint)
		}
	}
}
