// Package backup implements Stackstead's backup archives and runs.
package backup

import "time"

// ManifestFormat is the archive format version written into every manifest.
const ManifestFormat = 1

// ManifestEntryName is the archive entry that holds the manifest. It is always last.
const ManifestEntryName = "manifest.json"

// ItemResult records the outcome of one backed-up item.
type ItemResult struct {
	// Kind is "base", "volume", "image", "stack-files" or "upload".
	Kind  string `json:"kind"`
	Stack string `json:"stack,omitempty"`
	Name  string `json:"name,omitempty"`
	// Status is "ok", "failed" or "skipped".
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
}

// Manifest describes the contents of one archive. It is the last archive entry
// and is also written as a sidecar file next to the archive.
type Manifest struct {
	Format            int             `json:"format"`
	StacksteadVersion string          `json:"stacksteadVersion"`
	Host              string          `json:"host"`
	HostSlug          string          `json:"hostSlug"`
	InstanceID        string          `json:"instanceId"`
	CreatedAt         time.Time       `json:"createdAt"`
	Status            string          `json:"status"`
	Stacks            []ManifestStack `json:"stacks"`
	Networks          int             `json:"networks"`
	Items             []ItemResult    `json:"items"`
	// Encryption is set when the archive stream is encrypted. Any new Manifest
	// field needs a ManifestFormat bump: the sidecar MAC covers the JSON form.
	Encryption *ManifestEncryption `json:"encryption,omitempty"`
}

// ManifestEncryption describes how an encrypted archive was keyed. The
// passphrase itself is never recorded; KeyID tells which one an archive needs.
type ManifestEncryption struct {
	KeyID   string `json:"keyId"`
	KeySalt string `json:"keySalt"` // hex
	Argon2  struct {
		Time      uint32 `json:"time"`
		MemoryKiB uint32 `json:"memoryKiB"`
		Threads   uint8  `json:"threads"`
	} `json:"argon2"`
	// SidecarMAC authenticates the sidecar copy of the manifest. It is empty in
	// the in-archive manifest and is the only field excluded from the MAC.
	SidecarMAC string `json:"sidecarMac,omitempty"`
}

// ManifestStack describes one stack held by the archive.
type ManifestStack struct {
	Name        string           `json:"name"`
	ComposePath string           `json:"composePath"`
	Origin      string           `json:"origin"`
	Project     string           `json:"project"`
	WasRunning  bool             `json:"wasRunning"`
	Networks    []string         `json:"networks,omitempty"`
	Volumes     []ManifestVolume `json:"volumes"`
	Images      []string         `json:"images"`
	LiveCopy    bool             `json:"liveCopy"`
}

// ManifestVolume describes one volume or bind mount of a stack.
type ManifestVolume struct {
	// Key is a path-safe id used in the archive path volumes/<stack>/<key>/.
	Key string `json:"key"`
	// Kind is "volume" or "bind".
	Kind string `json:"kind"`
	// Name is the docker volume name or the host path.
	Name   string `json:"name"`
	Target string `json:"target"`
	Bytes  int64  `json:"bytes"`
}
