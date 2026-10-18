package update

import (
	"errors"
	"testing"
)

const (
	pkgReceipt = "/var/db/receipts/io.github.rcss-labs.stackstead.bom"
	dpkgList   = "/var/lib/dpkg/info/stackstead.list"
)

func existing(paths ...string) func(string) bool {
	return func(p string) bool {
		for _, q := range paths {
			if p == q {
				return true
			}
		}
		return false
	}
}

func TestDetect(t *testing.T) {
	for _, c := range []struct {
		name, goos, exe string
		exists          []string
		want            Kind
	}{
		{"darwin receipt", "darwin", "/usr/local/bin/stackstead", []string{pkgReceipt}, KindPkg},
		{"darwin no receipt", "darwin", "/usr/local/bin/stackstead", nil, KindScript},
		{"linux dpkg", "linux", "/usr/bin/stackstead", []string{dpkgList}, KindDeb},
		{"linux rpm var/lib", "linux", "/usr/bin/stackstead", []string{"/var/lib/rpm"}, KindRPM},
		{"linux rpm sysimage", "linux", "/usr/bin/stackstead", []string{"/usr/lib/sysimage/rpm"}, KindRPM},
		{"linux script with rpm db", "linux", "/usr/local/bin/stackstead", []string{"/var/lib/rpm"}, KindScript},
		{"dpkg wins over rpm db", "linux", "/usr/bin/stackstead", []string{dpkgList, "/var/lib/rpm"}, KindDeb},
		{"linux nothing", "linux", "/usr/local/bin/stackstead", nil, KindScript},
		{"receipt ignored on linux", "linux", "/usr/local/bin/stackstead", []string{pkgReceipt}, KindScript},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Detect(c.goos, c.exe, existing(c.exists...)); got != c.want {
				t.Errorf("Detect = %q, want %q", got, c.want)
			}
		})
	}
}

func TestUpgradeCommand(t *testing.T) {
	for _, c := range []struct {
		kind      Kind
		goos, exe string
		want      string
	}{
		{KindScript, "linux", "/usr/local/bin/stackstead", "sudo /usr/local/bin/stackstead upgrade"},
		{KindDeb, "linux", "/usr/bin/stackstead", "sudo /usr/bin/stackstead upgrade"},
		{KindRPM, "linux", "/usr/bin/stackstead", "sudo /usr/bin/stackstead upgrade"},
		{KindPkg, "darwin", "/usr/local/bin/stackstead", "sudo /usr/local/bin/stackstead upgrade"},
		{KindScript, "darwin", "/usr/local/bin/stackstead", "/usr/local/bin/stackstead upgrade"},
	} {
		if got := UpgradeCommand(c.kind, c.goos, c.exe); got != c.want {
			t.Errorf("UpgradeCommand(%s, %s, %s) = %q, want %q", c.kind, c.goos, c.exe, got, c.want)
		}
	}
}

func TestUpgradeCommandQuotesOddPaths(t *testing.T) {
	for exe, want := range map[string]string{
		"/opt/my tools/bin/stackstead": "sudo '/opt/my tools/bin/stackstead' upgrade",
		"/opt/it's/bin/stackstead":     `sudo '/opt/it'\''s/bin/stackstead' upgrade`,
		"/opt/a;rm -rf/stackstead":     "sudo '/opt/a;rm -rf/stackstead' upgrade",
		"/opt/$HOME/stackstead":        "sudo '/opt/$HOME/stackstead' upgrade",
	} {
		if got := UpgradeCommand(KindScript, "linux", exe); got != want {
			t.Errorf("exe %q: got %q, want %q", exe, got, want)
		}
	}
}

func TestPrefixOf(t *testing.T) {
	if got, err := prefixOf("/usr/local/bin/stackstead"); err != nil || got != "/usr/local" {
		t.Errorf("got %q, %v; want /usr/local", got, err)
	}
	if got, err := prefixOf("/home/me/.local/bin/stackstead"); err != nil || got != "/home/me/.local" {
		t.Errorf("got %q, %v; want /home/me/.local", got, err)
	}
	for _, bad := range []string{"/opt/stackstead", "/usr/local/sbin/stackstead", "/usr/local/bin/other", "stackstead", ""} {
		if _, err := prefixOf(bad); !errors.Is(err, ErrLayout) {
			t.Errorf("prefixOf(%q) err = %v, want ErrLayout", bad, err)
		}
	}
}
