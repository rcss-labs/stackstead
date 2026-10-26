package alerts

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// restartLoopThreshold is the restart count in 24 h that makes a loop.
const restartLoopThreshold = 3

// item is an alert with the internal identity Engine tracks "since" by.
type item struct {
	Alert
	subject       string    // distinguishes alerts that share kind, stack, service and container
	fromContainer bool      // derived from the container listing
	startedAt     time.Time // when the problem began, if known; otherwise Engine uses first observation
}

// key identifies an alert by names, not ids, so a recreated container keeps it.
func (i item) key() string {
	return strings.Join([]string{i.Kind, i.Stack, i.Service, i.Container, i.subject}, "|")
}

// Evaluate applies the alert rules. Since is left zero; Engine fills it.
// restarts is keyed by container id and may be nil.
func Evaluate(details []stacks.Detail, containers []engine.Container, restarts map[string]int, connected, degraded bool) []Alert {
	items := evaluate(details, containers, restarts, connected, degraded)
	out := make([]Alert, len(items))
	for i, it := range items {
		out[i] = it.Alert
	}
	return out
}

func evaluate(details []stacks.Detail, containers []engine.Container, restarts map[string]int, connected, degraded bool) []item {
	var out []item
	byProject := map[string]stacks.Detail{}
	for _, d := range details {
		if d.Project != "" {
			byProject[d.Project] = d
		}
	}
	for _, c := range containers {
		d, registered := byProject[c.Project()]
		out = append(out, containerAlerts(d, registered, c, restarts[c.ID])...)
	}
	for _, d := range details {
		out = append(out, portClashes(d, details, containers)...)
		for _, f := range d.Overrides {
			out = append(out, item{Alert: Alert{Kind: KindOverride, Severity: SeverityWarning, Stack: d.Name,
				Message: f + " found; override files are not supported yet"}, subject: f})
		}
		if d.ParseError != "" {
			first, _, _ := strings.Cut(d.ParseError, "\n")
			out = append(out, item{Alert: Alert{Kind: KindParseError, Severity: SeverityWarning, Stack: d.Name, Message: first}})
		}
	}
	if !connected {
		out = append(out, item{Alert: Alert{Kind: KindDockerDisconnected, Severity: SeverityDanger,
			Message: "Docker is unreachable; status may be stale"}})
	}
	if degraded {
		out = append(out, item{Alert: Alert{Kind: KindMetricsDegraded, Severity: SeverityWarning,
			Message: "Metrics are not being stored (database write failures)"}})
	}
	return out
}

// containerAlerts covers exited (registered stacks only), unhealthy and
// restart_loop (any container). Stack and service are filled only for a
// container of a registered stack; otherwise messages name the container.
func containerAlerts(d stacks.Detail, registered bool, c engine.Container, restarts int) []item {
	base := Alert{Container: c.Name}
	label := c.Name
	if registered {
		base.Stack, base.Service = d.Name, c.ComposeService()
		if base.Service != "" {
			label = base.Service
		}
	}
	var out []item
	add := func(kind, severity, msg string) {
		a := base
		a.Kind, a.Severity, a.Message = kind, severity, msg
		out = append(out, item{Alert: a, fromContainer: true})
	}
	if registered && c.State == "exited" && c.Labels["com.docker.compose.oneoff"] != "True" {
		if code, ok := c.ExitCode(); ok && code != 0 {
			msg := fmt.Sprintf("%s exited (%d)", label, code)
			if p := restartPolicy(d, c.ComposeService()); p == "always" || p == "unless-stopped" {
				msg += " and restart policy is " + p
			}
			if restarts > 0 {
				msg += fmt.Sprintf("; %d restarts in 24h", restarts)
			}
			add(KindExited, SeverityDanger, msg)
		}
	}
	if c.Health() == "unhealthy" {
		add(KindUnhealthy, SeverityDanger, label+" is unhealthy")
	}
	if restarts >= restartLoopThreshold {
		add(KindRestartLoop, SeverityWarning, fmt.Sprintf("%s restarted %d times in 24h", label, restarts))
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

// holder is whatever occupies a host port.
type holder struct {
	label         string
	fromContainer bool
}

// portClashes reports, per service, each distinct holder of a host port the
// service declares: another registered stack declaring it, or a running
// container of another project or of no project publishing it. A range yields
// one alert per holder, naming its first clashing port.
func portClashes(d stacks.Detail, all []stacks.Detail, containers []engine.Container) []item {
	var out []item
	for _, s := range d.Services {
		reported := map[string]bool{}
		for _, p := range declaredPorts(s) {
			for _, h := range portHolders(d, p, all, containers) {
				if reported[h.label] {
					continue
				}
				reported[h.label] = true
				out = append(out, item{
					Alert: Alert{Kind: KindPortClash, Severity: SeverityWarning, Stack: d.Name, Service: s.Name,
						Message: fmt.Sprintf("%s wants %d but %s holds it", s.Name, p.port, h.label)},
					subject:       fmt.Sprintf("%d/%s|%s", p.port, p.proto, h.label),
					fromContainer: h.fromContainer,
				})
			}
		}
	}
	return out
}

func portHolders(d stacks.Detail, p portKey, all []stacks.Detail, containers []engine.Container) []holder {
	var holders []holder
	registered := map[string]string{} // project -> stack
	for _, o := range all {
		if o.Project != "" {
			registered[o.Project] = o.Name
		}
		if o.Name == d.Name {
			continue
		}
		if declares(o, p) {
			holders = append(holders, holder{label: o.Name})
		}
	}
	for _, c := range containers {
		if c.State != "running" || (c.Project() != "" && c.Project() == d.Project) || !publishes(c, p) {
			continue
		}
		switch {
		case c.Project() == "":
			holders = append(holders, holder{"a non-compose container (" + c.Name + ")", true})
		case registered[c.Project()] != "":
			holders = append(holders, holder{registered[c.Project()], true})
		default:
			holders = append(holders, holder{"a container of project " + c.Project() + " (" + c.Name + ")", true})
		}
	}
	return holders
}

func declares(d stacks.Detail, p portKey) bool {
	for _, s := range d.Services {
		for _, q := range declaredPorts(s) {
			if q == p {
				return true
			}
		}
	}
	return false
}

func publishes(c engine.Container, p portKey) bool {
	for _, cp := range c.Ports {
		proto := cp.Type
		if proto == "" {
			proto = "tcp"
		}
		if cp.PublicPort == p.port && proto == p.proto {
			return true
		}
	}
	return false
}

// backupAlerts reports the last finished backup run when it failed or was
// partial. An unreadable history raises nothing: it is not evidence of a failed backup.
func (e *Engine) backupAlerts(ctx context.Context) []item {
	if e.backups == nil {
		return nil
	}
	run, ok, err := e.backups.LastRun(ctx)
	if err != nil || !ok {
		return nil
	}
	switch run.Status {
	case "failed":
		return []item{{Alert: Alert{Kind: KindBackupFailed, Severity: SeverityDanger, Message: "Last backup failed: " + run.Error}, startedAt: run.EndedAt}}
	case "partial":
		failed := 0
		for _, it := range run.Items {
			if it.Status == "failed" {
				failed++
			}
		}
		return []item{{Alert: Alert{Kind: KindBackupPartial, Severity: SeverityWarning,
			Message: fmt.Sprintf("Last backup was partial: %d item(s) failed", failed)}, startedAt: run.EndedAt}}
	}
	return nil
}
