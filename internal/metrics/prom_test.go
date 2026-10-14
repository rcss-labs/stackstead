package metrics

import (
	"bytes"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const goldenTwoSamples = `# HELP stackstead_container_cpu_percent Container CPU usage in percent of one core.
# TYPE stackstead_container_cpu_percent gauge
stackstead_container_cpu_percent{project="alpha",service="db",container="alpha-db-1"} 12.5
stackstead_container_cpu_percent{project="beta",service="web",container="beta-web-1"} 0
# HELP stackstead_container_memory_bytes Container memory usage in bytes.
# TYPE stackstead_container_memory_bytes gauge
stackstead_container_memory_bytes{project="alpha",service="db",container="alpha-db-1"} 1048576
stackstead_container_memory_bytes{project="beta",service="web",container="beta-web-1"} 2048
# HELP stackstead_container_memory_limit_bytes Container memory limit in bytes.
# TYPE stackstead_container_memory_limit_bytes gauge
stackstead_container_memory_limit_bytes{project="alpha",service="db",container="alpha-db-1"} 2097152
stackstead_container_memory_limit_bytes{project="beta",service="web",container="beta-web-1"} 0
# HELP stackstead_container_network_receive_bytes_total Total bytes received over the network.
# TYPE stackstead_container_network_receive_bytes_total counter
stackstead_container_network_receive_bytes_total{project="alpha",service="db",container="alpha-db-1"} 100
stackstead_container_network_receive_bytes_total{project="beta",service="web",container="beta-web-1"} 0
# HELP stackstead_container_network_transmit_bytes_total Total bytes transmitted over the network.
# TYPE stackstead_container_network_transmit_bytes_total counter
stackstead_container_network_transmit_bytes_total{project="alpha",service="db",container="alpha-db-1"} 200
stackstead_container_network_transmit_bytes_total{project="beta",service="web",container="beta-web-1"} 0
# HELP stackstead_container_blkio_read_bytes_total Total bytes read from block devices.
# TYPE stackstead_container_blkio_read_bytes_total counter
stackstead_container_blkio_read_bytes_total{project="alpha",service="db",container="alpha-db-1"} 300
stackstead_container_blkio_read_bytes_total{project="beta",service="web",container="beta-web-1"} 0
# HELP stackstead_container_blkio_write_bytes_total Total bytes written to block devices.
# TYPE stackstead_container_blkio_write_bytes_total counter
stackstead_container_blkio_write_bytes_total{project="alpha",service="db",container="alpha-db-1"} 400
stackstead_container_blkio_write_bytes_total{project="beta",service="web",container="beta-web-1"} 18446744073709551615
# HELP stackstead_metrics_degraded 1 if metrics collection is degraded (store write failures), else 0.
# TYPE stackstead_metrics_degraded gauge
stackstead_metrics_degraded 0
`

func twoSamples() []ExpositionSample {
	// Deliberately unsorted: output must be ordered by labels.
	return []ExpositionSample{
		{Project: "beta", Service: "web", Container: "beta-web-1", BlkWrite: math.MaxUint64, Mem: 2048},
		{Project: "alpha", Service: "db", Container: "alpha-db-1", CPU: 12.5, Mem: 1048576, MemLimit: 2097152,
			NetRx: 100, NetTx: 200, BlkRead: 300, BlkWrite: 400},
	}
}

func render(t *testing.T, s []ExpositionSample, degraded bool) string {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteExposition(&buf, s, degraded); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestWriteExpositionGolden(t *testing.T) {
	if got := render(t, twoSamples(), false); got != goldenTwoSamples {
		t.Fatalf("mismatch\n--- got ---\n%s\n--- want ---\n%s", got, goldenTwoSamples)
	}
}

func TestWriteExpositionDeterministic(t *testing.T) {
	a := render(t, twoSamples(), false)
	s := twoSamples()
	s[0], s[1] = s[1], s[0]
	if b := render(t, s, false); a != b {
		t.Fatal("output depends on input order")
	}
}

func TestWriteExpositionEscaping(t *testing.T) {
	out := render(t, []ExpositionSample{{Project: `a\b`, Service: "x\"y", Container: "l1\nl2"}}, false)
	want := `stackstead_container_cpu_percent{project="a\\b",service="x\"y",container="l1\nl2"} 0`
	if !strings.Contains(out, want+"\n") {
		t.Fatalf("missing escaped line %q in:\n%s", want, out)
	}
	validate(t, out)
}

func TestWriteExpositionEmptyAndDegraded(t *testing.T) {
	out := render(t, nil, true)
	for _, name := range []string{
		"stackstead_container_cpu_percent", "stackstead_container_memory_bytes",
		"stackstead_container_memory_limit_bytes", "stackstead_container_network_receive_bytes_total",
		"stackstead_container_network_transmit_bytes_total", "stackstead_container_blkio_read_bytes_total",
		"stackstead_container_blkio_write_bytes_total", "stackstead_metrics_degraded",
	} {
		if !strings.Contains(out, "# HELP "+name+" ") || !strings.Contains(out, "# TYPE "+name+" ") {
			t.Errorf("missing HELP/TYPE for %s", name)
		}
	}
	if !strings.Contains(out, "stackstead_metrics_degraded 1\n") {
		t.Errorf("degraded gauge not 1:\n%s", out)
	}
	validate(t, out)
}

func TestWriteExpositionParses(t *testing.T) {
	validate(t, render(t, twoSamples(), true))
}

var (
	reMeta   = regexp.MustCompile(`^# (HELP|TYPE) [a-zA-Z_:][a-zA-Z0-9_:]* .+$`)
	reSample = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{(?:[a-zA-Z_][a-zA-Z0-9_]*="(?:[^"\\\n]|\\[\\"n])*"(?:,|\}))+)? (\S+)$`)
)

// validate is a tiny line-by-line check of the 0.0.4 text format.
func validate(t *testing.T, out string) {
	t.Helper()
	if !strings.HasSuffix(out, "\n") {
		t.Error("output must end with newline")
	}
	for i, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if strings.HasPrefix(line, "#") {
			if !reMeta.MatchString(line) {
				t.Errorf("line %d: bad comment %q", i+1, line)
			}
			continue
		}
		m := reSample.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("line %d: bad sample %q", i+1, line)
			continue
		}
		if _, err := strconv.ParseFloat(m[3], 64); err != nil {
			t.Errorf("line %d: bad value %q", i+1, m[3])
		}
	}
}

func TestFormatFloatSpecials(t *testing.T) {
	for in, want := range map[float64]string{math.NaN(): "NaN", math.Inf(1): "+Inf", math.Inf(-1): "-Inf", 1.5: "1.5"} {
		if got := formatFloat(in); got != want {
			t.Errorf("formatFloat(%v)=%q want %q", in, got, want)
		}
	}
}
