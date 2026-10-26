package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// helperLabel marks containers created for backup and restore so they can be told apart and swept.
const helperLabel = "dev.stackstead.backup-helper"

// waitHeaderWait is how long Docker may take to answer a container wait,
// which answers only once the container stops; ctx still bounds it.
const waitHeaderWait = 24 * time.Hour

// HelperMount is one mount of a helper container. Type is "volume" or "bind".
type HelperMount struct {
	Type, Source, Target string
	ReadOnly             bool
}

// HelperSpec describes a short-lived helper container with no network.
type HelperSpec struct {
	Image  string
	Mounts []HelperMount
	Cmd    []string
	Labels map[string]string
}

// CreateContainer creates (without starting) a helper container with no
// network, labelled as a backup helper, and returns its ID.
func (c *Client) CreateContainer(ctx context.Context, spec HelperSpec) (string, error) {
	mounts := make([]map[string]any, 0, len(spec.Mounts))
	for _, m := range spec.Mounts {
		mounts = append(mounts, map[string]any{"Type": m.Type, "Source": m.Source, "Target": m.Target, "ReadOnly": m.ReadOnly})
	}
	labels := map[string]string{}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	labels[helperLabel] = "true"
	payload := map[string]any{
		"Image":      spec.Image,
		"Cmd":        spec.Cmd,
		"Labels":     labels,
		"HostConfig": map[string]any{"NetworkMode": "none", "Mounts": mounts},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode helper container: %w", err)
	}
	resp, cancel, err := c.postStream(ctx, "/containers/create", bytes.NewReader(raw), "application/json", nil)
	if err != nil {
		return "", storageResult(err, ErrImageNotFound)
	}
	defer cancel()
	defer resp.Body.Close()
	var result struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil || result.ID == "" {
		return "", errors.New("docker engine: container create returned no ID")
	}
	return result.ID, nil
}

// WaitContainer blocks until the container stops and returns its exit code.
func (c *Client) WaitContainer(ctx context.Context, id string) (int, error) {
	resp, cancel, err := c.openRequest(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/wait?condition=not-running", nil, "", nil, waitHeaderWait, true)
	if err != nil {
		return 0, actionResult(err)
	}
	defer cancel()
	defer resp.Body.Close()
	var result struct {
		StatusCode int
		Error      *struct{ Message string }
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return 0, c.streamResult(ctx, err)
	}
	if result.Error != nil && result.Error.Message != "" {
		return result.StatusCode, fmt.Errorf("container wait: %s", result.Error.Message)
	}
	return result.StatusCode, nil
}

// RemoveContainer removes a container; force also kills a running one.
func (c *Client) RemoveContainer(ctx context.Context, id string, force bool) error {
	path := "/containers/" + url.PathEscape(id)
	if force {
		path += "?force=1"
	}
	resp, cancel, err := c.openRequest(ctx, http.MethodDelete, path, nil, "", nil, c.timeout, true)
	if err != nil {
		return actionResult(err)
	}
	defer cancel()
	resp.Body.Close()
	return nil
}

// GetArchive streams the tar of path inside the container. The body is bound
// by ctx only; the caller must close it.
func (c *Client) GetArchive(ctx context.Context, id, path string) (io.ReadCloser, error) {
	resp, cancel, err := c.openRequest(ctx, http.MethodGet, archivePath(id, path), nil, "", nil, c.timeout, true)
	if err != nil {
		return nil, actionResult(err)
	}
	return cancelOnClose{resp.Body, cancel}, nil
}

// PutArchive extracts a tar stream into path inside the container.
func (c *Client) PutArchive(ctx context.Context, id, path string, tar io.Reader) error {
	resp, cancel, err := c.openRequest(ctx, http.MethodPut, archivePath(id, path), tar, "application/x-tar", nil, loadHeaderWait, true)
	if err != nil {
		return actionResult(err)
	}
	defer cancel()
	resp.Body.Close()
	return nil
}

func archivePath(id, path string) string {
	return "/containers/" + url.PathEscape(id) + "/archive?" + url.Values{"path": {path}}.Encode()
}

// PullImage pulls ref (a tag, digest or both), passing progress lines to out (which may be nil).
func (c *Client) PullImage(ctx context.Context, ref string, out func(string)) error {
	resp, cancel, err := c.postStream(ctx, "/images/create?"+url.Values{"fromImage": {ref}}.Encode(), nil, "", nil)
	if err != nil {
		return storageResult(err, ErrImageNotFound)
	}
	defer cancel()
	defer resp.Body.Close()
	err = decodeJSONStream(resp.Body, func(m jsonMessage) error {
		if msg := m.errorText(); msg != "" {
			return fmt.Errorf("pull failed: %s", msg)
		}
		if m.Status != "" && out != nil {
			line := m.Status
			if m.Progress != "" {
				line += " " + m.Progress
			}
			if m.ID != "" {
				line = m.ID + ": " + line
			}
			out(line)
		}
		return nil
	})
	if err != nil {
		if strings.HasPrefix(err.Error(), "pull failed: ") {
			return err
		}
		return c.streamResult(ctx, err)
	}
	return nil
}

// CreateVolume creates a named volume with labels; an existing volume of that name is left as is.
func (c *Client) CreateVolume(ctx context.Context, name string, labels map[string]string) error {
	raw, err := json.Marshal(map[string]any{"Name": name, "Labels": labels})
	if err != nil {
		return fmt.Errorf("encode volume: %w", err)
	}
	resp, cancel, err := c.postStream(ctx, "/volumes/create", bytes.NewReader(raw), "application/json", nil)
	if err != nil {
		return err
	}
	defer cancel()
	resp.Body.Close()
	return nil
}
