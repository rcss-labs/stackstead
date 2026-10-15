// Package apitypes holds the JSON wire types of the Stackstead HTTP API. The
// server (internal/api) aliases its DTOs to these and the CLI client decodes
// into them, so both sides share one definition. It imports only the standard
// library and must stay free of server packages.
package apitypes

import "time"

type StackSummary struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	Project      string `json:"project"`
	Origin       string `json:"origin"`
	Status       string `json:"status"`
	ServiceCount int    `json:"serviceCount"`
	Running      int    `json:"running"`
	// Health counts the stack's containers by healthcheck result; Failed counts
	// dead, restarting and non-zero-exit containers.
	Health HealthCounts `json:"health"`
	// Ports lists the host ports the compose file publishes, ranges as "a-b".
	Ports []string `json:"ports"`
}

type HealthCounts struct {
	Healthy   int `json:"healthy"`
	Unhealthy int `json:"unhealthy"`
	Starting  int `json:"starting"`
	None      int `json:"none"`
	Failed    int `json:"failed"`
}

type ContainerInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Service string `json:"service"`
	State   string `json:"state"`
	Status  string `json:"status"`
	// Health is "healthy", "unhealthy", "starting" or "none".
	Health string `json:"health"`
}

type Build struct {
	Context    string `json:"context"`
	Dockerfile string `json:"dockerfile,omitempty"`
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type NetworkAttachment struct {
	Name        string `json:"name"`
	IPv4Address string `json:"ipv4Address,omitempty"`
}

type Limits struct {
	CPUs   string `json:"cpus,omitempty"`
	Memory string `json:"memory,omitempty"`
}

type Healthcheck struct {
	Test        []string `json:"test,omitempty"`
	Interval    string   `json:"interval,omitempty"`
	Timeout     string   `json:"timeout,omitempty"`
	StartPeriod string   `json:"startPeriod,omitempty"`
	Retries     int      `json:"retries,omitempty"`
}

// ServiceConfig is the JSON form of one service's editable configuration.
type ServiceConfig struct {
	Image       string              `json:"image,omitempty"`
	Build       *Build              `json:"build,omitempty"`
	Ports       []string            `json:"ports,omitempty"`
	Volumes     []string            `json:"volumes,omitempty"`
	Environment []EnvVar            `json:"environment,omitempty"`
	Networks    []NetworkAttachment `json:"networks,omitempty"`
	NetworkMode string              `json:"networkMode,omitempty"`
	Limits      Limits              `json:"limits"`
	Restart     string              `json:"restart,omitempty"`
	Healthcheck *Healthcheck        `json:"healthcheck,omitempty"`
}

type ServiceDetail struct {
	Name       string          `json:"name"`
	Config     ServiceConfig   `json:"config"`
	ReadOnly   []string        `json:"readOnly"`
	Containers []ContainerInfo `json:"containers"`
}

// StackDetail flattens the summary but carries the per-service list under
// "services"; ServiceCount mirrors the summary field.
type StackDetail struct {
	Name         string          `json:"name"`
	Path         string          `json:"path"`
	Project      string          `json:"project"`
	Origin       string          `json:"origin"`
	Status       string          `json:"status"`
	Running      int             `json:"running"`
	ServiceCount int             `json:"serviceCount"`
	Hash         string          `json:"hash"`
	Services     []ServiceDetail `json:"services"`
	ParseError   string          `json:"parseError,omitempty"`
	Health       HealthCounts    `json:"health"`
	Ports        []string        `json:"ports"`
}

type Operation struct {
	ID        string     `json:"id"`
	Stack     string     `json:"stack"`
	Action    string     `json:"action"`
	State     string     `json:"state"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	ExitCode  *int       `json:"exitCode,omitempty"`
	Error     string     `json:"error,omitempty"`
}

type OperationDetail struct {
	Operation
	Lines []string `json:"lines"`
}

type Network struct {
	Name    string   `json:"name"`
	Driver  string   `json:"driver"`
	Subnets []string `json:"subnets"`
}

type FSEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

type Status struct {
	Docker string `json:"docker"`
	// DockerVersion is the engine version; empty when it could not be read.
	DockerVersion string `json:"dockerVersion"`
	Compose       string `json:"compose"`
	// Events is "connected" while the container status cache follows Docker
	// events, else "disconnected"; MetricsDegraded is true while the sampler
	// cannot store samples.
	Events          string `json:"events"`
	MetricsDegraded bool   `json:"metricsDegraded"`
	// Version is the server's build version; empty when unknown.
	Version string `json:"version"`
	// Metrics sampling settings, in seconds; 0 when unset.
	MetricsIntervalSeconds  int64 `json:"metricsIntervalSeconds"`
	MetricsRetentionSeconds int64 `json:"metricsRetentionSeconds"`
}

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type ErrorBody struct {
	Error  string       `json:"error"`
	Fields []FieldError `json:"fields,omitempty"`
}

type RawCompose struct {
	Content string `json:"content"`
	Hash    string `json:"hash"`
}

type HashResponse struct {
	Hash string `json:"hash"`
}

// PreviewResponse carries the candidate file and the hash it was computed
// against, which the save must send unchanged.
type PreviewResponse struct {
	Content string `json:"content"`
	Hash    string `json:"hash"`
}

type CreateStackRequest struct {
	Name    string        `json:"name"`
	Service string        `json:"service"`
	Config  ServiceConfig `json:"config"`
}

type ImportRequest struct {
	Path string `json:"path"`
}

type UpdateServiceRequest struct {
	Config ServiceConfig `json:"config"`
	Hash   string        `json:"hash"`
}

type LogEvent struct {
	Service   string    `json:"service"`
	Container string    `json:"container"`
	Stream    string    `json:"stream"`
	Time      time.Time `json:"time"`
	Text      string    `json:"text"`
}

type LogError struct {
	Service   string `json:"service"`
	Container string `json:"container"`
	Error     string `json:"error"`
}

type MetricPoint struct {
	Time     string  `json:"time"`
	Service  string  `json:"service"`
	CPU      float64 `json:"cpu"`
	Mem      uint64  `json:"mem"`
	MemLimit uint64  `json:"memLimit"`
	// Rates are bytes per second, summed across the service's replicas;
	// absent when no replica has a previous bucket to diff against.
	NetRx    *float64 `json:"netRxBytesPerSec,omitempty"`
	NetTx    *float64 `json:"netTxBytesPerSec,omitempty"`
	BlkRead  *float64 `json:"blkReadBytesPerSec,omitempty"`
	BlkWrite *float64 `json:"blkWriteBytesPerSec,omitempty"`
}

type MetricsHistory struct {
	Points []MetricPoint `json:"points"`
}

// Sample is one raw per-container reading with cumulative counters.
type Sample struct {
	Time      string  `json:"time"`
	Service   string  `json:"service"`
	Container string  `json:"container"`
	CPU       float64 `json:"cpu"`
	Mem       uint64  `json:"mem"`
	MemLimit  uint64  `json:"memLimit"`
	NetRx     uint64  `json:"netRx"`
	NetTx     uint64  `json:"netTx"`
	BlkRead   uint64  `json:"blkRead"`
	BlkWrite  uint64  `json:"blkWrite"`
}

type Change struct {
	Project string `json:"project"`
	Seq     uint64 `json:"seq"`
}

type StatusFlags struct {
	Events          string `json:"events"`
	MetricsDegraded bool   `json:"metricsDegraded"`
}

type SetupStatus struct {
	NeedsSetup bool `json:"needsSetup"`
}

type SetupRequest struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	SetupToken string `json:"setupToken"`
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type Me struct {
	Username string `json:"username"`
}

type ChangePasswordRequest struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

type MetricsTokenStatus struct {
	Configured bool `json:"configured"`
}

type MetricsToken struct {
	Token string `json:"token"`
}

type Health struct {
	OK bool `json:"ok"`
}

// Actions are the lifecycle actions an operation can run.
var Actions = []string{"up", "down", "restart", "pull", "build"}

// Operation states.
const (
	OpRunning   = "running"
	OpSucceeded = "succeeded"
	OpFailed    = "failed"
	OpCancelled = "cancelled"
	OpTimedOut  = "timed_out"
)
