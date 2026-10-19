package history

import (
	"reflect"
	"testing"
	"time"
)

func at(sec int64) time.Time { return time.Unix(sec, 0) }

func r(ts int64, container, state string) Row {
	return Row{TS: at(ts), Project: "p", Service: serviceOf(container), Container: container, State: state}
}

// serviceOf maps test container names like "a-1" to service "a"; names without a dash are their own service.
func serviceOf(container string) string {
	for i := 0; i < len(container); i++ {
		if container[i] == '-' {
			return container[:i]
		}
	}
	return container
}

func run(state string, from, to int64) Run { return Run{State: state, From: at(from), To: at(to)} }

func TestBuild(t *testing.T) {
	ha := func(ts int64, state string) Row {
		return Row{TS: at(ts), Project: "p", Service: "home-assistant", Container: "ha", State: state}
	}
	tests := []struct {
		name         string
		rows         []Row
		from, to     int64
		wantService  string
		wantRuns     []Run
		wantRestarts int
		wantNoSvc    bool
	}{
		{
			name:         "spec example",
			rows:         []Row{ha(1760860420, "healthy"), ha(1760861000, "restart"), ha(1760861500, "restart"), ha(1760862000, "restart"), ha(1760862900, "unhealthy")},
			from:         1760860000,
			to:           1760863600,
			wantService:  "home-assistant",
			wantRuns:     []Run{run("nodata", 1760860000, 1760860420), run("healthy", 1760860420, 1760862900), run("unhealthy", 1760862900, 1760863600)},
			wantRestarts: 3,
		},
		{
			name:        "baseline before window is clamped",
			rows:        []Row{r(500, "web", "running")},
			from:        1000,
			to:          2000,
			wantService: "web",
			wantRuns:    []Run{run("running", 1000, 2000)},
		},
		{
			name:      "no rows",
			from:      0,
			to:        100,
			wantNoSvc: true,
		},
		{
			name:        "restart rows outside window are ignored",
			rows:        []Row{r(5, "web", "restart"), r(10, "web", "healthy"), r(300, "web", "restart")},
			from:        10,
			to:          200,
			wantService: "web",
			wantRuns:    []Run{run("healthy", 10, 200)},
		},
		{
			name: "replicas: worst wins, removed loses to healthy",
			rows: []Row{
				r(0, "a-1", "healthy"), r(100, "a-1", "exited"), r(200, "a-1", "removed"),
				r(0, "a-2", "healthy"),
			},
			from:        0,
			to:          300,
			wantService: "a",
			wantRuns:    []Run{run("healthy", 0, 100), run("exited", 100, 200), run("healthy", 200, 300)},
		},
		{
			name:        "all replicas removed",
			rows:        []Row{r(0, "a-1", "removed"), r(0, "a-2", "removed")},
			from:        0,
			to:          10,
			wantService: "a",
			wantRuns:    []Run{run("removed", 0, 10)},
		},
		{
			name:        "running vs healthy tie goes to the alphabetically first container",
			rows:        []Row{r(0, "a-1", "healthy"), r(0, "a-2", "running")},
			from:        0,
			to:          10,
			wantService: "a",
			wantRuns:    []Run{run("healthy", 0, 10)},
		},
		{
			name:        "two rows at the same second: later wins, no zero-length run",
			rows:        []Row{r(5, "web", "starting"), r(5, "web", "healthy")},
			from:        0,
			to:          10,
			wantService: "web",
			wantRuns:    []Run{run("nodata", 0, 5), run("healthy", 5, 10)},
		},
		{
			name:        "row at exactly to produces no empty run",
			rows:        []Row{r(0, "web", "healthy"), r(10, "web", "exited")},
			from:        0,
			to:          10,
			wantService: "web",
			wantRuns:    []Run{run("healthy", 0, 10)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Build(tc.rows, at(tc.from), at(tc.to))
			if got.Services == nil {
				t.Fatal("Services must be non-nil")
			}
			if !got.From.Equal(at(tc.from)) || !got.To.Equal(at(tc.to)) {
				t.Fatalf("window = [%v,%v]", got.From, got.To)
			}
			if tc.wantNoSvc {
				if len(got.Services) != 0 {
					t.Fatalf("services = %+v, want none", got.Services)
				}
				return
			}
			if len(got.Services) != 1 || got.Services[0].Service != tc.wantService {
				t.Fatalf("services = %+v", got.Services)
			}
			svc := got.Services[0]
			if svc.Restarts != tc.wantRestarts {
				t.Errorf("restarts = %d, want %d", svc.Restarts, tc.wantRestarts)
			}
			if !reflect.DeepEqual(svc.Runs, tc.wantRuns) {
				t.Errorf("runs = %+v, want %+v", svc.Runs, tc.wantRuns)
			}
			assertContiguous(t, svc.Runs, at(tc.from), at(tc.to))
		})
	}
}

func TestBuildSortsServices(t *testing.T) {
	got := Build([]Row{r(0, "zeta", "healthy"), r(0, "alpha", "healthy")}, at(0), at(10))
	if len(got.Services) != 2 || got.Services[0].Service != "alpha" || got.Services[1].Service != "zeta" {
		t.Fatalf("services = %+v", got.Services)
	}
}

func assertContiguous(t *testing.T, runs []Run, from, to time.Time) {
	t.Helper()
	if len(runs) == 0 {
		t.Fatal("no runs")
	}
	if !runs[0].From.Equal(from) || !runs[len(runs)-1].To.Equal(to) {
		t.Errorf("runs do not span the window: %+v", runs)
	}
	for i, ru := range runs {
		if !ru.From.Before(ru.To) {
			t.Errorf("run %d is empty: %+v", i, ru)
		}
		if i > 0 && !runs[i-1].To.Equal(ru.From) {
			t.Errorf("gap between run %d and %d", i-1, i)
		}
	}
}

func TestRank(t *testing.T) {
	order := []string{StateNoData, StateRemoved, StateHealthy, StateStarting, StateRestarting, StateUnhealthy, StateExited}
	want := []int{0, 1, 2, 3, 4, 5, 6}
	for i, s := range order {
		if got := Rank(s); got != want[i] {
			t.Errorf("Rank(%s) = %d, want %d", s, got, want[i])
		}
	}
	if Rank(StateRunning) != Rank(StateHealthy) {
		t.Error("running and healthy must rank equal")
	}
}
