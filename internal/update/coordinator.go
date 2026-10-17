package update

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Coordinator struct {
	journal    *Journal
	releases   *ReleaseClient
	runtime    Runtime
	mu         sync.Mutex
	candidates map[string]Candidate
	cancel     map[string]context.CancelFunc
}

func NewCoordinator(r *ReleaseClient, j *Journal, rt Runtime) *Coordinator {
	return &Coordinator{journal: j, releases: r, runtime: rt, candidates: map[string]Candidate{}, cancel: map[string]context.CancelFunc{}}
}
func randomID() (string, error) {
	var b [16]byte
	_, e := rand.Read(b[:])
	return hex.EncodeToString(b[:]), e
}
func view(j Job) JobView {
	return JobView{ID: j.ID, Phase: j.Phase, CurrentVersion: j.Plan.Current.Identity.Version, TargetVersion: j.Plan.Target.Identity.Version, ErrorCode: j.ErrorCode, Message: j.ErrorMessage, UpdatedAt: j.UpdatedAt}
}
func terminal(p Phase) bool {
	return p == PhaseSucceeded || p == PhaseRolledBack || p == PhaseCancelled
}
func (c *Coordinator) Status(ctx context.Context, id string) (UpdateStatus, error) {
	if c.journal == nil {
		return UpdateStatus{}, ErrUnavailable
	}
	reg, e := readRegistration(c.journal.store, id)
	if e != nil {
		return UpdateStatus{}, e
	}
	s := UpdateStatus{Current: reg.Identity, Installer: reg.Installer, Capability: CapabilityReady}
	if !reg.UpdateEnabled {
		s.Capability = CapabilityDisabled
	}
	j, e := c.journal.Load(id)
	if e == nil {
		v := view(j)
		s.Job = &v
		if j.Phase == PhaseRecoveryRequired || j.ReleasePending {
			s.Capability = CapabilityRecoveryRequired
		} else if !terminal(j.Phase) {
			s.Capability = CapabilityBusy
		}
		if j.Phase == PhaseSucceeded {
			s.Current = j.Plan.Target.Identity
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return UpdateStatus{}, e
	}
	c.mu.Lock()
	candidate := c.candidates[id]
	c.mu.Unlock()
	if candidate.ID != "" {
		s.Candidate = &CandidateView{ID: candidate.ID, Version: candidate.Identity.Version, Commit: candidate.Identity.Commit, NotesURL: candidate.NotesURL, ExpiresAt: candidate.ExpiresAt}
	}
	return s, nil
}
func (c *Coordinator) Check(ctx context.Context, id string) (UpdateStatus, error) {
	s, e := c.Status(ctx, id)
	if e != nil {
		return s, e
	}
	if s.Capability != CapabilityReady {
		return s, nil
	}
	if c.releases == nil {
		return s, ErrUnavailable
	}
	reg, e := readRegistration(c.journal.store, id)
	if e != nil {
		return s, e
	}
	candidate, e := c.releases.Check(ctx, reg)
	if e != nil {
		return s, e
	}
	c.mu.Lock()
	c.candidates[id] = candidate
	c.mu.Unlock()
	return c.Status(ctx, id)
}
func (c *Coordinator) Job(_ context.Context, id, jobID string) (JobView, error) {
	if c.journal == nil {
		return JobView{}, ErrUnavailable
	}
	j, e := c.journal.Load(id)
	if e != nil {
		return JobView{}, e
	}
	if j.ID != jobID {
		return JobView{}, ErrUnauthorized
	}
	return view(j), nil
}
func (c *Coordinator) Begin(_ context.Context, id string, request BeginRequest) (JobView, error) {
	if c.journal == nil || c.runtime == nil {
		return JobView{}, ErrUnavailable
	}
	old, e := c.journal.Load(id)
	if e == nil && request.IdempotencyKey != "" && old.IdempotencyKey == request.IdempotencyKey {
		return view(old), nil
	}
	l, e := c.journal.TryLock(id)
	if e != nil {
		return JobView{}, e
	}
	closeLock := true
	defer func() {
		if closeLock {
			l.Close()
		}
	}()
	old, e = c.journal.Load(id)
	if e == nil && (!terminal(old.Phase) || old.ReleasePending) {
		return JobView{}, ErrBusy
	}
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return JobView{}, e
	}
	reg, e := readRegistration(c.journal.store, id)
	if e != nil {
		return JobView{}, e
	}
	if !reg.UpdateEnabled {
		return JobView{}, ErrDisabled
	}
	if request.IdempotencyKey == "" || !safeID.MatchString(request.IdempotencyKey) {
		return JobView{}, ErrIncompatible
	}
	c.mu.Lock()
	candidate := c.candidates[id]
	c.mu.Unlock()
	if candidate.ID == "" || candidate.ID != request.CandidateID || !time.Now().Before(candidate.ExpiresAt) {
		return JobView{}, ErrCandidateExpired
	}
	if c.releases == nil {
		return JobView{}, ErrUnavailable
	}
	worker, e := c.journal.selectWorker(nil)
	if e != nil {
		return JobView{}, e
	}
	jobID, e := randomID()
	if e != nil {
		return JobView{}, e
	}
	j := Job{ID: jobID, InstallationID: id, IdempotencyKey: request.IdempotencyKey, Protocol: JournalProtocol, Phase: PhaseAccepted, WorkerSHA256: filepath.Base(filepath.Dir(worker)), Plan: Plan{Registration: reg, Target: candidate}}
	if e = c.save(l, &j, PhaseAccepted); e != nil {
		return JobView{}, e
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.cancel[id] = cancel
	c.mu.Unlock()
	closeLock = false
	result := view(j)
	go func() {
		defer l.Close()
		defer cancel()
		defer func() { c.mu.Lock(); delete(c.cancel, id); c.mu.Unlock() }()
		_ = c.prepare(ctx, l, &j, candidate)
	}()
	return result, nil
}
func (c *Coordinator) Cancel(ctx context.Context, id, jobID string) (JobView, error) {
	c.mu.Lock()
	j, e := c.journal.Load(id)
	if e != nil {
		c.mu.Unlock()
		return JobView{}, e
	}
	if j.ID != jobID {
		c.mu.Unlock()
		return JobView{}, ErrUnauthorized
	}
	switch j.Phase {
	case PhaseAccepted, PhaseResolving, PhaseDownloading, PhaseVerified:
	default:
		c.mu.Unlock()
		return view(j), ErrBusy
	}
	cancel := c.cancel[id]
	if cancel == nil {
		c.mu.Unlock()
		return view(j), ErrBusy
	}
	cancel()
	c.mu.Unlock()
	for {
		j, e = c.journal.Load(id)
		if e != nil {
			return JobView{}, e
		}
		if j.Phase == PhaseCancelled {
			return view(j), nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return view(j), ctx.Err()
		case <-timer.C:
		}
	}
}
func (c *Coordinator) Startup(_ context.Context, id, instance string) (*Probation, error) {
	if c.journal == nil {
		return nil, ErrUnavailable
	}
	j, e := c.journal.Load(id)
	if errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	found := false
	for _, in := range j.Plan.Registration.Instances {
		found = found || in.ID == instance
	}
	if !found {
		return nil, ErrUnauthorized
	}
	switch j.Phase {
	case PhaseRecoveryRequired:
		if j.RecoveryDecision == RecoveryKeepOld || j.RecoveryDecision == RecoveryRollback {
			return &j.Probation, nil
		}
		return nil, ErrRecoveryRequired
	case PhaseStartingProbation, PhaseVerifying, PhaseRollingBack:
		return &j.Probation, nil
	case PhaseSucceeded, PhaseRolledBack:
		if j.ReleasePending {
			return &j.Probation, nil
		}
		return nil, nil
	case PhaseCancelled:
		return nil, nil
	default:
		return nil, ErrRecoveryRequired
	}
}
func (c *Coordinator) save(l *InstallationLock, j *Job, p Phase) error {
	j.Phase = p
	j.UpdatedAt = time.Now().UTC()
	return l.Save(*j)
}
func (c *Coordinator) prepare(ctx context.Context, l *InstallationLock, j *Job, candidate Candidate) error {
	if e := c.save(l, j, PhaseResolving); e != nil {
		return e
	}
	if e := c.save(l, j, PhaseDownloading); e != nil {
		return e
	}
	p, e := c.releases.Prepare(ctx, j.Plan.Registration, candidate)
	if e != nil {
		j.ErrorCode = "prepare-failed"
		j.ErrorMessage = "Update preparation failed"
		return c.save(l, j, PhaseCancelled)
	}
	j.Plan = p
	if e = c.save(l, j, PhaseVerified); e != nil {
		return e
	}
	c.mu.Lock()
	cancelled := ctx.Err() != nil
	delete(c.cancel, j.InstallationID)
	c.mu.Unlock()
	if cancelled {
		return c.save(l, j, PhaseCancelled)
	}
	return c.execute(context.Background(), l, j)
}
