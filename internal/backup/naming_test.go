package backup

import (
	"testing"
	"time"
)

func TestParseArchiveName(t *testing.T) {
	slug, ts, ok := ParseArchiveName("stackstead-my-host-20261026T030000Z.tar.gz")
	want := time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC)
	if !ok || slug != "my-host" || !ts.Equal(want) {
		t.Fatalf("got %q, %v, %v", slug, ts, ok)
	}
	for _, bad := range []string{"foo.tar.gz", "stackstead-20261026T030000Z.tar.gz", "stackstead-h-2026.tar.gz", "stackstead-h-20261026T030000Z.zip"} {
		if _, _, ok := ParseArchiveName(bad); ok {
			t.Errorf("%q parsed, want not ok", bad)
		}
	}
}

func TestArchiveNameRoundTrip(t *testing.T) {
	ts := time.Date(2026, 10, 26, 5, 0, 0, 0, time.FixedZone("x", 2*3600))
	name := ArchiveName("a-b", ts, false)
	if name != "stackstead-a-b-20261026T030000Z.tar.gz" {
		t.Fatal(name)
	}
	if SidecarName(name) != name+".manifest.json" {
		t.Fatal(SidecarName(name))
	}
	if slug, got, ok := ParseArchiveName(name); !ok || slug != "a-b" || !got.Equal(ts) {
		t.Fatal(slug, got, ok)
	}
}

func TestParseArchiveNameAcceptsEncryptedSuffix(t *testing.T) {
	slug, ts, ok := ParseArchiveName("stackstead-my-host-20261026T030000Z.tar.gz.enc")
	want := time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC)
	if !ok || slug != "my-host" || !ts.Equal(want) {
		t.Fatalf("got %q, %v, %v", slug, ts, ok)
	}
	if !IsEncryptedName("stackstead-my-host-20261026T030000Z.tar.gz.enc") || IsEncryptedName("stackstead-my-host-20261026T030000Z.tar.gz") {
		t.Fatal("IsEncryptedName does not follow the suffix")
	}
	if _, _, ok := ParseArchiveName("stackstead-h-20261026T030000Z.tar.enc"); ok {
		t.Fatal("a name without .gz parsed")
	}
	if !ValidArchiveName("stackstead-h-20261026T030000Z.tar.gz.enc") {
		t.Fatal("ValidArchiveName rejects the encrypted suffix")
	}
}

func TestArchiveNameEncrypted(t *testing.T) {
	ts := time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC)
	name := ArchiveName("a-b", ts, true)
	if name != "stackstead-a-b-20261026T030000Z.tar.gz.enc" {
		t.Fatal(name)
	}
	if slug, got, ok := ParseArchiveName(name); !ok || slug != "a-b" || !got.Equal(ts) {
		t.Fatal(slug, got, ok)
	}
	if SidecarName(name) != name+".manifest.json" {
		t.Fatal(SidecarName(name))
	}
}
