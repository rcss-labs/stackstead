// Package alerts derives dashboard alerts on request from state the server
// already holds. Nothing is persisted; see Engine for how "since" is kept.
package alerts

import (
	"context"
	"time"

	"github.com/rcss-labs/stackstead/internal/backup"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// Alert kinds.
const (
	KindExited             = "exited"
	KindUnhealthy          = "unhealthy"
	KindRestartLoop        = "restart_loop"
	KindPortClash          = "port_clash"
	KindOverride           = "override"
	KindParseError         = "parse_error"
	KindDockerDisconnected = "docker_disconnected"
	KindMetricsDegraded    = "metrics_degraded"
	KindBackupFailed       = "backup_failed"
	KindBackupPartial      = "backup_partial"
)

// Severities, most urgent first.
const (
	SeverityDanger  = "danger"
	SeverityWarning = "warning"
	SeverityInfo    = "info"
)

// Alert is one active problem. Fields that do not apply stay empty.
type Alert struct {
	Kind, Severity, Stack, Service, Container, Message string
	Since                                              time.Time
}

// StackDetails lists every registered stack; satisfied by *stacks.Service.
type StackDetails interface {
	Details(ctx context.Context) ([]stacks.Detail, error)
}

// ContainerSource lists containers; satisfied by state.Cache.
type ContainerSource interface {
	Containers(ctx context.Context) ([]engine.Container, error)
}

// RestartCounts reports restarts in the last 24 h by container id; nil before
// the collector's first refresh.
type RestartCounts interface{ RestartCounts() map[string]int }

// Connectivity reports whether Docker is reachable; satisfied by state.Cache.
type Connectivity interface{ Connected() bool }

// MetricsHealth reports whether metric writes are failing; satisfied by metrics.Sampler.
type MetricsHealth interface{ Degraded() bool }

// BackupHealth reports the most recent finished backup run; satisfied by *backup.Runner.
type BackupHealth interface {
	LastRun(ctx context.Context) (backup.Run, bool, error)
}
