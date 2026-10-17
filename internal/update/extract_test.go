package update

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractRejectsTraversalLinksAndSpecialFiles(t *testing.T) {
	parent := t.TempDir()
	outside := filepath.Join(parent, "escape")
	destination := filepath.Join(parent, "staging")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	for _, h := range []*tar.Header{{Name: "../escape", Typeflag: tar.TypeReg}, {Name: outside, Typeflag: tar.TypeReg}, {Name: "stackstead", Typeflag: tar.TypeSymlink, Linkname: "../escape"}, {Name: "stackstead", Typeflag: tar.TypeLink, Linkname: "outside"}, {Name: "stackstead", Typeflag: tar.TypeFifo}, {Name: "unexpected", Typeflag: tar.TypeReg}} {
		t.Run(fmt.Sprint(h.Typeflag, h.Name), func(t *testing.T) {
			if err := extractTar(bytes.NewReader(archive(t, []*tar.Header{h})), destination); !errors.Is(err, ErrIncompatible) {
				t.Fatalf("unsafe archive accepted: %v", err)
			}
			if _, err := os.Lstat(outside); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("archive escaped staging: %v", err)
			}
		})
	}
}
