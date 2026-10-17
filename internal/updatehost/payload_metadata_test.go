package updatehost

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/hex"
	"github.com/rcss-labs/stackstead/internal/update"
	"testing"
)

func provenanceFixture(t *testing.T) []byte {
	t.Helper()
	b, e := hex.DecodeString("00051607000200004d6163204f5320582020202020202020000200000009000000320000007100000002000000a300000000000000000000000000000000000000000000000000000000000000000000000000004154545200000000000000a3000000980000000b00000000000000000000000000000001000000980000000b000015636f6d2e6170706c652e70726f76656e616e6365000102000000000000000000")
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestPKGProducerStrictProvenanceMetadata(t *testing.T) {
	for _, kind := range []string{"", "acl", "resource", "unknown-attribute", "overlap", "duplicate", "oversize", "unaccounted"} {
		t.Run(kind, func(t *testing.T) {
			b := provenanceFixture(t)
			switch kind {
			case "acl":
				b[60] = 1
			case "resource":
				b[49] = 1
				b = append(b, 0)
			case "unknown-attribute":
				b[141] = 'X'
			case "overlap":
				b[33] = 49
			case "duplicate":
				b[119] = 2
			case "oversize":
				b = make([]byte, 4097)
			case "unaccounted":
				b = append(b, 0)
			}
			err := validateProvenanceMetadata(b)
			if kind == "" && err != nil {
				t.Fatalf("actual provenance-only AppleDouble refused: %v (size%d)", err, len(b))
			}
			if kind != "" && err == nil {
				t.Fatalf("unsafe metadata %s accepted", kind)
			}
		})
	}
}
func TestPKGProducerMetadataRequiresCanonicalCompanion(t *testing.T) {
	for _, kind := range []string{"", "orphan", "alias", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			var b bytes.Buffer
			tw := tar.NewWriter(&b)
			if kind != "orphan" {
				tw.WriteHeader(&tar.Header{Name: "Library/", Typeflag: tar.TypeDir, Mode: 0755, Format: tar.FormatUSTAR})
			}
			name := "._Library"
			if kind == "alias" {
				name = "./foo/../._Library"
			}
			count := 1
			if kind == "duplicate" {
				count = 2
			}
			for i := 0; i < count; i++ {
				raw := provenanceFixture(t)
				tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(raw)), Mode: 0644, Format: tar.FormatUSTAR})
				tw.Write(raw)
			}
			tw.Close()
			_, err := inventoryTar(context.Background(), bytes.NewReader(b.Bytes()), payloadRequest{Installer: update.InstallerPkg}, defaultPayloadBounds, false)
			if kind == "" && err != nil {
				t.Fatal(err)
			}
			if kind != "" && err == nil {
				t.Fatalf("%s metadata accepted", kind)
			}
		})
	}
}
