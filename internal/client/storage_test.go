package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

func TestStorageClientRequests(t *testing.T) {
	ctx := context.Background()
	t.Run("overview", func(t *testing.T) {
		c, got := newTestClient(t, jsonReply(200, apitypes.StorageOverview{Images: apitypes.StorageKindTotals{Count: 3}}))
		out, err := c.StorageOverview(ctx)
		if err != nil || out.Images.Count != 3 || got.method != "GET" || got.path != "/api/storage" {
			t.Fatalf("overview=%+v %v request=%+v", out, err, got)
		}
	})
	t.Run("create", func(t *testing.T) {
		c, got := newTestClient(t, jsonReply(201, apitypes.StoragePlan{ID: "p_one", ConfirmPhrase: "old_data"}))
		out, err := c.CreateStoragePlan(ctx, apitypes.StoragePlanRequest{Kind: "volume", Name: "old_data"})
		if err != nil || out.ID != "p_one" || got.method != "POST" || got.path != "/api/storage/plans" || got.body != `{"kind":"volume","name":"old_data"}` || got.header.Get("Content-Type") != "application/json" {
			t.Fatalf("create=%+v %v request=%+v", out, err, got)
		}
	})
	t.Run("confirm exact phrase", func(t *testing.T) {
		c, got := newTestClient(t, jsonReply(202, apitypes.Operation{ID: "op1", Stack: "", Action: "storage-cleanup"}))
		out, err := c.ConfirmStoragePlan(ctx, "p_one", " old_data ")
		if err != nil || out.Action != "storage-cleanup" || got.method != "POST" || got.path != "/api/storage/plans/p_one/confirm" || got.body != `{"confirm":" old_data "}` {
			t.Fatalf("confirm=%+v %v request=%+v", out, err, got)
		}
	})
	t.Run("refresh", func(t *testing.T) {
		c, got := newTestClient(t, jsonReply(202, apitypes.StorageRefresh{StartedAt: time.Unix(1, 0).UTC()}))
		out, err := c.RefreshStorage(ctx)
		if err != nil || out.StartedAt.Unix() != 1 || got.method != "POST" || got.path != "/api/storage/refresh" || got.body != "{}" {
			t.Fatalf("refresh=%+v %v request=%+v", out, err, got)
		}
	})
	t.Run("actions", func(t *testing.T) {
		c, got := newTestClient(t, jsonReply(200, []apitypes.ActionEntry{{Username: "alice", Bytes: nil}}))
		out, err := c.RecentActions(ctx, ActionsQuery{Kind: "storage.", Scope: "my scope&", Limit: 5})
		q, _ := url.ParseQuery(got.rawQuery)
		if err != nil || len(out) != 1 || out[0].Bytes != nil || q.Get("kind") != "storage." || q.Get("scope") != "my scope&" || q.Get("limit") != "5" || got.method != "GET" || got.path != "/api/actions" {
			t.Fatalf("actions=%+v %v request=%+v", out, err, got)
		}
	})
}
func TestStorageClientErrorsAndSession(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "59")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":"wait","retryAfter":59}`))
	})
	_, err := c.RefreshStorage(context.Background())
	var rate *Error
	if !errors.Is(err, ErrTooManyRequests) || !errors.As(err, &rate) || rate.RetryAfter != 59*time.Second {
		t.Fatalf("rate=%+v", err)
	}
	c.cfg.Session = ""
	for name, fn := range map[string]func() error{
		"overview": func() error { _, err := c.StorageOverview(context.Background()); return err },
		"create": func() error {
			_, err := c.CreateStoragePlan(context.Background(), apitypes.StoragePlanRequest{})
			return err
		},
		"confirm": func() error { _, err := c.ConfirmStoragePlan(context.Background(), "p", "x"); return err },
		"refresh": func() error { _, err := c.RefreshStorage(context.Background()); return err },
		"actions": func() error { _, err := c.RecentActions(context.Background(), ActionsQuery{}); return err },
	} {
		if err := fn(); !errors.Is(err, ErrNoSession) {
			t.Errorf("%s=%v", name, err)
		}
	}
}
