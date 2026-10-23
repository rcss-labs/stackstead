package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func TestCreateExecSendsSpec(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"Id":"e1"}`))
	}))
	c := New(sock, time.Second)
	id, err := c.CreateExec(context.Background(), "abc", ExecSpec{
		Cmd: []string{"sh"}, User: "root", Tty: true, Env: []string{"TERM=xterm-256color"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "e1" {
		t.Errorf("id = %q", id)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1.41/containers/abc/exec" {
		t.Errorf("request = %s %s", gotMethod, gotPath)
	}
	want := map[string]any{
		"AttachStdin": true, "AttachStdout": true, "AttachStderr": true, "Tty": true,
		"Cmd": []any{"sh"}, "User": "root", "Env": []any{"TERM=xterm-256color"},
	}
	if !reflect.DeepEqual(gotBody, want) {
		t.Errorf("body = %v, want %v", gotBody, want)
	}
}

func TestCreateExecMapsErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusNotFound, ErrNotFound},
		{http.StatusConflict, ErrNotRunning},
	} {
		sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			w.Write([]byte(`{"message":"nope"}`))
		}))
		_, err := New(sock, time.Second).CreateExec(context.Background(), "abc", ExecSpec{Cmd: []string{"sh"}})
		if !errors.Is(err, tc.want) {
			t.Errorf("status %d: err = %v, want %v", tc.status, err, tc.want)
		}
	}
}

// hijack answers 101 and hands the raw connection to fn, after checking the
// upgrade request the client made.
func hijack(t *testing.T, wantBody string, fn func(conn net.Conn, br *bufio.ReadWriter)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1.41/exec/e1/start" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Upgrade") != "tcp" || r.Header.Get("Connection") != "Upgrade" {
			t.Errorf("upgrade headers = %q / %q", r.Header.Get("Upgrade"), r.Header.Get("Connection"))
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, int64(len(wantBody))))
		if string(body) != wantBody {
			t.Errorf("body = %q, want %q", body, wantBody)
		}
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		brw.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
		brw.Flush()
		fn(conn, brw)
	})
}

func TestStartExecTTYRoundTrip(t *testing.T) {
	sock := serve(t, hijack(t, `{"Detach":false,"Tty":true}`, func(conn net.Conn, brw *bufio.ReadWriter) {
		buf := make([]byte, 16)
		n, _ := brw.Read(buf)
		conn.Write(buf[:n])
	}))
	s, err := New(sock, time.Second).StartExec(context.Background(), "e1", true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	stream, p, err := s.ReadFrame()
	if err != nil || stream != 1 || string(p) != "hi" {
		t.Errorf("ReadFrame = (%d, %q, %v)", stream, p, err)
	}
}

func TestStartExecNonTTYDemux(t *testing.T) {
	sock := serve(t, hijack(t, `{"Detach":false,"Tty":false}`, func(conn net.Conn, _ *bufio.ReadWriter) {
		conn.Write(frame(1, "out"))
		conn.Write(frame(2, "err"))
	}))
	s, err := New(sock, time.Second).StartExec(context.Background(), "e1", false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, want := range []struct {
		stream byte
		text   string
	}{{1, "out"}, {2, "err"}} {
		stream, p, err := s.ReadFrame()
		if err != nil || stream != want.stream || string(p) != want.text {
			t.Errorf("ReadFrame = (%d, %q, %v), want (%d, %q)", stream, p, err, want.stream, want.text)
		}
	}
	if _, _, err := s.ReadFrame(); err != io.EOF {
		t.Errorf("final err = %v, want io.EOF", err)
	}
}

func TestStartExecCloseWrite(t *testing.T) {
	sawEOF := make(chan bool, 1)
	sock := serve(t, hijack(t, `{"Detach":false,"Tty":true}`, func(conn net.Conn, brw *bufio.ReadWriter) {
		_, err := io.Copy(io.Discard, brw) // returns once stdin is half-closed
		sawEOF <- err == nil
		conn.Write([]byte("bye"))
	}))
	s, err := New(sock, time.Second).StartExec(context.Background(), "e1", true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	select {
	case ok := <-sawEOF:
		if !ok {
			t.Error("fake read failed instead of EOF")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fake never saw EOF")
	}
	_, p, err := s.ReadFrame()
	if err != nil || string(p) != "bye" {
		t.Errorf("ReadFrame after CloseWrite = (%q, %v)", p, err)
	}
}

func TestStartExecNotRunning(t *testing.T) {
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"message":"container is not running"}`))
	}))
	_, err := New(sock, time.Second).StartExec(context.Background(), "e1", true)
	if !errors.Is(err, ErrNotRunning) {
		t.Errorf("err = %v, want ErrNotRunning", err)
	}
}

func TestResizeExecQuery(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
	}))
	if err := New(sock, time.Second).ResizeExec(context.Background(), "e1", 120, 40); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1.41/exec/e1/resize" || gotQuery != "h=40&w=120" {
		t.Errorf("request = %s %s?%s", gotMethod, gotPath, gotQuery)
	}
}

func TestInspectExec(t *testing.T) {
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1.41/exec/e1/json" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Write([]byte(`{"Running":false,"ExitCode":3,"Pid":0}`))
	}))
	got, err := New(sock, time.Second).InspectExec(context.Background(), "e1")
	if err != nil {
		t.Fatal(err)
	}
	if want := (ExecInspect{Running: false, ExitCode: 3, Pid: 0}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
