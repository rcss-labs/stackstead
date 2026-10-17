package update

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
)

// ExternalReservation excludes updater mutations for a lifecycle tool's lifetime.
// Package callbacks must never wait on or reenter their parent's reservation.
type ExternalReservation struct{ lock *InstallationLock }

func (j *Journal) TryExternalReservation() (*ExternalReservation, error) {
	l, err := j.acquireLock("global", "update")
	if err != nil {
		return nil, err
	}
	if err = j.requireIdle(); err != nil {
		l.Close()
		return nil, err
	}
	return &ExternalReservation{lock: l}, nil
}
func (r *ExternalReservation) Close() error {
	if r.lock == nil {
		return nil
	}
	err := r.lock.Close()
	r.lock = nil
	return err
}

// records enumerates canonical bounded snapshots from a trusted descriptor.
// Atomic temporary files are the sole ignored namespace.
func (j *Journal) records(directory string) (map[string][]byte, error) {
	d, err := j.store.directory(directory, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.Join(ErrRecoveryRequired, err)
	}
	defer d.Close()
	names, err := d.Readdirnames(1025)
	if err != nil && err != io.EOF {
		return nil, errors.Join(ErrRecoveryRequired, err)
	}
	if len(names) > 1024 {
		return nil, ErrRecoveryRequired
	}
	records := make(map[string][]byte, len(names))
	for _, name := range names {
		if atomicTempPattern.MatchString(name) {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if name != id+".json" || !safeID.MatchString(id) {
			return nil, ErrRecoveryRequired
		}
		f, err := j.store.openFile(d, name, 0, 0, true)
		if err != nil {
			return nil, errors.Join(ErrRecoveryRequired, err)
		}
		b, readErr := readBounded(f)
		closeErr := f.Close()
		if readErr != nil || closeErr != nil {
			return nil, errors.Join(ErrRecoveryRequired, readErr, closeErr)
		}
		records[id] = b
	}
	return records, nil
}

// Registrations returns a bounded deterministic trusted snapshot without locks.
func (j *Journal) Registrations() ([]Registration, error) {
	records, err := j.records("registrations")
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	regs := make([]Registration, 0, len(ids))
	prefixes := map[string]bool{}
	for _, id := range ids {
		var reg Registration
		if json.Unmarshal(records[id], &reg) != nil || reg.ID != id || ValidateRegistration(reg) != nil || prefixes[reg.Prefix] {
			return nil, ErrRecoveryRequired
		}
		prefixes[reg.Prefix] = true
		regs = append(regs, reg)
	}
	return regs, nil
}

// PendingJob discovers durable ownership even after a process releases flock.
// Ambiguous or damaged evidence never selects an arbitrary recovery worker.
func (j *Journal) PendingJob() (*Job, error) {
	if _, err := j.Registrations(); err != nil {
		return nil, err
	}
	records, err := j.records("jobs")
	if err != nil {
		return nil, err
	}
	var pending *Job
	for id, b := range records {
		job, err := decodeJob(id, b)
		if err != nil {
			return nil, err
		}
		if !terminal(job.Phase) || job.ReleasePending {
			if pending != nil {
				return nil, ErrRecoveryRequired
			}
			pending = &job
		}
	}
	return pending, nil
}
func (j *Journal) requireIdle() error {
	pending, err := j.PendingJob()
	if err != nil {
		return err
	}
	if pending != nil {
		return ErrBusy
	}
	return nil
}
