// Package update defines the private managed-update contract.
package update

import (
	"errors"
	"time"
)

const (
	HelperProtocol    = 1
	BootstrapProtocol = 1
	JournalProtocol   = 1
	// StagingDirectory is relative to the platform-private updater root.
	StagingDirectory = "staging"
)

type InstallerKind string

const (
	InstallerScript InstallerKind = "script"
	InstallerDeb    InstallerKind = "deb"
	InstallerRPM    InstallerKind = "rpm"
	InstallerPkg    InstallerKind = "pkg"
)

type ReleaseIdentity struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}
type Identity struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}
type Manifest struct {
	SchemaVersion     int             `json:"schemaVersion"`
	Identity          ReleaseIdentity `json:"identity"`
	HelperMin         int             `json:"helperMin"`
	HelperMax         int             `json:"helperMax"`
	BootstrapProtocol int             `json:"bootstrapProtocol"`
	JournalProtocol   int             `json:"journalProtocol"`
	SourceSchemaMin   int             `json:"sourceSchemaMin"`
	TargetSchema      int             `json:"targetSchema"`
	Assets            []AssetSpec     `json:"assets"`
}
type AssetSpec struct {
	OS        string        `json:"os"`
	Arch      string        `json:"arch"`
	Installer InstallerKind `json:"installer"`
	Name      string        `json:"name"`
}
type VerifiedArtifact struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type Candidate struct {
	ID              string    `json:"id"`
	Identity        Identity  `json:"identity"`
	Manifest        Manifest  `json:"manifest"`
	MetadataSHA256  string    `json:"metadataSHA256"`
	ChecksumsSHA256 string    `json:"checksumsSHA256"`
	NotesURL        string    `json:"notesURL"`
	Asset           AssetSpec `json:"asset"`
	ExpiresAt       time.Time `json:"expiresAt"`
}
type SupervisorSpec struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	AdapterPath string `json:"adapterPath"`
}
type Instance struct {
	ID            string         `json:"id"`
	UID           uint32         `json:"uid"`
	GID           uint32         `json:"gid"`
	ConfigPath    string         `json:"configPath"`
	DataDir       string         `json:"dataDir"`
	ControlSocket string         `json:"controlSocket"`
	Supervisor    SupervisorSpec `json:"supervisor"`
}
type Registration struct {
	SchemaVersion int           `json:"schemaVersion"`
	ID            string        `json:"id"`
	Installer     InstallerKind `json:"installer"`
	Prefix        string        `json:"prefix"`
	Binary        string        `json:"binary"`
	Identity      Identity      `json:"identity"`
	Instances     []Instance    `json:"instances"`
	UpdateEnabled bool          `json:"updateEnabled"`
}
type ServiceState struct {
	InstanceID string `json:"instanceID"`
	Running    bool   `json:"running"`
	Enabled    bool   `json:"enabled"`
	Masked     bool   `json:"masked"`
	Loaded     bool   `json:"loaded"`
	PID        int    `json:"pid"`
}
type DBSnapshot struct {
	Size         int64  `json:"size"`
	Budget       int64  `json:"budget"`
	InstanceID   string `json:"instanceID"`
	Path         string `json:"path"`
	SHA256       string `json:"sha256"`
	ConfigSHA256 string `json:"configSHA256"`
	Schema       int    `json:"schema"`
}
type Plan struct {
	Registration     Registration     `json:"registration"`
	Current          Candidate        `json:"current"`
	Target           Candidate        `json:"target"`
	PreviousArtifact VerifiedArtifact `json:"previousArtifact"`
	TargetArtifact   VerifiedArtifact `json:"targetArtifact"`
	States           []ServiceState   `json:"states"`
}
type Probation struct {
	JobID    string   `json:"jobID"`
	Nonce    string   `json:"nonce"`
	Expected Identity `json:"expected"`
}
type Readiness struct {
	InstanceID   string   `json:"instanceID"`
	JobID        string   `json:"jobID"`
	Nonce        string   `json:"nonce"`
	ConfigSHA256 string   `json:"configSHA256"`
	Identity     Identity `json:"identity"`
	PID          int      `json:"pid"`
	Schema       int      `json:"schema"`
	Ready        bool     `json:"ready"`
}
type Phase string

const (
	PhaseAccepted          Phase = "accepted"
	PhaseResolving         Phase = "resolving"
	PhaseDownloading       Phase = "downloading"
	PhaseVerified          Phase = "verified"
	PhasePreflighted       Phase = "preflighted"
	PhaseQuiescing         Phase = "quiescing"
	PhaseStopped           Phase = "stopped"
	PhaseSnapshotted       Phase = "snapshotted"
	PhaseInstalling        Phase = "installing"
	PhaseStartingProbation Phase = "starting-probation"
	PhaseVerifying         Phase = "verifying"
	PhaseSucceeded         Phase = "succeeded"
	PhaseRollingBack       Phase = "rolling-back"
	PhaseRolledBack        Phase = "rolled-back"
	PhaseRecoveryRequired  Phase = "recovery-required"
	PhaseCancelled         Phase = "cancelled"
)

type Job struct {
	// RecoveryDecision is durable control state, independent of public diagnostics.
	RecoveryDecision RecoveryAction `json:"recoveryDecision,omitempty"`
	RecoveryStep     RecoveryStep   `json:"recoveryStep,omitempty"`
	ReleasePending   bool           `json:"releasePending,omitempty"`

	ID             string       `json:"id"`
	InstallationID string       `json:"installationID"`
	IdempotencyKey string       `json:"idempotencyKey"`
	Protocol       int          `json:"protocol"`
	Phase          Phase        `json:"phase"`
	Plan           Plan         `json:"plan"`
	WorkerSHA256   string       `json:"workerSHA256"`
	Probation      Probation    `json:"probation"`
	Snapshots      []DBSnapshot `json:"snapshots"`
	ErrorCode      string       `json:"errorCode"`
	ErrorMessage   string       `json:"errorMessage"`
	UpdatedAt      time.Time    `json:"updatedAt"`
}

var (
	ErrUnavailable      = errors.New("updater unavailable")
	ErrSetupRequired    = errors.New("updater setup required")
	ErrDisabled         = errors.New("updater disabled")
	ErrBusy             = errors.New("updater busy")
	ErrIncompatible     = errors.New("incompatible update")
	ErrCandidateExpired = errors.New("update candidate expired")
	ErrUnauthorized     = errors.New("unauthorized updater request")
	ErrRecoveryRequired = errors.New("updater recovery required")
)
