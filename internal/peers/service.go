package peers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

const unpairTimeout = 5 * time.Second

// ValidationError carries field errors for the person using Settings.
type ValidationError struct {
	Fields []apitypes.FieldError
}

func (e *ValidationError) Error() string {
	if len(e.Fields) == 0 {
		return "invalid request"
	}
	return e.Fields[0].Message
}

func validationError(field, message string) *ValidationError {
	return &ValidationError{Fields: []apitypes.FieldError{{Field: field, Message: message}}}
}

// Self describes this server for Settings. SuggestedURL is the address the
// browser used to reach it, for the person to confirm as the advertised one.
func (s *Service) Self(ctx context.Context, r *http.Request) (apitypes.ServerSelf, error) {
	id, err := s.identity(ctx)
	if err != nil {
		return apitypes.ServerSelf{}, err
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return apitypes.ServerSelf{
		InstanceID: id.InstanceID, Name: id.Name, Slug: id.Slug, AdvertisedURL: id.AdvertisedURL,
		SuggestedURL: scheme + "://" + r.Host, Version: s.cfg.Version,
	}, nil
}

// shapeOnly accepts any host as private, so ValidateAddress checks only the
// URL's shape; whether peers can reach it is their business.
func shapeOnly(context.Context, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

// SetSelf changes this server's name and advertised address and tells every
// active peer. Peers that cannot be reached learn it from the next hello.
func (s *Service) SetSelf(ctx context.Context, u apitypes.ServerSelfUpdate) (apitypes.ServerSelf, error) {
	name := strings.TrimSpace(stripUnprintable(u.Name))
	var fields []apitypes.FieldError
	switch {
	case name == "":
		fields = append(fields, apitypes.FieldError{Field: "name", Message: "a name is required"})
	case len([]rune(name)) > maxPeerNameLength:
		fields = append(fields, apitypes.FieldError{Field: "name", Message: fmt.Sprintf("the name can have at most %d characters", maxPeerNameLength)})
	}
	advertised := strings.TrimSpace(u.AdvertisedURL)
	if advertised != "" {
		base, err := ValidateAddress(advertised, true, shapeOnly)
		if err != nil {
			fields = append(fields, apitypes.FieldError{Field: "advertisedUrl", Message: err.Error()})
		} else {
			advertised = base.String()
		}
	}
	if len(fields) > 0 {
		return apitypes.ServerSelf{}, &ValidationError{Fields: fields}
	}
	id, err := s.cfg.Store.SetIdentity(ctx, name, advertised)
	if err != nil {
		return apitypes.ServerSelf{}, err
	}
	s.pushUpdate(ctx, apitypes.PeerUpdate{Name: id.Name, URL: id.AdvertisedURL})
	return apitypes.ServerSelf{
		InstanceID: id.InstanceID, Name: id.Name, Slug: id.Slug, AdvertisedURL: id.AdvertisedURL, Version: s.cfg.Version,
	}, nil
}

func (s *Service) pushUpdate(ctx context.Context, update apitypes.PeerUpdate) {
	list, err := s.cfg.Store.Peers(ctx)
	if err != nil {
		s.cfg.Logger.Warn("could not list peers to push an update", "err", err)
		return
	}
	var wg sync.WaitGroup
	for _, p := range list {
		if p.State != StateActive || p.OutCredential == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.callStored(ctx, p, "/api/peer/update", update, s.cfg.Timeout); err != nil {
				s.cfg.Logger.Info("peer update not delivered; it will get it with the next hello", "peer", p.Name, "err", err)
			}
		}()
	}
	wg.Wait()
}

// callStored posts body to an already paired peer with its credential.
func (s *Service) callStored(ctx context.Context, p Peer, path string, body any, timeout time.Duration) error {
	base, err := url.Parse(p.URL)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return s.callPeer(ctx, base, p.TLS, p.OutCredential.Reveal(), http.MethodPost, path, body, nil, timeout)
}

// CreateConnectionToken issues a token for another server to pair with this one.
func (s *Service) CreateConnectionToken(ctx context.Context) (apitypes.ConnectionToken, error) {
	token, expires, err := s.cfg.Store.CreateToken(ctx)
	if err != nil {
		return apitypes.ConnectionToken{}, err
	}
	return apitypes.ConnectionToken{Token: token, ExpiresAt: expires}, nil
}

// ProbeAddress reads a candidate server's health and certificate so the
// person can decide how to trust it. Nothing secret is sent.
func (s *Service) ProbeAddress(ctx context.Context, raw string) (apitypes.PeerProbe, error) {
	base, err := ValidateAddress(raw, true, s.cfg.Resolve)
	if err != nil {
		return apitypes.PeerProbe{}, validationError("url", err.Error())
	}
	res, err := s.probe(ctx, base)
	if err != nil {
		return apitypes.PeerProbe{}, err
	}
	return apitypes.PeerProbe{
		Peering: res.Peering, FingerprintSHA256: res.FingerprintSHA256, SystemTrusted: res.SystemTrusted,
		PlaintextAllowed: base.Scheme == "http",
	}, nil
}

// probe is Probe with errors worded for the person who typed the address, and
// a server without peering reported as peer_old.
func (s *Service) probe(ctx context.Context, base *url.URL) (ProbeResult, error) {
	res, err := Probe(ctx, base)
	if err != nil {
		var pe *PeerError
		if errors.As(err, &pe) && pe.Code == CodeUnreachable {
			return res, &PeerError{Code: CodeUnreachable, Server: base.String(), Msg: "could not reach " + base.String(), cause: pe.cause}
		}
		return res, err
	}
	if res.Peering == 0 {
		return res, &PeerError{Code: CodeOld, Server: base.Host, Msg: base.Host + " runs an older Stackstead without connected servers"}
	}
	return res, nil
}

type addPlan struct {
	base          *url.URL
	trust         TLSOptions
	advertised    string
	selfTrust     apitypes.PeerTrust
	advertisedRaw bool // the request supplied the advertised address
}

// validateAdd checks everything it can without the network.
func (s *Service) validateAdd(ctx context.Context, req apitypes.AddPeerRequest) (addPlan, error) {
	var (
		fields []apitypes.FieldError
		plan   addPlan
	)
	fail := func(field, message string) {
		fields = append(fields, apitypes.FieldError{Field: field, Message: message})
	}

	if strings.TrimSpace(req.Token) == "" {
		fail("token", "paste the connection token from the other server")
	}
	if u, err := url.Parse(strings.TrimSpace(req.URL)); err == nil && u.Scheme == "http" && !req.Trust.Plaintext {
		fail("trust.plaintext", "plain http needs you to accept sending a root-equivalent credential unencrypted")
	} else if base, err := ValidateAddress(req.URL, req.Trust.Plaintext, s.cfg.Resolve); err != nil {
		fail("url", err.Error())
	} else {
		plan.base = base
	}
	if req.Trust.Plaintext != req.SelfTrust.Plaintext {
		fail("selfTrust.plaintext", "the plaintext acceptance must be the same in both directions")
	}
	for prefix, trust := range map[string]apitypes.PeerTrust{"trust": req.Trust, "selfTrust": req.SelfTrust} {
		if pin := normalizeFingerprint(trust.PinSHA256); pin != "" {
			if raw, err := hex.DecodeString(pin); err != nil || len(raw) != 32 {
				fail(prefix+".pinSha256", "the fingerprint must be 64 hex characters (SHA-256)")
			}
		}
		if _, err := tlsConfig(TLSOptions{CAPEM: trust.CAPEM}); err != nil {
			fail(prefix+".caPem", err.Error())
		}
	}

	advertised := strings.TrimSpace(req.AdvertisedURL)
	plan.advertisedRaw = advertised != ""
	if advertised == "" {
		id, err := s.identity(ctx)
		if err != nil {
			return plan, err
		}
		advertised = id.AdvertisedURL
	}
	if advertised == "" {
		fail("advertisedUrl", "enter the address the other server should use to reach this one")
	} else if base, err := ValidateAddress(advertised, req.SelfTrust.Plaintext, shapeOnly); err != nil {
		fail("advertisedUrl", err.Error())
	} else {
		plan.advertised = base.String()
		plan.selfTrust = req.SelfTrust
		plan.selfTrust.Plaintext = base.Scheme == "http"
	}
	if len(fields) > 0 {
		sortFields(fields)
		return plan, &ValidationError{Fields: fields}
	}
	plan.trust = TLSOptions{CAPEM: req.Trust.CAPEM, PinSHA256: req.Trust.PinSHA256, Plaintext: plan.base.Scheme == "http"}
	return plan, nil
}

// sortFields keeps the order a form shows its inputs (map iteration above is random).
func sortFields(fields []apitypes.FieldError) {
	rank := func(f string) int {
		switch {
		case f == "url":
			return 0
		case strings.HasPrefix(f, "trust"):
			return 1
		case strings.HasPrefix(f, "selfTrust"):
			return 2
		case f == "advertisedUrl":
			return 3
		}
		return -1 // token first
	}
	sort.SliceStable(fields, func(a, b int) bool { return rank(fields[a].Field) < rank(fields[b].Field) })
}

// AddServer pairs this server with the one at req.URL. Pairing is mutual, so
// this server drives both halves: it stores its half as a pending row under a
// temporary ID, asks the other server to store its half, and replaces the
// pending row with the real peer only when that succeeded. Any failure leaves
// nothing behind here, and the other server stores nothing either.
func (s *Service) AddServer(ctx context.Context, req apitypes.AddPeerRequest) (apitypes.PeerView, error) {
	plan, err := s.validateAdd(ctx, req)
	if err != nil {
		return apitypes.PeerView{}, err
	}
	if _, err := s.probe(ctx, plan.base); err != nil {
		return apitypes.PeerView{}, err
	}
	own, err := s.identity(ctx)
	if err != nil {
		return apitypes.PeerView{}, err
	}
	if plan.advertisedRaw && own.AdvertisedURL != plan.advertised {
		if own, err = s.cfg.Store.SetIdentity(ctx, own.Name, plan.advertised); err != nil {
			return apitypes.PeerView{}, err
		}
	}

	credential, hash, err := NewCredential()
	if err != nil {
		return apitypes.PeerView{}, err
	}
	var rawID [8]byte
	if _, err := rand.Read(rawID[:]); err != nil {
		return apitypes.PeerView{}, err
	}
	tempID := "pending-" + hex.EncodeToString(rawID[:])
	now := s.cfg.Now()
	pending := Peer{InstanceID: tempID, Name: plan.base.Host, URL: plan.base.String(), TLS: plan.trust, State: StatePending, CreatedAt: now}
	if err := s.cfg.Store.InsertPeer(ctx, pending, hash); err != nil {
		return apitypes.PeerView{}, err
	}
	paired := false
	defer func() {
		if !paired {
			// The caller's context may be what failed; the cleanup must still run.
			if err := s.cfg.Store.DeletePeer(context.WithoutCancel(ctx), tempID); err != nil && !errors.Is(err, ErrPeerNotFound) {
				s.cfg.Logger.Warn("could not remove the pending server record", "err", err)
			}
		}
	}()

	var answer apitypes.PeerPairResponse
	err = s.callPeer(ctx, plan.base, plan.trust, strings.TrimSpace(req.Token), http.MethodPost, "/api/peer/pair",
		apitypes.PeerPairRequest{
			Protocol: Protocol, InstanceID: own.InstanceID, Name: own.Name, URL: plan.advertised,
			Version: s.cfg.Version, Credential: credential.Reveal(), Trust: plan.selfTrust,
		}, &answer, 2*s.cfg.Timeout)
	if err != nil {
		return apitypes.PeerView{}, mapPairError(plan.base.String(), err)
	}
	switch {
	case answer.Protocol != Protocol:
		return apitypes.PeerView{}, &PeerError{Code: CodeVersion, Server: plan.base.String(),
			Msg: fmt.Sprintf("%s speaks peer protocol %d; this server speaks %d", plan.base, answer.Protocol, Protocol)}
	case answer.InstanceID == "" || answer.Credential == "":
		return apitypes.PeerView{}, proxyError(plan.base.String())
	case answer.InstanceID == own.InstanceID:
		return apitypes.PeerView{}, validationError("url", "this is the address of this server itself")
	}

	peer := Peer{
		InstanceID: answer.InstanceID, Name: cleanName(answer.Name, plan.base.Host), URL: plan.base.String(),
		Version: answer.Version, TLS: plan.trust, OutCredential: Secret(answer.Credential),
		State: StateActive, CreatedAt: now, LastSeenAt: &now,
	}
	if err := s.cfg.Store.ActivatePending(ctx, tempID, peer, hash); err != nil {
		// The other server already stored its half; ask it to drop that.
		s.sendUnpair(context.WithoutCancel(ctx), peer)
		if errors.Is(err, ErrAlreadyPaired) {
			return apitypes.PeerView{}, validationError("url", "this server is already connected")
		}
		return apitypes.PeerView{}, err
	}
	paired = true
	s.Kick()
	// Pairing succeeded; failing to read the row back must not undo that.
	stored, err := s.cfg.Store.Peer(ctx, answer.InstanceID)
	if err != nil {
		stored = peer
	}
	s.cfg.Logger.Info("server paired", "peer", stored.Name, "instance", stored.InstanceID)
	return s.viewOf(stored), nil
}

// mapPairError turns the other server's answer to a pairing request into
// something Settings can show. It never produces a 401-style error: the
// browser would treat that as a lost session.
func mapPairError(server string, err error) error {
	var (
		pe     *PeerError
		remote *RemoteError
	)
	switch {
	case errors.As(err, &pe) && pe.Code == CodeUnpaired:
		return validationError("token", "the other server did not accept the connection token; it may have expired or been used already")
	case errors.As(err, &remote):
		switch {
		case remote.Status == http.StatusConflict:
			return validationError("url", remote.Body.Error)
		case remote.Status == http.StatusBadRequest:
			return validationError("advertisedUrl", remote.Body.Error)
		}
		switch remote.Body.Code {
		case CodeUnreachable, CodeTLS, CodeUnpaired, CodeProxy, CodeVersion, CodeWrongInstance:
			return &PeerError{Code: remote.Body.Code, Server: server, Msg: remote.Body.Error}
		}
		return &PeerError{Code: CodeProxy, Server: server, Msg: fmt.Sprintf("%s answered: %s", server, remote.Body.Error)}
	}
	return err
}

// callPeer sends one JSON request to a peer and decodes a JSON answer into
// out (nil for none). Failures are *PeerError or *RemoteError.
func (s *Service) callPeer(ctx context.Context, base *url.URL, trust TLSOptions, credential, method, path string, body, out any, timeout time.Duration) error {
	client, err := NewHTTPClient(base, trust, timeout)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	server := base.String()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, server+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set(PeerHeader, credential)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if cerr := Classify(server, resp, err); cerr != nil {
		if resp != nil {
			resp.Body.Close()
		}
		var remote *RemoteError
		if errors.As(cerr, &remote) {
			return cerr
		}
		return asPeerError(server, cerr)
	}
	defer resp.Body.Close()
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(out); err != nil {
		return proxyError(server)
	}
	return nil
}

// sendUnpair tells a peer to forget this server; failure only means the peer
// finds out at its next hello.
func (s *Service) sendUnpair(ctx context.Context, p Peer) {
	if err := s.callStored(ctx, p, "/api/peer/unpair", nil, unpairTimeout); err != nil {
		s.cfg.Logger.Info("peer was not told about the removal", "peer", p.Name, "err", err)
	}
}

// Remove forgets a peer here first, then tells it. The local delete never
// waits on the peer being reachable.
func (s *Service) Remove(ctx context.Context, id string) error {
	p, err := s.cfg.Store.Peer(ctx, id)
	if err != nil {
		return err
	}
	if err := s.cfg.Store.DeletePeer(ctx, id); err != nil {
		return err
	}
	s.Kick()
	if p.State == StateActive && p.OutCredential != "" {
		s.sendUnpair(ctx, p)
	}
	return nil
}

// Views lists the connected servers as Settings shows them.
func (s *Service) Views(ctx context.Context) ([]apitypes.PeerView, error) {
	list, err := s.cfg.Store.Peers(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]apitypes.PeerView, 0, len(list))
	for _, p := range list {
		views = append(views, s.viewOf(p))
	}
	return views, nil
}

// viewOf shows a peer with the checker's last verdict. Without one (just
// started) the persisted last error decides between reachable and unreachable.
func (s *Service) viewOf(p Peer) apitypes.PeerView {
	status := statusReachable
	switch {
	case p.State == StatePending:
		status = "pending"
	case p.State == StateRemoved:
		status = "removed"
	case p.LastError != "":
		status = statusUnreachable
	}
	if p.State == StateActive {
		if r, ok := s.runtimeOf(p.InstanceID); ok {
			status = r.status
		}
	}
	return apitypes.PeerView{
		ID: p.InstanceID, Name: p.Name, Slug: p.Slug, URL: p.URL, Version: p.Version, Status: status,
		StackCount: p.StackCount, LastSeenAt: p.LastSeenAt, LastError: p.LastError,
		Compatible: status != statusIncompatible, Plaintext: p.TLS.Plaintext,
	}
}
