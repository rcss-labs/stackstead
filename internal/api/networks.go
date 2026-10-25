package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
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
	ConnectNetwork(ctx context.Context, networkID, container string) error
	DisconnectNetwork(ctx context.Context, networkID, container string) error
	PruneNetworks(context.Context) ([]string, error)
}

const (
	networkListTTL            = 2 * time.Second
	networkListTimeout        = 10 * time.Second
	networkInspectConcurrency = 4
)

func (s *server) registerNetworkRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/networks", s.requireSession(s.handleNetworkList))
	mux.Handle("POST /api/networks", s.requireSession(s.handleNetworkCreate))
	mux.Handle("DELETE /api/networks/{id}", s.requireSession(s.handleNetworkDelete))
	mux.Handle("POST /api/networks/prune", s.requireSession(s.handleNetworkPrune))
	mux.Handle("POST /api/networks/{id}/connect", s.requireSession(s.handleNetworkConnect))
	mux.Handle("POST /api/networks/{id}/disconnect", s.requireSession(s.handleNetworkDisconnect))
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
	containers := s.containersByShortID(ctx)
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
			rows[i] = networkRow(n, projects, containers)
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

// containersByShortID indexes the cached container list for joining onto network endpoints.
// A failure only costs the joined fields, never the network list.
func (s *server) containersByShortID(ctx context.Context) map[string]apitypes.ContainerRow {
	if s.opts.ContainerEngine == nil && s.opts.Observe == nil {
		return nil
	}
	list, err := s.containerList(ctx)
	if err != nil {
		s.opts.Logger.Warn("network list: container join skipped", "err", err)
		return nil
	}
	index := make(map[string]apitypes.ContainerRow, len(list))
	for _, c := range list {
		index[c.ID] = c
	}
	return index
}

func isBuiltinNetwork(n engine.Network) bool {
	return n.Name == "bridge" && n.Driver == "bridge" || n.Name == "host" && n.Driver == "host" || n.Name == "none" && n.Driver == "null"
}

func networkRow(n engine.Network, projects map[string]string, containers map[string]apitypes.ContainerRow) apitypes.Network {
	row := apitypes.Network{ID: shortID(n.ID), Name: n.Name, Driver: n.Driver, Scope: n.Scope,
		Subnets: make([]string, 0, len(n.Subnets)), Gateways: make([]string, 0, len(n.Subnets)),
		Parent: n.Parent(), IPvlanMode: n.IPvlanMode(), Internal: n.Internal, Builtin: isBuiltinNetwork(n), Project: n.Project(), Stack: projects[n.Project()], Containers: make([]apitypes.NetworkContainer, 0, len(n.Containers)),
		FullID: n.ID, Attachable: n.Attachable, IPv6: n.IPv6}
	if !n.Created.IsZero() {
		row.Created = n.Created.UTC().Format(time.RFC3339)
	}
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
		nc := apitypes.NetworkContainer{Name: c.Name, IPv4: c.IPv4, ID: shortID(c.ID), MAC: c.MAC}
		if info, ok := containers[nc.ID]; ok {
			nc.Stack, nc.State, nc.Health, nc.ExitCode = info.Stack, info.State, info.Health, info.ExitCode
			if nc.Stack == "" {
				nc.Stack = info.Project
			}
		}
		row.Containers = append(row.Containers, nc)
	}
	sort.Slice(row.Containers, func(i, j int) bool { return row.Containers[i].Name < row.Containers[j].Name })
	return row
}

func (s *server) invalidateNetworkLists() {
	s.networkCache.invalidate()
	s.invalidateContainerList()
}

func (s *server) handleNetworkCreate(w http.ResponseWriter, r *http.Request) {
	var req apitypes.NetworkCreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	existing, err := s.opts.NetworkEngine.Networks(ctx)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	if fields := validateNetworkCreate(req, existing); len(fields) > 0 {
		writeJSON(w, http.StatusBadRequest, fieldErrorBody{Error: "invalid network configuration", Fields: fields})
		return
	}
	for _, n := range existing {
		if n.Name == req.Name {
			writeError(w, http.StatusConflict, fmt.Sprintf("network %s already exists", req.Name))
			return
		}
	}
	id, err := s.opts.NetworkEngine.CreateNetwork(ctx, toNetworkSpec(req))
	if err != nil {
		var action *engine.ActionError
		if errors.As(err, &action) {
			status := http.StatusBadRequest
			if action.Status == http.StatusConflict {
				status = http.StatusConflict
			}
			writeError(w, status, action.Message)
		} else {
			s.writeEngineError(w, r, err)
		}
		return
	}
	s.invalidateNetworkLists()
	s.opts.Logger.Info("network created", "name", req.Name, "driver", req.Driver, "parent", req.Parent)
	n, err := s.opts.NetworkEngine.InspectNetwork(ctx, id)
	if err != nil {
		s.writeNetworkError(w, r, err)
		return
	}
	projects, err := s.projectStacks(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, networkRow(n, projects, nil))
}

var networkIDPrefixRe = regexp.MustCompile(`^[a-fA-F0-9]{12,64}$`)

// resolveNetwork always reads Docker freshly; ambiguous prefixes are not addressable.
func (s *server) resolveNetwork(ctx context.Context, ref string) (engine.Network, bool, error) {
	networks, err := s.opts.NetworkEngine.Networks(ctx)
	if err != nil {
		return engine.Network{}, false, err
	}
	for _, n := range networks {
		if n.ID == ref || n.Name == ref {
			return n, true, nil
		}
	}
	if !networkIDPrefixRe.MatchString(ref) {
		return engine.Network{}, false, nil
	}
	var found engine.Network
	matches := 0
	for _, n := range networks {
		if strings.HasPrefix(n.ID, ref) {
			found = n
			matches++
		}
	}
	return found, matches == 1, nil
}

func (s *server) writeNetworkError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, engine.ErrNetworkNotFound) {
		writeError(w, http.StatusNotFound, "network not found")
		return
	}
	s.writeEngineError(w, r, err)
}

func (s *server) handleNetworkDelete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	n, found, err := s.resolveNetwork(ctx, r.PathValue("id"))
	if err != nil {
		s.writeNetworkError(w, r, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "network not found")
		return
	}
	n, err = s.opts.NetworkEngine.InspectNetwork(ctx, n.ID)
	if err != nil {
		s.writeNetworkError(w, r, err)
		return
	}
	refuse := func(reason string) {
		s.opts.Logger.Info("network removal refused", "name", n.Name, "reason", reason)
		writeError(w, http.StatusConflict, reason)
	}
	if isBuiltinNetwork(n) {
		refuse(fmt.Sprintf("%s is a built-in network", n.Name))
		return
	}
	projects, err := s.projectStacks(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if stack := projects[n.Project()]; stack != "" {
		refuse(fmt.Sprintf("%s belongs to stack %s; remove it with the stack", n.Name, stack))
		return
	}
	if len(n.Containers) > 0 {
		names := make([]string, 0, len(n.Containers))
		for _, c := range n.Containers {
			names = append(names, c.Name)
		}
		sort.Strings(names)
		message := fmt.Sprintf("%s is in use by %s", n.Name, strings.Join(names[:min(3, len(names))], ", "))
		if len(names) > 3 {
			message += fmt.Sprintf(" (+%d more)", len(names)-3)
		}
		refuse(message)
		return
	}
	if err = s.opts.NetworkEngine.RemoveNetwork(ctx, n.ID); err != nil {
		var action *engine.ActionError
		if !errors.Is(err, engine.ErrNetworkNotFound) && errors.As(err, &action) {
			refuse(action.Message)
		} else {
			s.writeNetworkError(w, r, err)
		}
		return
	}
	s.invalidateNetworkLists()
	s.opts.Logger.Info("network removed", "name", n.Name)
	w.WriteHeader(http.StatusNoContent)
}

// handleNetworkMembership resolves the network and container, then runs connect or disconnect.
func (s *server) handleNetworkMembership(w http.ResponseWriter, r *http.Request, verb string) {
	var req apitypes.NetworkContainerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Container == "" {
		writeError(w, http.StatusBadRequest, "container is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	n, found, err := s.resolveNetwork(ctx, r.PathValue("id"))
	if err != nil {
		s.writeNetworkError(w, r, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "network not found")
		return
	}
	if verb == "connect" {
		if n.Driver == "host" || n.Driver == "null" {
			writeError(w, http.StatusConflict, fmt.Sprintf("containers can't join the %s network at runtime", n.Name))
			return
		}
		err = s.opts.NetworkEngine.ConnectNetwork(ctx, n.ID, req.Container)
	} else {
		err = s.opts.NetworkEngine.DisconnectNetwork(ctx, n.ID, req.Container)
	}
	if err != nil {
		var action *engine.ActionError
		switch {
		case errors.As(err, &action) && action.Status == http.StatusNotFound:
			writeError(w, http.StatusNotFound, action.Message)
		case errors.As(err, &action):
			writeError(w, http.StatusConflict, action.Message)
		default:
			s.writeEngineError(w, r, err)
		}
		return
	}
	s.invalidateNetworkLists()
	s.opts.Logger.Info("network "+verb, "network", n.Name, "container", req.Container)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleNetworkConnect(w http.ResponseWriter, r *http.Request) {
	s.handleNetworkMembership(w, r, "connect")
}

func (s *server) handleNetworkDisconnect(w http.ResponseWriter, r *http.Request) {
	s.handleNetworkMembership(w, r, "disconnect")
}

func (s *server) handleNetworkPrune(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	removed, err := s.opts.NetworkEngine.PruneNetworks(ctx)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	removed = append([]string{}, removed...)
	sort.Strings(removed)
	s.invalidateNetworkLists()
	s.opts.Logger.Info("networks pruned", "count", len(removed))
	writeJSON(w, http.StatusOK, apitypes.NetworkPruneResult{Removed: removed})
}
