package awsauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/secret"
)

// ErrNoRole means the instance has no IAM role attached.
var ErrNoRole = errors.New("awsauth: no IAM role attached to this instance")

const defaultIMDSBase = "http://169.254.169.254"

// IMDS fetches role credentials from the EC2 instance metadata service (v2).
type IMDS struct {
	Base   string       // default http://169.254.169.254
	Client *http.Client // default: 2 s timeout
}

func (i *IMDS) base() string {
	if i.Base != "" {
		return strings.TrimRight(i.Base, "/")
	}
	return defaultIMDSBase
}

func (i *IMDS) client() *http.Client {
	if i.Client != nil {
		return i.Client
	}
	return &http.Client{Timeout: 2 * time.Second}
}

func (i *IMDS) do(ctx context.Context, method, path string, headers map[string]string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, i.base()+path, nil)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := i.client().Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("awsauth: instance metadata %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, err
}

// RoleCredentials returns the credentials of the first attached IAM role.
func (i *IMDS) RoleCredentials(ctx context.Context) (Credentials, error) {
	const credPath = "/latest/meta-data/iam/security-credentials/"
	status, tok, err := i.do(ctx, http.MethodPut, "/latest/api/token",
		map[string]string{"X-aws-ec2-metadata-token-ttl-seconds": "21600"})
	if err != nil {
		return Credentials{}, err
	}
	if status != http.StatusOK {
		return Credentials{}, fmt.Errorf("awsauth: instance metadata token request returned %d", status)
	}
	hdr := map[string]string{"X-aws-ec2-metadata-token": strings.TrimSpace(string(tok))}

	status, list, err := i.do(ctx, http.MethodGet, credPath, hdr)
	if err != nil {
		return Credentials{}, err
	}
	if status == http.StatusNotFound {
		return Credentials{}, ErrNoRole
	}
	if status != http.StatusOK {
		return Credentials{}, fmt.Errorf("awsauth: instance metadata role listing returned %d", status)
	}
	role := strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(list)), "\n", 2)[0])
	if role == "" {
		return Credentials{}, ErrNoRole
	}

	status, body, err := i.do(ctx, http.MethodGet, credPath+role, hdr)
	if err != nil {
		return Credentials{}, err
	}
	if status == http.StatusNotFound {
		return Credentials{}, ErrNoRole
	}
	if status != http.StatusOK {
		return Credentials{}, fmt.Errorf("awsauth: instance metadata credentials returned %d", status)
	}
	var doc struct {
		AccessKeyID     string `json:"AccessKeyId"`
		SecretAccessKey string
		Token           string
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		// Never wrap err or include body: they could echo secret material.
		return Credentials{}, errors.New("awsauth: instance metadata returned malformed credentials")
	}
	if doc.AccessKeyID == "" || doc.SecretAccessKey == "" {
		return Credentials{}, errors.New("awsauth: instance metadata credentials are incomplete")
	}
	return Credentials{
		AccessKeyID:     doc.AccessKeyID,
		SecretAccessKey: secret.Secret(doc.SecretAccessKey),
		SessionToken:    secret.Secret(doc.Token),
	}, nil
}
