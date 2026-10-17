package updatehost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"github.com/rcss-labs/stackstead/internal/update"
	"golang.org/x/sys/unix"
)

// prefixWriter exactly implements darwinPrefixWriter. The caller supplies only
// the already verified payload and proven directory descriptors; root performs
// no writes through a user's prefix and the child receives no destination path.
func (e *childExecutor) prefixWriter(ctx context.Context, uid, gid uint32, source, dir *os.File, size int64) error {
	_, err := e.execute(ctx, childRequest{Protocol: 1, Role: childWritePrefix, JobID: "prefix-writer", Instance: update.Instance{ID: "prefix-writer", UID: uid, GID: gid}, Limit: size}, source, nil, dir)
	return err
}
func writeChildPrefix(ctx context.Context, in update.Instance, size int64, source, dir *os.File) error {
	if source == nil || dir == nil || size <= 0 || size > childArtifactLimit || os.Geteuid() == 0 || uint32(os.Geteuid()) != in.UID || uint32(os.Getegid()) != in.GID {
		return update.ErrUnauthorized
	}
	var destination, payload unix.Stat_t
	if unix.Fstat(int(dir.Fd()), &destination) != nil || destination.Mode&unix.S_IFMT != unix.S_IFDIR || destination.Uid != in.UID || destination.Mode&0200 == 0 {
		return update.ErrUnauthorized
	}
	if unix.Fstat(int(source.Fd()), &payload) != nil || payload.Mode&unix.S_IFMT != unix.S_IFREG || payload.Size != size {
		return update.ErrIncompatible
	}
	original, err := writerLeaf(dir)
	if err != nil {
		return err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	name := ".stackstead-update-" + hex.EncodeToString(random[:])
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0700)
	if err != nil {
		return err
	}
	temp := os.NewFile(uintptr(fd), name)
	defer unix.Unlinkat(int(dir.Fd()), name, 0)
	if _, err = source.Seek(0, io.SeekStart); err == nil {
		err = copyChildBounded(temp, source, size)
	}
	if err == nil {
		err = temp.Chmod(0755)
	}
	if err == nil {
		err = temp.Sync()
	}
	err = errors.Join(err, temp.Close())
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	latest, err := writerLeaf(dir)
	if err != nil || latest.Dev != original.Dev || latest.Ino != original.Ino || latest.Uid != original.Uid || latest.Mode != original.Mode {
		return update.ErrRecoveryRequired
	}
	if err = unix.Renameat(int(dir.Fd()), name, int(dir.Fd()), "stackstead"); err != nil {
		return err
	}
	return dir.Sync()
}
func writerLeaf(dir *os.File) (unix.Stat_t, error) {
	var st unix.Stat_t
	if unix.Fstatat(int(dir.Fd()), "stackstead", &st, unix.AT_SYMLINK_NOFOLLOW) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0111 == 0 {
		return st, update.ErrSetupRequired
	}
	return st, nil
}
