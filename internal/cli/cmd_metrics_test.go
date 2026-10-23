package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

func t9F(v float64) *float64 { return &v }

// t9API serves the metrics history endpoint with whatever fn returns.
func t9API(t *testing.T, fn func(r *http.Request) any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fn(r))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestMetricsHistoryTable(t *testing.T) {
	oldLocal := time.Local
	time.Local = time.FixedZone("T9", 5*3600+1800) // non-UTC, so a missing local conversion shows
	t.Cleanup(func() { time.Local = oldLocal })
	var query string
	srv := t9API(t, func(r *http.Request) any {
		query = r.URL.RawQuery
		return apitypes.MetricsHistory{Points: []apitypes.MetricPoint{
			{Time: "2026-10-14T11:00:00Z", Service: "web", CPU: 12.34, Mem: 3 << 20, MemLimit: 1 << 30},
			{Time: "2026-10-14T11:00:10Z", Service: "web", CPU: 20, Mem: 4 << 20, MemLimit: 1 << 30,
				NetRx: t9F(2048), NetTx: t9F(0), BlkRead: t9F(1 << 20), BlkWrite: t9F(512)},
		}}
	})
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	if err := t9Run(context.Background(), te, "metrics", "demo", "--service", "web", "--step", "30s"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(te.Out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("output = %q", te.Out.String())
	}
	header := lines[0]
	for _, col := range []string{"TIME", "SERVICE", "CPU (AVG)", "MEM (AVG)", "LIMIT",
		"NET RX/S (TOTAL)", "NET TX/S (TOTAL)", "BLK R/S (TOTAL)", "BLK W/S (TOTAL)"} {
		if !strings.Contains(header, col) {
			t.Errorf("header %q lacks %q", header, col)
		}
	}
	if n := strings.Count(header, "(TOTAL)"); n != 4 {
		t.Errorf("(TOTAL) appears %d times, want 4", n)
	}
	tm := func(s string) string {
		ts, _ := time.Parse(time.RFC3339, s)
		return ts.Local().Format("2006-01-02 15:04:05")
	}
	first, second := strings.Fields(lines[1]), strings.Fields(lines[2])
	wantFirst := append(strings.Fields(tm("2026-10-14T11:00:00Z")), "web", "12.3%", "3.0", "MiB", "1.0", "GiB", "-", "-", "-", "-")
	if strings.Join(first, " ") != strings.Join(wantFirst, " ") {
		t.Errorf("first row = %q, want %q", first, wantFirst)
	}
	wantSecond := append(strings.Fields(tm("2026-10-14T11:00:10Z")), "web", "20.0%", "4.0", "MiB", "1.0", "GiB",
		"2.0", "KiB/s", "0", "B/s", "1.0", "MiB/s", "512", "B/s")
	if strings.Join(second, " ") != strings.Join(wantSecond, " ") {
		t.Errorf("second row = %q, want %q", second, wantSecond)
	}
	if !strings.Contains(query, "service=web") || !strings.Contains(query, "step=30") {
		t.Errorf("query = %q", query)
	}
}

func TestMetricsHistoryJSONKeepsWireShape(t *testing.T) {
	srv := t9API(t, func(r *http.Request) any {
		return apitypes.MetricsHistory{Points: []apitypes.MetricPoint{{Time: "2026-10-14T11:00:00Z", Service: "web", CPU: 1}}}
	})
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	if err := t9Run(context.Background(), te, "metrics", "demo", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	var out apitypes.MetricsHistory
	if err := json.Unmarshal([]byte(te.Out.String()), &out); err != nil || len(out.Points) != 1 || out.Points[0].Time != "2026-10-14T11:00:00Z" {
		t.Errorf("out = %q (%v)", te.Out.String(), err)
	}
}

func TestMetricsFromDuration(t *testing.T) {
	var from, to string
	srv := t9API(t, func(r *http.Request) any {
		from, to = r.URL.Query().Get("from"), r.URL.Query().Get("to")
		return apitypes.MetricsHistory{}
	})
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	if err := t9Run(context.Background(), te, "metrics", "demo", "--from", "30m"); err != nil {
		t.Fatal(err)
	}
	got, err := time.Parse(time.RFC3339, from)
	if err != nil {
		t.Fatalf("from = %q: %v", from, err)
	}
	if d := got.Sub(te.Now().Add(-30 * time.Minute)); d < -time.Second || d > time.Second {
		t.Errorf("from = %s, want about now-30m (%s)", got, te.Now().Add(-30*time.Minute))
	}
	if to != "" {
		t.Errorf("to = %q, want none", to)
	}
}

func TestMetricsFromToAcceptRFC3339AndUnix(t *testing.T) {
	var from, to string
	srv := t9API(t, func(r *http.Request) any {
		from, to = r.URL.Query().Get("from"), r.URL.Query().Get("to")
		return apitypes.MetricsHistory{}
	})
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	if err := t9Run(context.Background(), te, "metrics", "demo", "--from", "1700000000", "--to", "2023-11-14T23:00:00+01:00"); err != nil {
		t.Fatal(err)
	}
	if from != "2023-11-14T22:13:20Z" || to != "2023-11-14T22:00:00Z" {
		t.Errorf("from=%q to=%q", from, to)
	}
}

func TestMetricsRejectsBadInput(t *testing.T) {
	for _, args := range [][]string{
		{"--from", "yesterday"}, {"--to", "5m"}, {"--step", "fast"}, {"--step", "-5s"},
		{"--from", "99999999d"}, {"--live", "--from", "5m"}, {"--live", "--step", "5s"},
	} {
		te := newTestEnv(t, nil)
		t9Seed(t, te, "http://127.0.0.1:1")
		err := t9Run(context.Background(), te, append([]string{"metrics", "demo"}, args...)...)
		if codeFor(err) != ExitUsage {
			t.Errorf("%v: code = %d, err = %v", args, codeFor(err), err)
		}
	}
}

func TestMetricsLiveRows(t *testing.T) {
	s := func(ctr string, cpu float64, mem, rx uint64) apitypes.Sample {
		return apitypes.Sample{Time: "2026-10-14T11:00:00Z", Service: "web", Container: ctr, CPU: cpu,
			Mem: mem, MemLimit: 1 << 30, NetRx: rx, NetTx: 10, BlkRead: 1 << 20, BlkWrite: 0}
	}
	srv := t9Serve(t,
		t9Conn{Frames: []string{
			t9Ev("sample", "", s("aaaaaaaaaaaa", 5.5, 2<<20, 4096)),
			t9Ev("dropped", "", struct{}{}),
		}},
		t9Conn{Frames: []string{t9Ev("sample", "", s("bbbbbbbbbbbb", 7, 3<<20, 8192))}, Hold: true},
	)
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- t9Run(ctx, te, "metrics", "demo", "--live") }()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(te.Out.String(), "bbbbbbbbbbbb") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	err := <-done
	if codeFor(err) != ExitInterrupted {
		t.Errorf("code = %d, err = %v", codeFor(err), err)
	}
	out := te.Out.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("output = %q", out)
	}
	if !strings.Contains(lines[0], "NET RX (TOTAL)") || !strings.Contains(lines[0], "CONTAINER") {
		t.Errorf("header = %q (live counters are cumulative totals, not rates)", lines[0])
	}
	if f := strings.Join(strings.Fields(lines[1]), " "); !strings.Contains(f, "web aaaaaaaaaaaa 5.5% 2.0 MiB 1.0 GiB 4.0 KiB 10 B 1.0 MiB 0 B") {
		t.Errorf("row 1 = %q", f)
	}
	if !strings.Contains(lines[2], "bbbbbbbbbbbb") || !strings.Contains(lines[2], "8.0 KiB") {
		t.Errorf("row 2 = %q", lines[2])
	}
	if reqs := srv.Requests(); len(reqs) != 2 || reqs[0].Path != "/api/stacks/demo/metrics/live" {
		t.Errorf("requests = %+v (dropped must reconnect)", reqs)
	}
}

func TestMetricsLiveJSON(t *testing.T) {
	srv := t9Serve(t, t9Conn{Frames: []string{
		t9Ev("sample", "", apitypes.Sample{Time: "2026-10-14T11:00:00Z", Service: "web", Container: "aaaaaaaaaaaa", CPU: 1}),
	}, Hold: true})
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- t9Run(ctx, te, "metrics", "demo", "--live", "-o", "json") }()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(te.Out.String(), "\n") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	var v struct {
		Event string          `json:"event"`
		Data  apitypes.Sample `json:"data"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(te.Out.String())), &v); err != nil || v.Event != "sample" || v.Data.Container != "aaaaaaaaaaaa" {
		t.Errorf("out = %q (%v)", te.Out.String(), err)
	}
}

func TestMetricsLiveServiceFilter(t *testing.T) {
	for _, mode := range []string{"table", "json"} {
		t.Run(mode, func(t *testing.T) {
			s := func(svc, ctr string) string {
				return t9Ev("sample", "", apitypes.Sample{Time: "2026-10-14T11:00:00Z", Service: svc, Container: ctr, CPU: 1})
			}
			srv := t9Serve(t, t9Conn{Frames: []string{s("web", "webcontainer1"), s("db", "dbcontainer01"), s("web", "webcontainer2")}, Hold: true})
			te := newTestEnv(t, nil)
			t9Seed(t, te, srv.URL)
			args := []string{"metrics", "demo", "--live", "--service", "web"}
			if mode == "json" {
				args = append(args, "-o", "json")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- t9Run(ctx, te, args...) }()
			deadline := time.Now().Add(5 * time.Second)
			for !strings.Contains(te.Out.String(), "webcontainer2") && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			cancel()
			<-done
			out := te.Out.String()
			if !strings.Contains(out, "webcontainer1") || !strings.Contains(out, "webcontainer2") {
				t.Errorf("web samples missing: %q", out)
			}
			if strings.Contains(out, "dbcontainer01") {
				t.Errorf("--service web leaked another service: %q", out)
			}
		})
	}
}

func TestEventsLines(t *testing.T) {
	srv := t9Serve(t, t9Conn{Frames: []string{
		t9Ev("status", "", apitypes.StatusFlags{Events: "connected", MetricsDegraded: false}),
		t9Ev("change", "", apitypes.Change{Project: "demo", Seq: 7}),
		t9Ev("status", "", apitypes.StatusFlags{Events: "disconnected", MetricsDegraded: true}),
	}, Hold: true})
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- t9Run(ctx, te, "events") }()
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(te.Out.String(), "\n") < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	stamp := te.Now().Local().Format("2006-01-02 15:04:05")
	want := stamp + "  status events=connected metrics=ok\n" +
		stamp + "  change demo seq=7\n" +
		stamp + "  status events=disconnected metrics=degraded\n"
	if te.Out.String() != want {
		t.Errorf("stdout = %q, want %q", te.Out.String(), want)
	}
}

func TestEventsJSON(t *testing.T) {
	srv := t9Serve(t, t9Conn{Frames: []string{
		t9Ev("change", "", apitypes.Change{Project: "demo", Seq: 7}),
	}, Hold: true})
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- t9Run(ctx, te, "events", "-o", "json") }()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(te.Out.String(), "\n") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	var v struct {
		Event string          `json:"event"`
		Data  apitypes.Change `json:"data"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(te.Out.String())), &v); err != nil || v.Event != "change" || v.Data.Seq != 7 {
		t.Errorf("out = %q (%v)", te.Out.String(), err)
	}
}

func TestEventsCtrlCExits130(t *testing.T) {
	srv := t9Serve(t, t9Conn{Frames: []string{
		t9Ev("status", "", apitypes.StatusFlags{Events: "connected"}),
	}, Hold: true})
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- t9Run(ctx, te, "events") }()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(te.Out.String(), "\n") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if codeFor(err) != ExitInterrupted {
			t.Errorf("code = %d, err = %v", codeFor(err), err)
		}
		if msg := errString(err); msg != "" {
			t.Errorf("error text %q, want none", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("events did not stop after cancel")
	}
	if te.Err.String() != "" {
		t.Errorf("stderr = %q", te.Err.String())
	}
}

func TestMetricsLiveHeaderAlignsWithColour(t *testing.T) {
	srv := t9Serve(t, t9Conn{Frames: []string{
		t9Ev("sample", "", apitypes.Sample{Time: "2026-10-14T11:00:00Z", Service: "web", Container: "myproject-web-app-1", CPU: 1, Mem: 1 << 20, MemLimit: 2 << 30}),
	}, Hold: true})
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	te.Stdout = f
	te.IsTerminal = func(uintptr) bool { return true }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- t9Run(ctx, te, "metrics", "demo", "--live") }()
	var lines []string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		b, _ := os.ReadFile(f.Name())
		if lines = strings.Split(strings.TrimRight(string(b), "\n"), "\n"); len(lines) >= 2 {
			break
		}
	}
	cancel()
	<-done
	if len(lines) < 2 {
		t.Fatalf("lines = %q", lines)
	}
	if !strings.Contains(lines[0], "\x1b[") {
		t.Fatalf("header should be coloured: %q", lines[0])
	}
	header, row := stripANSI(lines[0]), stripANSI(lines[1])
	for _, col := range []string{"SERVICE", "CONTAINER", "CPU", "MEM", "LIMIT"} {
		// Each column starts where the matching cell of the row starts.
		cell := map[string]string{"SERVICE": "web", "CONTAINER": "myproject-web-app-1", "CPU": "1.0%", "MEM": "1.0 MiB", "LIMIT": "2.0 GiB"}[col]
		if strings.Index(header, col) != strings.Index(row, cell) {
			t.Errorf("column %s at %d in header but %d in row:\n%s\n%s", col, strings.Index(header, col), strings.Index(row, cell), header, row)
		}
	}
}

func TestMetricsAndEventsNamesCannotForgeLines(t *testing.T) {
	forged := "web] real\n[db\x1b[2J"
	srv := t9API(t, func(r *http.Request) any {
		return apitypes.MetricsHistory{Points: []apitypes.MetricPoint{{Time: "2026-10-14T11:00:00Z", Service: forged}}}
	})
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	if err := t9Run(context.Background(), te, "metrics", "demo"); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(te.Out.String(), "\n"); n != 2 || strings.Contains(te.Out.String(), "\x1b") {
		t.Errorf("history output = %q", te.Out.String())
	}

	live := t9Serve(t, t9Conn{Frames: []string{
		t9Ev("sample", "", apitypes.Sample{Time: "2026-10-14T11:00:00Z", Service: forged, Container: forged}),
	}, Hold: true})
	te = newTestEnv(t, nil)
	t9Seed(t, te, live.URL)
	t9Stream(t, te, "\n", "metrics", "demo", "--live")
	if n := strings.Count(te.Out.String(), "\n"); n != 2 || strings.Contains(te.Out.String(), "\x1b") {
		t.Errorf("live output = %q", te.Out.String())
	}

	ev := t9Serve(t, t9Conn{Frames: []string{
		t9Ev("status", "", apitypes.StatusFlags{Events: "up\n1\x1b[2J"}),
		t9Ev("change", "", apitypes.Change{Project: forged, Seq: 1}),
	}, Hold: true})
	te = newTestEnv(t, nil)
	t9Seed(t, te, ev.URL)
	t9Stream(t, te, "seq=1\n", "events")
	out := te.Out.String()
	if n := strings.Count(out, "\n"); n != 2 || strings.Contains(out, "\x1b") {
		t.Errorf("events output = %q", out)
	}
}

// t9Stream runs a streaming command until stdout contains want, then cancels it.
func t9Stream(t *testing.T, te *testEnv, want string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- t9Run(ctx, te, args...) }()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline) && !strings.Contains(te.Out.String(), want); time.Sleep(5 * time.Millisecond) {
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
}

func TestMetricsLiveTruncatesVeryLongContainerNames(t *testing.T) {
	long := "an-extremely-long-project-name-web-1"
	var b strings.Builder
	liveRow(&b, "t", "svc", long, "1.0%")
	got := strings.Fields(b.String())[2]
	if !strings.HasSuffix(got, "…") || len([]rune(got)) != 28 {
		t.Errorf("container cell = %q, want 28 runes ending in an ellipsis", got)
	}
	b.Reset()
	liveRow(&b, "t", "svc", "myproject-web-app-1", "1.0%")
	if !strings.Contains(b.String(), "myproject-web-app-1  ") {
		t.Errorf("a typical name was cut: %q", b.String())
	}
}
