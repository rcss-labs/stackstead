package updatehost

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"testing"
)

func TestNativeRPMNewcPreservesInstallationAttributes(t *testing.T) {
	var source bytes.Buffer
	record := func(name string, mode, uid, gid int) {
		v := [13]int{1, mode, uid, gid, 1, 0, 0, 0, 0, 0, 0, len(name) + 1, 0}
		source.WriteString("070701")
		for _, x := range v {
			fmt.Fprintf(&source, "%08x", x)
		}
		source.WriteString(name)
		source.WriteByte(0)
		for source.Len()%4 != 0 {
			source.WriteByte(0)
		}
	}
	record("usr/bin/stackstead", 0104755, 501, 20)
	record("TRAILER!!!", 0, 0, 0)
	var converted bytes.Buffer
	if e := newcToTar(bytes.NewReader(source.Bytes()), &converted); e != nil {
		t.Fatal(e)
	}
	tr := tar.NewReader(bytes.NewReader(converted.Bytes()))
	h, e := tr.Next()
	if e != nil {
		t.Fatal(e)
	}
	if h.Mode != 04755 || h.Uid != 501 || h.Gid != 20 {
		t.Fatalf("RPM attributes stripped before proof: mode%o UID%d GID%d", h.Mode, h.Uid, h.Gid)
	}
	if _, e = tr.Next(); e != io.EOF {
		t.Fatal(e)
	}
}
func TestNativeRPMNewcRejectsUnaccountedModeBits(t *testing.T) {
	var source bytes.Buffer
	record := func(name string, mode int) {
		v := [13]int{1, mode, 0, 0, 1, 0, 0, 0, 0, 0, 0, len(name) + 1, 0}
		source.WriteString("070701")
		for _, x := range v {
			fmt.Fprintf(&source, "%08x", x)
		}
		source.WriteString(name)
		source.WriteByte(0)
		for source.Len()%4 != 0 {
			source.WriteByte(0)
		}
	}
	record("usr/bin/stackstead", 0300755)
	record("TRAILER!!!", 0)
	if e := newcToTar(bytes.NewReader(source.Bytes()), io.Discard); e == nil {
		t.Fatal("unknown high mode bits stripped before correlation")
	}
}
func TestNativeRPMNewcCanonicalNamesAndCollisions(t *testing.T) {
	for _, bad := range []string{"//usr/bin/stackstead", "/", ".", "./usr/bin/stackstead", "usr/../bin/stackstead", "usr/bin/../stackstead", "usr/bin/stack\tstead", "collision"} {
		t.Run(bad, func(t *testing.T) {
			var source bytes.Buffer
			record := func(name string) {
				v := [13]int{1, 0100755, 0, 0, 1, 0, 0, 0, 0, 0, 0, len(name) + 1, 0}
				source.WriteString("070701")
				for _, x := range v {
					fmt.Fprintf(&source, "%08x", x)
				}
				source.WriteString(name)
				source.WriteByte(0)
				for source.Len()%4 != 0 {
					source.WriteByte(0)
				}
			}
			if bad == "collision" {
				record("/usr/bin/stackstead")
				record("usr/bin/stackstead")
			} else {
				record(bad)
			}
			record("TRAILER!!!")
			if e := newcToTar(bytes.NewReader(source.Bytes()), io.Discard); e == nil {
				t.Fatalf("unsafe/ambiguous CPIO %q accepted", bad)
			}
		})
	}
}
