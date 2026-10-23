package images

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/imageref"
	"github.com/rcss-labs/stackstead/internal/peers"
)

// PeerSource is the part of the peers service the library needs; *peers.Service
// satisfies it. Sharing reaches directly paired servers only.
type PeerSource interface {
	Views(ctx context.Context) ([]apitypes.PeerView, error)
	Usable(id string) (peers.Peer, error)
}

var _ PeerSource = (*peers.Service)(nil)

const (
	libraryCacheTTL    = 10 * time.Second
	defaultPeerTimeout = 3 * time.Second
	maxPeerAnswerBytes = 16 << 20
	peerImagesPath     = "/api/peer/images"
	peerUsagePath      = "/api/peer/images/usage"

	statusOld         = "old"
	statusUnreachable = "unreachable"
	statusReachable   = "reachable"
)

// errPeerOld means the peer answered 404: it predates the image library.
var errPeerOld = errors.New("peer does not offer the image library")

// peerResult is what one peer's library looked like when last asked.
type peerResult struct {
	server  ServerInfo
	images  []LibraryEntry
	fetched time.Time
}

type peerCache struct {
	mu         sync.Mutex
	entries    map[string]peerResult
	refreshing bool
}

// activePeers lists the peers worth asking: everything except pending and removed pairings.
func (s *Service) activePeers(ctx context.Context) ([]apitypes.PeerView, error) {
	views, err := s.peers.Views(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.PeerView, 0, len(views))
	for _, v := range views {
		if v.Status != "pending" && v.Status != "removed" {
			out = append(out, v)
		}
	}
	return out, nil
}

func peerServer(v apitypes.PeerView) ServerInfo {
	return ServerInfo{ID: v.ID, Name: v.Name, Slug: v.Slug, Status: statusReachable}
}

// peerGet asks one peer for a JSON answer. Errors are *peers.PeerError,
// *peers.RemoteError or errPeerOld, and never contain the URL or credential.
func (s *Service) peerGet(ctx context.Context, id, path string, out any) error {
	p, err := s.peers.Usable(id)
	if err != nil {
		return err
	}
	base, err := url.Parse(p.URL)
	if err != nil {
		return &peers.PeerError{Code: peers.CodeUnreachable, Server: p.Name, Msg: p.Name + " could not be reached"}
	}
	ctx, cancel := context.WithTimeout(ctx, s.peerTimeout)
	defer cancel()
	client, err := peers.NewHTTPClient(base, p.TLS, s.peerTimeout)
	if err != nil {
		return &peers.PeerError{Code: peers.CodeUnreachable, Server: p.Name, Msg: p.Name + " could not be reached"}
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.URL, "/")+path, nil)
	if err != nil {
		return &peers.PeerError{Code: peers.CodeUnreachable, Server: p.Name, Msg: p.Name + " could not be reached"}
	}
	req.Header.Set(peers.PeerHeader, p.OutCredential.Reveal())
	resp, err := client.Do(req)
	if resp != nil {
		defer resp.Body.Close()
	}
	if err == nil && resp.StatusCode == http.StatusNotFound {
		return errPeerOld
	}
	if cerr := peers.Classify(p.Name, resp, err); cerr != nil {
		return cerr
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxPeerAnswerBytes)).Decode(out); err != nil {
		return &peers.PeerError{Code: peers.CodeProxy, Server: p.Name, Msg: p.Name + " sent an answer that could not be read"}
	}
	return nil
}

// peerFailure turns a peerGet error into a server status and a message safe to show.
func peerFailure(v apitypes.PeerView, err error) (status, message string) {
	if errors.Is(err, errPeerOld) {
		return statusOld, v.Name + " runs a version without the image library"
	}
	var pe *peers.PeerError
	var re *peers.RemoteError
	if errors.As(err, &pe) || errors.As(err, &re) {
		return statusUnreachable, err.Error()
	}
	return statusUnreachable, v.Name + " could not be reached"
}

// fetchPeerLibrary asks one peer for its images.
func (s *Service) fetchPeerLibrary(ctx context.Context, v apitypes.PeerView) peerResult {
	res := peerResult{server: peerServer(v), images: []LibraryEntry{}, fetched: time.Now()}
	var dtos []apitypes.LibraryImageDetail
	if err := s.peerGet(ctx, v.ID, peerImagesPath, &dtos); err != nil {
		res.server.Status, res.server.Error = peerFailure(v, err)
		return res
	}
	for _, d := range dtos {
		res.images = append(res.images, entryFromPeer(d, res.server))
	}
	return res
}

// entryFromPeer converts a peer's image into a library entry owned by server.
// Only succeeded versions are kept; push targets belong to the owner.
func entryFromPeer(d apitypes.LibraryImageDetail, owner ServerInfo) LibraryEntry {
	e := LibraryEntry{
		Image: Image{Slug: d.Slug, Name: d.Name, Source: Source(d.Source), DockerfilePath: d.DockerfilePath,
			BuildArgs: d.BuildArgs, Retention: Retention{KeepLast: d.Retention.KeepLast, KeepDays: d.Retention.KeepDays},
			CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt},
		Owner: owner, Versions: []Version{}, PushTargets: []PushTarget{}, Pushes: map[string][]Push{},
	}
	if d.GitHub != nil {
		e.GitHub = GitHubSource{Repo: d.GitHub.Repo, Branch: d.GitHub.Branch, Path: d.GitHub.Path}
	}
	for _, v := range d.Versions {
		if VersionStatus(v.Status) != VersionSucceeded {
			continue
		}
		e.Versions = append(e.Versions, Version{ID: v.ID, Tag: v.Tag, Status: VersionSucceeded, Revision: v.Revision,
			Commit: v.Commit, BuildArgs: v.BuildArgs, Arch: v.Arch, ImageDigest: v.ImageID, Size: v.Size,
			StartedAt: v.StartedAt, EndedAt: v.EndedAt})
	}
	return e
}

// peerLibraries returns every active peer's library in parallel. Results
// younger than the cache TTL are reused; the rest are fetched, each bounded
// by the peer timeout, so a hanging peer cannot hold the call longer.
func (s *Service) peerLibraries(ctx context.Context) ([]peerResult, error) {
	views, err := s.activePeers(ctx)
	if err != nil {
		return nil, err
	}
	results := make([]peerResult, len(views))
	var wg sync.WaitGroup
	for i, v := range views {
		s.peerCache.mu.Lock()
		cached, ok := s.peerCache.entries[v.ID]
		s.peerCache.mu.Unlock()
		if ok && time.Since(cached.fetched) < s.libraryTTL {
			cached.server = refreshName(cached.server, v)
			results[i] = cached
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = s.fetchPeerLibrary(ctx, v)
			s.peerCache.mu.Lock()
			if s.peerCache.entries == nil {
				s.peerCache.entries = map[string]peerResult{}
			}
			s.peerCache.entries[v.ID] = results[i]
			s.peerCache.mu.Unlock()
		}()
	}
	wg.Wait()
	s.peerCache.mu.Lock()
	for id := range s.peerCache.entries {
		found := false
		for _, v := range views {
			found = found || v.ID == id
		}
		if !found {
			delete(s.peerCache.entries, id)
		}
	}
	s.peerCache.mu.Unlock()
	return results, nil
}

// refreshName keeps a cached result's server name current after a rename.
func refreshName(s ServerInfo, v apitypes.PeerView) ServerInfo {
	s.Name, s.Slug = v.Name, v.Slug
	return s
}

// cachedPeerLibraries returns what is cached without waiting for any peer.
// Stale or missing entries are refreshed in the background.
func (s *Service) cachedPeerLibraries(ctx context.Context) []peerResult {
	if s.peers == nil {
		return nil
	}
	views, err := s.activePeers(ctx)
	if err != nil {
		return nil
	}
	out := make([]peerResult, 0, len(views))
	stale := false
	s.peerCache.mu.Lock()
	for _, v := range views {
		e, ok := s.peerCache.entries[v.ID]
		if !ok || time.Since(e.fetched) >= s.libraryTTL {
			stale = true
		}
		if !ok {
			e = peerResult{server: peerServer(v)}
		}
		e.server = refreshName(e.server, v)
		out = append(out, e)
	}
	start := stale && !s.peerCache.refreshing
	if start {
		s.peerCache.refreshing = true
	}
	s.peerCache.mu.Unlock()
	if start {
		go func() {
			defer func() {
				s.peerCache.mu.Lock()
				s.peerCache.refreshing = false
				s.peerCache.mu.Unlock()
			}()
			if _, err := s.peerLibraries(context.Background()); err != nil {
				s.log.Warn("refresh peer libraries failed", "err", err)
			}
		}()
	}
	return out
}

// resolvePeerRef fills r from the cached libraries when ref belongs to a peer.
func resolvePeerRef(results []peerResult, ref imageref.Ref, r *ResolvedRef) {
	for _, res := range results {
		if imageref.OwnerID(res.server.ID) != ref.Owner {
			continue
		}
		r.OwnerName = res.server.Name
		for _, e := range res.images {
			if e.Slug != ref.Slug {
				continue
			}
			var current *Version
			for i := range e.Versions {
				if e.Versions[i].Tag == ref.Tag {
					current = &e.Versions[i]
				}
			}
			if current == nil || len(e.Versions) == 0 {
				return
			}
			r.Known, r.Arch = true, current.Arch
			latest := e.Versions[0] // newest first
			r.LatestTag, r.Newer = latest.Tag, latest.ID != current.ID
			return
		}
		return
	}
}

// Library lists this server's images plus every active peer's, fetched in
// parallel and cached for 10 s. A peer that answers 404 is "old"; one that
// fails is "unreachable" with Error set. Neither fails the call.
func (s *Service) Library(ctx context.Context) (Library, error) {
	self, local, err := s.localLibrary(ctx)
	if err != nil {
		return Library{}, err
	}
	lib := Library{Images: local, Servers: []ServerInfo{self}}
	if s.peers == nil {
		return lib, nil
	}
	results, err := s.peerLibraries(ctx)
	if err != nil {
		s.log.Warn("list peers for the library failed", "err", err)
		return lib, nil
	}
	for _, res := range results {
		lib.Servers = append(lib.Servers, res.server)
		lib.Images = append(lib.Images, res.images...)
	}
	return lib, nil
}

// peerUsage asks every active peer which of its stack services run an image
// accepted by matches. Peers that cannot answer (including old ones) are
// returned as unreachable so the caller never guesses.
func (s *Service) peerUsage(ctx context.Context, matches func(imageref.Ref) bool) ([]UsedBy, []ServerInfo, error) {
	uses, unreachable := []UsedBy{}, []ServerInfo{}
	if s.peers == nil {
		return uses, unreachable, nil
	}
	views, err := s.activePeers(ctx)
	if err != nil {
		return nil, nil, err
	}
	type answer struct {
		usages []apitypes.LibraryUsage
		err    error
	}
	answers := make([]answer, len(views))
	var wg sync.WaitGroup
	for i, v := range views {
		wg.Add(1)
		go func() {
			defer wg.Done()
			answers[i].err = s.peerGet(ctx, v.ID, peerUsagePath, &answers[i].usages)
		}()
	}
	wg.Wait()
	for i, v := range views {
		if answers[i].err != nil {
			info := peerServer(v)
			info.Status, info.Error = peerFailure(v, answers[i].err)
			unreachable = append(unreachable, info)
			continue
		}
		for _, u := range answers[i].usages {
			if ref, ok := imageref.Parse(u.Ref); ok && matches(ref) {
				uses = append(uses, UsedBy{Server: v.ID, ServerName: v.Name, Stack: u.Stack, Service: u.Service})
			}
		}
	}
	return uses, unreachable, nil
}

// OwnImages is what /api/peer/images serves: this server's images with
// succeeded versions only. Logs, push targets and push state stay private.
func (s *Service) OwnImages(ctx context.Context) ([]LibraryEntry, error) {
	self, local, err := s.localLibrary(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]LibraryEntry, 0, len(local))
	for _, e := range local {
		versions := make([]Version, 0, len(e.Versions))
		for _, v := range e.Versions {
			if v.Status == VersionSucceeded {
				v.Error = ""
				versions = append(versions, v)
			}
		}
		if len(versions) == 0 {
			continue
		}
		e.Owner, e.Versions = self, versions
		e.PushTargets, e.Pushes = []PushTarget{}, map[string][]Push{}
		out = append(out, e)
	}
	return out, nil
}

// OwnUsage is what /api/peer/images/usage serves: every library reference
// this server's stacks run.
func (s *Service) OwnUsage(ctx context.Context) ([]imageref.Usage, error) {
	src := s.usageSource()
	if src == nil {
		return []imageref.Usage{}, nil
	}
	usages, err := src.LibraryUsage(ctx)
	if err != nil {
		return nil, err
	}
	if usages == nil {
		usages = []imageref.Usage{}
	}
	return usages, nil
}

// OpenVersionTar streams a succeeded version's image (docker save). It
// answers ErrImageNotFound or ErrVersionNotFound for anything else.
func (s *Service) OpenVersionTar(ctx context.Context, slug, tag string) (io.ReadCloser, string, error) {
	_, owner, err := s.selfInfo(ctx)
	if err != nil {
		return nil, "", err
	}
	img, err := s.store.Image(ctx, slug)
	if err != nil {
		return nil, "", err
	}
	v, err := s.store.Version(ctx, img.ID, tag)
	if err != nil {
		return nil, "", err
	}
	if v.Status != VersionSucceeded {
		return nil, "", ErrVersionNotFound
	}
	rc, err := s.eng.SaveImage(ctx, imageref.Format(owner, slug, tag))
	if errors.Is(err, engine.ErrImageNotFound) {
		return nil, "", &NotBuiltError{Slug: slug, Tag: tag}
	}
	if err != nil {
		return nil, "", err
	}
	return rc, v.ImageDigest, nil
}

// FindOwner finds the server whose instance ID starts with owner: this one or
// an active peer. It makes no network call.
func (s *Service) FindOwner(ctx context.Context, owner string) (ServerInfo, bool, error) {
	self, selfOwner, err := s.localServer(ctx)
	if err != nil {
		return ServerInfo{}, false, err
	}
	if owner == selfOwner {
		return self, true, nil
	}
	if s.peers == nil {
		return ServerInfo{}, false, nil
	}
	views, err := s.activePeers(ctx)
	if err != nil {
		return ServerInfo{}, false, err
	}
	for _, v := range views {
		if imageref.OwnerID(v.ID) == owner {
			info := peerServer(v)
			if v.Status != statusReachable {
				info.Status, info.Error = statusUnreachable, v.LastError
			}
			return info, true, nil
		}
	}
	return ServerInfo{}, false, nil
}
