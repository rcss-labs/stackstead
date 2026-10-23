package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

func imagesRun(te *testEnv, args ...string) error {
	return Run(context.Background(), &Command{Name: "stackstead", Sub: []*Command{imagesCommand()}}, args, te.Env)
}

func imageDetail(suggested, hint string, targets ...apitypes.PushTarget) apitypes.LibraryImageDetail {
	d := apitypes.LibraryImageDetail{SuggestedTag: suggested, TagHint: hint}
	d.Slug, d.PushTargets = "web", targets
	return d
}

func buildStarted() apitypes.BuildStarted {
	op := t8Op("running")
	op.Action = "build"
	return apitypes.BuildStarted{Operation: op}
}

func TestImagesLsTable(t *testing.T) {
	built := time.Date(2026, 10, 14, 11, 55, 0, 0, time.UTC)
	lib := apitypes.Library{Images: []apitypes.LibraryImage{
		{Slug: "web", Source: "upload", Owner: apitypes.LibraryServer{Name: "Host A"},
			LatestVersion: &apitypes.LibraryVersion{Tag: "3"},
			LastBuild:     &apitypes.LibraryVersion{Status: "succeeded", StartedAt: built},
			PushTargets:   []apitypes.PushTarget{{RegistryName: "hub"}, {RegistryName: "ecr"}}},
		{Slug: "api", Source: "github", Owner: apitypes.LibraryServer{Name: "Host B"}},
	}}
	te := t8Setup(t, map[string]http.HandlerFunc{"GET /api/images": func(w http.ResponseWriter, r *http.Request) { t8JSON(w, 200, lib) }})
	if err := imagesRun(te, "images", "ls"); err != nil {
		t.Fatal(err)
	}
	out := strings.Join(strings.Fields(te.Out.String()), " ")
	for _, want := range []string{"NAME SOURCE OWNER LATEST LAST BUILD PUSH TARGETS", "web upload Host A 3 succeeded 5m ago hub, ecr", "api github Host B - - -"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}

	te = t8Setup(t, map[string]http.HandlerFunc{"GET /api/images": func(w http.ResponseWriter, r *http.Request) { t8JSON(w, 200, lib) }})
	if err := imagesRun(te, "images", "ls", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	var got apitypes.Library
	if err := json.Unmarshal([]byte(te.Out.String()), &got); err != nil || len(got.Images) != 2 {
		t.Fatalf("json %q: %v", te.Out.String(), err)
	}
}

func TestImagesBuildUsesSuggestedTag(t *testing.T) {
	var req apitypes.BuildRequest
	st := &t8Stream{scripts: [][]t8Event{{t8Line(0, "building"), t8Done(func() apitypes.Operation { o := t8Op("succeeded"); o.Action = "build"; return o }())}}}
	te := t8Setup(t, map[string]http.HandlerFunc{
		"GET /api/images/web": func(w http.ResponseWriter, r *http.Request) { t8JSON(w, 200, imageDetail("4", "")) },
		"POST /api/images/web/builds": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&req)
			t8JSON(w, 202, buildStarted())
		},
		"GET /api/operations/op1/stream": st.handler,
	})
	if err := imagesRun(te, "images", "build", "web", "--ref", "main"); err != nil {
		t.Fatal(err)
	}
	if req.Tag != "4" || req.Ref != "main" || len(req.BuildArgs) != 0 {
		t.Errorf("request %+v", req)
	}
	if !strings.Contains(te.Out.String(), "building") {
		t.Errorf("stdout %q", te.Out.String())
	}
}

func TestImagesBuildNoSuggestionIsUsageError(t *testing.T) {
	built := false
	te := t8Setup(t, map[string]http.HandlerFunc{
		"GET /api/images/web":         func(w http.ResponseWriter, r *http.Request) { t8JSON(w, 200, imageDetail("", "nightly")) },
		"POST /api/images/web/builds": func(w http.ResponseWriter, r *http.Request) { built = true },
	})
	err := imagesRun(te, "images", "build", "web")
	if codeFor(err) != ExitUsage || !strings.Contains(err.Error(), "nightly") || !strings.Contains(err.Error(), "--tag") {
		t.Fatalf("code %d err %v", codeFor(err), err)
	}
	if built {
		t.Error("build was requested")
	}
}

func TestImagesBuildArgsParsed(t *testing.T) {
	var req apitypes.BuildRequest
	te := t8Setup(t, map[string]http.HandlerFunc{
		"POST /api/images/web/builds": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&req)
			t8JSON(w, 202, buildStarted())
		},
	})
	if err := imagesRun(te, "images", "build", "web", "--tag", "v2", "--build-arg", "A=1", "--build-arg", "B=x=y", "--detach"); err != nil {
		t.Fatal(err)
	}
	if req.Tag != "v2" || req.BuildArgs["A"] != "1" || req.BuildArgs["B"] != "x=y" || len(req.BuildArgs) != 2 {
		t.Errorf("request %+v", req)
	}
	if !strings.Contains(te.Out.String(), "op1") {
		t.Errorf("no operation id: %q", te.Out.String())
	}

	te = t8Setup(t, nil)
	err := imagesRun(te, "images", "build", "web", "--tag", "v2", "--build-arg", "NOEQUALS")
	if codeFor(err) != ExitUsage {
		t.Fatalf("code %d err %v", codeFor(err), err)
	}

	te = t8Setup(t, nil)
	err = imagesRun(te, "images", "build", "web", "--tag", "v2", "--build-arg", "=V")
	if codeFor(err) != ExitUsage {
		t.Fatalf("empty key: code %d err %v", codeFor(err), err)
	}
}

func TestBuildArgFlagParse(t *testing.T) {
	args := buildArgFlag{}
	for _, v := range []string{"K=", "A=1", "A=2"} {
		if err := args.Set(v); err != nil {
			t.Fatalf("Set(%q): %v", v, err)
		}
	}
	if v, ok := args["K"]; !ok || v != "" {
		t.Errorf("K= should be allowed with an empty value, got %q, %v", v, ok)
	}
	if args["A"] != "2" {
		t.Errorf("repeated key = %q, want the last value", args["A"])
	}
	if err := args.Set("=V"); err == nil {
		t.Error("=V must be rejected")
	}
}

func TestImagesBuildConflictExit(t *testing.T) {
	for _, tt := range []struct {
		status, code, msg string
		want              int
	}{
		{"409", "tag_taken", "tag 4 is already used", ExitConflict},
		{"409", "build_running", "a build is already running", ExitConflict},
		{"409", "github_unconfigured", "GitHub token is not set", ExitConflict},
		{"429", "github_rate_limited", "GitHub rate limit hit", ExitRateLimited},
		{"502", "peer_unreachable", "owner is unreachable", ExitUnavailable},
		{"502", "github_unreachable", "GitHub is unreachable", ExitUnavailable},
	} {
		t.Run(tt.code, func(t *testing.T) {
			status := map[string]int{"409": 409, "429": 429, "502": 502}[tt.status]
			te := t8Setup(t, map[string]http.HandlerFunc{
				"POST /api/images/web/builds": func(w http.ResponseWriter, r *http.Request) {
					t8JSON(w, status, apitypes.ErrorBody{Error: tt.msg, Code: tt.code})
				},
			})
			err := imagesRun(te, "images", "build", "web", "--tag", "4")
			if codeFor(err) != tt.want || err == nil || !strings.Contains(err.Error(), tt.msg) {
				t.Fatalf("code %d want %d err %v", codeFor(err), tt.want, err)
			}
		})
	}
}

func TestImagesBuildRefRefusedForUpload(t *testing.T) {
	te := t8Setup(t, map[string]http.HandlerFunc{
		"POST /api/images/web/builds": func(w http.ResponseWriter, r *http.Request) {
			t8JSON(w, 422, apitypes.ErrorBody{Error: "invalid request", Fields: []apitypes.FieldError{{Field: "ref", Message: "only GitHub images build at a ref"}}})
		},
	})
	err := imagesRun(te, "images", "build", "web", "--tag", "4", "--ref", "main")
	if codeFor(err) != ExitValidation || !strings.Contains(err.Error(), "only GitHub images build at a ref") {
		t.Fatalf("code %d err %v", codeFor(err), err)
	}
}

func TestImagesPushTargetRequiredWhenAmbiguous(t *testing.T) {
	hub := apitypes.PushTarget{ID: "t_hub", RegistryName: "hub", Repository: "me/web"}
	ecr := apitypes.PushTarget{ID: "t_ecr", RegistryName: "ecr", Repository: "web"}
	var pushed apitypes.PushRequest
	routes := func(d apitypes.LibraryImageDetail) map[string]http.HandlerFunc {
		return map[string]http.HandlerFunc{
			"GET /api/images/web": func(w http.ResponseWriter, r *http.Request) { t8JSON(w, 200, d) },
			"POST /api/images/web/versions/3/push": func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&pushed)
				op := t8Op("running")
				op.Action = "push"
				t8JSON(w, 202, op)
			},
		}
	}

	te := t8Setup(t, routes(imageDetail("4", "", hub, ecr)))
	err := imagesRun(te, "images", "push", "web", "3", "--detach")
	if codeFor(err) != ExitUsage || !strings.Contains(err.Error(), "hub") || !strings.Contains(err.Error(), "ecr") || pushed.Target != "" {
		t.Fatalf("code %d err %v pushed %+v", codeFor(err), err, pushed)
	}

	te = t8Setup(t, routes(imageDetail("4", "", hub, ecr)))
	if err := imagesRun(te, "images", "push", "web", "3", "--target", "ecr", "--detach"); err != nil || pushed.Target != "t_ecr" {
		t.Fatalf("err %v pushed %+v", err, pushed)
	}

	te = t8Setup(t, routes(imageDetail("4", "", hub, ecr)))
	err = imagesRun(te, "images", "push", "web", "3", "--target", "nope", "--detach")
	if codeFor(err) != ExitUsage {
		t.Fatalf("unknown target: code %d err %v", codeFor(err), err)
	}

	pushed = apitypes.PushRequest{}
	te = t8Setup(t, routes(imageDetail("4", "", hub)))
	if err := imagesRun(te, "images", "push", "web", "3", "--detach"); err != nil || pushed.Target != "t_hub" {
		t.Fatalf("single target: err %v pushed %+v", err, pushed)
	}

	te = t8Setup(t, routes(imageDetail("4", "")))
	if err := imagesRun(te, "images", "push", "web", "3"); codeFor(err) != ExitUsage {
		t.Fatalf("no targets: code %d err %v", codeFor(err), err)
	}
}

func TestImagesPushStreams(t *testing.T) {
	done := t8Op("succeeded")
	done.Action = "push"
	st := &t8Stream{scripts: [][]t8Event{{t8Line(0, "pushing layer"), t8Done(done)}}}
	te := t8Setup(t, map[string]http.HandlerFunc{
		"GET /api/images/web": func(w http.ResponseWriter, r *http.Request) {
			t8JSON(w, 200, imageDetail("4", "", apitypes.PushTarget{ID: "t_hub", RegistryName: "hub"}))
		},
		"POST /api/images/web/versions/3/push": func(w http.ResponseWriter, r *http.Request) {
			op := t8Op("running")
			op.Action = "push"
			t8JSON(w, 202, op)
		},
		"GET /api/operations/op1/stream": st.handler,
	})
	if err := imagesRun(te, "images", "push", "web", "3"); err != nil {
		t.Fatal(err)
	}
	if te.Out.String() != "pushing layer\n" {
		t.Errorf("stdout %q", te.Out.String())
	}
}

func TestImagesPushTargetSelectedByRegistryAndRepository(t *testing.T) {
	app := apitypes.PushTarget{ID: "t_app", RegistryName: "hub", Repository: "me/app"}
	debug := apitypes.PushTarget{ID: "t_debug", RegistryName: "hub", Repository: "me/app-debug"}
	var pushed apitypes.PushRequest
	routes := map[string]http.HandlerFunc{
		"GET /api/images/web": func(w http.ResponseWriter, r *http.Request) { t8JSON(w, 200, imageDetail("4", "", app, debug)) },
		"POST /api/images/web/versions/3/push": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&pushed)
			op := t8Op("running")
			op.Action = "push"
			t8JSON(w, 202, op)
		},
	}

	te := t8Setup(t, routes)
	if err := imagesRun(te, "images", "push", "web", "3", "--target", "hub/me/app-debug", "--detach"); err != nil || pushed.Target != "t_debug" {
		t.Fatalf("by registry/repository: err %v pushed %+v", err, pushed)
	}

	te = t8Setup(t, routes)
	if err := imagesRun(te, "images", "push", "web", "3", "--target", "t_app", "--detach"); err != nil || pushed.Target != "t_app" {
		t.Fatalf("by id: err %v pushed %+v", err, pushed)
	}

	pushed = apitypes.PushRequest{}
	te = t8Setup(t, routes)
	err := imagesRun(te, "images", "push", "web", "3", "--target", "hub", "--detach")
	if codeFor(err) != ExitUsage || pushed.Target != "" {
		t.Fatalf("ambiguous registry: code %d err %v pushed %+v", codeFor(err), err, pushed)
	}
	for _, want := range []string{"hub/me/app", "hub/me/app-debug", "t_app", "t_debug"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not list %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error %q still blames duplicates", err)
	}
}

func TestImagesBuildEmptyTagIsUsageError(t *testing.T) {
	built := false
	te := t8Setup(t, map[string]http.HandlerFunc{
		"GET /api/images/web":         func(w http.ResponseWriter, r *http.Request) { t8JSON(w, 200, imageDetail("4", "")) },
		"POST /api/images/web/builds": func(w http.ResponseWriter, r *http.Request) { built = true },
	})
	err := imagesRun(te, "images", "build", "web", "--tag", "")
	if codeFor(err) != ExitUsage || !strings.Contains(err.Error(), "tag") {
		t.Fatalf("code %d err %v", codeFor(err), err)
	}
	if built {
		t.Error("build was requested")
	}
}
