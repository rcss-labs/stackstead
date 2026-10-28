package backup

import (
	"strings"
	"time"
)

const (
	archivePrefix     = "stackstead-"
	archiveSuffix     = ".tar.gz"
	archiveSuffixEnc  = ".tar.gz.enc"
	sidecarSuffix     = ".manifest.json"
	archiveTimeLayout = "20060102T150405Z"
)

// ArchiveName returns "stackstead-<slug>-<YYYYMMDDTHHMMSSZ>.tar.gz" with t in
// UTC, with ".enc" appended for an encrypted archive.
func ArchiveName(slug string, t time.Time, encrypted bool) string {
	suffix := archiveSuffix
	if encrypted {
		suffix = archiveSuffixEnc
	}
	return archivePrefix + slug + "-" + t.UTC().Format(archiveTimeLayout) + suffix
}

// ParseArchiveName splits an archive file name into slug and timestamp.
// The timestamp is read from the end, so the slug may contain "-".
func ParseArchiveName(name string) (slug string, t time.Time, ok bool) {
	suffix := archiveSuffix
	if IsEncryptedName(name) {
		suffix = archiveSuffixEnc
	}
	if !strings.HasPrefix(name, archivePrefix) || !strings.HasSuffix(name, suffix) {
		return "", time.Time{}, false
	}
	core := strings.TrimSuffix(strings.TrimPrefix(name, archivePrefix), suffix)
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

// IsEncryptedName reports whether an archive name carries the encrypted suffix.
// It is for listing only; opening an archive sniffs the content.
func IsEncryptedName(name string) bool { return strings.HasSuffix(name, archiveSuffixEnc) }
