package api

import (
	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/fsguard"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// Response and request shapes. They live in apitypes, not in package stacks, so
// the domain packages stay free of transport concerns.

// Wire types live in apitypes; these aliases keep the handlers and
// converters unchanged.
type (
	summaryDTO           = apitypes.StackSummary
	containerDTO         = apitypes.ContainerInfo
	serviceDetailDTO     = apitypes.ServiceDetail
	detailDTO            = apitypes.StackDetail
	serviceConfig        = apitypes.ServiceConfig
	buildDTO             = apitypes.Build
	envVarDTO            = apitypes.EnvVar
	attachmentDTO        = apitypes.NetworkAttachment
	limitsDTO            = apitypes.Limits
	healthcheckDTO       = apitypes.Healthcheck
	addServiceRequest    = apitypes.AddServiceRequest
	createStackRequest   = apitypes.CreateStackRequest
	importRequest        = apitypes.ImportRequest
	rawComposeBody       = apitypes.RawCompose
	updateServiceRequest = apitypes.UpdateServiceRequest
	hashResponse         = apitypes.HashResponse
	previewResponse      = apitypes.PreviewResponse
	operationDTO         = apitypes.Operation
	fsEntryDTO           = apitypes.FSEntry
	statusDTO            = apitypes.Status
	operationDetailDTO   = apitypes.OperationDetail
)

func toSummary(s stacks.Summary) summaryDTO {
	return summaryDTO{Name: s.Name, Path: s.Path, Project: s.Project, Origin: string(s.Origin),
		Status: string(s.Status), ServiceCount: s.Services, Running: s.Running,
		Health: apitypes.HealthCounts(s.Health), Ports: nonNil(s.Ports)}
}

// nonNil keeps empty lists encoding as [] instead of null.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func toDetail(d stacks.Detail) detailDTO {
	out := detailDTO{Name: d.Name, Path: d.Path, Project: d.Project, Origin: string(d.Origin),
		Status: string(d.Status), Running: d.Running, ServiceCount: len(d.Services), Hash: d.Hash, ParseError: d.ParseError,
		Health: apitypes.HealthCounts(d.Health), Ports: nonNil(d.Ports),
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

func serviceConfigToDomain(c serviceConfig) composefile.ServiceConfig {
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

func toOperation(o stacks.Operation) operationDTO {
	dto := operationDTO{ID: o.ID, Stack: o.Stack, Service: o.Service, Action: o.Action, State: string(o.State),
		StartedAt: o.StartedAt, Error: o.Error}
	// The internal -1 means "no exit code"; only report real ones. A
	// succeeded operation ran compose to exit 0.
	switch {
	case o.State == stacks.OpSucceeded:
		zero := 0
		dto.ExitCode = &zero
	case o.State == stacks.OpFailed && o.ExitCode >= 0:
		code := o.ExitCode
		dto.ExitCode = &code
	}
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

func toFSEntries(entries []fsguard.Entry) []fsEntryDTO {
	out := make([]fsEntryDTO, 0, len(entries))
	for _, e := range entries {
		out = append(out, fsEntryDTO(e))
	}
	return out
}
