package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestTableAlignsColumns(t *testing.T) {
	var buf bytes.Buffer
	p := &Printer{Out: &buf}
	p.Table([]string{"name", "state"}, [][]string{{"web", "running"}, {"database-long", "stopped"}})
	want := "NAME           STATE\nweb            running\ndatabase-long  stopped\n"
	if buf.String() != want {
		t.Errorf("table =\n%q\nwant\n%q", buf.String(), want)
	}
}

func TestTableAlignsWithColouredCells(t *testing.T) {
	var buf bytes.Buffer
	p := &Printer{Out: &buf, Color: true}
	p.Table([]string{"name", "state"}, [][]string{{"a", p.Status("running")}, {"bbbb", p.Status("failed")}})
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %q", lines)
	}
	col := func(line string) int { return strings.Index(stripANSI(line), "running") }
	if col(lines[1]) != 6 { // "a" padded to the 4-wide column plus a 2-space gap
		t.Errorf("state column at %d in %q", col(lines[1]), stripANSI(lines[1]))
	}
	if !strings.Contains(lines[0], "\x1b[2m") {
		t.Errorf("header not dimmed: %q", lines[0])
	}
}

func TestStatusColours(t *testing.T) {
	on := &Printer{Color: true}
	off := &Printer{}
	cases := []struct{ state, code string }{
		{"running", "32"}, {"partial", "33"}, {"failed", "31"}, {"stopped", "31"}, {"unavailable", "31"}, {"unknown", "2"},
		{"disconnected", "31"}, {"degraded", "31"},
	}
	for _, c := range cases {
		if got, want := on.Status(c.state), "\x1b["+c.code+"m"+c.state+"\x1b[0m"; got != want {
			t.Errorf("Status(%q) = %q, want %q", c.state, got, want)
		}
		if got := off.Status(c.state); got != c.state {
			t.Errorf("uncoloured Status(%q) = %q", c.state, got)
		}
	}
	if got := on.Status("weird"); got != "weird" {
		t.Errorf("unknown state coloured: %q", got)
	}
}

func TestJSONValueIndentsTwoSpaces(t *testing.T) {
	var buf bytes.Buffer
	p := &Printer{Out: &buf, JSON: true}
	if err := p.JSONValue(map[string]any{"a": 1, "b": []int{1}}); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"a\": 1,\n  \"b\": [\n    1\n  ]\n}\n"
	if buf.String() != want {
		t.Errorf("got %q want %q", buf.String(), want)
	}
}

func TestNDJSONLineShape(t *testing.T) {
	var buf bytes.Buffer
	p := &Printer{Out: &buf, JSON: true}
	if err := p.NDJSON("log", "42", map[string]string{"line": "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := p.NDJSON("done", "", map[string]bool{"ok": true}); err != nil {
		t.Fatal(err)
	}
	want := `{"event":"log","id":"42","data":{"line":"hi"}}` + "\n" + `{"event":"done","data":{"ok":true}}` + "\n"
	if buf.String() != want {
		t.Errorf("got\n%s\nwant\n%s", buf.String(), want)
	}
}

func TestColourDecision(t *testing.T) {
	// A real *os.File is needed for the fd check; IsTerminal is injected.
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cases := []struct {
		name     string
		vars     map[string]string
		tty      bool
		noColor  bool
		stdout   func() any
		wantFile bool
		want     bool
	}{
		{"terminal", nil, true, false, nil, true, true},
		{"NO_COLOR any value", map[string]string{"NO_COLOR": "1"}, true, false, nil, true, false},
		{"TERM dumb", map[string]string{"TERM": "dumb"}, true, false, nil, true, false},
		{"--no-color", nil, true, true, nil, true, false},
		{"not a terminal", nil, false, false, nil, true, false},
		{"stdout is not a file", nil, true, false, nil, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			te := newTestEnv(t, c.vars)
			te.Env.IsTerminal = func(uintptr) bool { return c.tty }
			if c.wantFile {
				te.Env.Stdout = f
			}
			p := newPrinter(te.Env, &globalFlags{NoColor: c.noColor, Output: "table"})
			if p.Color != c.want {
				t.Errorf("Color = %v, want %v", p.Color, c.want)
			}
		})
	}
}

func TestNewPrinterJSONFollowsOutputFlag(t *testing.T) {
	te := newTestEnv(t, nil)
	if !newPrinter(te.Env, &globalFlags{Output: "json"}).JSON {
		t.Error("JSON = false with -o json")
	}
	if newPrinter(te.Env, &globalFlags{Output: "table"}).JSON {
		t.Error("JSON = true with -o table")
	}
}
