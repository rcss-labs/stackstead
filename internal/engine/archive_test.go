package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCreateContainerSendsMountsAndNoNetwork(t *testing.T) {
	c := opsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1.41/containers/create" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		var got struct {
			Image      string
			Cmd        []string
			Labels     map[string]string
			HostConfig struct {
				NetworkMode string
				Mounts      []struct {
					Type, Source, Target string
					ReadOnly             bool
				}
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		if got.Image != "busybox:1" || len(got.Cmd) != 2 || got.Cmd[0] != "sleep" {
			t.Errorf("image/cmd = %q %q", got.Image, got.Cmd)
		}
		if got.HostConfig.NetworkMode != "none" {
			t.Errorf("NetworkMode = %q", got.HostConfig.NetworkMode)
		}
		if got.Labels["dev.stackstead.backup-helper"] != "true" || got.Labels["extra"] != "v" {
			t.Errorf("labels = %v", got.Labels)
		}
		if len(got.HostConfig.Mounts) != 1 {
			t.Fatalf("mounts = %v", got.HostConfig.Mounts)
		}
		m := got.HostConfig.Mounts[0]
		if m.Type != "volume" || m.Source != "data" || m.Target != "/data" || !m.ReadOnly {
			t.Errorf("mount = %+v", m)
		}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"Id":"abc123","Warnings":[]}`)
	})
	id, err := c.CreateContainer(context.Background(), HelperSpec{
		Image:  "busybox:1",
		Mounts: []HelperMount{{Type: "volume", Source: "data", Target: "/data", ReadOnly: true}},
		Cmd:    []string{"sleep", "60"},
		Labels: map[string]string{"extra": "v"},
	})
	if err != nil || id != "abc123" {
		t.Fatalf("id=%q err=%v", id, err)
	}
}

func TestGetArchiveStreams(t *testing.T) {
	const total = 10 << 20
	const chunk = 64 << 10
	release := make(chan struct{})
	c := opsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1.41/containers/c1/archive" || r.URL.Query().Get("path") != "/data/." {
			t.Errorf("got %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		buf := bytes.Repeat([]byte{'x'}, chunk)
		for sent := 0; sent < total; sent += chunk {
			w.Write(buf)
			w.(http.Flusher).Flush()
			if sent == 0 {
				<-release // the client must read the first chunk before the fake finishes
			}
		}
	})
	rc, err := c.GetArchive(context.Background(), "c1", "/data/.")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	first := make([]byte, chunk)
	if _, err := io.ReadFull(rc, first); err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	close(release)
	rest, err := io.Copy(io.Discard, rc)
	if err != nil || rest+chunk != total {
		t.Fatalf("read %d more bytes, err=%v", rest, err)
	}
}

func TestPutArchiveStreamsBody(t *testing.T) {
	var gotBody []byte
	c := opsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/v1.41/containers/c1/archive" || r.URL.Query().Get("path") != "/data" {
			t.Errorf("got %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-tar" {
			t.Errorf("content-type = %q", ct)
		}
		gotBody, _ = io.ReadAll(r.Body)
	})
	// An io.Pipe has no length, so a pass proves the body is streamed rather than buffered.
	pr, pw := io.Pipe()
	go func() { io.WriteString(pw, "tar-bytes"); pw.Close() }()
	if err := c.PutArchive(context.Background(), "c1", "/data", pr); err != nil {
		t.Fatal(err)
	}
	if string(gotBody) != "tar-bytes" {
		t.Errorf("body = %q", gotBody)
	}
}

func TestPutArchiveErrorStatus(t *testing.T) {
	c := opsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message":"No such container: c1"}`)
	})
	err := c.PutArchive(context.Background(), "c1", "/data", strings.NewReader("x"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestWaitContainerReturnsExitCode(t *testing.T) {
	c := opsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1.41/containers/c1/wait" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		io.WriteString(w, `{"StatusCode":3}`)
	})
	code, err := c.WaitContainer(context.Background(), "c1")
	if err != nil || code != 3 {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestRemoveContainerForce(t *testing.T) {
	c := opsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1.41/containers/c1" || r.URL.Query().Get("force") != "1" {
			t.Errorf("got %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.RemoveContainer(context.Background(), "c1", true); err != nil {
		t.Fatal(err)
	}
}

func TestPullImageReportsErrorLine(t *testing.T) {
	fail := false
	c := opsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1.41/images/create" ||
			r.URL.Query().Get("fromImage") != "busybox:1.37.0@sha256:abc" {
			t.Errorf("got %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		io.WriteString(w, `{"status":"Pulling from library/busybox","id":"1.37.0"}`)
		if fail {
			io.WriteString(w, `{"errorDetail":{"message":"manifest unknown"},"error":"manifest unknown"}`)
		}
	})
	var lines []string
	if err := c.PullImage(context.Background(), "busybox:1.37.0@sha256:abc", func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0] != "1.37.0: Pulling from library/busybox" {
		t.Errorf("lines = %q", lines)
	}
	fail = true
	err := c.PullImage(context.Background(), "busybox:1.37.0@sha256:abc", nil)
	if err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateVolumeSendsLabels(t *testing.T) {
	c := opsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1.41/volumes/create" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		var got struct {
			Name   string
			Labels map[string]string
		}
		json.NewDecoder(r.Body).Decode(&got)
		if got.Name != "vol" || got.Labels["a"] != "b" {
			t.Errorf("body = %+v", got)
		}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"Name":"vol"}`)
	})
	if err := c.CreateVolume(context.Background(), "vol", map[string]string{"a": "b"}); err != nil {
		t.Fatal(err)
	}
}
