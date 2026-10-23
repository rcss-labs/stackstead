package images

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/rcss-labs/stackstead/internal/images/github"
	"github.com/rcss-labs/stackstead/internal/secret"
)

// GitHubAPI is the part of the GitHub client the image library uses
// (*github.Client implements it).
type GitHubAPI interface {
	User(ctx context.Context, token secret.Secret) (string, error)
	Head(ctx context.Context, token secret.Secret, repo, branch string) (string, error)
	File(ctx context.Context, token secret.Secret, repo, ref, path string) ([]byte, error)
	Tarball(ctx context.Context, token secret.Secret, repo, ref string) (io.ReadCloser, error)
}

var _ GitHubAPI = (*github.Client)(nil)

// ErrGitHubUnconfigured means no GitHub token is stored.
var ErrGitHubUnconfigured = errors.New("no GitHub token is configured")

// Sentinels for GitHub failures; the messages stay token-free.
var (
	ErrGitHubUnauthorized = errors.New("GitHub rejected the token")
	ErrGitHubRateLimited  = errors.New("GitHub rate limit reached, try again later")
	ErrGitHubUnreachable  = errors.New("GitHub is unreachable")
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func errGitHubReadOnly() error { return fieldError("source", "GitHub images are read-only") }

// githubToken returns the stored token or ErrGitHubUnconfigured.
func (s *Service) githubToken(ctx context.Context) (secret.Secret, error) {
	if s.settings == nil || s.github == nil {
		return "", ErrGitHubUnconfigured
	}
	t, err := s.settings.GitHubToken(ctx)
	if err != nil {
		return "", err
	}
	if t == "" {
		return "", ErrGitHubUnconfigured
	}
	return t, nil
}

// checkGitHubSource validates a GitHub source and requires a token. It
// returns the source with its path cleaned.
func (s *Service) checkGitHubSource(ctx context.Context, gs GitHubSource) (GitHubSource, error) {
	if gs.Repo == "" {
		return gs, fieldError("github.repo", "repository is required")
	}
	if err := github.ValidateRepo(gs.Repo); err != nil {
		return gs, fieldError("github.repo", err.Error())
	}
	if err := github.ValidateBranch(gs.Branch); err != nil {
		return gs, fieldError("github.branch", err.Error())
	}
	if gs.Path != "" {
		clean, err := ValidatePath(gs.Path)
		if err != nil {
			return gs, fieldError("github.path", err.Error())
		}
		gs.Path = clean
	}
	if _, err := s.githubToken(ctx); err != nil {
		return gs, err
	}
	return gs, nil
}

// ---- token setting ----

// GitHubStatus says whether a token is stored and who it belongs to, as of
// the last successful test.
type GitHubStatus struct {
	Configured bool
	Login      string
}

// GitHubStatus reports the token state without revealing the token.
func (s *Service) GitHubStatus(ctx context.Context) (GitHubStatus, error) {
	if s.settings == nil {
		return GitHubStatus{}, nil
	}
	t, err := s.settings.GitHubToken(ctx)
	if err != nil || t == "" {
		return GitHubStatus{}, err
	}
	login, err := s.settings.GitHubLogin(ctx)
	return GitHubStatus{Configured: true, Login: login}, err
}

// SetGitHubToken stores the token; an empty token clears it. The action log
// records only "set" or "cleared".
func (s *Service) SetGitHubToken(ctx context.Context, user string, token secret.Secret) error {
	if s.settings == nil {
		return errors.New("settings are not configured")
	}
	if err := s.settings.SetGitHubToken(ctx, token); err != nil {
		return err
	}
	if token != "" {
		// The stored login belonged to the previous token.
		if err := s.settings.SetGitHubLogin(ctx, ""); err != nil {
			return err
		}
	}
	result, detail := "set", "GitHub token set"
	if token == "" {
		result, detail = "cleared", "GitHub token cleared"
	}
	s.record(ctx, user, "github.update", "host", "github", detail, result, "")
	return nil
}

// TestGitHub checks the stored token and remembers its login.
func (s *Service) TestGitHub(ctx context.Context) (string, error) {
	token, err := s.githubToken(ctx)
	if err != nil {
		return "", err
	}
	login, err := s.github.User(ctx, token)
	if err != nil {
		return "", githubFailure(err)
	}
	if err := s.settings.SetGitHubLogin(ctx, login); err != nil {
		return "", err
	}
	return login, nil
}

// ---- head view ----

// GitHubHead is the branch head of a GitHub image with its Dockerfile.
type GitHubHead struct{ Commit, Dockerfile, Path string }

// GitHubHead reads the head commit of the image's branch and its Dockerfile.
func (s *Service) GitHubHead(ctx context.Context, slug string) (GitHubHead, error) {
	img, err := s.store.Image(ctx, slug)
	if err != nil {
		return GitHubHead{}, err
	}
	if img.Source != SourceGitHub {
		return GitHubHead{}, fieldError("source", "this image is not built from GitHub")
	}
	token, err := s.githubToken(ctx)
	if err != nil {
		return GitHubHead{}, err
	}
	commit, err := s.github.Head(ctx, token, img.GitHub.Repo, img.GitHub.Branch)
	if err != nil {
		if errors.Is(err, github.ErrNotFound) {
			return GitHubHead{}, fieldError("github.branch", "repository or branch not found")
		}
		return GitHubHead{}, githubFailure(err)
	}
	p := img.DockerfilePath
	if img.GitHub.Path != "" {
		p = img.GitHub.Path + "/" + p
	}
	content, err := s.github.File(ctx, token, img.GitHub.Repo, commit, p)
	switch {
	case errors.Is(err, github.ErrNotFound):
		return GitHubHead{}, fieldError("github.path", "Dockerfile not found at "+p)
	case errors.Is(err, github.ErrTooLarge):
		return GitHubHead{}, fieldError("github.path", "Dockerfile is too large")
	case err != nil:
		return GitHubHead{}, githubFailure(err)
	}
	return GitHubHead{Commit: commit, Dockerfile: string(content), Path: p}, nil
}

// ---- build ----

// resolveCommit turns a build ref into a 40-hex commit: empty means the head
// of the image's branch, a 40-hex SHA is used as is, anything else is a
// branch or tag name resolved with Head.
func (s *Service) resolveCommit(ctx context.Context, gs GitHubSource, ref string) (string, error) {
	token, err := s.githubToken(ctx)
	if err != nil {
		return "", err
	}
	switch {
	case commitPattern.MatchString(ref):
		return ref, nil
	case ref == "":
		sha, err := s.github.Head(ctx, token, gs.Repo, gs.Branch)
		if errors.Is(err, github.ErrNotFound) {
			return "", fieldError("github.branch", "repository or branch not found")
		}
		if err != nil {
			return "", githubFailure(err)
		}
		return sha, nil
	default:
		if err := github.ValidateBranch(ref); err != nil {
			return "", fieldError("ref", err.Error())
		}
	}
	sha, err := s.github.Head(ctx, token, gs.Repo, ref)
	if errors.Is(err, github.ErrNotFound) {
		return "", fieldError("ref", "branch, tag or commit not found")
	}
	if err != nil {
		return "", githubFailure(err)
	}
	return sha, nil
}

// extractGitHub downloads the commit's tarball and unpacks the image's path
// into dst. The caller removes dst on every outcome.
func (s *Service) extractGitHub(ctx context.Context, img Image, commit, dst string) error {
	token, err := s.githubToken(ctx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return fmt.Errorf("prepare build context: %w", err)
	}
	body, err := s.github.Tarball(ctx, token, img.GitHub.Repo, commit)
	if err != nil {
		return githubFailure(err)
	}
	defer body.Close()
	_, err = github.ExtractContext(body, img.GitHub.Path, dst, s.limits.MaxFileBytes, s.limits.MaxContextBytes)
	if err != nil {
		return githubFailure(err)
	}
	return nil
}

// mappedError keeps the GitHub message and also matches an images sentinel.
type mappedError struct {
	msg    string
	target error
}

func (e *mappedError) Error() string { return e.msg }
func (e *mappedError) Unwrap() error { return e.target }

// githubFailure maps a github package error to a message safe for the build
// log and the failed version. None of the messages can carry the token.
func githubFailure(err error) error {
	switch {
	case errors.Is(err, github.ErrBadPath):
		return &mappedError{err.Error(), ErrBadPath}
	case errors.Is(err, github.ErrFileTooLarge):
		return &mappedError{err.Error(), ErrFileTooLarge}
	case errors.Is(err, github.ErrContextTooLarge):
		return &mappedError{err.Error(), ErrContextTooLarge}
	case errors.Is(err, github.ErrArchiveTooLarge):
		return errors.New("repository archive is too large")
	case errors.Is(err, github.ErrStalled):
		return &mappedError{"GitHub download stalled", ErrGitHubUnreachable}
	case errors.Is(err, github.ErrRateLimited):
		return &mappedError{ErrGitHubRateLimited.Error(), ErrGitHubRateLimited}
	case errors.Is(err, github.ErrUnauthorized):
		return &mappedError{ErrGitHubUnauthorized.Error(), ErrGitHubUnauthorized}
	case errors.Is(err, github.ErrNotFound):
		return errors.New("GitHub repository or ref not found")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	}
	return &mappedError{"GitHub request failed: " + strings.TrimPrefix(err.Error(), "github: "), ErrGitHubUnreachable}
}
