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
	labelCreated = "dev.stackstead.created"
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
	ID, Name, State, Status, Image, ImageID string // Name has no leading "/"
	Labels                                  map[string]string
	Mounts                                  []Mount
	Ports                                   []Port
	Networks                                map[string]string // network name -> IPv4 address
	Created                                 time.Time         // zero when Docker reports none
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
	Type, Name, Source, Destination string
	ReadOnly                        bool
}

// ContainerInspect is the safe subset of a container's inspect output:
// deliberately without Env, Cmd, Entrypoint or User, which can hold secrets.
type ContainerInspect struct {
	ID, Name, Image, State, Status, Health string    // Name has no leading "/"; Health is healthy|unhealthy|starting|none
	StartedAt, FinishedAt                  time.Time // zero when Docker reports none
	ExitCode                               int
	Running                                bool
	RestartPolicy                          string
	MemoryLimit                            int64 // HostConfig.Memory in bytes; 0 = unlimited
	Labels                                 map[string]string
	Ports                                  []Port
	Networks                               map[string]string // network name -> IPv4 address
	Mounts                                 []Mount
}

// Network is a Docker network with its IPv4 IPAM configuration.
type Network struct {
	ID, Name, Driver, Scope string
	Internal                bool
	Subnets                 []Subnet
	Options, Labels         map[string]string
	Containers              []NetworkEndpoint // populated only by InspectNetwork
}

// Project returns the compose project that owns the network, or "".
func (n Network) Project() string { return n.Labels[labelProject] }

// Parent returns the parent interface driver option, or "".
func (n Network) Parent() string { return n.Options["parent"] }

// IPvlanMode returns the ipvlan mode driver option, or "".
func (n Network) IPvlanMode() string { return n.Options["ipvlan_mode"] }

// NetworkEndpoint is one container attached to a network.
type NetworkEndpoint struct{ ID, Name, IPv4 string }

// NetworkSpec is the configuration sent when creating a Docker network.
type NetworkSpec struct {
	Name, Driver, Subnet, Gateway, IPRange, Parent, IPvlanMode string
	Internal                                                   bool
}

// Subnet is one IPv4 IPAM entry.
type Subnet struct {
	Prefix  netip.Prefix
	Gateway netip.Addr   // zero value if none
	IPRange netip.Prefix // zero value if none
}

// Wire shapes; only the fields we read.
type apiContainer struct {
	ID              string `json:"Id"`
	Names           []string
	Image           string
	ImageID         string
	Mounts          []apiMount
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
	ID                  string `json:"Id"`
	Name, Driver, Scope string
	Internal            bool
	Options, Labels     map[string]string
	IPAM                struct {
		Config []struct{ Subnet, Gateway, IPRange string }
	}
	Containers map[string]struct{ Name, IPv4Address string }
}

func (r apiNetwork) toNetwork() Network {
	n := Network{ID: r.ID, Name: r.Name, Driver: r.Driver, Scope: r.Scope, Internal: r.Internal, Options: r.Options, Labels: r.Labels}
	if n.Options == nil {
		n.Options = make(map[string]string)
	}
	if n.Labels == nil {
		n.Labels = make(map[string]string)
	}
	for _, cfg := range r.IPAM.Config {
		prefix, err := netip.ParsePrefix(cfg.Subnet)
		if err != nil || !prefix.Addr().Is4() {
			continue
		}
		sn := Subnet{Prefix: prefix.Masked()}
		if gw, err := netip.ParseAddr(cfg.Gateway); err == nil && gw.Is4() {
			sn.Gateway = gw
		}
		if ipRange, err := netip.ParsePrefix(cfg.IPRange); err == nil && ipRange.Addr().Is4() {
			sn.IPRange = ipRange.Masked()
		}
		n.Subnets = append(n.Subnets, sn)
	}
	return n
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
		Memory        int64
	}
	Mounts          []apiMount
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
		RestartPolicy: r.HostConfig.RestartPolicy.Name, MemoryLimit: r.HostConfig.Memory, Labels: r.Config.Labels,
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
		out.Mounts = append(out.Mounts, m.toMount())
	}
	return out
}

// Image is the storage inventory subset of a Docker image summary.
type Image struct {
	ID                    string
	RepoTags, RepoDigests []string
	Created               time.Time
	Size                  int64
	Labels                map[string]string
}

// ImageInspect contains image details without environment, command or entrypoint.
// User is internal metadata and must never be included in route responses.
type ImageInspect struct {
	Image
	User, OS, Architecture, Variant string
	LayerCount                      int
}

// ImageDeleteResult records Docker's individual untag and delete results.
type ImageDeleteResult struct{ Untagged, Deleted []string }

// Volume is the safe subset of Docker volume metadata. Other driver options
// can contain credentials and are deliberately never decoded.
type Volume struct {
	Name, Driver, Scope, Mountpoint string
	CreatedAt                       time.Time
	Labels                          map[string]string
	OptionType                      string
}

// VolumeInspect shares the safe volume inventory shape.
type VolumeInspect = Volume

type apiMount struct {
	Type, Name, Source, Destination string
	RW                              bool
}

func (m apiMount) toMount() Mount {
	name := ""
	if m.Type == "volume" {
		name = m.Name
	}
	return Mount{Type: m.Type, Name: name, Source: m.Source, Destination: m.Destination, ReadOnly: !m.RW}
}

type apiImage struct {
	ID                    string `json:"Id"`
	RepoTags, RepoDigests []string
	Created               int64
	Size                  int64
	Labels                map[string]string
}

func (r apiImage) toImage() Image {
	image := Image{ID: r.ID, RepoTags: imageTags(r.RepoTags), RepoDigests: r.RepoDigests, Size: r.Size, Labels: r.Labels}
	if r.Created > 0 {
		image.Created = time.Unix(r.Created, 0).UTC()
	}
	return image
}
func imageTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag != "<none>:<none>" && tag != "<none>" && tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

type apiImageInspect struct {
	ID                    string `json:"Id"`
	RepoTags, RepoDigests []string
	Created               time.Time
	Size                  int64
	Config                struct {
		Labels map[string]string
		User   string
	}
	OS, Architecture, Variant string
	RootFS                    struct{ Layers []string }
}

func (r apiImageInspect) toInspect() ImageInspect {
	return ImageInspect{Image: Image{ID: r.ID, RepoTags: imageTags(r.RepoTags), RepoDigests: r.RepoDigests, Created: r.Created, Size: r.Size, Labels: r.Config.Labels}, User: r.Config.User, OS: r.OS, Architecture: r.Architecture, Variant: r.Variant, LayerCount: len(r.RootFS.Layers)}
}

type apiVolume struct {
	Name, Driver, Scope, Mountpoint string
	CreatedAt                       time.Time
	Labels                          map[string]string
	Options                         struct{ Type string }
}

func (r apiVolume) toVolume() Volume {
	return Volume{Name: r.Name, Driver: r.Driver, Scope: r.Scope, Mountpoint: r.Mountpoint, CreatedAt: r.CreatedAt, Labels: r.Labels, OptionType: r.Options.Type}
}

// DFKind selects one Docker disk-usage category.
type DFKind string

const (
	DFImages     DFKind = "image"
	DFContainers DFKind = "container"
	DFVolumes    DFKind = "volume"
	DFBuildCache DFKind = "build-cache"
)

// DFSnapshot marks populated categories explicitly, including empty categories.
// A nil size means Docker did not report a size; negative sizes mean unknown.
type DFSnapshot struct {
	Kinds      []DFKind
	Images     []DFImage
	Containers []DFContainer
	Volumes    []DFVolume
	BuildCache []DFBuildCacheRecord
}

type DFImage struct {
	ID               string
	Size, SharedSize *int64
}
type DFContainer struct {
	ID     string
	SizeRw *int64
}
type DFVolume struct {
	Name      string
	UsageData *DFVolumeUsage
}
type DFVolumeUsage struct {
	Size, RefCount int64
}
type DFBuildCacheRecord struct {
	ID, Type, Description string
	Size                  *int64
	InUse, Shared         bool
	CreatedAt             time.Time
	LastUsedAt            *time.Time
	UsageCount            int64
}
