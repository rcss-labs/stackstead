package engine

import (
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	labelProject = "com.docker.compose.project"
	labelService = "com.docker.compose.service"
)

// Port is one published or exposed container port.
type Port struct {
	IP          string
	PrivatePort uint16
	PublicPort  uint16 // 0 when the port is not published
	Type        string
}

// Container is the subset of the Engine API container summary Stackstead reads.
type Container struct {
	ID, Name, State, Status, Image string // Name has no leading "/"
	Labels                         map[string]string
	Ports                          []Port
	Networks                       map[string]string // network name -> IPv4 address
	Created                        time.Time         // zero when Docker reports none
}

// Project returns the compose project the container belongs to, or "".
func (c Container) Project() string { return c.Labels[labelProject] }

// ComposeService returns the compose service name, or "".
func (c Container) ComposeService() string { return c.Labels[labelService] }

// Health reports the container's healthcheck result parsed from Docker's
// human Status text: "healthy", "unhealthy", "starting", or "none" when the
// container is not running or has no healthcheck.
func (c Container) Health() string {
	if c.State != "running" {
		return "none"
	}
	switch {
	case strings.Contains(c.Status, "(unhealthy)"):
		return "unhealthy"
	case strings.Contains(c.Status, "(healthy)"):
		return "healthy"
	case strings.Contains(c.Status, "(health: starting)"):
		return "starting"
	}
	return "none"
}

var exitedStatus = regexp.MustCompile(`^Exited \((-?\d+)\)`)

// ExitCode returns the exit code from an "Exited (N) ..." Status, or ok=false.
func (c Container) ExitCode() (code int, ok bool) {
	m := exitedStatus.FindStringSubmatch(c.Status)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// Mount is one container mount.
type Mount struct {
	Type, Source, Destination string
	ReadOnly                  bool
}

// ContainerInspect is the safe subset of a container's inspect output:
// deliberately without Env, Cmd, Entrypoint or User, which can hold secrets.
type ContainerInspect struct {
	ID, Name, Image, State, Status, Health string    // Name has no leading "/"; Health is healthy|unhealthy|starting|none
	StartedAt, FinishedAt                  time.Time // zero when Docker reports none
	ExitCode                               int
	Running                                bool
	RestartPolicy                          string
	Labels                                 map[string]string
	Ports                                  []Port
	Networks                               map[string]string // network name -> IPv4 address
	Mounts                                 []Mount
}

// Network is a Docker network with its IPv4 IPAM configuration.
type Network struct {
	ID, Name, Driver string
	Subnets          []Subnet
}

// Subnet is one IPv4 IPAM entry.
type Subnet struct {
	Prefix  netip.Prefix
	Gateway netip.Addr // zero value if none
}

// Wire shapes; only the fields we read.
type apiContainer struct {
	ID              string `json:"Id"`
	Names           []string
	Image           string
	Created         int64 // unix seconds
	State           string
	Status          string
	Labels          map[string]string
	Ports           []apiPort
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress  string
			IPAMConfig *struct{ IPv4Address string }
		}
	}
}

type apiPort struct {
	IP          string
	PrivatePort uint16
	PublicPort  uint16
	Type        string
}

type apiNetwork struct {
	ID     string `json:"Id"`
	Name   string
	Driver string
	IPAM   struct {
		Config []struct{ Subnet, Gateway string }
	}
}

type apiInspect struct {
	ID    string `json:"Id"`
	Name  string
	Image string
	State struct {
		Status     string
		Running    bool
		ExitCode   int
		StartedAt  time.Time
		FinishedAt time.Time
		Health     *struct{ Status string }
	}
	Config struct {
		Image  string
		Labels map[string]string
	}
	HostConfig struct {
		RestartPolicy struct{ Name string }
	}
	Mounts []struct {
		Type, Source, Destination string
		RW                        bool
	}
	NetworkSettings struct {
		Ports    map[string][]struct{ HostIp, HostPort string }
		Networks map[string]struct{ IPAddress string }
	}
}

func (r apiInspect) toInspect() ContainerInspect {
	out := ContainerInspect{
		ID: r.ID, Name: strings.TrimPrefix(r.Name, "/"), Image: r.Config.Image,
		State: r.State.Status, Status: r.State.Status, Health: "none",
		StartedAt: r.State.StartedAt, FinishedAt: r.State.FinishedAt,
		ExitCode: r.State.ExitCode, Running: r.State.Running,
		RestartPolicy: r.HostConfig.RestartPolicy.Name, Labels: r.Config.Labels,
		Networks: make(map[string]string, len(r.NetworkSettings.Networks)),
	}
	if out.Image == "" {
		out.Image = r.Image
	}
	if h := r.State.Health; h != nil && h.Status != "" {
		out.Health = h.Status
	}
	// Docker reports the zero time as year 1; normalise to the Go zero value.
	if out.StartedAt.Year() <= 1 {
		out.StartedAt = time.Time{}
	}
	if out.FinishedAt.Year() <= 1 {
		out.FinishedAt = time.Time{}
	}
	for key, bindings := range r.NetworkSettings.Ports {
		portStr, proto, _ := strings.Cut(key, "/")
		private, err := strconv.ParseUint(portStr, 10, 16)
		if err != nil {
			continue
		}
		for _, b := range bindings {
			public, err := strconv.ParseUint(b.HostPort, 10, 16)
			if err != nil {
				continue
			}
			out.Ports = append(out.Ports, Port{IP: b.HostIp, PrivatePort: uint16(private), PublicPort: uint16(public), Type: proto})
		}
	}
	sort.Slice(out.Ports, func(i, j int) bool {
		a, b := out.Ports[i], out.Ports[j]
		if a.PrivatePort != b.PrivatePort {
			return a.PrivatePort < b.PrivatePort
		}
		return a.PublicPort < b.PublicPort
	})
	for name, n := range r.NetworkSettings.Networks {
		out.Networks[name] = n.IPAddress
	}
	for _, m := range r.Mounts {
		out.Mounts = append(out.Mounts, Mount{Type: m.Type, Source: m.Source, Destination: m.Destination, ReadOnly: !m.RW})
	}
	return out
}
