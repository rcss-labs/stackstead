package backup

import (
	"strings"
	"time"
)

const (
	archivePrefix     = "stackstead-"
	archiveSuffix     = ".tar.gz"
	sidecarSuffix     = ".manifest.json"
	archiveTimeLayout = "20060102T150405Z"
)

// ArchiveName returns "stackstead-<slug>-<YYYYMMDDTHHMMSSZ>.tar.gz" with t in UTC.
func ArchiveName(slug string, t time.Time) string {
	return archivePrefix + slug + "-" + t.UTC().Format(archiveTimeLayout) + archiveSuffix
}

// ParseArchiveName splits an archive file name into slug and timestamp.
// The timestamp is read from the end, so the slug may contain "-".
func ParseArchiveName(name string) (slug string, t time.Time, ok bool) {
	if !strings.HasPrefix(name, archivePrefix) || !strings.HasSuffix(name, archiveSuffix) {
		return "", time.Time{}, false
	}
	core := strings.TrimSuffix(strings.TrimPrefix(name, archivePrefix), archiveSuffix)
	tsLen := len(archiveTimeLayout)
	if len(core) < tsLen+2 || core[len(core)-tsLen-1] != '-' {
		return "", time.Time{}, false
	}
	parsed, err := time.Parse(archiveTimeLayout, core[len(core)-tsLen:])
	if err != nil {
		return "", time.Time{}, false
	}
	return core[:len(core)-tsLen-1], parsed, true
}

// SidecarName returns the manifest sidecar file name for an archive.
func SidecarName(archive string) string {
	return archive + sidecarSuffix
}
