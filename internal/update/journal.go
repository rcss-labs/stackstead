package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

type Journal struct {
	root  string
	store *trustStore
}
type journalEnvelope struct {
	SHA256 string          `json:"sha256"`
	Job    json.RawMessage `json:"job"`
}

// InstallationLock excludes every binary writer. Close releases flock; its inode is never deleted.
type InstallationLock struct {
	journal *Journal
	id      string
	file    *os.File
}

func OpenJournal(root string) (*Journal, error) {
	s, err := productionStore(root)
	if err != nil {
		return nil, err
	}
	return &Journal{root: root, store: s}, nil
}

// TryLock is nonblocking. Acquire installation locks before the workers lock.
func (j *Journal) TryLock(id string) (*InstallationLock, error) {
	if !safeID.MatchString(id) {
		return nil, ErrUnauthorized
	}
	return j.acquireLock("locks", id)
}
func (j *Journal) acquireLock(directory, id string) (*InstallationLock, error) {
	d, err := j.store.directory(directory, true)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	f, err := j.store.openFile(d, id+".lock", unix.O_RDWR|unix.O_CREAT, 0600, true)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("%w: installation lock", ErrBusy)
	}
	if err = d.Sync(); err != nil {
		f.Close()
		return nil, err
	}
	return &InstallationLock{journal: j, id: id, file: f}, nil
}
func (l *InstallationLock) Close() error {
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
func (l *InstallationLock) Save(job Job) error {
	if l.file == nil || job.InstallationID != l.id {
		return ErrUnauthorized
	}
	if !safeID.MatchString(job.ID) || job.Protocol != JournalProtocol {
		return ErrIncompatible
	}
	b, err := json.Marshal(job)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	data, err := json.Marshal(journalEnvelope{hex.EncodeToString(sum[:]), b})
	if err != nil {
		return err
	}
	return l.journal.store.atomic("jobs", job.InstallationID+".json", data, 0600)
}

// Save takes a short mutation lock. Transactions holding TryLock must use lock.Save instead.
func (j *Journal) Save(job Job) error {
	l, err := j.TryLock(job.InstallationID)
	if err != nil {
		return err
	}
	defer l.Close()
	return l.Save(job)
}

// Load reads a bounded atomic snapshot without waiting for the mutation lock or repairing state.
func (j *Journal) Load(id string) (Job, error) {
	if !safeID.MatchString(id) {
		return Job{}, ErrUnauthorized
	}
	b, err := j.store.read("jobs", id+".json")
	if err != nil {
		return Job{}, err
	}
	var e journalEnvelope
	if json.Unmarshal(b, &e) != nil {
		return Job{}, ErrRecoveryRequired
	}
	sum := sha256.Sum256(e.Job)
	if hex.EncodeToString(sum[:]) != e.SHA256 {
		return Job{}, ErrRecoveryRequired
	}
	var job Job
	if json.Unmarshal(e.Job, &job) != nil || job.Protocol != JournalProtocol || job.InstallationID != id || !safeID.MatchString(job.ID) {
		return Job{}, ErrRecoveryRequired
	}
	return job, nil
}
