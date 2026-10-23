package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/peers"
	"github.com/rcss-labs/stackstead/internal/registry"
)

// fakeRegistryProvider stands in for Docker Hub and AWS: no network is used.
type fakeRegistryProvider struct {
	testErr error
	credErr error
	onTest  func()
	result  registry.TestResult
}

func (f *fakeRegistryProvider) Test(_ context.Context, r registry.Registry) (registry.TestResult, error) {
	if f.onTest != nil {
		f.onTest()
	}
	if f.testErr != nil {
		return registry.TestResult{}, f.testErr
	}
	if f.result != (registry.TestResult{}) {
		return f.result, nil
	}
	return registry.TestResult{OK: true, Host: r.Host, Message: "ok"}, nil
}

func (f *fakeRegistryProvider) Credentials(_ context.Context, r registry.Registry) (engine.RegistryAuth, error) {
	if f.credErr != nil {
		return engine.RegistryAuth{}, f.credErr
	}
	return engine.RegistryAuth{Username: "alice", Password: "tok", ServerAddress: r.Host}, nil
}

const hubBody = `{"name":"hub","kind":"dockerhub","hub":{"username":"alice","token":"dckr_pat_SECRETVALUE"}}`

func (e *imagesEnv) createRegistry(t *testing.T, body string) apitypes.RegistryView {
	t.Helper()
	rec := e.req(t, "POST", "/api/settings/registries", body)
	if rec.Code != 201 {
		t.Fatalf("create registry = %d %s", rec.Code, rec.Body)
	}
	var v apitypes.RegistryView
	decodeBody(t, rec, &v)
	return v
}

func TestRegistryViewNeverContainsSecrets(t *testing.T) {
	env := newImagesEnv(t)
	secrets := []string{"dckr_pat_SECRETVALUE", "wJalrSECRETKEY", "SESSIONSECRET"}
	for _, body := range []string{hubBody, `{"name":"aws","kind":"ecr","ecr":{"region":"eu-west-1","mode":"static","accessKeyId":"AKIAEXAMPLE","secretAccessKey":"wJalrSECRETKEY","sessionToken":"SESSIONSECRET"}}`} {
		rec := env.req(t, "POST", "/api/settings/registries", body)
		if rec.Code != 201 {
			t.Fatalf("create = %d %s", rec.Code, rec.Body)
		}
		for _, sec := range secrets {
			if strings.Contains(rec.Body.String(), sec) {
				t.Errorf("201 body contains secret %q: %s", sec, rec.Body)
			}
		}
	}
	var list0 []apitypes.RegistryView
	decodeBody(t, env.req(t, "GET", "/api/settings/registries", ""), &list0)
	var hub, static apitypes.RegistryView
	for _, v := range list0 {
		if v.Kind == "ecr" {
			static = v
		} else {
			hub = v
		}
	}
	if static.AccessKeyID != "AKIAEXAMPLE" || !static.Configured {
		t.Errorf("static view = %+v, want accessKeyId and configured", static)
	}
	if !hub.Configured || hub.Username != "alice" {
		t.Errorf("hub view = %+v", hub)
	}
	bodies := []string{
		env.req(t, "GET", "/api/settings/registries", "").Body.String(),
		env.req(t, "PUT", "/api/settings/registries/"+static.ID, `{"name":"aws2","kind":"ecr","ecr":{"region":"eu-west-1","mode":"static","accessKeyId":"AKIAEXAMPLE","secretAccessKey":"","sessionToken":""}}`).Body.String(),
		env.req(t, "GET", "/api/images/registries", "").Body.String(),
	}
	for i, b := range bodies {
		for _, s := range secrets {
			if strings.Contains(b, s) {
				t.Errorf("body %d contains secret %q: %s", i, s, b)
			}
		}
	}
	if !strings.Contains(bodies[0], "AKIAEXAMPLE") {
		t.Errorf("list lacks accessKeyId: %s", bodies[0])
	}
}

func TestRegistryErrorCodes(t *testing.T) {
	env := newImagesEnv(t)
	hub := env.createRegistry(t, hubBody)
	for _, tc := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"invalid", "POST", "/api/settings/registries", `{"name":"","kind":"dockerhub","hub":{"username":"a","token":"t"}}`, 400},
		{"name taken", "POST", "/api/settings/registries", hubBody, 409},
		{"update missing", "PUT", "/api/settings/registries/nope", hubBody, 404},
		{"delete missing", "DELETE", "/api/settings/registries/nope", "", 404},
		{"test missing", "POST", "/api/settings/registries/nope/test", "", 404},
		{"delete", "DELETE", "/api/settings/registries/" + hub.ID, "", 204},
	} {
		if rec := env.req(t, tc.method, tc.path, tc.body); rec.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, rec.Code, rec.Body, tc.want)
		}
	}
	rec := env.req(t, "POST", "/api/settings/registries", `{"name":"","kind":"dockerhub","hub":{"username":"a","token":"t"}}`)
	var body struct {
		Fields []apitypes.FieldError `json:"fields"`
	}
	decodeBody(t, rec, &body)
	if len(body.Fields) != 1 || body.Fields[0].Field != "name" {
		t.Errorf("fields = %+v, want one on name", body.Fields)
	}
}

func TestRegistryTestRecordsResult(t *testing.T) {
	env := newImagesEnv(t)
	hub := env.createRegistry(t, hubBody)

	rec := env.req(t, "POST", "/api/settings/registries/"+hub.ID+"/test", "")
	var res apitypes.RegistryTest
	decodeBody(t, rec, &res)
	if rec.Code != 200 || !res.OK {
		t.Fatalf("test = %d %+v", rec.Code, res)
	}
	var list []apitypes.RegistryView
	decodeBody(t, env.req(t, "GET", "/api/settings/registries", ""), &list)
	if len(list) != 1 || list[0].LastTestAt == nil || !list[0].LastTestOK {
		t.Errorf("recorded = %+v", list)
	}

	env.prov.testErr = errors.New("connection refused for dckr_pat_SECRETVALUE")
	rec = env.req(t, "POST", "/api/settings/registries/"+hub.ID+"/test", "")
	decodeBody(t, rec, &res)
	if rec.Code != 200 || res.OK || res.Message == "" {
		t.Fatalf("failed test = %d %+v", rec.Code, res)
	}
	if strings.Contains(rec.Body.String(), "dckr_pat_SECRETVALUE") {
		t.Errorf("test answer leaks token: %s", rec.Body)
	}
	decodeBody(t, env.req(t, "GET", "/api/settings/registries", ""), &list)
	if list[0].LastTestOK || strings.Contains(list[0].LastTestMessage, "dckr_pat_SECRETVALUE") || list[0].LastTestMessage == "" {
		t.Errorf("recorded failure = %+v", list[0])
	}
}

func TestRegistrySettingsDeniedToPeer(t *testing.T) {
	clock := &peerTestClock{t: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	st, psvc := newPeerStore(t, clock)
	if err := st.InsertPeer(context.Background(), peers.Peer{InstanceID: "inst-a", Name: "mini-01", URL: "http://127.0.0.1:1", State: peers.StateActive}, peers.HashSecret("ssp_good")); err != nil {
		t.Fatal(err)
	}
	env := newImagesEnv(t, func(o *api.Options) { o.Peers = psvc })
	for _, rt := range []struct{ method, path string }{
		{"GET", "/api/settings/registries"}, {"POST", "/api/settings/registries"},
		{"PUT", "/api/settings/registries/x"}, {"DELETE", "/api/settings/registries/x"},
		{"POST", "/api/settings/registries/x/test"},
	} {
		rec := env.do(t, rt.method, rt.path, `{}`, peerReq("ssp_good", "")...)
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), "peer_forbidden") {
			t.Errorf("%s %s = %d %s, want 403 peer_forbidden", rt.method, rt.path, rec.Code, rec.Body)
		}
	}
}

func TestImageRegistriesListForwardable(t *testing.T) {
	clock := &peerTestClock{t: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	st, psvc := newPeerStore(t, clock)
	if err := st.InsertPeer(context.Background(), peers.Peer{InstanceID: "inst-a", Name: "mini-01", URL: "http://127.0.0.1:1", State: peers.StateActive}, peers.HashSecret("ssp_good")); err != nil {
		t.Fatal(err)
	}
	env := newImagesEnv(t, func(o *api.Options) { o.Peers = psvc })
	env.createRegistry(t, hubBody)
	rec := env.do(t, "GET", "/api/images/registries", "", peerReq("ssp_good", "")...)
	if rec.Code != 200 {
		t.Fatalf("peer list = %d %s", rec.Code, rec.Body)
	}
	var raw []map[string]any
	decodeBody(t, rec, &raw)
	if len(raw) != 1 || raw[0]["name"] != "hub" || raw[0]["kind"] != "dockerhub" || raw[0]["host"] != "docker.io" {
		t.Fatalf("list = %v", raw)
	}
	for _, k := range []string{"username", "region", "profile", "accessKeyId", "lastTestMessage"} {
		if raw[0][k] != nil && raw[0][k] != "" {
			t.Errorf("peer view carries %q = %v", k, raw[0][k])
		}
	}
	if strings.Contains(rec.Body.String(), "alice") {
		t.Errorf("peer view leaks username: %s", rec.Body)
	}
}

func TestPushTargetViewsCarryRegistryName(t *testing.T) {
	env := newImagesEnv(t)
	hub := env.createRegistry(t, hubBody)
	env.createImage(t, "web")
	rec := env.req(t, "POST", "/api/images/web/push-targets", `{"registryId":"`+hub.ID+`","repository":"alice/web","autoPush":false}`)
	if rec.Code != 201 {
		t.Fatalf("add target = %d %s", rec.Code, rec.Body)
	}
	var tg apitypes.PushTarget
	decodeBody(t, rec, &tg)
	if tg.RegistryName != "hub" || tg.Kind != "dockerhub" {
		t.Errorf("target = %+v", tg)
	}
	rec = env.req(t, "PUT", "/api/images/web/push-targets/"+tg.ID, `{"registryId":"`+hub.ID+`","repository":"alice/web2","autoPush":true}`)
	if rec.Code != 200 {
		t.Fatalf("update target = %d %s", rec.Code, rec.Body)
	}
	var img apitypes.LibraryImageDetail
	decodeBody(t, env.req(t, "GET", "/api/images/web", ""), &img)
	if len(img.PushTargets) != 1 || img.PushTargets[0].RegistryName != "hub" || img.PushTargets[0].Repository != "alice/web2" {
		t.Errorf("detail targets = %+v", img.PushTargets)
	}
	if rec := env.req(t, "DELETE", "/api/images/web/push-targets/"+tg.ID, ""); rec.Code != 204 {
		t.Errorf("delete target = %d", rec.Code)
	}
	if rec := env.req(t, "DELETE", "/api/images/web/push-targets/"+tg.ID, ""); rec.Code != 404 {
		t.Errorf("delete again = %d, want 404", rec.Code)
	}
}

func TestPushRouteCodes(t *testing.T) {
	env := newImagesEnv(t)
	hub := env.createRegistry(t, hubBody)
	env.createImage(t, "web")
	env.buildDone(t, "web", "1")
	addTarget := func(registryID string) apitypes.PushTarget {
		rec := env.req(t, "POST", "/api/images/web/push-targets", `{"registryId":"`+registryID+`","repository":"alice/web","autoPush":false}`)
		if rec.Code != 201 {
			t.Fatalf("add target = %d %s", rec.Code, rec.Body)
		}
		var tg apitypes.PushTarget
		decodeBody(t, rec, &tg)
		return tg
	}
	tg := addTarget(hub.ID)
	push := func(slug, tag, target string) *httptest.ResponseRecorder {
		return env.req(t, "POST", "/api/images/"+slug+"/versions/"+tag+"/push", `{"target":"`+target+`"}`)
	}

	if rec := push("web", "9", tg.ID); rec.Code != 404 {
		t.Errorf("unknown tag = %d %s, want 404", rec.Code, rec.Body)
	}
	if rec := push("web", "1", "nope"); rec.Code != 404 {
		t.Errorf("unknown target = %d %s, want 404", rec.Code, rec.Body)
	}
	if rec := push("nope", "1", tg.ID); rec.Code != 404 {
		t.Errorf("unknown image = %d %s, want 404", rec.Code, rec.Body)
	}

	rec := push("web", "1", tg.ID)
	if rec.Code != 202 {
		t.Fatalf("push = %d %s, want 202", rec.Code, rec.Body)
	}
	var op apitypes.Operation
	decodeBody(t, rec, &op)
	if op.ID == "" || op.Action != "push" {
		t.Errorf("operation = %+v", op)
	}
	waitOp(t, env.ops, op.ID)

	// A registry with no credentials answers 409 registry_unconfigured.
	bare := env.createRegistry(t, `{"name":"bare","kind":"dockerhub","hub":{"username":"alice","token":""}}`)
	bareTarget := addTarget(bare.ID)
	rec = push("web", "1", bareTarget.ID)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"registry_unconfigured"`) {
		t.Errorf("unconfigured = %d %s", rec.Code, rec.Body)
	}

	// A running build on the image answers 409 build_running.
	gate := make(chan struct{})
	env.eng.mu.Lock()
	env.eng.gate = gate
	env.eng.mu.Unlock()
	if rec := env.build(t, "web", "2"); rec.Code != 202 {
		t.Fatalf("build = %d %s", rec.Code, rec.Body)
	}
	rec = push("web", "1", tg.ID)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"build_running"`) {
		t.Errorf("busy = %d %s", rec.Code, rec.Body)
	}
	close(gate)
}

func TestRegistryTestSaveFailureStillReportsOK(t *testing.T) {
	env := newImagesEnv(t)
	hub := env.createRegistry(t, hubBody)
	// The registry disappears while the provider test runs, so saving the
	// result fails although the connection works.
	env.prov.onTest = func() {
		if err := env.regs.Delete(context.Background(), "x", hub.ID); err != nil {
			t.Error(err)
		}
	}
	rec := env.req(t, "POST", "/api/settings/registries/"+hub.ID+"/test", "")
	var res apitypes.RegistryTest
	decodeBody(t, rec, &res)
	if rec.Code != 200 || !res.OK || res.Message != "ok" {
		t.Errorf("test = %d %+v, want 200 ok:true", rec.Code, res)
	}
}

func TestReservedImageSlugs(t *testing.T) {
	env := newImagesEnv(t)
	for _, name := range []string{"Registries", "fetch"} {
		rec := env.req(t, "POST", "/api/images", `{"name":"`+name+`","source":"upload","dockerfilePath":"","buildArgs":{}}`)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"field":"name"`) || !strings.Contains(rec.Body.String(), "reserved") {
			t.Errorf("create %q = %d %s, want 400 reserved on name", name, rec.Code, rec.Body)
		}
	}
}

func TestPushStateCarriesRegistryName(t *testing.T) {
	env := newImagesEnv(t)
	hub := env.createRegistry(t, hubBody)
	env.createImage(t, "web")
	env.buildDone(t, "web", "1")
	rec := env.req(t, "POST", "/api/images/web/push-targets", `{"registryId":"`+hub.ID+`","repository":"alice/web","autoPush":false}`)
	var tg apitypes.PushTarget
	decodeBody(t, rec, &tg)
	rec = env.req(t, "POST", "/api/images/web/versions/1/push", `{"target":"`+tg.ID+`"}`)
	var op apitypes.Operation
	decodeBody(t, rec, &op)
	waitOp(t, env.ops, op.ID)

	var detail apitypes.LibraryImageDetail
	decodeBody(t, env.req(t, "GET", "/api/images/web", ""), &detail)
	if len(detail.Versions) != 1 || len(detail.Versions[0].Pushes) != 1 || detail.Versions[0].Pushes[0].Registry != "hub" {
		t.Fatalf("detail pushes = %+v", detail.Versions)
	}
	if detail.LatestVersion == nil || detail.LatestVersion.Pushes[0].Registry != "hub" {
		t.Errorf("latest pushes = %+v", detail.LatestVersion)
	}
	var lib apitypes.Library
	decodeBody(t, env.req(t, "GET", "/api/images", ""), &lib)
	if len(lib.Images) != 1 || lib.Images[0].LatestVersion == nil || lib.Images[0].LatestVersion.Pushes[0].Registry != "hub" {
		t.Errorf("list pushes = %+v", lib.Images)
	}
}

func TestPushUnconfiguredProviderIs409RegistryUnconfigured(t *testing.T) {
	env := newImagesEnv(t)
	hub := env.createRegistry(t, hubBody)
	env.createImage(t, "web")
	env.buildDone(t, "web", "1")
	rec := env.req(t, "POST", "/api/images/web/push-targets", `{"registryId":"`+hub.ID+`","repository":"alice/web","autoPush":false}`)
	var tg apitypes.PushTarget
	decodeBody(t, rec, &tg)
	env.prov.credErr = fmt.Errorf("%w: region is required", registry.ErrUnconfigured)

	rec = env.req(t, "POST", "/api/images/web/versions/1/push", `{"target":"`+tg.ID+`"}`)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"registry_unconfigured"`) {
		t.Fatalf("push = %d %s, want 409 registry_unconfigured", rec.Code, rec.Body)
	}
}

func TestPushCredentialFailureIs502WithoutSecrets(t *testing.T) {
	env := newImagesEnv(t)
	hub := env.createRegistry(t, hubBody)
	env.createImage(t, "web")
	env.buildDone(t, "web", "1")
	rec := env.req(t, "POST", "/api/images/web/push-targets", `{"registryId":"`+hub.ID+`","repository":"alice/web","autoPush":false}`)
	var tg apitypes.PushTarget
	decodeBody(t, rec, &tg)
	env.prov.credErr = errors.New("token service rejected dckr_pat_SECRETVALUE")

	rec = env.req(t, "POST", "/api/images/web/versions/1/push", `{"target":"`+tg.ID+`"}`)
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), `"registry_auth_failed"`) || !strings.Contains(rec.Body.String(), "token service rejected") {
		t.Fatalf("push = %d %s, want 502 registry_auth_failed with the message", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "dckr_pat_SECRETVALUE") {
		t.Errorf("body contains the token: %s", rec.Body)
	}
}
