package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

func TestOpsLsRunningFilter(t *testing.T) {
	var queries []string
	end := time.Date(2026, 10, 14, 11, 59, 30, 0, time.UTC)
	exit := 0
	finished := t8Op("succeeded")
	finished.ID, finished.EndedAt, finished.ExitCode = "op0", &end, &exit
	te := t8Setup(t, map[string]http.HandlerFunc{"GET /api/operations": func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		if r.URL.Query().Get("state") == "running" {
			t8JSON(w, 200, []apitypes.Operation{t8Op("running")})
			return
		}
		t8JSON(w, 200, []apitypes.Operation{t8Op("running"), finished})
	}})
	if err := t8Run(te, "ops", "ls", "--running"); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 1 || queries[0] != "state=running" {
		t.Errorf("queries = %q", queries)
	}
	lines := strings.Split(strings.TrimSpace(te.Out.String()), "\n")
	if len(lines) != 2 || strings.Join(strings.Fields(lines[0]), " ") != "ID STACK ACTION STATE STARTED DURATION EXIT" {
		t.Fatalf("stdout = %q", te.Out.String())
	}
	if f := strings.Fields(lines[1]); f[0] != "op1" || f[1] != "web" || f[2] != "up" || f[3] != "running" {
		t.Errorf("row = %q", lines[1])
	}

	te.Out.buf.Reset()
	if err := t8Run(te, "ops", "ls"); err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSpace(te.Out.String()), "\n")
	if queries[1] != "" || len(rows) != 3 {
		t.Fatalf("unfiltered: query %q, stdout %q", queries[1], te.Out.String())
	}
	if got := strings.Join(strings.Fields(rows[2]), " "); !strings.HasPrefix(got, "op0 web up succeeded") || !strings.HasSuffix(got, "30s 0") {
		t.Errorf("finished row = %q, want duration 30s and exit 0", got)
	}
}

func TestOpsShowPrintsHeaderAndLines(t *testing.T) {
	detail := apitypes.OperationDetail{Operation: t8Op("failed"), Lines: []string{"first", "sec\x1b[31mond"}}
	detail.Error = "exit status 1"
	te := t8Setup(t, map[string]http.HandlerFunc{"GET /api/operations/op1": func(w http.ResponseWriter, r *http.Request) { t8JSON(w, 200, detail) }})
	if err := t8Run(te, "ops", "show", "op1"); err != nil {
		t.Fatal(err)
	}
	out := te.Out.String()
	for _, want := range []string{"op1", "web", "up", "failed", "exit status 1", "first\n", "second\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("control characters in output: %q", out)
	}
}

func TestOpsStreamFinishedOp(t *testing.T) {
	st := &t8Stream{scripts: [][]t8Event{{t8Line(0, "old line"), t8Done(t8Op("succeeded"))}}}
	te := t8Setup(t, map[string]http.HandlerFunc{
		"GET /api/operations/op1": func(w http.ResponseWriter, r *http.Request) {
			t8JSON(w, 200, apitypes.OperationDetail{Operation: t8Op("succeeded")})
		},
		"GET /api/operations/op1/stream": st.handler,
	})
	if err := t8Run(te, "ops", "stream", "op1"); err != nil {
		t.Fatal(err)
	}
	if te.Out.String() != "old line\n" || !strings.Contains(te.Err.String(), "up web succeeded") {
		t.Errorf("stdout = %q, stderr = %q", te.Out.String(), te.Err.String())
	}

	// A finished-but-failed operation exits by its state.
	failed := &t8Stream{scripts: [][]t8Event{{t8Done(t8Op("failed"))}}}
	te = t8Setup(t, map[string]http.HandlerFunc{
		"GET /api/operations/op1": func(w http.ResponseWriter, r *http.Request) {
			t8JSON(w, 200, apitypes.OperationDetail{Operation: t8Op("failed")})
		},
		"GET /api/operations/op1/stream": failed.handler,
	})
	if err := t8Run(te, "ops", "stream", "op1"); t8Code(err) != ExitOperationFailed {
		t.Errorf("code = %d (%v), want 9", t8Code(err), err)
	}
}

func TestOpsStreamUnknownIDExits4(t *testing.T) {
	te := t8Setup(t, map[string]http.HandlerFunc{"GET /api/operations/nope": func(w http.ResponseWriter, r *http.Request) {
		t8JSON(w, 404, apitypes.ErrorBody{Error: "operation not found"})
	}})
	if err := t8Run(te, "ops", "stream", "nope"); t8Code(err) != ExitNotFound {
		t.Errorf("code = %d (%v), want 4", t8Code(err), err)
	}
}

func TestOpsCancel(t *testing.T) {
	te := t8Setup(t, map[string]http.HandlerFunc{"POST /api/operations/op1/cancel": func(w http.ResponseWriter, r *http.Request) {
		t8JSON(w, 200, t8Op("cancelled"))
	}})
	if err := t8Run(te, "ops", "cancel", "op1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(te.Out.String(), "cancelled") {
		t.Errorf("stdout = %q, want the returned state", te.Out.String())
	}
	te.Out.buf.Reset()
	if err := t8Run(te, "ops", "cancel", "op1", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	var op apitypes.Operation
	if err := json.Unmarshal([]byte(te.Out.String()), &op); err != nil || op.State != "cancelled" {
		t.Errorf("json = %q (%v)", te.Out.String(), err)
	}
}
