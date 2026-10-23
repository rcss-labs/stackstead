package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/peers"
)

func TestImageDefaultsRoundTrip(t *testing.T) {
	env := newImagesEnv(t)
	var got apitypes.ImageDefaults
	rec := env.req(t, "GET", "/api/settings/images", "")
	decodeBody(t, rec, &got)
	if rec.Code != 200 || got.Retention != (apitypes.Retention{}) {
		t.Fatalf("initial GET = %d %+v, want 200 and zero retention", rec.Code, got)
	}
	rec = env.req(t, "PUT", "/api/settings/images", `{"retention":{"keepLast":5,"keepDays":30}}`)
	if rec.Code != 200 {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body)
	}
	decodeBody(t, env.req(t, "GET", "/api/settings/images", ""), &got)
	if got.Retention.KeepLast != 5 || got.Retention.KeepDays != 30 {
		t.Errorf("GET after PUT = %+v", got)
	}
	// A new image created without retention picks up the default.
	rec = env.req(t, "POST", "/api/images", `{"name":"fresh","source":"upload"}`)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"keepLast":5`) {
		t.Errorf("create = %d %s, want default retention applied", rec.Code, rec.Body)
	}
}

func TestImageDefaultsValidation(t *testing.T) {
	env := newImagesEnv(t)
	for _, body := range []string{
		`{"retention":{"keepLast":-1,"keepDays":0}}`,
		`{"retention":{"keepLast":0,"keepDays":-3}}`,
		`{"retention":{"keepLast":1001,"keepDays":0}}`,
		`{"retention":{"keepLast":0,"keepDays":3651}}`,
	} {
		rec := env.req(t, "PUT", "/api/settings/images", body)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"field":"retention`) {
			t.Errorf("PUT %s = %d %s, want 400 with a retention field error", body, rec.Code, rec.Body)
		}
	}
	var got apitypes.ImageDefaults
	decodeBody(t, env.req(t, "GET", "/api/settings/images", ""), &got)
	if got.Retention != (apitypes.Retention{}) {
		t.Errorf("rejected PUTs changed the default: %+v", got)
	}
}

func TestImageDefaultsDeniedToPeer(t *testing.T) {
	clock := &peerTestClock{t: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	st, psvc := newPeerStore(t, clock)
	if err := st.InsertPeer(context.Background(), peers.Peer{InstanceID: "inst-a", Name: "mini-01", URL: "http://127.0.0.1:1", State: peers.StateActive}, peers.HashSecret("ssp_good")); err != nil {
		t.Fatal(err)
	}
	env := newImagesEnv(t, func(o *api.Options) { o.Peers = psvc })
	for _, method := range []string{"GET", "PUT"} {
		rec := env.do(t, method, "/api/settings/images", `{"retention":{"keepLast":1,"keepDays":1}}`, peerReq("ssp_good", "")...)
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), "peer_forbidden") {
			t.Errorf("%s = %d %s, want 403 peer_forbidden", method, rec.Code, rec.Body)
		}
	}
}
