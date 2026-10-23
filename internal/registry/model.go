// Package registry stores container-registry connections and talks to the
// providers behind them. Credentials are secret.Secret values and never reach
// logs, errors or API views.
package registry

import (
	"errors"
	"time"

	"github.com/rcss-labs/stackstead/internal/secret"
)

// Kind is the registry provider.
type Kind string

const (
	KindDockerHub Kind = "dockerhub"
	KindECR       Kind = "ecr"
)

// ECRMode selects how an ECR connection obtains AWS credentials.
type ECRMode string

const (
	ECRStatic  ECRMode = "static"
	ECRRole    ECRMode = "role"
	ECRProfile ECRMode = "profile"
)

// HubHost is the host stored for Docker Hub connections.
const HubHost = "docker.io"

var (
	ErrNotFound     = errors.New("registry not found")
	ErrNameTaken    = errors.New("registry name already in use")
	ErrUnconfigured = errors.New("registry is missing required settings")
	ErrInvalid      = errors.New("invalid registry")
)

// Hub holds Docker Hub settings. TokenSet reports whether a token is stored,
// so views can show "configured" without the value.
type Hub struct {
	Username string
	Token    secret.Secret
	TokenSet bool
}

// ECR holds Amazon ECR settings. The *Set flags report stored secrets.
type ECR struct {
	Region          string
	Mode            ECRMode
	AccessKeyID     string
	SecretAccessKey secret.Secret
	SessionToken    secret.Secret
	Profile         string
	AccountID       string

	SecretKeySet    bool
	SessionTokenSet bool
}

// Registry is one stored connection.
type Registry struct {
	ID, Name        string
	Kind            Kind
	Host            string
	Hub             Hub
	ECR             ECR
	CreatedAt       time.Time
	UpdatedAt       time.Time
	LastTestAt      *time.Time
	LastTestOK      bool
	LastTestMessage string
}

// withoutSecrets returns a copy with every secret value cleared; the *Set
// flags keep saying whether one is stored.
func (r Registry) withoutSecrets() Registry {
	r.Hub.Token = ""
	r.ECR.SecretAccessKey = ""
	r.ECR.SessionToken = ""
	return r
}

// withFlags recomputes the *Set flags from the secret values.
func (r Registry) withFlags() Registry {
	r.Hub.TokenSet = r.Hub.Token != ""
	r.ECR.SecretKeySet = r.ECR.SecretAccessKey != ""
	r.ECR.SessionTokenSet = r.ECR.SessionToken != ""
	return r
}

// configured reports whether the kind's required fields are present.
func (r Registry) configured() bool {
	switch r.Kind {
	case KindDockerHub:
		return r.Hub.Username != "" && r.Hub.Token != ""
	case KindECR:
		if r.ECR.Region == "" {
			return false
		}
		switch r.ECR.Mode {
		case ECRStatic:
			return r.ECR.AccessKeyID != "" && r.ECR.SecretAccessKey != ""
		case ECRProfile:
			return r.ECR.Profile != ""
		case ECRRole:
			return true
		}
	}
	return false
}

func secretOf(s string) secret.Secret { return secret.Secret(s) }
