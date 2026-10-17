package update

import (
	"crypto/sha256"
	"encoding/hex"
	"golang.org/x/sys/unix"
	"io"
	"path/filepath"
	"strings"
)

// ExtractVerifiedPayload is a root-internal adapter API. Both paths must be in
// helper-private staging; neither is accepted from IPC or browser requests.
func ExtractVerifiedPayload(a VerifiedArtifact, destination string) error {
	s, err := productionStore(PrivateRoot)
	if err != nil {
		return err
	}
	return extractVerifiedPayload(s, a, destination)
}
func extractVerifiedPayload(s *trustStore, a VerifiedArtifact, destination string) error {
	relative := func(path string) (string, error) {
		r, err := filepath.Rel(s.root, path)
		if err != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path || !strings.HasPrefix(r, StagingDirectory+string(filepath.Separator)) {
			return "", ErrSetupRequired
		}
		return r, nil
	}
	dest, err := relative(destination)
	if err != nil {
		return err
	}
	source, err := relative(a.Path)
	if err != nil {
		return err
	}
	d, err := s.directory(dest, false)
	if err != nil {
		return err
	}
	defer d.Close()
	entries, err := d.Readdirnames(1)
	if len(entries) != 0 || err != io.EOF {
		return ErrSetupRequired
	}
	parent, err := s.directory(filepath.Dir(source), false)
	if err != nil {
		return err
	}
	defer parent.Close()
	f, err := s.openFile(parent, filepath.Base(source), unix.O_RDONLY, 0, true)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if a.Size <= 0 || a.Size > artifactLimit || st.Size() != a.Size || !digestPattern.MatchString(a.SHA256) {
		return incompatible("artifact size or digest invalid")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, artifactLimit+1))
	if err != nil {
		return err
	}
	if n != a.Size || hex.EncodeToString(hash.Sum(nil)) != a.SHA256 {
		return incompatible("artifact changed after verification")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return extractTar(f, destination)
}
