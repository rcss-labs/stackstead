package updatehost

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestNativeRPMFileMetadataRequiresExactTypedCorrelation(t *testing.T) {
	base := "/usr/bin/stackstead\t100755\troot\troot\n/var/lib/stackstead\t40700\tstackstead\tstackstead\n"
	for _, bad := range []string{"", "duplicate", "unknown-user", "separator", "relative", "special-type", "missing", "extra", "mode", "type", "numeric-owner"} {
		t.Run(bad, func(t *testing.T) {
			raw := base
			entries := []payloadEntry{{Path: "/usr/bin/stackstead", Mode: 0755}, {Path: "/var/lib/stackstead", Mode: 0700, Directory: true}}
			switch bad {
			case "duplicate":
				raw += strings.Split(base, "\n")[0] + "\n"
			case "unknown-user":
				raw = strings.Replace(raw, "\troot\t", "\tother\t", 1)
			case "separator":
				raw = strings.Replace(raw, "stackstead\t100755", "stack\tstead\t100755", 1)
			case "relative":
				raw = strings.TrimPrefix(raw, "/")
			case "special-type":
				raw = strings.Replace(raw, "100755", "120755", 1)
			case "missing":
				raw = strings.Split(base, "\n")[0] + "\n"
			case "extra":
				raw += "/usr/libexec/extra\t100755\troot\troot\n"
			case "mode":
				entries[0].Mode = 0777
			case "type":
				entries[0].Directory = true
			case "numeric-owner":
				entries[0].UID = 501
			}
			facts, e := readRPMFileMetadata(strings.NewReader(raw))
			if e == nil {
				e = correlateRPMFileMetadata(entries, facts)
			}
			if bad == "" {
				if e != nil {
					t.Fatal(e)
				}
				if entries[1].Uname != "stackstead" || entries[1].Gname != "stackstead" {
					t.Fatal("symbolic native ownership lost")
				}
			} else if e == nil {
				t.Fatalf("unmatched RPM %s evidence accepted", bad)
			}
		})
	}
}
func TestNativeRPMMetadataQueryUsesSameVerifiedDescriptor(t *testing.T) {
	f, e := os.CreateTemp(t.TempDir(), "verified-artifact")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if _, e = f.WriteString("verified-rpm-fixture\n"); e != nil {
		t.Fatal(e)
	}
	// The fixture replaces only the unavailable RPM tool. The real bounded
	// converter still starts a UID subprocess and inherits the exact descriptor.
	facts, e := rpmMetadataWithConverter(context.Background(), f, func(ctx context.Context, input *os.File, tool string, args []string, limit int64, consume func(io.Reader) error) error {
		if input != f || tool != "/usr/bin/rpm" || !reflect.DeepEqual(args, []string{"-qp", "--queryformat", rpmFilesQueryFormat, "/dev/fd/3"}) || limit != childMetadataLimit {
			return errors.New("query not bound to fixed same-FD operation")
		}
		return convertPayloadStream(ctx, input, "/bin/sh", []string{"-c", `IFS= read -r value <&3; [ "$value" = verified-rpm-fixture ] || exit 71; printf '/usr/bin/stackstead\t100755\troot\troot\n'`}, limit, consume)
	})
	if e != nil || facts["/usr/bin/stackstead"].User != "root" {
		t.Fatalf("same-FD metadata proof failed: %v %+v", e, facts)
	}
	b := make([]byte, 8)
	if _, e = f.Read(b); e != nil || string(b) != "verified" {
		t.Fatal("verified artifact was not rewound for CPIO conversion")
	}
}
