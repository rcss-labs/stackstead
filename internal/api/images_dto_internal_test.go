package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/images"
)

// The TS types are non-nullable, so empty lists and maps must encode as [] and {}.
func TestImageDTOEmptyCollectionsNeverNull(t *testing.T) {
	owner := images.ServerInfo{ID: "0123456789abcdef", Name: "A", Local: true, Status: "reachable"}
	entry := images.LibraryEntry{Image: images.Image{Slug: "web", Name: "Web"}, Owner: owner}
	detail := images.Detail{Image: entry.Image}
	for name, v := range map[string]any{
		"image":   toLibraryImage(entry),
		"detail":  toLibraryImageDetail(detail, owner),
		"library": toLibrary(images.Library{Servers: []images.ServerInfo{owner}}),
		"files":   toImageFiles(nil, 1),
		"revs":    toRevisionDTOs(nil, ""),
		"version": toVersionDetail(images.VersionDetail{}, owner, "web"),
	} {
		out, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "null") {
			t.Errorf("%s contains null: %s", name, out)
		}
	}
}
