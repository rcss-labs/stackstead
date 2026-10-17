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
	tr := tar.NewReader(gz)
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
	// Consume the gzip footer so corrupted/trailing compressed data cannot hide
	// behind the tar end markers. The decompressed total remains bounded.
	extra, err := io.Copy(io.Discard, io.LimitReader(gz, artifactLimit-size+1))
	if err != nil || extra > artifactLimit-size {
		return incompatible("invalid archive trailer")
	}
	return nil
}
