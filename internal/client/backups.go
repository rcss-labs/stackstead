package client

import (
	"context"
	"net/http"
	"strconv"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

// BackupRun starts a manual backup and returns its operation.
func (c *Client) BackupRun(ctx context.Context) (apitypes.Operation, error) {
	var out apitypes.BackupRunStarted
	_, err := c.authed(ctx, http.MethodPost, "/api/backups/run", struct{}{}, &out)
	return out.Operation, err
}

// BackupRuns returns the most recent backup runs, newest first.
func (c *Client) BackupRuns(ctx context.Context, limit int) ([]apitypes.BackupRun, error) {
	path := "/api/backups/runs"
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	var out []apitypes.BackupRun
	_, err := c.authed(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// BackupArchives lists the archives found at the configured destinations.
func (c *Client) BackupArchives(ctx context.Context) ([]apitypes.BackupArchive, error) {
	var out []apitypes.BackupArchive
	_, err := c.authed(ctx, http.MethodGet, "/api/backups/archives", nil, &out)
	return out, err
}
