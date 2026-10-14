// Command mkrelease turns built binaries into deterministic release tarballs
// and a SHA256SUMS file. Stdlib only.
//
//	mkrelease -version <git describe> -dist <dir> -bin <dir>
//	  [-targets linux/amd64,...] [-extra [os:]name=path ...] [-allow-dev] [-print-version]
//
// Binaries are read from <bin>/stackstead-<os>-<arch>. Every requested target
// must have a binary: a missing one is an error, never a silently thinner release.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const defaultTargets = "linux/amd64,linux/arm64,darwin/amd64,darwin/arm64"

type versionInfo struct {
	Ver        string // artifact version, e.g. 1.2.3-rc.1
	PkgVer     string // deb/rpm version, e.g. 1.2.3~rc.1
	Prerelease string
	Dev        bool
}

type extra struct {
	OS   string // empty = every target
	Name string // name inside the archive
	Path string // source file
}

type options struct {
	Version  string
	Dist     string
	Bin      string
	Targets  []string
	Extras   []extra
	Epoch    int64
	AllowDev bool
}

var (
	// vX.Y.Z[-prerelease][-N-g<sha>] (after "-dirty" has been stripped).
	tagRe     = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)(?:-([0-9A-Za-z]+(?:\.[0-9A-Za-z]+)*?))?(?:-(\d+)-g([0-9a-f]+))?$`)
	bareSHARe = regexp.MustCompile(`^[0-9a-f]{4,40}$`)
	nameRe    = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	osArchRe  = regexp.MustCompile(`^[a-z0-9]+$`)
)

// normalizeVersion maps `git describe --tags --always --dirty` output to
// artifact and package versions. Anything that is not exactly a clean tag or
// a clean release candidate is a Dev version: commits past a tag, a dirty
// tree, an explicit dev prerelease, or a bare commit sha.
func normalizeVersion(in string) (versionInfo, error) {
	rest, dirty := strings.CutSuffix(in, "-dirty")
	var base, pre string
	dev := dirty
	if m := tagRe.FindStringSubmatch(rest); m != nil {
		base, pre = m[1], m[2]
		if m[3] != "" {
			dev = true
			d := "dev." + m[3] + "." + m[4]
			if pre != "" {
				pre += "." + d
			} else {
				pre = d
			}
		}
	} else if bareSHARe.MatchString(rest) {
		base, pre, dev = "0.0.0", "dev."+rest, true
	} else {
		return versionInfo{}, fmt.Errorf("cannot normalise version %q (want vX.Y.Z[-pre], git describe output or a commit sha)", in)
	}
	if pre == "dev" || strings.HasPrefix(pre, "dev.") || strings.Contains(pre, ".dev.") {
		dev = true
	}
	if dirty {
		if pre != "" {
			pre += "."
		}
		pre += "dirty"
	}
	if pre == "" {
		return versionInfo{Ver: base, PkgVer: base}, nil
	}
	return versionInfo{Ver: base + "-" + pre, PkgVer: base + "~" + pre, Prerelease: pre, Dev: dev}, nil
}

func splitTarget(t string) (goos, goarch string, err error) {
	goos, goarch, ok := strings.Cut(t, "/")
	if !ok || !osArchRe.MatchString(goos) || !osArchRe.MatchString(goarch) {
		return "", "", fmt.Errorf("bad target %q (want os/arch)", t)
	}
	return goos, goarch, nil
}

type member struct {
	name string
	path string
	mode int64
}

func build(o options) error {
	v, err := normalizeVersion(o.Version)
	if err != nil {
		return err
	}
	if v.Dev && !o.AllowDev {
		return fmt.Errorf("refusing to release dev version %s (tag the commit, or set ALLOW_DEV_RELEASE=1)", v.Ver)
	}
	if len(o.Targets) == 0 {
		return errors.New("no targets")
	}
	for _, e := range o.Extras {
		if !nameRe.MatchString(e.Name) || e.Name == "." || e.Name == ".." {
			return fmt.Errorf("bad archive entry name %q (want [A-Za-z0-9._-]+)", e.Name)
		}
		if e.OS != "" && !osArchRe.MatchString(e.OS) {
			return fmt.Errorf("bad os %q for extra %q", e.OS, e.Name)
		}
	}
	if err := checkDistVersion(o.Dist, v); err != nil {
		return err
	}
	// Validate everything before writing anything.
	type job struct {
		archive string
		members []member
	}
	var jobs []job
	for _, t := range o.Targets {
		goos, goarch, err := splitTarget(t)
		if err != nil {
			return err
		}
		binPath := filepath.Join(o.Bin, "stackstead-"+goos+"-"+goarch)
		ms := []member{{"stackstead", binPath, 0o755}}
		for _, e := range o.Extras {
			if e.OS != "" && e.OS != goos {
				continue
			}
			mode := int64(0o644)
			if e.Name == "install.sh" {
				mode = 0o755
			}
			ms = append(ms, member{e.Name, e.Path, mode})
		}
		for _, m := range ms {
			st, err := os.Stat(m.path)
			if err != nil {
				return fmt.Errorf("%s: %w", m.name, err)
			}
			if !st.Mode().IsRegular() {
				return fmt.Errorf("%s: %s is not a regular file", m.name, m.path)
			}
		}
		sort.Slice(ms, func(i, j int) bool { return ms[i].name < ms[j].name })
		for i := 1; i < len(ms); i++ {
			if ms[i].name == ms[i-1].name {
				return fmt.Errorf("duplicate archive entry %q for %s", ms[i].name, t)
			}
		}
		jobs = append(jobs, job{fmt.Sprintf("stackstead_%s_%s_%s.tar.gz", v.Ver, goos, goarch), ms})
	}
	if err := os.MkdirAll(o.Dist, 0o755); err != nil {
		return err
	}
	for _, j := range jobs {
		if err := writeArchive(filepath.Join(o.Dist, j.archive), j.members, time.Unix(o.Epoch, 0)); err != nil {
			return fmt.Errorf("%s: %w", j.archive, err)
		}
	}
	return writeSums(o.Dist)
}

// checkDistVersion refuses a dist/ that already holds release artifacts of a
// different version, which would otherwise end up in SHA256SUMS.
func checkDistVersion(dist string, v versionInfo) error {
	ents, err := os.ReadDir(dist)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range ents {
		n := e.Name()
		if !strings.HasPrefix(n, "stackstead_") && !strings.HasPrefix(n, "stackstead-") {
			continue
		}
		if !strings.Contains(n, v.Ver) && !strings.Contains(n, v.PkgVer) {
			return fmt.Errorf("%s/%s is not version %s: run make dist-clean first", dist, n, v.Ver)
		}
	}
	return nil
}

func writeArchive(path string, ms []member, mtime time.Time) (err error) {
	tmp := path + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	gz := gzip.NewWriter(out) // zero Header: no name, no mtime
	tw := tar.NewWriter(gz)
	for _, m := range ms {
		data, rerr := os.ReadFile(m.path)
		if rerr != nil {
			_ = out.Close()
			return rerr
		}
		hdr := &tar.Header{
			Name: m.name, Mode: m.mode, Size: int64(len(data)), ModTime: mtime,
			Uname: "root", Gname: "root", Typeflag: tar.TypeReg, Format: tar.FormatUSTAR,
		}
		if err = tw.WriteHeader(hdr); err != nil {
			_ = out.Close()
			return err
		}
		if _, err = tw.Write(data); err != nil {
			_ = out.Close()
			return err
		}
	}
	if err = tw.Close(); err != nil {
		_ = out.Close()
		return err
	}
	if err = gz.Close(); err != nil {
		_ = out.Close()
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// writeSums writes <dist>/SHA256SUMS over every regular file except SHA256SUMS*.
func writeSums(dist string) error {
	ents, err := os.ReadDir(dist)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range ents {
		if e.Type().IsRegular() && !strings.HasPrefix(e.Name(), "SHA256SUMS") && !strings.HasSuffix(e.Name(), ".tmp") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var sb strings.Builder
	for _, n := range names {
		f, err := os.Open(filepath.Join(dist, n))
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		_ = f.Close()
		if err != nil {
			return err
		}
		fmt.Fprintf(&sb, "%x  %s\n", h.Sum(nil), n)
	}
	return os.WriteFile(filepath.Join(dist, "SHA256SUMS"), []byte(sb.String()), 0o644)
}

type extraFlags []extra

func (e *extraFlags) String() string { return "" }
func (e *extraFlags) Set(v string) error {
	spec, path, ok := strings.Cut(v, "=")
	if !ok || spec == "" || path == "" {
		return fmt.Errorf("bad -extra %q (want [os:]name=path)", v)
	}
	var x extra
	if osName, name, hasOS := strings.Cut(spec, ":"); hasOS {
		x.OS, spec = osName, name
	}
	x.Name, x.Path = spec, path
	*e = append(*e, x)
	return nil
}

func run(args []string, getenv func(string) string, stdout io.Writer) error {
	fs := flag.NewFlagSet("mkrelease", flag.ContinueOnError)
	version := fs.String("version", "", "git describe output (required)")
	dist := fs.String("dist", "dist", "output directory")
	bin := fs.String("bin", "bin", "directory with stackstead-<os>-<arch> binaries")
	targets := fs.String("targets", defaultTargets, "comma-separated os/arch list")
	printVersion := fs.Bool("print-version", false, `print "<ver>\t<pkgver>\t<prerelease>" and exit`)
	allowDev := fs.Bool("allow-dev", false, "allow releasing a dev version")
	var extras extraFlags
	fs.Var(&extras, "extra", "extra archive member [os:]name=path (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version == "" {
		return errors.New("-version is required")
	}
	v, err := normalizeVersion(*version)
	if err != nil {
		return err
	}
	if *printVersion {
		_, err = fmt.Fprintf(stdout, "%s\t%s\t%s\n", v.Ver, v.PkgVer, v.Prerelease)
		return err
	}
	s := getenv("SOURCE_DATE_EPOCH")
	if s == "" {
		return errors.New("SOURCE_DATE_EPOCH is required for reproducible archives (the Makefile passes the commit time)")
	}
	epoch, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("bad SOURCE_DATE_EPOCH %q", s)
	}
	return build(options{
		Version: *version, Dist: *dist, Bin: *bin, Targets: strings.Split(*targets, ","),
		Extras: extras, Epoch: epoch, AllowDev: *allowDev,
	})
}

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mkrelease:", err)
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		os.Exit(1)
	}
}
