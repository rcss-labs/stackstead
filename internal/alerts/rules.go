package alerts

import (
	"fmt"
	"strings"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// restartLoopThreshold is the restart count in 24 h that makes a loop.
const restartLoopThreshold = 3

// Evaluate applies the alert rules. Since is left zero; Engine fills it.
// restarts is keyed by container id and may be nil.
func Evaluate(details []stacks.Detail, containers []engine.Container, restarts map[string]int, connected, degraded bool) []Alert {
	var out []Alert
	byProject := map[string]stacks.Detail{}
	for _, d := range details {
		if d.Project != "" {
			byProject[d.Project] = d
		}
	}
	for _, c := range containers {
		d, ok := byProject[c.Project()]
		if !ok {
			continue
		}
		out = append(out, containerAlerts(d, c, restarts[c.ID])...)
	}
	for _, d := range details {
		out = append(out, portClashes(d, details, containers)...)
		for _, f := range d.Overrides {
			out = append(out, Alert{Kind: KindOverride, Severity: SeverityWarning, Stack: d.Name,
				Message: f + " found; override files are not supported yet"})
		}
		if d.ParseError != "" {
			first, _, _ := strings.Cut(d.ParseError, "\n")
			out = append(out, Alert{Kind: KindParseError, Severity: SeverityWarning, Stack: d.Name, Message: first})
		}
	}
	if !connected {
		out = append(out, Alert{Kind: KindDockerDisconnected, Severity: SeverityDanger,
			Message: "Docker is unreachable; status may be stale"})
	}
	if degraded {
		out = append(out, Alert{Kind: KindMetricsDegraded, Severity: SeverityWarning,
			Message: "Metrics are not being stored (database write failures)"})
	}
	return out
}

// containerAlerts covers exited, unhealthy and restart_loop for a container
// of registered stack d.
func containerAlerts(d stacks.Detail, c engine.Container, restarts int) []Alert {
	service := c.ComposeService()
	base := Alert{Stack: d.Name, Service: service, Container: c.Name}
	var out []Alert
	if c.State == "exited" {
		if code, ok := c.ExitCode(); ok && code != 0 {
			msg := fmt.Sprintf("%s exited (%d)", service, code)
			if p := restartPolicy(d, service); p == "always" || p == "unless-stopped" {
				msg += " and restart policy is " + p
			}
			if restarts > 0 {
				msg += fmt.Sprintf("; %d restarts in 24h", restarts)
			}
			a := base
			a.Kind, a.Severity, a.Message = KindExited, SeverityDanger, msg
			out = append(out, a)
		}
	}
	if c.Health() == "unhealthy" {
		a := base
		a.Kind, a.Severity, a.Message = KindUnhealthy, SeverityDanger, service+" is unhealthy"
		out = append(out, a)
	}
	if restarts >= restartLoopThreshold {
		a := base
		a.Kind, a.Severity = KindRestartLoop, SeverityWarning
		a.Message = fmt.Sprintf("%s restarted %d times in 24h", service, restarts)
		out = append(out, a)
	}
	return out
}

func restartPolicy(d stacks.Detail, service string) string {
	for _, s := range d.Services {
		if s.Name == service {
			return s.Config.Restart
		}
	}
	return ""
}

type portKey struct {
	port  uint16
	proto string
}

// declaredPorts expands a service's port specs into host ports in order.
func declaredPorts(s stacks.ServiceDetail) []portKey {
	var out []portKey
	for _, spec := range s.Config.Ports {
		hps, err := stacks.ParsePorts(spec)
		if err != nil {
			continue
		}
		for _, hp := range hps {
			out = append(out, portKey{hp.Port, hp.Proto})
		}
	}
	return out
}

// portClashes reports, per service, each distinct holder of a host port the
// service declares: another registered stack declaring it, or a running
// container of another project or of no project publishing it. A range yields
// one alert per holder, naming its first clashing port.
func portClashes(d stacks.Detail, all []stacks.Detail, containers []engine.Container) []Alert {
	var out []Alert
	for _, s := range d.Services {
		reported := map[string]bool{}
		for _, p := range declaredPorts(s) {
			for _, holder := range portHolders(d, p, all, containers) {
				if reported[holder] {
					continue
				}
				reported[holder] = true
				out = append(out, Alert{Kind: KindPortClash, Severity: SeverityWarning, Stack: d.Name, Service: s.Name,
					Message: fmt.Sprintf("%s wants %d but %s holds it", s.Name, p.port, holder)})
			}
		}
	}
	return out
}

func portHolders(d stacks.Detail, p portKey, all []stacks.Detail, containers []engine.Container) []string {
	var holders []string
	for _, other := range all {
		if other.Name == d.Name {
			continue
		}
		for _, s := range other.Services {
			for _, q := range declaredPorts(s) {
				if q == p {
					holders = append(holders, other.Name)
					goto nextStack
				}
			}
		}
	nextStack:
	}
	registered := map[string]string{} // project -> stack
	for _, o := range all {
		if o.Project != "" {
			registered[o.Project] = o.Name
		}
	}
	for _, c := range containers {
		if c.State != "running" || (c.Project() != "" && c.Project() == d.Project) {
			continue
		}
		for _, cp := range c.Ports {
			proto := cp.Type
			if proto == "" {
				proto = "tcp"
			}
			if cp.PublicPort != p.port || proto != p.proto {
				continue
			}
			switch {
			case c.Project() == "":
				holders = append(holders, "a non-compose container ("+c.Name+")")
			case registered[c.Project()] != "":
				holders = append(holders, registered[c.Project()])
			default:
				holders = append(holders, c.Project())
			}
			break
		}
	}
	return holders
}
