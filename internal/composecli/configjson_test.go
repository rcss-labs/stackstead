package composecli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigJSONArgumentsDirectoryAndWarnings(t *testing.T) {
	r, file := newRunner(t, "args")
	got, err := r.ConfigJSON(ctxT(t), file)
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := filepath.EvalSymlinks(filepath.Dir(file))
	if !strings.Contains(string(got), "ARGS: compose -f "+file+" config --format json") || !strings.Contains(string(got), "PWD: "+dir) {
		t.Fatalf("unexpected invocation %s", got)
	}
	r.Env = []string{"FAKE_MODE=project"}
	got, err = r.ConfigJSON(ctxT(t), file)
	if err != nil || strings.Contains(string(got), "warning") {
		t.Fatalf("stdout %q err %v", got, err)
	}
}

func TestConfigJSONFailureNeverIncludesStdout(t *testing.T) {
	r, file := newRunner(t, "fail")
	if err := os.WriteFile(r.Binary, []byte("#!/bin/sh\necho hunter2\necho 'bad config' >&2\nexit 17\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := r.ConfigJSON(ctxT(t), file)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 17 {
		t.Fatalf("error %v", err)
	}
	if len(got) != 0 || strings.Contains(err.Error(), "hunter2") || !strings.Contains(err.Error(), "bad config") {
		t.Fatalf("output %q error %v", got, err)
	}
}
