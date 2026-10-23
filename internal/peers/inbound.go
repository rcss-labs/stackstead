package peers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

// Protocol is the connected-servers protocol version this binary speaks.
const Protocol = 1

// Optional capabilities a server advertises in its hello. A peer that does
// not list one must not be sent requests that need it.
const (
	FeatureYAMLCreate        = "yaml-create"
	FeatureManagedDockerfile = "managed-dockerfile"
)

// PeerHeader carries a connection token (pairing) or a peer credential.
const PeerHeader = "X-Stackstead-Peer"

const maxPeerNameLength = 64

// pendingTTL is how long a pending row (a pairing in progress) is honoured.
// A pairing takes seconds; older rows are leftovers and are ignored and removed.
const pendingTTL = 2 * time.Minute

var (
	// ErrSelfPair is returned when a server is asked to pair with itself.
	ErrSelfPair = errors.New("cannot pair with itself")
	// ErrInvalidRequest wraps input the peer sent that cannot be used.
	ErrInvalidRequest = errors.New("invalid request")
)

type ServiceConfig struct {
	Store    *Store
	Version  string
	Hostname func() (string, error)
	Now      func() time.Time
	Logger   *slog.Logger
	Resolve  func(ctx context.Context, host string) ([]netip.Addr, error)
	// Timeout bounds one unary call to a peer (default 30s).
	Timeout time.Duration
	// After returns a channel that fires after d (default time.After); a
	// seam for the status checker's tests.
	After func(d time.Duration) <-chan time.Time
}

// Service is this server's side of the peer protocol: it authenticates peers
// and handles pairing, rename and unpair requests they send.
type Service struct {
	cfg          ServiceConfig
	lastActivity atomic.Int64
	kick         chan struct{}
	runtimeMu    sync.RWMutex
	runtime      map[string]peerRuntime
	clients      ClientCache
}

func NewService(cfg ServiceConfig) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Hostname == nil {
		cfg.Hostname = os.Hostname
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.Resolve == nil {
		cfg.Resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	if cfg.After == nil {
		cfg.After = time.After
	}
	return &Service{cfg: cfg, kick: make(chan struct{}, 1), runtime: map[string]peerRuntime{}}
}

// NoteSessionActivity records that a person is using this server's UI.
func (s *Service) NoteSessionActivity() {
	now := s.cfg.Now()
	previous := s.lastActivity.Swap(now.Unix())
	if previous == 0 || now.Sub(time.Unix(previous, 0)) > sessionActiveWindow {
		// A person is back: refresh the status they are about to look at.
		s.Kick()
	}
}

func (s *Service) identity(ctx context.Context) (Identity, error) {
	fallback, err := s.cfg.Hostname()
	if err != nil || fallback == "" {
		fallback = "stackstead"
	}
	return s.cfg.Store.Identity(ctx, fallback)
}

// Hello describes this server for a peer that authenticated; the caller adds
// the stack count.
func (s *Service) Hello(ctx context.Context) (apitypes.PeerHello, error) {
	id, err := s.identity(ctx)
	if err != nil {
		return apitypes.PeerHello{}, err
	}
	return apitypes.PeerHello{Protocol: Protocol, InstanceID: id.InstanceID, Name: id.Name, Version: s.cfg.Version, URL: id.AdvertisedURL,
		Features: []string{FeatureYAMLCreate, FeatureManagedDockerfile}}, nil
}

// Authenticate resolves the peer a credential belongs to. Pending peers (a
// pairing that has not finished) are accepted only when helloOnly.
func (s *Service) Authenticate(ctx context.Context, credential string, helloOnly bool) (Peer, error) {
	if credential == "" {
		return Peer{}, ErrNotPaired
	}
	p, err := s.cfg.Store.PeerByInboundHash(ctx, HashSecret(credential))
	if errors.Is(err, ErrPeerNotFound) {
		return Peer{}, ErrNotPaired
	}
	if err != nil {
		return Peer{}, err
	}
	switch p.State {
	case StateActive:
		return p, nil
	case StatePending:
		if helloOnly && s.cfg.Now().Sub(p.CreatedAt) <= pendingTTL {
			return p, nil
		}
	}
	return Peer{}, ErrNotPaired
}

// AcceptPair handles a pairing request from another server: it claims the
// connection token, verifies the requester by calling its hello endpoint with
// the credential it supplied, then issues a credential of its own. Any
// failure leaves the token usable and stores nothing.
func (s *Service) AcceptPair(ctx context.Context, token string, req apitypes.PeerPairRequest) (apitypes.PeerPairResponse, error) {
	var resp apitypes.PeerPairResponse
	err := s.cfg.Store.ConsumeToken(ctx, token, func() error {
		var err error
		resp, err = s.pair(ctx, req)
		return err
	})
	if err == nil {
		s.Kick()
	}
	return resp, err
}

func (s *Service) pair(ctx context.Context, req apitypes.PeerPairRequest) (apitypes.PeerPairResponse, error) {
	if req.InstanceID == "" || req.Credential == "" || req.URL == "" {
		return apitypes.PeerPairResponse{}, fmt.Errorf("%w: instance ID, URL and credential are required", ErrInvalidRequest)
	}
	if req.Protocol != Protocol {
		return apitypes.PeerPairResponse{}, &PeerError{Code: CodeVersion, Server: req.Name,
			Msg: fmt.Sprintf("the connecting server speaks peer protocol %d; this server speaks %d", req.Protocol, Protocol)}
	}
	own, err := s.identity(ctx)
	if err != nil {
		return apitypes.PeerPairResponse{}, err
	}
	if req.InstanceID == own.InstanceID {
		return apitypes.PeerPairResponse{}, ErrSelfPair
	}
	if existing, err := s.cfg.Store.Peer(ctx, req.InstanceID); err == nil && existing.State == StateActive {
		return apitypes.PeerPairResponse{}, ErrAlreadyPaired
	} else if err != nil && !errors.Is(err, ErrPeerNotFound) {
		return apitypes.PeerPairResponse{}, err
	}
	trust := TLSOptions{CAPEM: req.Trust.CAPEM, PinSHA256: req.Trust.PinSHA256, Plaintext: req.Trust.Plaintext}
	hello, baseURL, err := s.callHello(ctx, req.URL, req.Credential, trust)
	if err != nil {
		return apitypes.PeerPairResponse{}, err
	}
	if hello.InstanceID != req.InstanceID {
		return apitypes.PeerPairResponse{}, &PeerError{Code: CodeWrongInstance, Server: baseURL,
			Msg: fmt.Sprintf("%s is a different Stackstead server than the one that asked to connect", baseURL)}
	}

	credential, hash, err := NewCredential()
	if err != nil {
		return apitypes.PeerPairResponse{}, err
	}
	now := s.cfg.Now()
	name := cleanName(hello.Name, req.Name)
	err = s.cfg.Store.InsertOrReplaceInactivePeer(ctx, Peer{
		InstanceID: req.InstanceID, Name: name, URL: baseURL, Version: hello.Version, TLS: trust,
		OutCredential: Secret(req.Credential), StackCount: hello.StackCount, State: StateActive,
		CreatedAt: now, LastSeenAt: &now,
	}, hash)
	if err != nil {
		return apitypes.PeerPairResponse{}, err
	}
	s.cfg.Logger.Info("server paired", "peer", name, "instance", req.InstanceID)
	return apitypes.PeerPairResponse{
		Protocol: Protocol, InstanceID: own.InstanceID, Name: own.Name, Version: s.cfg.Version,
		Credential: credential.Reveal(),
	}, nil
}

// callHello asks the connecting server who it is, using the credential it
// gave us. Failures are PeerErrors; baseURL is the validated address.
func (s *Service) callHello(ctx context.Context, rawURL, credential string, trust TLSOptions) (apitypes.PeerHello, string, error) {
	base, err := ValidateAddress(rawURL, trust.Plaintext, s.cfg.Resolve)
	if err != nil {
		return apitypes.PeerHello{}, "", fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	server := base.String()
	client, err := NewHTTPClient(base, trust, s.cfg.Timeout)
	if err != nil {
		return apitypes.PeerHello{}, "", fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, server+"/api/peer/hello", nil)
	if err != nil {
		return apitypes.PeerHello{}, "", fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	httpReq.Header.Set(PeerHeader, credential)
	resp, err := client.Do(httpReq)
	if cerr := Classify(server, resp, err); cerr != nil {
		if resp != nil {
			resp.Body.Close()
		}
		return apitypes.PeerHello{}, "", asPeerError(server, cerr)
	}
	defer resp.Body.Close()
	var hello apitypes.PeerHello
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&hello); err != nil {
		return apitypes.PeerHello{}, "", proxyError(server)
	}
	if hello.Protocol != Protocol {
		return apitypes.PeerHello{}, "", &PeerError{Code: CodeVersion, Server: server,
			Msg: fmt.Sprintf("%s speaks peer protocol %d; this server speaks %d", server, hello.Protocol, Protocol)}
	}
	return hello, server, nil
}

// asPeerError turns a Classify result into a PeerError that names the
// address, because the pairing UI shows it to the person who typed it.
func asPeerError(server string, err error) error {
	var pe *PeerError
	if errors.As(err, &pe) {
		if pe.Code == CodeUnreachable {
			pe.Msg = "could not reach " + server
			if reason := errors.Unwrap(pe); reason != nil {
				pe.Msg += ": " + reason.Error()
			}
		}
		return pe
	}
	return &PeerError{Code: CodeProxy, Server: server, Msg: server + " answered with an error: " + err.Error()}
}

// stripUnprintable drops control and Unicode format characters (such as the
// right-to-left override) so a peer cannot disguise its name in logs and UI.
func stripUnprintable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return -1
		}
		return r
	}, s)
}

func cleanName(preferred, fallback string) string {
	for _, candidate := range []string{preferred, fallback} {
		name := strings.TrimSpace(stripUnprintable(candidate))
		if name == "" {
			continue
		}
		if r := []rune(name); len(r) > maxPeerNameLength {
			name = string(r[:maxPeerNameLength])
		}
		return name
	}
	return "server"
}

// ApplyUpdate renames the calling peer and/or changes its address.
func (s *Service) ApplyUpdate(ctx context.Context, from Peer, u apitypes.PeerUpdate) error {
	name := strings.TrimSpace(stripUnprintable(u.Name))
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidRequest)
	}
	if r := []rune(name); len(r) > maxPeerNameLength {
		name = string(r[:maxPeerNameLength])
	}
	newURL := ""
	if u.URL != "" {
		base, err := ValidateAddress(u.URL, from.TLS.Plaintext, s.cfg.Resolve)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
		}
		newURL = base.String()
	}
	return s.cfg.Store.RenamePeer(ctx, from.InstanceID, name, newURL)
}

// AcceptUnpair forgets the calling peer.
func (s *Service) AcceptUnpair(ctx context.Context, from Peer) error {
	err := s.cfg.Store.DeletePeer(ctx, from.InstanceID)
	s.forgetRuntime(from.InstanceID)
	s.clients.Forget(from.InstanceID)
	s.Kick()
	if errors.Is(err, ErrPeerNotFound) {
		return nil
	}
	return err
}
