package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/peers"
)

const (
	fleetPeerTimeout = 3 * time.Second
	fleetMaxEvents   = 50
	fleetMaxBody     = 8 << 20
)

func (s *server) registerFleetRoutes(mux *http.ServeMux) {
	if s.opts.Stacks == nil {
		return
	}
	mux.Handle("GET /api/fleet/stacks", s.requireSession(s.handleFleetStacks))
}

// peerFleet is what one peer contributed.
type peerFleet struct {
	server apitypes.FleetServer
	stacks []apitypes.FleetStack
	events []apitypes.FleetEvent
}

// handleFleetStacks lists the stacks and recent events of this server and of
// every connected one. Peers are asked in parallel with a short timeout, and a
// peer that cannot answer is reported in servers instead of failing the call,
// so the page renders at once when one server is down.
func (s *server) handleFleetStacks(w http.ResponseWriter, r *http.Request) {
	self, err := s.opts.Peers.Self(r.Context(), r)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	list, err := s.opts.Stacks.List(r.Context())
	if err != nil {
		s.writeStackError(w, r, err)
		return
	}
	out := apitypes.FleetStacks{
		Servers: []apitypes.FleetServer{{ID: self.InstanceID, Name: self.Name, Slug: self.Slug, Local: true, Status: "reachable", Version: self.Version}},
		Stacks:  make([]apitypes.FleetStack, 0, len(list)),
		Events:  []apitypes.FleetEvent{},
	}
	for _, sum := range list {
		out.Stacks = append(out.Stacks, apitypes.FleetStack{Server: self.InstanceID, StackSummary: toSummary(sum)})
	}
	for _, ev := range s.recentEvents() {
		out.Events = append(out.Events, apitypes.FleetEvent{Server: self.InstanceID, EventMessage: ev})
	}

	views, err := s.opts.Peers.Views(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var active []apitypes.PeerView
	for _, v := range views {
		if v.Status != "pending" {
			active = append(active, v)
		}
	}
	results := make([]peerFleet, len(active))
	var wg sync.WaitGroup
	user := userFrom(r.Context()).Username
	for i, view := range active {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = s.fetchPeerFleet(r.Context(), view, user)
		}()
	}
	wg.Wait()
	for _, res := range results {
		out.Servers = append(out.Servers, res.server)
		out.Stacks = append(out.Stacks, res.stacks...)
		out.Events = append(out.Events, res.events...)
	}

	sort.SliceStable(out.Events, func(i, j int) bool { return out.Events[i].Time.After(out.Events[j].Time) })
	if len(out.Events) > fleetMaxEvents {
		out.Events = out.Events[:fleetMaxEvents]
	}
	writeJSON(w, http.StatusOK, out)
}

// fetchPeerFleet asks one peer for its stacks and events. A peer that is not
// usable is reported with its checker status and gets no request at all.
func (s *server) fetchPeerFleet(ctx context.Context, view apitypes.PeerView, user string) peerFleet {
	res := peerFleet{server: apitypes.FleetServer{
		ID: view.ID, Name: view.Name, Slug: view.Slug, Status: view.Status, LastSeenAt: view.LastSeenAt, Version: view.Version,
	}}
	fail := func(status string, err error) peerFleet {
		res.server.Status, res.server.Error = status, err.Error()
		return res
	}
	peer, err := s.opts.Peers.Usable(view.ID)
	if err != nil {
		var pe *peers.PeerError
		if errors.As(err, &pe) && pe.Code != peers.CodeUnreachable {
			return fail(view.Status, err)
		}
		return fail("unreachable", err)
	}
	base, err := url.Parse(peer.URL)
	if err != nil {
		return fail("unreachable", err)
	}
	client, err := peers.NewHTTPClient(base, peer.TLS, fleetPeerTimeout)
	if err != nil {
		return fail("unreachable", err)
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, fleetPeerTimeout)
	defer cancel()

	var (
		summaries            []apitypes.StackSummary
		events               []apitypes.EventMessage
		stacksErr, eventsErr error
		wg                   sync.WaitGroup
	)
	get := func(path string, into any, errOut *error) {
		defer wg.Done()
		*errOut = getPeerJSON(ctx, client, base, peer, user, path, into)
	}
	wg.Add(2)
	go get("/api/stacks", &summaries, &stacksErr)
	go get("/api/events/recent", &events, &eventsErr)
	wg.Wait()

	if stacksErr != nil {
		status := "unreachable"
		var pe *peers.PeerError
		if errors.As(stacksErr, &pe) && pe.Code == peers.CodeUnpaired {
			status = "removed"
		}
		return fail(status, stacksErr)
	}
	// Events are a nicety: a peer that cannot list them still shows its stacks.
	res.server.Status = "reachable"
	for _, sum := range summaries {
		res.stacks = append(res.stacks, apitypes.FleetStack{Server: view.ID, StackSummary: sum})
	}
	if eventsErr == nil {
		for _, ev := range events {
			res.events = append(res.events, apitypes.FleetEvent{Server: view.ID, EventMessage: ev})
		}
	}
	return res
}

// getPeerJSON sends one authenticated GET to the peer and decodes the JSON
// answer. Failures are classified (*peers.PeerError or *peers.RemoteError)
// and never contain the credential.
func getPeerJSON(ctx context.Context, client *http.Client, base *url.URL, peer peers.Peer, user, path string, into any) error {
	server := base.String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set(peers.PeerHeader, peer.OutCredential.Reveal())
	req.Header.Set(peerUserHeader, user)
	resp, err := client.Do(req)
	if cerr := peers.Classify(server, resp, err); cerr != nil {
		if resp != nil {
			resp.Body.Close()
		}
		return cerr
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, fleetMaxBody)).Decode(into); err != nil {
		return &peers.PeerError{Code: peers.CodeProxy, Server: server, Msg: server + " did not answer like Stackstead"}
	}
	return nil
}
