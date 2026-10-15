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
	Type, Action, ContainerID string            // Type is always "container"
	Labels                    map[string]string // every actor attribute
	Time                      time.Time
	Name, Image               string
	ExitCode                  *int   // only on "die"
	Health                    string // "healthy" or "unhealthy"; only on "health_status"
}

// statsWindow is the extra time a stats call gets for Docker to compute the CPU delta.
const statsWindow = 5 * time.Second

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
	ctx, cancelCause := context.WithCancelCause(caller)
	cancel := context.CancelFunc(func() { cancelCause(context.Canceled) })
	// The cause lets a header timeout report a deadline rather than "canceled".
	headerTimer := time.AfterFunc(c.timeout, func() { cancelCause(context.DeadlineExceeded) })
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
		if cause := context.Cause(ctx); caller.Err() == nil && errors.Is(cause, context.DeadlineExceeded) {
			err = cause
		}
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

// event converts the wire form, reading name, image, exit code and health
// from the actor attributes. "health_status: unhealthy" becomes the action
// "health_status" with Health "unhealthy".
func (raw apiEvent) event() Event {
	attrs := raw.Actor.Attributes
	ev := Event{Type: raw.Type, Action: raw.Action, ContainerID: raw.Actor.ID, Labels: attrs, Name: attrs["name"], Image: attrs["image"]}
	if raw.TimeNano > 0 {
		ev.Time = time.Unix(0, raw.TimeNano)
	} else if raw.Time > 0 {
		ev.Time = time.Unix(raw.Time, 0)
	}
	if status, ok := strings.CutPrefix(raw.Action, "health_status: "); ok {
		ev.Action = "health_status"
		if status == "healthy" || status == "unhealthy" {
			ev.Health = status
		}
	}
	if ev.Action == "die" {
		if code, err := strconv.Atoi(attrs["exitCode"]); err == nil {
			ev.ExitCode = &code
		}
	}
	return ev
}

// EventsWindow returns every container event with since <= time < until whose
// action is in actions (nil = all). Docker ends the stream because until is
// set; the whole call is bounded by the client timeout.
func (c *Client) EventsWindow(ctx context.Context, since, until time.Time, actions []string) ([]Event, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	filters := map[string][]string{"type": {"container"}}
	if len(actions) > 0 {
		filters["event"] = actions
	}
	encoded, err := json.Marshal(filters)
	if err != nil {
		return nil, err
	}
	q := url.Values{
		"filters": {string(encoded)},
		"since":   {strconv.FormatInt(since.Unix(), 10)},
		"until":   {strconv.FormatInt(until.Unix(), 10)},
	}
	resp, cancelStream, err := c.openStream(ctx, "/events?"+q.Encode())
	if err != nil {
		return nil, err
	}
	defer cancelStream()
	defer resp.Body.Close()
	var out []Event
	dec := json.NewDecoder(resp.Body)
	for {
		var raw apiEvent
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, c.classify(ctx, err)
		}
		if raw.Type != "container" {
			continue
		}
		ev := raw.event()
		if ev.Time.Before(since) || !ev.Time.Before(until) {
			continue
		}
		out = append(out, ev)
	}
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
		ev := raw.event()
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

// Stats takes one reading. Docker computes the CPU delta over a sampling
// window of one to two seconds, so the call gets the client timeout plus
// statsWindow; callers bound it further through ctx.
func (c *Client) Stats(ctx context.Context, containerID string) (Stats, error) {
	body, err := c.getWithin(ctx, "/containers/"+url.PathEscape(containerID)+"/stats?stream=false&one-shot=false", c.timeout+statsWindow)
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
		// Nanosecond precision (inclusive on the daemon) lets a caller resume
		// exactly after a line it has already seen.
		q.Set("since", fmt.Sprintf("%d.%09d", opts.Since.Unix(), opts.Since.Nanosecond()))
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
	// Only a clean EOF is a normal end; a cut-short body or truncated frame is a failure.
	if readErr == nil || errors.Is(readErr, io.EOF) {
		return nil
	}
	return c.classify(ctx, readErr)
}

// demuxLogs feeds payload bytes to fn, tagged with their stream, until the
// reader ends or fn returns false. Reads are chunked so a bogus frame length
// cannot force a large allocation, and frames may arrive split anywhere.
//
// A multiplexed stream starts with a frame header (stream 0-2, three zero
// bytes). When the first bytes cannot be one, the stream is a raw TTY stream
// whose flag the caller did not know, and it is read as such. Only the first
// header is checked, which is where the two framings diverge.
func demuxLogs(r *bufio.Reader, tty bool, fn func(stream string, p []byte) bool) error {
	chunk := make([]byte, 32<<10)
	if !tty {
		// Peek does not consume. A short or failed peek leaves tty false so the
		// normal loop reports the truncation.
		first, _ := r.Peek(8)
		if len(first) > 0 && !validFrameStart(first) {
			tty = true
		}
	}
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

// validFrameStart reports whether the leading bytes (up to 8) are consistent
// with a multiplexed frame header: stream type 0-2 and three zero bytes.
func validFrameStart(b []byte) bool {
	if b[0] > 2 {
		return false
	}
	for _, v := range b[1:min(len(b), 4)] {
		if v != 0 {
			return false
		}
	}
	return true
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
		for back := 0; back < utf8.UTFMax-1 && cut > 0 && !utf8.RuneStart(line.Text[cut]); back++ {
			cut--
		}
		line.Text = line.Text[:cut]
	}
	return line
}
