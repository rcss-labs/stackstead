package cli

import (
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Printer renders command results as tables, JSON or NDJSON.
type Printer struct {
	JSON  bool
	Color bool
	Out   io.Writer
}

// newPrinter decides JSON and colour once per command. Colour needs a
// terminal stdout, no NO_COLOR (any value), TERM other than dumb, and no
// --no-color.
func newPrinter(env *Env, g *globalFlags) *Printer {
	p := &Printer{JSON: g.Output == "json", Out: env.Stdout}
	if f, ok := env.Stdout.(*os.File); ok && env.IsTerminal != nil {
		p.Color = env.IsTerminal(f.Fd()) &&
			env.Getenv("NO_COLOR") == "" && env.Getenv("TERM") != "dumb" && !g.NoColor
	}
	return p
}

const ansiReset = "\x1b[0m"

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiPattern.ReplaceAllString(s, "") }

func (p *Printer) paint(code, s string) string {
	if !p.Color || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + ansiReset
}

// Table prints an upper-case header (dimmed with colour) and rows with
// columns padded to the widest cell and two spaces between them. Widths
// ignore colour escape sequences, so coloured cells stay aligned. The last
// column is not padded.
func (p *Printer) Table(header []string, rows [][]string) {
	upper := make([]string, len(header))
	for i, h := range header {
		upper[i] = strings.ToUpper(h)
	}
	all := append([][]string{upper}, rows...)
	widths := make([]int, len(header))
	for _, row := range all {
		for i, cell := range row {
			if i < len(widths) {
				widths[i] = max(widths[i], utf8.RuneCountInString(stripANSI(cell)))
			}
		}
	}
	for n, row := range all {
		var line strings.Builder
		for i, cell := range row {
			line.WriteString(cell)
			if i < len(row)-1 {
				pad := 2
				if i < len(widths) {
					pad = widths[i] - utf8.RuneCountInString(stripANSI(cell)) + 2
				}
				line.WriteString(strings.Repeat(" ", pad))
			}
		}
		text := line.String()
		if n == 0 {
			text = p.paint("2", text)
		}
		io.WriteString(p.Out, text+"\n")
	}
}

// JSONValue prints v indented by two spaces.
func (p *Printer) JSONValue(v any) error {
	enc := json.NewEncoder(p.Out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// NDJSON prints one compact {"event","id"?,"data"} line.
func (p *Printer) NDJSON(event, id string, data any) error {
	enc := json.NewEncoder(p.Out)
	enc.SetEscapeHTML(false)
	return enc.Encode(struct {
		Event string `json:"event"`
		ID    string `json:"id,omitempty"`
		Data  any    `json:"data"`
	}{event, id, data})
}

// Status colours a state word; colour is never the only signal, the word is
// always printed. Unrecognised states are returned unchanged.
func (p *Printer) Status(s string) string {
	switch s {
	case "running", "succeeded":
		return p.paint("32", s)
	case "partial":
		return p.paint("33", s)
	case "failed", "stopped", "unavailable", "disconnected", "degraded":
		return p.paint("31", s)
	case "unknown":
		return p.paint("2", s)
	}
	return s
}
