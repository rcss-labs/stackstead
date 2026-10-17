package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type releaseTransport func(*http.Request) (*http.Response, error)

func (f releaseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body []byte, status int) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}
}
func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func nativeManifest(v string) Manifest {
	m := releaseManifest(v)
	m.Assets = []AssetSpec{{OS: "linux", Arch: "amd64", Installer: InstallerScript, Name: "stackstead_" + v + "_linux_amd64.tar.gz"}}
	return m
}
func registration() Registration {
	return Registration{ID: "test", UpdateEnabled: true, Installer: InstallerScript, Identity: Identity{Version: "1.0.0", Commit: strings.Repeat("a", 40), OS: "linux", Arch: "amd64"}}
}
func archive(t *testing.T, headers []*tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			tw.Write(bytes.Repeat([]byte("x"), int(h.Size)))
		}
	}
	tw.Close()
	gz.Close()
	return b.Bytes()
}
func releaseFixture(t *testing.T) (*ReleaseClient, map[string][]byte, *int) {
	t.Helper()
	files := map[string][]byte{}
	for _, v := range []string{"1.0.0", "2.0.0", "3.0.0"} {
		m := nativeManifest(v)
		data := manifestJSON(t, m)
		payload := archive(t, []*tar.Header{{Name: "stackstead", Typeflag: tar.TypeReg, Mode: 0755, Size: 1}})
		base := releaseDownload + "v" + v + "/"
		files[base+"stackstead-update.json"] = data
		files[base+m.Assets[0].Name] = payload
		files[base+"SHA256SUMS"] = []byte(fmt.Sprintf("%s  stackstead-update.json\n%s  %s\n", digest(data), digest(payload), m.Assets[0].Name))
	}
	latest := []byte(`{"tag_name":"v2.0.0","draft":false,"prerelease":false}`)
	files[latestReleaseURL] = latest
	requests := new(int)
	c := NewReleaseClient(&http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
		*requests++
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		body, ok := files[r.URL.String()]
		if !ok {
			return response(nil, 404), nil
		}
		return response(body, 200), nil
	})})
	c.stagingRoot = t.TempDir()
	c.checkTrustedRoot = func(string) error { return nil }
	return c, files, requests
}
func TestReleasePinsConfirmedCandidate(t *testing.T) {
	c, files, _ := releaseFixture(t)
	reg := registration()
	candidate, err := c.Check(context.Background(), reg)
	if err != nil {
		t.Fatal(err)
	}
	files[latestReleaseURL] = []byte(`{"tag_name":"v3.0.0"}`)
	plan, err := c.Prepare(context.Background(), reg, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Target.Identity.Version != "2.0.0" || plan.TargetArtifact.SHA256 == "" || plan.PreviousArtifact.Path == "" {
		t.Fatalf("candidate changed or artifacts unverified: %+v", plan)
	}
	files[releaseDownload+"v2.0.0/stackstead-update.json"] = manifestJSON(t, nativeManifest("3.0.0"))
	if _, err = c.Prepare(context.Background(), reg, candidate); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("changed metadata accepted: %v", err)
	}
}
func TestReleaseOutageIsNotUpToDate(t *testing.T) {
	for _, status := range []int{429, 500, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c := NewReleaseClient(&http.Client{Transport: releaseTransport(func(*http.Request) (*http.Response, error) { return response(nil, status), nil })})
			if _, err := c.Check(context.Background(), registration()); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("outage lost: %v", err)
			}
		})
	}
}
func TestPrepareRejectsMissingPreviousMetadata(t *testing.T) {
	c, files, _ := releaseFixture(t)
	candidate, err := c.Check(context.Background(), registration())
	if err != nil {
		t.Fatal(err)
	}
	delete(files, releaseDownload+"v1.0.0/stackstead-update.json")
	if _, err = c.Prepare(context.Background(), registration(), candidate); err == nil {
		t.Fatal("missing rollback metadata accepted")
	}
}
func TestReleaseNoUpdateAndUnstable(t *testing.T) {
	for _, v := range []string{"1.0.0", "0.9.0", "2.0.0-rc.1", "dev"} {
		t.Run(v, func(t *testing.T) {
			c, files, _ := releaseFixture(t)
			data, _ := json.Marshal(map[string]string{"tag_name": "v" + v})
			files[latestReleaseURL] = data
			candidate, err := c.Check(context.Background(), registration())
			if strings.Contains(v, "-") || v == "dev" {
				if !errors.Is(err, ErrIncompatible) {
					t.Fatalf("unstable accepted: %v", err)
				}
			} else if err != nil || candidate.ID != "" {
				t.Fatalf("not newer: %+v %v", candidate, err)
			}
		})
	}
}
func TestReleaseCacheExpiryAndCancellation(t *testing.T) {
	c, _, requests := releaseFixture(t)
	now := time.Now()
	c.now = func() time.Time { return now }
	first, err := c.Check(context.Background(), registration())
	if err != nil {
		t.Fatal(err)
	}
	count := *requests
	if _, err = c.Check(context.Background(), registration()); err != nil || *requests != count {
		t.Fatalf("cache miss: %v", err)
	}
	now = now.Add(6 * time.Minute)
	if _, err = c.Check(context.Background(), registration()); err != nil || *requests == count {
		t.Fatalf("cache never expires: %v", err)
	}
	now = now.Add(10 * time.Minute)
	if _, err = c.Prepare(context.Background(), registration(), first); !errors.Is(err, ErrCandidateExpired) {
		t.Fatalf("expired accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = c.Check(ctx, registration()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel lost: %v", err)
	}
}

func TestReleaseRejectsUnsafeRedirectsAndDropsCredentials(t *testing.T) {
	for _, address := range []string{"http://github.com/file", "https://evil.example/file", "https://user:password@github.com/file", "https://github.com:8443/file"} {
		t.Run(address, func(t *testing.T) {
			calls := 0
			c := NewReleaseClient(&http.Client{Transport: releaseTransport(func(*http.Request) (*http.Response, error) {
				calls++
				r := response(nil, 302)
				r.Header.Set("Location", address)
				return r, nil
			})})
			if _, err := c.Check(context.Background(), registration()); err == nil || calls != 1 {
				t.Fatalf("unsafe redirect followed: %v requests=%d", err, calls)
			}
		})
	}
	for _, host := range []string{"github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com"} {
		t.Run(host, func(t *testing.T) {
			calls := 0
			c := NewReleaseClient(&http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					r.Header.Set("Authorization", "private")
					r.Header.Set("Cookie", "private")
					r.Header.Set("Proxy-Authorization", "private")
					res := response(nil, 302)
					res.Header.Set("Location", "https://"+host+"/release")
					return res, nil
				}
				for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie"} {
					if r.Header.Get(name) != "" {
						t.Errorf("forwarded %s", name)
					}
				}
				return response([]byte(`{"tag_name":"v1.0.0"}`), 200), nil
			})})
			if _, err := c.Check(context.Background(), registration()); err != nil || calls != 2 {
				t.Fatalf("allowed redirect failed: %v requests=%d", err, calls)
			}
		})
	}
}
func TestReleaseTimeoutAndDocumentBound(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		c := NewReleaseClient(&http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
			deadline, ok := r.Context().Deadline()
			if !ok || time.Until(deadline) > 30*time.Second {
				t.Error("request lacks 30s bound")
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		})})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		defer cancel()
		if _, err := c.Check(ctx, registration()); !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("timeout lost: %v", err)
		}
	})
	t.Run("bounded document", func(t *testing.T) {
		c := NewReleaseClient(&http.Client{Transport: releaseTransport(func(*http.Request) (*http.Response, error) {
			return response(bytes.Repeat([]byte("x"), int(documentLimit+1)), 200), nil
		})})
		if _, err := c.Check(context.Background(), registration()); !errors.Is(err, ErrIncompatible) {
			t.Fatalf("oversize accepted: %v", err)
		}
	})
}
func TestPrepareRejectsIncompatibleOrAlteredCandidateBeforeArtifactDownload(t *testing.T) {
	for _, change := range []string{"current-commit", "target-protocol", "current-protocol", "selected-platform", "forged-expiry", "mutated-manifest", "wrong-artifact-checksum"} {
		t.Run(change, func(t *testing.T) {
			c, files, _ := releaseFixture(t)
			reg := registration()
			candidate, err := c.Check(context.Background(), reg)
			if err != nil {
				t.Fatal(err)
			}
			downloads := 0
			original := c.client.Transport
			c.client.Transport = releaseTransport(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, ".tar.gz") {
					downloads++
				}
				return original.RoundTrip(r)
			})
			switch change {
			case "current-commit":
				m := nativeManifest("1.0.0")
				m.Identity.Commit = strings.Repeat("b", 40)
				data := manifestJSON(t, m)
				files[releaseDownload+"v1.0.0/"+metadataName] = data
				files[releaseDownload+"v1.0.0/SHA256SUMS"] = []byte(digest(data) + "  " + metadataName + "\n" + digest(files[releaseDownload+"v1.0.0/"+m.Assets[0].Name]) + "  " + m.Assets[0].Name + "\n")
			case "target-protocol", "current-protocol":
				v := "2.0.0"
				if change == "current-protocol" {
					v = "1.0.0"
				}
				m := nativeManifest(v)
				m.HelperMax = 99
				m.HelperMin = 99
				files[releaseDownload+"v"+v+"/"+metadataName] = manifestJSON(t, m)
			case "selected-platform":
				candidate.Identity.Arch = "arm64"
			case "forged-expiry":
				candidate.ExpiresAt = candidate.ExpiresAt.Add(time.Hour)
			case "mutated-manifest":
				candidate.Manifest.Assets[0].Name = "other.tar.gz"
			case "wrong-artifact-checksum":
				files[releaseDownload+"v2.0.0/"+candidate.Asset.Name] = []byte("corrupt artifact")
			}
			_, err = c.Prepare(context.Background(), reg, candidate)
			if !errors.Is(err, ErrIncompatible) {
				t.Fatalf("incompatible accepted: %v", err)
			}
			if change != "wrong-artifact-checksum" && downloads != 0 {
				t.Fatalf("download before compatibility gate: %d", downloads)
			}
		})
	}
}
func TestPrepareRequiresTrustedStagingAndAllowsDifferentReleaseCommits(t *testing.T) {
	c, files, _ := releaseFixture(t)
	m := nativeManifest("2.0.0")
	m.Identity.Commit = strings.Repeat("b", 40)
	base := releaseDownload + "v2.0.0/"
	data := manifestJSON(t, m)
	files[base+metadataName] = data
	files[base+"SHA256SUMS"] = []byte(digest(data) + "  " + metadataName + "\n" + digest(files[base+m.Assets[0].Name]) + "  " + m.Assets[0].Name + "\n")
	candidate, err := c.Check(context.Background(), registration())
	if err != nil {
		t.Fatal(err)
	}
	c.checkTrustedRoot = func(string) error { return ErrSetupRequired }
	if _, err = c.Prepare(context.Background(), registration(), candidate); !errors.Is(err, ErrSetupRequired) {
		t.Fatalf("untrusted staging: %v", err)
	}
	c.checkTrustedRoot = func(string) error { return nil }
	plan, err := c.Prepare(context.Background(), registration(), candidate)
	if err != nil || plan.Current.Identity.Commit == plan.Target.Identity.Commit {
		t.Fatalf("independent release commits rejected: %v", err)
	}
}
func TestReleaseCacheCannotBeMutatedByCaller(t *testing.T) {
	c, _, _ := releaseFixture(t)
	first, err := c.Check(context.Background(), registration())
	if err != nil {
		t.Fatal(err)
	}
	name := first.Asset.Name
	first.Manifest.Assets[0].Name = "mutated"
	second, err := c.Check(context.Background(), registration())
	if err != nil || second.Manifest.Assets[0].Name != name {
		t.Fatalf("caller changed cache: %+v %v", second, err)
	}
}
func TestPrepareRetainsOnlyCompleteVerifiedArtifacts(t *testing.T) {
	c, files, _ := releaseFixture(t)
	candidate, err := c.Check(context.Background(), registration())
	if err != nil {
		t.Fatal(err)
	}
	files[releaseDownload+"v2.0.0/"+candidate.Asset.Name] = []byte("corrupt")
	if _, err = c.Prepare(context.Background(), registration(), candidate); err == nil {
		t.Fatal("corrupt accepted")
	}
	entries, err := os.ReadDir(c.stagingRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed staging retained: %v %v", entries, err)
	}
}

func TestPrepareNativeInstallerAssets(t *testing.T) {
	for _, platform := range []struct {
		os, arch  string
		installer InstallerKind
	}{{"linux", "amd64", InstallerScript}, {"linux", "arm64", InstallerScript}, {"darwin", "amd64", InstallerScript}, {"darwin", "arm64", InstallerScript}, {"linux", "amd64", InstallerDeb}, {"linux", "arm64", InstallerDeb}, {"linux", "amd64", InstallerRPM}, {"linux", "arm64", InstallerRPM}, {"darwin", "amd64", InstallerPkg}, {"darwin", "arm64", InstallerPkg}} {
		t.Run(platform.os+"/"+platform.arch+"/"+string(platform.installer), func(t *testing.T) {
			c, files, _ := releaseFixture(t)
			reg := registration()
			reg.Identity.OS = platform.os
			reg.Identity.Arch = platform.arch
			reg.Installer = platform.installer
			for _, v := range []string{"1.0.0", "2.0.0"} {
				m := nativeManifest(v)
				asset := AssetSpec{OS: platform.os, Arch: platform.arch, Installer: platform.installer}
				if platform.installer == InstallerPkg {
					asset.Arch = "universal"
				}
				asset.Name = officialAssetName(v, asset)
				m.Assets = []AssetSpec{asset}
				base := releaseDownload + "v" + v + "/"
				data := manifestJSON(t, m)
				files[base+metadataName] = data
				files[base+asset.Name] = []byte("verified native package")
				files[base+"SHA256SUMS"] = []byte(digest(data) + "  " + metadataName + "\n" + digest(files[base+asset.Name]) + "  " + asset.Name + "\n")
			}
			candidate, err := c.Check(context.Background(), reg)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := c.Prepare(context.Background(), reg, candidate)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Target.Identity.Arch != platform.arch || plan.TargetArtifact.Name != candidate.Asset.Name {
				t.Fatalf("wrong native artifact: %+v", plan.Target)
			}
		})
	}
}
