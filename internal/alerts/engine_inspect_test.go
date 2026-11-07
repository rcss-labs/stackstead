package alerts_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/rcss-labs/stackstead/internal/engine"
)

type fakeInspector struct {
	mu      sync.Mutex
	by      map[string]engine.ContainerInspect
	err     error
	inspect []string
}

func (f *fakeInspector) InspectContainer(_ context.Context, id string) (engine.ContainerInspect, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspect = append(f.inspect, id)
	if f.err != nil {
		return engine.ContainerInspect{}, f.err
	}
	return f.by[id], nil
}

func TestAlertCarriesExitCodeAndLimit(t *testing.T) {
	r := newRig()
	insp := &fakeInspector{by: map[string]engine.ContainerInspect{
		"c1": {ExitCode: 137, MemoryLimit: 256 << 20},
		"c2": {ExitCode: 0, MemoryLimit: 0},
		"c3": {ExitCode: 1},
	}}
	r.engine.SetInspector(insp)
	r.restarts["c2"] = 5
	r.cont.set(
		ctr("c1", "s-web-1", "s", "web", "exited", "Exited (137) 1 minute ago"),
		ctr("c2", "s-web-2", "s", "web", "running", "Up 5 seconds"),
		ctr("c3", "s-web-3", "s", "web", "running", "Up 1 minute (unhealthy)"),
	)
	byKind := map[string]int{}
	for _, a := range r.get(t) {
		byKind[a.Kind]++
		switch a.Kind {
		case "exited":
			if a.ExitCode == nil || *a.ExitCode != 137 || a.MemLimit != 256<<20 {
				t.Errorf("exited alert = %+v", a)
			}
		case "restart_loop":
			if a.ExitCode == nil || *a.ExitCode != 0 || a.MemLimit != 0 {
				t.Errorf("restart_loop alert = %+v", a)
			}
		default:
			if a.ExitCode != nil || a.MemLimit != 0 {
				t.Errorf("%s alert must not carry exit data: %+v", a.Kind, a)
			}
		}
	}
	if byKind["exited"] != 1 || byKind["restart_loop"] != 1 || byKind["unhealthy"] != 1 {
		t.Fatalf("kinds = %v", byKind)
	}
	for _, id := range insp.inspect {
		if id == "c3" {
			t.Error("inspected a container that has neither exited nor restart_loop alerts")
		}
	}
}

func TestAlertsSurviveInspectFailure(t *testing.T) {
	r := newRig()
	r.engine.SetInspector(&fakeInspector{err: errors.New("docker down")})
	r.cont.set(ctr("c1", "s-web-1", "s", "web", "exited", "Exited (1) 1 minute ago"))
	got := r.get(t)
	if len(got) != 1 || got[0].Kind != "exited" || got[0].ExitCode != nil || got[0].MemLimit != 0 {
		t.Fatalf("got %+v", got)
	}
}
