package registry

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/registry/awsauth"
)

const (
	ecrTarget      = "AmazonEC2ContainerRegistry_V20150921.GetAuthorizationToken"
	ecrContentType = "application/x-amz-json-1.1"
)

var (
	accountIDPattern   = regexp.MustCompile(`^[0-9]{12}$`)
	awsErrorTypeRegexp = regexp.MustCompile(`^[A-Za-z0-9_.#]{1,64}$`)
	unsupportedKeyRe   = regexp.MustCompile(`uses ([a-z_]+);`)
)

// ECRProvider tests and authenticates Amazon ECR connections using a
// hand-signed GetAuthorizationToken call. Every field is optional and
// injectable so tests never reach AWS or the metadata service.
type ECRProvider struct {
	Endpoint func(region string) string // default https://api.ecr.<region>.amazonaws.com
	Client   *http.Client
	IMDS     *awsauth.IMDS
	Paths    func() (credentialsPath, configPath string)
	Now      func() time.Time
}

func (p *ECRProvider) endpoint(region string) string {
	if p.Endpoint != nil {
		return strings.TrimRight(p.Endpoint(region), "/")
	}
	return "https://api.ecr." + region + ".amazonaws.com"
}

func (p *ECRProvider) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{
		Timeout: 15 * time.Second,
		// Never follow a redirect with a signed request attached.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (p *ECRProvider) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *ECRProvider) paths() (string, string) {
	if p.Paths != nil {
		return p.Paths()
	}
	home, _ := os.UserHomeDir()
	return awsauth.DefaultPaths(os.Getenv, home)
}

func (p *ECRProvider) imds() *awsauth.IMDS {
	if p.IMDS != nil {
		return p.IMDS
	}
	return &awsauth.IMDS{}
}

// unsupportedProfileError is a profile the app refuses; Message is shown to
// the operator as is.
type unsupportedProfileError struct{ message string }

func (e *unsupportedProfileError) Error() string { return e.message }

// resolve returns the AWS credentials and region for r according to its mode.
func (p *ECRProvider) resolve(ctx context.Context, r Registry) (awsauth.Credentials, string, error) {
	region := r.ECR.Region
	switch r.ECR.Mode {
	case ECRStatic:
		if r.ECR.AccessKeyID == "" || r.ECR.SecretAccessKey == "" {
			return awsauth.Credentials{}, "", ErrUnconfigured
		}
		creds := awsauth.Credentials{AccessKeyID: r.ECR.AccessKeyID, SecretAccessKey: r.ECR.SecretAccessKey, SessionToken: r.ECR.SessionToken}
		return creds, region, nil
	case ECRRole:
		creds, err := p.imds().RoleCredentials(ctx)
		if err != nil {
			if errors.Is(err, awsauth.ErrNoRole) {
				return awsauth.Credentials{}, "", errors.New("no IAM role is attached to this server")
			}
			if ctx.Err() != nil {
				return awsauth.Credentials{}, "", ctx.Err()
			}
			return awsauth.Credentials{}, "", errors.New("could not read role credentials from the instance metadata service")
		}
		return creds, region, nil
	case ECRProfile:
		if r.ECR.Profile == "" {
			return awsauth.Credentials{}, "", ErrUnconfigured
		}
		credPath, confPath := p.paths()
		creds, profileRegion, err := awsauth.LoadProfile(r.ECR.Profile, credPath, confPath)
		if err != nil {
			if errors.Is(err, awsauth.ErrProfileUnsupported) {
				key := "a delegated credential setting"
				if m := unsupportedKeyRe.FindStringSubmatch(err.Error()); m != nil {
					key = m[1]
				}
				return awsauth.Credentials{}, "", &unsupportedProfileError{
					message: fmt.Sprintf("profile %q uses %s, which is not supported; use static keys", r.ECR.Profile, key)}
			}
			if errors.Is(err, awsauth.ErrProfileNotFound) {
				return awsauth.Credentials{}, "", fmt.Errorf("profile %q was not found", r.ECR.Profile)
			}
			return awsauth.Credentials{}, "", fmt.Errorf("profile %q could not be loaded", r.ECR.Profile)
		}
		if region == "" {
			region = profileRegion
		}
		return creds, region, nil
	}
	return awsauth.Credentials{}, "", ErrUnconfigured
}

type ecrToken struct{ host, password string }

// authorize calls GetAuthorizationToken. Neither response bodies nor request
// headers ever reach an error: AWS error messages can echo them.
func (p *ECRProvider) authorize(ctx context.Context, r Registry) (ecrToken, error) {
	creds, region, err := p.resolve(ctx, r)
	if err != nil {
		return ecrToken{}, err
	}
	if region == "" {
		return ecrToken{}, fmt.Errorf("%w: region is required", ErrUnconfigured)
	}
	if !regionPattern.MatchString(region) {
		return ecrToken{}, fmt.Errorf("region %q is not a valid AWS region", region)
	}
	body := []byte("{}")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(region)+"/", bytes.NewReader(body))
	if err != nil {
		return ecrToken{}, errors.New("build ECR request")
	}
	req.Header.Set("Content-Type", ecrContentType)
	req.Header.Set("X-Amz-Target", ecrTarget)
	if err := awsauth.SignV4(req, body, creds, region, "ecr", p.now()); err != nil {
		return ecrToken{}, errors.New("sign ECR request")
	}
	resp, err := p.client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ecrToken{}, fmt.Errorf("ECR request: %w", ctx.Err())
		}
		return ecrToken{}, errors.New("could not reach the ECR endpoint")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return ecrToken{}, errors.New(ecrStatusText(resp.StatusCode, raw, creds))
	}
	var doc struct {
		AuthorizationData []struct {
			AuthorizationToken string `json:"authorizationToken"`
			ProxyEndpoint      string `json:"proxyEndpoint"`
		} `json:"authorizationData"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.AuthorizationData) == 0 {
		return ecrToken{}, errors.New("ECR returned an unexpected response")
	}
	data := doc.AuthorizationData[0]
	decoded, err := base64.StdEncoding.DecodeString(data.AuthorizationToken)
	if err != nil {
		return ecrToken{}, errors.New("ECR returned a malformed authorization token")
	}
	user, password, ok := strings.Cut(string(decoded), ":")
	if !ok || user != "AWS" || password == "" {
		return ecrToken{}, errors.New("ECR returned a malformed authorization token")
	}
	host := strings.TrimRight(strings.TrimPrefix(strings.TrimPrefix(data.ProxyEndpoint, "https://"), "http://"), "/")
	if host == "" {
		return ecrToken{}, errors.New("ECR returned no registry endpoint")
	}
	return ecrToken{host: host, password: password}, nil
}

// ecrStatusText describes a non-200 answer from the status and, when it is a
// plain identifier that does not contain a credential, the AWS error type.
func ecrStatusText(status int, body []byte, creds awsauth.Credentials) string {
	text := fmt.Sprintf("ECR answered with status %d", status)
	var doc struct {
		Type string `json:"__type"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return text
	}
	t := doc.Type
	if i := strings.LastIndex(t, "#"); i >= 0 {
		t = t[i+1:]
	}
	if !awsErrorTypeRegexp.MatchString(t) {
		return text
	}
	for _, s := range []string{creds.AccessKeyID, creds.SecretAccessKey.Reveal(), creds.SessionToken.Reveal()} {
		if s != "" && strings.Contains(t, s) {
			return text
		}
	}
	return text + " (" + t + ")"
}

// ecrAccountID returns the 12-digit account id from an ECR host, or "".
func ecrAccountID(host string) string {
	id, _, _ := strings.Cut(host, ".")
	if accountIDPattern.MatchString(id) {
		return id
	}
	return ""
}

// ecrRegion returns the region from an ECR host
// (<account>.dkr.ecr.<region>.amazonaws.com), or "".
func ecrRegion(host string) string {
	parts := strings.Split(host, ".")
	if len(parts) >= 6 && parts[1] == "dkr" && parts[2] == "ecr" && parts[len(parts)-2] == "amazonaws" {
		return parts[3]
	}
	return ""
}

// Test calls GetAuthorizationToken. An unsupported profile is a failed test
// result, not an error.
func (p *ECRProvider) Test(ctx context.Context, r Registry) (TestResult, error) {
	tok, err := p.authorize(ctx, r)
	if err != nil {
		var unsupported *unsupportedProfileError
		if errors.As(err, &unsupported) {
			return TestResult{OK: false, Message: unsupported.message}, nil
		}
		return TestResult{}, err
	}
	return TestResult{OK: true, Host: tok.host, Message: "Registry " + tok.host}, nil
}

// Credentials returns a fresh docker login for the registry.
func (p *ECRProvider) Credentials(ctx context.Context, r Registry) (engine.RegistryAuth, error) {
	tok, err := p.authorize(ctx, r)
	if err != nil {
		return engine.RegistryAuth{}, err
	}
	return engine.RegistryAuth{Username: "AWS", Password: tok.password, ServerAddress: tok.host}, nil
}
