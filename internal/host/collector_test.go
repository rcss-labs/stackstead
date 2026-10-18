package host

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/metrics"
)

// ---- fakes ----

type fakeDocker struct {
	mu        sync.Mutex
	info      engine.Info
	infoErr   error
	infoCalls atomic.Int32
	infoGate  chan struct{} // when set, Info signals infoIn and waits on it
	infoIn    chan struct{}

	volumes      engine.VolumeUsage
	volumesErr   error
	volumesCalls atomic.Int32
	volumesIn    chan struct{} // when set, VolumesDiskUsage signals here and waits on volumesGate
	volumesGate  chan struct{}

	events      []engine.Event
	eventsErr   error
	eventsCalls atomic.Int32
	gotActions  []string
	gotSince    time.Time
	gotUntil    time.Time

	// time left on the context deadline the last df / events call received
	volumesBudget, eventsBudget time.Duration
}

func budget(ctx context.Context) time.Duration {
	if dl, ok := ctx.Deadline(); ok {
		return time.Until(dl)
	}
	return 0
}

func (f *fakeDocker) Info(context.Context) (engine.Info, error) {
	f.infoCalls.Add(1)
	if f.infoIn != nil {
		select {
		case f.infoIn <- struct{}{}:
		default:
		}
		<-f.infoGate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.info, f.infoErr
}

func (f *fakeDocker) VolumesDiskUsage(ctx context.Context) (engine.VolumeUsage, error) {
	f.volumesCalls.Add(1)
	f.mu.Lock()
	f.volumesBudget = budget(ctx)
	in, gate := f.volumesIn, f.volumesGate
	f.mu.Unlock()
	if in != nil {
		in <- struct{}{}
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.volumes, f.volumesErr
}

func (f *fakeDocker) EventsWindow(ctx context.Context, since, until time.Time, actions []string) ([]engine.Event, error) {
	f.eventsCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.eventsBudget = budget(ctx)
	f.gotSince, f.gotUntil, f.gotActions = since, until, actions
	return f.events, f.eventsErr
}

type fakeSampler struct {
	mu      sync.Mutex
	samples []metrics.ExpositionSample
}

func (f *fakeSampler) Latest() []metrics.ExpositionSample {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]metrics.ExpositionSample(nil), f.samples...)
}

func (f *fakeSampler) set(s ...metrics.ExpositionSample) {
	f.mu.Lock()
	f.samples = s
	f.mu.Unlock()
}

type fakeContainers struct {
	list []engine.Container
	err  error
}

func (f *fakeContainers) Containers(context.Context) ([]engine.Container, error) {
	return f.list, f.err
}

type fakeStatfs struct {
	free, total uint64
	err         error
	path        string
}

func (f *fakeStatfs) Statfs(path string) (uint64, uint64, error) {
	f.path = path
	return f.free, f.total, f.err
}

// ---- helpers ----

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			copyDir(t, filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()))
			continue
		}
		in, err := os.Open(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out, err := os.Create(filepath.Join(dst, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(out, in); err != nil {
			t.Fatal(err)
		}
		in.Close()
		out.Close()
	}
}

// procDir returns a writable proc root holding the named fixture.
func procDir(t *testing.T, fixture string) string {
	t.Helper()
	dir := t.TempDir()
	copyDir(t, filepath.Join("testdata", "proc", fixture), dir)
	return dir
}

type rig struct {
	c       *Collector
	clock   *fakeClock
	docker  *fakeDocker
	sampler *fakeSampler
	conts   *fakeContainers
	statfs  *fakeStatfs
	proc    string
}

func newRig(t *testing.T, fixture string) *rig {
	t.Helper()
	r := &rig{
		clock:   newFakeClock(),
		docker:  &fakeDocker{info: engine.Info{NCPU: 4, MemTotal: 8000}},
		sampler: &fakeSampler{},
		conts:   &fakeContainers{},
		statfs:  &fakeStatfs{free: 100, total: 1000},
	}
	if fixture == "" {
		r.proc = filepath.Join(t.TempDir(), "no-proc")
	} else {
		r.proc = procDir(t, fixture)
	}
	r.c = New(Config{
		ProcRoot:   r.proc,
		SysRoot:    filepath.Join("testdata", "sys"),
		StacksDir:  "/opt/stacks",
		Docker:     r.docker,
		Sampler:    r.sampler,
		Containers: r.conts,
		Statfs:     r.statfs,
		Now:        r.clock.Now,
		Hostname:   func() (string, error) { return "host-01", nil },
		GOOS:       "linux",
	})
	return r
}

func (r *rig) swapProc(t *testing.T, fixture string) {
	t.Helper()
	copyDir(t, filepath.Join("testdata", "proc", fixture), r.proc)
}

func near(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, want %v", name, want)
	}
	if d := *got - want; d > 0.001 || d < -0.001 {
		t.Fatalf("%s = %v, want %v", name, *got, want)
	}
}

// ---- snapshot, proc source ----

func TestSnapshotProcSource(t *testing.T) {
	r := newRig(t, "a")
	ctx := context.Background()
	r.c.Snapshot(ctx)
	r.swapProc(t, "b")
	r.clock.Advance(10 * time.Second)
	s := r.c.Snapshot(ctx)

	if s.Source != "proc" || s.OS != "linux" || s.Hostname != "host-01" {
		t.Fatalf("identity = %q %q %q", s.Source, s.OS, s.Hostname)
	}
	if !s.SampledAt.Equal(r.clock.Now()) {
		t.Fatalf("SampledAt = %v, want %v", s.SampledAt, r.clock.Now())
	}
	if s.CPU == nil || s.CPU.Cores != 8 {
		t.Fatalf("cpu = %+v", s.CPU)
	}
	near(t, "cpu.percent", s.CPU.Percent, 50)
	if s.Memory == nil || *s.Memory != (Memory{Used: (16384000 - 5120000) * 1024, Total: 16384000 * 1024, SwapUsed: 1048576 * 1024, SwapTotal: 2097152 * 1024}) {
		t.Fatalf("memory = %+v", s.Memory)
	}
	if s.Disk == nil || *s.Disk != (Disk{Path: "/opt/stacks", Free: 100, Total: 1000}) || r.statfs.path != "/opt/stacks" {
		t.Fatalf("disk = %+v (statfs path %q)", s.Disk, r.statfs.path)
	}
	if s.Network == nil || s.Network.Interface != "eth0" {
		t.Fatalf("network = %+v", s.Network)
	}
	near(t, "rx", s.Network.RxBytesPerSec, 400000)
	near(t, "tx", s.Network.TxBytesPerSec, 100000)
	if s.DiskIO == nil {
		t.Fatal("diskIO nil")
	}
	near(t, "read", s.DiskIO.ReadBytesPerSec, 2048*512/10.0)
	near(t, "write", s.DiskIO.WriteBytesPerSec, (100+200)*512/10.0)
	if s.Uptime == nil || s.Uptime.Seconds != 12355.67 || s.Uptime.Load1 != 0.80 || s.Uptime.Load5 != 0.60 || s.Uptime.Load15 != 0.45 {
		t.Fatalf("uptime = %+v", s.Uptime)
	}
}

func TestSnapshotFirstReadingRatesNull(t *testing.T) {
	r := newRig(t, "a")
	s := r.c.Snapshot(context.Background())
	if s.CPU == nil || s.CPU.Percent != nil || s.CPU.Cores != 8 {
		t.Fatalf("cpu = %+v", s.CPU)
	}
	if s.Network == nil || s.Network.RxBytesPerSec != nil || s.Network.TxBytesPerSec != nil {
		t.Fatalf("network = %+v", s.Network)
	}
	if s.DiskIO == nil || s.DiskIO.ReadBytesPerSec != nil || s.DiskIO.WriteBytesPerSec != nil {
		t.Fatalf("diskIO = %+v", s.DiskIO)
	}
	if s.Memory == nil || s.Uptime == nil {
		t.Fatal("non-rate sections must be present on the first reading")
	}
}

func TestMemoryNullWithoutMemAvailable(t *testing.T) {
	r := newRig(t, "a")
	copyDir(t, filepath.Join("testdata", "proc", "nomemavail"), r.proc) // overlays meminfo
	if s := r.c.Snapshot(context.Background()); s.Memory != nil {
		t.Fatalf("memory = %+v, want nil", s.Memory)
	}
}

func TestNetworkIgnoresInterfaceAppearingBetweenReadings(t *testing.T) {
	r := newRig(t, "a")
	ctx := context.Background()
	r.c.Snapshot(ctx)
	r.swapProc(t, "b")
	// A NIC with a large counter shows up only in the second reading.
	netDev := filepath.Join(r.proc, "net", "dev")
	b, err := os.ReadFile(netDev)
	if err != nil {
		t.Fatal(err)
	}
	extra := "  eth9: 900000000    1    0    0    0     0          0         0  900000000    1    0    0    0     0       0          0\n"
	if err := os.WriteFile(netDev, append(b, extra...), 0o644); err != nil {
		t.Fatal(err)
	}
	sysRoot := t.TempDir()
	copyDir(t, filepath.Join("testdata", "sys"), sysRoot)
	for _, nic := range []string{"eth9"} {
		if err := os.MkdirAll(filepath.Join(sysRoot, "class", "net", nic, "device"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r.c.cfg.SysRoot = sysRoot
	r.clock.Advance(10 * time.Second)
	s := r.c.Snapshot(ctx)
	if s.Network == nil {
		t.Fatal("network nil")
	}
	near(t, "rx", s.Network.RxBytesPerSec, 400000)
	near(t, "tx", s.Network.TxBytesPerSec, 100000)
	if s.Network.Interface != "eth0" {
		t.Fatalf("interface = %q", s.Network.Interface)
	}
}

// ---- docker source ----

func TestFallbackMatrixDockerSource(t *testing.T) {
	r := newRig(t, "")
	r.docker.info = engine.Info{NCPU: 4, MemTotal: 8000}
	r.sampler.set(
		metrics.ExpositionSample{Container: "a", CPU: 100, Mem: 1000, NetRx: 1000, NetTx: 500, BlkRead: 2000, BlkWrite: 100},
		metrics.ExpositionSample{Container: "b", CPU: 60, Mem: 500, NetRx: 0, NetTx: 0, BlkRead: 0, BlkWrite: 0},
	)
	ctx := context.Background()
	s := r.c.Snapshot(ctx)
	if s.Source != "docker" {
		t.Fatalf("source = %q", s.Source)
	}
	if s.CPU == nil || s.CPU.Cores != 4 {
		t.Fatalf("cpu = %+v", s.CPU)
	}
	near(t, "cpu.percent", s.CPU.Percent, 40) // (100+60)/4
	if s.Memory == nil || *s.Memory != (Memory{Used: 1500, Total: 8000}) {
		t.Fatalf("memory = %+v", s.Memory)
	}
	if s.Network == nil || s.Network.Interface != "containers" || s.Network.RxBytesPerSec != nil {
		t.Fatalf("first network = %+v", s.Network)
	}
	if s.Uptime != nil {
		t.Fatalf("uptime = %+v, want nil", s.Uptime)
	}

	// Container a grows; container c is new and carries a huge counter that
	// must not count as growth.
	r.sampler.set(
		metrics.ExpositionSample{Container: "a", CPU: 100, Mem: 1000, NetRx: 2000, NetTx: 1500, BlkRead: 2000 + 5000, BlkWrite: 100},
		metrics.ExpositionSample{Container: "b", CPU: 60, Mem: 500},
		metrics.ExpositionSample{Container: "c", NetRx: 1 << 40, BlkRead: 1 << 40},
	)
	r.clock.Advance(10 * time.Second)
	s = r.c.Snapshot(ctx)
	near(t, "rx", s.Network.RxBytesPerSec, 100)
	near(t, "tx", s.Network.TxBytesPerSec, 100)
	near(t, "read", s.DiskIO.ReadBytesPerSec, 500)
	near(t, "write", s.DiskIO.WriteBytesPerSec, 0)
}

func TestDockerUnavailableNullsSections(t *testing.T) {
	for _, fixture := range []string{"a", ""} {
		r := newRig(t, fixture)
		r.docker.infoErr = errors.New("down")
		r.docker.volumesErr = errors.New("down")
		r.docker.eventsErr = errors.New("down")
		r.conts.err = errors.New("disconnected")
		r.sampler.set(metrics.ExpositionSample{Container: "a", CPU: 10, Mem: 10})
		s := r.c.Snapshot(context.Background())
		if s.Containers != nil || s.Volumes != nil || s.Restarts24h != nil {
			t.Fatalf("fixture %q: docker sections = %+v %+v %+v", fixture, s.Containers, s.Volumes, s.Restarts24h)
		}
		if fixture == "" {
			if s.CPU != nil || s.Memory != nil || s.Network != nil || s.DiskIO != nil {
				t.Fatalf("docker source kept host sections: %+v %+v %+v %+v", s.CPU, s.Memory, s.Network, s.DiskIO)
			}
		} else if s.CPU == nil || s.Memory == nil || s.Network == nil || s.DiskIO == nil || s.Uptime == nil {
			t.Fatal("proc source must keep host sections when docker is down")
		}
	}
}

// ---- containers ----

func TestContainerCountsFromCache(t *testing.T) {
	r := newRig(t, "a")
	r.conts.list = []engine.Container{
		{ID: "1", State: "running", Status: "Up 1 hour (healthy)"},
		{ID: "2", State: "running", Status: "Up 1 hour (unhealthy)"},
		{ID: "3", State: "running", Status: "Up 1 hour"},
		{ID: "4", State: "exited", Status: "Exited (1) 2 hours ago"},
		{ID: "5", State: "exited", Status: "Exited (0) 2 hours ago"},
		{ID: "6", State: "restarting", Status: "Restarting (1) 3 seconds ago"},
		{ID: "7", State: "dead", Status: "Dead"},
		{ID: "8", State: "created", Status: "Created"},
	}
	s := r.c.Snapshot(context.Background())
	want := ContainerCounts{Total: 8, Running: 3, Exited: 2, Bad: 4} // unhealthy + exit 1 + restarting + dead
	if s.Containers == nil || *s.Containers != want {
		t.Fatalf("containers = %+v, want %+v", s.Containers, want)
	}
}

func TestContainerCountsNullWhenDisconnected(t *testing.T) {
	r := newRig(t, "a")
	r.conts.err = errors.New("disconnected")
	if s := r.c.Snapshot(context.Background()); s.Containers != nil {
		t.Fatalf("containers = %+v, want nil", s.Containers)
	}
}

// ---- caching and single-flight ----

func TestSnapshotSingleFlight50(t *testing.T) {
	r := newRig(t, "")
	r.docker.infoGate = make(chan struct{})
	r.docker.infoIn = make(chan struct{}, 1)
	var wg sync.WaitGroup
	snaps := make([]Snapshot, 50)
	for i := range snaps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snaps[i] = r.c.Snapshot(context.Background())
		}()
	}
	<-r.docker.infoIn
	close(r.docker.infoGate)
	wg.Wait()
	if got := r.docker.infoCalls.Load(); got != 1 {
		t.Fatalf("Info called %d times, want 1", got)
	}
	for i := range snaps {
		if !snaps[i].SampledAt.Equal(snaps[0].SampledAt) {
			t.Fatalf("snapshot %d differs", i)
		}
	}
}

func TestSnapshotCachedFor5s(t *testing.T) {
	r := newRig(t, "")
	ctx := context.Background()
	first := r.c.Snapshot(ctx)
	r.clock.Advance(4 * time.Second)
	if second := r.c.Snapshot(ctx); !second.SampledAt.Equal(first.SampledAt) {
		t.Fatal("snapshot within 5s was recollected")
	}
	r.clock.Advance(time.Second)
	third := r.c.Snapshot(ctx)
	if !third.SampledAt.After(first.SampledAt) {
		t.Fatal("snapshot after 5s was not recollected")
	}
	if got := r.docker.infoCalls.Load(); got != 2 {
		t.Fatalf("Info called %d times, want 2", got)
	}
}

func TestSubCacheNullBeforeFirstRefresh(t *testing.T) {
	r := newRig(t, "a")
	r.docker.volumesIn = make(chan struct{})
	r.docker.volumesGate = make(chan struct{})
	r.docker.volumes = engine.VolumeUsage{Count: 3, Size: 30, Dangling: 1}
	s := r.c.Snapshot(context.Background()) // returns while the refresh is still blocked
	if s.Volumes != nil || s.Restarts24h != nil {
		t.Fatalf("sections before first refresh: %+v %+v", s.Volumes, s.Restarts24h)
	}
	<-r.docker.volumesIn
	close(r.docker.volumesGate)
	waitForRefresh(t, r.c.volumes)
	waitForRefresh(t, r.c.restarts)

	r.clock.Advance(5 * time.Second)
	s = r.c.Snapshot(context.Background())
	if s.Volumes == nil || *s.Volumes != (Volumes{Count: 3, Size: 30, Dangling: 1}) {
		t.Fatalf("volumes = %+v", s.Volumes)
	}
	if s.Restarts24h == nil || s.Restarts24h.Total != 0 || s.Restarts24h.Top == nil || len(s.Restarts24h.Top) != 0 {
		t.Fatalf("restarts = %+v", s.Restarts24h)
	}
}

func TestSubCacheServesStaleAndRefreshesOnce(t *testing.T) {
	r := newRig(t, "a")
	ctx := context.Background()
	r.docker.volumes = engine.VolumeUsage{Count: 1}
	r.c.Snapshot(ctx)
	waitForRefresh(t, r.c.volumes)
	waitForRefresh(t, r.c.restarts)
	r.clock.Advance(5 * time.Second)
	if s := r.c.Snapshot(ctx); s.Volumes == nil || s.Volumes.Count != 1 {
		t.Fatalf("volumes = %+v", s.Volumes)
	}

	// Make the next refresh slow and change what it will return.
	r.docker.mu.Lock()
	r.docker.volumes = engine.VolumeUsage{Count: 2}
	r.docker.volumesIn = make(chan struct{}, 1)
	r.docker.volumesGate = make(chan struct{})
	in, gate := r.docker.volumesIn, r.docker.volumesGate
	r.docker.mu.Unlock()

	r.clock.Advance(volumesMaxAge + time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s := r.c.Snapshot(ctx); s.Volumes == nil || s.Volumes.Count != 1 {
				t.Errorf("stale volumes = %+v, want count 1", s.Volumes)
			}
		}()
	}
	wg.Wait()
	<-in // refresh started and is blocked
	for i := 0; i < 5; i++ {
		r.clock.Advance(5 * time.Second)
		if s := r.c.Snapshot(ctx); s.Volumes == nil || s.Volumes.Count != 1 {
			t.Fatalf("stale volumes during refresh = %+v", s.Volumes)
		}
	}
	close(gate)
	waitForRefresh(t, r.c.volumes)
	if got := r.docker.volumesCalls.Load(); got != 2 {
		t.Fatalf("VolumesDiskUsage called %d times, want 2", got)
	}
	r.clock.Advance(5 * time.Second)
	if s := r.c.Snapshot(ctx); s.Volumes == nil || s.Volumes.Count != 2 {
		t.Fatalf("refreshed volumes = %+v", s.Volumes)
	}
}

// ---- restarts ----

func startEvent(id, name, service, project string) engine.Event {
	labels := map[string]string{}
	if service != "" {
		labels["com.docker.compose.service"] = service
		labels["com.docker.compose.project"] = project
	}
	return engine.Event{Type: "container", Action: "start", ContainerID: id, Name: name, Labels: labels}
}

func restartRig(t *testing.T) *rig {
	r := newRig(t, "a")
	var ev []engine.Event
	ev = append(ev, startEvent("fresh", "web-1", "web", "shop")) // 1 start -> 0
	for i := 0; i < 3; i++ {                                     // 3 starts -> 2
		ev = append(ev, startEvent("loop", "monitoring-loki-1", "loki", "monitoring"))
	}
	ev = append(ev, startEvent("manual", "db-1", "db", "shop"), startEvent("manual", "db-1", "db", "shop")) // 2 -> 1
	ev = append(ev, startEvent("manual2", "cache-1", "", ""), startEvent("manual2", "cache-1", "", ""))     // 2 -> 1
	ev = append(ev, engine.Event{Type: "container", Action: "die", ContainerID: "loop", Name: "monitoring-loki-1"},
		engine.Event{Type: "container", Action: "die", ContainerID: "fresh", Name: "web-1"})
	r.docker.events = ev
	return r
}

func TestRestartCounting(t *testing.T) {
	r := restartRig(t)
	ctx := context.Background()
	r.c.Snapshot(ctx)
	waitForRefresh(t, r.c.restarts)
	r.clock.Advance(5 * time.Second)
	s := r.c.Snapshot(ctx)
	if s.Restarts24h == nil {
		t.Fatal("restarts nil")
	}
	if s.Restarts24h.Total != 4 {
		t.Fatalf("total = %d, want 4 (die events and first starts must not count)", s.Restarts24h.Total)
	}
	top := s.Restarts24h.Top
	if len(top) != 2 {
		t.Fatalf("top = %+v", top)
	}
	if top[0] != (RestartTop{Container: "monitoring-loki-1", Service: "loki", Project: "monitoring", Count: 2}) {
		t.Fatalf("top[0] = %+v", top[0])
	}
	if top[1].Count != 1 {
		t.Fatalf("top[1] = %+v", top[1])
	}
	if want := []string{"start"}; len(r.docker.gotActions) != 1 || r.docker.gotActions[0] != want[0] {
		t.Fatalf("actions = %v", r.docker.gotActions)
	}
	if d := r.docker.gotUntil.Sub(r.docker.gotSince); d != 24*time.Hour {
		t.Fatalf("window = %v, want 24h", d)
	}
}

func TestRestartCountsExposed(t *testing.T) {
	r := restartRig(t)
	if got := r.c.RestartCounts(); got != nil {
		t.Fatalf("before first refresh = %v, want nil", got)
	}
	waitForRefresh(t, r.c.restarts)
	got := r.c.RestartCounts()
	want := map[string]int{"fresh": 0, "loop": 2, "manual": 1, "manual2": 1}
	if len(got) != len(want) {
		t.Fatalf("counts = %v, want %v", got, want)
	}
	for id, n := range want {
		if got[id] != n {
			t.Fatalf("counts[%s] = %d, want %d", id, got[id], n)
		}
	}
}

// ---- never errors ----

func TestSnapshotNeverErrors(t *testing.T) {
	r := newRig(t, "")
	r.docker.infoErr = errors.New("x")
	r.docker.volumesErr = errors.New("x")
	r.docker.eventsErr = errors.New("x")
	r.conts.err = errors.New("x")
	r.statfs.err = errors.New("x")
	s := r.c.Snapshot(context.Background())
	if s.Hostname != "host-01" || s.OS != "linux" || s.Source != "docker" || s.SampledAt.IsZero() {
		t.Fatalf("identity = %+v", s)
	}
	if s.Containers != nil || s.CPU != nil || s.Memory != nil || s.Disk != nil || s.Network != nil ||
		s.DiskIO != nil || s.Volumes != nil || s.Uptime != nil || s.Restarts24h != nil {
		t.Fatalf("expected all sections nil: %+v", s)
	}
}

func TestSourceNeedsReadableProcStat(t *testing.T) {
	r := newRig(t, "")
	if err := os.MkdirAll(filepath.Join(r.proc, "stat"), 0o755); err != nil { // exists but cannot be read as a file
		t.Fatal(err)
	}
	if s := r.c.Snapshot(context.Background()); s.Source != "docker" {
		t.Fatalf("source = %q, want docker for unreadable stat", s.Source)
	}
}

func TestDockerRatesHoldBetweenSamplerTicks(t *testing.T) {
	r := newRig(t, "")
	ctx := context.Background()
	tick1 := metrics.ExpositionSample{Container: "a", CPU: 1, Mem: 1, NetRx: 1000, BlkRead: 1000}
	tick2 := metrics.ExpositionSample{Container: "a", CPU: 2, Mem: 2, NetRx: 2000, BlkRead: 3000}

	r.sampler.set(tick1)
	r.c.Snapshot(ctx) // t
	r.clock.Advance(5 * time.Second)
	s := r.c.Snapshot(ctx) // t+5: identical tuple, nothing held yet -> explicit zeros (R10b)
	near(t, "idle rx", s.Network.RxBytesPerSec, 0)
	r.sampler.set(tick2)
	r.clock.Advance(5 * time.Second)
	s = r.c.Snapshot(ctx) // t+10: tick landed, 10 s since the unadvanced baseline
	near(t, "rx", s.Network.RxBytesPerSec, 100)
	near(t, "read", s.DiskIO.ReadBytesPerSec, 200)

	r.clock.Advance(5 * time.Second)
	s = r.c.Snapshot(ctx) // t+15: no new tick, keep the last rate
	near(t, "held rx", s.Network.RxBytesPerSec, 100)
	near(t, "held read", s.DiskIO.ReadBytesPerSec, 200)
}

func TestDockerRatesIdleContainersReportZeroNotNil(t *testing.T) {
	r := newRig(t, "")
	ctx := context.Background()
	r.sampler.set(metrics.ExpositionSample{Container: "a", NetRx: 500, NetTx: 500, BlkRead: 500, BlkWrite: 500})
	s := r.c.Snapshot(ctx)
	if s.Network.RxBytesPerSec != nil {
		t.Fatalf("first reading rx = %v, want nil", s.Network.RxBytesPerSec)
	}
	r.clock.Advance(5 * time.Second)
	s = r.c.Snapshot(ctx)
	near(t, "rx", s.Network.RxBytesPerSec, 0)
	near(t, "tx", s.Network.TxBytesPerSec, 0)
	near(t, "read", s.DiskIO.ReadBytesPerSec, 0)
	near(t, "write", s.DiskIO.WriteBytesPerSec, 0)
}

func TestDockerHeldRatesExpireAfter30Seconds(t *testing.T) {
	r := newRig(t, "")
	ctx := context.Background()
	r.sampler.set(metrics.ExpositionSample{Container: "a", NetRx: 1000})
	r.c.Snapshot(ctx) // t
	r.sampler.set(metrics.ExpositionSample{Container: "a", NetRx: 2000})
	r.clock.Advance(10 * time.Second)
	s := r.c.Snapshot(ctx) // t+10: 100 B/s computed
	near(t, "rx", s.Network.RxBytesPerSec, 100)
	r.clock.Advance(20 * time.Second)
	s = r.c.Snapshot(ctx) // t+30: no tick, still within 30 s -> held
	near(t, "held rx", s.Network.RxBytesPerSec, 100)
	r.clock.Advance(5 * time.Second)
	s = r.c.Snapshot(ctx) // t+35: computed at t+10, 25 s old -> still held
	near(t, "held rx", s.Network.RxBytesPerSec, 100)
	r.clock.Advance(10 * time.Second)
	s = r.c.Snapshot(ctx) // t+45: 35 s since computed -> expired, idle over the 35 s since baseline
	near(t, "expired rx", s.Network.RxBytesPerSec, 0)
}

// ---- background refresh bounds and cadence ----

func TestBackgroundRefreshesUseRefreshTimeout(t *testing.T) {
	r := newRig(t, "")
	r.conts.list = []engine.Container{{State: "running"}}
	r.c.cfg.RefreshTimeout = 20 * time.Second
	r.c.Snapshot(context.Background())
	waitForRefresh(t, r.c.volumes)
	waitForRefresh(t, r.c.restarts)
	r.docker.mu.Lock()
	vb, eb := r.docker.volumesBudget, r.docker.eventsBudget
	r.docker.mu.Unlock()
	for name, b := range map[string]time.Duration{"volumes": vb, "events": eb} {
		if b < 10*time.Second || b > 20*time.Second {
			t.Errorf("%s call budget = %v, want about the 20s RefreshTimeout", name, b)
		}
	}
}

func TestRefreshTimeoutDefaultsTo30Seconds(t *testing.T) {
	if got := New(Config{}).cfg.RefreshTimeout; got != 30*time.Second {
		t.Fatalf("default RefreshTimeout = %v, want 30s", got)
	}
}

func TestVolumesRefreshEveryTenMinutesRestartsEveryMinute(t *testing.T) {
	r := newRig(t, "")
	r.conts.list = []engine.Container{{State: "running"}}
	ctx := context.Background()
	snap := func(advance time.Duration) {
		r.clock.Advance(advance)
		r.c.Snapshot(ctx)
		waitForRefresh(t, r.c.volumes)
		waitForRefresh(t, r.c.restarts)
	}
	snap(0)
	snap(6 * time.Second) // past the 5 s snapshot cache
	snap(61 * time.Second)
	if v, e := r.docker.volumesCalls.Load(), r.docker.eventsCalls.Load(); v != 1 || e != 2 {
		t.Fatalf("after 67s: volumes=%d events=%d, want 1 and 2", v, e)
	}
	snap(8 * time.Minute) // 9 min 7 s since the first load
	if v := r.docker.volumesCalls.Load(); v != 1 {
		t.Fatalf("volumes refreshed after 9 min: %d calls", v)
	}
	snap(2 * time.Minute) // over 10 min
	if v := r.docker.volumesCalls.Load(); v != 2 {
		t.Fatalf("volumes not refreshed after 10 min: %d calls", v)
	}
}

type suppliedVolumes struct{ calls atomic.Int32 }

func (v *suppliedVolumes) VolumeSummary() (engine.VolumeUsage, bool) {
	n := v.calls.Add(1)
	return engine.VolumeUsage{Count: int(n), Size: uint64(n) * 100, Dangling: 1}, true
}
func TestCollectorUsesSuppliedVolumeCacheWithoutOwnTenMinuteCache(t *testing.T) {
	base := time.Now()
	var elapsed atomic.Int64
	source := &suppliedVolumes{}
	docker := &fakeDocker{}
	c := New(Config{Docker: docker, Containers: &fakeContainers{}, Volumes: source, ProcRoot: t.TempDir(), Now: func() time.Time { return base.Add(time.Duration(elapsed.Load())) }})
	first := c.Snapshot(context.Background())
	if first.Volumes == nil || first.Volumes.Size != 100 {
		t.Fatalf("first volumes=%+v", first.Volumes)
	}
	elapsed.Store(int64(6 * time.Second))
	second := c.Snapshot(context.Background())
	if second.Volumes == nil || second.Volumes.Size != 200 {
		t.Fatalf("second volumes=%+v", second.Volumes)
	}
	if n := docker.volumesCalls.Load(); n != 0 {
		t.Fatalf("collector ran its own df %d times", n)
	}
}
