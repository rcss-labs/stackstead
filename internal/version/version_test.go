package version

import (
	"runtime"
	"strings"
	"testing"
)

func setVars(t *testing.T, v, c, d string) {
	t.Helper()
	ov, oc, od := Version, Commit, Date
	Version, Commit, Date = v, c, d
	t.Cleanup(func() { Version, Commit, Date = ov, oc, od })
}

func TestLdflagsWin(t *testing.T) {
	setVars(t, "v1.2.3", "abc123456789", "2026-10-14T10:00:00Z")
	want := "stackstead v1.2.3 (abc123456789 2026-10-14T10:00:00Z) " + runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH + "\nLicense: AGPL-3.0"
	if got := Get().String(); got != want {
		t.Errorf("String() =\n%q\nwant\n%q", got, want)
	}
	if got := UserAgent(); got != "stackstead-cli/v1.2.3" {
		t.Errorf("UserAgent() = %q", got)
	}
}

func TestVersionFallsBackToBuildInfo(t *testing.T) {
	setVars(t, "dev", "", "")
	info := Get()
	if info.Version == "" {
		t.Error("Version is empty without ldflags")
	}
	if info.Go != runtime.Version() {
		t.Errorf("Go = %q, want %q", info.Go, runtime.Version())
	}
	if info.License != "AGPL-3.0" {
		t.Errorf("License = %q", info.License)
	}
	if strings.Contains(info.String(), "()") {
		t.Errorf("String() has an empty parenthesis: %q", info.String())
	}
}

func TestStringOmitsMissingCommitAndDate(t *testing.T) {
	setVars(t, "v1.0.0", "", "")
	first, _, _ := strings.Cut(Get().String(), "\n")
	want := "stackstead v1.0.0 " + runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH
	if first != want {
		t.Errorf("first line = %q, want %q", first, want)
	}
}

// Release commit identity must not be shortened for rollback matching.
func TestVersionPreservesFullStampedCommit(t *testing.T) {
	commit := "0123456789abcdef0123456789abcdef01234567"
	setVars(t, "v1.2.3", commit, "2026-10-17T00:00:00Z")
	if got := Get().Commit; got != commit {
		t.Errorf("Commit = %q, want %q", got, commit)
	}
}
