package images

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rcss-labs/stackstead/internal/imageref"
)

const (
	maxNameRunes     = 64
	maxSlugLength    = 40
	maxBuildArgCount = 64
	maxBuildArgBytes = 4096
)

var buildArgKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateName accepts 1-64 printable characters.
func ValidateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fieldError("name", "name is required")
	}
	if utf8.RuneCountInString(name) > maxNameRunes {
		return fieldError("name", "name must be at most 64 characters")
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return fieldError("name", "name must contain only printable characters")
		}
	}
	return nil
}

// Slugify derives a slug from a name: lowercase ASCII letters and digits
// joined by single "-" (or "_" when the name had a lone underscore), at most
// 40 characters. It returns "" when the name has no usable characters.
func Slugify(name string) string {
	var b strings.Builder
	sep := ""
	pending := false
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z':
			r += 'a' - 'A'
			fallthrough
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if pending && b.Len() > 0 {
				if sep == "" {
					sep = "-"
				}
				b.WriteString(sep)
			}
			sep, pending = "", false
			b.WriteRune(r)
		case r == '_' && !pending:
			sep, pending = "_", true
		default:
			sep, pending = "-", true
		}
	}
	slug := b.String()
	if len(slug) > maxSlugLength {
		slug = slug[:maxSlugLength]
	}
	return strings.TrimRight(slug, "-_")
}

// reservedSlugs would collide with fixed routes under /api/images.
var reservedSlugs = map[string]bool{"registries": true, "fetch": true}

// ValidateSlug enforces the slug pattern and the 40-character maximum.
func ValidateSlug(slug string) error {
	switch {
	case slug == "":
		return fieldError("slug", "slug is required")
	case len(slug) > maxSlugLength:
		return fieldError("slug", "slug must be at most 40 characters")
	case !imageref.SlugPattern.MatchString(slug):
		return fieldError("slug", "slug may contain lowercase letters, digits, and single - or _ between them")
	case reservedSlugs[slug]:
		return fieldError("slug", "this name is reserved")
	}
	return nil
}

// ValidateTag enforces the version tag pattern.
func ValidateTag(tag string) error {
	if tag == "" {
		return fieldError("tag", "tag is required")
	}
	if !imageref.TagPattern.MatchString(tag) {
		return fieldError("tag", "tag must start with a letter, digit or _, contain only letters, digits, _, . and -, and be at most 128 characters")
	}
	return nil
}

// ValidateBuildArgs checks build argument names and values.
func ValidateBuildArgs(args map[string]string) error {
	if len(args) > maxBuildArgCount {
		return fieldError("buildArgs", "at most 64 build arguments are allowed")
	}
	for k, v := range args {
		if !buildArgKeyPattern.MatchString(k) {
			return fieldError("buildArgs", "build argument name "+quoteArg(k)+" is invalid: use letters, digits and _, not starting with a digit")
		}
		if len(v) > maxBuildArgBytes {
			return fieldError("buildArgs", "value of build argument "+k+" is larger than 4 KiB")
		}
		if strings.IndexByte(v, 0) >= 0 {
			return fieldError("buildArgs", "value of build argument "+k+" must not contain NUL bytes")
		}
	}
	return nil
}

func quoteArg(s string) string {
	if len(s) > 40 {
		s = s[:40] + "..."
	}
	return `"` + strings.ReplaceAll(s, "\x00", "") + `"`
}
