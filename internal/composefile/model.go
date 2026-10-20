package composefile

// Field names a group of service settings the forms edit.
type Field string

const (
	FieldImage       Field = "image"
	FieldBuild       Field = "build"
	FieldPorts       Field = "ports"
	FieldVolumes     Field = "volumes"
	FieldEnvironment Field = "environment"
	FieldNetworks    Field = "networks"
	FieldNetworkMode Field = "network_mode"
	FieldLimits      Field = "limits"
	FieldRestart     Field = "restart"
	FieldHealthcheck Field = "healthcheck"
)

// NamedService pairs a service name with the configuration to render.
type NamedService struct {
	Name   string
	Config ServiceConfig
	Extra  Extra // compose keys the form does not model, merged under the form's values
}

// ServiceConfig is the subset of a compose service that Stackstead edits
// through forms. Empty values mean "not set".
type ServiceConfig struct {
	Image       string
	Build       *Build
	Ports       []string // short syntax, e.g. "8080:80"
	Volumes     []string // short syntax, e.g. "data:/var/lib/data"
	Environment []EnvVar
	Networks    []NetworkAttachment
	NetworkMode string
	Limits      Limits // deploy.resources.limits
	Restart     string
	Healthcheck *Healthcheck
}

type Build struct {
	Context    string
	Dockerfile string
}

type EnvVar struct {
	Name  string
	Value string
}

type NetworkAttachment struct {
	Name        string
	IPv4Address string
}

type Limits struct {
	CPUs   string // e.g. "0.5"
	Memory string // e.g. "512M"
}

type Healthcheck struct {
	Test        []string // exec form; a string test reads as ["CMD-SHELL", test]
	Interval    string
	Timeout     string
	StartPeriod string
	Retries     int
}

// Service is one service as read from a file.
type Service struct {
	Name   string
	Config ServiceConfig
	// ReadOnly lists fields present in a form the editor cannot represent
	// (long syntax, unknown sub-keys, YAML anchors/aliases, or inherited
	// through a merge key). They keep their zero value in Config and can
	// only be changed as raw YAML.
	ReadOnly []Field
}
