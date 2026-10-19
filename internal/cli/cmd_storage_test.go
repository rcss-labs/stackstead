package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

func storageRun(te *testEnv, args ...string) error {
	return Run(context.Background(), rootCommand(), args, te.Env)
}

func TestStorageDF(t *testing.T) {
	size, reclaim := int64(2048), int64(1024)
	at := time.Date(2026, 10, 14, 11, 55, 0, 0, time.UTC)
	overview := apitypes.StorageOverview{Images: apitypes.StorageKindTotals{Count: 2, Size: &size, Reclaimable: &reclaim, SizesAt: &at}, Volumes: apitypes.StorageKindTotals{Count: 3, Refreshing: true}}
	for _, output := range []string{"table", "json"} {
		t.Run(output, func(t *testing.T) {
			te := t8Setup(t, map[string]http.HandlerFunc{"GET /api/storage": func(w http.ResponseWriter, r *http.Request) { t8JSON(w, 200, overview) }})
			if err := storageRun(te, "storage", "df", "-o", output); err != nil {
				t.Fatal(err)
			}
			if output == "json" {
				var got apitypes.StorageOverview
				if err := json.Unmarshal([]byte(te.Out.String()), &got); err != nil || got.Images.Count != 2 || got.Images.Size == nil || *got.Images.Size != 2048 || !got.Volumes.Refreshing {
					t.Fatalf("overview %q: %v", te.Out.String(), err)
				}
				return
			}
			out := te.Out.String()
			for _, want := range []string{"KIND COUNT SIZE RECLAIMABLE AS OF", "images 2 2.0 KiB 1.0 KiB 5m ago", "containers 0 — — —", "volumes 3 — — — refreshing", "build-cache 0 — — —"} {
				if !strings.Contains(strings.Join(strings.Fields(out), " "), want) {
					t.Errorf("missing %q in %q", want, out)
				}
			}
		})
	}
}

func TestStoragePrune(t *testing.T) {
	for _, tt := range []struct {
		name, kind, input         string
		flags                     []string
		terminal, empty, fallback bool
		code, confirms            int
	}{
		{name: "preview", kind: "volumes", flags: []string{"--preview"}},
		{name: "nonterminal", kind: "images", code: 2},
		{name: "mismatch", kind: "volumes", terminal: true, input: "delete 2 volume\n", code: 6},
		{name: "yes", kind: "volumes", flags: []string{"--yes", "--detach"}, confirms: 1},
		{name: "typed", kind: "images", terminal: true, input: "delete 2 volumes\n", flags: []string{"--detach"}, confirms: 1},
		{name: "empty", kind: "volumes", empty: true},
		{name: "fallback", kind: "build-cache", flags: []string{"--preview"}, fallback: true},
		{name: "unknown", kind: "containers", code: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			n := int64(2048)
			plan := apitypes.StoragePlan{ID: "p_123", Kind: tt.kind, Items: []apitypes.StoragePlanItem{{Kind: tt.kind, ID: "id1", Ref: "old-one", Bytes: &n}, {Kind: tt.kind, ID: "id2", Ref: "old-two"}}, Protected: []apitypes.StorageProtectedItem{{Ref: "keep", Reason: "declared by stack", Stack: "db", StackState: "stopped"}}, Warnings: []string{"inventory warning\x1b[31m"}, Reclaim: apitypes.StorageReclaim{Min: &n, Max: &n, Exact: true}, ConfirmPhrase: "delete 2 volumes"}
			if tt.empty {
				plan.Items = nil
			}
			if tt.fallback {
				plan.Mode = "all-unused"
				plan.Reclaim.Exact = false
			}
			plans, confirms := 0, 0
			te := t8Setup(t, map[string]http.HandlerFunc{
				"POST /api/storage/plans": func(w http.ResponseWriter, r *http.Request) {
					plans++
					var req apitypes.StoragePlanRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
					}
					selection := "unused"
					if tt.kind == "images" {
						selection = "dangling"
					}
					if req.Kind != tt.kind || req.Selection != selection || req.Ref != "" || req.Name != "" {
						t.Errorf("bulk request %+v", req)
					}
					t8JSON(w, 201, plan)
				},
				"POST /api/storage/plans/p_123/confirm": func(w http.ResponseWriter, r *http.Request) {
					confirms++
					var req apitypes.StorageConfirmRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					if req.Confirm != "delete 2 volumes" {
						t.Errorf("phrase %q", req.Confirm)
					}
					op := t8Op("running")
					op.Stack = ""
					op.Action = "storage-cleanup"
					t8JSON(w, 202, op)
				},
			})
			te.IsTerminal = func(uintptr) bool { return tt.terminal }
			te.Stdin = strings.NewReader(tt.input)
			args := append([]string{"storage", "prune", tt.kind}, tt.flags...)
			err := storageRun(te, args...)
			if codeFor(err) != tt.code {
				t.Fatalf("code %d want %d: %v", codeFor(err), tt.code, err)
			}
			if confirms != tt.confirms {
				t.Errorf("confirmed %d times want %d", confirms, tt.confirms)
			}
			if tt.kind == "containers" {
				if plans != 0 {
					t.Fatal("unknown kind made plan")
				}
				return
			}
			if plans != 1 {
				t.Fatalf("plan calls %d", plans)
			}
			out := te.Out.String()
			if tt.empty {
				if !strings.Contains(out, "nothing to clean") {
					t.Errorf("empty output %q", out)
				}
				return
			}
			for _, want := range []string{"old-one", "old-two", "keep", "declared by stack", "db", "stopped", "inventory warning", "Reclaim", "2.0 KiB"} {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q in %q", want, out)
				}
			}
			if strings.Contains(out, "\x1b") {
				t.Errorf("unsafe output %q", out)
			}
			if tt.fallback && !strings.Contains(out, "not exact") {
				t.Errorf("fallback %q", out)
			}
			if tt.confirms > 0 && !strings.Contains(out, "op1") {
				t.Errorf("no detached id: %q", out)
			}
			if tt.code == 2 && !strings.Contains(err.Error(), "refusing to continue without confirmation: use --yes") {
				t.Error(err)
			}
			if tt.terminal && !strings.Contains(te.Err.String(), `Type "delete 2 volumes" to confirm:`) {
				t.Errorf("prompt %q", te.Err.String())
			}
		})
	}
}

func TestStoragePruneFollow(t *testing.T) {
	for _, state := range []string{"succeeded", "failed"} {
		t.Run(state, func(t *testing.T) {
			final := t8Op(state)
			final.Stack = ""
			final.Action = "storage-cleanup"
			st := &t8Stream{scripts: [][]t8Event{{t8Line(0, "cleaned"), t8Done(final)}}}
			te := t8Setup(t, map[string]http.HandlerFunc{
				"POST /api/storage/plans": func(w http.ResponseWriter, r *http.Request) {
					t8JSON(w, 201, apitypes.StoragePlan{ID: "p_123", Items: []apitypes.StoragePlanItem{{Ref: "old"}}, ConfirmPhrase: "delete 1 volume"})
				},
				"POST /api/storage/plans/p_123/confirm": func(w http.ResponseWriter, r *http.Request) { t8JSON(w, 202, final) },
				"GET /api/operations/op1/stream":        st.handler,
			})
			err := storageRun(te, "storage", "prune", "volumes", "--yes")
			want := 0
			if state == "failed" {
				want = 9
			}
			if codeFor(err) != want {
				t.Fatalf("code %d: %v", codeFor(err), err)
			}
			if !strings.Contains(te.Out.String(), "cleaned") {
				t.Error(te.Out.String())
			}
		})
	}
}

func TestStoragePruneAPIErrors(t *testing.T) {
	for _, stage := range []string{"plan", "confirm"} {
		for _, tt := range []struct{ status, code int }{{409, 5}, {400, 6}, {503, 7}, {429, 8}} {
			t.Run(fmt.Sprintf("%s-%d", stage, tt.status), func(t *testing.T) {
				failure := func(w http.ResponseWriter, r *http.Request) {
					t8JSON(w, tt.status, apitypes.ErrorBody{Error: "refused", Fields: []apitypes.FieldError{{Field: "confirm", Message: "wrong phrase"}}})
				}
				routes := map[string]http.HandlerFunc{"POST /api/storage/plans": func(w http.ResponseWriter, r *http.Request) {
					t8JSON(w, 201, apitypes.StoragePlan{ID: "p_123", Items: []apitypes.StoragePlanItem{{Ref: "old"}}, ConfirmPhrase: "delete 1 volume"})
				}, "POST /api/storage/plans/p_123/confirm": failure}
				if stage == "plan" {
					routes["POST /api/storage/plans"] = failure
				}
				te := t8Setup(t, routes)
				if err := storageRun(te, "storage", "prune", "volumes", "--yes", "--detach"); codeFor(err) != tt.code {
					t.Fatalf("code %d want %d: %v", codeFor(err), tt.code, err)
				}
			})
		}
	}
}

func TestStorageHostOperations(t *testing.T) {
	op := t8Op("succeeded")
	op.Stack = ""
	op.Action = "storage-cleanup"
	for _, args := range [][]string{{"ops", "ls"}, {"ops", "show", "op1"}} {
		te := t8Setup(t, map[string]http.HandlerFunc{"GET /api/operations": func(w http.ResponseWriter, r *http.Request) { t8JSON(w, 200, []apitypes.Operation{op}) }, "GET /api/operations/op1": func(w http.ResponseWriter, r *http.Request) { t8JSON(w, 200, apitypes.OperationDetail{Operation: op}) }})
		if err := storageRun(te, args...); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(te.Out.String(), "host") {
			t.Errorf("%v: %q", args, te.Out.String())
		}
	}
}

func TestStorageTypedConfirmation(t *testing.T) {
	for _, tt := range []struct {
		name, input   string
		yes, terminal bool
		code          int
	}{
		{name: "exact", input: "delete 2 volumes\n", terminal: true},
		{name: "EOF exact", input: "delete 2 volumes", terminal: true},
		{name: "CR preserved", input: "delete 2 volumes\r\n", terminal: true, code: 6},
		{name: "bare CR preserved", input: "delete 2 volumes\r", terminal: true, code: 6},
		{name: "space preserved", input: "delete 2 volumes \n", terminal: true, code: 6},
		{name: "leading space", input: " delete 2 volumes\n", terminal: true, code: 6},
		{name: "case preserved", input: "Delete 2 volumes\n", terminal: true, code: 6},
		{name: "missing plural", input: "delete 2 volume\n", terminal: true, code: 6},
		{name: "empty", input: "\n", terminal: true, code: 6},
		{name: "nonterminal", input: "delete 2 volumes\n", code: 2},
		{name: "yes", yes: true},
		{name: "bounded", input: strings.Repeat("x", maxSecretBytes+1), terminal: true, code: 6},
	} {
		t.Run(tt.name, func(t *testing.T) {
			te := newTestEnv(t, nil)
			te.IsTerminal = func(uintptr) bool { return tt.terminal }
			te.Stdin = strings.NewReader(tt.input)
			err := confirmTyped(te.Env, "delete 2 volumes", tt.yes)
			if codeFor(err) != tt.code {
				t.Fatalf("code %d want %d: %v", codeFor(err), tt.code, err)
			}
			if tt.yes && te.Err.String() != "" {
				t.Errorf("yes prompted: %q", te.Err.String())
			}
		})
	}
}
