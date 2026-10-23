package peers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

const (
	// activeCheckInterval applies while a browser session is active.
	activeCheckInterval = 30 * time.Second
	// idleCheckInterval applies otherwise.
	idleCheckInterval = 5 * time.Minute
	// sessionActiveWindow is how recent a session-cookie request must be for
	// the browser session to count as active.
	sessionActiveWindow = 60 * time.Second
)

// Runtime statuses a peer can have beyond the persisted state.
const (
	statusReachable     = "reachable"
	statusUnreachable   = "unreachable"
	statusWrongInstance = "wrong_instance"
	statusIncompatible  = "incompatible"
)

// peerRuntime is the last known outcome of a hello. It lives in memory so
// that Usable is cheap; the durable parts are mirrored into the peer row.
type peerRuntime struct {
	status  string
	message string
}

// maxReportedText caps peer-supplied text kept in the record.
const (
	maxReportedText   = 200
	usableReadTimeout = 2 * time.Second
)

var errNotActive = errors.New("peer is no longer active")

// Kick wakes the checker: a peer was added or removed, or a person returned.
// It never blocks and never starts a timer.
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Run checks the active peers until ctx ends: every 30 s while a browser
// session is active, every 5 min otherwise. With no active peers it waits for
// Kick without any timer.
func (s *Service) Run(ctx context.Context) {
	for {
		n, err := s.checkAll(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			s.cfg.Logger.Warn("could not list peers to check", "err", err)
		}
		var timer <-chan time.Time
		if n > 0 || err != nil {
			timer = s.cfg.After(s.interval())
		}
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
		case <-timer: // nil (blocks forever) when there is nothing to check
		}
	}
}

func (s *Service) interval() time.Duration {
	last := s.lastActivity.Load()
	if last != 0 && s.cfg.Now().Sub(time.Unix(last, 0)) <= sessionActiveWindow {
		return activeCheckInterval
	}
	return idleCheckInterval
}

// checkAll says hello to every active peer in parallel and returns how many
// there were.
func (s *Service) checkAll(ctx context.Context) (int, error) {
	s.expirePending(ctx)
	list, err := s.cfg.Store.Peers(ctx)
	if err != nil {
		return 0, err
	}
	known := make(map[string]bool, len(list))
	var wg sync.WaitGroup
	active := 0
	for _, p := range list {
		known[p.InstanceID] = true
		if p.State != StateActive {
			continue
		}
		active++
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.check(ctx, p)
		}()
	}
	wg.Wait()
	s.forgetRuntimeExcept(known)
	return active, nil
}

func (s *Service) forgetRuntimeExcept(known map[string]bool) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	for id := range s.runtime {
		if !known[id] {
			delete(s.runtime, id)
		}
	}
}

// CheckNow says hello to one peer right away and returns how it looks after.
func (s *Service) CheckNow(ctx context.Context, id string) (apitypes.PeerView, error) {
	p, err := s.cfg.Store.Peer(ctx, id)
	if err != nil {
		return apitypes.PeerView{}, err
	}
	if p.State == StateActive {
		s.check(ctx, p)
		if p, err = s.cfg.Store.Peer(ctx, id); err != nil {
			return apitypes.PeerView{}, err
		}
	}
	return s.viewOf(p), nil
}

func (s *Service) setRuntime(id, status, message string) {
	s.runtimeMu.Lock()
	s.runtime[id] = peerRuntime{status: status, message: message}
	s.runtimeMu.Unlock()
}

func (s *Service) forgetRuntime(id string) {
	s.runtimeMu.Lock()
	delete(s.runtime, id)
	s.runtimeMu.Unlock()
}

func (s *Service) runtimeOf(id string) (peerRuntime, bool) {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	r, ok := s.runtime[id]
	return r, ok
}

// check performs one hello and records its outcome. Only a Stackstead
// "peer_not_paired" answer ends the connection; anything else, including a
// proxy's login page, leaves the peer active and merely unreachable.
func (s *Service) check(ctx context.Context, p Peer) {
	base, err := url.Parse(p.URL)
	if err != nil {
		s.recordFailure(ctx, p, statusUnreachable, p.Name+" has an invalid address")
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	var hello apitypes.PeerHello
	err = s.callPaired(callCtx, base, p, http.MethodGet, "/api/peer/hello", nil, &hello, s.cfg.Timeout)
	if ctx.Err() != nil {
		return // shutting down or the caller left; say nothing about the peer
	}
	if err != nil {
		if errors.Is(err, ErrNotPaired) {
			s.recordRemoved(ctx, p)
			return
		}
		s.recordFailure(ctx, p, statusUnreachable, failureMessage(p.Name, err))
		return
	}
	s.recordHello(ctx, p, hello)
}

// failureMessage words a failed hello for the person; it never includes URLs
// or credentials.
func failureMessage(name string, err error) string {
	var (
		pe     *PeerError
		remote *RemoteError
	)
	switch {
	case errors.As(err, &pe):
		switch pe.Code {
		case CodeProxy:
			return name + "'s proxy answered instead of Stackstead (it may require its own login, see docs)"
		case CodeTLS:
			return "the TLS certificate of " + name + " could not be verified"
		}
	case errors.As(err, &remote):
		return name + " answered with an error: " + capText(remote.Body.Error, maxReportedText)
	}
	return name + " could not be reached"
}

func (s *Service) recordFailure(ctx context.Context, p Peer, status, message string) {
	if s.persist(ctx, p, func(cur *Peer) { cur.LastError = message }) {
		s.setRuntime(p.InstanceID, status, message)
	}
}

func (s *Service) recordRemoved(ctx context.Context, p Peer) {
	message := p.Name + " removed this connection"
	removed, err := s.cfg.Store.MarkRemoved(ctx, p.InstanceID, p.OutCredential, message)
	if err != nil {
		s.cfg.Logger.Warn("could not mark a server as removed", "peer", p.Name, "err", err)
		return
	}
	if !removed {
		return // the row changed since the hello was sent
	}
	s.forgetRuntime(p.InstanceID)
	s.cfg.Logger.Info("server removed this connection", "peer", p.Name, "instance", p.InstanceID)
}

func (s *Service) recordHello(ctx context.Context, p Peer, hello apitypes.PeerHello) {
	if hello.InstanceID != p.InstanceID {
		msg := p.Name + " now answers as a different server; connect it again"
		s.recordFailure(ctx, p, statusWrongInstance, msg)
		return
	}
	version := cleanVersion(hello.Version)
	status, message := statusReachable, ""
	switch {
	case hello.Protocol != Protocol:
		status = statusIncompatible
		message = fmt.Sprintf("%s speaks peer protocol %d, this server %d. Update to match", p.Name, hello.Protocol, Protocol)
	case !Compatible(s.cfg.Version, version):
		status = statusIncompatible
		message = versionMessage(p.Name, version, s.cfg.Version)
	}

	name := cleanName(hello.Name, p.Name)
	newURL := ""
	if hello.URL != "" {
		if base, err := ValidateAddress(hello.URL, p.TLS.Plaintext, s.cfg.Resolve); err == nil {
			newURL = base.String()
		}
	}
	if name != p.Name || (newURL != "" && newURL != p.URL) {
		// Re-check the pairing first so a stale hello cannot rename a re-paired row.
		if cur, err := s.cfg.Store.Peer(ctx, p.InstanceID); err == nil && cur.State == StateActive && cur.OutCredential == p.OutCredential {
			if err := s.cfg.Store.RenamePeer(ctx, p.InstanceID, name, newURL); err != nil && !errors.Is(err, ErrPeerNotFound) {
				s.cfg.Logger.Warn("could not store the name a server reported", "peer", p.Name, "err", err)
			}
		}
	}
	now := s.cfg.Now()
	if s.persist(ctx, p, func(cur *Peer) {
		cur.Version = version
		cur.StackCount = max(hello.StackCount, 0)
		cur.LastSeenAt = &now
		cur.LastError = message
	}) {
		s.setRuntime(p.InstanceID, status, message)
	}
}

// persist applies change to the row the hello was sent for. A row that was
// removed, deleted or paired again meanwhile is left alone (false).
func (s *Service) persist(ctx context.Context, p Peer, change func(*Peer)) bool {
	err := s.cfg.Store.UpdatePeer(ctx, p.InstanceID, func(cur *Peer) error {
		if cur.State != StateActive || cur.OutCredential != p.OutCredential {
			return errNotActive
		}
		change(cur)
		return nil
	})
	if err != nil {
		if !errors.Is(err, errNotActive) && !errors.Is(err, ErrPeerNotFound) {
			s.cfg.Logger.Warn("could not store a server's status", "instance", p.InstanceID, "err", err)
		}
		return false
	}
	return true
}

// cleanVersion bounds and cleans a version string the peer reported.
func cleanVersion(v string) string {
	return capText(v, 64)
}

func capText(v string, limit int) string {
	v = stripUnprintable(v)
	if r := []rune(v); len(r) > limit {
		v = string(r[:limit])
	}
	return v
}

func versionMessage(name, remote, local string) string {
	rMajor, rMinor, rok := majorMinor(remote)
	lMajor, lMinor, lok := majorMinor(local)
	if rok && lok {
		return fmt.Sprintf("%s runs v%d.%d, this server v%d.%d. Update to match", name, rMajor, rMinor, lMajor, lMinor)
	}
	return fmt.Sprintf("%s runs %s, this server %s. Update to match", name, remote, local)
}

// Usable returns the peer when requests may be forwarded to it. It is cheap
// (one indexed read plus the in-memory status) and safe for concurrent use.
// A peer last seen unreachable is still returned: the request itself finds
// out whether it is back.
func (s *Service) Usable(id string) (Peer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), usableReadTimeout)
	defer cancel()
	p, err := s.cfg.Store.Peer(ctx, id)
	if errors.Is(err, ErrPeerNotFound) {
		return Peer{}, &PeerError{Code: CodeUnknown, Server: id, Msg: "server not found"}
	}
	if err != nil {
		return Peer{}, err
	}
	switch p.State {
	case StateRemoved:
		return Peer{}, &PeerError{Code: CodeUnpaired, Server: p.Name, Msg: p.Name + " removed this connection", cause: ErrNotPaired}
	case StatePending:
		return Peer{}, &PeerError{Code: CodeUnknown, Server: id, Msg: "server not found"}
	}
	if r, ok := s.runtimeOf(id); ok {
		switch r.status {
		case statusWrongInstance:
			return Peer{}, &PeerError{Code: CodeWrongInstance, Server: p.Name, Msg: r.message}
		case statusIncompatible:
			return Peer{}, &PeerError{Code: CodeVersion, Server: p.Name, Msg: r.message}
		}
	}
	return p, nil
}
