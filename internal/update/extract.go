package update

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// extractTar accepts only the flat payload layout produced by mkrelease. No
// archive ownership, mode, path, link or special-file semantics reach the host.
// Callers provide a new private staging directory, never an installation path.
func extractTar(source io.Reader, destination string) error {
	return extractTarBudget(source, destination, artifactLimit)
}

// extractTarBudget bounds all decompressed bytes, including records tar.Next
// consumes internally, framing and trailer bytes. The smaller budget seam lets
// tests exercise the exact production path without expanding 512 MiB.
func extractTarBudget(source io.Reader, destination string, budget int64) error {
	if budget < 1 || budget > artifactLimit {
		return incompatible("invalid extraction budget")
	}
	st, err := os.Lstat(destination)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return incompatible("untrusted extraction directory")
	}
	gz, err := gzip.NewReader(source)
	if err != nil {
		return incompatible("invalid gzip artifact")
	}
	defer gz.Close()
	// One lookahead byte distinguishes exact-limit EOF from exhaustion. Never
	// give tar.Reader direct access to the unbounded decompressor.
	expanded := &io.LimitedReader{R: gz, N: budget + 1}
	tr := tar.NewReader(expanded)
	allowed := map[string]bool{"stackstead": true, "stackstead-updater": true, "stackstead-updater-bootstrap": true, "stackstead-update.json": true, "install.sh": true, "config.example.yaml": true, "README.md": true, "LICENSE": true, "stackstead.service": true, "launchagent.plist.in": true}
	seen := map[string]bool{}
	var size int64
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return incompatible("malformed artifact archive")
		}
		if !allowed[header.Name] || seen[header.Name] || (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) || header.Size < 0 || header.Size > artifactLimit-size {
			return incompatible("unexpected artifact member")
		}
		seen[header.Name] = true
		size += header.Size
		file, err := os.OpenFile(filepath.Join(destination, header.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return fmt.Errorf("extract artifact member: %w", err)
		}
		_, copyErr := io.CopyN(file, tr, header.Size)
		closeErr := file.Close()
		if copyErr != nil {
			return incompatible("truncated artifact member")
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if !seen["stackstead"] {
		return incompatible("missing application payload")
	}
	// Validate the gzip footer and account for bytes after tar's end markers
	// through the same whole-stream budget used during metadata parsing.
	_, err = io.Copy(io.Discard, expanded)
	if err != nil || expanded.N == 0 {
		return incompatible("invalid archive trailer")
	}
	return nil
}
