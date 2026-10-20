package peers

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeResponse(status int, contentType, body string) *http.Response {
	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

func TestClassify(t *testing.T) {
	const server = "mini-02"
	refused := func() error {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		_, err := http.Get(url)
		return err
	}()
	cases := []struct {
		name string
		resp *http.Response
		err  error
		code string // "" = nil error
	}{
		{"ok json", fakeResponse(200, "application/json; charset=utf-8", `{}`), nil, ""},
		{"not paired", fakeResponse(401, "application/json", `{"error":"x","code":"peer_not_paired"}`), nil, "peer_unpaired"},
		{"401 html", fakeResponse(401, "text/html", `<html>login</html>`), nil, "peer_proxy"},
		{"401 json other code", fakeResponse(401, "application/json", `{"error":"x"}`), nil, "peer_proxy"},
		{"200 html", fakeResponse(200, "text/html", `<html>sso</html>`), nil, "peer_proxy"},
		{"302 sso", fakeResponse(302, "", ``), nil, "peer_proxy"},
		{"403 json no code", fakeResponse(403, "application/json", `{"error":"no"}`), nil, "peer_proxy"},
		{"dial refused", nil, refused, "peer_unreachable"},
		{"204 no content type", fakeResponse(204, "", ``), nil, ""},
		{"200 event stream", fakeResponse(200, "text/event-stream", ``), nil, ""},
		{"200 no content type", fakeResponse(200, "", `x`), nil, "peer_proxy"},
		{"500 html", fakeResponse(500, "text/html", `<html>`), nil, "peer_proxy"},
		{"502 json without error", fakeResponse(502, "application/json", `{"x":1}`), nil, "peer_proxy"},
		{"redirect refused", nil, ErrProxyAnswered, "peer_proxy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Classify(server, c.resp, c.err)
			if c.code == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			var pe *PeerError
			if !errors.As(err, &pe) || pe.Code != c.code {
				t.Fatalf("err = %v, want code %s", err, c.code)
			}
			if !strings.Contains(err.Error(), server) || strings.Contains(err.Error(), "ssp_") {
				t.Errorf("bad message %q", err.Error())
			}
			if c.code == "peer_unpaired" && !errors.Is(err, ErrNotPaired) {
				t.Error("want errors.Is ErrNotPaired")
			}
		})
	}
}

func TestClassifyDoesNotLeakCredentialFromError(t *testing.T) {
	err := Classify("s", nil, errors.New(`Get "https://h/x?token=ssp_abc": dial tcp: refused`))
	if strings.Contains(err.Error(), "ssp_") {
		t.Errorf("leaked: %v", err)
	}
}

func TestClassifyRemoteError(t *testing.T) {
	resp := fakeResponse(409, "application/json",
		`{"error":"stack is busy","fields":[{"field":"name","message":"taken"}]}`)
	err := Classify("mini-02", resp, nil)
	var re *RemoteError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *RemoteError", err)
	}
	if re.Status != 409 || re.Body.Error != "stack is busy" || len(re.Body.Fields) != 1 {
		t.Errorf("got %+v", re)
	}
	if want := "mini-02: stack is busy"; err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
}
