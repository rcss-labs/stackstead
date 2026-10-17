package updatehost

import (
	"context"
	"github.com/rcss-labs/stackstead/internal/update"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
)

type rpmFileMetadata struct {
	Path        string
	Mode        int64
	User, Group string
}

const rpmFilesQueryFormat = "[%{FILENAMES}\t%{FILEMODES:octal}\t%{FILEUSERNAME}\t%{FILEGROUPNAME}\n]"

func rpmMetadataFromArtifact(ctx context.Context, input *os.File) (map[string]rpmFileMetadata, error) {
	return rpmMetadataWithConverter(ctx, input, convertPayloadStream)
}
func rpmMetadataWithConverter(ctx context.Context, input *os.File, convert func(context.Context, *os.File, string, []string, int64, func(io.Reader) error) error) (map[string]rpmFileMetadata, error) {
	var facts map[string]rpmFileMetadata
	_, err := input.Seek(0, 0)
	if err != nil {
		return nil, err
	}
	err = convert(ctx, input, "/usr/bin/rpm", []string{"-qp", "--queryformat", rpmFilesQueryFormat, "/dev/fd/3"}, childMetadataLimit, func(r io.Reader) error { var e error; facts, e = readRPMFileMetadata(r); return e })
	if err != nil {
		return nil, err
	}
	_, err = input.Seek(0, 0)
	return facts, err
}
func readRPMFileMetadata(r io.Reader) (map[string]rpmFileMetadata, error) {
	b, err := io.ReadAll(io.LimitReader(r, childMetadataLimit+1))
	if err != nil {
		return nil, err
	}
	if len(b) == 0 || len(b) > childMetadataLimit || b[len(b)-1] != '\n' {
		return nil, update.ErrIncompatible
	}
	facts := map[string]rpmFileMetadata{}
	names := 0
	for _, line := range strings.Split(string(b[:len(b)-1]), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 4 {
			return nil, update.ErrIncompatible
		}
		p := f[0]
		names += len(p)
		if len(p) > 1024 || names > 64<<10 || !strings.HasPrefix(p, "/") || path.Clean(p) != p || p == "/" || strings.ContainsAny(p, "\x00\r\n\t\\") {
			return nil, update.ErrIncompatible
		}
		if _, ok := facts[p]; ok {
			return nil, update.ErrIncompatible
		}
		if len(facts) >= 4096 {
			return nil, update.ErrIncompatible
		}
		for _, v := range f[1] {
			if v < '0' || v > '7' {
				return nil, update.ErrIncompatible
			}
		}
		mode, e := strconv.ParseInt(f[1], 8, 32)
		if e != nil || mode&^0177777 != 0 || (mode&0170000 != 0100000 && mode&0170000 != 0040000) {
			return nil, update.ErrIncompatible
		}
		if (f[2] != "root" && f[2] != "stackstead") || (f[3] != "root" && f[3] != "stackstead") {
			return nil, update.ErrIncompatible
		}
		facts[p] = rpmFileMetadata{Path: p, Mode: mode, User: f[2], Group: f[3]}
	}
	return facts, nil
}
func applyRPMFileMetadata(e *payloadEntry, facts map[string]rpmFileMetadata) error {
	f, ok := facts[e.Path]
	if !ok || f.Path != e.Path || e.UID != 0 || e.GID != 0 {
		return update.ErrIncompatible
	}
	fullMode := e.Mode | 0100000
	if e.Directory {
		fullMode = e.Mode | 0040000
	}
	if fullMode != f.Mode {
		return update.ErrIncompatible
	}
	if (e.Uname != "" && e.Uname != f.User) || (e.Gname != "" && e.Gname != f.Group) {
		return update.ErrIncompatible
	}
	e.Uname = f.User
	e.Gname = f.Group
	return nil
}
func correlateRPMFileMetadata(entries []payloadEntry, facts map[string]rpmFileMetadata) error {
	if len(entries) != len(facts) {
		return update.ErrIncompatible
	}
	seen := map[string]bool{}
	for i := range entries {
		if seen[entries[i].Path] {
			return update.ErrIncompatible
		}
		seen[entries[i].Path] = true
		if e := applyRPMFileMetadata(&entries[i], facts); e != nil {
			return e
		}
	}
	return nil
}
