package update

import "time"

// Public responses never carry private paths, full jobs or probation nonces.
type BeginRequest struct {
	CandidateID    string `json:"candidateID"`
	IdempotencyKey string `json:"idempotencyKey"`
}
type JobView struct {
	ID             string    `json:"id"`
	Phase          Phase     `json:"phase"`
	CurrentVersion string    `json:"currentVersion"`
	TargetVersion  string    `json:"targetVersion"`
	ErrorCode      string    `json:"errorCode"`
	Message        string    `json:"message"`
	UpdatedAt      time.Time `json:"updatedAt"`
}
type UpdateStatus struct {
	Current    Identity      `json:"current"`
	Installer  InstallerKind `json:"installer"`
	Capability string        `json:"capability"`
	Reason     string        `json:"reason"`
	// SetupCommand is an installer-generated command sanitized at the API boundary.
	SetupCommand string         `json:"setupCommand"`
	Candidate    *CandidateView `json:"candidate,omitempty"`
	Job          *JobView       `json:"job,omitempty"`
}
type CandidateView struct {
	ID        string    `json:"id"`
	Version   string    `json:"version"`
	Commit    string    `json:"commit"`
	NotesURL  string    `json:"notesURL"`
	ExpiresAt time.Time `json:"expiresAt"`
}

const (
	CapabilityReady            = "ready"
	CapabilitySetupRequired    = "setup-required"
	CapabilityDisabled         = "disabled"
	CapabilityBusy             = "busy"
	CapabilityRecoveryRequired = "recovery-required"
)
