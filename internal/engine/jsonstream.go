package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// maxJSONLine caps one line of a Docker JSON message stream.
const maxJSONLine = 1 << 20

var errJSONLineTooLong = errors.New("docker message line too long (over 1 MiB)")

// jsonMessage is one message of Docker's build, push and load streams.
type jsonMessage struct {
	Stream      string `json:"stream"`
	Status      string `json:"status"`
	ID          string `json:"id"`
	Progress    string `json:"progress"`
	Error       string `json:"error"`
	ErrorDetail struct {
		Message string `json:"message"`
	} `json:"errorDetail"`
	Aux json.RawMessage `json:"aux"`
}

// errorText is the message of an error entry, or "" when there is none.
func (m jsonMessage) errorText() string {
	if m.ErrorDetail.Message != "" {
		return m.ErrorDetail.Message
	}
	return m.Error
}

// lineLimit fails a read once more than maxJSONLine bytes arrive without a newline.
type lineLimit struct {
	r   io.Reader
	run int
}

func (l *lineLimit) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	first := bytes.IndexByte(p[:n], '\n')
	if first < 0 {
		first = n
	}
	if l.run+first > maxJSONLine {
		return 0, errJSONLineTooLong
	}
	if last := bytes.LastIndexByte(p[:n], '\n'); last >= 0 {
		l.run = n - last - 1
	} else {
		l.run += n
	}
	if l.run > maxJSONLine {
		return 0, errJSONLineTooLong
	}
	return n, err
}

// decodeJSONStream calls fn for each message until EOF. fn returning an error
// stops the stream with that error.
func decodeJSONStream(r io.Reader, fn func(msg jsonMessage) error) error {
	dec := json.NewDecoder(&lineLimit{r: r})
	for {
		var msg jsonMessage
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			var syntax *json.SyntaxError
			if errors.As(err, &syntax) {
				return fmt.Errorf("decode docker message: %w", err)
			}
			return err
		}
		if err := fn(msg); err != nil {
			return err
		}
	}
}
