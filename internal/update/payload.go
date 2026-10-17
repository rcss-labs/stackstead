package update

import (
	"crypto/sha256"
	"encoding/hex"
	"golang.org/x/sys/unix"
	"io"
	"os"
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
func artifactRelative(s *trustStore, path string) (string, error) {
	r, err := filepath.Rel(s.root, path)
	if err != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path || !strings.HasPrefix(r, StagingDirectory+string(filepath.Separator)) {
		return "", ErrSetupRequired
	}
	return r, nil
}
func extractVerifiedPayload(s *trustStore, a VerifiedArtifact, destination string) error {
	dest, err := artifactRelative(s, destination)
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
	f, err := openVerifiedArtifact(s, a)
	if err != nil {
		return err
	}
	defer f.Close()
	return extractTar(f, destination)
}

// VerifyArtifact is a root-internal native adapter API. The recorded path must
// remain helper-private; callers hold the managed transaction's writer guard.
func VerifyArtifact(a VerifiedArtifact) error {
	s, err := productionStore(PrivateRoot)
	if err != nil {
		return err
	}
	return verifyArtifact(s, a)
}
func verifyArtifact(s *trustStore, a VerifiedArtifact) error {
	f, err := openVerifiedArtifact(s, a)
	if err != nil {
		return err
	}
	return f.Close()
}
func openVerifiedArtifact(s *trustStore, a VerifiedArtifact) (*os.File, error) {
	source, err := artifactRelative(s, a.Path)
	if err != nil {
		return nil, err
	}
	parent, err := s.directory(filepath.Dir(source), false)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	f, err := s.openFile(parent, filepath.Base(source), unix.O_RDONLY, 0, true)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*os.File, error) { f.Close(); return nil, err }
	st, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	if a.Size <= 0 || a.Size > artifactLimit || st.Size() != a.Size || !digestPattern.MatchString(a.SHA256) {
		return fail(incompatible("artifact size or digest invalid"))
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, artifactLimit+1))
	if err != nil {
		return fail(err)
	}
	if n != a.Size || hex.EncodeToString(hash.Sum(nil)) != a.SHA256 {
		return fail(incompatible("artifact changed after verification"))
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	return f, nil
}
