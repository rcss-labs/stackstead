package stacks

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
)

// Mount is one volume or bind mount a stack's service declares.
type Mount struct {
	Service string
	// Kind is "volume" or "bind".
	Kind string
	// Source is the docker volume name or the absolute host path.
	Source string
	Target string
}

type resolvedConfig struct {
	Services map[string]struct {
		Image   string          `json:"image"`
		Build   json.RawMessage `json:"build"`
		Volumes []struct {
			Type   string `json:"type"`
			Source string `json:"source"`
			Target string `json:"target"`
		} `json:"volumes"`
	} `json:"services"`
	Volumes map[string]struct {
		Name string `json:"name"`
	} `json:"volumes"`
}

func (s *Service) resolvedConfig(ctx context.Context, stack string) (Entry, resolvedConfig, error) {
	var cfg resolvedConfig
	e, err := s.reg.Get(ctx, stack)
	if err != nil {
		return e, cfg, err
	}
	raw, err := s.compose.ConfigJSON(ctx, e.ComposePath)
	if err != nil {
		return e, cfg, fmt.Errorf("resolve compose config: %w", err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return e, cfg, fmt.Errorf("parse compose config: %w", err)
	}
	return e, cfg, nil
}

// Mounts lists the stack's named-volume and bind mounts, resolved from
// `docker compose config`. Named volumes carry their real docker volume name;
// relative bind sources are resolved against the compose directory. Anonymous
// volumes and tmpfs mounts are not listed.
func (s *Service) Mounts(ctx context.Context, stack string) ([]Mount, error) {
	e, cfg, err := s.resolvedConfig(ctx, stack)
	if err != nil {
		return nil, err
	}
	var out []Mount
	for svc, def := range cfg.Services {
		for _, v := range def.Volumes {
			if v.Source == "" {
				continue
			}
			switch v.Type {
			case "bind":
				src := v.Source
				if !filepath.IsAbs(src) {
					src = filepath.Join(filepath.Dir(e.ComposePath), src)
				}
				out = append(out, Mount{Service: svc, Kind: "bind", Source: filepath.Clean(src), Target: v.Target})
			case "volume":
				name := v.Source
				if d, ok := cfg.Volumes[v.Source]; ok && d.Name != "" {
					name = d.Name
				}
				out = append(out, Mount{Service: svc, Kind: "volume", Source: name, Target: v.Target})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.Target < b.Target
	})
	return out, nil
}

// ImageRefs returns the image IDs of the stack's containers, sorted and unique.
// With no containers it falls back to the image references the compose file
// declares, and buildOnly names the services that only build an image.
func (s *Service) ImageRefs(ctx context.Context, stack string) (refs, buildOnly []string, err error) {
	e, cfg, err := s.resolvedConfig(ctx, stack)
	if err != nil {
		return nil, nil, err
	}
	containers, err := s.engine.Containers(ctx)
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	for _, c := range containers {
		if c.Project() == e.Project && c.ImageID != "" {
			seen[c.ImageID] = true
		}
	}
	if len(seen) == 0 {
		for name, def := range cfg.Services {
			switch {
			case def.Image != "":
				seen[def.Image] = true
			case len(def.Build) > 0 && string(def.Build) != "null":
				buildOnly = append(buildOnly, name)
			}
		}
		sort.Strings(buildOnly)
	}
	out := make([]string, 0, len(seen))
	for ref := range seen {
		out = append(out, ref)
	}
	sort.Strings(out)
	return out, buildOnly, nil
}
