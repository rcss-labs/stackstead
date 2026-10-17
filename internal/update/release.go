package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"
)

const latestReleaseURL = "https://api.github.com/repos/rcss-labs/stackstead/releases/latest"
const releaseDownload = "https://github.com/rcss-labs/stackstead/releases/download/"
const metadataName = "stackstead-update.json"
const documentLimit int64 = 1 << 20
const artifactLimit int64 = 512 << 20
const candidateTTL = 15 * time.Minute
const checkCacheTTL = 5 * time.Minute
const requestTimeout = 30 * time.Second

type releaseCacheEntry struct {
	candidate Candidate
	checked   time.Time
}
type issuedCandidate struct {
	candidate       Candidate
	registrationKey string
}

// ReleaseClient trusts only the official HTTPS release origin and digest-bound
// versioned downloads. Staging is unavailable until the private trust store has
// been validated; no production path or source override is exposed.
type ReleaseClient struct {
	client           *http.Client
	mu               sync.Mutex
	cache            map[string]releaseCacheEntry
	issued           map[string]issuedCandidate
	now              func() time.Time
	stagingRoot      string
	checkTrustedRoot func(string) error
}

func NewReleaseClient(client *http.Client) *ReleaseClient {
	transport := http.DefaultTransport
	if client != nil && client.Transport != nil {
		transport = client.Transport
	}
	root := "/var/lib/stackstead-updater"
	if runtime.GOOS == "darwin" {
		root = "/Library/Application Support/StacksteadUpdater"
	}
	return &ReleaseClient{client: &http.Client{Transport: transport, Timeout: requestTimeout, CheckRedirect: releaseRedirect}, cache: make(map[string]releaseCacheEntry), issued: make(map[string]issuedCandidate), now: time.Now, stagingRoot: filepath.Join(root, StagingDirectory), checkTrustedRoot: func(string) error { return ErrSetupRequired }}
}

func releaseRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return incompatible("too many release redirects")
	}
	if req.URL.Scheme != "https" || req.URL.User != nil || req.URL.Port() != "" {
		return incompatible("unsafe release redirect")
	}
	switch req.URL.Hostname() {
	case "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
	default:
		return incompatible("untrusted release redirect host")
	}
	// Never forward caller or redirect-supplied credentials to another request.
	req.Header.Del("Authorization")
	req.Header.Del("Proxy-Authorization")
	req.Header.Del("Cookie")
	return nil
}

func registrationKey(reg Registration) string {
	return fmt.Sprintf("%s/%s/%s/%s/%s/%s", reg.ID, reg.Installer, reg.Identity.OS, reg.Identity.Arch, reg.Identity.Version, reg.Identity.Commit)
}
func issuedKey(c Candidate) string { return c.ID + "/" + c.ExpiresAt.Format(time.RFC3339Nano) }
func cloneCandidate(c Candidate) Candidate {
	c.Manifest.Assets = append([]AssetSpec(nil), c.Manifest.Assets...)
	return c
}

func (c *ReleaseClient) Check(ctx context.Context, reg Registration) (Candidate, error) {
	if err := ctx.Err(); err != nil {
		return Candidate{}, err
	}
	if !reg.UpdateEnabled {
		return Candidate{}, ErrDisabled
	}
	installed, err := stableVersion(strings.TrimPrefix(reg.Identity.Version, "v"))
	if err != nil {
		return Candidate{}, err
	}
	key := registrationKey(reg)
	now := c.now()
	c.mu.Lock()
	entry, ok := c.cache[key]
	c.mu.Unlock()
	if ok && now.Sub(entry.checked) >= 0 && now.Sub(entry.checked) < checkCacheTTL {
		return cloneCandidate(entry.candidate), nil
	}
	data, err := c.fetch(ctx, latestReleaseURL, documentLimit)
	if err != nil {
		return Candidate{}, err
	}
	var latest struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if json.Unmarshal(data, &latest) != nil {
		return Candidate{}, incompatible("invalid latest release response")
	}
	if latest.Draft || latest.Prerelease {
		return Candidate{}, incompatible("latest is not a stable release")
	}
	version := strings.TrimPrefix(latest.Tag, "v")
	target, err := stableVersion(version)
	if err != nil {
		return Candidate{}, err
	}
	var candidate Candidate
	if newerVersion(target, installed) {
		candidate, _, err = c.release(ctx, reg, version)
		if err != nil {
			return Candidate{}, err
		}
		candidate.ExpiresAt = now.Add(candidateTTL)
	}
	c.mu.Lock()
	for k, v := range c.issued {
		if !now.Before(v.candidate.ExpiresAt) {
			delete(c.issued, k)
		}
	}
	for k, v := range c.cache {
		if now.Sub(v.checked) >= checkCacheTTL {
			delete(c.cache, k)
		}
	}
	c.cache[key] = releaseCacheEntry{cloneCandidate(candidate), now}
	if candidate.ID != "" {
		c.issued[issuedKey(candidate)] = issuedCandidate{cloneCandidate(candidate), key}
	}
	c.mu.Unlock()
	return cloneCandidate(candidate), nil
}
func newerVersion(a, b [3]uint64) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// Prepare re-fetches exact version URLs and verifies the confirmed document
// digests. It never resolves latest or executes app/package code. Runtime
// preflight must inspect staged executable/native metadata before stopping.
func (c *ReleaseClient) Prepare(ctx context.Context, reg Registration, confirmed Candidate) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	if !reg.UpdateEnabled {
		return Plan{}, ErrDisabled
	}
	now := c.now()
	if !now.Before(confirmed.ExpiresAt) {
		return Plan{}, ErrCandidateExpired
	}
	c.mu.Lock()
	issued, ok := c.issued[issuedKey(confirmed)]
	c.mu.Unlock()
	if !ok || issued.registrationKey != registrationKey(reg) || !reflect.DeepEqual(issued.candidate, confirmed) {
		return Plan{}, incompatible("candidate was not confirmed by this client")
	}
	current, currentSums, err := c.release(ctx, reg, strings.TrimPrefix(reg.Identity.Version, "v"))
	if err != nil {
		return Plan{}, err
	}
	if err = current.Manifest.ValidateIdentity(reg.Identity); err != nil {
		return Plan{}, err
	}
	target, targetSums, err := c.release(ctx, reg, confirmed.Identity.Version)
	if err != nil {
		return Plan{}, err
	}
	target.ExpiresAt = confirmed.ExpiresAt
	if !reflect.DeepEqual(target, confirmed) {
		return Plan{}, incompatible("confirmed release documents changed")
	}
	if err = target.Manifest.ValidateIdentity(confirmed.Identity); err != nil {
		return Plan{}, err
	}
	// The actual database schema is checked by runtime preflight. This is the
	// release-to-release compatibility gate, using the previous release schema.
	if err = ValidateCompatibility(current.Manifest, target.Manifest, current.Manifest.TargetSchema); err != nil {
		return Plan{}, err
	}
	if err = c.checkTrustedRoot(c.stagingRoot); err != nil {
		return Plan{}, err
	}
	dir, err := os.MkdirTemp(c.stagingRoot, "release-")
	if err != nil {
		return Plan{}, fmt.Errorf("%w: cannot create private staging", ErrUnavailable)
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(dir)
		}
	}()
	previous, err := c.downloadArtifact(ctx, dir, current, currentSums)
	if err != nil {
		return Plan{}, err
	}
	staged, err := c.downloadArtifact(ctx, dir, target, targetSums)
	if err != nil {
		return Plan{}, err
	}
	success = true
	return Plan{Registration: reg, Current: current, Target: target, PreviousArtifact: previous, TargetArtifact: staged}, nil
}

func (c *ReleaseClient) release(ctx context.Context, reg Registration, version string) (Candidate, []byte, error) {
	if _, err := stableVersion(version); err != nil {
		return Candidate{}, nil, err
	}
	base := releaseDownload + "v" + version + "/"
	metadata, err := c.fetch(ctx, base+metadataName, documentLimit)
	if err != nil {
		return Candidate{}, nil, err
	}
	manifest, err := ParseManifest(metadata)
	if err != nil {
		return Candidate{}, nil, err
	}
	if manifest.Identity.Version != version {
		return Candidate{}, nil, incompatible("metadata version differs from release URL")
	}
	asset, err := manifest.ResolveAsset(reg.Installer, reg.Identity.OS, reg.Identity.Arch)
	if err != nil {
		return Candidate{}, nil, err
	}
	if asset.Name != officialAssetName(version, asset) {
		return Candidate{}, nil, incompatible("unexpected official artifact name")
	}
	sums, err := c.fetch(ctx, base+"SHA256SUMS", documentLimit)
	if err != nil {
		return Candidate{}, nil, err
	}
	if err = verifyChecksum(sums, metadataName, metadata); err != nil {
		return Candidate{}, nil, err
	}
	if _, err = checksumFor(sums, asset.Name); err != nil {
		return Candidate{}, nil, err
	}
	md, sd := sha256.Sum256(metadata), sha256.Sum256(sums)
	candidate := Candidate{Identity: Identity{Version: version, Commit: manifest.Identity.Commit, OS: reg.Identity.OS, Arch: reg.Identity.Arch}, Manifest: manifest, Asset: asset, MetadataSHA256: hex.EncodeToString(md[:]), ChecksumsSHA256: hex.EncodeToString(sd[:]), NotesURL: "https://github.com/rcss-labs/stackstead/releases/tag/v" + version}
	candidate.ID = CandidateID(candidate.MetadataSHA256, candidate.ChecksumsSHA256, asset.Name)
	return candidate, sums, nil
}
func officialAssetName(version string, a AssetSpec) string {
	switch a.Installer {
	case InstallerScript:
		return "stackstead_" + version + "_" + a.OS + "_" + a.Arch + ".tar.gz"
	case InstallerDeb:
		return "stackstead_" + version + "_" + a.Arch + ".deb"
	case InstallerRPM:
		arch := "x86_64"
		if a.Arch == "arm64" {
			arch = "aarch64"
		}
		return "stackstead-" + version + "-1." + arch + ".rpm"
	case InstallerPkg:
		return "stackstead_" + version + "_darwin.pkg"
	}
	return ""
}
func (c *ReleaseClient) request(ctx context.Context, address string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	if address == latestReleaseURL {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: release request failed: %w", ErrUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%w: release HTTP status %d", ErrUnavailable, resp.StatusCode)
	}
	return resp, nil
}
func (c *ReleaseClient) fetch(ctx context.Context, address string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	resp, err := c.request(ctx, address)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.ContentLength > limit {
		return nil, incompatible("release document exceeds size limit")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read release document: %w", ErrUnavailable, err)
	}
	if int64(len(data)) > limit {
		return nil, incompatible("release document exceeds size limit")
	}
	return data, nil
}
func (c *ReleaseClient) downloadArtifact(ctx context.Context, dir string, candidate Candidate, sums []byte) (VerifiedArtifact, error) {
	expected, err := checksumFor(sums, candidate.Asset.Name)
	if err != nil {
		return VerifiedArtifact{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	address := releaseDownload + "v" + url.PathEscape(candidate.Identity.Version) + "/" + url.PathEscape(candidate.Asset.Name)
	resp, err := c.request(ctx, address)
	if err != nil {
		return VerifiedArtifact{}, err
	}
	defer resp.Body.Close()
	if resp.ContentLength > artifactLimit {
		return VerifiedArtifact{}, incompatible("artifact exceeds size limit")
	}
	path := filepath.Join(dir, candidate.Asset.Name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return VerifiedArtifact{}, fmt.Errorf("%w: create artifact staging", ErrUnavailable)
	}
	defer f.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(resp.Body, artifactLimit+1))
	if err != nil {
		return VerifiedArtifact{}, fmt.Errorf("%w: download artifact: %w", ErrUnavailable, err)
	}
	if size > artifactLimit {
		return VerifiedArtifact{}, incompatible("artifact exceeds size limit")
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != expected {
		return VerifiedArtifact{}, incompatible("artifact SHA256 mismatch")
	}
	if err = f.Sync(); err != nil {
		return VerifiedArtifact{}, fmt.Errorf("%w: sync staged artifact", ErrUnavailable)
	}
	if err = f.Close(); err != nil {
		return VerifiedArtifact{}, fmt.Errorf("%w: close staged artifact", ErrUnavailable)
	}
	return VerifiedArtifact{Name: candidate.Asset.Name, Path: path, SHA256: actual, Size: size}, nil
}
