package images

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/imageref"
)

func keptIDs(keep map[string]bool) []string {
	var out []string
	for id, ok := range keep {
		if ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func daysAgo(now time.Time, days int) time.Time {
	return now.Add(-time.Duration(days) * 24 * time.Hour)
}

func TestKeepTable(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	// Listed oldest first on purpose: Keep ranks by StartedAt, not by position.
	versions := []Version{
		{ID: "g", Status: VersionBuilding, StartedAt: daysAgo(now, 90)},
		{ID: "f", Status: VersionSucceeded, StartedAt: daysAgo(now, 60)},
		{ID: "e", Status: VersionSucceeded, StartedAt: daysAgo(now, 40)},
		{ID: "d", Status: VersionSucceeded, StartedAt: daysAgo(now, 20)},
		{ID: "c", Status: VersionSucceeded, StartedAt: daysAgo(now, 10)},
		{ID: "b", Status: VersionFailed, StartedAt: daysAgo(now, 25)},
		{ID: "a", Status: VersionSucceeded, StartedAt: daysAgo(now, 1)},
	}
	cases := []struct {
		name   string
		policy Retention
		want   []string
	}{
		{"keep last 3", Retention{KeepLast: 3}, []string{"a", "c", "d", "g"}},
		{"keep 30 days", Retention{KeepDays: 30}, []string{"a", "b", "c", "d", "g"}},
		{"either limit keeps", Retention{KeepLast: 3, KeepDays: 30}, []string{"a", "b", "c", "d", "g"}},
		{"no limit keeps all", Retention{}, []string{"a", "b", "c", "d", "e", "f", "g"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := keptIDs(Keep(c.policy, versions, now)); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("kept %v, want %v", got, c.want)
			}
		})
	}
}

func TestKeepFailedBurstDoesNotEvictGoodVersions(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	versions := []Version{
		{ID: "s1", Status: VersionSucceeded, StartedAt: daysAgo(now, 10)},
		{ID: "s2", Status: VersionSucceeded, StartedAt: daysAgo(now, 9)},
		{ID: "old-fail", Status: VersionFailed, StartedAt: daysAgo(now, 20)},
	}
	for i := 0; i < 5; i++ {
		versions = append(versions, Version{ID: "f" + string(rune('1'+i)), Status: VersionFailed, StartedAt: daysAgo(now, 4-i/2)})
	}
	got := keptIDs(Keep(Retention{KeepLast: 2}, versions, now))
	want := []string{"f1", "f2", "f3", "f4", "f5", "s1", "s2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
}

func TestKeepAlwaysKeepsNewestSucceeded(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	old := []Version{
		{ID: "a", Status: VersionSucceeded, StartedAt: daysAgo(now, 5)},
		{ID: "b", Status: VersionSucceeded, StartedAt: daysAgo(now, 6)},
		{ID: "c", Status: VersionFailed, StartedAt: daysAgo(now, 7)},
	}
	if got := keptIDs(Keep(Retention{KeepDays: 1}, old, now)); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("keep-days only kept %v, want [a]", got)
	}
	// A newer failed row takes rank 1; the newest succeeded one must survive anyway.
	failedFirst := append([]Version{{ID: "x", Status: VersionFailed, StartedAt: daysAgo(now, 4)}}, old...)
	if got := keptIDs(Keep(Retention{KeepLast: 1}, failedFirst, now)); !reflect.DeepEqual(got, []string{"a", "x"}) {
		t.Fatalf("keep-last 1 kept %v, want [a x]", got)
	}
}

func (h *harness) setRetention(t *testing.T, slug string, r Retention) {
	t.Helper()
	img, err := h.svc.Get(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	img.Retention = r
	if err := h.svc.store.UpdateImage(context.Background(), img); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) tags(t *testing.T, slug string) []string {
	t.Helper()
	img, _ := h.svc.Get(context.Background(), slug)
	vs, err := h.svc.store.Versions(context.Background(), img.ID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, v := range vs {
		out = append(out, v.Tag)
	}
	sort.Strings(out)
	return out
}

func libRef(tag string) string { return "stackstead.invalid/" + testOwner + "/app:" + tag }

func TestRetentionAfterBuildRemovesOldest(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	h.setRetention(t, "app", Retention{KeepLast: 2})
	first := h.build(t, "app", "1", nil)
	h.build(t, "app", "2", nil)
	logFile := filepath.Join(h.dir, "app", "logs", first.ID+".log")
	if _, err := os.Stat(logFile); err != nil {
		t.Fatalf("log missing before retention: %v", err)
	}
	h.build(t, "app", "3", nil)

	if got := h.tags(t, "app"); !reflect.DeepEqual(got, []string{"2", "3"}) {
		t.Fatalf("tags = %v", got)
	}
	if h.eng.present[libRef("1")] || !h.eng.present[libRef("2")] || !h.eng.present[libRef("3")] {
		t.Fatalf("docker tags = %v", h.eng.present)
	}
	if _, err := os.Stat(logFile); !os.IsNotExist(err) {
		t.Fatalf("log file left: %v", err)
	}
	e, ok := h.log.last("image.retain")
	if !ok || e.Username != "alice" || e.Detail != "1" || e.Scope != "app" {
		t.Fatalf("retain log = %+v ok=%v", e, ok)
	}
	for _, c := range h.eng.engineRemoved() {
		if strings.Contains(c, ":2") || strings.Contains(c, ":3") {
			t.Fatalf("removed a kept version: %s", c)
		}
	}
}

func TestRetentionDoesNotFailBuildOnError(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	h.setRetention(t, "app", Retention{KeepLast: 1})
	h.build(t, "app", "1", nil)
	h.svc.SetUsage(panicUsage{})
	var buf bytes.Buffer
	h.svc.log = slog.New(slog.NewTextHandler(&buf, nil))
	v := h.build(t, "app", "2", nil)
	if v.Status != VersionSucceeded {
		t.Fatalf("build status = %s", v.Status)
	}
	if !strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("no warning logged: %s", buf.String())
	}
}

func TestRetentionKeepsInUse(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	for _, tag := range []string{"1", "2", "3"} {
		h.build(t, "app", tag, nil)
	}
	h.usage.usages = []imageref.Usage{{Stack: "web", Service: "api", Ref: imageref.Ref{Owner: testOwner, Slug: "app", Tag: "1"}}}
	h.setRetention(t, "app", Retention{KeepLast: 1})

	res, err := h.svc.ApplyRetention(context.Background(), SweepUser, "app")
	if err != nil || res.Skipped != "" {
		t.Fatalf("res %+v err %v", res, err)
	}
	if got := h.tags(t, "app"); !reflect.DeepEqual(got, []string{"1", "3"}) {
		t.Fatalf("tags = %v", got)
	}
	if !h.eng.present[libRef("1")] || h.eng.present[libRef("2")] {
		t.Fatalf("docker tags = %v", h.eng.present)
	}
	if len(res.Removed) != 1 || res.Removed[0].Tag != "2" || res.Kept != 2 {
		t.Fatalf("result = %+v", res)
	}
	if e, ok := h.log.last("image.retain"); !ok || e.Username != "system" || e.Detail != "2" {
		t.Fatalf("retain log = %+v", e)
	}
}

func TestRetentionSkipsWhenPeerUnreachable(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	h.build(t, "app", "1", nil)
	h.build(t, "app", "2", nil)
	h.setRetention(t, "app", Retention{KeepLast: 1})
	hung := newFakePeer(t, peerDID, "Host D")
	hung.hanging.Store(true)
	h.withPeers(t, 200*time.Millisecond, hung)
	var buf bytes.Buffer
	h.svc.log = slog.New(slog.NewTextHandler(&buf, nil))

	res, err := h.svc.ApplyRetention(context.Background(), SweepUser, "app")
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != "Host D" || len(res.Removed) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if got := h.tags(t, "app"); !reflect.DeepEqual(got, []string{"1", "2"}) {
		t.Fatalf("tags = %v", got)
	}
	if rm := h.eng.engineRemoved(); len(rm) != 0 {
		t.Fatalf("docker removals: %v", rm)
	}
	if _, ok := h.log.last("image.retain"); ok {
		t.Fatal("retain logged")
	}
	if out := buf.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "Host D") {
		t.Fatalf("warn missing: %s", out)
	}
}

func TestRetentionTreatsOldPeerAsUnused(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	h.build(t, "app", "1", nil)
	h.build(t, "app", "2", nil)
	h.setRetention(t, "app", Retention{KeepLast: 1})
	old := newFakePeer(t, peerCID, "Host C")
	old.status, old.jsonErr = http.StatusNotFound, true
	h.withPeers(t, time.Second, old)

	res, err := h.svc.ApplyRetention(context.Background(), SweepUser, "app")
	if err != nil || res.Skipped != "" || len(res.Removed) != 1 {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestRetentionUsedOnPeerSurvives(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	h.build(t, "app", "1", nil)
	h.build(t, "app", "2", nil)
	h.setRetention(t, "app", Retention{KeepLast: 1})
	good := newFakePeer(t, peerBID, "Host B")
	good.usage = []apitypes.LibraryUsage{{Ref: libRef("1"), Stack: "web", Service: "api"}}
	h.withPeers(t, time.Second, good)

	res, err := h.svc.ApplyRetention(context.Background(), SweepUser, "app")
	if err != nil || len(res.Removed) != 0 || res.Kept != 2 {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestRetentionNeverTouchesRevisions(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	img := h.create(t, "app")
	for _, tag := range []string{"1", "2", "3"} {
		h.build(t, "app", tag, nil)
	}
	before, _ := h.svc.store.Revisions(ctx, img.ID)
	h.setRetention(t, "app", Retention{KeepLast: 1})
	if res, err := h.svc.ApplyRetention(ctx, SweepUser, "app"); err != nil || len(res.Removed) != 2 {
		t.Fatalf("res %+v err %v", res, err)
	}
	after, _ := h.svc.store.Revisions(ctx, img.ID)
	if len(before) == 0 || len(after) != len(before) {
		t.Fatalf("revisions %d -> %d", len(before), len(after))
	}
}

func TestRetentionKeepDaysNeverDeletesEverything(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	h.build(t, "app", "1", nil)
	h.build(t, "app", "2", nil)
	h.setRetention(t, "app", Retention{KeepDays: 1})
	h.svc.now = func() time.Time { return time.Now().Add(365 * 24 * time.Hour) }

	res, err := h.svc.ApplyRetention(context.Background(), SweepUser, "app")
	if err != nil || len(res.Removed) != 1 {
		t.Fatalf("res %+v err %v", res, err)
	}
	if got := h.tags(t, "app"); !reflect.DeepEqual(got, []string{"2"}) {
		t.Fatalf("tags = %v", got)
	}
}

func TestRetentionRemovesFailedByTheSameRule(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	h.eng.buildErr = &engine.BuildError{Message: "boom"}
	failed := h.build(t, "app", "1", nil)
	h.eng.buildErr = nil
	h.build(t, "app", "2", nil)
	h.build(t, "app", "3", nil)
	h.setRetention(t, "app", Retention{KeepLast: 1})
	res, err := h.svc.ApplyRetention(context.Background(), SweepUser, "app")
	if err != nil || len(res.Removed) != 2 {
		t.Fatalf("res %+v err %v", res, err)
	}
	if _, err := h.svc.store.VersionByID(context.Background(), failed.ID); err != ErrVersionNotFound {
		t.Fatalf("failed row still there: %v", err)
	}
	if !h.eng.present[libRef("3")] {
		t.Fatal("newest version removed")
	}
}

func TestDeleteVersionInUseLists(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	v := h.build(t, "app", "1", nil)
	h.usage.usages = []imageref.Usage{{Stack: "web", Service: "api", Ref: imageref.Ref{Owner: testOwner, Slug: "app", Tag: "1"}}}
	err := h.svc.DeleteVersion(context.Background(), "alice", "app", v.ID)
	if err == nil || !strings.Contains(err.Error(), "web/api on Host A") {
		t.Fatalf("err = %v", err)
	}
}

type panicUsage struct{}

func (panicUsage) LibraryUsage(context.Context) ([]imageref.Usage, error) { panic("usage boom") }

func TestSweepSchedule(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	h.create(t, "other")
	for _, tag := range []string{"1", "2"} {
		h.build(t, "app", tag, nil)
		h.build(t, "other", tag, nil)
	}
	h.setRetention(t, "app", Retention{KeepLast: 1})

	asked := make(chan time.Duration)
	fire := make(chan time.Time)
	sw := &Sweeper{Service: h.svc, After: func(d time.Duration) <-chan time.Time { asked <- d; return fire }}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { sw.Run(ctx); close(finished) }()

	expect := func(want time.Duration) {
		t.Helper()
		select {
		case d := <-asked:
			if d != want {
				t.Fatalf("After(%v), want %v", d, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("After(%v) never asked", want)
		}
	}
	expect(5 * time.Minute)
	fire <- time.Now()
	expect(6 * time.Hour) // asked again only once the sweep has finished
	if got := h.tags(t, "app"); !reflect.DeepEqual(got, []string{"2"}) {
		t.Fatalf("app tags = %v", got)
	}
	if got := h.tags(t, "other"); !reflect.DeepEqual(got, []string{"1", "2"}) {
		t.Fatalf("image without a policy was pruned: %v", got)
	}
	if e, ok := h.log.last("image.retain"); !ok || e.Username != "system" {
		t.Fatalf("retain log = %+v", e)
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}

func TestSweepSurvivesPanic(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	h.build(t, "app", "1", nil)
	h.build(t, "app", "2", nil)
	h.setRetention(t, "app", Retention{KeepLast: 1})
	h.svc.SetUsage(panicUsage{})

	asked := make(chan time.Duration)
	fire := make(chan time.Time)
	var buf bytes.Buffer
	h.svc.log = slog.New(slog.NewTextHandler(&buf, nil))
	sw := &Sweeper{Service: h.svc,
		After: func(d time.Duration) <-chan time.Time { asked <- d; return fire }}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { sw.Run(ctx); close(finished) }()
	<-asked
	fire <- time.Now()
	if d := <-asked; d != 6*time.Hour {
		t.Fatalf("Run stopped after a panic: After(%v)", d)
	}
	cancel()
	<-finished
	if !strings.Contains(buf.String(), "level=WARN") || !strings.Contains(buf.String(), "panicked") {
		t.Fatalf("panic not logged: %s", buf.String())
	}
}

func TestRetentionRemovingFailedRowKeepsSameTagSucceeded(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	h.eng.buildErr = &engine.BuildError{Message: "boom"}
	failed := h.build(t, "app", "1", nil)
	h.eng.buildErr = nil
	good := h.build(t, "app", "1", nil)
	h.build(t, "app", "2", nil)
	h.setRetention(t, "app", Retention{KeepLast: 2})

	res, err := h.svc.ApplyRetention(context.Background(), SweepUser, "app")
	if err != nil || len(res.Removed) != 1 || res.Removed[0].ID != failed.ID {
		t.Fatalf("res %+v err %v", res, err)
	}
	if !h.eng.present[libRef("1")] || len(h.eng.engineRemoved()) != 0 {
		t.Fatalf("succeeded 1 was untagged: present=%v removed=%v", h.eng.present, h.eng.engineRemoved())
	}
	if _, err := h.svc.store.VersionByID(context.Background(), good.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionDeferredWhenImageBusy(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	h.build(t, "app", "1", nil)
	h.build(t, "app", "2", nil)
	h.setRetention(t, "app", Retention{KeepLast: 1})
	release := make(chan struct{})
	if _, err := h.ops.StartTimeout(OpKey(testOwner, "app"), "push", time.Minute, func(ctx context.Context, _ func(string)) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	defer close(release)

	res, err := h.svc.ApplyRetention(context.Background(), SweepUser, "app")
	if err != nil || len(res.Removed) != 0 || res.Skipped != "" {
		t.Fatalf("res %+v err %v", res, err)
	}
	if got := h.tags(t, "app"); !reflect.DeepEqual(got, []string{"1", "2"}) {
		t.Fatalf("tags = %v", got)
	}
}

func TestRetentionUntagFailureKeepsRowAndWarns(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	h.build(t, "app", "1", nil)
	h.build(t, "app", "2", nil)
	h.setRetention(t, "app", Retention{KeepLast: 1})
	h.eng.removeFn = func(context.Context, string) error { return errors.New("daemon down") }
	var buf bytes.Buffer
	h.svc.log = slog.New(slog.NewTextHandler(&buf, nil))

	res, err := h.svc.ApplyRetention(context.Background(), SweepUser, "app")
	if err != nil || len(res.Removed) != 0 {
		t.Fatalf("res %+v err %v", res, err)
	}
	if got := h.tags(t, "app"); !reflect.DeepEqual(got, []string{"1", "2"}) {
		t.Fatalf("tags = %v", got)
	}
	if _, ok := h.log.last("image.retain"); ok {
		t.Fatal("retain logged for a failed removal")
	}
	if !strings.Contains(buf.String(), "level=WARN") || !strings.Contains(buf.String(), "daemon down") {
		t.Fatalf("warn missing: %s", buf.String())
	}
}

func TestRetentionUntagBlockedDoesNotHoldImageLockAndHonoursCtx(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	h.build(t, "app", "1", nil)
	h.build(t, "app", "2", nil)
	h.setRetention(t, "app", Retention{KeepLast: 1})
	started := make(chan struct{})
	h.eng.removeFn = func(ctx context.Context, _ string) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := h.svc.ApplyRetention(ctx, SweepUser, "app")
		finished <- err
	}()
	<-started

	updated := make(chan error, 1)
	go func() {
		_, err := h.svc.Update(context.Background(), "bob", "app", UpdateRequest{Name: "Renamed", BuildArgs: map[string]string{"A": "1"}, Retention: Retention{KeepLast: 1}})
		updated <- err
	}()
	select {
	case err := <-updated:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("image lock is held while the untag blocks")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("ApplyRetention did not return after ctx was cancelled")
	}
	if got := h.tags(t, "app"); !reflect.DeepEqual(got, []string{"1", "2"}) {
		t.Fatalf("tags = %v", got)
	}
}

func TestRunExclusiveReturnsPanic(t *testing.T) {
	h := newHarness(t)
	err := h.svc.runExclusive(context.Background(), "app", testOwner, func(context.Context) error { panic("boom") })
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	want := errors.New("plain")
	if err := h.svc.runExclusive(context.Background(), "app", testOwner, func(context.Context) error { return want }); !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
}
