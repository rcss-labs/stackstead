package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/engine"
)

// NetworkEngineAPI is the Docker network management surface.
type NetworkEngineAPI interface {
	Networks(context.Context) ([]engine.Network, error)
	InspectNetwork(context.Context, string) (engine.Network, error)
	CreateNetwork(context.Context, engine.NetworkSpec) (string, error)
	RemoveNetwork(context.Context, string) error
}

const (
	networkListTTL            = 2 * time.Second
	networkListTimeout        = 10 * time.Second
	networkInspectConcurrency = 4
)

func (s *server) registerNetworkRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/networks", s.requireSession(s.handleNetworkList))
}

func (s *server) handleNetworkList(w http.ResponseWriter, r *http.Request) {
	body, err := s.networkCache.get(r.Context(), s.opts.Now(), networkListTTL, networkListTimeout, s.computeNetworkList)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	if r.Context().Err() == nil {
		writeJSON(w, http.StatusOK, body)
	}
}

func (s *server) computeNetworkList(ctx context.Context) ([]apitypes.Network, error) {
	networks, err := s.opts.NetworkEngine.Networks(ctx)
	if err != nil {
		return nil, err
	}
	projects, err := s.projectStacks(ctx)
	if err != nil {
		// This is a registry failure, independently of any Docker error it contains.
		return nil, fmt.Errorf("list registered stacks: %v", err)
	}
	// Each worker owns its input index; aggregation happens after all finish.
	rows := make([]apitypes.Network, len(networks))
	keep := make([]bool, len(networks))
	errs := make([]error, len(networks))
	sem := make(chan struct{}, networkInspectConcurrency)
	var wg sync.WaitGroup
	for i, n := range networks {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return nil, ctx.Err()
		}
		wg.Go(func() {
			defer func() { <-sem }()
			// Worker panics must reach the cache as errors instead of crashing the server.
			defer func() {
				if r := recover(); r != nil {
					errs[i] = fmt.Errorf("network inspect panicked: %v", r)
				}
			}()
			inspected, err := s.opts.NetworkEngine.InspectNetwork(ctx, n.ID)
			if errors.Is(err, engine.ErrNetworkNotFound) {
				return
			}
			if err != nil {
				errs[i] = err
				return
			}
			n.Containers = inspected.Containers
			rows[i] = networkRow(n, projects)
			keep[i] = true
		})
	}
	wg.Wait()
	out := make([]apitypes.Network, 0, len(networks))
	for i := range rows {
		if errs[i] != nil {
			return nil, errs[i]
		}
		if keep[i] {
			out = append(out, rows[i])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func isBuiltinNetwork(n engine.Network) bool {
	return n.Name == "bridge" && n.Driver == "bridge" || n.Name == "host" && n.Driver == "host" || n.Name == "none" && n.Driver == "null"
}

func networkRow(n engine.Network, projects map[string]string) apitypes.Network {
	row := apitypes.Network{ID: shortID(n.ID), Name: n.Name, Driver: n.Driver, Scope: n.Scope,
		Subnets: make([]string, 0, len(n.Subnets)), Gateways: make([]string, 0, len(n.Subnets)),
		Parent: n.Parent(), IPvlanMode: n.IPvlanMode(), Internal: n.Internal, Builtin: isBuiltinNetwork(n), Project: n.Project(), Stack: projects[n.Project()], Containers: make([]apitypes.NetworkContainer, 0, len(n.Containers))}
	for _, subnet := range n.Subnets {
		row.Subnets = append(row.Subnets, subnet.Prefix.String())
		gateway := ""
		if subnet.Gateway.IsValid() {
			gateway = subnet.Gateway.String()
		}
		row.Gateways = append(row.Gateways, gateway)
	}
	if len(n.Subnets) > 0 && n.Subnets[0].IPRange.IsValid() {
		row.IPRange = n.Subnets[0].IPRange.String()
	}
	for _, c := range n.Containers {
		row.Containers = append(row.Containers, apitypes.NetworkContainer{Name: c.Name, IPv4: c.IPv4})
	}
	sort.Slice(row.Containers, func(i, j int) bool { return row.Containers[i].Name < row.Containers[j].Name })
	return row
}
