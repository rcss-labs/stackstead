package images

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/imageref"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// UsageSource lists the stack services that run library images.
type UsageSource interface {
	LibraryUsage(ctx context.Context) ([]imageref.Usage, error)
}

var _ UsageSource = (*stacks.Service)(nil)

// UsedBy names one stack service running a version. Server is the full instance ID.
type UsedBy struct{ Server, ServerName, Stack, Service string }

// InUseError says why a version or image cannot be deleted.
type InUseError struct {
	UsedBy      []UsedBy
	Unreachable []ServerInfo
}

func (e *InUseError) Error() string {
	var parts []string
	if len(e.UsedBy) > 0 {
		uses := make([]string, len(e.UsedBy))
		for i, u := range e.UsedBy {
			uses[i] = fmt.Sprintf("%s/%s on %s", u.Stack, u.Service, u.ServerName)
		}
		parts = append(parts, "in use by "+strings.Join(uses, ", "))
	}
	if len(e.Unreachable) > 0 {
		names := make([]string, len(e.Unreachable))
		for i, s := range e.Unreachable {
			names[i] = s.Name
		}
		parts = append(parts, "cannot confirm use: "+strings.Join(names, ", ")+" could not be asked")
	}
	return strings.Join(parts, "; ")
}

// ErrImageNotBuilt is matched by errors for versions missing on this server.
var ErrImageNotBuilt = errors.New("image is not built on this server")

// NotBuiltError says a version is not present in Docker on this server.
type NotBuiltError struct{ Slug, Tag string }

func (e *NotBuiltError) Error() string {
	return fmt.Sprintf("version %s of %s is not built on this server", e.Tag, e.Slug)
}

func (e *NotBuiltError) Is(target error) bool { return target == ErrImageNotBuilt }

func (s *Service) usageSource() UsageSource {
	s.usageMu.RLock()
	defer s.usageMu.RUnlock()
	return s.usage
}

// usedBy lists the stack services running ref (any tag when ref.Tag is
// empty) on this server and on every active peer. Peers that cannot be asked
// are returned in unreachable: the caller must not treat them as "unused".
func (s *Service) usedBy(ctx context.Context, ref imageref.Ref) (uses []UsedBy, unreachable []ServerInfo, err error) {
	uses, unreachable = []UsedBy{}, []ServerInfo{}
	matches := func(r imageref.Ref) bool {
		return r.Owner == ref.Owner && r.Slug == ref.Slug && (ref.Tag == "" || r.Tag == ref.Tag)
	}
	if src := s.usageSource(); src != nil {
		me, _, err := s.selfInfo(ctx)
		if err != nil {
			return nil, nil, err
		}
		usages, err := src.LibraryUsage(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("read library usage: %w", err)
		}
		for _, u := range usages {
			if matches(u.Ref) {
				uses = append(uses, UsedBy{Server: me.InstanceID, ServerName: me.Name, Stack: u.Stack, Service: u.Service})
			}
		}
	}
	peerUses, unreachable, err := s.peerUsage(ctx, matches)
	if err != nil {
		return nil, nil, err
	}
	return append(uses, peerUses...), unreachable, nil
}

// checkUnused refuses with *InUseError when ref is in use anywhere or when
// that cannot be established.
func (s *Service) checkUnused(ctx context.Context, ref imageref.Ref) error {
	used, unreachable, err := s.usedBy(ctx, ref)
	if err != nil {
		return err
	}
	if len(used) > 0 || len(unreachable) > 0 {
		return &InUseError{UsedBy: used, Unreachable: unreachable}
	}
	return nil
}

// ---- library views ----

// ServerInfo describes a server whose images the library shows. ID is the
// full instance ID; derive the reference owner with imageref.OwnerID.
type ServerInfo struct {
	ID, Name, Slug string
	Local          bool
	Status, Error  string // Status: reachable | unreachable | old
}

// LibraryEntry is one image with its versions and push state.
type LibraryEntry struct {
	Image
	Owner       ServerInfo
	Versions    []Version
	PushTargets []PushTarget
	Pushes      map[string][]Push
}

// Library is every image known to this server and the servers it came from.
type Library struct {
	Images  []LibraryEntry
	Servers []ServerInfo
}

func (s *Service) localServer(ctx context.Context) (ServerInfo, string, error) {
	me, owner, err := s.selfInfo(ctx)
	if err != nil {
		return ServerInfo{}, "", err
	}
	return ServerInfo{ID: me.InstanceID, Name: me.Name, Slug: me.Slug, Local: true, Status: "reachable"}, owner, nil
}

func (s *Service) entryOf(ctx context.Context, img Image, owner ServerInfo) (LibraryEntry, error) {
	versions, err := s.store.Versions(ctx, img.ID)
	if err != nil {
		return LibraryEntry{}, err
	}
	targets, err := s.store.PushTargets(ctx, img.ID)
	if err != nil {
		return LibraryEntry{}, err
	}
	ids := make([]string, len(versions))
	for i, v := range versions {
		ids[i] = v.ID
	}
	pushes, err := s.store.Pushes(ctx, ids)
	if err != nil {
		return LibraryEntry{}, err
	}
	return LibraryEntry{Image: img, Owner: owner, Versions: versions, PushTargets: targets, Pushes: pushes}, nil
}

// localLibrary lists this server's images.
func (s *Service) localLibrary(ctx context.Context) (ServerInfo, []LibraryEntry, error) {
	self, _, err := s.localServer(ctx)
	if err != nil {
		return ServerInfo{}, nil, err
	}
	imgs, err := s.store.Images(ctx)
	if err != nil {
		return ServerInfo{}, nil, err
	}
	entries := make([]LibraryEntry, 0, len(imgs))
	for _, img := range imgs {
		e, err := s.entryOf(ctx, img, self)
		if err != nil {
			return ServerInfo{}, nil, err
		}
		entries = append(entries, e)
	}
	return self, entries, nil
}

// Detail is the image screen: versions, pushes, source revision and tag hint.
type Detail struct {
	Image
	Versions                    []Version
	Pushes                      map[string][]Push
	PushTargets                 []PushTarget
	Revision                    int
	SuggestedTag, TagHint, Arch string
}

// Detail returns everything the image screen shows.
func (s *Service) Detail(ctx context.Context, slug string) (Detail, error) {
	img, err := s.store.Image(ctx, slug)
	if err != nil {
		return Detail{}, err
	}
	self, _, err := s.localServer(ctx)
	if err != nil {
		return Detail{}, err
	}
	e, err := s.entryOf(ctx, img, self)
	if err != nil {
		return Detail{}, err
	}
	_, rev, err := s.manifestOf(ctx, img)
	if err != nil {
		return Detail{}, err
	}
	d := Detail{Image: img, Versions: e.Versions, Pushes: e.Pushes, PushTargets: e.PushTargets, Revision: rev}
	latest := ""
	for _, v := range e.Versions { // newest first; failed tags are free to reuse
		if v.Status != VersionFailed {
			latest = v.Tag
			break
		}
	}
	d.SuggestedTag, d.TagHint = SuggestTag(latest)
	if s.eng != nil {
		if arch, err := s.eng.Arch(ctx); err == nil {
			d.Arch = arch
		}
	}
	return d, nil
}

// ---- resolving ----

// ResolvedRef describes a library reference found in a compose file.
type ResolvedRef struct {
	Known                                         bool
	Owner, OwnerName, Image, Tag, LatestTag, Arch string
	Newer                                         bool
}

// Resolve describes the given references. This server's images come from the
// database; peer-owned ones from the cached peer libraries, which never makes
// it wait on a peer.
func (s *Service) Resolve(ctx context.Context, refs []string) (map[string]ResolvedRef, error) {
	out := make(map[string]ResolvedRef, len(refs))
	self, owner, err := s.localServer(ctx)
	if err != nil {
		return nil, err
	}
	var cached []peerResult
	loaded := false
	for _, raw := range refs {
		ref, ok := imageref.Parse(raw)
		if !ok {
			continue
		}
		r := ResolvedRef{Owner: ref.Owner, Image: ref.Slug, Tag: ref.Tag}
		if ref.Owner == owner {
			r.OwnerName = self.Name
			if err := s.resolveLocal(ctx, ref, &r); err != nil {
				return nil, err
			}
		} else {
			if !loaded {
				cached, loaded = s.cachedPeerLibraries(ctx), true
			}
			resolvePeerRef(cached, ref, &r)
		}
		out[raw] = r
	}
	return out, nil
}

func (s *Service) resolveLocal(ctx context.Context, ref imageref.Ref, r *ResolvedRef) error {
	img, err := s.store.Image(ctx, ref.Slug)
	if errors.Is(err, ErrImageNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	v, err := s.store.Version(ctx, img.ID, ref.Tag)
	if errors.Is(err, ErrVersionNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	r.Known, r.Arch = true, v.Arch
	versions, err := s.store.Versions(ctx, img.ID)
	if err != nil {
		return err
	}
	for _, other := range versions { // newest first
		if other.Status != VersionSucceeded {
			continue
		}
		r.LatestTag = other.Tag
		r.Newer = other.ID != v.ID
		break
	}
	return nil
}

// ensureOwn checks that a version this server owns is built and present in Docker.
func (s *Service) ensureOwn(ctx context.Context, ref imageref.Ref) error {
	notBuilt := &NotBuiltError{Slug: ref.Slug, Tag: ref.Tag}
	img, err := s.store.Image(ctx, ref.Slug)
	if errors.Is(err, ErrImageNotFound) {
		return notBuilt
	}
	if err != nil {
		return err
	}
	v, err := s.store.Version(ctx, img.ID, ref.Tag)
	if errors.Is(err, ErrVersionNotFound) || (err == nil && v.Status != VersionSucceeded) {
		return notBuilt
	}
	if err != nil {
		return err
	}
	if _, err := s.eng.InspectImage(ctx, ref.String()); err != nil {
		if errors.Is(err, engine.ErrImageNotFound) {
			return notBuilt
		}
		return fmt.Errorf("inspect %s: %w", ref, err)
	}
	return nil
}
