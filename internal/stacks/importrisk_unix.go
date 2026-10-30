//go:build linux || darwin

package stacks

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

// defaultWriteTrust trusts root, the service's own user and groups, and the
// docker group when the host has one.
func defaultWriteTrust() writeTrust {
	t := writeTrust{
		uids: map[uint32]bool{0: true, uint32(os.Getuid()): true, uint32(os.Geteuid()): true},
		gids: map[uint32]bool{0: true, uint32(os.Getgid()): true, uint32(os.Getegid()): true},
	}
	if g, err := user.LookupGroup("docker"); err == nil {
		if gid, err := strconv.ParseUint(g.Gid, 10, 32); err == nil {
			t.gids[uint32(gid)] = true
		}
	}
	return t
}

// untrustedWriters checks the compose file and every folder above it. An entry
// is flagged when
//   - it is owned by a user outside the trust set (an owner can always chmod
//     and write), or
//   - it is world-writable, or group-writable by a group outside the trust set.
//
// A sticky folder only lets users remove or rename their own entries, so for
// a folder above the file's own the write bits are ignored: the entry below it
// is checked by its owner instead, which makes /tmp itself harmless while a
// file or folder someone else owns inside it is flagged. The file's own folder
// is held to the full rule even when sticky, because anyone who can write there
// can still add files compose reads next to it, such as .env.
func untrustedWriters(composePath string, trust writeTrust) []string {
	var out []string
	for path := composePath; ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err != nil {
			break
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			mode := info.Mode()
			sticky := mode.IsDir() && mode&os.ModeSticky != 0
			ignoreWriteBits := sticky && path != filepath.Dir(composePath)
			switch {
			case !trust.uids[uint32(st.Uid)]:
				out = append(out, fmt.Sprintf("%s is owned by uid %d", path, st.Uid))
			case ignoreWriteBits:
			case mode.Perm()&0o002 != 0:
				out = append(out, fmt.Sprintf("%s is writable by everyone (mode %04o)", path, mode.Perm()))
			case mode.Perm()&0o020 != 0 && !trust.gids[uint32(st.Gid)]:
				out = append(out, fmt.Sprintf("%s is writable by group %d (mode %04o)", path, st.Gid, mode.Perm()))
			}
		}
		if parent := filepath.Dir(path); parent == path {
			break
		}
	}
	return out
}
