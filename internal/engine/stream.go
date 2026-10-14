package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// maxLogLine caps the text of one log line; the rest of the line is dropped.
const maxLogLine = 16 << 10

// Event is one Docker container event.
type Event struct {
	Type, Action, ContainerID string // Type is always "container"
	Labels                    map[string]string
	Time                      time.Time
}

// Stats is one point-in-time resource reading of a container.
type Stats struct {
	Read               time.Time
	CPUPercent         float64 // Docker's formula; 0 when the deltas are 0
	MemUsage, MemLimit uint64  // usage minus page cache that the kernel can reclaim
	NetRx, NetTx       uint64  // summed over interfaces
	BlkRead, BlkWrite  uint64  // summed over devices
}

// LogLine is one line of container output.
type LogLine struct {
	Stream string // "stdout" or "stderr"
	Time   time.Time
	Text   string
}

// LogOptions selects which logs to read.
type LogOptions struct {
	Follow bool
	Tail   int // -1 = all
	Since  time.Time
	TTY    bool // the container has a TTY, so the stream is raw rather than multiplexed
}

// openStream issues a versioned GET whose body is bound only by caller. The
// client timeout still bounds version negotiation and the wait for response
// headers. The returned cancel must be called once the body is done with.
func (c *Client) openStream(caller context.Context, path string) (*http.Response, context.CancelFunc, error) {
	if err := caller.Err(); err != nil {
		return nil, nil, fmt.Errorf("docker engine request: %w", err)
	}
	vctx, vcancel := context.WithTimeout(caller, c.timeout)
	v, err := c.apiVersion(caller, vctx)
	vcancel()
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(caller)
	headerTimer := time.AfterFunc(c.timeout, cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/v"+v+path, nil)
	if err != nil {
		headerTimer.Stop()
		cancel()
		return nil, nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		headerTimer.Stop()
		cancel()
		return nil, nil, c.classify(caller, err)
	}
	if !headerTimer.Stop() { // the header deadline fired just as headers arrived
		resp.Body.Close()
		cancel()
		return nil, nil, c.classify(caller, context.DeadlineExceeded)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer cancel()
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var e struct{ Message string }
		if json.Unmarshal(body, &e) == nil && e.Message != "" {
			return nil, nil, fmt.Errorf("docker engine: %s (HTTP %d)", e.Message, resp.StatusCode)
		}
		return nil, nil, fmt.Errorf("docker engine: HTTP %d", resp.StatusCode)
	}
	return resp, cancel, nil
}

type apiEvent struct {
	Type   string `json:"Type"`
	Action string `json:"Action"`
	Actor  struct {
		ID         string            `json:"ID"`
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
	Time     int64 `json:"time"`
	TimeNano int64 `json:"timeNano"`
}

// Events streams container events from since (zero = now). Both channels
// close when the stream ends. Cancelling ctx ends it with no error; any other
// ending delivers exactly one error first, and a daemon that closes the stream
// yields io.EOF wrapped with ErrUnavailable.
func (c *Client) Events(ctx context.Context, since time.Time) (<-chan Event, <-chan error) {
	out := make(chan Event)
	errs := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errs)
		if err := c.streamEvents(ctx, since, out); err != nil && ctx.Err() == nil {
			errs <- err
		}
	}()
	return out, errs
}

func (c *Client) streamEvents(ctx context.Context, since time.Time, out chan<- Event) error {
	q := url.Values{"filters": {`{"type":["container"]}`}}
	if !since.IsZero() {
		q.Set("since", strconv.FormatInt(since.Unix(), 10))
	}
	resp, cancel, err := c.openStream(ctx, "/events?"+q.Encode())
	if err != nil {
		return err
	}
	defer cancel()
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var raw apiEvent
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return fmt.Errorf("%w: %w", ErrUnavailable, io.EOF)
			}
			return c.classify(ctx, err)
		}
		if raw.Type != "container" {
			continue
		}
		ev := Event{Type: raw.Type, Action: raw.Action, ContainerID: raw.Actor.ID, Labels: raw.Actor.Attributes}
		if raw.TimeNano > 0 {
			ev.Time = time.Unix(0, raw.TimeNano)
		} else if raw.Time > 0 {
			ev.Time = time.Unix(raw.Time, 0)
		}
		select {
		case out <- ev:
		case <-ctx.Done():
			return nil
		}
	}
}

type apiStats struct {
	Read     time.Time `json:"read"`
	CPUStats apiCPU    `json:"cpu_stats"`
	PreCPU   apiCPU    `json:"precpu_stats"`
	Memory   struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	Blkio struct {
		IOServiceBytes []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
}

type apiCPU struct {
	Usage struct {
		Total  uint64   `json:"total_usage"`
		PerCPU []uint64 `json:"percpu_usage"`
	} `json:"cpu_usage"`
	System uint64 `json:"system_cpu_usage"`
	Online uint64 `json:"online_cpus"`
}

// Stats takes one reading. Docker computes the CPU delta over about a second,
// so the call is bounded by the client timeout like any other request.
func (c *Client) Stats(ctx context.Context, containerID string) (Stats, error) {
	body, err := c.get(ctx, "/containers/"+url.PathEscape(containerID)+"/stats?stream=false&one-shot=false")
	if err != nil {
		return Stats{}, err
	}
	var raw apiStats
	if err := json.Unmarshal(body, &raw); err != nil {
		return Stats{}, fmt.Errorf("decode stats: %w", err)
	}
	s := Stats{Read: raw.Read, MemUsage: raw.Memory.Usage, MemLimit: raw.Memory.Limit}

	cpuDelta := float64(raw.CPUStats.Usage.Total) - float64(raw.PreCPU.Usage.Total)
	sysDelta := float64(raw.CPUStats.System) - float64(raw.PreCPU.System)
	if cpuDelta > 0 && sysDelta > 0 {
		online := float64(raw.CPUStats.Online)
		if online == 0 {
			online = float64(len(raw.CPUStats.Usage.PerCPU))
		}
		if online == 0 {
			online = 1
		}
		s.CPUPercent = cpuDelta / sysDelta * online * 100
	}

	reclaimable, ok := raw.Memory.Stats["inactive_file"] // cgroup v2
	if !ok {
		reclaimable = raw.Memory.Stats["cache"] // cgroup v1
	}
	if reclaimable < s.MemUsage {
		s.MemUsage -= reclaimable
	} else {
		s.MemUsage = 0
	}

	for _, n := range raw.Networks {
		s.NetRx += n.RxBytes
		s.NetTx += n.TxBytes
	}
	for _, b := range raw.Blkio.IOServiceBytes {
		switch strings.ToLower(b.Op) {
		case "read":
			s.BlkRead += b.Value
		case "write":
			s.BlkWrite += b.Value
		}
	}
	return s, nil
}

// Logs streams a container's output as lines. Both channels close when the
// stream ends. Cancelling ctx ends it with no error, and so does the daemon
// finishing the stream (non-follow, or the container exited); any other
// failure is delivered as one error first.
func (c *Client) Logs(ctx context.Context, containerID string, opts LogOptions) (<-chan LogLine, <-chan error) {
	out := make(chan LogLine)
	errs := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errs)
		if err := c.streamLogs(ctx, containerID, opts, out); err != nil && ctx.Err() == nil {
			errs <- err
		}
	}()
	return out, errs
}

func (c *Client) streamLogs(ctx context.Context, containerID string, opts LogOptions, out chan<- LogLine) error {
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"}}
	if opts.Follow {
		q.Set("follow", "1")
	}
	if opts.Tail < 0 {
		q.Set("tail", "all")
	} else {
		q.Set("tail", strconv.Itoa(opts.Tail))
	}
	if !opts.Since.IsZero() {
		q.Set("since", strconv.FormatInt(opts.Since.Unix(), 10))
	}
	resp, cancel, err := c.openStream(ctx, "/containers/"+url.PathEscape(containerID)+"/logs?"+q.Encode())
	if err != nil {
		return err
	}
	defer cancel()
	defer resp.Body.Close()

	stopped := false
	sink := func(stream string) func([]byte) {
		return func(raw []byte) {
			if stopped {
				return
			}
			line := parseLogLine(stream, raw)
			select {
			case out <- line:
			case <-ctx.Done():
				stopped = true
			}
		}
	}
	emit := map[string]func([]byte){"stdout": sink("stdout"), "stderr": sink("stderr")}
	bufs := map[string]*lineBuf{"stdout": {}, "stderr": {}}

	br := bufio.NewReaderSize(resp.Body, 32<<10)
	readErr := demuxLogs(br, opts.TTY, func(stream string, p []byte) bool {
		bufs[stream].write(p, emit[stream])
		return !stopped
	})
	if stopped || ctx.Err() != nil {
		return nil
	}
	for _, s := range []string{"stdout", "stderr"} {
		bufs[s].flush(emit[s])
	}
	if readErr == nil || errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
		return nil
	}
	return c.classify(ctx, readErr)
}

// demuxLogs feeds payload bytes to fn, tagged with their stream, until the
// reader ends or fn returns false. Reads are chunked so a bogus frame length
// cannot force a large allocation, and frames may arrive split anywhere.
func demuxLogs(r *bufio.Reader, tty bool, fn func(stream string, p []byte) bool) error {
	chunk := make([]byte, 32<<10)
	if tty {
		for {
			n, err := r.Read(chunk)
			if n > 0 && !fn("stdout", chunk[:n]) {
				return nil
			}
			if err != nil {
				return err
			}
		}
	}
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return err
		}
		stream := "stdout"
		if hdr[0] == 2 {
			stream = "stderr"
		}
		remaining := int64(uint32(hdr[4])<<24 | uint32(hdr[5])<<16 | uint32(hdr[6])<<8 | uint32(hdr[7]))
		for remaining > 0 {
			n := int64(len(chunk))
			if remaining < n {
				n = remaining
			}
			if _, err := io.ReadFull(r, chunk[:n]); err != nil {
				return err
			}
			if !fn(stream, chunk[:n]) {
				return nil
			}
			remaining -= n
		}
	}
}

// lineBuf accumulates bytes into lines. A line that outgrows its cap is
// emitted truncated and the rest of it is skipped, so memory stays bounded.
type lineBuf struct {
	buf      []byte
	skipping bool
}

// maxPending leaves room for the timestamp prefix on top of the text cap.
const maxPending = maxLogLine + 64

func (b *lineBuf) write(p []byte, emit func([]byte)) {
	for len(p) > 0 {
		i := indexNewline(p)
		if i < 0 {
			if !b.skipping {
				b.buf = append(b.buf, p...)
				if len(b.buf) > maxPending {
					emit(b.buf)
					b.buf, b.skipping = nil, true
				}
			}
			return
		}
		if !b.skipping {
			b.buf = append(b.buf, p[:i]...)
			emit(b.buf)
		}
		b.buf, b.skipping = b.buf[:0], false
		p = p[i+1:]
	}
}

// flush emits a final unterminated line.
func (b *lineBuf) flush(emit func([]byte)) {
	if !b.skipping && len(b.buf) > 0 {
		emit(b.buf)
	}
	b.buf, b.skipping = nil, false
}

func indexNewline(p []byte) int {
	for i, c := range p {
		if c == '\n' {
			return i
		}
	}
	return -1
}

// parseLogLine splits "<RFC3339Nano> text"; a line without a valid timestamp
// keeps its whole content and a zero time.
func parseLogLine(stream string, raw []byte) LogLine {
	s := strings.TrimSuffix(string(raw), "\r")
	line := LogLine{Stream: stream, Text: s}
	if i := strings.IndexByte(s, ' '); i > 0 {
		if t, err := time.Parse(time.RFC3339Nano, s[:i]); err == nil {
			line.Time, line.Text = t, s[i+1:]
		}
	}
	if len(line.Text) > maxLogLine {
		cut := maxLogLine
		for cut > 0 && !utf8.RuneStart(line.Text[cut]) {
			cut--
		}
		line.Text = line.Text[:cut]
	}
	return line
}
