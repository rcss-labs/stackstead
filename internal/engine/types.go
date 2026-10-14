package engine

import "net/netip"

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
}

// Project returns the compose project the container belongs to, or "".
func (c Container) Project() string { return c.Labels[labelProject] }

// ComposeService returns the compose service name, or "".
func (c Container) ComposeService() string { return c.Labels[labelService] }

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
