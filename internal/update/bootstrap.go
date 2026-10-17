package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// PinWorker is root-internal. Source is never accepted from IPC or application config.
func PinWorker(root, source string) (string, error) {
	j, err := OpenJournal(root)
	if err != nil {
		return "", err
	}
	return j.pinWorker(source)
}
func (j *Journal) pinWorker(source string) (string, error) {
	if !filepath.IsAbs(source) || filepath.Clean(source) != source {
		return "", ErrUnauthorized
	}
	parent := &trustStore{root: filepath.Dir(source), owner: j.store.owner, publicDirs: true}
	if j.store.anchor != "" {
		rel, err := filepath.Rel(j.root, filepath.Dir(source))
		if err != nil || rel == ".." {
			return "", ErrUnauthorized
		}
		parent.anchor = j.root
		parent.root = j.root
		d, err := parent.directory(rel, false)
		if err != nil {
			return "", err
		}
		defer d.Close()
		return j.copyWorker(parent, d, filepath.Base(source))
	}
	d, err := parent.directory("", false)
	if err != nil {
		return "", err
	}
	defer d.Close()
	return j.copyWorker(parent, d, filepath.Base(source))
}
func (j *Journal) copyWorker(s *trustStore, d *os.File, name string) (string, error) {
	f, err := s.openFile(d, name, unix.O_RDONLY, 0, false)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if info.Mode().Perm()&0100 == 0 {
		return "", ErrIncompatible
	}
	b, err := io.ReadAll(io.LimitReader(f, artifactLimit+1))
	if err != nil {
		return "", err
	}
	if len(b) == 0 || int64(len(b)) > artifactLimit {
		return "", ErrIncompatible
	}
	sum := sha256.Sum256(b)
	digest := hex.EncodeToString(sum[:])
	l, err := j.acquireLock("worker-locks", "pins")
	if err != nil {
		return "", err
	}
	defer l.Close()
	dir := "workers/" + digest
	// Worker bytes are immutable once published. Verify an existing pin rather than replace it.
	if _, err = j.worker(digest); err == nil {
		return digest, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err = j.store.atomic(dir, "worker", b, 0700); err != nil {
		return "", err
	}
	return digest, nil
}
func SelectWorker(root string, job *Job) (string, error) {
	j, err := OpenJournal(root)
	if err != nil {
		return "", err
	}
	return j.selectWorker(job)
}
func (j *Journal) selectWorker(job *Job) (string, error) {
	if job != nil {
		if job.Protocol != JournalProtocol {
			return "", ErrIncompatible
		}
		return j.worker(job.WorkerSHA256)
	}
	b, err := j.store.read("workers", "active.json")
	if err != nil {
		return "", err
	}
	var digest string
	if json.Unmarshal(b, &digest) != nil {
		return "", ErrRecoveryRequired
	}
	return j.worker(digest)
}
func (j *Journal) worker(digest string) (string, error) {
	if !digestPattern.MatchString(digest) {
		return "", ErrRecoveryRequired
	}
	d, err := j.store.directory("workers/"+digest, false)
	if err != nil {
		return "", err
	}
	defer d.Close()
	f, err := j.store.openFile(d, "worker", unix.O_RDONLY, 0, true)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Mode().Perm()&0100 == 0 {
		return "", ErrRecoveryRequired
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, artifactLimit+1))
	if err != nil || n > artifactLimit || hex.EncodeToString(h.Sum(nil)) != digest {
		return "", ErrRecoveryRequired
	}
	return filepath.Join(j.root, "workers", digest, "worker"), nil
}

// SetActiveWorker affects only idle selection; a job always retains its own pin.
func SetActiveWorker(root, digest string) error {
	j, err := OpenJournal(root)
	if err != nil {
		return err
	}
	l, err := j.acquireLock("worker-locks", "pins")
	if err != nil {
		return err
	}
	defer l.Close()
	if _, err = j.worker(digest); err != nil {
		return err
	}
	b, err := json.Marshal(digest)
	if err != nil {
		return err
	}
	return j.store.atomic("workers", "active.json", b, 0600)
}
