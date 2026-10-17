package updatehost

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"errors"
	"github.com/rcss-labs/stackstead/internal/update"
	"golang.org/x/sys/unix"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"
)

func payloadTar(t *testing.T, headers []*tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, h := range headers {
		if e := w.WriteHeader(h); e != nil {
			t.Fatal(e)
		}
		if h.Size > 0 {
			w.Write(make([]byte, h.Size))
		}
	}
	w.Close()
	return b.Bytes()
}
func TestPKGProducerBoundsNestedStreams(t *testing.T) {
	raw := payloadTar(t, []*tar.Header{{Name: "file", Size: 2048, Mode: 0644}})
	if _, e := inventoryTar(context.Background(), bytes.NewReader(raw), payloadRequest{}, payloadBounds{Decoded: 1024, File: 512 << 20, Entries: 4096, Names: 64 << 10}, false); e == nil {
		t.Fatal("expanded stream over limit accepted")
	}
}
func TestPKGProducerRejectsUnexpectedComponentAndPaths(t *testing.T) {
	for _, h := range []*tar.Header{{Name: "../escape", Mode: 0644}, {Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/tmp/x"}, {Name: "device", Typeflag: tar.TypeChar}} {
		raw := payloadTar(t, []*tar.Header{h})
		if _, e := inventoryTar(context.Background(), bytes.NewReader(raw), payloadRequest{}, defaultPayloadBounds, false); e == nil {
			t.Fatalf("unsafe member accepted: %+v", h)
		}
	}
	raw := payloadTar(t, []*tar.Header{{Name: "same", Mode: 0644}, {Name: "same", Mode: 0644}})
	if _, e := inventoryTar(context.Background(), bytes.NewReader(raw), payloadRequest{}, defaultPayloadBounds, false); e == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestPKGProducerConverterOverflowTimeoutAndEarlyExitAreReaped(t *testing.T) {
	for _, kind := range []string{"overflow", "timeout", "early-exit"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			pid := 0
			script := "echo $$; exec /usr/bin/yes x"
			if kind == "timeout" {
				script = "echo $$; exec /bin/sleep 30"
			}
			if kind == "early-exit" {
				script = "echo $$; exit 19"
			}
			start := time.Now()
			err := convertPayloadStream(ctx, nil, "/bin/sh", []string{"-c", script}, 1024, func(r io.Reader) error {
				reader := bufio.NewReader(r)
				line, e := reader.ReadString('\n')
				if e != nil {
					return e
				}
				pid, e = strconv.Atoi(strings.TrimSpace(line))
				if e != nil {
					return e
				}
				_, e = io.Copy(io.Discard, reader)
				return e
			})
			if err == nil || pid <= 0 || time.Since(start) > 3*time.Second {
				t.Fatalf("unbounded/accepted converter %s: pid%d error%v", kind, pid, err)
			}
			var status unix.WaitStatus
			if _, e := unix.Wait4(pid, &status, unix.WNOHANG, nil); !errors.Is(e, unix.ECHILD) {
				t.Fatalf("converter was not reaped: %v", e)
			}
		})
	}
}
func TestPKGProducerParentRejectsMalformedInventoryProof(t *testing.T) {
	id := update.Identity{Version: "1.2.3", Commit: strings.Repeat("a", 40), OS: "linux", Arch: "amd64"}
	for _, bad := range []string{"", "missing", "duplicate", "size", "decoded", "name", "metadata"} {
		t.Run(bad, func(t *testing.T) {
			binary := executableInspection{Identity: id, Slices: []update.Identity{id}}
			p := payloadInspection{Installer: update.InstallerScript, App: binary, Helper: binary, Bootstrap: binary, DecodedBytes: 4096, Entries: []payloadEntry{{Path: "/stackstead", Size: 1}, {Path: "/stackstead-updater", Size: 1}, {Path: "/stackstead-updater-bootstrap", Size: 1}}}
			switch bad {
			case "missing":
				p.Entries = p.Entries[:2]
			case "duplicate":
				p.Entries = append(p.Entries, p.Entries[0])
			case "size":
				p.Entries[0].Size = childArtifactLimit + 1
			case "decoded":
				p.DecodedBytes = 2<<30 + 1
			case "name":
				p.Entries[0].Path = "/../stackstead"
			case "metadata":
				p.Entries[0].Metadata = &payloadMetadataProof{Path: "/stackstead", Companion: "/x", Attribute: "arbitrary", Size: 163}
			}
			err := validatePayloadInventory(p)
			if bad == "" && err != nil {
				t.Fatal(err)
			}
			if bad != "" && err == nil {
				t.Fatalf("malformed %s inventory accepted", bad)
			}
		})
	}
}
func TestPKGProducerRejectsDuplicateArchiveRoot(t *testing.T) {
	raw := payloadTar(t, []*tar.Header{{Name: ".", Typeflag: tar.TypeDir, Mode: 0755}, {Name: "./", Typeflag: tar.TypeDir, Mode: 0755}})
	if _, e := inventoryTar(context.Background(), bytes.NewReader(raw), payloadRequest{}, defaultPayloadBounds, false); e == nil {
		t.Fatal("duplicate archive root escaped inventory accounting")
	}
}
