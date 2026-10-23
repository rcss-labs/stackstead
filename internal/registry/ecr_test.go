package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/registry/awsauth"
	"github.com/rcss-labs/stackstead/internal/secret"
)

const (
	testAccount  = "123456789012"
	testProxy    = "https://" + testAccount + ".dkr.ecr.eu-west-1.amazonaws.com"
	testHost     = testAccount + ".dkr.ecr.eu-west-1.amazonaws.com"
	testSecret   = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	testSession  = "IQoJb3JpZ2luX2VjEXAMPLESESSIONTOKEN"
	testPassword = "ecr-password-1"
)

var ecrNow = time.Date(2026, 10, 23, 9, 30, 0, 0, time.UTC)

type ecrSeen struct {
	accessKey string
	token     string
	calls     int
}

// ecrServer answers GetAuthorizationToken after re-signing the request with the
// expected credentials and comparing Authorization headers.
func ecrServer(t *testing.T, wantKey, wantSecret, wantToken string, seen *ecrSeen) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen.calls++
		seen.token = r.Header.Get("X-Amz-Security-Token")
		if r.Method != http.MethodPost || r.URL.Path != "/" ||
			r.Header.Get("Content-Type") != "application/x-amz-json-1.1" ||
			r.Header.Get("X-Amz-Target") != "AmazonEC2ContainerRegistry_V20150921.GetAuthorizationToken" ||
			string(body) != "{}" {
			t.Errorf("unexpected request %s %s %v %q", r.Method, r.URL.Path, r.Header, body)
		}
		auth := r.Header.Get("Authorization")
		re := regexp.MustCompile(`^AWS4-HMAC-SHA256 Credential=([^/]+)/20261023/eu-west-1/ecr/aws4_request, SignedHeaders=[a-z0-9;-]*x-amz-target[a-z0-9;-]*, Signature=[0-9a-f]{64}$`)
		m := re.FindStringSubmatch(auth)
		if m == nil {
			t.Errorf("Authorization shape: %s", auth)
		} else {
			seen.accessKey = m[1]
		}
		ref, _ := http.NewRequest(r.Method, "http://"+r.Host+r.URL.RequestURI(), nil)
		// The transport adds unsigned headers (User-Agent, ...); re-sign only
		// those the client declared as signed.
		_, signedList, _ := strings.Cut(auth, "SignedHeaders=")
		signedList, _, _ = strings.Cut(signedList, ",")
		for _, name := range strings.Split(signedList, ";") {
			if name != "host" && name != "x-amz-date" && name != "x-amz-security-token" {
				ref.Header[http.CanonicalHeaderKey(name)] = r.Header.Values(name)
			}
		}
		creds := awsauth.Credentials{AccessKeyID: wantKey, SecretAccessKey: secret.Secret(wantSecret), SessionToken: secret.Secret(wantToken)}
		if err := awsauth.SignV4(ref, body, creds, "eu-west-1", "ecr", ecrNow); err != nil {
			t.Error(err)
		} else if ref.Header.Get("Authorization") != auth {
			t.Errorf("signature mismatch:\n got %s\nwant %s", auth, ref.Header.Get("Authorization"))
		}
		tok := base64.StdEncoding.EncodeToString([]byte("AWS:" + testPassword))
		_ = json.NewEncoder(w).Encode(map[string]any{"authorizationData": []map[string]string{{"authorizationToken": tok, "proxyEndpoint": testProxy}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newECRProvider(srv *httptest.Server) *ECRProvider {
	return &ECRProvider{
		Endpoint: func(string) string { return srv.URL },
		Client:   srv.Client(),
		Now:      func() time.Time { return ecrNow },
	}
}

func staticECR() Registry {
	return Registry{Name: "ecr", Kind: KindECR, ECR: ECR{Region: "eu-west-1", Mode: ECRStatic, AccessKeyID: "AKIDEXAMPLE",
		SecretAccessKey: secret.Secret(testSecret), SessionToken: secret.Secret(testSession)}}
}

func TestECRStaticCredentials(t *testing.T) {
	var seen ecrSeen
	srv := ecrServer(t, "AKIDEXAMPLE", testSecret, testSession, &seen)
	p := newECRProvider(srv)
	res, err := p.Test(context.Background(), staticECR())
	if err != nil || !res.OK || res.Message != "Registry "+testHost || res.Host != testHost {
		t.Fatalf("test: %+v, %v", res, err)
	}
	if seen.accessKey != "AKIDEXAMPLE" || seen.token != testSession {
		t.Errorf("seen: %+v", seen)
	}
	auth, err := p.Credentials(context.Background(), staticECR())
	if err != nil || auth.Username != "AWS" || auth.Password != testPassword || auth.ServerAddress != testHost {
		t.Fatalf("credentials: %+v, %v", auth, err)
	}
}

func imdsServer(t *testing.T) *awsauth.IMDS {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/latest/api/token":
			_, _ = w.Write([]byte("imds-token"))
		case r.URL.Path == "/latest/meta-data/iam/security-credentials/":
			_, _ = w.Write([]byte("my-role\n"))
		case r.URL.Path == "/latest/meta-data/iam/security-credentials/my-role":
			_, _ = w.Write([]byte(`{"AccessKeyId":"ASIAROLE","SecretAccessKey":"` + testSecret + `","Token":"` + testSession + `"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &awsauth.IMDS{Base: srv.URL, Client: srv.Client()}
}

func TestECRRoleViaIMDS(t *testing.T) {
	var seen ecrSeen
	srv := ecrServer(t, "ASIAROLE", testSecret, testSession, &seen)
	p := newECRProvider(srv)
	p.IMDS = imdsServer(t)
	r := Registry{Kind: KindECR, ECR: ECR{Region: "eu-west-1", Mode: ECRRole}}
	res, err := p.Test(context.Background(), r)
	if err != nil || !res.OK || res.Host != testHost {
		t.Fatalf("%+v, %v", res, err)
	}
	if seen.accessKey != "ASIAROLE" {
		t.Errorf("signed with %q", seen.accessKey)
	}
}

func writeAWSFiles(t *testing.T, creds, config string) func() (string, string) {
	t.Helper()
	dir := t.TempDir()
	c, f := filepath.Join(dir, "credentials"), filepath.Join(dir, "config")
	if err := os.WriteFile(c, []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return func() (string, string) { return c, f }
}

func TestECRProfile(t *testing.T) {
	var seen ecrSeen
	srv := ecrServer(t, "AKIDPROFILE", testSecret, "", &seen)
	p := newECRProvider(srv)
	p.Paths = writeAWSFiles(t, "[dev]\naws_access_key_id = AKIDPROFILE\naws_secret_access_key = "+testSecret+"\n", "[profile dev]\nregion = eu-west-1\n")
	// The record carries no region: it comes from the profile.
	r := Registry{Kind: KindECR, ECR: ECR{Mode: ECRProfile, Profile: "dev"}}
	res, err := p.Test(context.Background(), r)
	if err != nil || !res.OK || res.Host != testHost {
		t.Fatalf("%+v, %v", res, err)
	}
	if seen.accessKey != "AKIDPROFILE" || seen.token != "" {
		t.Errorf("seen: %+v", seen)
	}
}

func TestECRProfileUnsupportedMessage(t *testing.T) {
	var seen ecrSeen
	srv := ecrServer(t, "x", "x", "", &seen)
	p := newECRProvider(srv)
	p.Paths = writeAWSFiles(t, "", "[profile x]\nrole_arn = arn:aws:iam::1:role/r\nsource_profile = y\nregion = eu-west-1\n")
	r := Registry{Kind: KindECR, ECR: ECR{Region: "eu-west-1", Mode: ECRProfile, Profile: "x"}}
	res, err := p.Test(context.Background(), r)
	if err != nil || res.OK || res.Message != `profile "x" uses role_arn, which is not supported; use static keys` {
		t.Fatalf("%+v, %v", res, err)
	}
	if seen.calls != 0 {
		t.Error("ECR must not be called for an unsupported profile")
	}
	if _, err := p.Credentials(context.Background(), r); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("credentials error: %v", err)
	}
}

func TestECRHostFromProxyEndpoint(t *testing.T) {
	var seen ecrSeen
	srv := ecrServer(t, "AKIDEXAMPLE", testSecret, testSession, &seen)
	res, err := newECRProvider(srv).Test(context.Background(), staticECR())
	if err != nil || res.Host != testHost || strings.Contains(res.Host, "://") {
		t.Fatalf("%+v, %v", res, err)
	}
	if got := ecrAccountID(res.Host); got != testAccount {
		t.Errorf("account = %q", got)
	}
}

// Review Focus 3: AWS error bodies can echo the request, including keys.
func TestECRErrorRedacts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"__type":"InvalidSignatureException","message":"bad key AKIDEXAMPLE secret ` + testSecret + ` token ` + testSession + ` auth ` + r.Header.Get("Authorization") + `"}`))
	}))
	defer srv.Close()
	res, err := newECRProvider(srv).Test(context.Background(), staticECR())
	if err == nil {
		t.Fatalf("want error, got %+v", res)
	}
	for _, text := range []string{err.Error(), res.Message} {
		for _, leak := range []string{testSecret, testSession, "AKIDEXAMPLE", "Signature="} {
			if strings.Contains(text, leak) {
				t.Errorf("leaked %q in %q", leak, text)
			}
		}
	}
	if _, err := newECRProvider(srv).Credentials(context.Background(), staticECR()); err == nil || strings.Contains(err.Error(), testSecret) {
		t.Errorf("credentials error: %v", err)
	}
}

func TestECRHostRequirements(t *testing.T) {
	base := staticECR()
	if base.configured() {
		t.Error("ECR without account or host must not be configured")
	}
	withAccount := base
	withAccount.ECR.AccountID = testAccount
	if !withAccount.configured() {
		t.Error("account id and region are enough to build the host")
	}
	withHost := base
	withHost.Host = testHost
	if !withHost.configured() {
		t.Error("a discovered host is enough")
	}
	if got := PushRef(base, "app", "1"); strings.HasPrefix(got, "/") {
		t.Errorf("PushRef without host = %q", got)
	}
	if got := PushRef(withAccount, "app", "1"); got != testHost+"/app:1" {
		t.Errorf("PushRef from account = %q", got)
	}
}

func TestServiceTestStoresDiscoveredECRHost(t *testing.T) {
	var seen ecrSeen
	srv := ecrServer(t, "AKIDEXAMPLE", testSecret, testSession, &seen)
	svc := NewService(newTestStore(t), map[Kind]Provider{KindECR: newECRProvider(srv)}, nil, slog.New(slog.DiscardHandler))
	ctx := context.Background()
	e := staticECR().ECR
	created, err := svc.Create(ctx, "u", Request{Name: "ecr", Kind: KindECR, ECR: &e})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Credentials(ctx, created.ID); err == nil {
		t.Fatal("credentials before the first test must be unconfigured")
	}
	if res, err := svc.Test(ctx, created.ID); err != nil || !res.OK {
		t.Fatalf("%+v, %v", res, err)
	}
	stored, _ := svc.store.Get(ctx, created.ID)
	if stored.Host != testHost || stored.ECR.AccountID != testAccount {
		t.Fatalf("host/account not stored: %q %q", stored.Host, stored.ECR.AccountID)
	}
	auth, host, err := svc.Credentials(ctx, created.ID)
	if err != nil || host != testHost || auth.Password != testPassword {
		t.Errorf("%+v %q %v", auth, host, err)
	}
}
