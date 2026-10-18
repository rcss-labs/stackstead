package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestImagesDecode(t *testing.T) {
	c := New(serve(t, fileHandler(t, map[string]string{"/v1.41/images/json": "images.json"})), time.Second)
	images, err := c.Images(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 2 {
		t.Fatalf("count = %d", len(images))
	}
	image := images[0]
	if image.ID != "sha256:img" || !reflect.DeepEqual(image.RepoTags, []string{"nginx:1.25"}) || !reflect.DeepEqual(image.RepoDigests, []string{"nginx@sha256:digest"}) || image.Size != 1234 || image.Labels["owner"] != "demo" || !image.Created.Equal(time.Unix(1760000000, 0)) {
		t.Errorf("image = %+v", image)
	}
	if len(images[1].RepoTags) != 0 || !images[1].Created.IsZero() {
		t.Errorf("dangling = %+v", images[1])
	}
}

func TestInspectImageOmitsEnv(t *testing.T) {
	c := New(serve(t, fileHandler(t, map[string]string{"/v1.41/images/sha256:img/json": "image-inspect.json"})), time.Second)
	image, err := c.InspectImage(ctx(t), "sha256:img")
	if err != nil {
		t.Fatal(err)
	}
	if image.ID != "sha256:img" || !reflect.DeepEqual(image.RepoTags, []string{"nginx:1.25"}) || len(image.RepoDigests) != 1 || image.Size != 1234 || image.User != "1000" || image.Labels["owner"] != "demo" || image.OS != "linux" || image.Architecture != "arm64" || image.Variant != "v8" || image.LayerCount != 2 || !image.Created.Equal(time.Date(2026, 10, 18, 12, 34, 56, 0, time.UTC)) {
		t.Errorf("safe image fields differ")
	}
	assertNoFixtureSecrets(t, reflect.ValueOf(image))
}

func TestVolumesDecode(t *testing.T) {
	c := New(serve(t, fileHandler(t, map[string]string{"/v1.41/volumes": "volumes.json", "/v1.41/volumes/demo-data": "volume-inspect.json"})), time.Second)
	volumes, err := c.Volumes(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(volumes) != 1 {
		t.Fatalf("count = %d", len(volumes))
	}
	checkVolume(t, volumes[0])
}
func TestInspectVolumeOmitsOptions(t *testing.T) {
	c := New(serve(t, fileHandler(t, map[string]string{"/v1.41/volumes/demo-data": "volume-inspect.json"})), time.Second)
	volume, err := c.InspectVolume(ctx(t), "demo-data")
	if err != nil {
		t.Fatal(err)
	}
	checkVolume(t, volume)
}
func checkVolume(t *testing.T, v Volume) {
	t.Helper()
	if v.Name != "demo-data" || v.Driver != "local" || v.Scope != "local" || v.Mountpoint != "/var/lib/docker/volumes/demo-data/_data" || v.Labels["owner"] != "demo" || v.OptionType != "nfs" || !v.CreatedAt.Equal(time.Date(2026, 10, 18, 12, 34, 56, 0, time.UTC)) {
		t.Error("safe volume fields differ")
	}
	assertNoFixtureSecrets(t, reflect.ValueOf(v))
}
func assertNoFixtureSecrets(t *testing.T, v reflect.Value) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		if strings.Contains(v.String(), "fixture-") {
			t.Error("secret-bearing fixture value retained")
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			assertNoFixtureSecrets(t, v.Field(i))
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			assertNoFixtureSecrets(t, v.Index(i))
		}
	case reflect.Map:
		for _, key := range v.MapKeys() {
			assertNoFixtureSecrets(t, key)
			assertNoFixtureSecrets(t, v.MapIndex(key))
		}
	case reflect.Interface, reflect.Pointer:
		if !v.IsNil() {
			assertNoFixtureSecrets(t, v.Elem())
		}
	}
}

func TestContainersDecodeStorageReferences(t *testing.T) {
	c := New(serve(t, fileHandler(t, map[string]string{"/v1.41/containers/json": "containers-storage.json"})), time.Second)
	containers, err := c.Containers(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 1 {
		t.Fatalf("count = %d", len(containers))
	}
	container := containers[0]
	if container.ImageID != "sha256:img" || len(container.Mounts) != 2 {
		t.Fatalf("storage references = %+v", container)
	}
	if container.Mounts[0].Name != "demo-data" || container.Mounts[0].Type != "volume" || container.Mounts[0].Source != "/volumes/data" || container.Mounts[0].Destination != "/data" || container.Mounts[0].ReadOnly || container.Mounts[1].Name != "" || !container.Mounts[1].ReadOnly {
		t.Errorf("mounts = %+v", container.Mounts)
	}
}

func TestRemoveImageByRefNeverForces(t *testing.T) {
	for _, ref := range []string{"nginx:1.25", "registry.example/team/nginx@sha256:abc"} {
		t.Run(ref, func(t *testing.T) {
			c := New(serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || r.URL.RawQuery != "" || r.URL.Path != "/v1.41/images/"+ref {
					t.Errorf("request = %s %s", r.Method, r.URL)
				}
				if strings.Contains(ref, ":") && !strings.Contains(r.URL.EscapedPath(), "%3A") {
					t.Errorf("path not escaped: %s", r.URL.EscapedPath())
				}
				io.WriteString(w, `[{"Untagged":"nginx:1.25"},{"Deleted":"sha256:img"}]`)
			})), time.Second)
			result, err := c.RemoveImage(ctx(t), ref)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Untagged, []string{"nginx:1.25"}) || !reflect.DeepEqual(result.Deleted, []string{"sha256:img"}) {
				t.Errorf("result = %+v", result)
			}
		})
	}
}

func TestRemoveVolumeNeverForces(t *testing.T) {
	c := New(serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.EscapedPath() != "/v1.41/volumes/demo-data" || r.URL.RawQuery != "" {
			t.Errorf("request = %s %s", r.Method, r.URL)
		}
		w.WriteHeader(http.StatusNoContent)
	})), time.Second)
	if err := c.RemoveVolume(ctx(t), "demo-data"); err != nil {
		t.Fatal(err)
	}
}

func TestStorageStatusMapping(t *testing.T) {
	for _, status := range []int{404, 409, 500} {
		for _, kind := range []string{"image-remove", "volume-remove", "image-inspect", "volume-inspect"} {
			t.Run(fmt.Sprintf("%s/%d", kind, status), func(t *testing.T) {
				c := New(serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(status)
					io.WriteString(w, `{"message":"resource conflict"}`)
				})), time.Second)
				var err error
				switch kind {
				case "image-remove":
					_, err = c.RemoveImage(ctx(t), "img")
				case "volume-remove":
					err = c.RemoveVolume(ctx(t), "vol")
				case "image-inspect":
					_, err = c.InspectImage(ctx(t), "img")
				case "volume-inspect":
					_, err = c.InspectVolume(ctx(t), "vol")
				}
				var ae *ActionError
				if !errors.As(err, &ae) || ae.Status != status || ae.Message != "resource conflict" {
					t.Fatalf("error = %v", err)
				}
				sentinel := ErrImageNotFound
				if strings.HasPrefix(kind, "volume") {
					sentinel = ErrVolumeNotFound
				}
				if errors.Is(err, sentinel) != (status == 404) {
					t.Errorf("not-found mapping = %v", err)
				}
			})
		}
	}
}

func TestStorageRemovalDeadline(t *testing.T) {
	for _, kind := range []string{"image", "volume"} {
		t.Run(kind, func(t *testing.T) {
			c := New("", time.Millisecond)
			c.version = "1.41"
			c.http = &http.Client{Transport: storageRoundTrip(func(r *http.Request) (*http.Response, error) {
				dl, ok := r.Context().Deadline()
				if !ok || time.Until(dl) < 59*time.Second || time.Until(dl) > 60*time.Second {
					t.Error("remove must use its own 60s deadline")
				}
				if r.Method != http.MethodDelete || r.URL.RawQuery != "" {
					t.Errorf("request = %s %s", r.Method, r.URL)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("[]")), Header: make(http.Header)}, nil
			})}
			if kind == "image" {
				_, err := c.RemoveImage(context.Background(), "img")
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if err := c.RemoveVolume(context.Background(), "vol"); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestStorageRemovalHonorsCaller(t *testing.T) {
	for _, kind := range []string{"image", "volume"} {
		t.Run(kind, func(t *testing.T) {
			c := New(serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })), time.Second)
			caller, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			var err error
			if kind == "image" {
				_, err = c.RemoveImage(caller, "img")
			} else {
				err = c.RemoveVolume(caller, "vol")
			}
			if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrUnavailable) {
				t.Errorf("caller error = %v", err)
			}
		})
	}
}

type storageRoundTrip func(*http.Request) (*http.Response, error)

func (f storageRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
