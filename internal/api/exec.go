package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/net/websocket"

	"github.com/rcss-labs/stackstead/internal/actionlog"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/peers"
)

// ExecOptions configures the container exec route.
type ExecOptions struct {
	Enabled     bool
	MaxSessions int           // concurrent sessions; below 1 means the default of 8
	IdleTimeout time.Duration // 0 disables the idle timeout
}

const (
	codeContainerNotFound = "container_not_found"
	codeExecDisabled      = "exec_disabled"
	codeBadOrigin         = "bad_origin"
	codeNotRunning        = "not_running"
	codeExecLimit         = "exec_limit"

	defaultExecSessions = 8
	execMaxArgs         = 64
	execMaxArgBytes     = 4096
	execMaxUserBytes    = 256
	execDefaultCols     = 80
	execDefaultRows     = 24
	execAuditDetailMax  = 300

	execPing        = 20 * time.Second
	execPongTimeout = 60 * time.Second
	execDetachCheck = 2 * time.Second

	autoShellScript = "if [ -x /bin/bash ]; then exec /bin/bash; else exec /bin/sh; fi"
	noShellMessage  = "no shell found in this container; pass a command"
)

func (s *server) registerExecRoute(mux *http.ServeMux) {
	slots := s.opts.Exec.MaxSessions
	if slots < 1 {
		slots = defaultExecSessions
	}
	s.execSlots = make(chan struct{}, slots)
	mux.Handle("GET /api/containers/{id}/exec", s.requireSession(s.handleContainerExec))
}

// originAllowed reports whether a request's Origin names this server's own
// host[:port]. The scheme is deliberately not compared: the documented proxy
// setup does not forward it. A missing Origin is allowed, since browsers
// always send one on WebSocket upgrades and anything else still needs a
// credential.
func originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

type execRequest struct {
	cmd        []string
	autoShell  bool
	user       string
	tty        bool
	cols, rows uint16
}

// parseExecRequest validates the query parameters; the error is safe to show.
func parseExecRequest(q url.Values) (execRequest, error) {
	req := execRequest{cmd: q["cmd"], user: q.Get("user"), tty: true, cols: execDefaultCols, rows: execDefaultRows}
	if len(req.cmd) > execMaxArgs {
		return req, fmt.Errorf("at most %d cmd arguments are allowed", execMaxArgs)
	}
	total := 0
	for _, a := range req.cmd {
		total += len(a)
		if strings.ContainsRune(a, 0) {
			return req, errors.New("cmd must not contain NUL bytes")
		}
	}
	if total > execMaxArgBytes {
		return req, fmt.Errorf("cmd is limited to %d bytes in total", execMaxArgBytes)
	}
	if len(req.user) > execMaxUserBytes || strings.ContainsFunc(req.user, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return req, errors.New("invalid user")
	}
	switch q.Get("tty") {
	case "", "1":
	case "0":
		req.tty = false
	default:
		return req, errors.New("tty must be 1 or 0")
	}
	for name, dst := range map[string]*uint16{"cols": &req.cols, "rows": &req.rows} {
		if v := q.Get(name); v != "" {
			n, err := strconv.ParseUint(v, 10, 16)
			if err != nil || n == 0 {
				return req, fmt.Errorf("%s must be between 1 and 65535", name)
			}
			*dst = uint16(n)
		}
	}
	if len(req.cmd) == 0 {
		req.autoShell = true
		req.cmd = []string{"/bin/sh", "-c", autoShellScript}
	}
	return req, nil
}

func isUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// handleContainerExec runs every check for any GET, upgrade or not, so a
// client that was refused a handshake can re-issue a plain GET and read the
// JSON reason.
func (s *server) handleContainerExec(w http.ResponseWriter, r *http.Request) {
	if !s.opts.Exec.Enabled {
		writeCodedError(w, http.StatusForbidden, "container exec is disabled on this server", codeExecDisabled)
		return
	}
	if !originAllowed(r) {
		writeCodedError(w, http.StatusForbidden, "origin not allowed", codeBadOrigin)
		return
	}
	insp, ok := s.inspectContainer(w, r)
	if !ok {
		return
	}
	if !insp.Running || insp.State != "running" {
		writeCodedError(w, http.StatusConflict, "container is not running", codeNotRunning)
		return
	}
	req, err := parseExecRequest(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	projects, err := s.projectStacks(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	scope := "host"
	if managed, stack := containerManaged(engine.Container{Labels: insp.Labels}, projects); managed == managedStack {
		scope = stack
	}

	select {
	case s.execSlots <- struct{}{}:
	default:
		writeCodedError(w, http.StatusTooManyRequests, "too many exec sessions", codeExecLimit)
		return
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { <-s.execSlots }) }
	defer release()

	if !isUpgrade(r) {
		writeError(w, http.StatusBadRequest, "websocket upgrade required")
		return
	}

	sess := execSession{
		s: s, req: req, insp: insp, scope: scope, user: userFrom(r.Context()).Username,
		authorize: s.execAuthorizer(r), release: release,
	}
	// The handshake only accepts what the checks above already passed; it
	// replaces x/net's default Origin check.
	websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler:   sess.serve,
	}.ServeHTTP(w, r)
}

// execAuthorizer re-validates the credential the session started with.
func (s *server) execAuthorizer(r *http.Request) func(context.Context) bool {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		token := cookie.Value
		return func(ctx context.Context) bool {
			_, err := s.opts.Auth.Authenticate(ctx, token)
			return err == nil
		}
	}
	credential := r.Header.Get(peers.PeerHeader)
	return func(ctx context.Context) bool {
		_, err := s.opts.Peers.Authenticate(ctx, credential, false)
		return err == nil
	}
}

type execSession struct {
	s         *server
	req       execRequest
	insp      engine.ContainerInspect
	scope     string
	user      string
	authorize func(context.Context) bool
	release   func()
}

// slotConn frees the session slot the moment the client's read side fails, so
// a vanished client does not hold a slot through the detach check.
type slotConn struct {
	wsConn
	release func()
}

func (c slotConn) ReadFrame() (bool, []byte, error) {
	text, data, err := c.wsConn.ReadFrame()
	if err != nil {
		c.release()
	}
	return text, data, err
}

// execBackendAdapter binds one exec ID to the engine.
type execBackendAdapter struct {
	eng    ContainerEngineAPI
	execID string
}

func (b execBackendAdapter) Resize(ctx context.Context, cols, rows uint16) error {
	return b.eng.ResizeExec(ctx, b.execID, cols, rows)
}

func (b execBackendAdapter) Inspect(ctx context.Context) (engine.ExecInspect, error) {
	return b.eng.InspectExec(ctx, b.execID)
}

func (e execSession) serve(ws *websocket.Conn) {
	// The hijacked connection keeps the HTTP server's deadlines until cleared.
	_ = ws.SetDeadline(time.Time{})
	conn := slotConn{wsConn: newWSConn(ws, execPongTimeout), release: e.release}
	ctx := ws.Request().Context()
	eng := e.s.opts.ContainerEngine

	commandLine := "auto shell"
	if !e.req.autoShell {
		commandLine = truncateUTF8(strings.Join(e.req.cmd, " "), execAuditDetailMax)
	}
	started := time.Now()
	e.audit(ctx, "started", commandLine)
	result := "error"
	defer func() {
		detail := fmt.Sprintf("%s; duration %s", commandLine, time.Since(started).Round(time.Second))
		e.audit(ctx, result, detail)
	}()

	spec := engine.ExecSpec{Cmd: e.req.cmd, User: e.req.user, Tty: e.req.tty}
	if e.req.tty {
		spec.Env = []string{"TERM=xterm-256color"}
	}
	execID, err := eng.CreateExec(ctx, e.insp.ID, spec)
	var stream engine.ExecStream
	if err == nil {
		stream, err = eng.StartExec(ctx, execID, e.req.tty)
	}
	if err != nil {
		e.s.opts.Logger.Info("container exec failed to start", "container", e.insp.Name, "err", err)
		_ = conn.WriteText(controlMsg{Type: "error", Message: e.startFailureMessage(err)})
		_ = conn.Close()
		return
	}
	if e.req.tty {
		_ = eng.ResizeExec(ctx, execID, e.req.cols, e.req.rows)
	}

	result = runSession(ctx, conn, stream, execBackendAdapter{eng: eng, execID: execID}, sessionConfig{
		Ping:            execPing,
		PongTimeout:     execPongTimeout,
		IdleTimeout:     e.s.opts.Exec.IdleTimeout,
		DetachCheck:     execDetachCheck,
		Now:             time.Now,
		After:           time.After,
		StillAuthorized: e.authorize,
		Closing:         e.s.opts.Closing,
	})
}

func (e execSession) startFailureMessage(err error) string {
	switch {
	case errors.Is(err, engine.ErrNotRunning):
		return "container is not running"
	case missingExecutable(err):
		if e.req.autoShell {
			return noShellMessage
		}
		return "command not found in this container"
	}
	return "could not start the command"
}

func missingExecutable(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such file") || strings.Contains(msg, "executable file not found")
}

// audit records one entry; a failure is logged but never ends the session.
func (e execSession) audit(ctx context.Context, result, detail string) {
	store := e.s.opts.Actions
	if store == nil {
		return
	}
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	user := e.req.user
	if user != "" {
		detail += "; user " + user
	}
	err := store.Append(actx, actionlog.Entry{
		Username: e.user, Kind: "container.exec", Scope: e.scope, Target: e.insp.Name,
		Detail: detail, Result: result,
	})
	if err != nil {
		e.s.opts.Logger.Warn("container exec audit failed", "container", e.insp.Name, "err", err)
	}
}

func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "..."
}
