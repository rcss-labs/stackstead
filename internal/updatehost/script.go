package updatehost

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rcss-labs/stackstead/internal/update"
	"golang.org/x/sys/unix"
)

type scriptAdapter struct {
	runner              CommandRunner
	platform            string
	owner               uint32
	anchor, stagingRoot string
	ownership           func(context.Context, string) error
	extract             func(update.VerifiedArtifact, string) error
}

func NewScript(runner CommandRunner) PackageAdapter {
	s := &scriptAdapter{runner: runner, stagingRoot: filepath.Join(update.PrivateRoot, update.StagingDirectory), extract: update.ExtractVerifiedPayload}
	s.ownership = s.refusePackageOwnership
	return s
}
func (s *scriptAdapter) registration(p update.Plan) error {
	r := p.Registration
	platform := s.platform
	if platform == "" {
		platform = "linux"
	}
	if s.runner == nil || r.Installer != update.InstallerScript || r.Identity.OS != platform || !filepath.IsAbs(r.Prefix) || filepath.Clean(r.Prefix) != r.Prefix || r.Binary != filepath.Join(r.Prefix, "bin", "stackstead") || len(r.Instances) == 0 {
		return update.ErrSetupRequired
	}
	for _, in := range r.Instances {
		if in.UID == 0 || !instanceID.MatchString(in.ID) {
			return update.ErrSetupRequired
		}
	}
	return nil
}
func (s *scriptAdapter) payload(ctx context.Context, p update.Plan, a update.VerifiedArtifact) (string, error) {
	if err := s.registration(p); err != nil {
		return "", err
	}
	if a != p.TargetArtifact && a != p.PreviousArtifact {
		return "", update.ErrUnauthorized
	}
	if s.ownership == nil || s.extract == nil {
		return "", update.ErrSetupRequired
	}
	if err := s.ownership(ctx, p.Registration.Binary); err != nil {
		return "", err
	}
	d, err := trustedDirectory(s.stagingRoot, s.owner, s.anchor)
	if err != nil {
		return "", err
	}
	defer d.Close()
	info, err := d.Stat()
	if err != nil {
		return "", err
	}
	if info.Mode().Perm()&0077 != 0 {
		return "", update.ErrSetupRequired
	}
	stage, err := os.MkdirTemp(s.stagingRoot, "script-")
	if err != nil {
		return "", err
	}
	if err = s.extract(a, stage); err != nil {
		os.RemoveAll(stage)
		return "", err
	}
	fd, err := unix.Open(filepath.Join(stage, "stackstead"), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		os.RemoveAll(stage)
		return "", update.ErrIncompatible
	}
	var st unix.Stat_t
	err = unix.Fstat(fd, &st)
	unix.Close(fd)
	if err != nil || st.Uid != s.owner || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Size <= 0 || st.Size > 512<<20 {
		os.RemoveAll(stage)
		return "", update.ErrIncompatible
	}
	return stage, nil
}
func (s *scriptAdapter) Validate(ctx context.Context, p update.Plan, a update.VerifiedArtifact) error {
	stage, err := s.payload(ctx, p, a)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	d, err := trustedDirectory(filepath.Dir(p.Registration.Binary), s.owner, s.anchor)
	if err != nil {
		return err
	}
	defer d.Close()
	return s.checkBinary(d)
}
func (s *scriptAdapter) checkBinary(d *os.File) error {
	fd, err := unix.Openat(int(d.Fd()), "stackstead", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return update.ErrSetupRequired
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != s.owner || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0022 != 0 || st.Mode&0111 == 0 {
		return update.ErrSetupRequired
	}
	return nil
}
func (s *scriptAdapter) Install(ctx context.Context, p update.Plan, a update.VerifiedArtifact) error {
	stage, err := s.payload(ctx, p, a)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	d, err := trustedDirectory(filepath.Dir(p.Registration.Binary), s.owner, s.anchor)
	if err != nil {
		return err
	}
	defer d.Close()
	if err = s.checkBinary(d); err != nil {
		return err
	}
	source, err := unix.Open(filepath.Join(stage, "stackstead"), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(source), "payload")
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(source, &st) != nil || st.Uid != s.owner || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Size <= 0 || st.Size > 512<<20 {
		return update.ErrIncompatible
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	name := ".stackstead-update-" + hex.EncodeToString(nonce[:])
	fd, err := unix.Openat(int(d.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0700)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(int(d.Fd()), name, 0)
	out := os.NewFile(uintptr(fd), name)
	_, err = io.CopyN(out, f, st.Size)
	if err == nil {
		err = out.Chmod(0755)
	}
	if err == nil {
		err = out.Sync()
	}
	err = errors.Join(err, out.Close())
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = unix.Renameat(int(d.Fd()), name, int(d.Fd()), "stackstead"); err != nil {
		return err
	}
	return d.Sync()
}
func (s *scriptAdapter) Inspect(ctx context.Context, p update.Plan) (update.Identity, error) {
	var id update.Identity
	if err := s.registration(p); err != nil {
		return id, err
	}
	return inspectExecutable(ctx, s.runner, p)
}

func inspectExecutable(ctx context.Context, runner CommandRunner, p update.Plan) (update.Identity, error) {
	return inspectPlatformExecutable(ctx, runner, p, "linux")
}

func inspectPlatformExecutable(ctx context.Context, runner CommandRunner, p update.Plan, platform string) (update.Identity, error) {
	var id update.Identity
	if len(p.Registration.Instances) == 0 || p.Registration.Instances[0].UID == 0 {
		return id, update.ErrSetupRequired
	}
	in := p.Registration.Instances[0]
	out, _, err := runner.Run(ctx, in.UID, in.GID, p.Registration.Binary, []string{"version", "-o", "json"}, nil)
	if err != nil {
		return id, err
	}
	if len(out) > commandOutputLimit {
		return id, update.ErrIncompatible
	}
	decoder := json.NewDecoder(bytes.NewReader(out))
	if decoder.Decode(&id) != nil {
		return id, update.ErrIncompatible
	}
	var trailing json.RawMessage
	if decoder.Decode(&trailing) != io.EOF || id.Version == "" || id.Commit == "" || id.OS != platform || (id.Arch != "amd64" && id.Arch != "arm64") {
		return id, update.ErrIncompatible
	}
	id.Version = strings.TrimPrefix(id.Version, "v")
	return id, nil
}
func (s *scriptAdapter) refusePackageOwnership(ctx context.Context, binary string) error {
	// Missing package tools are normal on script-only hosts. A present but broken
	// package database must fail closed; only the documented no-owner answer passes.
	tools := []struct {
		path    string
		args    []string
		noOwner func([]byte, []byte) bool
	}{
		{"/usr/bin/dpkg-query", []string{"-S", "--", binary}, func(out, stderr []byte) bool {
			return len(out) == 0 && strings.TrimSpace(string(stderr)) == "dpkg-query: no path found matching pattern "+binary
		}},
		{"/usr/bin/rpm", []string{"-qf", "--", binary}, func(out, stderr []byte) bool {
			return len(stderr) == 0 && strings.TrimSpace(string(out)) == "file "+binary+" is not owned by any package"
		}},
		{"/sbin/apk", []string{"info", "--who-owns", "--", binary}, func(out, stderr []byte) bool { return apkNoOwner(binary, out, stderr) }},
	}
	for _, tool := range tools {
		if _, err := os.Lstat(tool.path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return update.ErrSetupRequired
		}
		out, stderr, err := s.runner.Run(ctx, 0, 0, tool.path, tool.args, nil)
		if err == nil {
			return update.ErrIncompatible
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 || !tool.noOwner(out, stderr) {
			return update.ErrSetupRequired
		}
	}
	return nil
}

func apkNoOwner(binary string, out, stderr []byte) bool {
	return len(out) == 0 && strings.TrimSpace(string(stderr)) == "ERROR: "+binary+": Could not find owner package"
}
