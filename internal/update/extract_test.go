package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
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

func TestExtractBoundsHiddenMetadataAndArchiveFraming(t *testing.T) {
	// GNU long-name records are consumed inside tar.Next, never returned as
	// visible payload headers. Repeating them must still consume the budget.
	const recordSize = 128 << 10
	var rawHeader bytes.Buffer
	tw := tar.NewWriter(&rawHeader)
	if err := tw.WriteHeader(&tar.Header{Name: "metadata", Mode: 0600, Size: recordSize, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	header := append([]byte(nil), rawHeader.Bytes()[:512]...)
	header[156] = tar.TypeGNULongName
	for i := 148; i < 156; i++ {
		header[i] = ' '
	}
	sum := 0
	for _, b := range header {
		sum += int(b)
	}
	copy(header[148:156], []byte(fmt.Sprintf("%06o\x00 ", sum)))
	metadata := make([]byte, recordSize)
	copy(metadata, []byte("stackstead\x00"))
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	for range 9 {
		if _, err := gz.Write(header); err != nil {
			t.Fatal(err)
		}
		if _, err := gz.Write(metadata); err != nil {
			t.Fatal(err)
		}
	}
	tail := tar.NewWriter(gz)
	if err := tail.WriteHeader(&tar.Header{Name: "stackstead", Mode: 0600, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tail.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tail.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractTarBudget(bytes.NewReader(compressed.Bytes()), t.TempDir(), 1<<20); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("hidden metadata exceeded decompression budget but was accepted: %v", err)
	}
}

func TestExtractCountsHeadersPaddingAndTrailer(t *testing.T) {
	compressed := archive(t, []*tar.Header{{Name: "stackstead", Typeflag: tar.TypeReg, Mode: 0755, Size: 1}})
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	expanded, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	gz.Close()
	for _, limit := range []int64{int64(len(expanded)), int64(len(expanded) - 1)} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			destination := t.TempDir()
			err := extractTarBudget(bytes.NewReader(compressed), destination, limit)
			if limit < int64(len(expanded)) {
				if !errors.Is(err, ErrIncompatible) {
					t.Fatalf("framing budget exceeded but accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(destination, "stackstead"))
			if err != nil || string(data) != "x" {
				t.Fatalf("valid payload not extracted: %q %v", data, err)
			}
		})
	}
}
