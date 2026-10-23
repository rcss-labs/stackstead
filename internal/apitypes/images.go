package apitypes

import "time"

// Image library wire types. The web mirror lives in web/src/lib/api/imageTypes.ts
// and must change with this file.

type Retention struct {
	// KeepLast and KeepDays are 0 for unlimited.
	KeepLast int `json:"keepLast"`
	KeepDays int `json:"keepDays"`
}

type GitHubSource struct {
	// Repo is "owner/name"; Path "" means the repository root.
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Path   string `json:"path"`
}

type LibraryServer struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	Local bool   `json:"local"`
	// Status is reachable, unreachable or old.
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type PushTarget struct {
	ID           string `json:"id"`
	RegistryID   string `json:"registryId"`
	RegistryName string `json:"registryName"`
	Kind         string `json:"kind"`
	Repository   string `json:"repository"`
	AutoPush     bool   `json:"autoPush"`
}

type PushState struct {
	Target     string    `json:"target"`
	Registry   string    `json:"registry"`
	Repository string    `json:"repository"`
	Status     string    `json:"status"`
	Digest     string    `json:"digest"`
	Error      string    `json:"error"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type LibraryVersion struct {
	ID          string            `json:"id"`
	Tag         string            `json:"tag"`
	Status      string            `json:"status"`
	Revision    int               `json:"revision"`
	Commit      string            `json:"commit"`
	BuildArgs   map[string]string `json:"buildArgs"`
	Arch        string            `json:"arch"`
	ImageID     string            `json:"imageId"`
	Size        int64             `json:"size"`
	StartedAt   time.Time         `json:"startedAt"`
	EndedAt     *time.Time        `json:"endedAt,omitempty"`
	Error       string            `json:"error"`
	OperationID string            `json:"operationId"`
	Ref         string            `json:"ref"`
	Pushes      []PushState       `json:"pushes"`
}

type LibraryImage struct {
	Slug           string            `json:"slug"`
	Name           string            `json:"name"`
	Source         string            `json:"source"`
	DockerfilePath string            `json:"dockerfilePath"`
	Owner          LibraryServer     `json:"owner"`
	GitHub         *GitHubSource     `json:"github,omitempty"`
	BuildArgs      map[string]string `json:"buildArgs"`
	Retention      Retention         `json:"retention"`
	LatestVersion  *LibraryVersion   `json:"latestVersion,omitempty"`
	LastBuild      *LibraryVersion   `json:"lastBuild,omitempty"`
	PushTargets    []PushTarget      `json:"pushTargets"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
}

type Library struct {
	Images  []LibraryImage  `json:"images"`
	Servers []LibraryServer `json:"servers"`
}

type LibraryImageDetail struct {
	LibraryImage
	Versions     []LibraryVersion `json:"versions"`
	SuggestedTag string           `json:"suggestedTag"`
	TagHint      string           `json:"tagHint"`
	Arch         string           `json:"arch"`
	Revision     int              `json:"revision"`
}

type LibraryUse struct {
	Server     string `json:"server"`
	ServerName string `json:"serverName"`
	Stack      string `json:"stack"`
	Service    string `json:"service"`
}

type LibraryVersionDetail struct {
	LibraryVersion
	Log    string       `json:"log"`
	UsedBy []LibraryUse `json:"usedBy"`
}

type LibraryUsage struct {
	Ref     string `json:"ref"`
	Stack   string `json:"stack"`
	Service string `json:"service"`
}

type ImageFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type ImageFiles struct {
	Revision int         `json:"revision"`
	Files    []ImageFile `json:"files"`
}

type ImageFileContent struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Revision int    `json:"revision"`
}

// ImageDockerfile is both the PUT body and the GET answer.
type ImageDockerfile struct {
	Content  string `json:"content"`
	Revision int    `json:"revision"`
}

type ImageRevision struct {
	Number            int       `json:"number"`
	CreatedAt         time.Time `json:"createdAt"`
	Summary           string    `json:"summary"`
	DockerfileChanged bool      `json:"dockerfileChanged"`
}

type ImageRevisionDetail struct {
	ImageRevision
	Dockerfile string      `json:"dockerfile"`
	Previous   string      `json:"previous"`
	Files      []ImageFile `json:"files"`
}

type CreateImageRequest struct {
	Name           string            `json:"name"`
	Source         string            `json:"source"`
	DockerfilePath string            `json:"dockerfilePath"`
	GitHub         *GitHubSource     `json:"github,omitempty"`
	BuildArgs      map[string]string `json:"buildArgs"`
	Retention      *Retention        `json:"retention,omitempty"`
}

type UpdateImageRequest struct {
	Name           string            `json:"name"`
	DockerfilePath string            `json:"dockerfilePath"`
	GitHub         *GitHubSource     `json:"github,omitempty"`
	BuildArgs      map[string]string `json:"buildArgs"`
	Retention      Retention         `json:"retention"`
}

type RenameFileRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type BuildRequest struct {
	Tag       string            `json:"tag"`
	BuildArgs map[string]string `json:"buildArgs"`
	SaveArgs  bool              `json:"saveArgs"`
	Ref       string            `json:"ref"`
}

type BuildStarted struct {
	Version   LibraryVersion `json:"version"`
	Operation Operation      `json:"operation"`
}

type PushRequest struct {
	Target string `json:"target"`
}

type PushTargetRequest struct {
	RegistryID string `json:"registryId"`
	Repository string `json:"repository"`
	AutoPush   bool   `json:"autoPush"`
}

type FetchRequest struct {
	Ref string `json:"ref"`
}

// ServiceLibrary is the library badge on a service whose image is a library reference.
type ServiceLibrary struct {
	Ref       string `json:"ref"`
	Owner     string `json:"owner"`
	OwnerName string `json:"ownerName"`
	Image     string `json:"image"`
	Tag       string `json:"tag"`
	LatestTag string `json:"latestTag"`
	Arch      string `json:"arch"`
	Newer     bool   `json:"newer"`
	Known     bool   `json:"known"`
}

type GitHubHead struct {
	Commit     string `json:"commit"`
	Dockerfile string `json:"dockerfile"`
	Path       string `json:"path"`
}

type GitHubSettings struct {
	Configured bool   `json:"configured"`
	Login      string `json:"login"`
}

type GitHubTokenRequest struct {
	Token string `json:"token"`
}

type GitHubTest struct {
	OK      bool   `json:"ok"`
	Login   string `json:"login"`
	Message string `json:"message"`
}

// RegistryHub carries the Token write-only; answers never include it.
type RegistryHub struct {
	Username string `json:"username"`
	Token    string `json:"token"`
}

// RegistryECR secrets are write-only; answers never include them.
type RegistryECR struct {
	Region          string `json:"region"`
	Mode            string `json:"mode"`
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken"`
	Profile         string `json:"profile"`
}

type RegistryRequest struct {
	Name string       `json:"name"`
	Kind string       `json:"kind"`
	Hub  *RegistryHub `json:"hub,omitempty"`
	ECR  *RegistryECR `json:"ecr,omitempty"`
}

type RegistryView struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Kind            string     `json:"kind"`
	Host            string     `json:"host"`
	Username        string     `json:"username"`
	Region          string     `json:"region"`
	Mode            string     `json:"mode"`
	Profile         string     `json:"profile"`
	AccountID       string     `json:"accountId"`
	AccessKeyID     string     `json:"accessKeyId,omitempty"`
	Configured      bool       `json:"configured"`
	LastTestAt      *time.Time `json:"lastTestAt,omitempty"`
	LastTestOK      bool       `json:"lastTestOk"`
	LastTestMessage string     `json:"lastTestMessage"`
}

// RegistryOption is the slim registry entry of the push-target picker.
type RegistryOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Host string `json:"host"`
}

type RegistryTest struct {
	OK      bool   `json:"ok"`
	Host    string `json:"host"`
	Message string `json:"message"`
}

type ImageDefaults struct {
	Retention Retention `json:"retention"`
}
