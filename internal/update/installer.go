package update

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Kind string

const (
	KindScript Kind = "script"
	KindDeb    Kind = "deb"
	KindRPM    Kind = "rpm"
	KindPkg    Kind = "pkg"
)

var ErrLayout = errors.New("unexpected install layout")

// Detect guesses how this binary was installed from receipts and package databases.
func Detect(goos, exe string, exists func(string) bool) Kind {
	switch {
	case goos == "darwin" && exists("/var/db/receipts/io.github.rcss-labs.stackstead.bom"):
		return KindPkg
	case goos == "linux" && exists("/var/lib/dpkg/info/stackstead.list"):
		return KindDeb
	case goos == "linux" && exe == "/usr/bin/stackstead" && (exists("/var/lib/rpm") || exists("/usr/lib/sysimage/rpm")):
		return KindRPM
	}
	return KindScript
}

// Executable is the absolute, symlink-free path of this binary.
func Executable() string {
	exe, err := os.Executable()
	if err != nil {
		return "stackstead"
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return exe
}

// UpgradeCommand names the binary by path: sudo's secure_path may not include its prefix.
func UpgradeCommand(kind Kind, goos, exe string) string {
	cmd := shellQuote(exe) + " upgrade"
	if kind == KindScript && goos == "darwin" {
		return cmd
	}
	return "sudo " + cmd
}

var safeShellWord = regexp.MustCompile(`^[A-Za-z0-9_./+:@%=-]+$`)

func shellQuote(s string) string {
	if safeShellWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// prefixOf maps <prefix>/bin/stackstead to <prefix>.
func prefixOf(exe string) (string, error) {
	bin := filepath.Dir(exe)
	if !filepath.IsAbs(exe) || filepath.Base(exe) != "stackstead" || filepath.Base(bin) != "bin" {
		return "", ErrLayout
	}
	return filepath.Dir(bin), nil
}
