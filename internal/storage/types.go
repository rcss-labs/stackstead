package storage

import (
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
)

// DFReading is one kind's cached usage reading. LoadedAt and Snapshot.Kinds
// jointly distinguish a successful empty reading from a kind not yet loaded.
type DFReading struct {
	Snapshot   engine.DFSnapshot
	LoadedAt   time.Time
	Refreshing bool
	Error      string
}

type Inputs struct {
	Images             []engine.Image
	Volumes            []engine.Volume
	Containers         []engine.Container
	DF                 map[engine.DFKind]DFReading
	Stacks             []StackDeclarations
	RegisteredProjects map[string]string // Compose project -> registered stack name
}

type Consumer struct{ ContainerID, Name, State, Project, Service, Stack string }
type Declaration struct{ Stack, Service, Ref, StackState string }
type Protection struct{ Reason, Stack string }

type Image struct {
	ID               string
	Tags, Digests    []string
	Created          time.Time
	Size, SharedSize *int64
	Containers       []Consumer
	DeclaredBy       []Declaration
	Dangling         bool
	Status           string
}

type Volume struct {
	Name, Driver, Scope, Project string
	CreatedAt                    time.Time
	Labels                       map[string]string
	Size, RefCount               *int64
	SizesPending                 bool
	Containers                   []Consumer
	DeclaredBy                   []Declaration
	Protection                   *Protection
	Status                       string
}

type BuildRecord = engine.DFBuildCacheRecord

type ContainerLayer struct {
	Consumer
	SizeRw *int64
}

type KindTotals struct {
	Count             int
	Size, Reclaimable *int64
	SizesAt           *time.Time
	Refreshing        bool
	Error             string
}

type Inventory struct {
	Images              []Image
	Volumes             []Volume
	BuildCache          []BuildRecord
	Containers          []ContainerLayer
	TopContainers       []ContainerLayer
	Totals              map[engine.DFKind]KindTotals
	DeclarationsUnknown []string
}
