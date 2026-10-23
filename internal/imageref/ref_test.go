package imageref

import "testing"

func TestParseFormatRoundTrip(t *testing.T) {
	want := Ref{Owner: "0123456789ab", Slug: "web-app", Tag: "1.2"}
	s := Format(want.Owner, want.Slug, want.Tag)
	if s != "stackstead.invalid/0123456789ab/web-app:1.2" {
		t.Fatalf("Format = %q", s)
	}
	got, ok := Parse(s)
	if !ok || got != want {
		t.Fatalf("Parse(%q) = %+v, %v; want %+v, true", s, got, ok, want)
	}
	if got.String() != s {
		t.Errorf("String() = %q; want %q", got.String(), s)
	}
}

func TestParseRejectsInvalidReferences(t *testing.T) {
	for _, s := range []string{
		"nginx:1",
		"stackstead.invalid/x/y",
		"stackstead.invalid/0123456789ab/Bad_Slug:1",
		"stackstead.invalid/short/web:1",
		"stackstead/0123456789ab/web:1",
	} {
		if _, ok := Parse(s); ok {
			t.Errorf("Parse(%q) accepted an invalid reference", s)
		}
	}
}

func TestIsLibrary(t *testing.T) {
	if !IsLibrary("stackstead.invalid/0123456789ab/web:1") {
		t.Error("library reference not recognised")
	}
	if IsLibrary("nginx:1") {
		t.Error("external image recognised as library")
	}
}

func TestOwnerID(t *testing.T) {
	if got := OwnerID("0123456789abcdef0123456789abcdef"); got != "0123456789ab" {
		t.Errorf("OwnerID = %q", got)
	}
	if got := OwnerID("abc"); got != "" {
		t.Errorf("OwnerID(short) = %q; want empty", got)
	}
}
