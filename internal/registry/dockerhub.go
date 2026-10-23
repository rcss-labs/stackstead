package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
)

const (
	defaultHubBase  = "https://hub.docker.com"
	hubServerAddr   = "https://index.docker.io/v1/"
	hubRejectedText = "Docker Hub rejected the username or token"
)

// HubProvider tests and authenticates Docker Hub connections. Base and Client
// are injectable so tests never reach the network.
type HubProvider struct {
	Base   string
	Client *http.Client
}

func (p *HubProvider) base() string {
	if p.Base != "" {
		return strings.TrimRight(p.Base, "/")
	}
	return defaultHubBase
}

func (p *HubProvider) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// Test signs in against Docker Hub's auth endpoint. The response body is never
// read into an error: Hub may echo the credential back.
func (p *HubProvider) Test(ctx context.Context, r Registry) (TestResult, error) {
	if !r.configured() {
		return TestResult{}, ErrUnconfigured
	}
	body, err := json.Marshal(map[string]string{"username": r.Hub.Username, "password": r.Hub.Token.Reveal()})
	if err != nil {
		return TestResult{}, errors.New("encode Docker Hub login")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base()+"/v2/users/login", bytes.NewReader(body))
	if err != nil {
		return TestResult{}, errors.New("build Docker Hub request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return TestResult{}, fmt.Errorf("Docker Hub test: %w", ctx.Err())
		}
		return TestResult{}, errors.New("could not reach Docker Hub")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch resp.StatusCode {
	case http.StatusOK:
		return TestResult{OK: true, Host: HubHost, Message: "Signed in as " + r.Hub.Username}, nil
	case http.StatusUnauthorized:
		return TestResult{OK: false, Host: HubHost, Message: hubRejectedText}, nil
	default:
		return TestResult{}, fmt.Errorf("Docker Hub answered with status %d", resp.StatusCode)
	}
}

// Credentials returns the docker push credential for r.
func (p *HubProvider) Credentials(_ context.Context, r Registry) (engine.RegistryAuth, error) {
	if !r.configured() {
		return engine.RegistryAuth{}, ErrUnconfigured
	}
	return engine.RegistryAuth{Username: r.Hub.Username, Password: r.Hub.Token.Reveal(), ServerAddress: hubServerAddr}, nil
}
