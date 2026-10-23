// Package execws is the client side of Stackstead's container exec
// WebSocket: it dials GET /api/containers/{id}/exec and speaks the framed
// protocol (binary frames carry a stream ID byte plus payload; text frames
// carry JSON control messages).
//
// It lives apart from internal/client so that package stays free of
// non-stdlib imports.
package execws

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"

	"github.com/rcss-labs/stackstead/internal/client"
)

const (
	sessionCookie  = "stackstead_session"
	defaultTimeout = 30 * time.Second // matches client.New
)

// Binary frame stream IDs (first payload byte).
const (
	streamStdin  = 0
	streamStdout = 1
	streamStderr = 2
)

// ErrOldServer is returned when the server has no exec route at all.
var ErrOldServer = errors.New("this server needs a newer Stackstead to open a terminal")

// Options selects the command to run.
type Options struct {
	Cmd        []string // empty: the server picks a shell
	User       string
	TTY        bool
	Cols, Rows int // initial size, TTY only; 0 lets the server default
}

// End describes how a session finished.
type End struct {
	ExitCode *int   // set when the command exited
	Reason   string // "", "idle", "session_expired", "shutdown", or "error: <message>"
}

// Session is one open exec WebSocket.
type Session struct {
	conn *websocket.Conn
	mu   sync.Mutex // serialises writes: Stdin, Resize and pongs come from different goroutines
	stop chan struct{}
	once sync.Once
}

type controlFrame struct {
	Type    string `json:"type"`
	Code    *int   `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Seconds int    `json:"seconds,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type resizeFrame struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// frame keeps the WebSocket payload type, which websocket.Message hides.
type frame struct {
	text bool
	data []byte
}

var frameCodec = websocket.Codec{
	Marshal: func(v any) ([]byte, byte, error) {
		f := v.(frame)
		if f.text {
			return f.data, websocket.TextFrame, nil
		}
		return f.data, websocket.BinaryFrame, nil
	},
	Unmarshal: func(data []byte, payloadType byte, v any) error {
		*(v.(*frame)) = frame{text: payloadType == websocket.TextFrame, data: append([]byte(nil), data...)}
		return nil
	},
}

// execURL returns the ws(s):// URL for the exec route and the Origin to send
// (the base's own scheme://host, which the server's same-origin check wants).
func execURL(base *url.URL, containerID string, o Options) (wsURL, origin string) {
	scheme := "ws"
	if base.Scheme == "https" {
		scheme = "wss"
	}
	q := url.Values{}
	for _, c := range o.Cmd {
		q.Add("cmd", c)
	}
	if o.User != "" {
		q.Set("user", o.User)
	}
	if o.TTY {
		q.Set("tty", "1")
	} else {
		q.Set("tty", "0")
	}
	if o.Cols > 0 {
		q.Set("cols", strconv.Itoa(o.Cols))
	}
	if o.Rows > 0 {
		q.Set("rows", strconv.Itoa(o.Rows))
	}
	path := strings.TrimRight(base.EscapedPath(), "/") + "/api/containers/" + url.PathEscape(containerID) + "/exec"
	return scheme + "://" + base.Host + path + "?" + q.Encode(), base.Scheme + "://" + base.Host
}

// Dial opens the exec WebSocket. Pre-upgrade HTTP errors come back as
// *client.Error; a 404 without an error code means the server predates exec
// and yields ErrOldServer.
func Dial(ctx context.Context, cfg client.Config, containerID string, o Options) (*Session, error) {
	if cfg.BaseURL == nil {
		return nil, errors.New("execws: BaseURL is required")
	}
	tlsCfg, err := client.TLSConfig(cfg)
	if err != nil {
		return nil, err
	}
	wsURL, origin := execURL(cfg.BaseURL, containerID, o)
	wc, err := websocket.NewConfig(wsURL, origin)
	if err != nil {
		return nil, err
	}
	wc.TlsConfig = tlsCfg
	if cfg.Session != "" {
		wc.Header.Set("Cookie", sessionCookie+"="+cfg.Session)
	}
	if cfg.UserAgent != "" {
		wc.Header.Set("User-Agent", cfg.UserAgent)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	// Bounds the connect, the handshake and a refusal replay; the session
	// itself is long-lived and only follows ctx.
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := wc.DialContext(dialCtx)
	if err != nil {
		var de *websocket.DialError // does not unwrap, so compare its cause
		if errors.As(err, &de) && de.Err == websocket.ErrBadStatus {
			return nil, upgradeRefusal(dialCtx, cfg, tlsCfg, wsURL, origin)
		}
		return nil, fmt.Errorf("connect to %s: %w", cfg.BaseURL.Host, err)
	}
	s := &Session{conn: conn, stop: make(chan struct{})}
	go func() { // unblock a pending Run when the context ends
		select {
		case <-ctx.Done():
			s.Close()
		case <-s.stop:
		}
	}()
	return s, nil
}

// upgradeRefusal explains a refused handshake. x/net/websocket discards the
// refusing response, so the same request is repeated as a plain GET (no
// Upgrade headers, so it can never start a session) to read the JSON error.
func upgradeRefusal(ctx context.Context, cfg client.Config, tlsCfg *tls.Config, wsURL, origin string) error {
	httpURL := "http" + strings.TrimPrefix(wsURL, "ws")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpURL, nil)
	if err != nil {
		return err
	}
	if cfg.Session != "" {
		req.Header.Set("Cookie", sessionCookie+"="+cfg.Session)
	}
	if cfg.UserAgent != "" {
		req.Header.Set("User-Agent", cfg.UserAgent)
	}
	req.Header.Set("Origin", origin) // same as the handshake, so Origin checks answer identically
	req.Header.Set("Accept", "application/json")
	hc := &http.Client{
		Transport:     &http.Transport{TLSClientConfig: tlsCfg},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer hc.CloseIdleConnections()
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", cfg.BaseURL.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		return errors.New("server refused the WebSocket upgrade")
	}
	apiErr, code := client.ReadErrorBody(resp)
	if resp.StatusCode == http.StatusNotFound && code == "" {
		return ErrOldServer
	}
	return apiErr
}

// Close ends the session. It is safe to call more than once.
func (s *Session) Close() error {
	var err error
	s.once.Do(func() {
		close(s.stop)
		err = s.conn.Close()
	})
	return err
}

func (s *Session) send(f frame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return frameCodec.Send(s.conn, f)
}

func (s *Session) sendControl(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.send(frame{text: true, data: b})
}

// Stdin sends input to the command.
func (s *Session) Stdin(p []byte) error {
	return s.send(frame{data: append([]byte{streamStdin}, p...)})
}

// CloseStdin signals end of input.
func (s *Session) CloseStdin() error { return s.sendControl(controlFrame{Type: "eof"}) }

// Resize reports a new terminal size.
func (s *Session) Resize(cols, rows int) error {
	return s.sendControl(resizeFrame{Type: "resize", Cols: cols, Rows: rows})
}

// Run copies stdout and stderr frames to the writers, answers pings, and
// reports how the session ended. onIdleWarning may be nil. A connection that
// drops without an exit or closed frame is an error.
func (s *Session) Run(stdout, stderr io.Writer, onIdleWarning func(seconds int)) (End, error) {
	for {
		var f frame
		if err := frameCodec.Receive(s.conn, &f); err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return End{}, fmt.Errorf("exec connection lost: %w", err)
		}
		if !f.text {
			if len(f.data) < 2 { // stream ID only, nothing to write
				continue
			}
			var w io.Writer
			switch f.data[0] {
			case streamStdout:
				w = stdout
			case streamStderr:
				w = stderr
			default:
				continue
			}
			if _, err := w.Write(f.data[1:]); err != nil {
				return End{}, err
			}
			continue
		}
		var c controlFrame
		if err := json.Unmarshal(f.data, &c); err != nil {
			continue // ignore control frames this client does not understand
		}
		switch c.Type {
		case "ping":
			if err := s.sendControl(controlFrame{Type: "pong"}); err != nil {
				return End{}, err
			}
		case "exit":
			return End{ExitCode: c.Code}, nil
		case "error":
			return End{Reason: "error: " + c.Message}, nil
		case "idle_warning":
			if onIdleWarning != nil {
				onIdleWarning(c.Seconds)
			}
		case "closed":
			return End{Reason: c.Reason}, nil
		}
	}
}
