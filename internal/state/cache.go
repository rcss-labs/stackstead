// Package state keeps an events-driven snapshot of the Docker containers so
// that status reads do not hit the engine and clients can be told when to
// refetch.
package state

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
)

const (
	defaultDebounce   = 250 * time.Millisecond
	defaultMinBackoff = 500 * time.Millisecond
	defaultMaxBackoff = 30 * time.Second
	subscriberBuffer  = 64
)

// Source is the engine as the cache uses it; satisfied by *engine.Client.
type Source interface {
	Containers(ctx context.Context) ([]engine.Container, error)
	Events(ctx context.Context, since time.Time) (<-chan engine.Event, <-chan error)
}

// Backoff bounds the wait between reconnect attempts: exponential from Min to
// Max with jitter. Zero values mean 500ms and 30s.
type Backoff struct{ Min, Max time.Duration }

// Change says that something in Project changed and clients should refetch.
// A Change with an empty Project means the cache's connection state changed
// (it connected or disconnected); read Connected to learn which. Seq increases
// by one with every published Change.
type Change struct {
	Project string
	Seq     uint64
}

// Cache holds the latest container snapshot, kept current from the engine's
// event stream. Create it with New and drive it with Run.
type Cache struct {
	src      Source
	log      *slog.Logger
	backoff  Backoff
	debounce time.Duration
	onWait   func(time.Duration) // test hook: called with each reconnect wait

	mu         sync.RWMutex
	containers []engine.Container
	connected  bool
	seq        uint64
	nextSub    int
	subs       map[int]chan Change
	eventSubs  map[int]chan engine.Event
}

// ForwardedActions are the container event actions SubscribeEvents delivers.
var ForwardedActions = []string{"create", "start", "die", "stop", "kill", "restart", "destroy",
	"pause", "unpause", "oom", "health_status"}

func New(src Source, logger *slog.Logger, backoff Backoff) *Cache {
	if logger == nil {
		logger = slog.Default()
	}
	if backoff.Min <= 0 {
		backoff.Min = defaultMinBackoff
	}
	if backoff.Max <= 0 {
		backoff.Max = defaultMaxBackoff
	}
	if backoff.Max < backoff.Min {
		backoff.Max = backoff.Min
	}
	return &Cache{src: src, log: logger, backoff: backoff, debounce: defaultDebounce,
		subs: map[int]chan Change{}, eventSubs: map[int]chan engine.Event{}}
}

// Run keeps the cache current until ctx is done. Each round subscribes to
// events first and only then resyncs, so no change falls between the two; a
// failed round marks the cache disconnected and retries after a backoff.
func (c *Cache) Run(ctx context.Context) {
	defer c.setConnected(false, false)
	attempt := 0
	for ctx.Err() == nil {
		stable, err := c.session(ctx)
		if ctx.Err() != nil {
			return
		}
		c.setConnected(false, true)
		if stable {
			attempt = 0
		}
		wait := c.delay(attempt)
		attempt++
		if c.onWait != nil {
			c.onWait(wait)
		}
		c.log.Warn("container event stream lost; retrying", "error", err, "in", wait)
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// delay is the exponential backoff for the given attempt, jittered into the
// upper half of the step so concurrent retries do not align.
func (c *Cache) delay(attempt int) time.Duration {
	d := c.backoff.Min
	for range attempt {
		d *= 2
		if d >= c.backoff.Max {
			d = c.backoff.Max
			break
		}
	}
	return d/2 + rand.N(d/2+1)
}

// session is one subscribe-resync-follow round. It returns whether the round
// proved the event stream works (it delivered an event or lasted at least
// Backoff.Max) and why the round ended. A listing that works while the stream
// keeps failing (say a proxy denying /events) is therefore not "stable", so
// retries keep backing off instead of flapping. Its event stream is cancelled
// on return, so rounds never overlap.
func (c *Cache) session(ctx context.Context) (stable bool, err error) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	begun := time.Now()
	// The no-gap guarantee relies on Docker replaying events from "since"
	// (second granularity); the refetch makes replayed events harmless.
	events, errs := c.src.Events(sctx, begun)
	// Network connect/disconnect events are not watched: the event filter is
	// container-only, and only container changes alter what the cache holds.
	synced := false
	// Lasting Backoff.Max counts as stable only after a successful resync; a
	// listing that hangs and then fails proves nothing.
	defer func() { stable = stable || (synced && time.Since(begun) >= c.backoff.Max) }()
	if err := c.resync(sctx); err != nil {
		return false, err
	}
	synced = true

	var timer *time.Timer
	var fire <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return stable, ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return stable, streamEnd(errs)
			}
			if strings.HasPrefix(ev.Action, "exec_") {
				continue // healthchecks emit these every interval; they change nothing
			}
			stable = true
			if slices.Contains(ForwardedActions, ev.Action) {
				c.publishEvent(ev)
			}
			if fire == nil {
				timer = time.NewTimer(c.debounce)
				fire = timer.C
			}
		case err, ok := <-errs:
			if !ok {
				return stable, streamEnd(nil)
			}
			return stable, err
		case <-fire:
			fire = nil
			if err := c.resync(sctx); err != nil {
				return stable, err
			}
		}
	}
}

// streamEnd reports why a closed event stream ended.
func streamEnd(errs <-chan error) error {
	if errs != nil {
		if err, ok := <-errs; ok && err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: event stream closed", engine.ErrUnavailable)
}

// resync replaces the snapshot with a fresh listing, marks the cache
// connected and publishes a Change for every project whose containers differ
// from the previous snapshot (including projects that vanished). The previous
// snapshot is kept across disconnects so changes made meanwhile are reported.
func (c *Cache) resync(ctx context.Context) error {
	fresh, err := c.src.Containers(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	old := byProject(c.containers)
	c.containers = fresh
	projects := changedProjects(old, byProject(fresh))
	if !c.connected {
		c.connected = true
		c.publishLocked("")
	}
	for _, p := range projects {
		c.publishLocked(p)
	}
	return nil
}

// setConnected records a connection state; publish announces a transition.
func (c *Cache) setConnected(v, publish bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.connected == v {
		return
	}
	c.connected = v
	if publish {
		c.publishLocked("")
	}
}

func byProject(cs []engine.Container) map[string][]view {
	m := map[string][]view{}
	for _, ct := range cs {
		if p := ct.Project(); p != "" {
			m[p] = append(m[p], project(ct))
		}
	}
	for _, list := range m {
		slices.SortFunc(list, func(a, b view) int { return strings.Compare(a.ID, b.ID) })
	}
	return m
}

// view is what a change in a container means to clients. Docker's human
// Status text ("Up 5 minutes") is left out because it changes with time alone;
// the health and exit code parsed from it are kept.
type view struct {
	ID, Name, Service, State, Image string
	Health                          string // "", "healthy", "unhealthy" or "starting"
	ExitCode                        string // from "Exited (N)", else ""
	Ports                           []engine.Port
	Networks                        map[string]string
}

func project(c engine.Container) view {
	v := view{ID: c.ID, Name: c.Name, Service: c.ComposeService(), State: c.State, Image: c.Image,
		Ports: c.Ports, Networks: c.Networks}
	if h := c.Health(); h != "none" {
		v.Health = h
	}
	if code, ok := c.ExitCode(); ok {
		v.ExitCode = strconv.Itoa(code)
	}
	return v
}

func changedProjects(old, fresh map[string][]view) []string {
	var out []string
	for p, list := range fresh {
		if prev, ok := old[p]; !ok || !reflect.DeepEqual(prev, list) {
			out = append(out, p)
		}
	}
	for p := range old {
		if _, ok := fresh[p]; !ok {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

// publishLocked fans a Change out without blocking; a subscriber whose buffer
// is full is dropped (its channel is closed) and must resubscribe and refetch.
func (c *Cache) publishLocked(project string) {
	c.seq++
	ch := Change{Project: project, Seq: c.seq}
	for id, sub := range c.subs {
		select {
		case sub <- ch:
		default:
			delete(c.subs, id)
			close(sub)
		}
	}
}

// Containers returns a copy of the latest snapshot. While the cache has never
// synced or is disconnected it returns an error wrapping engine.ErrUnavailable
// rather than stale data.
func (c *Cache) Containers(context.Context) ([]engine.Container, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.connected {
		return nil, fmt.Errorf("%w: container state is not synced", engine.ErrUnavailable)
	}
	out := make([]engine.Container, len(c.containers))
	for i, ct := range c.containers {
		ct.Labels = maps.Clone(ct.Labels)
		ct.Networks = maps.Clone(ct.Networks)
		ct.Ports = slices.Clone(ct.Ports)
		out[i] = ct
	}
	return out, nil
}

// Connected reports whether the snapshot is current.
func (c *Cache) Connected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

// Subscribe returns a channel of Changes and an idempotent cancel. Delivery
// never blocks the cache: a subscriber that falls behind has its channel
// closed and should resubscribe and refetch everything.
func (c *Cache) Subscribe() (changes <-chan Change, cancel func()) {
	ch := make(chan Change, subscriberBuffer)
	c.mu.Lock()
	id := c.nextSub
	c.nextSub++
	c.subs[id] = ch
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if _, ok := c.subs[id]; ok {
			delete(c.subs, id)
			close(ch)
		}
	}
}

// publishEvent fans a container event out without blocking; like publishLocked,
// a subscriber whose buffer is full is dropped (its channel is closed).
func (c *Cache) publishEvent(ev engine.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, sub := range c.eventSubs {
		select {
		case sub <- ev:
		default:
			delete(c.eventSubs, id)
			close(sub)
		}
	}
}

// SubscribeEvents returns a channel of container events whose action is in
// ForwardedActions, and an idempotent cancel. Subscribers survive reconnects.
// Same drop-on-slow semantics as Subscribe: a full buffer closes the channel.
func (c *Cache) SubscribeEvents() (events <-chan engine.Event, cancel func()) {
	ch := make(chan engine.Event, subscriberBuffer)
	c.mu.Lock()
	id := c.nextSub
	c.nextSub++
	c.eventSubs[id] = ch
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if _, ok := c.eventSubs[id]; ok {
			delete(c.eventSubs, id)
			close(ch)
		}
	}
}
