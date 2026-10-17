package updatehost

import (
	"context"
	"errors"
	"github.com/rcss-labs/stackstead/internal/update"
	"golang.org/x/sys/unix"
	"os"
	"strings"
)

func pkgPayloadDirectory(p string) bool {
	switch p {
	case "/usr", "/usr/local", "/usr/local/bin", "/usr/local/share", "/usr/local/share/stackstead", "/Library", "/Library/PrivilegedHelperTools", "/Library/LaunchDaemons":
		return true
	}
	return false
}
func provePKGDestinations(ctx context.Context, proof pkgPayloadProof) error {
	root, e := os.Open("/")
	if e != nil {
		return e
	}
	defer root.Close()
	return provePKGDestinationAt(ctx, root, proof, 0, platformDescriptorTrustFD)
}

// Test-owned descriptor roots permit exercising the same walk without host changes.
// Production always anchors at / and requires UID0 and Darwin ACL evidence.
func provePKGDestinationAt(ctx context.Context, root *os.File, proof pkgPayloadProof, owner uint32, acl func(int) error) error {
	check := func(fd int, directory bool) error {
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || st.Uid != owner || st.Mode&0022 != 0 || st.Mode&06000 != 0 {
			return update.ErrSetupRequired
		}
		kind := uint32(unix.S_IFREG)
		if directory {
			kind = unix.S_IFDIR
		}
		if uint32(st.Mode)&unix.S_IFMT != kind || (!directory && st.Nlink != 1) {
			return update.ErrSetupRequired
		}
		return acl(fd)
	}
	if root == nil || check(int(root.Fd()), true) != nil {
		return update.ErrSetupRequired
	}
	metadata := map[string]bool{}
	paths := map[string]bool{}
	for _, p := range proof.PayloadPaths {
		paths[p] = true
	}
	for _, m := range proof.Metadata {
		if m.Size != 163 || m.Attribute != "com.apple.provenance" || !pkgPayloadDestination(m.Companion) || m.Path != metadataPath(m.Companion) || !paths[m.Companion] || !paths[m.Path] || metadata[m.Path] {
			return update.ErrIncompatible
		}
		metadata[m.Path] = true
	}
	for _, p := range proof.PayloadPaths {
		if e := ctx.Err(); e != nil {
			return e
		}
		if !pkgPayloadDestination(p) && !metadata[p] {
			return update.ErrIncompatible
		}
		fd, e := unix.Dup(int(root.Fd()))
		if e != nil {
			return e
		}
		parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
		missing := false
		for i, part := range parts {
			directory := i < len(parts)-1 || pkgPayloadDirectory(p)
			// Stable root-only ancestry remains stable against unprivileged replacement.
			// Missing descendants are permitted only after an already proven ancestor.
			if missing {
				continue
			}
			flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
			if directory {
				flags |= unix.O_DIRECTORY
			}
			next, err := unix.Openat(fd, part, flags, 0)
			if errors.Is(err, unix.ENOENT) {
				missing = true
				continue
			}
			if err != nil {
				unix.Close(fd)
				return update.ErrSetupRequired
			}
			if err = check(next, directory); err != nil {
				unix.Close(next)
				unix.Close(fd)
				return err
			}
			unix.Close(fd)
			fd = next
		}
		unix.Close(fd)
	}
	return nil
}
func pkgProofFromInspection(p payloadInspection) pkgPayloadProof {
	cv := func(b executableInspection) pkgExecutableProof {
		v := pkgExecutableProof{Identity: b.Identity, Architectures: [2]string{"amd64", "arm64"}}
		if len(b.Slices) == 2 {
			copy(v.Slices[:], b.Slices)
		}
		return v
	}
	paths := make([]string, len(p.Entries))
	for i, e := range p.Entries {
		paths[i] = e.Path
	}
	metadata := []payloadMetadataProof{}
	for _, e := range p.Entries {
		if e.Metadata != nil {
			metadata = append(metadata, *e.Metadata)
		}
	}
	return pkgPayloadProof{Metadata: metadata, Artifact: p.Artifact, Identifier: p.Identifier, Version: p.Version, InstallLocation: p.InstallLocation, App: cv(p.App), Helper: cv(p.Helper), Bootstrap: cv(p.Bootstrap), PayloadPaths: paths}
}
