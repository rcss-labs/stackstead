package images

import "testing"

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
