package apitypes_test

import (
	"slices"
	"testing"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

func TestImagesJSONNames(t *testing.T) {
	cases := []struct {
		name string
		v    any
		key  string
	}{
		{"StackSummary.libraryUpdates", apitypes.StackSummary{LibraryUpdates: 1}, "libraryUpdates"},
		{"ServiceDetail.library", apitypes.ServiceDetail{Library: &apitypes.ServiceLibrary{}}, "library"},
		{"Status.arch", apitypes.Status{Arch: "amd64"}, "arch"},
		{"LibraryImage.latestVersion", apitypes.LibraryImage{LatestVersion: &apitypes.LibraryVersion{}}, "latestVersion"},
		{"LibraryImage.pushTargets", apitypes.LibraryImage{}, "pushTargets"},
		{"LibraryImageDetail.suggestedTag", apitypes.LibraryImageDetail{SuggestedTag: "1"}, "suggestedTag"},
		{"LibraryImageDetail.arch", apitypes.LibraryImageDetail{Arch: "amd64"}, "arch"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			keys := keysOf(t, c.v)
			if !slices.Contains(keys, c.key) {
				t.Fatalf("JSON keys %v lack %q", keys, c.key)
			}
		})
	}
}
