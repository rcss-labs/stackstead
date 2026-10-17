package updatehost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rcss-labs/stackstead/internal/config"
	"github.com/rcss-labs/stackstead/internal/dbmaint"
	"github.com/rcss-labs/stackstead/internal/lockfile"
	"github.com/rcss-labs/stackstead/internal/update"
	"golang.org/x/sys/unix"
)

// All functions in this file run only in the authenticated credential-dropped
// child. The parent never opens these paths or receives config/database bytes
// through metadata.
func childDataDirectory(in update.Instance) (*os.File, error) {
	if os.Geteuid() == 0 || uint32(os.Geteuid()) != in.UID || uint32(os.Getegid()) != in.GID {
		return nil, update.ErrUnauthorized
	}
	f, err := openChildPath(in.DataDir, true)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if unix.Fstat(int(f.Fd()), &st) != nil || st.Uid != in.UID || st.Mode&0022 != 0 {
		f.Close()
		return nil, update.ErrUnauthorized
	}
	for _, name := range []string{"stackstead.db", "stackstead.db-wal", "stackstead.db-shm", "stackstead.lock"} {
		var leaf unix.Stat_t
		err := unix.Fstatat(int(f.Fd()), name, &leaf, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) && name != "stackstead.db" {
			continue
		}
		if err != nil || leaf.Mode&unix.S_IFMT != unix.S_IFREG || leaf.Uid != in.UID || leaf.Nlink != 1 {
			f.Close()
			return nil, update.ErrUnauthorized
		}
	}
	return f, nil
}
func openChildPath(path string, directory bool) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, update.ErrUnauthorized
	}
	// The fixed OS alias is explicit. Other symlinks remain refused.
	if strings.HasPrefix(path, "/var/") {
		if target, err := os.Readlink("/var"); err == nil && target == "private/var" {
			path = "/private" + path
		}
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), "/")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		if i < len(parts)-1 || directory {
			flags |= unix.O_DIRECTORY
		}
		next, err := unix.Openat(int(current.Fd()), part, flags, 0)
		current.Close()
		if err != nil {
			return nil, err
		}
		current = os.NewFile(uintptr(next), part)
	}
	return current, nil
}
func childConfigFingerprint(in update.Instance) (string, error) {
	f, err := openChildPath(in.ConfigPath, false)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(int(f.Fd()), &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Size < 0 || st.Size > childConfigLimit {
		return "", update.ErrIncompatible
	}
	raw, err := io.ReadAll(io.LimitReader(f, childConfigLimit+1))
	if err != nil || int64(len(raw)) > childConfigLimit {
		return "", update.ErrIncompatible
	}
	// config.LoadWithFingerprint parses the bounded exact source bytes, with no
	// application environment overrides. Equality with app effective config is a
	// separate startup/preflight proof; a file-only digest cannot grant it.
	temp, err := os.CreateTemp(in.DataDir, ".updater-config-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(raw); err != nil {
		temp.Close()
		return "", err
	}
	if err = temp.Close(); err != nil {
		return "", err
	}
	cfg, digest, err := config.LoadWithFingerprint(temp.Name(), func(string) string { return "" })
	if err != nil {
		return "", err
	}
	if cfg.DataDir != in.DataDir {
		return "", update.ErrIncompatible
	}
	return digest, nil
}
func inspectChildDB(ctx context.Context, in update.Instance) (dbInspection, error) {
	var proof dbInspection
	dir, err := childDataDirectory(in)
	if err != nil {
		return proof, err
	}
	defer dir.Close()
	proof.ConfigSHA256, err = childConfigFingerprint(in)
	if err != nil {
		return proof, err
	}
	proof.Schema, _, err = dbmaint.VerifyDatabaseFile(ctx, filepath.Join(in.DataDir, "stackstead.db"))
	if err != nil {
		return proof, err
	}
	sizes := []*int64{&proof.DBBytes, &proof.WALBytes, &proof.SHMBytes}
	for i, name := range []string{"stackstead.db", "stackstead.db-wal", "stackstead.db-shm"} {
		var st unix.Stat_t
		e := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(e, unix.ENOENT) && i > 0 {
			continue
		}
		if e != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != in.UID || st.Size < 0 {
			return dbInspection{}, update.ErrIncompatible
		}
		*sizes[i] = st.Size
	}
	var fs unix.Statfs_t
	if err = unix.Fstatfs(int(dir.Fd()), &fs); err != nil {
		return dbInspection{}, err
	}
	if fs.Bsize <= 0 || uint64(fs.Bavail) > ^uint64(0)/uint64(fs.Bsize) {
		return dbInspection{}, update.ErrIncompatible
	}
	proof.FreeBytes = uint64(fs.Bavail) * uint64(fs.Bsize)
	proof.UID = uint32(os.Geteuid())
	proof.GID = uint32(os.Getegid())
	return proof, nil
}
func snapshotChildDB(ctx context.Context, in update.Instance, limit int64, output *os.File) (snapshotProof, error) {
	var proof snapshotProof
	dir, err := childDataDirectory(in)
	if err != nil {
		return proof, err
	}
	defer dir.Close()
	proof.ConfigSHA256, err = childConfigFingerprint(in)
	if err != nil {
		return proof, err
	}
	temp, err := os.MkdirTemp(in.DataDir, ".updater-snapshot-*")
	if err != nil {
		return proof, err
	}
	defer os.RemoveAll(temp)
	path := filepath.Join(temp, "snapshot.db")
	proof.Schema, proof.SHA256, err = dbmaint.Snapshot(ctx, in.DataDir, path)
	if err != nil {
		return proof, err
	}
	f, err := os.Open(path)
	if err != nil {
		return proof, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return proof, err
	}
	proof.Size = st.Size()
	proof.Budget = limit
	if proof.Size <= 0 || proof.Size > limit {
		return snapshotProof{}, update.ErrIncompatible
	}
	if _, err = io.CopyN(output, f, proof.Size); err != nil {
		return snapshotProof{}, err
	}
	return proof, nil
}

type childRestoreMarker struct {
	Protocol        int           `json:"protocol"`
	JobID           string        `json:"jobID"`
	InstanceID      string        `json:"instanceID"`
	Expected        snapshotProof `json:"expected"`
	Phase           string        `json:"phase"`
	InstalledSHA256 string        `json:"installedSHA256,omitempty"`
	ArchiveSHA256   string        `json:"archiveSHA256,omitempty"`
}

func restoreChildDB(ctx context.Context, jobID string, in update.Instance, expected snapshotProof, input *os.File) error {
	dir, err := childDataDirectory(in)
	if err != nil {
		return err
	}
	defer dir.Close()
	cfg, err := childConfigFingerprint(in)
	if err != nil {
		return err
	}
	if cfg != expected.ConfigSHA256 {
		return update.ErrRecoveryRequired
	}
	sum := sha256.Sum256([]byte(jobID + "\x00" + in.ID))
	stateDir := filepath.Join(in.DataDir, ".updater-restore-"+hex.EncodeToString(sum[:16]))
	markerPath := filepath.Join(stateDir, "marker.json")
	// Existing state is never repaired or removed automatically. A prepared marker
	// means a swap might have occurred. Only a fully verified completed marker is
	// idempotent; changed bytes/sidecars fail closed without calling Restore again.
	if _, err = os.Lstat(stateDir); err == nil {
		lock, err := lockfile.Acquire(filepath.Join(in.DataDir, "stackstead.lock"))
		if err != nil {
			return err
		}
		defer lock.Release()
		marker, err := readChildRestoreMarker(markerPath)
		if err != nil {
			return err
		}
		if marker.Protocol != 1 || marker.JobID != jobID || marker.InstanceID != in.ID || marker.Expected != expected || marker.Phase != "complete" {
			return update.ErrRecoveryRequired
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if _, err = os.Lstat(filepath.Join(in.DataDir, "stackstead.db") + suffix); !errors.Is(err, os.ErrNotExist) {
				return update.ErrRecoveryRequired
			}
		}
		digest, _, err := hashChildFile(filepath.Join(in.DataDir, "stackstead.db"), expected.Size+1)
		if err != nil || digest != marker.InstalledSHA256 {
			return update.ErrRecoveryRequired
		}
		archive, err := hashChildArchive(filepath.Join(stateDir, "failed"), expected.Budget)
		if err != nil || archive != marker.ArchiveSHA256 {
			return update.ErrRecoveryRequired
		}
		schema, _, err := dbmaint.VerifyDatabaseFile(ctx, filepath.Join(in.DataDir, "stackstead.db"))
		if err != nil || schema != expected.Schema {
			return update.ErrRecoveryRequired
		}
		// Drain and reverify the root-bound stream even on a completed retry.
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(input, expected.Size+1))
		if err != nil || n != expected.Size || hex.EncodeToString(h.Sum(nil)) != expected.SHA256 {
			return update.ErrRecoveryRequired
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return update.ErrRecoveryRequired
	}
	if err = childDBFootprint(dir, expected.Budget); err != nil {
		return err
	}
	temp, err := os.CreateTemp(in.DataDir, ".updater-restore-input-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(temp, h), io.LimitReader(input, expected.Size+1))
	if err != nil || n != expected.Size || hex.EncodeToString(h.Sum(nil)) != expected.SHA256 {
		temp.Close()
		return update.ErrIncompatible
	}
	if err = temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	schema, size, err := dbmaint.VerifyDatabaseFile(ctx, temp.Name())
	if err != nil || schema != expected.Schema || size != expected.Size {
		return update.ErrIncompatible
	}
	if err = os.Mkdir(stateDir, 0700); err != nil {
		return err
	}
	if err = dbmaint.SyncDir(in.DataDir); err != nil {
		return err
	}
	marker := childRestoreMarker{Protocol: 1, JobID: jobID, InstanceID: in.ID, Expected: expected, Phase: "prepared"}
	if err = writeChildRestoreMarker(markerPath, marker, false); err != nil {
		return err
	}
	if err = childDBFootprint(dir, expected.Budget); err != nil {
		return err
	}
	if err = dbmaint.Restore(ctx, in.DataDir, temp.Name(), filepath.Join(stateDir, "failed")); err != nil {
		return err
	}
	// Lock once more before publishing completion; never claim a proof if a server
	// was admitted concurrently. Coordinator forbids restore after old startup.
	lock, err := lockfile.Acquire(filepath.Join(in.DataDir, "stackstead.lock"))
	if err != nil {
		return err
	}
	defer lock.Release()
	marker.InstalledSHA256, _, err = hashChildFile(filepath.Join(in.DataDir, "stackstead.db"), expected.Size+1)
	if err != nil {
		return err
	}
	marker.ArchiveSHA256, err = hashChildArchive(filepath.Join(stateDir, "failed"), expected.Budget)
	if err != nil {
		return err
	}
	marker.Phase = "complete"
	return writeChildRestoreMarker(markerPath, marker, true)
}
func hashChildFile(path string, limit int64) (string, int64, error) {
	f, err := openChildPath(path, false)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(int(f.Fd()), &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Geteuid()) || st.Size < 0 || st.Size > limit {
		return "", 0, update.ErrRecoveryRequired
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, limit+1))
	if err != nil || n > limit {
		return "", 0, update.ErrRecoveryRequired
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
func childDBFootprint(dir *os.File, budget int64) error {
	if budget <= 0 {
		return update.ErrRecoveryRequired
	}
	remaining := budget
	for _, name := range []string{"stackstead.db", "stackstead.db-wal", "stackstead.db-shm"} {
		var st unix.Stat_t
		err := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) && name != "stackstead.db" {
			continue
		}
		if err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Geteuid()) || st.Size < 0 || st.Size > remaining {
			return update.ErrRecoveryRequired
		}
		remaining -= st.Size
	}
	return nil
}
func hashChildArchive(path string, budget int64) (string, error) {
	d, err := openChildPath(path, true)
	if err != nil {
		return "", err
	}
	defer d.Close()
	if err = childDBFootprint(d, budget); err != nil {
		return "", err
	}
	entries, err := d.ReadDir(4)
	if err != nil && err != io.EOF {
		return "", err
	}
	if len(entries) == 0 || len(entries) > 3 {
		return "", update.ErrRecoveryRequired
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	h := sha256.New()
	remaining := budget
	for _, entry := range entries {
		name := entry.Name()
		if name != "stackstead.db" && name != "stackstead.db-wal" && name != "stackstead.db-shm" {
			return "", update.ErrRecoveryRequired
		}
		digest, n, err := hashChildFile(filepath.Join(path, name), remaining)
		if err != nil {
			return "", err
		}
		remaining -= n
		fmt.Fprintf(h, "%s\x00%d\x00%s\x00", name, n, digest)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func readChildRestoreMarker(path string) (childRestoreMarker, error) {
	var marker childRestoreMarker
	f, err := openChildPath(path, false)
	if err != nil {
		return marker, err
	}
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(int(f.Fd()), &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Geteuid()) || st.Mode&0777 != 0600 || st.Size > childMetadataLimit {
		return marker, update.ErrRecoveryRequired
	}
	dec := json.NewDecoder(io.LimitReader(f, childMetadataLimit+1))
	dec.DisallowUnknownFields()
	if dec.Decode(&marker) != nil || dec.Decode(new(json.RawMessage)) != io.EOF {
		return marker, update.ErrRecoveryRequired
	}
	return marker, nil
}
func writeChildRestoreMarker(path string, marker childRestoreMarker, replace bool) error {
	b, err := json.Marshal(marker)
	if err != nil || len(b) > childMetadataLimit {
		return update.ErrRecoveryRequired
	}
	if !replace {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, err = f.Write(b)
		if err == nil {
			err = f.Sync()
		}
		err = errors.Join(err, f.Close())
		if err != nil {
			return err
		}
		return dbmaint.SyncDir(filepath.Dir(path))
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".marker-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return dbmaint.SyncDir(filepath.Dir(path))
}
