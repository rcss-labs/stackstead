package cli

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a bytes.Buffer safe for a server goroutine writing logs
// while the test reads them.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Reset drops what was written so far, for a test that runs a server twice.
func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

type testEnv struct {
	*Env
	Out, Err *syncBuffer
}

// newTestEnv returns an Env with captured output, a fixed clock, no
// terminal, and an environment backed by vars.
func newTestEnv(t *testing.T, vars map[string]string) *testEnv {
	t.Helper()
	out, errOut := &syncBuffer{}, &syncBuffer{}
	return &testEnv{
		Env: &Env{
			Stdin:      bytes.NewReader(nil),
			Stdout:     out,
			Stderr:     errOut,
			Getenv:     func(k string) string { return vars[k] },
			Now:        func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) },
			IsTerminal: func(uintptr) bool { return false },
			ConfigDir:  t.TempDir(),
			Args0:      "stackstead",
		},
		Out: out, Err: errOut,
	}
}
