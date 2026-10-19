// Package apitypes holds the JSON wire types of the Stackstead HTTP API. The
// server (internal/api) aliases its DTOs to these and the CLI client decodes
// into them, so both sides share one definition. It imports only the standard
// library and must stay free of server packages.
package apitypes

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

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
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	Driver     string             `json:"driver"`
	Scope      string             `json:"scope"`
	Subnets    []string           `json:"subnets"`
	Gateways   []string           `json:"gateways"`
	IPRange    string             `json:"ipRange"`
	Parent     string             `json:"parent"`
	IPvlanMode string             `json:"ipvlanMode"`
	Internal   bool               `json:"internal"`
	Builtin    bool               `json:"builtin"`
	Project    string             `json:"project"`
	Stack      string             `json:"stack"`
	Containers []NetworkContainer `json:"containers"`
}

type NetworkContainer struct {
	Name string `json:"name"`
	IPv4 string `json:"ipv4"`
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

type CreateStackService struct {
	Service string        `json:"service"`
	Config  ServiceConfig `json:"config"`
}

type CreateStackRequest struct {
	Name            string               `json:"name"`
	Service         string               `json:"service,omitempty"`
	Config          *ServiceConfig       `json:"config,omitempty"`
	Services        []CreateStackService `json:"services,omitempty"`
	servicesPresent bool
	legacyPresent   bool
}

// UnmarshalJSON preserves field presence: null and empty legacy fields still
// conflict with the services form. Decode strictly here because an outer
// decoder's DisallowUnknownFields does not reach custom unmarshallers.
func (r *CreateStackRequest) UnmarshalJSON(data []byte) error {
	type wire CreateStackRequest
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for key := range fields {
		switch {
		case strings.EqualFold(key, "services"):
			decoded.servicesPresent = true
		case strings.EqualFold(key, "service"), strings.EqualFold(key, "config"):
			decoded.legacyPresent = true
		}
	}
	*r = CreateStackRequest(decoded)
	return nil
}

func (r CreateStackRequest) UsesServices() bool  { return r.servicesPresent }
func (r CreateStackRequest) HasMixedForms() bool { return r.servicesPresent && r.legacyPresent }

type AddServiceRequest struct {
	Service string        `json:"service"`
	Config  ServiceConfig `json:"config"`
	Hash    string        `json:"hash"`
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

// HostSnapshot is the GET /api/host body. Every section except the first four
// fields is a pointer without omitempty so an unavailable source is an
// explicit JSON null; rate fields are null until two readings exist.
type HostSnapshot struct {
	Hostname    string          `json:"hostname"`
	OS          string          `json:"os"`
	Source      string          `json:"source"` // "proc" or "docker"
	SampledAt   time.Time       `json:"sampledAt"`
	Containers  *HostContainers `json:"containers"`
	CPU         *HostCPU        `json:"cpu"`
	Memory      *HostMemory     `json:"memory"`
	Disk        *HostDisk       `json:"disk"`
	Network     *HostNetwork    `json:"network"`
	DiskIO      *HostDiskIO     `json:"diskIO"`
	Volumes     *HostVolumes    `json:"volumes"`
	Uptime      *HostUptime     `json:"uptime"`
	Restarts24h *HostRestarts   `json:"restarts24h"`
}

type HostContainers struct {
	Total   int `json:"total"`
	Running int `json:"running"`
	Exited  int `json:"exited"`
	Bad     int `json:"bad"`
}

type HostCPU struct {
	Percent *float64 `json:"percent"`
	Cores   int      `json:"cores"`
}

type HostMemory struct {
	Used      uint64 `json:"used"`
	Total     uint64 `json:"total"`
	SwapUsed  uint64 `json:"swapUsed"`
	SwapTotal uint64 `json:"swapTotal"`
}

type HostDisk struct {
	Path  string `json:"path"`
	Free  uint64 `json:"free"`
	Total uint64 `json:"total"`
}

type HostNetwork struct {
	RxBytesPerSec *float64 `json:"rxBytesPerSec"`
	TxBytesPerSec *float64 `json:"txBytesPerSec"`
	Interface     string   `json:"interface"`
}

type HostDiskIO struct {
	ReadBytesPerSec  *float64 `json:"readBytesPerSec"`
	WriteBytesPerSec *float64 `json:"writeBytesPerSec"`
}

type HostVolumes struct {
	Count    int    `json:"count"`
	Size     uint64 `json:"size"`
	Dangling int    `json:"dangling"`
}

type HostUptime struct {
	Seconds float64 `json:"seconds"`
	Load1   float64 `json:"load1"`
	Load5   float64 `json:"load5"`
	Load15  float64 `json:"load15"`
}

type HostRestarts struct {
	Total int              `json:"total"`
	Top   []HostRestartTop `json:"top"`
}

type HostRestartTop struct {
	Container string `json:"container"`
	Service   string `json:"service"`
	Project   string `json:"project"`
	Count     int    `json:"count"`
}

// Alert is one active problem from GET /api/alerts; stack, service and
// container are omitted when they do not apply.
type Alert struct {
	Kind      string    `json:"kind"`
	Severity  string    `json:"severity"` // danger, warning or info
	Stack     string    `json:"stack,omitempty"`
	Service   string    `json:"service,omitempty"`
	Container string    `json:"container,omitempty"`
	Message   string    `json:"message"`
	Since     time.Time `json:"since"`
}

// MetricsSummary is the GET /api/metrics/summary body. Step is in seconds;
// Stacks is keyed by compose project.
type MetricsSummary struct {
	Window string                    `json:"window"`
	Step   int                       `json:"step"`
	From   time.Time                 `json:"from"`
	To     time.Time                 `json:"to"`
	Stacks map[string]ProjectSummary `json:"stacks"`
	Top    SummaryTop                `json:"top"`
}

// ProjectSummary holds one project's sparkline data; null entries are empty
// buckets and Mem is in bytes.
type ProjectSummary struct {
	CPU              []*float64 `json:"cpu"`
	Mem              []*float64 `json:"mem"`
	CPUNow           *float64   `json:"cpuNow"`
	MemNow           *float64   `json:"memNow"`
	NetRxBytesPerSec *float64   `json:"netRxBytesPerSec"`
	NetTxBytesPerSec *float64   `json:"netTxBytesPerSec"`
}

type SummaryTop struct {
	CPU []TopEntry `json:"cpu"`
	Mem []TopEntry `json:"mem"`
}

type TopEntry struct {
	Container string  `json:"container"`
	Service   string  `json:"service"`
	Project   string  `json:"project"`
	Value     float64 `json:"value"`
}

// EventMessage is the payload of an "event" message on GET /api/events: one
// Docker container event. Project and Service are empty for containers that
// are not part of a compose project. ExitCode is set only for "die" and Health
// only for "health_status".
type EventMessage struct {
	Action    string    `json:"action"`
	Container string    `json:"container"`
	Project   string    `json:"project"`
	Service   string    `json:"service"`
	Image     string    `json:"image"`
	Time      time.Time `json:"time"`
	ExitCode  *int      `json:"exitCode,omitempty"`
	Health    string    `json:"health,omitempty"`
}

// ContainerRow is one row of GET /api/containers.
type ContainerRow struct {
	ID          string            `json:"id"` // first 12 hex
	Name        string            `json:"name"`
	Image       string            `json:"image"`
	ImageID     string            `json:"imageId"`
	State       string            `json:"state"`
	Status      string            `json:"status"`
	Health      string            `json:"health"`
	ExitCode    *int              `json:"exitCode"` // null unless exited with a parsed code
	CreatedAt   time.Time         `json:"createdAt"`
	Project     string            `json:"project"`
	Service     string            `json:"service"`
	Stack       string            `json:"stack"`
	Managed     string            `json:"managed"`  // "stack" | "compose" | "none"
	Ports       []string          `json:"ports"`    // published only, never null
	Networks    map[string]string `json:"networks"` // never null
	CPU         *float64          `json:"cpu"`      // null without a sampler reading
	Mem         *uint64           `json:"mem"`
	MemLimit    *uint64           `json:"memLimit"`
	Restarts24h int               `json:"restarts24h"`
}

// ContainerMount is one mount in ContainerDetail.
type ContainerMount struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	ReadOnly    bool   `json:"readOnly"`
}

// ContainerDetail is GET /api/containers/{id}. It is deliberately without
// env, command, entrypoint, user and host config: they can hold secrets.
type ContainerDetail struct {
	ID            string            `json:"id"` // full
	Name          string            `json:"name"`
	Image         string            `json:"image"`
	ImageID       string            `json:"imageId"`
	State         string            `json:"state"`
	Status        string            `json:"status"`
	Health        string            `json:"health"`
	StartedAt     *time.Time        `json:"startedAt"`  // null when Docker reports none
	FinishedAt    *time.Time        `json:"finishedAt"` // null when Docker reports none
	ExitCode      *int              `json:"exitCode"`   // null while running
	RestartPolicy string            `json:"restartPolicy"`
	MemoryLimit   *uint64           `json:"memoryLimit"` // HostConfig.Memory in bytes; null when unlimited
	Restarts24h   int               `json:"restarts24h"`
	Project       string            `json:"project"`
	Service       string            `json:"service"`
	Stack         string            `json:"stack"`
	Managed       string            `json:"managed"`
	Ports         []string          `json:"ports"`    // never null
	Networks      map[string]string `json:"networks"` // never null
	Mounts        []ContainerMount  `json:"mounts"`   // never null
	Labels        map[string]string `json:"labels"`   // never null
}

// NetworkCreateRequest configures a user-created Docker network.
type NetworkCreateRequest struct {
	Name       string `json:"name"`
	Driver     string `json:"driver"`
	Subnet     string `json:"subnet"`
	Gateway    string `json:"gateway"`
	IPRange    string `json:"ipRange"`
	Parent     string `json:"parent"`
	IPvlanMode string `json:"ipvlanMode"`
	Internal   bool   `json:"internal"`
}

// UpdateStatus is the update-notice state: GET /api/settings/update, the
// checker's Status(), and `stackstead upgrade --check -o json`.
type UpdateStatus struct {
	Current   string    `json:"current"`
	Installer string    `json:"installer"`
	Command   string    `json:"command"`
	State     string    `json:"state"`
	Latest    string    `json:"latest"`
	NotesURL  string    `json:"notesURL"`
	CheckedAt time.Time `json:"checkedAt,omitzero"`
	Error     string    `json:"error"`
}

// Storage response DTOs deliberately expose only safe inventory metadata.
type StorageConsumer struct {
	ContainerID string `json:"containerId"`
	Name        string `json:"name"`
	State       string `json:"state"`
	Project     string `json:"project"`
	Service     string `json:"service"`
	Stack       string `json:"stack"`
}

type StorageDeclaration struct {
	Stack      string `json:"stack"`
	Service    string `json:"service"`
	Ref        string `json:"ref"`
	StackState string `json:"stackState"`
}

type StorageProtection struct {
	Reason string `json:"reason"`
	Stack  string `json:"stack"`
}

type StorageImage struct {
	ID         string               `json:"id"`
	Tags       []string             `json:"tags"`
	Digests    []string             `json:"digests"`
	Created    time.Time            `json:"created"`
	Size       *int64               `json:"size"`
	SharedSize *int64               `json:"sharedSize"`
	Containers []StorageConsumer    `json:"containers"`
	DeclaredBy []StorageDeclaration `json:"declaredBy"`
	Dangling   bool                 `json:"dangling"`
	Status     string               `json:"status"`
}

type StorageVolume struct {
	Name         string               `json:"name"`
	Driver       string               `json:"driver"`
	Scope        string               `json:"scope"`
	Project      string               `json:"project"`
	CreatedAt    time.Time            `json:"createdAt"`
	Labels       map[string]string    `json:"labels"`
	Size         *int64               `json:"size"`
	RefCount     *int64               `json:"refCount"`
	SizesPending bool                 `json:"sizesPending"`
	Containers   []StorageConsumer    `json:"containers"`
	DeclaredBy   []StorageDeclaration `json:"declaredBy"`
	Protection   *StorageProtection   `json:"protection"`
	Status       string               `json:"status"`
}

type StorageBuildRecord struct {
	ID          string     `json:"id"`
	Type        string     `json:"type"`
	Description string     `json:"description"`
	Size        *int64     `json:"size"`
	InUse       bool       `json:"inUse"`
	Shared      bool       `json:"shared"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastUsedAt  *time.Time `json:"lastUsedAt"`
	UsageCount  int64      `json:"usageCount"`
}

type StorageKindTotals struct {
	Count       int        `json:"count"`
	Size        *int64     `json:"size"`
	Reclaimable *int64     `json:"reclaimable"`
	SizesAt     *time.Time `json:"sizesAt"`
	Refreshing  bool       `json:"refreshing"`
	Error       string     `json:"error"`
}

type StorageTopContainer struct {
	Name   string `json:"name"`
	Stack  string `json:"stack"`
	SizeRw *int64 `json:"sizeRw"`
}

type StorageOverview struct {
	Images              StorageKindTotals     `json:"images"`
	Containers          StorageKindTotals     `json:"containers"`
	Volumes             StorageKindTotals     `json:"volumes"`
	BuildCache          StorageKindTotals     `json:"buildCache"`
	TopContainers       []StorageTopContainer `json:"topContainers"`
	DeclarationsUnknown []string              `json:"declarationsUnknown"`
}

type StorageImages struct {
	Images []StorageImage    `json:"images"`
	Totals StorageKindTotals `json:"totals"`
}

type StorageVolumes struct {
	Volumes []StorageVolume   `json:"volumes"`
	Totals  StorageKindTotals `json:"totals"`
}

type StorageBuildCache struct {
	Records []StorageBuildRecord `json:"records"`
	Totals  StorageKindTotals    `json:"totals"`
}

type StoragePlanRequest struct {
	Kind      string `json:"kind"`
	Selection string `json:"selection,omitempty"`
	Ref       string `json:"ref,omitempty"`
	Name      string `json:"name,omitempty"`
}

type StoragePlanItem struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Ref   string `json:"ref"`
	Bytes *int64 `json:"bytes"`
}

type StorageProtectedItem struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	Ref        string `json:"ref"`
	Reason     string `json:"reason"`
	Stack      string `json:"stack"`
	StackState string `json:"stackState"`
}

type StorageReclaim struct {
	Min   *int64 `json:"min"`
	Max   *int64 `json:"max"`
	Exact bool   `json:"exact"`
}

type StoragePlan struct {
	ID            string                 `json:"id"`
	Kind          string                 `json:"kind"`
	Mode          string                 `json:"mode,omitempty"`
	CreatedAt     time.Time              `json:"createdAt"`
	ExpiresAt     time.Time              `json:"expiresAt"`
	Items         []StoragePlanItem      `json:"items"`
	Protected     []StorageProtectedItem `json:"protected"`
	Warnings      []string               `json:"warnings"`
	Reclaim       StorageReclaim         `json:"reclaim"`
	ConfirmPhrase string                 `json:"confirmPhrase"`
}

type StorageConfirmRequest struct {
	Confirm string `json:"confirm"`
}

type StorageRefresh struct {
	StartedAt time.Time `json:"startedAt"`
}

type StorageRateLimit struct {
	Error      string `json:"error"`
	RetryAfter int    `json:"retryAfter"`
}

type ActionEntry struct {
	ID          int64     `json:"id"`
	TS          time.Time `json:"ts"`
	Username    string    `json:"username"`
	Kind        string    `json:"kind"`
	Scope       string    `json:"scope"`
	Target      string    `json:"target"`
	Detail      string    `json:"detail"`
	Bytes       *int64    `json:"bytes"`
	Result      string    `json:"result"`
	OperationID string    `json:"operationId"`
}

type StorageImageDetail struct {
	StorageImage
	Architecture string            `json:"architecture"`
	OS           string            `json:"os"`
	Layers       int               `json:"layers"`
	Labels       map[string]string `json:"labels"`
}

type StorageVolumeDetail struct {
	StorageVolume
	Mountpoint string `json:"mountpoint"`
}
