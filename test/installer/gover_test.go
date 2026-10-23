//go:build installer

package installer_test

import (
	"strings"
	"testing"
)

// The release guard must refuse to build release artifacts with a Go older than
// the patched floor (issue #23): a go.mod bump alone cannot patch a binary built
// by an old compiler. GOTOOLCHAIN=local stops go from switching to the floor.
func TestReleaseGuardRefusesOldGo(t *testing.T) {
	out, err := lintMake(t, "check-go-version", "GO_RELEASE_MIN=1.99.0", "GOTOOLCHAIN=local")
	if err == nil {
		t.Fatalf("check-go-version accepted a Go older than the floor:\n%s", out)
	}
	if !strings.Contains(out, "1.99.0") {
		t.Fatalf("refusal does not name the required version:\n%s", out)
	}
}

func TestReleaseGuardAcceptsCurrentGo(t *testing.T) {
	if out, err := lintMake(t, "check-go-version"); err != nil {
		t.Fatalf("check-go-version refused the toolchain in use: %v\n%s", err, out)
	}
}

func TestBinariesTargetIsGuarded(t *testing.T) {
	d := newLintDirs(t)
	out, err := lintMake(t, d.args("binaries", "SKIP_UI=1", "VERSION=v0.0.1", "ALLOW_DEV_RELEASE=1", "GO_RELEASE_MIN=1.99.0", "GOTOOLCHAIN=local")...)
	if err == nil {
		t.Fatalf("make binaries built with a Go older than the floor:\n%s", out)
	}
}
