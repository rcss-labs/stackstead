package history

import (
	"context"
	"sort"
	"time"
)

// Run is a stretch of time during which a service was in one state.
type Run struct {
	State    string
	From, To time.Time
}

// ServiceTimeline is the contiguous runs of one service across the window.
type ServiceTimeline struct {
	Service  string
	Restarts int
	Runs     []Run
}

// Timeline covers [From, To] with one ServiceTimeline per service.
type Timeline struct {
	From, To time.Time
	Services []ServiceTimeline
}

// Rank orders states by severity; the highest-ranked state across a
// service's containers is the service's state.
func Rank(state string) int {
	switch state {
	case StateExited:
		return 6
	case StateUnhealthy:
		return 5
	case StateRestarting:
		return 4
	case StateStarting:
		return 3
	case StateHealthy, StateRunning:
		return 2
	case StateRemoved:
		return 1
	}
	return 0
}

// Timeline loads the window's rows and builds the timeline.
func (s *Store) Timeline(ctx context.Context, project string, from, to time.Time) (Timeline, error) {
	rows, err := s.Query(ctx, project, from, to)
	if err != nil {
		return Timeline{}, err
	}
	return Build(rows, from, to), nil
}

// stateChange is one container state taking effect at at (seconds).
type stateChange struct {
	at    int64
	state string
}

// Build turns rows (container state changes, including the baseline row per
// container that precedes from) into per-service runs covering [from, to].
// Rows of one container must be in insertion order; for equal timestamps the
// later row wins. Ties in rank go to the alphabetically first container.
func Build(rows []Row, from, to time.Time) Timeline {
	f, t := from.Unix(), to.Unix()
	tl := Timeline{From: from, To: to, Services: []ServiceTimeline{}}

	// service -> container -> changes
	containers := map[string]map[string][]stateChange{}
	restarts := map[string]int{}
	for _, row := range rows {
		ts := row.TS.Unix()
		if containers[row.Service] == nil {
			containers[row.Service] = map[string][]stateChange{}
		}
		if row.State == StateRestart {
			if ts >= f && ts <= t {
				restarts[row.Service]++
			}
			continue
		}
		if ts > t {
			continue
		}
		if ts < f {
			ts = f // baseline: in force when the window opens
		}
		containers[row.Service][row.Container] = append(containers[row.Service][row.Container], stateChange{ts, row.State})
	}

	names := make([]string, 0, len(containers))
	for name := range containers {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		tl.Services = append(tl.Services, ServiceTimeline{
			Service:  name,
			Restarts: restarts[name],
			Runs:     buildRuns(containers[name], f, t),
		})
	}
	return tl
}

func buildRuns(byContainer map[string][]stateChange, f, t int64) []Run {
	containerNames := make([]string, 0, len(byContainer))
	boundarySet := map[int64]bool{f: true, t: true}
	for name, changes := range byContainer {
		containerNames = append(containerNames, name)
		sort.SliceStable(changes, func(i, j int) bool { return changes[i].at < changes[j].at })
		for _, c := range changes {
			boundarySet[c.at] = true
		}
	}
	sort.Strings(containerNames)
	boundaries := make([]int64, 0, len(boundarySet))
	for b := range boundarySet {
		boundaries = append(boundaries, b)
	}
	sort.Slice(boundaries, func(i, j int) bool { return boundaries[i] < boundaries[j] })

	runs := []Run{}
	for i := 0; i+1 < len(boundaries); i++ {
		state := worstStateAt(byContainer, containerNames, boundaries[i])
		start, end := time.Unix(boundaries[i], 0), time.Unix(boundaries[i+1], 0)
		if n := len(runs); n > 0 && runs[n-1].State == state {
			runs[n-1].To = end
			continue
		}
		runs = append(runs, Run{State: state, From: start, To: end})
	}
	return runs
}

func worstStateAt(byContainer map[string][]stateChange, containerNames []string, at int64) string {
	worst := StateNoData
	for _, name := range containerNames {
		current := ""
		for _, c := range byContainer[name] {
			if c.at > at {
				break
			}
			current = c.state
		}
		if current != "" && Rank(current) > Rank(worst) {
			worst = current
		}
	}
	return worst
}
