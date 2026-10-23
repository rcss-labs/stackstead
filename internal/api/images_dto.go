package api

import (
	"bytes"
	"errors"
	"strconv"
	"unicode/utf8"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/imageref"
	"github.com/rcss-labs/stackstead/internal/images"
)

// Domain-to-wire conversions for the image library. Every list and map is
// built non-nil: the web types are non-nullable.

func nonNilArgs(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func toLibraryServer(s images.ServerInfo) apitypes.LibraryServer {
	return apitypes.LibraryServer{ID: s.ID, Name: s.Name, Slug: s.Slug, Local: s.Local, Status: s.Status, Error: s.Error}
}

func toLibraryServers(list []images.ServerInfo) []apitypes.LibraryServer {
	out := make([]apitypes.LibraryServer, 0, len(list))
	for _, s := range list {
		out = append(out, toLibraryServer(s))
	}
	return out
}

func toPushTargets(list []images.PushTarget) []apitypes.PushTarget {
	out := make([]apitypes.PushTarget, 0, len(list))
	for _, t := range list {
		// Registry name and kind are filled in by the handlers from the registry list.
		out = append(out, apitypes.PushTarget{ID: t.ID, RegistryID: t.RegistryID, Repository: t.Repository, AutoPush: t.AutoPush})
	}
	return out
}

func toPushStates(pushes []images.Push, targets []images.PushTarget) []apitypes.PushState {
	out := make([]apitypes.PushState, 0, len(pushes))
	for _, p := range pushes {
		state := apitypes.PushState{Target: p.TargetID, Status: string(p.Status), Digest: p.Digest, Error: p.Error, UpdatedAt: p.UpdatedAt}
		for _, t := range targets {
			if t.ID == p.TargetID {
				state.Registry, state.Repository = t.RegistryID, t.Repository
			}
		}
		out = append(out, state)
	}
	return out
}

func toLibraryVersion(v images.Version, owner, slug string, pushes []images.Push, targets []images.PushTarget) apitypes.LibraryVersion {
	return apitypes.LibraryVersion{
		ID: v.ID, Tag: v.Tag, Status: string(v.Status), Revision: v.Revision, Commit: v.Commit,
		BuildArgs: nonNilArgs(v.BuildArgs), Arch: v.Arch, ImageID: v.ImageDigest, Size: v.Size,
		StartedAt: v.StartedAt, EndedAt: v.EndedAt, Error: v.Error, OperationID: v.OperationID,
		Ref: imageref.Format(owner, slug, v.Tag), Pushes: toPushStates(pushes, targets),
	}
}

func toLibraryVersions(versions []images.Version, owner, slug string, pushes map[string][]images.Push, targets []images.PushTarget) []apitypes.LibraryVersion {
	out := make([]apitypes.LibraryVersion, 0, len(versions))
	for _, v := range versions {
		out = append(out, toLibraryVersion(v, owner, slug, pushes[v.ID], targets))
	}
	return out
}

// latestAndLast picks the newest succeeded version and the newest version of
// any status from a newest-first list.
func latestAndLast(versions []apitypes.LibraryVersion) (latest, last *apitypes.LibraryVersion) {
	for i := range versions {
		if last == nil {
			last = &versions[i]
		}
		if latest == nil && versions[i].Status == string(images.VersionSucceeded) {
			latest = &versions[i]
		}
	}
	return latest, last
}

func toLibraryImageBase(img images.Image, owner images.ServerInfo, versions []images.Version, pushes map[string][]images.Push, targets []images.PushTarget) (apitypes.LibraryImage, []apitypes.LibraryVersion) {
	vs := toLibraryVersions(versions, imageref.OwnerID(owner.ID), img.Slug, pushes, targets)
	out := apitypes.LibraryImage{
		Slug: img.Slug, Name: img.Name, Source: string(img.Source), DockerfilePath: img.DockerfilePath,
		Owner: toLibraryServer(owner), BuildArgs: nonNilArgs(img.BuildArgs),
		Retention:   apitypes.Retention{KeepLast: img.Retention.KeepLast, KeepDays: img.Retention.KeepDays},
		PushTargets: toPushTargets(targets), CreatedAt: img.CreatedAt, UpdatedAt: img.UpdatedAt,
	}
	if img.Source == images.SourceGitHub {
		out.GitHub = &apitypes.GitHubSource{Repo: img.GitHub.Repo, Branch: img.GitHub.Branch, Path: img.GitHub.Path}
	}
	out.LatestVersion, out.LastBuild = latestAndLast(vs)
	return out, vs
}

func toLibraryImage(e images.LibraryEntry) apitypes.LibraryImage {
	out, _ := toLibraryImageBase(e.Image, e.Owner, e.Versions, e.Pushes, e.PushTargets)
	return out
}

func toLibrary(l images.Library) apitypes.Library {
	out := apitypes.Library{Images: make([]apitypes.LibraryImage, 0, len(l.Images)), Servers: toLibraryServers(l.Servers)}
	for _, e := range l.Images {
		out.Images = append(out.Images, toLibraryImage(e))
	}
	return out
}

func toLibraryImageDetail(d images.Detail, owner images.ServerInfo) apitypes.LibraryImageDetail {
	base, vs := toLibraryImageBase(d.Image, owner, d.Versions, d.Pushes, d.PushTargets)
	return apitypes.LibraryImageDetail{LibraryImage: base, Versions: vs, SuggestedTag: d.SuggestedTag, TagHint: d.TagHint, Arch: d.Arch, Revision: d.Revision}
}

func toVersionDetail(d images.VersionDetail, owner images.ServerInfo, slug string) apitypes.LibraryVersionDetail {
	return apitypes.LibraryVersionDetail{
		LibraryVersion: toLibraryVersion(d.Version, imageref.OwnerID(owner.ID), slug, nil, nil),
		Log:            d.Log, UsedBy: toLibraryUses(d.UsedBy),
	}
}

func toLibraryUse(u images.UsedBy) apitypes.LibraryUse {
	return apitypes.LibraryUse{Server: u.Server, ServerName: u.ServerName, Stack: u.Stack, Service: u.Service}
}

func toLibraryUses(list []images.UsedBy) []apitypes.LibraryUse {
	out := make([]apitypes.LibraryUse, 0, len(list))
	for _, u := range list {
		out = append(out, toLibraryUse(u))
	}
	return out
}

func toImageFiles(files []images.ImageFileInfo, revision int) apitypes.ImageFiles {
	out := apitypes.ImageFiles{Revision: revision, Files: make([]apitypes.ImageFile, 0, len(files))}
	for _, f := range files {
		out.Files = append(out.Files, apitypes.ImageFile{Path: f.Path, Size: f.Size})
	}
	return out
}

// toRevisionDTOs converts revisions (newest first). dockerfilePath names the
// file whose change DockerfileChanged reports; the oldest revision counts as
// changed.
func toRevisionDTOs(revs []images.Revision, dockerfilePath string) []apitypes.ImageRevision {
	out := make([]apitypes.ImageRevision, 0, len(revs))
	for i, r := range revs {
		changed := true
		if i+1 < len(revs) {
			changed = dockerfileHash(r, dockerfilePath) != dockerfileHash(revs[i+1], dockerfilePath)
		}
		out = append(out, apitypes.ImageRevision{Number: r.Number, CreatedAt: r.CreatedAt, Summary: r.Summary, DockerfileChanged: changed})
	}
	return out
}

func dockerfileHash(r images.Revision, dockerfilePath string) string {
	m, err := images.ParseManifest(r.ManifestJSON)
	if err != nil {
		return ""
	}
	return m[dockerfilePath].Hash
}

func revisionFiles(r images.Revision) []apitypes.ImageFile {
	out := []apitypes.ImageFile{}
	if m, err := images.ParseManifest(r.ManifestJSON); err == nil {
		for _, f := range m.Files() {
			out = append(out, apitypes.ImageFile{Path: f.Path, Size: f.Size})
		}
	}
	return out
}

func toServiceLibrary(ref string, r images.ResolvedRef) *apitypes.ServiceLibrary {
	return &apitypes.ServiceLibrary{Ref: ref, Owner: r.Owner, OwnerName: r.OwnerName, Image: r.Image, Tag: r.Tag,
		LatestTag: r.LatestTag, Arch: r.Arch, Newer: r.Newer, Known: r.Known}
}

// ---- requests ----

func toCreateRequest(r apitypes.CreateImageRequest) images.CreateRequest {
	out := images.CreateRequest{Name: r.Name, Source: images.Source(r.Source), DockerfilePath: r.DockerfilePath, BuildArgs: r.BuildArgs}
	if r.GitHub != nil {
		out.GitHub = images.GitHubSource{Repo: r.GitHub.Repo, Branch: r.GitHub.Branch, Path: r.GitHub.Path}
	}
	if r.Retention != nil {
		out.Retention = &images.Retention{KeepLast: r.Retention.KeepLast, KeepDays: r.Retention.KeepDays}
	}
	return out
}

func toUpdateRequest(r apitypes.UpdateImageRequest) images.UpdateRequest {
	out := images.UpdateRequest{Name: r.Name, DockerfilePath: r.DockerfilePath, BuildArgs: r.BuildArgs,
		Retention: images.Retention{KeepLast: r.Retention.KeepLast, KeepDays: r.Retention.KeepDays}}
	if r.GitHub != nil {
		out.GitHub = images.GitHubSource{Repo: r.GitHub.Repo, Branch: r.GitHub.Branch, Path: r.GitHub.Path}
	}
	return out
}

// ---- small helpers used by the handlers ----

var errNotText = errors.New("file is not text")

// textContent returns b as a string when it is valid UTF-8 without NUL bytes.
func textContent(b []byte) (string, error) {
	if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		return "", errNotText
	}
	return string(b), nil
}

func parseRevisionNumber(raw string) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, errors.New("revision must be a positive number")
	}
	return n, nil
}
