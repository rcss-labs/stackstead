package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// ErrNotRunning is wrapped when Docker refuses an exec because the container
// is not running (HTTP 409).
var ErrNotRunning = errors.New("docker engine: container is not running")

// ExecSpec describes a command to run inside a container.
type ExecSpec struct {
	Cmd  []string
	User string
	Tty  bool
	Env  []string
}

// ExecInspect is the state of an exec instance.
type ExecInspect struct {
	Running  bool
	ExitCode int
	Pid      int
}

// ExecStream is an attached exec session. Not safe for concurrent reads, but
// one reader, one writer and Close may run concurrently.
type ExecStream interface {
	// ReadFrame returns the next chunk of output. stream is 1 (stdout) or
	// 2 (stderr); a TTY merges both and always reports 1. It returns io.EOF
	// when the process closed its output.
	ReadFrame() (stream byte, p []byte, err error)
	Write(p []byte) (int, error) // stdin
	CloseWrite() error           // stdin EOF; output stays readable
	Close() error
}

// execMapError maps Docker's 404 and 409 answers to the package errors.
func execMapError(err error) error {
	var ae *ActionError
	if errors.As(err, &ae) && ae.Status == http.StatusNotModified {
		return err // actionResult would turn this into success
	}
	err = actionResult(err)
	if errors.As(err, &ae) && ae.Status == http.StatusConflict {
		return fmt.Errorf("%w: %w", ErrNotRunning, ae)
	}
	return err
}

// CreateExec creates an exec instance in the container and returns its ID.
func (c *Client) CreateExec(ctx context.Context, containerID string, spec ExecSpec) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("docker engine request: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, c.callerBound(ctx))
	defer cancel()
	v, err := c.apiVersion(ctx, reqCtx)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"AttachStdin":  true,
		"AttachStdout": true,
		"AttachStderr": true,
		"Tty":          spec.Tty,
		"Cmd":          spec.Cmd,
		"User":         spec.User,
		"Env":          spec.Env,
	}
	resp, err := c.postJSON(ctx, reqCtx, "/v"+v+"/containers/"+url.PathEscape(containerID)+"/exec", payload)
	if err != nil {
		return "", execMapError(err)
	}
	var out struct{ Id string }
	if err := json.Unmarshal(resp.body, &out); err != nil || out.Id == "" {
		return "", fmt.Errorf("decode exec create: missing id (%v)", err)
	}
	return out.Id, nil
}

// ResizeExec sets the TTY size of a running exec.
func (c *Client) ResizeExec(ctx context.Context, execID string, cols, rows uint16) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("docker engine request: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, c.callerBound(ctx))
	defer cancel()
	v, err := c.apiVersion(ctx, reqCtx)
	if err != nil {
		return err
	}
	path := "/v" + v + "/exec/" + url.PathEscape(execID) + "/resize?h=" +
		strconv.Itoa(int(rows)) + "&w=" + strconv.Itoa(int(cols))
	_, err = c.request(ctx, reqCtx, http.MethodPost, path)
	return execMapError(err)
}

// InspectExec returns the state and exit code of an exec instance.
func (c *Client) InspectExec(ctx context.Context, execID string) (ExecInspect, error) {
	body, err := c.get(ctx, "/exec/"+url.PathEscape(execID)+"/json")
	if err != nil {
		return ExecInspect{}, execMapError(err)
	}
	var out ExecInspect
	if err := json.Unmarshal(body, &out); err != nil {
		return ExecInspect{}, fmt.Errorf("decode exec inspect: %w", err)
	}
	return out, nil
}

// StartExec starts the exec and attaches to it over a hijacked connection.
// The connection lives until Close or until ctx is done, whichever is first;
// ctx bounds the whole session, not only the handshake.
func (c *Client) StartExec(ctx context.Context, execID string, tty bool) (ExecStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("docker engine request: %w", err)
	}
	hctx, cancel := context.WithTimeout(ctx, c.callerBound(ctx))
	v, err := c.apiVersion(ctx, hctx)
	cancel()
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(map[string]bool{"Detach": false, "Tty": tty})
	if err != nil {
		return nil, fmt.Errorf("encode docker request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://docker/v"+v+"/exec/"+url.PathEscape(execID)+"/start", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")

	dialCtx, dialCancel := context.WithTimeout(ctx, c.timeout)
	conn, err := c.dial(dialCtx)
	dialCancel()
	if err != nil {
		return nil, c.classify(ctx, err)
	}
	s := &execStream{conn: conn, tty: tty, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-s.done:
		}
	}()

	// Header phase: bounded by the client timeout.
	_ = conn.SetDeadline(time.Now().Add(c.timeout))
	br := bufio.NewReaderSize(conn, 32<<10)
	resp, err := func() (*http.Response, error) {
		if err := req.Write(conn); err != nil {
			return nil, err
		}
		return http.ReadResponse(br, req)
	}()
	if err != nil {
		s.Close()
		return nil, c.classify(ctx, err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		s.Close()
		var e struct{ Message string }
		_ = json.Unmarshal(raw, &e)
		return nil, execMapError(&ActionError{Status: resp.StatusCode, Message: e.Message})
	}
	_ = conn.SetDeadline(time.Time{})
	s.br = br // keeps bytes already buffered behind the headers
	return s, nil
}

// execStream implements ExecStream over a hijacked connection.
type execStream struct {
	conn net.Conn
	br   *bufio.Reader
	tty  bool

	remaining uint32 // unread payload bytes of the current multiplexed frame
	kind      byte   // stream type of the current frame

	closeOnce sync.Once
	done      chan struct{}
}

func (s *execStream) ReadFrame() (byte, []byte, error) {
	buf := make([]byte, 32<<10)
	if s.tty {
		n, err := s.br.Read(buf)
		if n > 0 {
			return 1, buf[:n], nil
		}
		return 0, nil, err
	}
	for s.remaining == 0 {
		kind, size, err := readFrameHeader(s.br)
		if err != nil {
			return 0, nil, err // a clean end between frames is io.EOF
		}
		s.kind, s.remaining = kind, size
	}
	n := min(uint32(len(buf)), s.remaining)
	if _, err := io.ReadFull(s.br, buf[:n]); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return 0, nil, err
	}
	s.remaining -= n
	return s.kind, buf[:n], nil
}

func (s *execStream) Write(p []byte) (int, error) { return s.conn.Write(p) }

func (s *execStream) CloseWrite() error {
	if cw, ok := s.conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return errors.New("docker engine: connection cannot half-close")
}

func (s *execStream) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.done)
		err = s.conn.Close()
	})
	return err
}
