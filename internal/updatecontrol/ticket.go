// Package updatecontrol controls the app's shared maintenance gate. It never
// creates durable probation tickets or writes the helper journal.
package updatecontrol

import (
	"sync"

	"github.com/rcss-labs/stackstead/internal/maintenance"
	"github.com/rcss-labs/stackstead/internal/update"
)

type State struct {
	mu        sync.Mutex
	Instance  update.Instance
	Gate      *maintenance.Gate
	readiness update.Readiness
	ticket    *update.Probation
}

func NewState(in update.Instance, g *maintenance.Gate, p *update.Probation) *State {
	s := &State{Instance: in, Gate: g}
	if p != nil {
		x := *p
		s.ticket = &x
	}
	return s
}
func (s *State) Apply(p update.Probation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.JobID == "" || p.Nonce == "" || s.Gate == nil || !s.Gate.Frozen() {
		return update.ErrUnauthorized
	}
	if s.ticket != nil && *s.ticket != p {
		return update.ErrUnauthorized
	}
	x := p
	s.ticket = &x
	return nil
}
func (s *State) Release(job string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ticket == nil || job == "" || job != s.ticket.JobID {
		return update.ErrUnauthorized
	}
	s.ticket = nil
	s.Gate.Thaw()
	return nil
}
func (s *State) Ready() update.Readiness {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.readiness
	r.InstanceID = s.Instance.ID
	if s.ticket == nil || r.JobID != s.ticket.JobID || r.Nonce != s.ticket.Nonce || r.Identity != s.ticket.Expected || r.PID <= 0 || r.ConfigSHA256 == "" {
		r.Ready = false
	}
	return r
}
func (s *State) MarkReady(r update.Readiness) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readiness = r
	if s.ticket != nil && r.Identity == s.ticket.Expected {
		s.readiness.JobID = s.ticket.JobID
		s.readiness.Nonce = s.ticket.Nonce
	}
}
