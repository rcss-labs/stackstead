package client

import (
	"context"
	"errors"
	"testing"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

func TestImagesClientRequests(t *testing.T) {
	ctx := context.Background()
	t.Run("library", func(t *testing.T) {
		c, got := newTestClient(t, jsonReply(200, apitypes.Library{Images: []apitypes.LibraryImage{{Slug: "web"}}}))
		out, err := c.Images(ctx)
		if err != nil || len(out.Images) != 1 || got.method != "GET" || got.path != "/api/images" {
			t.Fatalf("Images=%+v %v request=%+v", out, err, got)
		}
	})
	t.Run("detail", func(t *testing.T) {
		c, got := newTestClient(t, jsonReply(200, apitypes.LibraryImageDetail{SuggestedTag: "2"}))
		out, err := c.Image(ctx, "web")
		if err != nil || out.SuggestedTag != "2" || got.method != "GET" || got.path != "/api/images/web" {
			t.Fatalf("Image=%+v %v request=%+v", out, err, got)
		}
	})
	t.Run("build", func(t *testing.T) {
		c, got := newTestClient(t, jsonReply(202, apitypes.BuildStarted{Operation: apitypes.Operation{ID: "op1"}}))
		out, err := c.BuildImage(ctx, "web", apitypes.BuildRequest{Tag: "2", BuildArgs: map[string]string{"A": "b"}, Ref: "main"})
		if err != nil || out.Operation.ID != "op1" || got.method != "POST" || got.path != "/api/images/web/builds" ||
			got.body != `{"tag":"2","buildArgs":{"A":"b"},"saveArgs":false,"ref":"main"}` {
			t.Fatalf("BuildImage=%+v %v request=%+v", out, err, got)
		}
	})
	t.Run("push", func(t *testing.T) {
		c, got := newTestClient(t, jsonReply(202, apitypes.Operation{ID: "op2"}))
		out, err := c.PushImageVersion(ctx, "web", "v1.2", "t_1")
		if err != nil || out.ID != "op2" || got.method != "POST" || got.path != "/api/images/web/versions/v1.2/push" || got.body != `{"target":"t_1"}` {
			t.Fatalf("PushImageVersion=%+v %v request=%+v", out, err, got)
		}
	})
	t.Run("conflict keeps message", func(t *testing.T) {
		c, _ := newTestClient(t, jsonReply(409, apitypes.ErrorBody{Error: "tag 2 is taken", Code: "tag_taken"}))
		_, err := c.BuildImage(ctx, "web", apitypes.BuildRequest{Tag: "2"})
		var ce *Error
		if !errors.Is(err, ErrConflict) || !errors.As(err, &ce) || ce.Message != "tag 2 is taken" {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestImagesClientNeedsSession(t *testing.T) {
	c, _ := newTestClient(t, jsonReply(200, nil))
	c.cfg.Session = ""
	if _, err := c.Images(context.Background()); !errors.Is(err, ErrNoSession) {
		t.Fatalf("err=%v", err)
	}
}
