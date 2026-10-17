package updatehost

import (
	"bytes"
	"encoding/binary"
	"github.com/rcss-labs/stackstead/internal/update"
	"path"
	"strings"
)

type payloadMetadataProof struct {
	Path, Companion, Attribute string
	Size                       int64
}

// The stock legacy pkgbuild shape contains a zero FinderInfo, exactly one
// eleven-byte provenance attribute, and an empty resource fork. Its entry table
// and ATTR offsets account for every byte. Unknown layouts fail closed.
func validateProvenanceMetadata(b []byte) error {
	if len(b) != 163 {
		return update.ErrIncompatible
	}
	u32 := func(i int) uint32 { return binary.BigEndian.Uint32(b[i : i+4]) }
	if u32(0) != 0x00051607 || u32(4) != 0x00020000 || string(b[8:24]) != "Mac OS X        " || binary.BigEndian.Uint16(b[24:26]) != 2 {
		return update.ErrIncompatible
	}
	if u32(26) != 9 || u32(30) != 50 || u32(34) != 113 || u32(38) != 2 || u32(42) != 163 || u32(46) != 0 {
		return update.ErrIncompatible
	}
	if !bytes.Equal(b[50:84], make([]byte, 34)) || string(b[84:88]) != "ATTR" || u32(88) != 0 || u32(92) != 163 || u32(96) != 152 || u32(100) != 11 || !bytes.Equal(b[104:118], make([]byte, 14)) || binary.BigEndian.Uint16(b[118:120]) != 1 {
		return update.ErrIncompatible
	}
	if u32(120) != 152 || u32(124) != 11 || binary.BigEndian.Uint16(b[128:130]) != 0 || b[130] != 21 || string(b[131:152]) != "com.apple.provenance\x00" {
		return update.ErrIncompatible
	}
	return nil
}
func metadataCompanion(p string) (string, bool) {
	base := path.Base(p)
	if !strings.HasPrefix(base, "._") || len(base) <= 2 {
		return "", false
	}
	return path.Join(path.Dir(p), strings.TrimPrefix(base, "._")), true
}
func metadataPath(p string) string { return path.Join(path.Dir(p), "._"+path.Base(p)) }
