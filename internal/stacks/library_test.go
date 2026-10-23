package stacks_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/imageref"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

const (
	libWeb = "stackstead.invalid/0123456789ab/web-app:1"
	libAPI = "stackstead.invalid/0123456789ab/api:2.0"
)

type fakeFetcher struct {
	mu    sync.Mutex
	calls []imageref.Ref
	err   error
	// onCall, when set, observes compose runs at fetch time.
	onCall func()
}

func (f *fakeFetcher) EnsureLocal(_ context.Context, ref imageref.Ref, out func(string)) error {
	f.mu.Lock()
	f.calls = append(f.calls, ref)
	err, hook := f.err, f.onCall
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	out("fetcher: " + ref.Tag)
	return err
}

func (f *fakeFetcher) refs() []imageref.Ref {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func libHarness(t *testing.T, fetcher stacks.LibraryFetcher) *harness {
	t.Helper()
	h := newHarness(t)
	h.svc = stacks.NewService(stacks.Deps{
		Registry: h.reg, Ops: h.ops, Compose: h.compose, Engine: h.eng,
		StacksDir: h.dir, AllowedRoots: []string{h.dir},
		Now:     func() time.Time { return fixedNow },
		Library: fetcher,
	})
	return h
}

func (h *harness) createMulti(t *testing.T, name string, services map[string]string) {
	t.Helper()
	var named []composefile.NamedService
	for _, n := range []string{"web", "db"} {
		if img, ok := services[n]; ok {
			named = append(named, composefile.NamedService{Name: n, Config: composefile.ServiceConfig{Image: img}})
		}
	}
	if _, err := h.svc.Create(context.Background(), stacks.CreateRequest{Name: name, Services: named}); err != nil {
		t.Fatalf("Create %s: %v", name, err)
	}
}

func TestLibraryUsageAcrossStacks(t *testing.T) {
	h := libHarness(t, nil)
	h.createMulti(t, "one", map[string]string{"web": libWeb, "db": "nginx"})
	h.createMulti(t, "two", map[string]string{"web": libAPI})
	got, err := h.svc.LibraryUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []imageref.Usage{
		{Stack: "one", Service: "web", Ref: imageref.Ref{Owner: "0123456789ab", Slug: "web-app", Tag: "1"}},
		{Stack: "two", Service: "web", Ref: imageref.Ref{Owner: "0123456789ab", Slug: "api", Tag: "2.0"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("usage = %+v, want %+v", got, want)
	}
}

func TestSummaryLibraryRefs(t *testing.T) {
	h := libHarness(t, nil)
	h.createMulti(t, "one", map[string]string{"web": libWeb, "db": libWeb})
	h.createMulti(t, "plain", map[string]string{"web": "nginx"})
	list, err := h.svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		switch s.Name {
		case "one":
			if !reflect.DeepEqual(s.LibraryRefs, []string{libWeb}) {
				t.Fatalf("one refs = %v", s.LibraryRefs)
			}
		case "plain":
			if len(s.LibraryRefs) != 0 {
				t.Fatalf("plain refs = %v", s.LibraryRefs)
			}
		}
	}
}

func TestUpEnsuresLibraryImages(t *testing.T) {
	f := &fakeFetcher{}
	h := libHarness(t, f)
	h.createMulti(t, "one", map[string]string{"web": libWeb, "db": "nginx"})
	f.onCall = func() {
		if len(h.compose.runs) != 0 {
			t.Error("compose ran before the library image was ensured")
		}
	}
	op, err := h.svc.Act(context.Background(), "one", "up")
	if err != nil {
		t.Fatal(err)
	}
	op = wait(t, h.ops, op.ID)
	if op.State != stacks.OpSucceeded {
		t.Fatalf("state = %s (%s)", op.State, op.Error)
	}
	if got := f.refs(); !reflect.DeepEqual(got, []imageref.Ref{{Owner: "0123456789ab", Slug: "web-app", Tag: "1"}}) {
		t.Fatalf("fetcher calls = %+v", got)
	}
	lines, _ := h.ops.Lines(op.ID)
	if out := strings.Join(lines, "\n"); !strings.Contains(out, "ensuring "+libWeb) || !strings.Contains(out, "fetcher: 1") {
		t.Fatalf("output = %q", out)
	}
	if got := h.compose.lastRun().Args; got[0] != "up" {
		t.Fatalf("compose args = %v", got)
	}
}

func TestUpFailsWhenLibraryImageUnavailable(t *testing.T) {
	f := &fakeFetcher{err: errors.New("image not built")}
	h := libHarness(t, f)
	h.createMulti(t, "one", map[string]string{"web": libWeb})
	op, err := h.svc.Act(context.Background(), "one", "up")
	if err != nil {
		t.Fatal(err)
	}
	op = wait(t, h.ops, op.ID)
	if op.State != stacks.OpFailed || !strings.Contains(op.Error, "image not built") {
		t.Fatalf("state = %s error = %q", op.State, op.Error)
	}
	if len(h.compose.runs) != 0 {
		t.Fatalf("compose ran: %v", h.compose.runs)
	}
}

func TestServiceUpEnsuresOnlyThatServicesImage(t *testing.T) {
	f := &fakeFetcher{}
	h := libHarness(t, f)
	h.createMulti(t, "one", map[string]string{"web": libWeb, "db": libAPI})
	op, err := h.svc.ActService(context.Background(), "one", "db", "up")
	if err != nil {
		t.Fatal(err)
	}
	if op = wait(t, h.ops, op.ID); op.State != stacks.OpSucceeded {
		t.Fatalf("state = %s (%s)", op.State, op.Error)
	}
	if got := f.refs(); len(got) != 1 || got[0].Slug != "api" {
		t.Fatalf("fetcher calls = %+v", got)
	}
}

func TestPullSkipsLibraryServices(t *testing.T) {
	f := &fakeFetcher{}
	h := libHarness(t, f)
	h.createMulti(t, "mixed", map[string]string{"web": libWeb, "db": "nginx"})
	op, err := h.svc.Act(context.Background(), "mixed", "pull")
	if err != nil {
		t.Fatal(err)
	}
	if op = wait(t, h.ops, op.ID); op.State != stacks.OpSucceeded {
		t.Fatalf("state = %s (%s)", op.State, op.Error)
	}
	if got := h.compose.lastRun().Args; !reflect.DeepEqual(got, []string{"pull", "--", "db"}) {
		t.Fatalf("compose args = %v", got)
	}

	h.createMulti(t, "libonly", map[string]string{"web": libWeb, "db": libAPI})
	before := len(h.compose.runs)
	op, err = h.svc.Act(context.Background(), "libonly", "pull")
	if err != nil {
		t.Fatal(err)
	}
	if op = wait(t, h.ops, op.ID); op.State != stacks.OpSucceeded {
		t.Fatalf("state = %s (%s)", op.State, op.Error)
	}
	if len(h.compose.runs) != before {
		t.Fatalf("compose ran for a library-only pull: %v", h.compose.runs[before:])
	}
}

func TestNilLibraryLeavesComposeAlone(t *testing.T) {
	h := libHarness(t, nil)
	h.createMulti(t, "one", map[string]string{"web": libWeb, "db": "nginx"})
	for action, want := range map[string][]string{
		"pull": {"pull"},
		"up":   {"up", "-d", "--build", "--remove-orphans"},
	} {
		op, err := h.svc.Act(context.Background(), "one", action)
		if err != nil {
			t.Fatal(err)
		}
		if op = wait(t, h.ops, op.ID); op.State != stacks.OpSucceeded {
			t.Fatalf("%s state = %s (%s)", action, op.State, op.Error)
		}
		if got := h.compose.lastRun().Args; !reflect.DeepEqual(got, want) {
			t.Fatalf("%s args = %v, want %v", action, got, want)
		}
	}
}
