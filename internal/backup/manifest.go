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
}

// ManifestStack describes one stack held by the archive.
type ManifestStack struct {
	Name        string           `json:"name"`
	ComposePath string           `json:"composePath"`
	Origin      string           `json:"origin"`
	Project     string           `json:"project"`
	WasRunning  bool             `json:"wasRunning"`
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
