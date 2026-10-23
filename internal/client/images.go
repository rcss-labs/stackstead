package client

import (
	"context"
	"net/http"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

// Images returns the merged library: this server's images and its peers'.
func (c *Client) Images(ctx context.Context) (apitypes.Library, error) {
	var out apitypes.Library
	_, err := c.authed(ctx, http.MethodGet, "/api/images", nil, &out)
	return out, err
}

func (c *Client) Image(ctx context.Context, slug string) (apitypes.LibraryImageDetail, error) {
	var out apitypes.LibraryImageDetail
	_, err := c.authed(ctx, http.MethodGet, "/api/images/"+esc(slug), nil, &out)
	return out, err
}

func (c *Client) BuildImage(ctx context.Context, slug string, req apitypes.BuildRequest) (apitypes.BuildStarted, error) {
	var out apitypes.BuildStarted
	_, err := c.authed(ctx, http.MethodPost, "/api/images/"+esc(slug)+"/builds", req, &out)
	return out, err
}

// PushImageVersion pushes a built version to the push target with the given ID.
func (c *Client) PushImageVersion(ctx context.Context, slug, tag, target string) (apitypes.Operation, error) {
	var out apitypes.Operation
	_, err := c.authed(ctx, http.MethodPost, "/api/images/"+esc(slug)+"/versions/"+esc(tag)+"/push", apitypes.PushRequest{Target: target}, &out)
	return out, err
}
