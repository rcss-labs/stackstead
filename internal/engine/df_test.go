package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDiskUsageKinds(t *testing.T) {
	for _, kind := range []DFKind{DFImages, DFContainers, DFVolumes, DFBuildCache} {
		t.Run(string(kind), func(t *testing.T) {
			var queries []string
			f := serveVer(t, "1.47", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				queries = append(queries, r.URL.RawQuery)
				body, err := os.ReadFile("testdata/system_df_" + string(kind) + ".json")
				if err != nil {
					t.Fatal(err)
				}
				w.Write(body)
			}))
			got, err := New(f.sock, time.Second).DiskUsage(ctx(t), kind)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Kinds, []DFKind{kind}) || !reflect.DeepEqual(queries, []string{"type=" + string(kind)}) {
				t.Fatalf("snapshot %+v queries %v", got, queries)
			}
			switch kind {
			case DFImages:
				if len(got.Images) != 2 || *got.Images[0].Size != 100 || *got.Images[0].SharedSize != 40 || got.Images[1].Size != nil {
					t.Fatalf("images %+v", got.Images)
				}
			case DFContainers:
				if len(got.Containers) != 1 || *got.Containers[0].SizeRw != 20 {
					t.Fatalf("containers %+v", got.Containers)
				}
			case DFVolumes:
				if len(got.Volumes) != 2 || got.Volumes[0].UsageData.Size != 30 || got.Volumes[0].UsageData.RefCount != 0 || got.Volumes[1].UsageData != nil {
					t.Fatalf("volumes %+v", got.Volumes)
				}
			case DFBuildCache:
				b := got.BuildCache[0]
				if b.ID != "cache" || b.Type != "regular" || b.Description != "RUN true" || *b.Size != 50 || !b.InUse || !b.Shared || b.UsageCount != 3 || b.LastUsedAt == nil || b.CreatedAt.IsZero() {
					t.Fatalf("cache %+v", b)
				}
			}
		})
	}
}
func TestDiskUsageFallback(t *testing.T) {
	for _, tc := range []struct {
		version string
		status  int
		want    []string
	}{{"1.44", 200, []string{""}}, {"1.47", 400, []string{"type=image", ""}}} {
		t.Run(tc.version, func(t *testing.T) {
			var queries []string
			f := serveVer(t, tc.version, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				queries = append(queries, r.URL.RawQuery)
				if r.URL.RawQuery != "" {
					w.WriteHeader(tc.status)
					return
				}
				body, _ := os.ReadFile("testdata/system_df_full.json")
				w.Write(body)
			}))
			got, err := New(f.sock, time.Second).DiskUsage(ctx(t), DFImages)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Kinds) != 4 || len(got.Images) != 2 || len(got.Containers) != 1 || len(got.Volumes) != 2 || len(got.BuildCache) != 1 || !reflect.DeepEqual(queries, tc.want) {
				t.Fatalf("snapshot %+v queries %v", got, queries)
			}
		})
	}
}
func TestDiskUsageInvalidAndError(t *testing.T) {
	c := New("", time.Second)
	if _, err := c.DiskUsage(context.Background(), DFKind("image&all=true")); err == nil || errors.Is(err, ErrUnavailable) {
		t.Fatalf("invalid kind: %v", err)
	}
	for _, status := range []int{400, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			c.version = "1.47"
			c.http = &http.Client{Transport: storageRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"message":"failure"}`))}, nil
			})}
			_, err := c.DiskUsage(context.Background(), DFVolumes)
			var action *ActionError
			if !errors.As(err, &action) || action.Status != status || calls != map[int]int{400: 2, 500: 1}[status] {
				t.Fatalf("calls %d error %v", calls, err)
			}
		})
	}
}
func TestBuildPruneCapability(t *testing.T) {
	for _, v := range []string{"1.47", "1.48", "1.49"} {
		c := New("", time.Second)
		c.version = v
		c.http = &http.Client{Transport: storageRoundTrip(func(*http.Request) (*http.Response, error) { t.Fatal("capability issued a request"); return nil, nil })}
		got, err := c.buildPruneByID(context.Background(), "1.48")
		if err != nil || got != (v != "1.47") {
			t.Fatalf("%s => %v %v", v, got, err)
		}
	}
	c := New("", time.Second)
	c.version = maxAPIVersion
	if got, err := c.BuildPruneByID(context.Background()); got || err != nil {
		t.Fatalf("capability must stay off: %v %v", got, err)
	}
}
func TestPruneBuildCache(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ids   []string
		bound time.Duration
	}{{"all", nil, 10 * time.Minute}, {"ids", []string{"a", "b"}, 10 * time.Minute}, {"caller", nil, time.Second}, {"empty", []string{}, 10 * time.Minute}} {
		t.Run(tc.name, func(t *testing.T) {
			c := New("", time.Millisecond)
			c.version = "1.47"
			calls := 0
			c.http = &http.Client{Transport: storageRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/v1.47/build/prune" || r.URL.Query().Get("all") != "true" {
					t.Fatalf("request %s %s", r.Method, r.URL)
				}
				dl, ok := r.Context().Deadline()
				if !ok || time.Until(dl) > tc.bound || time.Until(dl) < tc.bound-time.Second/2 {
					t.Errorf("deadline %v", time.Until(dl))
				}
				filter := r.URL.Query().Get("filters")
				if tc.ids == nil && filter != "" {
					t.Errorf("unexpected filter %s", filter)
				}
				if len(tc.ids) > 0 {
					var got map[string][]string
					if json.Unmarshal([]byte(filter), &got) != nil || !reflect.DeepEqual(got, map[string][]string{"id": tc.ids}) {
						t.Errorf("filter %s", filter)
					}
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"SpaceReclaimed":123}`))}, nil
			})}
			caller := context.Background()
			if tc.name == "caller" {
				var cancel context.CancelFunc
				caller, cancel = context.WithTimeout(caller, time.Second)
				defer cancel()
			}
			got, err := c.PruneBuildCache(caller, tc.ids)
			wantCalls, wantBytes := 1, int64(123)
			if tc.ids != nil && len(tc.ids) == 0 {
				wantCalls, wantBytes = 0, 0
			}
			if err != nil || got != wantBytes || calls != wantCalls {
				t.Fatalf("bytes %d calls %d err %v", got, calls, err)
			}
		})
	}
	c := New("", time.Second)
	if got, err := c.PruneBuildCache(context.Background(), []string{}); got != 0 || err != nil {
		t.Fatalf("empty ids negotiated: %d %v", got, err)
	}
}
