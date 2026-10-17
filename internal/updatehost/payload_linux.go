package updatehost

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"github.com/rcss-labs/stackstead/internal/update"
	"io"
	"os"
	"strconv"
	"strings"
)

func inspectNativePayload(ctx context.Context, req payloadRequest, f *os.File) (payloadInspection, error) {
	var proof payloadInspection
	switch req.Installer {
	case update.InstallerDeb:
		err := convertPayloadStream(ctx, f, "/usr/bin/dpkg-deb", []string{"--fsys-tarfile", "/dev/fd/3"}, 2<<30, func(r io.Reader) error {
			var e error
			proof, e = inventoryTar(ctx, r, req, defaultPayloadBounds, true)
			return e
		})
		return proof, err
	case update.InstallerRPM:
		err := convertPayloadStream(ctx, f, "/usr/bin/rpm2cpio", []string{"/dev/fd/3"}, 2<<30, func(r io.Reader) error {
			pr, pw := io.Pipe()
			done := make(chan error, 1)
			go func() { e := newcToTar(r, pw); pw.CloseWithError(e); done <- e }()
			var e error
			proof, e = inventoryTar(ctx, pr, req, defaultPayloadBounds, true)
			pr.CloseWithError(e)
			return errors.Join(e, <-done)
		})
		return proof, err
	default:
		return proof, update.ErrUnavailable
	}
}

// RPM's fixed converter emits newc; translate only regular files/directories.
// No paths are ever extracted by a native tool or used as staging names.
func newcToTar(r io.Reader, w io.Writer) error {
	tw := tar.NewWriter(w)
	count := 0
	names := 0
	pad := func(n uint64) error {
		n = (4 - n%4) % 4
		var b [3]byte
		_, e := io.ReadFull(r, b[:n])
		if e != nil {
			return e
		}
		for _, v := range b[:n] {
			if v != 0 {
				return update.ErrIncompatible
			}
		}
		return nil
	}
	for {
		var h [110]byte
		if _, e := io.ReadFull(r, h[:]); e != nil {
			return e
		}
		if string(h[:6]) != "070701" {
			return update.ErrIncompatible
		}
		vals := make([]uint64, 13)
		for i := range vals {
			v, e := strconv.ParseUint(string(h[6+i*8:14+i*8]), 16, 32)
			if e != nil {
				return e
			}
			vals[i] = v
		}
		mode, size, links, nameSize := vals[1], vals[6], vals[4], vals[11]
		if nameSize < 2 || nameSize > 1025 || size > uint64(childArtifactLimit) || vals[12] != 0 {
			return update.ErrIncompatible
		}
		name := make([]byte, nameSize)
		if _, e := io.ReadFull(r, name); e != nil {
			return e
		}
		if name[len(name)-1] != 0 || strings.ContainsRune(string(name[:len(name)-1]), 0) {
			return update.ErrIncompatible
		}
		if e := pad(110 + nameSize); e != nil {
			return e
		}
		raw := string(name[:len(name)-1])
		if raw == "TRAILER!!!" {
			if size != 0 {
				return update.ErrIncompatible
			}
			break
		}
		count++
		names += len(raw)
		if count > 4096 || names > 64<<10 {
			return update.ErrIncompatible
		}
		if _, e := payloadName(raw); e != nil {
			return e
		}
		hdr := &tar.Header{Name: raw, Mode: int64(mode & 0777), Size: int64(size), Format: tar.FormatUSTAR}
		switch mode & 0170000 {
		case 0100000:
			if links != 1 {
				return update.ErrIncompatible
			}
			hdr.Typeflag = tar.TypeReg
		case 0040000:
			if size != 0 {
				return update.ErrIncompatible
			}
			hdr.Typeflag = tar.TypeDir
		default:
			return update.ErrIncompatible
		}
		if e := tw.WriteHeader(hdr); e != nil {
			return fmt.Errorf("cpio header: %w", e)
		}
		if _, e := io.CopyN(tw, r, int64(size)); e != nil {
			return e
		}
		if e := pad(size); e != nil {
			return e
		}
	}
	var buf [4096]byte
	for {
		n, e := r.Read(buf[:])
		for _, b := range buf[:n] {
			if b != 0 {
				return update.ErrIncompatible
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
	}
	return tw.Close()
}
