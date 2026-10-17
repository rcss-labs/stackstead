package updatehost

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"github.com/rcss-labs/stackstead/internal/update"
	"strings"
	"testing"
)

func nativePolicyProof(kind update.InstallerKind) payloadInspection {
	id := update.Identity{Version: "1.2.3", Commit: strings.Repeat("a", 40), OS: "linux", Arch: "amd64"}
	bin := executableInspection{Identity: id, Slices: []update.Identity{id}}
	return payloadInspection{Installer: kind, DecodedBytes: 4096, App: bin, Helper: bin, Bootstrap: bin, Entries: []payloadEntry{{Path: "/usr/bin/stackstead", Size: 1, Mode: 0755, Uname: "root", Gname: "root"}, {Path: "/usr/libexec/stackstead/stackstead-updater", Size: 1, Mode: 0755, Uname: "root", Gname: "root"}, {Path: "/usr/libexec/stackstead/stackstead-updater-bootstrap", Size: 1, Mode: 0755, Uname: "root", Gname: "root"}, {Path: "/var/lib/stackstead", Directory: true, Mode: 0700, Uname: "stackstead", Gname: "stackstead"}, {Path: "/etc/stackstead/config.yaml", Size: 1, Mode: 0640, Uname: "root", Gname: "stackstead"}}}
}
func policyTar(t *testing.T, entries []payloadEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, e := range entries {
		typ := byte(tar.TypeReg)
		if e.Directory {
			typ = tar.TypeDir
		}
		h := &tar.Header{Name: strings.TrimPrefix(e.Path, "/"), Typeflag: typ, Mode: e.Mode, Uid: int(e.UID), Gid: int(e.GID), Uname: e.Uname, Gname: e.Gname, Size: e.Size, Format: tar.FormatUSTAR}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.Size > 0 {
			w.Write(make([]byte, e.Size))
		}
	}
	w.Close()
	return b.Bytes()
}
func TestNativePayloadAttributesRejectInfrastructureMutation(t *testing.T) {
	for _, kind := range []update.InstallerKind{update.InstallerDeb, update.InstallerRPM} {
		for _, bad := range []string{"", "writable-leaf", "setuid", "setgid", "sticky", "uid", "gid", "owner-name", "group-name", "writable-directory", "nonroot-directory"} {
			t.Run(string(kind)+"/"+bad, func(t *testing.T) {
				p := nativePolicyProof(kind)
				switch bad {
				case "writable-leaf":
					p.Entries[0].Mode = 0777
				case "setuid":
					p.Entries[0].Mode = 04755
				case "setgid":
					p.Entries[0].Mode = 02755
				case "sticky":
					p.Entries[0].Mode = 01755
				case "uid":
					p.Entries[0].UID = 501
				case "gid":
					p.Entries[0].GID = 20
				case "owner-name":
					p.Entries[0].Uname = "stackstead"
				case "group-name":
					p.Entries[0].Gname = "stackstead"
				case "writable-directory":
					p.Entries = append(p.Entries, payloadEntry{Path: "/usr/bin", Directory: true, Mode: 0777, Uname: "root", Gname: "root"})
				case "nonroot-directory":
					p.Entries = append(p.Entries, payloadEntry{Path: "/usr/libexec", Directory: true, Mode: 0755, UID: 501, Uname: "stackstead", Gname: "root"})
				}
				err := validatePayloadInventory(p)
				if bad == "" && err != nil {
					t.Fatal(err)
				}
				if bad != "" && err == nil {
					t.Errorf("parent accepted unsafe %s native attributes", bad)
				}
				// Child content policy is independent of the root-side proof validator.
				if bad != "" {
					_, err = inventoryTar(context.Background(), bytes.NewReader(policyTar(t, p.Entries)), payloadRequest{Installer: kind}, defaultPayloadBounds, false)
					if err == nil {
						t.Fatalf("child accepted unsafe %s native attributes", bad)
					}
				}
			})
		}
	}
}
func TestLinuxPayloadNeverOwnsUpdaterPrivateState(t *testing.T) {
	for _, kind := range []update.InstallerKind{update.InstallerDeb, update.InstallerRPM} {
		for _, forbidden := range []string{"/var/lib/stackstead-updater", "/var/lib/stackstead-updater/workers/active.json", "/var/lib/stackstead-updater/bootstrap/v1/stackstead-updater-bootstrap", "/var/lib/stackstead-updater/journal/job.json", "/run/stackstead-updater", "/var/run/stackstead-updater"} {
			t.Run(string(kind)+forbidden, func(t *testing.T) {
				p := nativePolicyProof(kind)
				p.Entries = append(p.Entries, payloadEntry{Path: forbidden, Mode: 0755, Uname: "root", Gname: "root"})
				if validatePayloadInventory(p) == nil {
					t.Error("parent accepted ownership of updater authority")
				}
				if _, e := inventoryTar(context.Background(), bytes.NewReader(policyTar(t, p.Entries)), payloadRequest{Installer: kind}, defaultPayloadBounds, false); e == nil {
					t.Fatal("child accepted ownership of updater authority")
				}
			})
		}
	}
}
func TestNativePayloadRawFramingRejectsHiddenRecords(t *testing.T) {
	raw := policyTar(t, []payloadEntry{{Path: "/usr/bin/stackstead", Mode: 0755, Uname: "root", Gname: "root"}})
	for _, typ := range []byte{'L', 'K', 'x', 'g', 'S', '1', '2', '3', '4', '6', '7'} {
		t.Run(string(typ), func(t *testing.T) {
			b := append([]byte{}, raw...)
			b[156] = typ
			for i := 148; i < 156; i++ {
				b[i] = ' '
			}
			sum := 0
			for _, v := range b[:512] {
				sum += int(v)
			}
			copy(b[148:156], []byte(fmt.Sprintf("%06o\x00 ", sum)))
			if _, e := inventoryTar(context.Background(), bytes.NewReader(b), payloadRequest{Installer: update.InstallerDeb}, defaultPayloadBounds, false); e == nil {
				t.Fatalf("hidden/special record %q accepted", typ)
			}
		})
	}
	t.Run("after-EOF", func(t *testing.T) {
		b := append(append([]byte{}, raw...), raw[:512]...)
		if _, e := inventoryTar(context.Background(), bytes.NewReader(b), payloadRequest{Installer: update.InstallerDeb}, defaultPayloadBounds, false); e == nil {
			t.Fatal("nonzero header after EOF accepted")
		}
	})
	t.Run("base-256", func(t *testing.T) {
		b := append([]byte{}, raw...)
		b[124] = 0x80
		if _, e := inventoryTar(context.Background(), bytes.NewReader(b), payloadRequest{Installer: update.InstallerDeb}, defaultPayloadBounds, false); e == nil {
			t.Fatal("base256 size accepted")
		}
	})
}
