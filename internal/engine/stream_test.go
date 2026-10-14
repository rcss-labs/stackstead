package engine

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func evJSON(typ, action, id string) string {
	return fmt.Sprintf(`{"Type":%q,"Action":%q,"Actor":{"ID":%q,"Attributes":{"com.docker.compose.project":"demo"}},"time":1760436000,"timeNano":1760436000123456789}`+"\n", typ, action, id)
}

func drainEvents(t *testing.T, evs <-chan Event, errs <-chan error) ([]Event, []error) {
	t.Helper()
	var got []Event
	var es []error
	timeout := time.After(5 * time.Second)
	for evs != nil || errs != nil {
		select {
		case e, ok := <-evs:
			if !ok {
				evs = nil
				continue
			}
			got = append(got, e)
		case e, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			es = append(es, e)
		case <-timeout:
			t.Fatal("channels did not close")
		}
	}
	return got, es
}

func TestEventsStreamThenEOF(t *testing.T) {
	var query string
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		if r.URL.Path != "/v1.41/events" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, evJSON("container", "start", "aaa"))
		io.WriteString(w, evJSON("network", "connect", "net1")) // unknown type: skipped
		io.WriteString(w, evJSON("container", "die", "bbb"))
		io.WriteString(w, evJSON("container", "stop", "ccc"))
	}))
	since := time.Unix(1760435000, 0)
	evs, errs := New(sock, 2*time.Second).Events(ctx(t), since)
	got, es := drainEvents(t, evs, errs)
	if len(got) != 3 || got[0].ContainerID != "aaa" || got[1].Action != "die" || got[2].ContainerID != "ccc" {
		t.Fatalf("events = %+v", got)
	}
	if got[0].Type != "container" || got[0].Labels["com.docker.compose.project"] != "demo" || got[0].Time.UnixNano() != 1760436000123456789 {
		t.Errorf("event fields = %+v", got[0])
	}
	if len(es) != 1 || !errors.Is(es[0], ErrUnavailable) || !errors.Is(es[0], io.EOF) {
		t.Fatalf("errs = %v", es)
	}
	if !strings.Contains(query, "since=1760435000") || !strings.Contains(query, "filters=") {
		t.Errorf("query = %q", query)
	}
}

func TestEventsCancelClosesWithoutError(t *testing.T) {
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, evJSON("container", "start", "aaa"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	c, cancel := ctxCancel(t)
	evs, errs := New(sock, 2*time.Second).Events(c, time.Time{})
	select {
	case <-evs:
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
	}
	cancel()
	_, es := drainEvents(t, evs, errs)
	if len(es) != 0 {
		t.Fatalf("errs = %v", es)
	}
}

func TestEventsUnreachable(t *testing.T) {
	evs, errs := New(filepath.Join(t.TempDir(), "none.sock"), time.Second).Events(ctx(t), time.Time{})
	got, es := drainEvents(t, evs, errs)
	if len(got) != 0 || len(es) != 1 || !errors.Is(es[0], ErrUnavailable) {
		t.Fatalf("got %v %v", got, es)
	}
}

func TestStreamsHaveNoClientTimeout(t *testing.T) {
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 8; i++ {
			io.WriteString(w, evJSON("container", "start", fmt.Sprint("c", i)))
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	evs, errs := New(sock, 300*time.Millisecond).Events(ctx(t), time.Time{}) // stream lasts ~800ms
	got, es := drainEvents(t, evs, errs)
	if len(got) != 8 {
		t.Fatalf("delivered %d of 8 events (errs %v)", len(got), es)
	}
}

func TestStreamHeaderTimeoutBoundsConnect(t *testing.T) {
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select { // never sends headers
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	start := time.Now()
	evs, errs := New(sock, 200*time.Millisecond).Events(ctx(t), time.Time{})
	_, es := drainEvents(t, evs, errs)
	if len(es) != 1 || !errors.Is(es[0], ErrUnavailable) || !errors.Is(es[0], context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("errs %v after %v", es, time.Since(start))
	}
}

func ctxCancel(t *testing.T) (context.Context, context.CancelFunc) {
	c, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return c, cancel
}

func frame(stream byte, payload string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(payload)))
	return append(h, payload...)
}

func logHandler(t *testing.T, body []byte, query *string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/containers/abc/logs") {
			http.NotFound(w, r)
			return
		}
		if query != nil {
			*query = r.URL.RawQuery
		}
		w.Write(body)
	})
}

func collectLogs(t *testing.T, lines <-chan LogLine, errs <-chan error) ([]LogLine, []error) {
	t.Helper()
	var got []LogLine
	var es []error
	timeout := time.After(5 * time.Second)
	for lines != nil || errs != nil {
		select {
		case l, ok := <-lines:
			if !ok {
				lines = nil
				continue
			}
			got = append(got, l)
		case e, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			es = append(es, e)
		case <-timeout:
			t.Fatal("log channels did not close")
		}
	}
	return got, es
}

func TestLogsMultiplexedSplitAtEveryByte(t *testing.T) {
	var all []byte
	all = append(all, frame(1, "2026-10-14T10:00:00.000000001Z hello\n2026-10-14T10:00:01Z wor")...)
	all = append(all, frame(2, "2026-10-14T10:00:02Z oops\r\n")...)
	all = append(all, frame(1, "ld\n")...)
	for split := 0; split <= len(all); split++ {
		t.Run(fmt.Sprint("split", split), func(t *testing.T) {
			sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write(all[:split])
				w.(http.Flusher).Flush()
				time.Sleep(2 * time.Millisecond)
				w.Write(all[split:])
			}))
			lines, errs := New(sock, 2*time.Second).Logs(ctx(t), "abc", LogOptions{Tail: -1})
			got, es := collectLogs(t, lines, errs)
			if len(es) != 0 {
				t.Fatalf("errs = %v", es)
			}
			want := []LogLine{
				{"stdout", time.Date(2026, 10, 14, 10, 0, 0, 1, time.UTC), "hello"},
				{"stderr", time.Date(2026, 10, 14, 10, 0, 2, 0, time.UTC), "oops"},
				{"stdout", time.Date(2026, 10, 14, 10, 0, 1, 0, time.UTC), "world"},
			}
			if len(got) != len(want) {
				t.Fatalf("got %+v", got)
			}
			for i := range want {
				if got[i].Stream != want[i].Stream || got[i].Text != want[i].Text || !got[i].Time.Equal(want[i].Time) {
					t.Errorf("line %d = %+v, want %+v", i, got[i], want[i])
				}
			}
		})
	}
}

func TestLogsTTYRaw(t *testing.T) {
	var q string
	sock := serve(t, logHandler(t, []byte("2026-10-14T10:00:00Z a\r\n2026-10-14T10:00:01Z b\n"), &q))
	lines, errs := New(sock, 2*time.Second).Logs(ctx(t), "abc", LogOptions{TTY: true, Tail: 50, Follow: true, Since: time.Unix(100, 0)})
	got, es := collectLogs(t, lines, errs)
	if len(es) != 0 || len(got) != 2 || got[0].Text != "a" || got[1].Text != "b" || got[0].Stream != "stdout" {
		t.Fatalf("got %+v %v", got, es)
	}
	for _, want := range []string{"timestamps=1", "follow=1", "tail=50", "since=100", "stdout=1", "stderr=1"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q lacks %s", q, want)
		}
	}
}

func TestLogsTailAll(t *testing.T) {
	var q string
	sock := serve(t, logHandler(t, nil, &q))
	lines, errs := New(sock, 2*time.Second).Logs(ctx(t), "abc", LogOptions{Tail: -1})
	collectLogs(t, lines, errs)
	if !strings.Contains(q, "tail=all") || strings.Contains(q, "follow=1") {
		t.Errorf("query = %q", q)
	}
}

func TestLogsPartialLastLineFlushedAtEOF(t *testing.T) {
	sock := serve(t, logHandler(t, frame(1, "2026-10-14T10:00:00Z no newline"), nil))
	lines, errs := New(sock, 2*time.Second).Logs(ctx(t), "abc", LogOptions{})
	got, _ := collectLogs(t, lines, errs)
	if len(got) != 1 || got[0].Text != "no newline" {
		t.Fatalf("got %+v", got)
	}
}

func TestLogsOverlongLineCappedOnRuneBoundary(t *testing.T) {
	long := "a" + strings.Repeat("é", 20000) // the 16 KiB cut lands mid-rune
	body := append(frame(1, "2026-10-14T10:00:00Z "+long+"\n"), frame(1, "2026-10-14T10:00:01Z next\n")...)
	sock := serve(t, logHandler(t, body, nil))
	lines, errs := New(sock, 2*time.Second).Logs(ctx(t), "abc", LogOptions{})
	got, _ := collectLogs(t, lines, errs)
	if len(got) != 2 || got[1].Text != "next" {
		t.Fatalf("got %d lines", len(got))
	}
	if len(got[0].Text) > 16<<10 || len(got[0].Text) < 16<<10-3 || !utf8.ValidString(got[0].Text) {
		t.Errorf("capped line len %d valid=%v", len(got[0].Text), utf8.ValidString(got[0].Text))
	}
}

func TestLogsOverlongTTYLineWithoutNewlineBounded(t *testing.T) {
	body := []byte("2026-10-14T10:00:00Z " + strings.Repeat("x", 1<<20))
	sock := serve(t, logHandler(t, body, nil))
	lines, errs := New(sock, 2*time.Second).Logs(ctx(t), "abc", LogOptions{TTY: true})
	got, _ := collectLogs(t, lines, errs)
	if len(got) != 1 || len(got[0].Text) != 16<<10 {
		t.Fatalf("got %d lines", len(got))
	}
}

func TestLogsLineWithoutTimestamp(t *testing.T) {
	sock := serve(t, logHandler(t, frame(1, "garbage line\n"), nil))
	lines, errs := New(sock, 2*time.Second).Logs(ctx(t), "abc", LogOptions{})
	got, _ := collectLogs(t, lines, errs)
	if len(got) != 1 || got[0].Text != "garbage line" || !got[0].Time.IsZero() {
		t.Fatalf("got %+v", got)
	}
}

func TestLogsHTTPErrorDelivered(t *testing.T) {
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"No such container"}`, 404)
	}))
	lines, errs := New(sock, 2*time.Second).Logs(ctx(t), "abc", LogOptions{})
	got, es := collectLogs(t, lines, errs)
	if len(got) != 0 || len(es) != 1 || errors.Is(es[0], ErrUnavailable) || !strings.Contains(es[0].Error(), "No such container") {
		t.Fatalf("got %v %v", got, es)
	}
}

func TestLogsCancelNoErrorNoLeak(t *testing.T) {
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(frame(1, "2026-10-14T10:00:00Z hi\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	cl := New(sock, 2*time.Second)
	if err := cl.Ping(ctx(t)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		c, cancel := ctxCancel(t)
		lines, errs := cl.Logs(c, "abc", LogOptions{Follow: true})
		<-lines
		evs, eerrs := cl.Events(c, time.Time{})
		cancel()
		// do not read lines: the producer must still exit
		_, es := collectLogs(t, lines, errs)
		if len(es) != 0 {
			t.Fatalf("errs = %v", es)
		}
		drainEvents(t, evs, eerrs)
	}
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before {
		buf := make([]byte, 1<<16)
		t.Fatalf("goroutines %d > %d\n%s", n, before, buf[:runtime.Stack(buf, true)])
	}
}

func statsFrom(t *testing.T, body string) (Stats, string) {
	t.Helper()
	var q string
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.RawQuery
		if r.URL.Path != "/v1.41/containers/abc/stats" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	s, err := New(sock, 2*time.Second).Stats(ctx(t), "abc")
	if err != nil {
		t.Fatal(err)
	}
	return s, q
}

func readTestdata(t *testing.T, name string) string {
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStatsV2(t *testing.T) {
	s, q := statsFrom(t, readTestdata(t, "stats_v2.json"))
	if !strings.Contains(q, "stream=false") || !strings.Contains(q, "one-shot=false") {
		t.Errorf("query = %q", q)
	}
	if math.Abs(s.CPUPercent-40) > 1e-9 {
		t.Errorf("cpu = %v", s.CPUPercent)
	}
	if s.MemUsage != 250000000 || s.MemLimit != 1000000000 {
		t.Errorf("mem = %d/%d", s.MemUsage, s.MemLimit)
	}
	if s.NetRx != 1010 || s.NetTx != 2020 {
		t.Errorf("net = %d/%d", s.NetRx, s.NetTx)
	}
	if s.BlkRead != 4097 || s.BlkWrite != 8194 {
		t.Errorf("blk = %d/%d", s.BlkRead, s.BlkWrite)
	}
	if !s.Read.Equal(time.Date(2026, 10, 14, 10, 0, 1, 500000000, time.UTC)) {
		t.Errorf("read = %v", s.Read)
	}
}

func TestStatsV1CacheAndPercpuFallback(t *testing.T) {
	s, _ := statsFrom(t, readTestdata(t, "stats_v1.json"))
	if s.MemUsage != 500 || s.MemLimit != 2000 {
		t.Errorf("mem = %d/%d", s.MemUsage, s.MemLimit)
	}
	// cpuDelta 200, sysDelta 1000, 2 cpus from percpu_usage -> 40
	if math.Abs(s.CPUPercent-40) > 1e-9 {
		t.Errorf("cpu = %v", s.CPUPercent)
	}
	if s.NetRx != 0 || s.NetTx != 0 || s.BlkRead != 0 {
		t.Errorf("missing sections should be zero: %+v", s)
	}
}

func TestStatsZeroDeltas(t *testing.T) {
	s, _ := statsFrom(t, `{"cpu_stats":{"cpu_usage":{"total_usage":5},"system_cpu_usage":9,"online_cpus":2},"precpu_stats":{"cpu_usage":{"total_usage":5},"system_cpu_usage":9},"memory_stats":{"usage":10,"limit":20,"stats":{"inactive_file":50}}}`)
	if s.CPUPercent != 0 {
		t.Errorf("cpu = %v", s.CPUPercent)
	}
	if s.MemUsage != 0 { // inactive_file larger than usage must not underflow
		t.Errorf("mem = %d", s.MemUsage)
	}
}

func TestStatsStoppedContainerError(t *testing.T) {
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"container is not running"}`, 409)
	}))
	_, err := New(sock, 2*time.Second).Stats(ctx(t), "abc")
	if err == nil || errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("err = %v", err)
	}
}

// cutMidStream sends some bytes then closes the connection without finishing
// the chunked body, as a crashing daemon would.
func cutMidStream(prefix []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/logs") {
			http.NotFound(w, r)
			return
		}
		w.Write(prefix)
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	})
}

func TestLogsDaemonCrashMidStreamIsUnavailable(t *testing.T) {
	for name, tc := range map[string]struct {
		tty  bool
		body []byte
	}{
		"multiplexed at frame boundary": {false, frame(1, "2026-10-14T10:00:00Z hi\n")},
		"multiplexed mid frame":         {false, frame(1, "2026-10-14T10:00:00Z hi\n")[:15]},
		"tty":                           {true, []byte("2026-10-14T10:00:00Z hi\n")},
	} {
		t.Run(name, func(t *testing.T) {
			sock := serve(t, cutMidStream(tc.body))
			lines, errs := New(sock, 2*time.Second).Logs(ctx(t), "abc", LogOptions{Follow: true, TTY: tc.tty})
			_, es := collectLogs(t, lines, errs)
			if len(es) != 1 || !errors.Is(es[0], ErrUnavailable) {
				t.Fatalf("errs = %v", es)
			}
		})
	}
}

func TestLogsTruncatedFinalFrameSurfacesError(t *testing.T) {
	body := frame(1, "2026-10-14T10:00:00Z hi\n")
	sock := serve(t, logHandler(t, body[:len(body)-4], nil)) // clean HTTP end, short frame
	lines, errs := New(sock, 2*time.Second).Logs(ctx(t), "abc", LogOptions{})
	_, es := collectLogs(t, lines, errs)
	if len(es) != 1 || !errors.Is(es[0], io.ErrUnexpectedEOF) {
		t.Fatalf("errs = %v", es)
	}
}

func waitGoroutines(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > baseline {
		buf := make([]byte, 1<<16)
		t.Fatalf("goroutines %d > %d\n%s", n, baseline, buf[:runtime.Stack(buf, true)])
	}
}

func TestAbandonedConsumerDoesNotLeak(t *testing.T) {
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; r.Context().Err() == nil && i < 100000; i++ {
			if strings.HasSuffix(r.URL.Path, "/logs") {
				w.Write(frame(1, "2026-10-14T10:00:00Z line\n"))
			} else {
				io.WriteString(w, evJSON("container", "start", "aaa"))
			}
			if i%50 == 0 {
				w.(http.Flusher).Flush()
			}
		}
	}))
	cl := New(sock, 2*time.Second)
	if err := cl.Ping(ctx(t)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()
	for i := 0; i < 3; i++ {
		c, cancel := ctxCancel(t)
		lines, lerrs := cl.Logs(c, "abc", LogOptions{Follow: true})
		evs, eerrs := cl.Events(c, time.Time{})
		<-lines
		<-evs
		time.Sleep(100 * time.Millisecond) // producers are now blocked sending
		cancel()
		_ = lerrs
		_ = eerrs
	}
	waitGoroutines(t, before)
}

func TestParseLogLineWalkBackCappedAtThreeBytes(t *testing.T) {
	raw := strings.Repeat("\x80", 3*maxLogLine) // only continuation bytes
	got := parseLogLine("stdout", []byte(raw))
	if len(got.Text) != maxLogLine-3 {
		t.Fatalf("len = %d, want %d", len(got.Text), maxLogLine-3)
	}
}
