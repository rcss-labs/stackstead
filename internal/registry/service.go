package registry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode"

	"github.com/rcss-labs/stackstead/internal/actionlog"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/secret"
)

// TestResult is the outcome of a connection test.
type TestResult struct {
	OK            bool
	Host, Message string
}

// Provider implements the registry-specific parts. Providers are keyed by Kind
// in the map given to NewService; the ECR provider plugs in the same way.
type Provider interface {
	Test(ctx context.Context, r Registry) (TestResult, error)
	Credentials(ctx context.Context, r Registry) (engine.RegistryAuth, error)
}

// ActionLog records operator actions.
type ActionLog interface {
	Append(context.Context, actionlog.Entry) error
}

// Request is the operator input for creating or updating a registry. On
// update, an empty secret keeps the stored one.
type Request struct {
	Name string
	Kind Kind
	Hub  *Hub
	ECR  *ECR
}

// Service applies validation, secret handling, logging and providers on top of Store.
type Service struct {
	store     *Store
	providers map[Kind]Provider
	actions   ActionLog
	log       *slog.Logger
}

// NewService returns a Service. A nil logger discards output.
func NewService(store *Store, providers map[Kind]Provider, actions ActionLog, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{store: store, providers: providers, actions: actions, log: logger}
}

func (s *Service) record(ctx context.Context, user, kind, target, detail string) {
	if s.actions == nil {
		return
	}
	e := actionlog.Entry{Username: user, Kind: kind, Scope: "host", Target: target, Detail: detail, Result: "ok"}
	if err := s.actions.Append(context.WithoutCancel(ctx), e); err != nil {
		s.log.Warn("append action log failed", "kind", kind, "err", err)
	}
}

func validName(name string) bool {
	n := 0
	for _, c := range name {
		if !unicode.IsPrint(c) {
			return false
		}
		n++
	}
	return n >= 1 && n <= 64
}

// redactedLiteral is what views show for a stored secret; a form that sends it
// back means "keep the stored value".
const redactedLiteral = "[redacted]"

func isBlankSecret(s secret.Secret) bool { return s == "" || s.Reveal() == redactedLiteral }

// mergeECR applies req over the stored ECR settings. Blank secrets keep the
// stored ones, except that a new key pair invalidates the stored session
// token, and a non-static mode drops all static credentials.
func mergeECR(stored, req ECR) (ECR, error) {
	e := req
	if req.Mode != ECRStatic {
		e.AccessKeyID, e.SecretAccessKey, e.SessionToken = "", "", ""
		return e, nil
	}
	newSecret := !isBlankSecret(req.SecretAccessKey)
	newSession := !isBlankSecret(req.SessionToken)
	if req.AccessKeyID == "" {
		e.AccessKeyID = stored.AccessKeyID
	}
	keyChanged := stored.AccessKeyID != "" && e.AccessKeyID != stored.AccessKeyID
	if keyChanged && !newSecret {
		return ECR{}, fmt.Errorf("%w: secretAccessKey is required when accessKeyId changes", ErrInvalid)
	}
	e.SecretAccessKey, e.SessionToken = stored.SecretAccessKey, stored.SessionToken
	if newSecret {
		e.SecretAccessKey = req.SecretAccessKey
	}
	switch {
	case newSession:
		e.SessionToken = req.SessionToken
	case newSecret || keyChanged:
		e.SessionToken = ""
	}
	return e, nil
}

// build merges req over base (the stored registry on update, zero on create).
func build(base Registry, req Request) (Registry, error) {
	r := base
	if !validName(req.Name) {
		return Registry{}, fmt.Errorf("%w: name must be 1-64 printable characters", ErrInvalid)
	}
	r.Name = req.Name
	if base.ID != "" && req.Kind != "" && req.Kind != base.Kind {
		return Registry{}, fmt.Errorf("%w: kind cannot change", ErrInvalid)
	}
	if base.ID == "" {
		r.Kind = req.Kind
	}
	switch r.Kind {
	case KindDockerHub:
		r.Host = HubHost
		if req.Hub == nil {
			return Registry{}, fmt.Errorf("%w: Docker Hub settings required", ErrInvalid)
		}
		r.Hub.Username = strings.TrimSpace(req.Hub.Username)
		if !isBlankSecret(req.Hub.Token) {
			r.Hub.Token = req.Hub.Token
		}
	case KindECR:
		if req.ECR == nil {
			return Registry{}, fmt.Errorf("%w: ECR settings required", ErrInvalid)
		}
		switch req.ECR.Mode {
		case ECRStatic, ECRRole, ECRProfile:
		default:
			return Registry{}, fmt.Errorf("%w: unknown ECR mode", ErrInvalid)
		}
		e, err := mergeECR(r.ECR, *req.ECR)
		if err != nil {
			return Registry{}, err
		}
		if e.Region != "" && !regionPattern.MatchString(e.Region) {
			return Registry{}, fmt.Errorf("%w: region %q is not a valid AWS region", ErrInvalid, e.Region)
		}
		if e.AccountID != "" && !accountIDPattern.MatchString(e.AccountID) {
			return Registry{}, fmt.Errorf("%w: accountId must be 12 digits", ErrInvalid)
		}
		if e.AccountID == "" {
			e.AccountID = r.ECR.AccountID // discovered by a test; keep it across edits
		}
		r.ECR = e
		if e.AccountID != "" && e.Region != "" {
			r.Host = e.AccountID + ".dkr.ecr." + e.Region + ".amazonaws.com"
		} else {
			r.Host = ""
		}
	default:
		return Registry{}, fmt.Errorf("%w: unknown kind", ErrInvalid)
	}
	return r.withFlags(), nil
}

// Create stores a new registry.
func (s *Service) Create(ctx context.Context, user string, req Request) (Registry, error) {
	r, err := build(Registry{}, req)
	if err != nil {
		return Registry{}, err
	}
	created, err := s.store.Create(ctx, r)
	if err != nil {
		return Registry{}, err
	}
	s.record(ctx, user, "registry.create", created.ID, fmt.Sprintf("name=%s kind=%s", created.Name, created.Kind))
	return created.withoutSecrets(), nil
}

// Update changes a registry; blank secrets keep the stored ones.
func (s *Service) Update(ctx context.Context, user, id string, req Request) (Registry, error) {
	cur, err := s.store.Get(ctx, id)
	if err != nil {
		return Registry{}, err
	}
	r, err := build(cur, req)
	if err != nil {
		return Registry{}, err
	}
	if err := s.store.Update(ctx, r); err != nil {
		return Registry{}, err
	}
	s.record(ctx, user, "registry.update", id, fmt.Sprintf("name=%s kind=%s", r.Name, r.Kind))
	return s.Get(ctx, id)
}

// Delete removes a registry and its push targets.
func (s *Service) Delete(ctx context.Context, user, id string) error {
	cur, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.store.Delete(ctx, id); err != nil {
		return err
	}
	s.record(ctx, user, "registry.delete", id, fmt.Sprintf("name=%s kind=%s", cur.Name, cur.Kind))
	return nil
}

// List returns every registry with secret values removed.
func (s *Service) List(ctx context.Context) ([]Registry, error) {
	all, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		all[i] = all[i].withoutSecrets()
	}
	return all, nil
}

// Get returns one registry with secret values removed.
func (s *Service) Get(ctx context.Context, id string) (Registry, error) {
	r, err := s.store.Get(ctx, id)
	if err != nil {
		return Registry{}, err
	}
	return r.withoutSecrets(), nil
}

func (s *Service) provider(k Kind) (Provider, error) {
	p, ok := s.providers[k]
	if !ok {
		return nil, fmt.Errorf("%w: no provider for %q", ErrInvalid, k)
	}
	return p, nil
}

// scrub removes every stored secret value from text.
func scrub(r Registry, text string) string {
	for _, v := range []string{r.Hub.Token.Reveal(), r.ECR.SecretAccessKey.Reveal(), r.ECR.SessionToken.Reveal()} {
		if v != "" {
			text = strings.ReplaceAll(text, v, "[redacted]")
		}
	}
	return text
}

// Test checks the connection and records the outcome. A provider error is
// recorded as a failure with a scrubbed message and returned scrubbed.
func (s *Service) Test(ctx context.Context, id string) (TestResult, error) {
	r, err := s.store.Get(ctx, id)
	if err != nil {
		return TestResult{}, err
	}
	p, err := s.provider(r.Kind)
	if err != nil {
		return TestResult{}, err
	}
	res, err := p.Test(ctx, r)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return TestResult{}, fmt.Errorf("registry test: %w", ctxErr)
		}
		msg := scrub(r, err.Error())
		s.log.Warn("registry test failed", "registry", r.Name, "kind", r.Kind, "reason", msg)
		if rerr := s.store.RecordTest(context.WithoutCancel(ctx), id, false, msg); rerr != nil {
			s.log.Warn("record registry test failed", "registry", r.Name, "err", rerr)
		}
		if errors.Is(err, ErrUnconfigured) {
			return TestResult{OK: false, Message: msg}, ErrUnconfigured
		}
		return TestResult{OK: false, Host: r.Host, Message: msg}, errors.New(msg)
	}
	res.Message = scrub(r, res.Message)
	if res.OK && r.Kind == KindECR {
		s.storeDiscoveredECRHost(ctx, r, res.Host)
	}
	if rerr := s.store.RecordTest(context.WithoutCancel(ctx), id, res.OK, res.Message); rerr != nil {
		return res, rerr
	}
	return res, nil
}

// storeDiscoveredECRHost saves the host, account and region ECR reported,
// filling only values that are still empty. A failure is logged, not fatal.
func (s *Service) storeDiscoveredECRHost(ctx context.Context, r Registry, host string) {
	account := ecrAccountID(host)
	if host == "" || account == "" {
		return
	}
	if err := s.store.SetDiscoveredHost(context.WithoutCancel(ctx), r.ID, host, account, ecrRegion(host)); err != nil {
		s.log.Warn("store discovered ECR host failed", "registry", r.Name, "err", err)
	}
}

// Credentials returns the push credential and a usable registry host for id.
// The host is the stored one, or for an ECR registry that has none yet, the one
// discovered from the token (which is then stored without overwriting values the
// operator set). ErrUnconfigured means the kind's credential fields are missing.
func (s *Service) Credentials(ctx context.Context, id string) (engine.RegistryAuth, string, error) {
	r, err := s.store.Get(ctx, id)
	if err != nil {
		return engine.RegistryAuth{}, "", err
	}
	if !r.configured() {
		return engine.RegistryAuth{}, "", ErrUnconfigured
	}
	p, err := s.provider(r.Kind)
	if err != nil {
		return engine.RegistryAuth{}, "", err
	}
	auth, err := p.Credentials(ctx, r)
	if err != nil {
		return engine.RegistryAuth{}, "", &AuthError{Msg: scrub(r, err.Error())}
	}
	host := r.Host
	if r.Kind == KindECR && host == "" {
		host = auth.ServerAddress
		s.storeDiscoveredECRHost(ctx, r, host)
	}
	if host == "" {
		host = r.ecrHost()
	}
	return auth, host, nil
}

// PushRef returns the full image reference for repository:tag on r.
func PushRef(r Registry, repository, tag string) string {
	if r.Kind == KindDockerHub {
		if !strings.Contains(repository, "/") {
			repository = r.Hub.Username + "/" + repository
		}
		return HubHost + "/" + repository + ":" + tag
	}
	host := r.ecrHost()
	if host == "" {
		return "" // the account is not known yet; callers check configured first
	}
	return host + "/" + repository + ":" + tag
}
