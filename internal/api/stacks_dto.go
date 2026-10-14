package api

import (
	"time"

	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/fsguard"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// Response and request shapes. They live here, not in package stacks, so the
// domain packages stay free of transport concerns.

type summaryDTO struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	Project      string `json:"project"`
	Origin       string `json:"origin"`
	Status       string `json:"status"`
	ServiceCount int    `json:"serviceCount"`
	Running      int    `json:"running"`
}

func toSummary(s stacks.Summary) summaryDTO {
	return summaryDTO{Name: s.Name, Path: s.Path, Project: s.Project, Origin: string(s.Origin),
		Status: string(s.Status), ServiceCount: s.Services, Running: s.Running}
}

type containerDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Service string `json:"service"`
	State   string `json:"state"`
	Status  string `json:"status"`
}

type serviceDetailDTO struct {
	Name       string         `json:"name"`
	Config     serviceConfig  `json:"config"`
	ReadOnly   []string       `json:"readOnly"`
	Containers []containerDTO `json:"containers"`
}

// detailDTO flattens the summary but carries the per-service list under
// "services"; serviceCount mirrors the summary field.
type detailDTO struct {
	Name         string             `json:"name"`
	Path         string             `json:"path"`
	Project      string             `json:"project"`
	Origin       string             `json:"origin"`
	Status       string             `json:"status"`
	Running      int                `json:"running"`
	ServiceCount int                `json:"serviceCount"`
	Hash         string             `json:"hash"`
	Services     []serviceDetailDTO `json:"services"`
	ParseError   string             `json:"parseError,omitempty"`
}

func toDetail(d stacks.Detail) detailDTO {
	out := detailDTO{Name: d.Name, Path: d.Path, Project: d.Project, Origin: string(d.Origin),
		Status: string(d.Status), Running: d.Running, ServiceCount: len(d.Services), Hash: d.Hash, ParseError: d.ParseError,
		Services: make([]serviceDetailDTO, 0, len(d.Services))}
	for _, sd := range d.Services {
		dto := serviceDetailDTO{Name: sd.Name, Config: toServiceConfig(sd.Config),
			ReadOnly: make([]string, 0, len(sd.ReadOnly)), Containers: make([]containerDTO, 0, len(sd.Containers))}
		for _, f := range sd.ReadOnly {
			dto.ReadOnly = append(dto.ReadOnly, string(f))
		}
		for _, c := range sd.Containers {
			dto.Containers = append(dto.Containers, containerDTO(c))
		}
		out.Services = append(out.Services, dto)
	}
	return out
}

// serviceConfig is the JSON form of composefile.ServiceConfig.
type serviceConfig struct {
	Image       string          `json:"image,omitempty"`
	Build       *buildDTO       `json:"build,omitempty"`
	Ports       []string        `json:"ports,omitempty"`
	Volumes     []string        `json:"volumes,omitempty"`
	Environment []envVarDTO     `json:"environment,omitempty"`
	Networks    []attachmentDTO `json:"networks,omitempty"`
	NetworkMode string          `json:"networkMode,omitempty"`
	Limits      limitsDTO       `json:"limits"`
	Restart     string          `json:"restart,omitempty"`
	Healthcheck *healthcheckDTO `json:"healthcheck,omitempty"`
}

type buildDTO struct {
	Context    string `json:"context"`
	Dockerfile string `json:"dockerfile,omitempty"`
}
type envVarDTO struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type attachmentDTO struct {
	Name        string `json:"name"`
	IPv4Address string `json:"ipv4Address,omitempty"`
}
type limitsDTO struct {
	CPUs   string `json:"cpus,omitempty"`
	Memory string `json:"memory,omitempty"`
}
type healthcheckDTO struct {
	Test        []string `json:"test,omitempty"`
	Interval    string   `json:"interval,omitempty"`
	Timeout     string   `json:"timeout,omitempty"`
	StartPeriod string   `json:"startPeriod,omitempty"`
	Retries     int      `json:"retries,omitempty"`
}

func toServiceConfig(c composefile.ServiceConfig) serviceConfig {
	out := serviceConfig{Image: c.Image, Ports: c.Ports, Volumes: c.Volumes, NetworkMode: c.NetworkMode,
		Limits: limitsDTO(c.Limits), Restart: c.Restart}
	if c.Build != nil {
		b := buildDTO(*c.Build)
		out.Build = &b
	}
	for _, e := range c.Environment {
		out.Environment = append(out.Environment, envVarDTO(e))
	}
	for _, n := range c.Networks {
		out.Networks = append(out.Networks, attachmentDTO(n))
	}
	if c.Healthcheck != nil {
		h := healthcheckDTO(*c.Healthcheck)
		out.Healthcheck = &h
	}
	return out
}

func (c serviceConfig) toDomain() composefile.ServiceConfig {
	out := composefile.ServiceConfig{Image: c.Image, Ports: c.Ports, Volumes: c.Volumes, NetworkMode: c.NetworkMode,
		Limits: composefile.Limits(c.Limits), Restart: c.Restart}
	if c.Build != nil {
		b := composefile.Build(*c.Build)
		out.Build = &b
	}
	for _, e := range c.Environment {
		out.Environment = append(out.Environment, composefile.EnvVar(e))
	}
	for _, n := range c.Networks {
		out.Networks = append(out.Networks, composefile.NetworkAttachment(n))
	}
	if c.Healthcheck != nil {
		h := composefile.Healthcheck(*c.Healthcheck)
		out.Healthcheck = &h
	}
	return out
}

type createStackRequest struct {
	Name    string        `json:"name"`
	Service string        `json:"service"`
	Config  serviceConfig `json:"config"`
}

type importRequest struct {
	Path string `json:"path"`
}

type rawComposeBody struct {
	Content string `json:"content"`
	Hash    string `json:"hash"`
}

type updateServiceRequest struct {
	Config serviceConfig `json:"config"`
	Hash   string        `json:"hash"`
}

type hashResponse struct {
	Hash string `json:"hash"`
}

type operationDTO struct {
	ID        string     `json:"id"`
	Stack     string     `json:"stack"`
	Action    string     `json:"action"`
	State     string     `json:"state"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	ExitCode  int        `json:"exitCode"`
	Error     string     `json:"error,omitempty"`
}

func toOperation(o stacks.Operation) operationDTO {
	dto := operationDTO{ID: o.ID, Stack: o.Stack, Action: o.Action, State: string(o.State),
		StartedAt: o.StartedAt, ExitCode: o.ExitCode, Error: o.Error}
	if !o.EndedAt.IsZero() {
		t := o.EndedAt
		dto.EndedAt = &t
	}
	return dto
}

func toOperations(ops []stacks.Operation) []operationDTO {
	out := make([]operationDTO, 0, len(ops))
	for _, o := range ops {
		out = append(out, toOperation(o))
	}
	return out
}

type operationDetailDTO struct {
	operationDTO
	Lines []string `json:"lines"`
}

type networkDTO struct {
	Name    string   `json:"name"`
	Driver  string   `json:"driver"`
	Subnets []string `json:"subnets"`
}

func toNetworks(nets []engine.Network) []networkDTO {
	out := make([]networkDTO, 0, len(nets))
	for _, n := range nets {
		dto := networkDTO{Name: n.Name, Driver: n.Driver, Subnets: make([]string, 0, len(n.Subnets))}
		for _, s := range n.Subnets {
			dto.Subnets = append(dto.Subnets, s.Prefix.String())
		}
		out = append(out, dto)
	}
	return out
}

type fsEntryDTO struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

func toFSEntries(entries []fsguard.Entry) []fsEntryDTO {
	out := make([]fsEntryDTO, 0, len(entries))
	for _, e := range entries {
		out = append(out, fsEntryDTO(e))
	}
	return out
}

type statusDTO struct {
	Docker  string `json:"docker"`
	Compose string `json:"compose"`
}
