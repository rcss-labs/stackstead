package peers

import "testing"

func TestCompatible(t *testing.T) {
	cases := []struct {
		local, remote string
		want          bool
	}{
		{"v1.5.0", "v1.5.3", true},
		{"v1.4.9", "v1.5.0", false},
		{"1.5.0", "v1.5.1", true},
		{"dev", "dev", true},
		{"dev", "v1.5.0", false},
		{"v1.5.0", "dev", false},
		{"v0.3.0-5-gabc", "v0.3.1", true},
		{"v2.5.0", "v1.5.0", false},
	}
	for _, c := range cases {
		if got := Compatible(c.local, c.remote); got != c.want {
			t.Errorf("Compatible(%q, %q) = %v, want %v", c.local, c.remote, got, c.want)
		}
	}
}
