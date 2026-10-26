package cli

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

func backupRun(te *testEnv, args ...string) error {
	return Run(context.Background(), &Command{Name: "stackstead", Sub: []*Command{backupCommand()}}, args, te.Env)
}

func backupOp(state string) apitypes.Operation {
	op := t8Op(state)
	op.Stack, op.Action = "", "backup"
	return op
}

func backupRoutes(final apitypes.Operation, runs []apitypes.BackupRun) map[string]http.HandlerFunc {
	st := &t8Stream{scripts: [][]t8Event{{t8Line(0, "backup started"), t8Done(final)}}}
	return map[string]http.HandlerFunc{
		"POST /api/backups/run": func(w http.ResponseWriter, r *http.Request) {
			t8JSON(w, 202, apitypes.BackupRunStarted{Operation: backupOp("running")})
		},
		"GET /api/operations/op1/stream": st.handler,
		"GET /api/backups/runs":          func(w http.ResponseWriter, r *http.Request) { t8JSON(w, 200, runs) },
	}
}

func TestBackupRunCommandStreamsAndExitCode(t *testing.T) {
	failed := backupOp("failed")
	failed.Error = "disk full"
	te := t8Setup(t, backupRoutes(failed, nil))
	err := backupRun(te, "backup", "run")
	if got := t8Code(err); got != ExitOperationFailed {
		t.Fatalf("exit %d, want %d (err %v)", got, ExitOperationFailed, err)
	}
	if !strings.Contains(te.Out.String(), "backup started") {
		t.Errorf("stdout %q", te.Out.String())
	}

	te = t8Setup(t, backupRoutes(backupOp("succeeded"), []apitypes.BackupRun{{Status: "success"}}))
	if err := backupRun(te, "backup", "run"); err != nil {
		t.Fatalf("success run: %v", err)
	}

	partial := []apitypes.BackupRun{{Status: "partial", Items: []apitypes.BackupItem{{Status: "ok"}, {Status: "failed"}}}}
	te = t8Setup(t, backupRoutes(backupOp("succeeded"), partial))
	err = backupRun(te, "backup", "run")
	if got := t8Code(err); got != ExitOperationFailed {
		t.Fatalf("partial exit %d, want %d (err %v)", got, ExitOperationFailed, err)
	}
	if err == nil || !strings.Contains(err.Error(), "1 item(s) failed") {
		t.Errorf("summary missing: %v", err)
	}

	te = t8Setup(t, backupRoutes(backupOp("succeeded"), nil))
	if err := backupRun(te, "backup", "run", "--detach"); err != nil || !strings.Contains(te.Out.String(), "operation op1 started") {
		t.Fatalf("detach: %v %q", err, te.Out.String())
	}
}

func TestBackupListTable(t *testing.T) {
	archives := []apitypes.BackupArchive{
		{Dest: "local", Name: "a.tar.zst", HostSlug: "lab", CreatedAt: time.Date(2026, 10, 14, 11, 55, 0, 0, time.UTC), Size: 2048},
	}
	te := t8Setup(t, map[string]http.HandlerFunc{"GET /api/backups/archives": func(w http.ResponseWriter, r *http.Request) { t8JSON(w, 200, archives) }})
	if err := backupRun(te, "backup", "list"); err != nil {
		t.Fatal(err)
	}
	out := strings.Join(strings.Fields(te.Out.String()), " ")
	for _, want := range []string{"HOST TIME SIZE DESTINATION", "lab 5m ago 2.0 KiB local"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
}
