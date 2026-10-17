package updatehost

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/rcss-labs/stackstead/internal/update"
	"golang.org/x/sys/unix"
)

// The fixed-purpose writer receives handles, never an executable, destination
// path or basename. Task 13 must supply a trusted child that drops credentials
// and groups before accessing the destination; prefix code is never root code.
type darwinPrefixWriter func(context.Context, uint32, uint32, *os.File, *os.File, int64) error
type darwinScriptAdapter struct {
	runner   CommandRunner
	core     *scriptAdapter
	writer   darwinPrefixWriter
	aclProof func(*os.File) error
}

func NewDarwinScript(r CommandRunner) PackageAdapter {
	core := NewScript(r).(*scriptAdapter)
	core.platform = "darwin"
	s := &darwinScriptAdapter{runner: r, core: core, aclProof: darwinACLProof}
	core.ownership = s.refusePackageOwnership
	return s
}
func (s *darwinScriptAdapter) refusePackageOwnership(ctx context.Context, binary string) error {
	out, stderr, e := s.runner.Run(ctx, 0, 0, "/usr/sbin/pkgutil", []string{"--file-info", binary}, nil)
	if e != nil || len(stderr) != 0 || len(out) > commandOutputLimit {
		return update.ErrSetupRequired
	}
	if strings.Contains(string(out), "pkgid: ") {
		return update.ErrIncompatible
	}
	if string(out) != "volume: /\npath: "+binary+"\n" {
		return update.ErrSetupRequired
	}
	return nil
}

// destination allows root writes only through the existing strict trust walk.
// A writable chain is opened without following links, then handed exclusively to
// a registered owner of the final directory. Root never mutates that directory.
func (s *darwinScriptAdapter) destination(p update.Plan) (*os.File, *update.Instance, error) {
	path := filepath.Dir(p.Registration.Binary)
	if d, e := trustedDirectoryWithProof(path, s.core.owner, s.core.anchor, s.aclProof); e == nil {
		return d, nil, nil
	}
	start, relative := "/", strings.TrimPrefix(path, "/")
	if s.core.anchor != "" {
		var e error
		relative, e = filepath.Rel(s.core.anchor, path)
		if e != nil || relative == ".." || strings.HasPrefix(relative, "../") {
			return nil, nil, update.ErrSetupRequired
		}
		start = s.core.anchor
	}
	fd, e := unix.Open(start, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, nil, update.ErrSetupRequired
	}
	current := os.NewFile(uintptr(fd), start)
	for _, part := range strings.Split(relative, "/") {
		if part == "" || part == "." {
			continue
		}
		next, err := unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		current.Close()
		if err != nil {
			return nil, nil, update.ErrSetupRequired
		}
		current = os.NewFile(uintptr(next), part)
	}
	var st unix.Stat_t
	if unix.Fstat(int(current.Fd()), &st) != nil {
		current.Close()
		return nil, nil, update.ErrSetupRequired
	}
	var writer *update.Instance
	for _, in := range p.Registration.Instances {
		if in.UID != 0 && in.UID == st.Uid && st.Mode&0200 != 0 {
			if writer != nil && (writer.UID != in.UID || writer.GID != in.GID) {
				current.Close()
				return nil, nil, update.ErrSetupRequired
			}
			copy := in
			writer = &copy
		}
	}
	if writer == nil {
		current.Close()
		return nil, nil, update.ErrSetupRequired
	}
	return current, writer, nil
}
func (s *darwinScriptAdapter) Validate(ctx context.Context, p update.Plan, a update.VerifiedArtifact) error {
	stage, e := s.core.payload(ctx, p, a)
	if e != nil {
		return e
	}
	defer os.RemoveAll(stage)
	d, writer, e := s.destination(p)
	if e != nil {
		return e
	}
	defer d.Close()
	if writer != nil {
		if s.writer == nil {
			return update.ErrUnavailable
		}
		return darwinExistingBinary(d)
	}
	return s.core.checkBinary(d)
}
func darwinExistingBinary(d *os.File) error {
	fd, e := unix.Openat(int(d.Fd()), "stackstead", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return update.ErrSetupRequired
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0111 == 0 {
		return update.ErrSetupRequired
	}
	return nil
}
func (s *darwinScriptAdapter) Install(ctx context.Context, p update.Plan, a update.VerifiedArtifact) error {
	if e := s.core.registration(p); e != nil {
		return e
	}
	d, writer, e := s.destination(p)
	if e != nil {
		return e
	}
	defer d.Close()
	if writer == nil {
		return s.core.Install(ctx, p, a)
	}
	if s.writer == nil {
		return update.ErrUnavailable
	}
	stage, e := s.core.payload(ctx, p, a)
	if e != nil {
		return e
	}
	defer os.RemoveAll(stage)
	if e = darwinExistingBinary(d); e != nil {
		return e
	}
	fd, e := unix.Open(filepath.Join(stage, "stackstead"), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return e
	}
	source := os.NewFile(uintptr(fd), "verified-payload")
	defer source.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != s.core.owner || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Size <= 0 || st.Size > 512<<20 {
		return update.ErrIncompatible
	}
	return s.writer(ctx, writer.UID, writer.GID, source, d, st.Size)
}
func (s *darwinScriptAdapter) Inspect(ctx context.Context, p update.Plan) (update.Identity, error) {
	if e := s.core.registration(p); e != nil {
		return update.Identity{}, e
	}
	return inspectPlatformExecutable(ctx, s.runner, p, "darwin")
}
