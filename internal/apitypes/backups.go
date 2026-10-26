package apitypes

import "time"

// Backup wire types. The web mirror lives in web/src/lib/api/backupTypes.ts
// and must change with this file.

type BackupSchedule struct {
	Enabled bool `json:"enabled"`
	// Frequency is daily or weekly; Weekday is 0 (Sunday) to 6 and is used for weekly.
	Frequency string `json:"frequency"`
	Weekday   int    `json:"weekday"`
	// Time is HH:MM in the host's time zone.
	Time string `json:"time"`
}

type BackupLocal struct {
	Enabled  bool   `json:"enabled"`
	Path     string `json:"path"`
	KeepLast int    `json:"keepLast"`
}

type BackupS3 struct {
	Enabled     bool   `json:"enabled"`
	Bucket      string `json:"bucket"`
	Prefix      string `json:"prefix"`
	Region      string `json:"region"`
	Endpoint    string `json:"endpoint"`
	PathStyle   bool   `json:"pathStyle"`
	AccessKeyID string `json:"accessKeyId"`
	// SecretKey is accepted on PUT and the S3 test and never returned; an
	// omitted or empty value keeps the stored secret.
	SecretKey string `json:"secretKey,omitempty"`
	// SecretKeySet reports whether a secret is stored.
	SecretKeySet bool `json:"secretKeySet"`
}

type BackupConfig struct {
	Schedule BackupSchedule `json:"schedule"`
	Local    BackupLocal    `json:"local"`
	S3       BackupS3       `json:"s3"`
	// NextRun is when the schedule fires next; absent when it is off. Ignored on PUT.
	NextRun *time.Time `json:"nextRun,omitempty"`
	// PausedAfterRestore is set when a host restore turned the schedule off;
	// saving the schedule enabled clears it. Ignored on PUT.
	PausedAfterRestore bool `json:"pausedAfterRestore"`
}

type BackupMount struct {
	// Kind is volume or bind; Name is the volume name or host path.
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Target string `json:"target"`
	// Bytes is -1 when the size is unknown.
	Bytes int64 `json:"bytes"`
}

// BackupStackSettings is what a backup of one stack includes.
type BackupStackSettings struct {
	Volumes    bool `json:"volumes"`
	Images     bool `json:"images"`
	StopDuring bool `json:"stopDuring"`
}

type BackupStack struct {
	Stack      string        `json:"stack"`
	Volumes    bool          `json:"volumes"`
	Images     bool          `json:"images"`
	StopDuring bool          `json:"stopDuring"`
	Mounts     []BackupMount `json:"mounts"`
}

type BackupItem struct {
	// Kind is base, volume, image, stack-files or upload; Status is ok, failed or skipped.
	Kind   string `json:"kind"`
	Stack  string `json:"stack,omitempty"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
}

type BackupRun struct {
	ID        int64      `json:"id"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	// Trigger is manual or schedule; Status is running, success, partial or failed.
	Trigger      string       `json:"trigger"`
	Status       string       `json:"status"`
	Archive      string       `json:"archive,omitempty"`
	Size         int64        `json:"size"`
	Destinations []string     `json:"destinations"`
	Items        []BackupItem `json:"items"`
	Error        string       `json:"error,omitempty"`
}

type BackupArchive struct {
	// Dest is local or s3.
	Dest      string    `json:"dest"`
	Name      string    `json:"name"`
	HostSlug  string    `json:"hostSlug"`
	CreatedAt time.Time `json:"createdAt"`
	Size      int64     `json:"size"`
}

// BackupRunStarted is the 202 body of POST /api/backups/run.
type BackupRunStarted struct {
	Operation Operation `json:"operation"`
}

// Whole-host restore from the setup screen (/api/setup/restore/...).

// HostRestoreS3 locates an archive in S3. Key is the full object key; the
// secret key is only ever sent, never returned.
type HostRestoreS3 struct {
	Bucket      string `json:"bucket"`
	Key         string `json:"key"`
	Region      string `json:"region"`
	Endpoint    string `json:"endpoint"`
	PathStyle   bool   `json:"pathStyle"`
	AccessKeyID string `json:"accessKeyId"`
	SecretKey   string `json:"secretKey,omitempty"`
}

// HostRestoreSource is where the archive comes from. Kind is s3, path or
// upload; Path is used for path, S3 for s3, and upload means the archive
// sent to POST /api/setup/restore/upload.
type HostRestoreSource struct {
	Kind string         `json:"kind"`
	Path string         `json:"path,omitempty"`
	S3   *HostRestoreS3 `json:"s3,omitempty"`
}

// HostRestoreUploaded is the body of a finished upload.
type HostRestoreUploaded struct {
	Source HostRestoreSource `json:"source"`
}

// HostRestoreInspectRequest asks what an archive holds and what it would
// overwrite. SetupToken is needed unless the request comes from this machine.
type HostRestoreInspectRequest struct {
	Source     HostRestoreSource `json:"source"`
	SetupToken string            `json:"setupToken"`
}

// HostRestoreStartRequest starts the restore. ConfirmedDigest is the
// conflictsDigest of the inspection the user confirmed.
type HostRestoreStartRequest struct {
	Source          HostRestoreSource `json:"source"`
	StartStacks     bool              `json:"startStacks"`
	ConfirmedDigest string            `json:"confirmedDigest"`
	SetupToken      string            `json:"setupToken"`
}

// HostRestoreStarted is the 202 body of POST /api/setup/restore/start.
// RestoreToken authorises GET /api/setup/restore/progress.
type HostRestoreStarted struct {
	RestoreToken string    `json:"restoreToken"`
	Operation    Operation `json:"operation"`
}

// HostRestoreConflict is existing state the restore meets. Kind is volume,
// network, container or stack-dir; Action is overwrite, recreate or keep.
type HostRestoreConflict struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Action string `json:"action"`
}

// HostRestoreSummary is the answer of POST /api/setup/restore/inspect.
type HostRestoreSummary struct {
	Manifest        BackupManifest        `json:"manifest"`
	Conflicts       []HostRestoreConflict `json:"conflicts"`
	ConflictsDigest string                `json:"conflictsDigest"`
}

// BackupManifest describes one archive.
type BackupManifest struct {
	Format            int                   `json:"format"`
	StacksteadVersion string                `json:"stacksteadVersion"`
	Host              string                `json:"host"`
	HostSlug          string                `json:"hostSlug"`
	InstanceID        string                `json:"instanceId"`
	CreatedAt         time.Time             `json:"createdAt"`
	Status            string                `json:"status"`
	Stacks            []BackupManifestStack `json:"stacks"`
	Networks          int                   `json:"networks"`
	Items             []BackupItem          `json:"items"`
}

type BackupManifestStack struct {
	Name        string                 `json:"name"`
	ComposePath string                 `json:"composePath"`
	Origin      string                 `json:"origin"`
	Project     string                 `json:"project"`
	WasRunning  bool                   `json:"wasRunning"`
	Networks    []string               `json:"networks,omitempty"`
	Volumes     []BackupManifestVolume `json:"volumes"`
	Images      []string               `json:"images"`
	LiveCopy    bool                   `json:"liveCopy"`
}

type BackupManifestVolume struct {
	Key    string `json:"key"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Target string `json:"target"`
	Bytes  int64  `json:"bytes"`
}
