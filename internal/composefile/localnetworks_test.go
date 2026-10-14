package composefile

import (
	"reflect"
	"testing"
)

func TestLocalNetworksDecodesExternalAsYAMLBool(t *testing.T) {
	cases := []struct {
		name     string
		external string
		local    bool
	}{
		{"true", "true", false},
		{"True", "True", false},
		{"TRUE", "TRUE", false},
		{"yes", "yes", false},
		{"false", "false", true},
		{"no", "no", true},
		{"absent", "", true},
		{"mapping form", "{name: shared}", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := "services:\n  a:\n    image: x\nnetworks:\n  lan:\n"
			if c.external != "" {
				src += "    external: " + c.external + "\n"
			} else {
				src += "    driver: bridge\n"
			}
			f, err := Parse([]byte(src))
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]bool{}
			if c.local {
				want["lan"] = true
			}
			if got := f.LocalNetworks(); !reflect.DeepEqual(got, want) {
				t.Errorf("LocalNetworks = %v, want %v", got, want)
			}
		})
	}
}
