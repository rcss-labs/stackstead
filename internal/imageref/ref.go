// Package imageref builds and parses library image references of the form
// stackstead.invalid/<owner-id>/<slug>:<tag>.
package imageref

import (
	"regexp"
	"strings"
)

const Domain = "stackstead.invalid"

const maxSlugLength = 40

var (
	SlugPattern  = regexp.MustCompile(`^[a-z0-9]+(?:[-_][a-z0-9]+)*$`)
	TagPattern   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	ownerPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)
)

// Ref identifies one tagged version of a library image.
type Ref struct{ Owner, Slug, Tag string }

// Usage records that a stack service runs a library image.
type Usage struct {
	Stack, Service string
	Ref            Ref
}

// OwnerID returns the owner component for an instance ID: its first 12
// characters, or "" when the ID is shorter than that.
func OwnerID(instanceID string) string {
	if len(instanceID) < 12 {
		return ""
	}
	return instanceID[:12]
}

// Format returns the library reference for the given parts.
func Format(owner, slug, tag string) string {
	return Domain + "/" + owner + "/" + slug + ":" + tag
}

// String returns the library reference for r.
func (r Ref) String() string {
	return Format(r.Owner, r.Slug, r.Tag)
}

// Parse reports whether s is a well-formed library reference and returns it.
func Parse(s string) (Ref, bool) {
	rest, ok := strings.CutPrefix(s, Domain+"/")
	if !ok {
		return Ref{}, false
	}
	owner, nameAndTag, ok := strings.Cut(rest, "/")
	if !ok || !ownerPattern.MatchString(owner) {
		return Ref{}, false
	}
	slug, tag, ok := strings.Cut(nameAndTag, ":")
	if !ok || len(slug) > maxSlugLength || !SlugPattern.MatchString(slug) || !TagPattern.MatchString(tag) {
		return Ref{}, false
	}
	return Ref{Owner: owner, Slug: slug, Tag: tag}, true
}

// IsLibrary reports whether s is in the library's reference domain.
func IsLibrary(s string) bool {
	return strings.HasPrefix(s, Domain+"/")
}
