// Package images keeps the image library: images built from uploaded or
// GitHub sources, their source revisions, tagged versions, and push targets.
// Domain types here are independent of the wire types in internal/apitypes.
package images

import (
	"errors"
	"time"

	"github.com/rcss-labs/stackstead/internal/stacks"
)

// Source says where an image's build context comes from.
type Source string

const (
	SourceUpload Source = "upload"
	SourceGitHub Source = "github"
)

// Retention limits how many versions of an image are kept; zero means no limit.
type Retention struct{ KeepLast, KeepDays int }

// GitHubSource locates a build context inside a GitHub repository.
type GitHubSource struct{ Repo, Branch, Path string }

// Image is a library image. Its slug is part of every reference and never changes.
type Image struct {
	ID, Slug, Name string
	Source         Source
	DockerfilePath string
	GitHub         GitHubSource
	BuildArgs      map[string]string
	Retention      Retention
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// VersionStatus is the state of one tagged build.
type VersionStatus string

const (
	VersionBuilding  VersionStatus = "building"
	VersionSucceeded VersionStatus = "succeeded"
	VersionFailed    VersionStatus = "failed"
)

// Version is one tagged build of an image.
type Version struct {
	ID, ImageID, Tag  string
	Status            VersionStatus
	Revision          int
	Commit            string
	BuildArgs         map[string]string
	Arch, ImageDigest string
	Size              int64
	StartedAt         time.Time
	EndedAt           *time.Time
	Error             string
	OperationID       string
}

// VersionResult is what a finished build records on its version.
type VersionResult struct {
	Status            VersionStatus
	Arch, ImageDigest string
	Size              int64
	Error             string
}

// Revision is a snapshot of an image's source manifest.
type Revision struct {
	ImageID      string
	Number       int
	CreatedAt    time.Time
	Summary      string
	ManifestJSON string
}

// PushTarget is a registry repository an image can be pushed to.
type PushTarget struct {
	ID, ImageID, RegistryID, Repository string
	AutoPush                            bool
}

// PushStatus is the state of pushing one version to one target.
type PushStatus string

const (
	PushPending PushStatus = "pending"
	PushPushing PushStatus = "pushing"
	PushPushed  PushStatus = "pushed"
	PushFailed  PushStatus = "failed"
)

// Push records the push of one version to one target.
type Push struct {
	VersionID, TargetID string
	Status              PushStatus
	Digest, Error       string
	UpdatedAt           time.Time
}

var (
	ErrImageExists      = errors.New("an image with this slug already exists")
	ErrImageNotFound    = errors.New("image not found")
	ErrVersionNotFound  = errors.New("version not found")
	ErrTagTaken         = errors.New("this tag is already used by another version")
	ErrBuildRunning     = errors.New("a build is already running for this image")
	ErrRevisionNotFound = errors.New("revision not found")
	ErrTargetNotFound   = errors.New("push target not found")
)

// ValidationError carries field errors; the API answers 400 with them.
type ValidationError = stacks.ValidationError

func fieldError(field, msg string) error {
	return &ValidationError{Fields: []stacks.FieldError{{Field: field, Message: msg}}}
}
