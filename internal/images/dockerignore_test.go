package images

import (
	"strings"
	"testing"
	"time"
)

func TestDockerignoreLongLine(t *testing.T) {
	long := strings.Repeat("x", 100*1024)
	ig, err := ParseDockerignore([]byte(long + "\n*.log\n"))
	if err != nil {
		t.Fatalf("a 100 KiB line must parse: %v", err)
	}
	if !ig.Excluded("a.log") {
		t.Error("rules after the long line must still apply")
	}
}

func TestDockerignore(t *testing.T) {
	cases := []struct {
		name     string
		patterns string
		path     string
		want     bool
	}{
		{"glob root", "*.log", "a.log", true},
		{"glob only at root", "*.log", "sub/a.log", false},
		{"glob miss", "*.log", "a.txt", false},
		{"negation", "*.log\n!keep.log", "keep.log", false},
		{"negation others still excluded", "*.log\n!keep.log", "other.log", true},
		{"last match wins", "!keep.log\n*.log", "keep.log", true},
		{"dir excludes children", "node_modules", "node_modules/x/y.js", true},
		{"dir name file", "node_modules", "node_modules", true},
		{"dir prefix is not substring", "node_modules", "node_modules2/x", false},
		{"double star any depth", "**/tmp", "a/b/tmp", true},
		{"double star zero depth", "**/tmp", "tmp", true},
		{"double star children", "**/tmp", "a/tmp/f", true},
		{"single star segment", "a/*/b", "a/x/b", true},
		{"single star one segment only", "a/*/b", "a/x/y/b", false},
		{"question mark", "f?.txt", "f1.txt", true},
		{"comment", "# *.log", "a.log", false},
		{"blank lines and spaces", "\n  *.log  \n", "a.log", true},
		{"leading slash", "/a.log", "a.log", true},
		{"dot slash", "./a.log", "a.log", true},
		{"excluding Dockerfile has no effect", "Dockerfile\n*", "Dockerfile", false},
		{"excluding .dockerignore has no effect", ".dockerignore\n.*", ".dockerignore", false},
		{"star excludes others", "*", "README.md", true},
		{"nested Dockerfile is ordinary", "**/Dockerfile", "sub/Dockerfile", true},
	}
	for _, c := range cases {
		ig, err := ParseDockerignore([]byte(c.patterns))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := ig.Excluded(c.path); got != c.want {
			t.Errorf("%s: Excluded(%q) = %v, want %v", c.name, c.path, got, c.want)
		}
	}
	if _, err := ParseDockerignore([]byte("[")); err == nil {
		t.Error("bad pattern accepted")
	}
}

func TestDockerignoreRepeatedDoubleStarIsLinear(t *testing.T) {
	ig, err := ParseDockerignore([]byte(strings.Repeat("**/", 1000) + "zz\n"))
	if err != nil {
		t.Fatal(err)
	}
	deep := strings.Repeat("d/", 99) + "f"
	start := time.Now()
	for i := 0; i < 200; i++ {
		if ig.Excluded(deep) {
			t.Fatal("path without a zz segment excluded")
		}
	}
	if !ig.Excluded(strings.Repeat("d/", 99) + "zz") {
		t.Fatal("path ending in zz not excluded")
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("matching took %v, want under 1s", took)
	}
}
