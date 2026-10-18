package update

import (
	"errors"
	"testing"
)

func mustParse(t *testing.T, s string) Version {
	t.Helper()
	v, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return v
}

func TestParseAndCompare(t *testing.T) {
	order := []struct {
		lo, hi string
	}{
		{"v0.2.0", "0.3.0"},
		{"0.3.0-rc.1", "0.3.0"},
		{"0.3.0", "0.3.0-2-gabcdef"},
		{"0.3.0-2-gabcdef", "0.3.0-10-g123abc"},
		{"0.3.0-2-gabcdef", "0.3.1"},
		{"0.3.0-rc.2", "0.3.0-rc.10"},
		{"0.3.0-rc.1", "0.3.0-rc.1.1"},
		{"0.3.0-1", "0.3.0-alpha"},
		{"0.9.0", "0.10.0"},
		{"1.9.9", "2.0.0"},
	}
	for _, c := range order {
		lo, hi := mustParse(t, c.lo), mustParse(t, c.hi)
		if got := Compare(lo, hi); got != -1 {
			t.Errorf("Compare(%s, %s) = %d, want -1", c.lo, c.hi, got)
		}
		if got := Compare(hi, lo); got != 1 {
			t.Errorf("Compare(%s, %s) = %d, want 1", c.hi, c.lo, got)
		}
	}
	if got := Compare(mustParse(t, "1.0.0+build"), mustParse(t, "1.0.0")); got != 0 {
		t.Errorf("build metadata must be ignored, got %d", got)
	}
	if got := Compare(mustParse(t, "v1.2.3"), mustParse(t, "1.2.3")); got != 0 {
		t.Errorf("leading v must be ignored, got %d", got)
	}

	for _, bad := range []string{"01.0.0", "1.0", "dev", "dev-dirty", "", "1.0.0.0", "12345678901.0.0", "1.0.0-", "1.0.0-a..b", "1.0.0-01", "v", "vv1.0.0", "1.a.0"} {
		if _, err := Parse(bad); !errors.Is(err, ErrNotRelease) {
			t.Errorf("Parse(%q) err = %v, want ErrNotRelease", bad, err)
		}
	}

	for s, want := range map[string]bool{"0.3.0": true, "v0.3.0+b": true, "0.3.0-rc.1": false, "0.3.0-2-gabcdef": false} {
		if got := mustParse(t, s).IsStable(); got != want {
			t.Errorf("IsStable(%s) = %v, want %v", s, got, want)
		}
	}
	if !mustParse(t, "0.3.0-2-gabcdef").IsPost {
		t.Error("-N-g<hex> must parse as post-tag")
	}
	for in, want := range map[string]string{"v0.3.0": "0.3.0", "0.3.0-rc.1": "0.3.0-rc.1", "1.0.0+x": "1.0.0"} {
		if got := mustParse(t, in).String(); got != want {
			t.Errorf("String(%s) = %q, want %q", in, got, want)
		}
	}
}
