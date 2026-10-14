package metrics

import (
	"bufio"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

// ExpositionSample is the current reading of one container, as exposed on /metrics.
type ExpositionSample struct {
	Project, Service, Container string
	CPU                         float64
	Mem, MemLimit               uint64
	NetRx, NetTx                uint64
	BlkRead, BlkWrite           uint64
}

type family struct {
	name, help, typ string
	value           func(ExpositionSample) string
}

func uintValue(f func(ExpositionSample) uint64) func(ExpositionSample) string {
	return func(s ExpositionSample) string { return strconv.FormatUint(f(s), 10) }
}

var families = []family{
	{"stackstead_container_cpu_percent", "Container CPU usage in percent of one core.", "gauge",
		func(s ExpositionSample) string { return formatFloat(s.CPU) }},
	{"stackstead_container_memory_bytes", "Container memory usage in bytes.", "gauge",
		uintValue(func(s ExpositionSample) uint64 { return s.Mem })},
	{"stackstead_container_memory_limit_bytes", "Container memory limit in bytes.", "gauge",
		uintValue(func(s ExpositionSample) uint64 { return s.MemLimit })},
	{"stackstead_container_network_receive_bytes_total", "Total bytes received over the network.", "counter",
		uintValue(func(s ExpositionSample) uint64 { return s.NetRx })},
	{"stackstead_container_network_transmit_bytes_total", "Total bytes transmitted over the network.", "counter",
		uintValue(func(s ExpositionSample) uint64 { return s.NetTx })},
	{"stackstead_container_blkio_read_bytes_total", "Total bytes read from block devices.", "counter",
		uintValue(func(s ExpositionSample) uint64 { return s.BlkRead })},
	{"stackstead_container_blkio_write_bytes_total", "Total bytes written to block devices.", "counter",
		uintValue(func(s ExpositionSample) uint64 { return s.BlkWrite })},
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// helpEscaper escapes HELP text per the exposition format: backslash and
// newline (double quotes are only escaped in label values).
var helpEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`)

func writeFamilyHeader(bw *bufio.Writer, name, help, typ string) {
	bw.WriteString("# HELP " + name + " " + helpEscaper.Replace(help) + "\n")
	bw.WriteString("# TYPE " + name + " " + typ + "\n")
}

// formatFloat renders a float the way the Prometheus text format expects.
func formatFloat(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// WriteExposition writes samples in Prometheus text format 0.0.4. Samples are
// sorted by (project, service, container) so output is deterministic; the
// input slice is not modified.
func WriteExposition(w io.Writer, samples []ExpositionSample, degraded bool) error {
	sorted := append([]ExpositionSample(nil), samples...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.Container < b.Container
	})

	bw := bufio.NewWriter(w)
	for _, f := range families {
		writeFamilyHeader(bw, f.name, f.help, f.typ)
		for _, s := range sorted {
			bw.WriteString(f.name + `{project="` + labelEscaper.Replace(s.Project) +
				`",service="` + labelEscaper.Replace(s.Service) +
				`",container="` + labelEscaper.Replace(s.Container) + `"} ` + f.value(s) + "\n")
		}
	}
	d := "0"
	if degraded {
		d = "1"
	}
	writeFamilyHeader(bw, "stackstead_metrics_degraded", "1 if metrics collection is degraded (store write failures), else 0.", "gauge")
	bw.WriteString("stackstead_metrics_degraded " + d + "\n")
	return bw.Flush()
}
