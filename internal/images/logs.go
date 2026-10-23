package images

import (
	"io"
	"os"
	"path/filepath"
	"sync"
)

const (
	maxLogBytes     = 8 << 20
	truncatedMarker = "…(truncated)"
)

func (s *Service) logPath(slug, versionID string) string {
	return filepath.Join(s.imageDir(slug), "logs", versionID+".log")
}

// buildLog appends build output to a version's log file as lines arrive, so a
// crash keeps the partial log. Output beyond the cap is replaced by one
// truncation line.
type buildLog struct {
	mu        sync.Mutex
	f         *os.File
	written   int64
	truncated bool
}

func openBuildLog(path string) (*buildLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	return &buildLog{f: f}, nil
}

func (l *buildLog) line(text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.truncated {
		return
	}
	if l.written+int64(len(text))+1 > maxLogBytes {
		l.truncated = true
		l.f.WriteString(truncatedMarker + "\n")
		return
	}
	n, _ := l.f.WriteString(text + "\n")
	l.written += int64(n)
}

func (l *buildLog) close() { l.f.Close() }

// readLog returns a version's log, or "" when it has none.
func (s *Service) readLog(slug, versionID string) (string, error) {
	f, err := os.Open(s.logPath(slug, versionID))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxLogBytes+64))
	return string(b), err
}
