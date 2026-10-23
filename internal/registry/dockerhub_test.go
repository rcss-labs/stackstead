package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func hubServer(t *testing.T, handler http.HandlerFunc) *HubProvider {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &HubProvider{Base: srv.URL, Client: srv.Client()}
}

func TestHubTestOKAndRejected(t *testing.T) {
	var gotBody map[string]string
	p := hubServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v2/users/login" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		if gotBody["password"] == "good" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"token":"jwt"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	res, err := p.Test(context.Background(), hubRegistry("hub", "good"))
	if err != nil || !res.OK || res.Message != "Signed in as alice" || res.Host != HubHost {
		t.Fatalf("ok case: %+v, %v", res, err)
	}
	if gotBody["username"] != "alice" {
		t.Errorf("body: %v", gotBody)
	}
	res, err = p.Test(context.Background(), hubRegistry("hub", "bad"))
	if err != nil || res.OK || res.Message != "Docker Hub rejected the username or token" {
		t.Fatalf("rejected case: %+v, %v", res, err)
	}
}

func TestHubCredentials(t *testing.T) {
	auth, err := (&HubProvider{}).Credentials(context.Background(), hubRegistry("hub", "tok"))
	if err != nil {
		t.Fatal(err)
	}
	if auth.Username != "alice" || auth.Password != "tok" || auth.ServerAddress != "https://index.docker.io/v1/" {
		t.Errorf("auth: %#v", auth)
	}
}

func TestHubTestFailureRedacts(t *testing.T) {
	const token = "dckr_pat_LEAKME"
	p := hubServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"bad login for ` + body["password"] + `"}`))
	})
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	st := newTestStore(t)
	r, err := st.Create(context.Background(), hubRegistry("hub", token))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(st, map[Kind]Provider{KindDockerHub: p}, nil, logger)
	res, err := svc.Test(context.Background(), r.ID)
	if err == nil {
		t.Fatal("expected an error for a 500")
	}
	got, _ := st.Get(context.Background(), r.ID)
	for name, text := range map[string]string{
		"error": err.Error(), "result message": res.Message, "recorded message": got.LastTestMessage, "logs": logs.String(),
	} {
		if strings.Contains(text, token) {
			t.Errorf("%s leaks the token: %q", name, text)
		}
	}
	if got.LastTestOK || got.LastTestMessage == "" {
		t.Errorf("failure not recorded: %+v", got)
	}
}
