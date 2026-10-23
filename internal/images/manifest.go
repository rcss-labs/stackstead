package images

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// FileEntry is one source file: its blob, size and mode (0644 or 0755).
type FileEntry struct {
	Hash string `json:"hash"`
	Size int64  `json:"size"`
	Mode uint32 `json:"mode"`
}

// Manifest maps clean slash paths to their files.
type Manifest map[string]FileEntry

// ImageFileInfo is a file as listed to clients.
type ImageFileInfo struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// ParseManifest reads the JSON written by Manifest.JSON. An empty string is an
// empty manifest. Every key and entry is validated.
func ParseManifest(s string) (Manifest, error) {
	m := Manifest{}
	if strings.TrimSpace(s) == "" {
		return m, nil
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if m == nil {
		m = Manifest{}
	}
	for p, e := range m {
		if clean, err := ValidatePath(p); err != nil || clean != p {
			return nil, fmt.Errorf("manifest: %w: %q", ErrBadPath, p)
		}
		if !blobHashValid(e.Hash) || e.Size < 0 || (e.Mode != 0o644 && e.Mode != 0o755) {
			return nil, fmt.Errorf("manifest: invalid entry for %q", p)
		}
	}
	return m, nil
}

// JSON is the manifest with keys sorted, so equal manifests serialize equally.
func (m Manifest) JSON() string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if m == nil {
		m = Manifest{}
	}
	if err := enc.Encode(map[string]FileEntry(m)); err != nil {
		return "{}"
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// Files lists the manifest's files sorted by path.
func (m Manifest) Files() []ImageFileInfo {
	out := make([]ImageFileInfo, 0, len(m))
	for p, e := range m {
		out = append(out, ImageFileInfo{Path: p, Size: e.Size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func (m Manifest) clone() Manifest {
	c := make(Manifest, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func (m Manifest) totalBytes() int64 {
	var n int64
	for _, e := range m {
		n += e.Size
	}
	return n
}
