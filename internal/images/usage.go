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

// UsedBy names one stack service running a version.
type UsedBy struct{ Server, ServerName, Stack, Service string }

// InUseError says why a version or image cannot be deleted.
type InUseError struct {
	UsedBy      []UsedBy
	Unreachable []ServerInfo
}

func (e *InUseError) Error() string {
	if len(e.UsedBy) > 0 {
		parts := make([]string, len(e.UsedBy))
		for i, u := range e.UsedBy {
			parts[i] = fmt.Sprintf("%s/%s on %s", u.Stack, u.Service, u.ServerName)
		}
		return "in use by " + strings.Join(parts, ", ")
	}
	names := make([]string, len(e.Unreachable))
	for i, s := range e.Unreachable {
		names[i] = s.Name
	}
	return "cannot confirm use: " + strings.Join(names, ", ") + " could not be reached"
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

// usedBy lists the stack services running owner/slug:tag (any tag when empty).
func (s *Service) usedBy(ctx context.Context, owner, slug, tag string) ([]UsedBy, error) {
	out := []UsedBy{}
	src := s.usageSource()
	if src == nil {
		return out, nil
	}
	me, _, err := s.selfInfo(ctx)
	if err != nil {
		return nil, err
	}
	usages, err := src.LibraryUsage(ctx)
	if err != nil {
		return nil, fmt.Errorf("read library usage: %w", err)
	}
	for _, u := range usages {
		if u.Ref.Owner == owner && u.Ref.Slug == slug && (tag == "" || u.Ref.Tag == tag) {
			out = append(out, UsedBy{Server: owner, ServerName: me.Name, Stack: u.Stack, Service: u.Service})
		}
	}
	return out, nil
}

func (s *Service) checkUnused(ctx context.Context, owner, slug, tag string) error {
	used, err := s.usedBy(ctx, owner, slug, tag)
	if err != nil {
		return err
	}
	if len(used) > 0 {
		return &InUseError{UsedBy: used, Unreachable: []ServerInfo{}}
	}
	return nil
}

// ---- library views ----

// ServerInfo describes a server whose images the library shows. ID is the
// owner id used in references (first 12 characters of the instance ID).
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
	return ServerInfo{ID: owner, Name: me.Name, Slug: me.Slug, Local: true, Status: "reachable"}, owner, nil
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

// Library lists this server's images; Servers holds only this server.
func (s *Service) Library(ctx context.Context) (Library, error) {
	self, _, err := s.localServer(ctx)
	if err != nil {
		return Library{}, err
	}
	imgs, err := s.store.Images(ctx)
	if err != nil {
		return Library{}, err
	}
	lib := Library{Images: make([]LibraryEntry, 0, len(imgs)), Servers: []ServerInfo{self}}
	for _, img := range imgs {
		e, err := s.entryOf(ctx, img, self)
		if err != nil {
			return Library{}, err
		}
		lib.Images = append(lib.Images, e)
	}
	return lib, nil
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

// Resolve describes the given references; only this server's images are known.
func (s *Service) Resolve(ctx context.Context, refs []string) (map[string]ResolvedRef, error) {
	out := make(map[string]ResolvedRef, len(refs))
	self, owner, err := s.localServer(ctx)
	if err != nil {
		return nil, err
	}
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

// EnsureLocal checks that the version is built and present in Docker here.
// Fetching from peers arrives with a later milestone.
func (s *Service) EnsureLocal(ctx context.Context, ref imageref.Ref, out func(string)) error {
	_, owner, err := s.selfInfo(ctx)
	if err != nil {
		return err
	}
	notBuilt := &NotBuiltError{Slug: ref.Slug, Tag: ref.Tag}
	if ref.Owner != owner {
		return notBuilt
	}
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
