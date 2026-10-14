package web_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/web"
)

func TestDistFallsBackToPlaceholder(t *testing.T) {
	data, err := fs.ReadFile(web.Dist(), "index.html")
	if err != nil {
		t.Fatalf("Dist() has no index.html: %v", err)
	}
	want := "not been built"
	if web.Built() {
		want = `<div id="app"`
	}
	if !strings.Contains(string(data), want) {
		t.Errorf("Built()=%v but index.html lacks %q:\n%s", web.Built(), want, data)
	}
	if !web.Built() && !strings.Contains(string(data), "make ui") {
		t.Error("placeholder should name `make ui`")
	}
}
