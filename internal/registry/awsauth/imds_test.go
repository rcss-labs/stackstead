package awsauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIMDSRoleCredentials(t *testing.T) {
	var sawPut bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PUT" && r.URL.Path == "/latest/api/token":
			if r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds") != "21600" {
				t.Errorf("ttl header = %q", r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds"))
			}
			sawPut = true
			_, _ = w.Write([]byte("TOKEN123"))
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/latest/meta-data/iam/security-credentials/"):
			if r.Header.Get("X-aws-ec2-metadata-token") != "TOKEN123" {
				http.Error(w, "unauthorized", 401)
				return
			}
			if r.URL.Path == "/latest/meta-data/iam/security-credentials/" {
				_, _ = w.Write([]byte("my-role\n"))
				return
			}
			if r.URL.Path != "/latest/meta-data/iam/security-credentials/my-role" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"Code":"Success","AccessKeyId":"AKIA1","SecretAccessKey":"sek","Token":"sess"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := (&IMDS{Base: srv.URL}).RoleCredentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !sawPut || c.AccessKeyID != "AKIA1" || c.SecretAccessKey.Reveal() != "sek" || c.SessionToken.Reveal() != "sess" {
		t.Fatalf("unexpected credentials: %+v", c)
	}
}

func TestIMDSNoRole(t *testing.T) {
	for name, listing := range map[string]func(http.ResponseWriter, *http.Request){
		"empty": func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("\n")) },
		"404":   func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "PUT" {
					_, _ = w.Write([]byte("T"))
					return
				}
				listing(w, r)
			}))
			defer srv.Close()
			_, err := (&IMDS{Base: srv.URL}).RoleCredentials(context.Background())
			if !errors.Is(err, ErrNoRole) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestIMDSUnreachableFailsFast(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	_, err := (&IMDS{Base: base}).RoleCredentials(context.Background())
	if err == nil || errors.Is(err, ErrNoRole) {
		t.Fatalf("err = %v", err)
	}
}

func TestIMDSErrorOmitsSecrets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			_, _ = w.Write([]byte("T"))
			return
		}
		if r.URL.Path == "/latest/meta-data/iam/security-credentials/" {
			_, _ = w.Write([]byte("r"))
			return
		}
		_, _ = w.Write([]byte(`{"AccessKeyId":"AKIA1","SecretAccessKey":"topsecret"`)) // malformed
	}))
	defer srv.Close()
	_, err := (&IMDS{Base: srv.URL}).RoleCredentials(context.Background())
	if err == nil || strings.Contains(err.Error(), "topsecret") {
		t.Fatalf("err = %v", err)
	}
}
