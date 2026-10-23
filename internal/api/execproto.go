package api

import (
	"encoding/json"
	"errors"
	"time"

	"golang.org/x/net/websocket"
)

// Binary frame stream IDs. The first byte of every binary frame.
const (
	streamStdin  byte = 0 // client to server
	streamStdout byte = 1 // server to client
	streamStderr byte = 2 // server to client
)

// maxExecFramePayload bounds one inbound WebSocket frame.
const maxExecFramePayload = 1 << 20

// controlMsg is a JSON text frame, in either direction.
type controlMsg struct {
	Type    string `json:"type"`
	Cols    int    `json:"cols,omitempty"`
	Rows    int    `json:"rows,omitempty"`
	Code    *int   `json:"code,omitempty"` // pointer so an exit code of 0 is still sent
	Seconds int    `json:"seconds,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// wsConn is the exec session's view of a WebSocket. Reads and writes may run
// concurrently, but writes are serialised by the session, not by the conn.
type wsConn interface {
	ReadFrame() (text bool, data []byte, err error)
	WriteBinary(p []byte) error
	WriteText(v controlMsg) error
	Close() error
}

// inboundFrame receives one frame and remembers whether it was text.
type inboundFrame struct {
	text bool
	data []byte
}

// frameCodec sends []byte as a binary frame and controlMsg as a JSON text
// frame; on receive it captures the payload type and raw bytes.
var frameCodec = websocket.Codec{
	Marshal: func(v any) ([]byte, byte, error) {
		switch m := v.(type) {
		case []byte:
			return m, websocket.BinaryFrame, nil
		case controlMsg:
			raw, err := json.Marshal(m)
			return raw, websocket.TextFrame, err
		}
		return nil, 0, errors.New("exec: unsupported frame value")
	},
	Unmarshal: func(data []byte, payloadType byte, v any) error {
		f, ok := v.(*inboundFrame)
		if !ok {
			return errors.New("exec: unsupported receive target")
		}
		f.text = payloadType == websocket.TextFrame
		f.data = data
		return nil
	},
}

// defaultWriteTimeout bounds one write when the caller passes no timeout.
const defaultWriteTimeout = 60 * time.Second

type xnetConn struct {
	c            *websocket.Conn
	writeTimeout time.Duration
}

// newWSConn adapts an x/net WebSocket, caps inbound frame size and bounds each
// write by writeTimeout (default 60s). Sessions are unbounded, but a write to
// a client that vanished silently must not block forever; the deadline error
// is treated as the client being gone.
func newWSConn(c *websocket.Conn, writeTimeout time.Duration) wsConn {
	c.MaxPayloadBytes = maxExecFramePayload
	if writeTimeout <= 0 {
		writeTimeout = defaultWriteTimeout
	}
	return &xnetConn{c: c, writeTimeout: writeTimeout}
}

func (x *xnetConn) send(v any) error {
	if err := x.c.SetWriteDeadline(time.Now().Add(x.writeTimeout)); err != nil {
		return err
	}
	return frameCodec.Send(x.c, v)
}

func (x *xnetConn) ReadFrame() (bool, []byte, error) {
	var f inboundFrame
	if err := frameCodec.Receive(x.c, &f); err != nil {
		return false, nil, err
	}
	return f.text, f.data, nil
}

func (x *xnetConn) WriteBinary(p []byte) error   { return x.send(p) }
func (x *xnetConn) WriteText(v controlMsg) error { return x.send(v) }
func (x *xnetConn) Close() error                 { return x.c.Close() }
