package images

import (
	"errors"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/stacks"
)

func isValidation(err error) bool {
	var ve *ValidationError
	return errors.As(err, &ve)
}

func TestValidateName(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"My App", true},
		{"a", true},
		{strings.Repeat("a", 64), true},
		{"", false},
		{"   ", false},
		{strings.Repeat("a", 65), false},
		{"bad\x00name", false},
		{"tab\tname", false},
	}
	for _, c := range cases {
		err := ValidateName(c.name)
		if (err == nil) != c.ok {
			t.Errorf("ValidateName(%q) = %v, want ok=%v", c.name, err, c.ok)
		}
		if err != nil && !isValidation(err) {
			t.Errorf("ValidateName(%q) error %T is not a ValidationError", c.name, err)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"My App!", "my-app"},
		{"  Hello   World ", "hello-world"},
		{"under_score", "under_score"},
		{"--a--b--", "a-b"},
		{"Ünïcode name", "n-code-name"},
		{"!!!", ""},
		{strings.Repeat("a", 60), strings.Repeat("a", 40)},
		{strings.Repeat("a", 39) + " b", strings.Repeat("a", 39)},
	}
	for _, c := range cases {
		if got := Slugify(c.in); got != c.want {
			t.Errorf("Slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidateSlug(t *testing.T) {
	cases := []struct {
		slug string
		ok   bool
	}{
		{"my-app", true},
		{"a_b", true},
		{strings.Repeat("a", 40), true},
		{strings.Repeat("a", 41), false},
		{"", false},
		{"My-App", false},
		{"-a", false},
		{"a--b", false},
		{"a b", false},
	}
	for _, c := range cases {
		err := ValidateSlug(c.slug)
		if (err == nil) != c.ok {
			t.Errorf("ValidateSlug(%q) = %v, want ok=%v", c.slug, err, c.ok)
		}
		if err != nil && !isValidation(err) {
			t.Errorf("ValidateSlug(%q) error %T is not a ValidationError", c.slug, err)
		}
	}
}

func TestValidateTag(t *testing.T) {
	cases := []struct {
		tag string
		ok  bool
	}{
		{"1", true},
		{"v1.4.0-rc1", true},
		{"_x", true},
		{strings.Repeat("a", 128), true},
		{strings.Repeat("a", 129), false},
		{"", false},
		{".x", false},
		{"-x", false},
		{"a:b", false},
		{"a b", false},
	}
	for _, c := range cases {
		err := ValidateTag(c.tag)
		if (err == nil) != c.ok {
			t.Errorf("ValidateTag(%q) = %v, want ok=%v", c.tag, err, c.ok)
		}
		if err != nil && !isValidation(err) {
			t.Errorf("ValidateTag(%q) error %T is not a ValidationError", c.tag, err)
		}
	}
}

func TestValidateBuildArgs(t *testing.T) {
	many := map[string]string{}
	for i := 0; i < 65; i++ {
		many["K"+strings.Repeat("x", i)] = "v"
	}
	sixtyFour := map[string]string{}
	for i := 0; i < 64; i++ {
		sixtyFour["K"+strings.Repeat("x", i)] = "v"
	}
	cases := []struct {
		name string
		args map[string]string
		ok   bool
	}{
		{"nil", nil, true},
		{"simple", map[string]string{"VERSION": "1", "_a1": ""}, true},
		{"64 keys", sixtyFour, true},
		{"65 keys", many, false},
		{"digit first", map[string]string{"1A": "x"}, false},
		{"dash", map[string]string{"A-B": "x"}, false},
		{"empty key", map[string]string{"": "x"}, false},
		{"value 4KiB", map[string]string{"A": strings.Repeat("x", 4096)}, true},
		{"value over 4KiB", map[string]string{"A": strings.Repeat("x", 4097)}, false},
		{"NUL in value", map[string]string{"A": "a\x00b"}, false},
	}
	for _, c := range cases {
		err := ValidateBuildArgs(c.args)
		if (err == nil) != c.ok {
			t.Errorf("%s: ValidateBuildArgs = %v, want ok=%v", c.name, err, c.ok)
		}
		if err != nil && !isValidation(err) {
			t.Errorf("%s: error %T is not a ValidationError", c.name, err)
		}
	}
}

var _ *stacks.ValidationError = (*ValidationError)(nil)
