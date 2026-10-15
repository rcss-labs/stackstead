package host

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/metrics"
)

const (
	snapshotMaxAge  = 5 * time.Second
	restartsMaxAge  = time.Minute
	volumesMaxAge   = 10 * time.Minute // system df is slow and volumes change rarely
	defaultRefresh  = 30 * time.Second
	restartWindow   = 24 * time.Hour
	defaultTimeout  = 5 * time.Second
	labelService    = "com.docker.compose.service"
	labelProject    = "com.docker.compose.project"
	dockerSourceNIC = "containers"
)

// DockerAPI is the part of the engine client the collector uses.
type DockerAPI interface {
	Info(ctx context.Context) (engine.Info, error)
	VolumesDiskUsage(ctx context.Context) (engine.VolumeUsage, error)
	EventsWindow(ctx context.Context, since, until time.Time, actions []string) ([]engine.Event, error)
}

// SamplerLatest exposes the metrics sampler's current per-container readings.
type SamplerLatest interface {
	Latest() []metrics.ExpositionSample
}

// ContainerSource lists containers with state.Cache semantics: it returns an
// error while Docker is disconnected.
type ContainerSource interface {
	Containers(ctx context.Context) ([]engine.Container, error)
}

// Statfser reports free and total bytes of the filesystem holding path.
type Statfser interface {
	Statfs(path string) (free, total uint64, err error)
}

// Config wires the collector. Empty ProcRoot, SysRoot, Now, Hostname, GOOS
// and CallTimeout take their defaults; nil dependencies behave as unavailable.
type Config struct {
	ProcRoot, SysRoot, StacksDir string
	Docker                       DockerAPI
	Sampler                      SamplerLatest
	Containers                   ContainerSource
	Statfs                       Statfser
	Now                          func() time.Time
	Hostname                     func() (string, error)
	GOOS                         string
	CallTimeout                  time.Duration // per Docker call on a request path, default 5s
	RefreshTimeout               time.Duration // per background df / events refresh, default 30s
}

// Snapshot is the GET /api/host body. A nil section means "no source".
type Snapshot struct {
	Hostname    string           `json:"hostname"`
	OS          string           `json:"os"`
	Source      string           `json:"source"` // "proc" or "docker"
	SampledAt   time.Time        `json:"sampledAt"`
	Containers  *ContainerCounts `json:"containers"`
	CPU         *CPU             `json:"cpu"`
	Memory      *Memory          `json:"memory"`
	Disk        *Disk            `json:"disk"`
	Network     *Network         `json:"network"`
	DiskIO      *DiskIO          `json:"diskIO"`
	Volumes     *Volumes         `json:"volumes"`
	Uptime      *Uptime          `json:"uptime"`
	Restarts24h *Restarts        `json:"restarts24h"`
}

type ContainerCounts struct {
	Total   int `json:"total"`
	Running int `json:"running"`
	Exited  int `json:"exited"`
	Bad     int `json:"bad"`
}

type CPU struct {
	Percent *float64 `json:"percent"`
	Cores   int      `json:"cores"`
}

type Memory struct {
	Used      uint64 `json:"used"`
	Total     uint64 `json:"total"`
	SwapUsed  uint64 `json:"swapUsed"`
	SwapTotal uint64 `json:"swapTotal"`
}

type Disk struct {
	Path  string `json:"path"`
	Free  uint64 `json:"free"`
	Total uint64 `json:"total"`
}

type Network struct {
	RxBytesPerSec *float64 `json:"rxBytesPerSec"`
	TxBytesPerSec *float64 `json:"txBytesPerSec"`
	Interface     string   `json:"interface"`
}

type DiskIO struct {
	ReadBytesPerSec  *float64 `json:"readBytesPerSec"`
	WriteBytesPerSec *float64 `json:"writeBytesPerSec"`
}

type Volumes struct {
	Count    int    `json:"count"`
	Size     uint64 `json:"size"`
	Dangling int    `json:"dangling"`
}

type Uptime struct {
	Seconds float64 `json:"seconds"`
	Load1   float64 `json:"load1"`
	Load5   float64 `json:"load5"`
	Load15  float64 `json:"load15"`
}

type Restarts struct {
	Total int          `json:"total"`
	Top   []RestartTop `json:"top"`
}

type RestartTop struct {
	Container string `json:"container"`
	Service   string `json:"service"`
	Project   string `json:"project"`
	Count     int    `json:"count"`
}

// restartScan is the result of one 24 h events scan.
type restartScan struct {
	byID     map[string]int
	restarts Restarts
}

// Collector builds host snapshots on request. It owns no timers or
// goroutines of its own: collection happens inside Snapshot callers, and the
// slow Docker sub-caches refresh in a short-lived goroutine started from a
// request when they are stale.
type Collector struct {
	cfg      Config
	snap     *cached[Snapshot]
	volumes  *cached[engine.VolumeUsage]
	restarts *cached[restartScan]

	// Previous readings. Only touched inside the snapshot load, which the
	// single-flight in snap runs one at a time.
	prevProc   procPrev
	prevDocker dockerPrev
}

type procPrev struct {
	has                   bool
	at                    time.Time
	cpu                   cpuTimes
	cpuOK, netOK, diskOK  bool
	netByIface            map[string][2]uint64
	diskRead, diskWritten uint64
}

// containerCounters are one container's cumulative counters. cpu and mem are
// not used for rates; they only reveal whether the sampler ticked, because a
// container with idle counters would otherwise look like "no new tick".
type containerCounters struct {
	rx, tx, blkRead, blkWrite uint64
	cpu                       float64
	mem                       uint64
}

type dockerRates struct{ rx, tx, blkRead, blkWrite *float64 }

type dockerPrev struct {
	has        bool
	at         time.Time
	containers map[string]containerCounters
	last       dockerRates // rates computed from the last advance, served between sampler ticks
	lastAt     time.Time   // when last was computed; held rates expire dockerHoldMax after it
}

// dockerHoldMax bounds how long rates are held while the sampler shows no
// change. Beyond it an unchanged tuple means idle containers, so rates are
// computed normally (0 B/s) instead of staying unavailable forever.
const dockerHoldMax = 30 * time.Second

func New(cfg Config) *Collector {
	if cfg.ProcRoot == "" {
		cfg.ProcRoot = "/proc"
	}
	if cfg.SysRoot == "" {
		cfg.SysRoot = "/sys"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Hostname == nil {
		cfg.Hostname = os.Hostname
	}
	if cfg.GOOS == "" {
		cfg.GOOS = runtime.GOOS
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = defaultTimeout
	}
	if cfg.RefreshTimeout <= 0 {
		cfg.RefreshTimeout = defaultRefresh
	}
	return &Collector{
		cfg:      cfg,
		snap:     newCached[Snapshot](cfg.Now),
		volumes:  newCached[engine.VolumeUsage](cfg.Now),
		restarts: newCached[restartScan](cfg.Now),
	}
}

// Snapshot returns the host state, collecting at most once per 5 s however
// many callers ask. It never fails: unavailable sources become nil sections.
func (c *Collector) Snapshot(ctx context.Context) Snapshot {
	s, err := c.snap.Get(ctx, snapshotMaxAge, func(ctx context.Context) (Snapshot, error) {
		// Detach so one caller going away cannot poison the shared result
		// with half-collected sections; every Docker call has its own timeout.
		return c.collect(context.WithoutCancel(ctx)), nil
	})
	if err != nil { // only a waiting caller's own context ending
		return c.identity()
	}
	return s
}

// RestartCounts maps container id to its restarts in the last 24 h, or nil
// before the first scan has finished. The map is shared: do not modify it.
func (c *Collector) RestartCounts() map[string]int {
	scan, ok := c.restarts.GetStale(restartsMaxAge, c.loadRestarts)
	if !ok {
		return nil
	}
	return scan.byID
}

func (c *Collector) identity() Snapshot {
	host, _ := c.cfg.Hostname()
	source := "docker"
	if _, err := os.ReadFile(filepath.Join(c.cfg.ProcRoot, "stat")); err == nil { // must be readable, not just present
		source = "proc"
	}
	return Snapshot{Hostname: host, OS: c.cfg.GOOS, Source: source, SampledAt: c.cfg.Now()}
}

func (c *Collector) collect(ctx context.Context) Snapshot {
	s := c.identity()
	now := s.SampledAt

	counts, containersErr := c.containerCounts(ctx)
	s.Containers = counts
	dockerUp := containersErr == nil

	if s.Source == "proc" {
		c.collectProc(&s, now)
	} else {
		c.collectDocker(ctx, &s, now, dockerUp)
	}

	s.Disk = c.disk()

	if dockerUp {
		if v, ok := c.volumes.GetStale(volumesMaxAge, c.loadVolumes); ok {
			s.Volumes = &Volumes{Count: v.Count, Size: v.Size, Dangling: v.Dangling}
		}
		if scan, ok := c.restarts.GetStale(restartsMaxAge, c.loadRestarts); ok {
			r := scan.restarts
			s.Restarts24h = &r
		}
	}
	return s
}

func (c *Collector) containerCounts(ctx context.Context) (*ContainerCounts, error) {
	if c.cfg.Containers == nil {
		return nil, context.Canceled
	}
	list, err := c.cfg.Containers.Containers(ctx)
	if err != nil {
		return nil, err
	}
	counts := &ContainerCounts{Total: len(list)}
	for _, ct := range list {
		switch ct.State {
		case "running":
			counts.Running++
			if ct.Health() == "unhealthy" {
				counts.Bad++
			}
		case "dead", "restarting":
			counts.Bad++
		case "exited":
			counts.Exited++
			if code, ok := ct.ExitCode(); ok && code != 0 {
				counts.Bad++
			}
		}
	}
	return counts, nil
}

func (c *Collector) disk() *Disk {
	if c.cfg.Statfs == nil {
		return nil
	}
	free, total, err := c.cfg.Statfs.Statfs(c.cfg.StacksDir)
	if err != nil {
		return nil
	}
	return &Disk{Path: c.cfg.StacksDir, Free: free, Total: total}
}

func (c *Collector) collectProc(s *Snapshot, now time.Time) {
	root := c.cfg.ProcRoot
	prev := c.prevProc
	dt := now.Sub(prev.at)
	haveBase := prev.has && dt >= time.Second

	cur := procPrev{has: true, at: now}

	if cpu, cores, err := readStat(root); err == nil {
		cur.cpu, cur.cpuOK = cpu, true
		s.CPU = &CPU{Cores: cores}
		if haveBase && prev.cpuOK {
			s.CPU.Percent = cpuPercent(prev.cpu, cpu)
		}
	}

	if used, total, swapUsed, swapTotal, ok, err := readMeminfo(root); err == nil && ok {
		s.Memory = &Memory{Used: used, Total: total, SwapUsed: swapUsed, SwapTotal: swapTotal}
	}

	if _, _, perIface, err := readNetDev(root, c.cfg.SysRoot); err == nil {
		cur.netByIface, cur.netOK = perIface, true
		s.Network = &Network{}
		if haveBase && prev.netOK {
			// Deltas only over interfaces present in both readings, so a NIC
			// appearing between them cannot show up as a one-interval spike.
			var dRx, dTx uint64
			common := 0
			for name, cv := range perIface {
				pv, ok := prev.netByIface[name]
				if !ok {
					continue
				}
				common++
				if cv[0] >= pv[0] {
					dRx += cv[0] - pv[0]
				}
				if cv[1] >= pv[1] {
					dTx += cv[1] - pv[1]
				}
			}
			if common > 0 {
				s.Network.RxBytesPerSec = rate(0, dRx, dt)
				s.Network.TxBytesPerSec = rate(0, dTx, dt)
			}
			s.Network.Interface = busiest(prev.netByIface, perIface)
		}
	}

	if r, w, err := readDiskstats(root); err == nil {
		cur.diskRead, cur.diskWritten, cur.diskOK = r, w, true
		s.DiskIO = &DiskIO{}
		if haveBase && prev.diskOK {
			s.DiskIO.ReadBytesPerSec = rate(prev.diskRead, r, dt)
			s.DiskIO.WriteBytesPerSec = rate(prev.diskWritten, w, dt)
		}
	}

	if secs, err := readUptime(root); err == nil {
		if l1, l5, l15, err := readLoadavg(root); err == nil {
			s.Uptime = &Uptime{Seconds: secs, Load1: l1, Load5: l5, Load15: l15}
		}
	}

	if !prev.has || haveBase {
		c.prevProc = cur
	}
}

func (c *Collector) collectDocker(ctx context.Context, s *Snapshot, now time.Time, dockerUp bool) {
	var info engine.Info
	if c.cfg.Docker != nil {
		callCtx, cancel := context.WithTimeout(ctx, c.cfg.CallTimeout)
		var err error
		info, err = c.cfg.Docker.Info(callCtx)
		cancel()
		if err != nil {
			dockerUp = false
		}
	} else {
		dockerUp = false
	}
	if !dockerUp {
		c.prevDocker = dockerPrev{} // do not span an outage when rates resume
		return
	}

	var samples []metrics.ExpositionSample
	if c.cfg.Sampler != nil {
		samples = c.cfg.Sampler.Latest()
	}
	var cpuSum float64
	var memSum uint64
	cur := dockerPrev{has: true, at: now, containers: make(map[string]containerCounters, len(samples))}
	for _, sm := range samples {
		cpuSum += sm.CPU
		memSum += sm.Mem
		cur.containers[sm.Container] = containerCounters{sm.NetRx, sm.NetTx, sm.BlkRead, sm.BlkWrite, sm.CPU, sm.Mem}
	}

	s.CPU = &CPU{Cores: info.NCPU}
	if info.NCPU > 0 {
		pct := cpuSum / float64(info.NCPU)
		s.CPU.Percent = &pct
	}
	s.Memory = &Memory{Used: memSum, Total: info.MemTotal}
	s.Network = &Network{Interface: dockerSourceNIC}
	s.DiskIO = &DiskIO{}

	prev := c.prevDocker
	dt := now.Sub(prev.at)
	if prev.has && dt >= time.Second {
		// Per-container deltas over containers present in both readings, so a
		// container starting or being removed cannot spike or dip the rate.
		var d containerCounters
		shared, changed := 0, false
		for name, cv := range cur.containers {
			pv, ok := prev.containers[name]
			if !ok {
				continue
			}
			shared++
			changed = changed || cv != pv
			d.rx += sub(cv.rx, pv.rx)
			d.tx += sub(cv.tx, pv.tx)
			d.blkRead += sub(cv.blkRead, pv.blkRead)
			d.blkWrite += sub(cv.blkWrite, pv.blkWrite)
		}
		if shared > 0 && !changed {
			// The sampler has not ticked since prev (it runs less often than
			// snapshots). Never advance the baseline, so the next tick is
			// divided by the whole interval it covers. Serve the last rates
			// while they exist and are at most dockerHoldMax old; otherwise
			// the counters really did not move: explicit zeros.
			if prev.last.rx != nil && now.Sub(prev.lastAt) <= dockerHoldMax {
				s.Network.RxBytesPerSec, s.Network.TxBytesPerSec = prev.last.rx, prev.last.tx
				s.DiskIO.ReadBytesPerSec, s.DiskIO.WriteBytesPerSec = prev.last.blkRead, prev.last.blkWrite
			} else {
				z := func() *float64 { v := 0.0; return &v }
				s.Network.RxBytesPerSec, s.Network.TxBytesPerSec = z(), z()
				s.DiskIO.ReadBytesPerSec, s.DiskIO.WriteBytesPerSec = z(), z()
			}
			return
		}
		s.Network.RxBytesPerSec = rate(0, d.rx, dt)
		s.Network.TxBytesPerSec = rate(0, d.tx, dt)
		s.DiskIO.ReadBytesPerSec = rate(0, d.blkRead, dt)
		s.DiskIO.WriteBytesPerSec = rate(0, d.blkWrite, dt)
		cur.last = dockerRates{s.Network.RxBytesPerSec, s.Network.TxBytesPerSec, s.DiskIO.ReadBytesPerSec, s.DiskIO.WriteBytesPerSec}
		cur.lastAt = now
	}
	if !prev.has || dt >= time.Second {
		c.prevDocker = cur
	}
}

// sub is cur-prev, or 0 when the counter dropped (container restarted).
func sub(cur, prev uint64) uint64 {
	if cur < prev {
		return 0
	}
	return cur - prev
}

// loadVolumes runs on a detached context: it must outlive the request that
// noticed the cache was stale.
func (c *Collector) loadVolumes() (engine.VolumeUsage, error) {
	if c.cfg.Docker == nil {
		return engine.VolumeUsage{}, context.Canceled
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.RefreshTimeout)
	defer cancel()
	return c.cfg.Docker.VolumesDiskUsage(ctx)
}

func (c *Collector) loadRestarts() (restartScan, error) {
	if c.cfg.Docker == nil {
		return restartScan{}, context.Canceled
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.RefreshTimeout)
	defer cancel()
	now := c.cfg.Now()
	events, err := c.cfg.Docker.EventsWindow(ctx, now.Add(-restartWindow), now, []string{"start"})
	if err != nil {
		return restartScan{}, err
	}
	return countRestarts(events), nil
}

// countRestarts turns start events into per-container restart counts: the
// first start of a container is not a restart, every further one is. Only
// "start" actions count even if the engine returns others.
func countRestarts(events []engine.Event) restartScan {
	type seen struct {
		starts                 int
		name, service, project string
	}
	byID := map[string]*seen{}
	for _, ev := range events {
		if ev.Action != "start" {
			continue
		}
		e := byID[ev.ContainerID]
		if e == nil {
			e = &seen{name: ev.Name, service: ev.Labels[labelService], project: ev.Labels[labelProject]}
			byID[ev.ContainerID] = e
		}
		e.starts++
	}

	scan := restartScan{byID: make(map[string]int, len(byID)), restarts: Restarts{Top: []RestartTop{}}}
	var top []RestartTop
	for id, e := range byID {
		n := e.starts - 1
		scan.byID[id] = n
		scan.restarts.Total += n
		if n > 0 {
			top = append(top, RestartTop{Container: e.name, Service: e.service, Project: e.project, Count: n})
		}
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].Count != top[j].Count {
			return top[i].Count > top[j].Count
		}
		return top[i].Container < top[j].Container
	})
	if len(top) > 2 {
		top = top[:2]
	}
	scan.restarts.Top = append(scan.restarts.Top, top...)
	return scan
}
