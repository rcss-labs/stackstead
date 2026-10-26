package awsauth

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/secret"
)

var vectorTime = time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)

func vectorCreds() Credentials {
	return Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: secret.Secret("wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY")}
}

// AWS "Signature Version 4 signing process" documentation example.
func TestSignV4KnownVector(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	if err := SignV4(req, nil, vectorCreds(), "us-east-1", "iam", vectorTime); err != nil {
		t.Fatal(err)
	}
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request, SignedHeaders=content-type;host;x-amz-date, Signature=5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization\n got %s\nwant %s", got, want)
	}
	if req.Header.Get("X-Amz-Date") != "20150830T123600Z" {
		t.Errorf("X-Amz-Date = %q", req.Header.Get("X-Amz-Date"))
	}
}

// AWS SigV4 test suite: get-vanilla.
func TestSignV4GetVanillaSuite(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://example.amazonaws.com/", nil)
	if err := SignV4(req, nil, vectorCreds(), "us-east-1", "service", vectorTime); err != nil {
		t.Fatal(err)
	}
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization\n got %s\nwant %s", got, want)
	}
}

func TestSignV4SessionTokenHeader(t *testing.T) {
	c := vectorCreds()
	c.SessionToken = secret.Secret("tok/en")
	req, _ := http.NewRequest("GET", "https://example.amazonaws.com/", nil)
	if err := SignV4(req, nil, c, "us-east-1", "service", vectorTime); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("X-Amz-Security-Token") != "tok/en" {
		t.Errorf("token header = %q", req.Header.Get("X-Amz-Security-Token"))
	}
	if !strings.Contains(req.Header.Get("Authorization"), "SignedHeaders=host;x-amz-date;x-amz-security-token,") {
		t.Errorf("token header not signed: %s", req.Header.Get("Authorization"))
	}
}

func TestSignV4PayloadHash(t *testing.T) {
	sign := func(body string) string {
		req, _ := http.NewRequest("POST", "https://api.ecr.us-east-1.amazonaws.com/", nil)
		if err := SignV4(req, []byte(body), vectorCreds(), "us-east-1", "ecr", vectorTime); err != nil {
			t.Fatal(err)
		}
		return req.Header.Get("Authorization")
	}
	a, b := sign(`{"a":1}`), sign(`{"a":2}`)
	if a == b {
		t.Error("body must influence the signature")
	}
	if a != sign(`{"a":1}`) {
		t.Error("signing must be deterministic")
	}
}

func TestSignV4CanonicalQuerySorted(t *testing.T) {
	sig := func(u string) string {
		req, _ := http.NewRequest("GET", u, nil)
		if err := SignV4(req, nil, vectorCreds(), "us-east-1", "service", vectorTime); err != nil {
			t.Fatal(err)
		}
		return req.Header.Get("Authorization")
	}
	if sig("https://example.amazonaws.com/?b=2&a=1") != sig("https://example.amazonaws.com/?a=1&b=2") {
		t.Error("query order must not change the signature")
	}
}

func TestSignV4RejectsMissingKeys(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://example.amazonaws.com/", nil)
	if err := SignV4(req, nil, Credentials{}, "us-east-1", "s", vectorTime); err == nil {
		t.Fatal("expected error")
	}
}

// Pinned vector for an ECR-style request: session token, messy header
// whitespace and an unsorted, partly empty query. The signature was computed
// independently with a reference script.
func TestSignV4ECRStyleVector(t *testing.T) {
	c := vectorCreds()
	c.SessionToken = secret.Secret("tok/en+=")
	req, _ := http.NewRequest("POST", "https://api.ecr.eu-west-1.amazonaws.com/?c&b=x%20y&a=1", nil)
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AmazonEC2ContainerRegistry_V20150921.GetAuthorizationToken")
	req.Header.Set("X-Custom", "  a   b  c ")
	if err := SignV4(req, []byte("{}"), c, "eu-west-1", "ecr", vectorTime); err != nil {
		t.Fatal(err)
	}
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/eu-west-1/ecr/aws4_request, SignedHeaders=content-type;host;x-amz-date;x-amz-security-token;x-amz-target;x-custom, Signature=2f900099ab15d5dd406702a4eb241af146863f8aff558de046f5a0d63a969e89"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization\n got %s\nwant %s", got, want)
	}
}

func TestSignV4DropsStaleSecurityToken(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://example.amazonaws.com/", nil)
	req.Header.Set("X-Amz-Security-Token", "stale")
	if err := SignV4(req, nil, vectorCreds(), "us-east-1", "service", vectorTime); err != nil {
		t.Fatal(err)
	}
	if v := req.Header.Get("X-Amz-Security-Token"); v != "" {
		t.Errorf("stale token header kept: %q", v)
	}
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization = %s", got)
	}
}

func TestSignV4IgnoresHostHeaderEntry(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://example.amazonaws.com/", nil)
	req.Header.Set("Host", "evil.example.com")
	if err := SignV4(req, nil, vectorCreds(), "us-east-1", "service", vectorTime); err != nil {
		t.Fatal(err)
	}
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("Host header entry influenced the signature: %s", got)
	}
}

func TestSignV4MergesCaseVariantHeaders(t *testing.T) {
	sign := func(h http.Header) string {
		req, _ := http.NewRequest("GET", "https://example.amazonaws.com/", nil)
		for k, v := range h {
			req.Header[k] = v
		}
		if err := SignV4(req, nil, vectorCreds(), "us-east-1", "service", vectorTime); err != nil {
			t.Fatal(err)
		}
		return req.Header.Get("Authorization")
	}
	merged := sign(http.Header{"X-Custom": {"a", "b"}})
	split := sign(http.Header{"X-Custom": {"a"}, "x-custom": {"b"}})
	if merged != split {
		t.Errorf("case variants must merge:\n%s\n%s", merged, split)
	}
	if split == sign(http.Header{"X-Custom": {"b"}}) {
		t.Error("first variant was overwritten")
	}
}

func TestSignV4S3SetsContentSHA(t *testing.T) {
	body := []byte("hello")
	req, _ := http.NewRequest("PUT", "https://b.s3.us-east-1.amazonaws.com/a b/c.txt", nil)
	if err := SignV4(req, body, vectorCreds(), "us-east-1", "s3", vectorTime); err != nil {
		t.Fatal(err)
	}
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got := req.Header.Get("X-Amz-Content-Sha256"); got != want {
		t.Fatalf("x-amz-content-sha256 = %q, want %q", got, want)
	}
	if !strings.Contains(req.Header.Get("Authorization"), "x-amz-content-sha256") {
		t.Errorf("content sha header not signed: %s", req.Header.Get("Authorization"))
	}
}

func TestSignV4ECRUnchanged(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://api.ecr.us-east-1.amazonaws.com/", nil)
	if err := SignV4(req, []byte("{}"), vectorCreds(), "us-east-1", "ecr", vectorTime); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("X-Amz-Content-Sha256"); got != "" {
		t.Errorf("ECR request must not carry x-amz-content-sha256, got %q", got)
	}
	if strings.Contains(req.Header.Get("Authorization"), "x-amz-content-sha256") {
		t.Errorf("ECR signed headers changed: %s", req.Header.Get("Authorization"))
	}
}
