// Package version carries the build identity of the binary: set by
// -ldflags -X in release builds, recovered from the Go build info otherwise.
package version

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// Set by -ldflags "-X github.com/rcss-labs/stackstead/internal/version.Version=…".
var (
	Version = "dev"
	Commit  string
	Date    string
)

// License is stated by the binary so a shipped copy carries its terms.
const License = "AGPL-3.0"

// Info is the build identity, also the JSON shape of `stackstead version -o json`.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	Go      string `json:"go"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	License string `json:"license"`
}

// Get returns the build identity. ldflags values win; whatever they leave
// unset is filled from the VCS data the Go toolchain embeds.
func Get() Info {
	info := Info{
		Version: Version, Commit: Commit, Date: Date,
		Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH, License: License,
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	var revision string
	var modified bool
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.time":
			if info.Date == "" {
				info.Date = s.Value
			}
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if info.Commit == "" {
		info.Commit = revision
		if len(info.Commit) > 12 {
			info.Commit = info.Commit[:12]
		}
	}
	if info.Version == "dev" {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			info.Version = v
		} else if modified && revision != "" {
			info.Version = "dev-dirty"
		}
	}
	return info
}

// String renders the two-line `stackstead version` output.
func (i Info) String() string {
	line := "stackstead " + i.Version
	if rev := strings.TrimSpace(i.Commit + " " + i.Date); rev != "" {
		line += " (" + rev + ")"
	}
	return line + " " + i.Go + " " + i.OS + "/" + i.Arch + "\nLicense: " + i.License
}

// UserAgent is sent by the CLI's HTTP client.
func UserAgent() string { return "stackstead-cli/" + Get().Version }
