package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func opsClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	return New(serve(t, h), 2*time.Second)
}

func TestBuildSendsContextAndOptions(t *testing.T) {
	tarBytes := []byte("fake-tar-bytes")
	var gotBody []byte
	c := opsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1.41/build" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		if got := q["t"]; len(got) != 2 || got[0] != "a:1" || got[1] != "a:latest" {
			t.Errorf("t = %v", got)
		}
		if q.Get("dockerfile") != "sub/Dockerfile" || q.Get("rm") != "1" || q.Get("forcerm") != "1" {
			t.Errorf("query = %v", q)
		}
		var args, labels map[string]string
		if err := json.Unmarshal([]byte(q.Get("buildargs")), &args); err != nil || args["K"] != "V" {
			t.Errorf("buildargs = %q", q.Get("buildargs"))
		}
		if err := json.Unmarshal([]byte(q.Get("labels")), &labels); err != nil || labels["l"] != "v" {
			t.Errorf("labels = %q", q.Get("labels"))
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-tar" {
			t.Errorf("content-type = %q", ct)
		}
		gotBody, _ = io.ReadAll(r.Body)
		io.WriteString(w, `{"stream":"Step 1/2 : FROM x\n"}{"stream":"ok\n"}{"aux":{"ID":"sha256:abc"}}`)
	})
	var lines []string
	res, err := c.Build(context.Background(), bytes.NewReader(tarBytes), BuildOptions{
		Tags: []string{"a:1", "a:latest"}, Dockerfile: "sub/Dockerfile",
		BuildArgs: map[string]string{"K": "V"}, Labels: map[string]string{"l": "v"},
	}, func(l string) { lines = append(lines, l) })
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBody, tarBytes) {
		t.Errorf("body = %q", gotBody)
	}
	if strings.Join(lines, "|") != "Step 1/2 : FROM x|ok" {
		t.Errorf("lines = %q", lines)
	}
	if res.ImageID != "sha256:abc" {
		t.Errorf("id = %q", res.ImageID)
	}
}

func TestBuildErrorMessage(t *testing.T) {
	c := opsClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"stream":"Step 1/1\n"}`+"\n"+`{"errorDetail":{"message":"boom"},"error":"boom"}`+"\n")
	})
	var lines []string
	_, err := c.Build(context.Background(), strings.NewReader("t"), BuildOptions{}, func(l string) { lines = append(lines, l) })
	var be *BuildError
	if !errors.As(err, &be) || be.Message != "boom" || err.Error() != "build failed: boom" {
		t.Fatalf("err = %v", err)
	}
	if len(lines) != 1 || lines[0] != "Step 1/1" {
		t.Errorf("lines = %q", lines)
	}
}

func TestPushAuthHeaderAndDigest(t *testing.T) {
	c := opsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1.41/images/h:5000/o/img/push" || r.URL.Query().Get("tag") != "2" {
			t.Errorf("got %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		raw, err := base64.URLEncoding.DecodeString(r.Header.Get("X-Registry-Auth"))
		if err != nil {
			t.Errorf("header: %v", err)
		}
		var got map[string]string
		json.Unmarshal(raw, &got)
		if got["username"] != "u" || got["password"] != "p" || got["serveraddress"] != "h" {
			t.Errorf("auth = %v", got)
		}
		io.WriteString(w, `{"status":"Pushing","id":"l1"}{"aux":{"Digest":"sha256:d"}}`)
	})
	var lines []string
	digest, err := c.Push(context.Background(), "h:5000/o/img:2", RegistryAuth{Username: "u", Password: "p", ServerAddress: "h"}, func(l string) { lines = append(lines, l) })
	if err != nil {
		t.Fatal(err)
	}
	if digest != "sha256:d" || len(lines) != 1 || lines[0] != "l1: Pushing" {
		t.Errorf("digest=%q lines=%q", digest, lines)
	}
}

func TestPushErrorNoSecretInError(t *testing.T) {
	c := opsClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"error":"denied"}`)
	})
	_, err := c.Push(context.Background(), "h/o/img:1", RegistryAuth{Username: "u", Password: "s3cretpw", ServerAddress: "h"}, nil)
	if err == nil || err.Error() != "push failed: denied" || strings.Contains(err.Error(), "s3cretpw") {
		t.Fatalf("err = %v", err)
	}
}

func TestTagImageQuery(t *testing.T) {
	c := opsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1.41/images/sha256:abc/tag" ||
			r.URL.Query().Get("repo") != "h/o/img" || r.URL.Query().Get("tag") != "3" {
			t.Errorf("got %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusCreated)
	})
	if err := c.TagImage(context.Background(), "sha256:abc", "h/o/img", "3"); err != nil {
		t.Fatal(err)
	}
}

func TestSaveImageStreamsBody(t *testing.T) {
	release := make(chan struct{})
	c := opsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1.41/images/h/o/img:1/get" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		io.WriteString(w, "first")
		w.(http.Flusher).Flush()
		<-release
		io.WriteString(w, "second")
	})
	// A client timeout shorter than the body proves the body is bound by ctx only.
	c.timeout = 300 * time.Millisecond
	rc, err := c.SaveImage(context.Background(), "h/o/img:1")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	buf := make([]byte, 5)
	if _, err := io.ReadFull(rc, buf); err != nil || string(buf) != "first" {
		t.Fatalf("read %q %v", buf, err)
	}
	time.Sleep(600 * time.Millisecond)
	close(release)
	rest, err := io.ReadAll(rc)
	if err != nil || string(rest) != "second" {
		t.Fatalf("rest %q %v", rest, err)
	}
}

func TestLoadImageSendsTarAndErrors(t *testing.T) {
	var gotBody []byte
	fail := false
	c := opsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1.41/images/load" || r.URL.Query().Get("quiet") != "1" {
			t.Errorf("got %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		gotBody, _ = io.ReadAll(r.Body)
		if fail {
			io.WriteString(w, `{"error":"bad archive"}`)
			return
		}
		io.WriteString(w, `{"stream":"Loaded image: x\n"}`)
	})
	if err := c.LoadImage(context.Background(), strings.NewReader("tarbytes")); err != nil {
		t.Fatal(err)
	}
	if string(gotBody) != "tarbytes" {
		t.Errorf("body = %q", gotBody)
	}
	fail = true
	err := c.LoadImage(context.Background(), strings.NewReader("x"))
	if err == nil || !strings.Contains(err.Error(), "bad archive") {
		t.Fatalf("err = %v", err)
	}
}

func TestArch(t *testing.T) {
	c := opsClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"Version":"27.1.1","Arch":"arm64"}`)
	})
	got, err := c.Arch(context.Background())
	if err != nil || got != "arm64" {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestImageOps404(t *testing.T) {
	c := opsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message":"No such image: x"}`)
	})
	ctx := context.Background()
	if err := c.TagImage(ctx, "x", "r", "t"); !errors.Is(err, ErrImageNotFound) {
		t.Errorf("tag: %v", err)
	}
	if _, err := c.SaveImage(ctx, "x"); !errors.Is(err, ErrImageNotFound) {
		t.Errorf("save: %v", err)
	}
	if _, err := c.Push(ctx, "x:1", RegistryAuth{}, nil); !errors.Is(err, ErrImageNotFound) {
		t.Errorf("push: %v", err)
	}
}

func TestJSONStreamLineTooLong(t *testing.T) {
	huge := `{"stream":"` + strings.Repeat("a", 1<<20+10) + `"}` + "\n"
	err := decodeJSONStream(strings.NewReader(huge), func(jsonMessage) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("err = %v", err)
	}
}

// Docker may answer before it has read the whole upload. The late
// WroteRequest callback must not re-arm the header timer mid-stream.
func TestBuildSurvivesHeadersBeforeUploadEnds(t *testing.T) {
	c := opsClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"stream":"started\n"}`)
		w.(http.Flusher).Flush()
		time.Sleep(1200 * time.Millisecond)
		io.WriteString(w, `{"aux":{"ID":"sha256:late"}}`)
	})
	c.timeout = 300 * time.Millisecond
	pr, pw := io.Pipe()
	go func() {
		pw.Write([]byte("part"))
		time.Sleep(600 * time.Millisecond) // longer than the header timeout
		pw.Close()
	}()
	res, err := c.Build(context.Background(), pr, BuildOptions{}, nil)
	if err != nil || res.ImageID != "sha256:late" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestBuildErrorOmitsQueryValues(t *testing.T) {
	c := opsClient(t, func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	})
	_, err := c.Build(context.Background(), strings.NewReader("t"), BuildOptions{
		BuildArgs: map[string]string{"TOKEN": "s3cretvalue"}, Labels: map[string]string{"l": "s3cretlabel"},
	}, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, leak := range []string{"s3cretvalue", "s3cretlabel", "buildargs", "TOKEN"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error leaks %q: %v", leak, err)
		}
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
}

func TestPushWithoutDigestFails(t *testing.T) {
	c := opsClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"status":"Pushing","id":"l1"}`)
	})
	digest, err := c.Push(context.Background(), "h/o/img:1", RegistryAuth{}, nil)
	if err == nil || digest != "" {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
}
