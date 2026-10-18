// Package storage gathers storage inventory and stack declarations.
package storage

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/stacks"
)

// DeclaredImage identifies one service's image, including locally built images.
type DeclaredImage struct {
	Service, Reference string
	Built              bool
}

// DeclaredVolume records a Compose volume's resolved Docker name.
type DeclaredVolume struct {
	Key, Name string
	External  bool
}

// StackDeclarations contains only safe inventory fields. State is caller-owned:
// the service assigns the current stack state after reading these declarations.
type StackDeclarations struct {
	Name, Project, State string
	Known                bool
	Images               []DeclaredImage
	Volumes              []DeclaredVolume
}

// ComposeConfigRunner resolves the single Compose file registered for a stack.
type ComposeConfigRunner interface {
	ConfigJSON(context.Context, string) ([]byte, error)
}

// buildPresence discards the build configuration while preserving its presence.
type buildPresence bool

func (b *buildPresence) UnmarshalJSON(data []byte) error {
	*b = string(data) != "null"
	return nil
}

type declarationValue struct {
	project string
	images  []DeclaredImage
	volumes []DeclaredVolume
}
type declarationFlight struct {
	hash  [32]byte
	done  chan struct{}
	value declarationValue
	known bool
}
type declarationSlot struct {
	hash   [32]byte
	value  declarationValue
	known  bool
	flight *declarationFlight
}

// DeclarationCache caches successful, sanitized declarations by file content.
// No full Compose configuration or live stack state is retained.
type DeclarationCache struct {
	runner ComposeConfigRunner
	log    *slog.Logger
	slots  chan struct{}
	mu     sync.Mutex
	stacks map[string]*declarationSlot
}

func NewDeclarationCache(runner ComposeConfigRunner, logger *slog.Logger) *DeclarationCache {
	if logger == nil {
		logger = slog.Default()
	}
	return &DeclarationCache{runner: runner, log: logger, slots: make(chan struct{}, 2), stacks: make(map[string]*declarationSlot)}
}

// Get returns empty lists and Known=false on failure. Failed reads are retried on
// the next request; concurrent requests for one stack share a single run.
func (c *DeclarationCache) Get(ctx context.Context, e stacks.Entry) StackDeclarations {
	for {
		if ctx.Err() != nil {
			return declarationResult(e, declarationValue{}, false)
		}
		hash, err := declarationHash(e.ComposePath)
		if err != nil {
			c.failure(e.Name, err)
			return declarationResult(e, declarationValue{}, false)
		}
		c.mu.Lock()
		slot := c.stacks[e.Name]
		if slot == nil {
			slot = &declarationSlot{}
			c.stacks[e.Name] = slot
		}
		if slot.flight != nil {
			flight := slot.flight
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return declarationResult(e, declarationValue{}, false)
			case <-flight.done:
			}
			if flight.hash == hash {
				return declarationResult(e, flight.value, flight.known)
			}
			continue
		}
		if slot.known && slot.hash == hash {
			value := slot.value
			c.mu.Unlock()
			return declarationResult(e, value, true)
		}
		flight := &declarationFlight{hash: hash, done: make(chan struct{})}
		slot.flight = flight
		c.mu.Unlock()
		value, err := c.load(ctx, e.ComposePath)
		if err != nil {
			c.failure(e.Name, err)
		}
		c.mu.Lock()
		flight.value, flight.known = value, err == nil
		// Discard old declarations on failure: a later request must retry even if
		// the file was changed back to its previously successful content.
		slot.hash, slot.value, slot.known = hash, value, err == nil
		slot.flight = nil
		close(flight.done)
		c.mu.Unlock()
		return declarationResult(e, value, err == nil)
	}
}

func declarationResult(e stacks.Entry, value declarationValue, known bool) StackDeclarations {
	out := StackDeclarations{Name: e.Name, Project: e.Project, Known: known, Images: []DeclaredImage{}, Volumes: []DeclaredVolume{}}
	if known {
		out.Project = value.project
		out.Images = append(out.Images, value.images...)
		out.Volumes = append(out.Volumes, value.volumes...)
	}
	return out
}

func (c *DeclarationCache) load(ctx context.Context, path string) (declarationValue, error) {
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		return declarationValue{}, ctx.Err()
	}
	defer func() { <-c.slots }()
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	data, err := c.runner.ConfigJSON(runCtx, path)
	if err != nil {
		return declarationValue{}, err
	}
	// Decode only fields needed for inventory. Unknown fields (including all
	// environment and driver options) never enter the cached representation.
	var doc struct {
		Name     string `json:"name"`
		Services map[string]struct {
			Image string        `json:"image"`
			Build buildPresence `json:"build"`
		} `json:"services"`
		Volumes map[string]struct {
			Name     string `json:"name"`
			External bool   `json:"external"`
		} `json:"volumes"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return declarationValue{}, err
	}
	if doc.Name == "" {
		return declarationValue{}, errors.New("compose project missing")
	}
	out := declarationValue{project: doc.Name}
	for name, service := range doc.Services {
		built := bool(service.Build)
		ref := service.Image
		if ref == "" && built {
			ref = doc.Name + "-" + name
		}
		if ref != "" {
			out.images = append(out.images, DeclaredImage{Service: name, Reference: ref, Built: built})
		}
	}
	for key, volume := range doc.Volumes {
		name := volume.Name
		if name == "" {
			name = doc.Name + "_" + key
		}
		out.volumes = append(out.volumes, DeclaredVolume{Key: key, Name: name, External: volume.External})
	}
	sort.Slice(out.images, func(i, j int) bool { return out.images[i].Service < out.images[j].Service })
	sort.Slice(out.volumes, func(i, j int) bool { return out.volumes[i].Key < out.volumes[j].Key })
	return out, nil
}

func (c *DeclarationCache) failure(stack string, err error) {
	code := -1
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	}
	c.log.Debug("stack declarations unavailable", "stack", stack, "exit_code", code)
}

func declarationHash(path string) ([32]byte, error) {
	h := sha256.New()
	// Include path so replacing a stack registration with an identical file in
	// another directory cannot reuse differently resolved Compose declarations.
	for _, p := range []string{path, filepath.Join(filepath.Dir(path), ".env")} {
		data, err := os.ReadFile(p)
		if err != nil && !(p != path && errors.Is(err, os.ErrNotExist)) {
			return [32]byte{}, err
		}
		for _, part := range [][]byte{[]byte(p), data} {
			var size [8]byte
			binary.BigEndian.PutUint64(size[:], uint64(len(part)))
			h.Write(size[:])
			h.Write(part)
		}
		if err == nil {
			h.Write([]byte{1})
		} else {
			h.Write([]byte{0})
		}
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}
