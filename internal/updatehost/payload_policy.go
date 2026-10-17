package updatehost

import (
	"github.com/rcss-labs/stackstead/internal/update"
	"strings"
)

// Native package ownership is finite: names are installer semantics, numeric
// zero IDs alone are not evidence that an nfpm entry installs as root.
func validateNativePayloadEntry(kind update.InstallerKind, e payloadEntry) error {
	if kind != update.InstallerPkg && kind != update.InstallerDeb && kind != update.InstallerRPM {
		return nil
	}
	if e.Mode < 0 || e.Mode&^0777 != 0 || e.UID != 0 || e.GID != 0 {
		return update.ErrIncompatible
	}
	mode := int64(0644)
	directory := false
	user, group := "root", "root"
	if kind == update.InstallerPkg {
		group = "wheel"
		p := e.Path
		if e.Metadata != nil {
			if e.Metadata.Path != e.Path || e.Path != metadataPath(e.Metadata.Companion) {
				return update.ErrIncompatible
			}
			p = e.Metadata.Companion
		}
		switch p {
		case "/":
			if e.Metadata != nil {
				return update.ErrIncompatible
			}
			directory = true
			mode = 0755
		case "/usr", "/usr/local", "/usr/local/bin", "/usr/local/share", "/usr/local/share/stackstead", "/Library", "/Library/PrivilegedHelperTools", "/Library/LaunchDaemons":
			directory = true
			mode = 0755
		case "/usr/local/bin/stackstead", "/Library/PrivilegedHelperTools/stackstead-updater", "/Library/PrivilegedHelperTools/stackstead-updater-bootstrap", "/usr/local/share/stackstead/install.sh":
			mode = 0755
		case "/usr/local/share/stackstead/launchagent.plist.in", "/usr/local/share/stackstead/config.example.yaml", "/usr/local/share/stackstead/LICENSE", "/Library/LaunchDaemons/io.github.rcss-labs.stackstead-updater.plist":
		default:
			return update.ErrIncompatible
		}
		if e.Metadata != nil {
			directory = false
			mode = 0644
		}
	} else {
		switch e.Path {
		case "/", "/usr", "/usr/bin", "/usr/lib", "/usr/libexec", "/usr/libexec/stackstead", "/usr/lib/systemd", "/usr/lib/systemd/system", "/usr/share", "/usr/share/doc", "/usr/share/doc/stackstead", "/usr/share/lintian", "/usr/share/lintian/overrides", "/usr/share/licenses", "/usr/share/licenses/stackstead", "/etc", "/etc/stackstead", "/var", "/var/lib":
			directory = true
			mode = 0755
		case "/usr/bin/stackstead", "/usr/libexec/stackstead/stackstead-updater", "/usr/libexec/stackstead/stackstead-updater-bootstrap":
			mode = 0755
		case "/usr/lib/systemd/system/stackstead.service":
		case "/usr/share/lintian/overrides/stackstead", "/usr/share/doc/stackstead/copyright":
			if kind != update.InstallerDeb {
				return update.ErrIncompatible
			}
		case "/usr/share/licenses/stackstead/LICENSE":
			if kind != update.InstallerRPM {
				return update.ErrIncompatible
			}
		case "/etc/stackstead/config.yaml":
			mode = 0640
			group = "stackstead"
		case "/var/lib/stackstead":
			directory = true
			mode = 0700
			user = "stackstead"
			group = "stackstead"
		default:
			return update.ErrIncompatible
		}
	}
	if e.Directory != directory || e.Mode != mode {
		return update.ErrIncompatible
	}
	matches := func(actual, want string) bool { return actual == want || (actual == "" && want != "stackstead") }
	if !matches(e.Uname, user) || !matches(e.Gname, group) || len(e.Uname) > 32 || len(e.Gname) > 32 || strings.ContainsAny(e.Uname+e.Gname, "\x00\r\n\t") {
		return update.ErrIncompatible
	}
	return nil
}
