package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

type seen struct {
	method, path, rawQuery, body string
	header                       http.Header
}

// newTestClient serves handler and returns a Client with a session.
func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *seen) {
	t.Helper()
	got := &seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*got = seen{r.Method, r.URL.Path, r.URL.RawQuery, string(b), r.Header.Clone()}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return newClientFor(t, srv.URL, "sess123"), got
}

func newClientFor(t *testing.T, rawURL, session string) *Client {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{BaseURL: u, Session: session, UserAgent: "stackstead-test/1"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func jsonReply(status int, v any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
}

func TestActSendsEmptyJSONBodyCookieAndUserAgent(t *testing.T) {
	c, got := newTestClient(t, jsonReply(202, apitypes.Operation{ID: "op1"}))
	op, err := c.Act(context.Background(), "my stack", "up")
	if err != nil || op.ID != "op1" {
		t.Fatalf("Act = %+v, %v", op, err)
	}
	if got.method != "POST" || got.path != "/api/stacks/my stack/actions/up" {
		t.Errorf("request = %s %s", got.method, got.path)
	}
	if got.body != "{}" {
		t.Errorf("body = %q, want {}", got.body)
	}
	if ct := got.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if ck := got.header.Get("Cookie"); ck != "stackstead_session=sess123" {
		t.Errorf("Cookie = %q", ck)
	}
	if ua := got.header.Get("User-Agent"); ua != "stackstead-test/1" {
		t.Errorf("User-Agent = %q", ua)
	}
	if a := got.header.Get("Accept"); a != "application/json" {
		t.Errorf("Accept = %q", a)
	}
}

func TestDeleteSendsNoBodyNoContentType(t *testing.T) {
	c, got := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	if err := c.DeleteMetricsToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.method != "DELETE" || got.body != "" || got.header.Get("Content-Type") != "" {
		t.Errorf("got %s body=%q ct=%q", got.method, got.body, got.header.Get("Content-Type"))
	}
}

func TestRemoveVolumesQuery(t *testing.T) {
	c, got := newTestClient(t, jsonReply(202, apitypes.Operation{}))
	if _, err := c.Remove(context.Background(), "web", true); err != nil {
		t.Fatal(err)
	}
	if got.method != "DELETE" || got.rawQuery != "volumes=true" || got.body != "" {
		t.Errorf("got %s ?%s body %q", got.method, got.rawQuery, got.body)
	}
}

func TestSessionlessMethodsReturnErrNoSessionWithoutRequest(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer srv.Close()
	c := newClientFor(t, srv.URL, "")
	ctx := context.Background()
	if _, err := c.Stacks(ctx); !errors.Is(err, ErrNoSession) {
		t.Errorf("Stacks err = %v", err)
	}
	if err := c.StreamEvents(ctx, StreamOptions{}, func(Event) error { return nil }); !errors.Is(err, ErrNoSession) {
		t.Errorf("StreamEvents err = %v", err)
	}
	if requests != 0 {
		t.Errorf("%d requests sent", requests)
	}
}

func TestSessionlessMethodsThatAreExempt(t *testing.T) {
	c, got := newTestClient(t, jsonReply(200, apitypes.Health{OK: true}))
	c.cfg.Session = ""
	if err := c.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.header.Get("Cookie") != "" {
		t.Errorf("Cookie sent: %q", got.header.Get("Cookie"))
	}
}

func TestErrorBodyMapping(t *testing.T) {
	c, _ := newTestClient(t, jsonReply(400, map[string]any{
		"error":  "invalid stack",
		"fields": []apitypes.FieldError{{Field: "name", Message: "too long"}},
	}))
	_, err := c.Stacks(context.Background())
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v", err)
	}
	if apiErr.Status != 400 || apiErr.Message != "invalid stack" || len(apiErr.Fields) != 1 || apiErr.Fields[0].Field != "name" {
		t.Errorf("error = %+v", apiErr)
	}
	if apiErr.Error() != "invalid stack" {
		t.Errorf("Error() = %q", apiErr.Error())
	}
}

func TestStatusSentinels(t *testing.T) {
	cases := map[int]error{
		401: ErrUnauthorized, 403: ErrForbidden, 404: ErrNotFound,
		409: ErrConflict, 429: ErrTooManyRequests, 503: ErrUnavailable,
	}
	for status, sentinel := range cases {
		c, _ := newTestClient(t, jsonReply(status, map[string]string{"error": "x"}))
		_, err := c.Stacks(context.Background())
		if !errors.Is(err, sentinel) {
			t.Errorf("status %d: errors.Is(%v, sentinel) = false", status, err)
		}
		for other, os := range cases {
			if other != status && errors.Is(err, os) {
				t.Errorf("status %d matched sentinel of %d", status, other)
			}
		}
	}
}

func TestNonJSONErrorBodyFallsBackToStatus(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "<html>bad gateway</html>", 502)
	})
	_, err := c.Stacks(context.Background())
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Message != "HTTP 502" {
		t.Fatalf("err = %v", err)
	}
}

func TestRetryAfterOn429(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "42")
		jsonReply(429, map[string]string{"error": "slow down"})(w, r)
	})
	err := c.ChangePassword(context.Background(), "a", "b")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.RetryAfter != 42*time.Second {
		t.Fatalf("err = %#v", err)
	}
}

func TestConnectionRefusedStaysMatchable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()
	c := newClientFor(t, addr, "s")
	err := c.Health(context.Background())
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "connect to ") {
		t.Errorf("message = %q", err)
	}
}

func TestLoginReturnsSessionAndExpiry(t *testing.T) {
	c, got := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "stackstead_session", Value: "tok", MaxAge: 3600, Path: "/"})
		w.WriteHeader(204)
	})
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return fixed }
	sess, exp, err := c.Login(context.Background(), "admin", "pw")
	if err != nil || sess != "tok" || !exp.Equal(fixed.Add(time.Hour)) {
		t.Fatalf("Login = %q %v %v", sess, exp, err)
	}
	var body apitypes.LoginRequest
	_ = json.Unmarshal([]byte(got.body), &body)
	if body.Username != "admin" || body.Password != "pw" {
		t.Errorf("body = %q", got.body)
	}
}

func TestLoginWithoutCookieIsAnError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	_, _, err := c.Login(context.Background(), "a", "b")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Message != "login succeeded but no session cookie was set" {
		t.Fatalf("err = %v", err)
	}
}

func TestRedirectToOtherHostRefused(t *testing.T) {
	var leaked bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer other.Close()
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/api/stacks", http.StatusFound)
	})
	_, err := c.Stacks(context.Background())
	if err == nil || !errors.Is(err, errRedirectHost) {
		t.Fatalf("err = %v", err)
	}
	if leaked {
		t.Error("the redirect target received a request")
	}
}

func TestPathPrefixIsKept(t *testing.T) {
	c, got := newTestClient(t, jsonReply(200, []apitypes.StackSummary{}))
	c.prefix = "/stackstead"
	if _, err := c.Stacks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.path != "/stackstead/api/stacks" {
		t.Errorf("path = %q", got.path)
	}
}

func TestMetricsHistoryQueryEncoding(t *testing.T) {
	c, got := newTestClient(t, jsonReply(200, apitypes.MetricsHistory{}))
	loc := time.FixedZone("x", 3*3600)
	_, err := c.MetricsHistory(context.Background(), "web", MetricsQuery{
		From: time.Date(2026, 1, 1, 3, 0, 0, 0, loc), To: time.Date(2026, 1, 1, 4, 0, 0, 0, time.UTC),
		Step: 90 * time.Second, Service: "app",
	})
	if err != nil {
		t.Fatal(err)
	}
	q, _ := url.ParseQuery(got.rawQuery)
	if q.Get("from") != "2026-01-01T00:00:00Z" || q.Get("to") != "2026-01-01T04:00:00Z" || q.Get("step") != "90" || q.Get("service") != "app" {
		t.Errorf("query = %v", q)
	}
	if _, err := c.MetricsHistory(context.Background(), "web", MetricsQuery{}); err != nil || got.rawQuery != "" {
		t.Errorf("zero query sent ?%s (%v)", got.rawQuery, err)
	}
}

func TestImportAndSaveComposeReportPartialChecks(t *testing.T) {
	c, got := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Stackstead-Checks", "partial")
		jsonReply(201, apitypes.StackSummary{Name: "web"})(w, r)
	})
	imp, err := c.Import(context.Background(), "/srv/web/compose.yaml")
	if err != nil || !imp.ChecksPartial || imp.Data.Name != "web" {
		t.Fatalf("Import = %+v %v", imp, err)
	}
	if !strings.Contains(got.body, `"path":"/srv/web/compose.yaml"`) {
		t.Errorf("body = %s", got.body)
	}
	save, err := c.SaveCompose(context.Background(), "web", "services: {}\n", "h1")
	if err != nil || !save.ChecksPartial {
		t.Fatalf("SaveCompose = %+v %v", save, err)
	}
	if got.method != "PUT" || !strings.Contains(got.body, `"hash":"h1"`) {
		t.Errorf("request = %s %s", got.method, got.body)
	}
}

func TestStreamLogsQueryEncoding(t *testing.T) {
	c, got := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	_ = c.StreamLogs(context.Background(), "web", LogsQuery{Follow: true, Tail: "all", Since: "10m", Service: "app"}, StreamOptions{}, func(Event) error { return nil })
	q, _ := url.ParseQuery(got.rawQuery)
	if q.Get("follow") != "true" || q.Get("tail") != "all" || q.Get("since") != "10m" || q.Get("service") != "app" || got.path != "/api/stacks/web/logs" {
		t.Errorf("%s ?%v", got.path, q)
	}
}

func TestNewRejectsBadBaseURL(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Error("nil BaseURL accepted")
	}
	u, _ := url.Parse("ftp://host")
	if _, err := New(Config{BaseURL: u}); err == nil {
		t.Error("ftp scheme accepted")
	}
}

func TestRedirectRefusedOnSchemeChange(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/api/stacks", http.StatusFound)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	c, err := New(Config{BaseURL: u, Session: "s", Insecure: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Stacks(context.Background()); !errors.Is(err, errRedirectHost) {
		t.Errorf("unary err = %v", err)
	}
	err = c.Stream(context.Background(), "/api/events", StreamOptions{}, func(Event) error { return nil })
	if !errors.Is(err, errRedirectHost) {
		t.Errorf("stream err = %v", err)
	}
}
