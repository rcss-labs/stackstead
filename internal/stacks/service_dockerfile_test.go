package stacks_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

const imageOnlyCompose = "services:\n  web:\n    image: nginx:1\n"

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func composeHash(t *testing.T, h *harness, stack string) string {
	t.Helper()
	return composefile.Hash([]byte(readTestFile(t, h.path(stack))))
}

func buildCfg(context string) composefile.ServiceConfig {
	return composefile.ServiceConfig{Build: &composefile.Build{Context: context}}
}

func TestAddServiceWritesManagedDockerfile(t *testing.T) {
	h := newHarness(t)
	dir := dockerfileStack(t, h, "lab", imageOnlyCompose)
	hash := composeHash(t, h, "lab")
	_, err := h.svc.AddService(context.Background(), "lab", "api", buildCfg("./api"), hash,
		&stacks.DockerfileWrite{Content: []byte("FROM alpine\n")})
	if err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, filepath.Join(dir, "api", "Dockerfile")); got != "FROM alpine\n" {
		t.Errorf("Dockerfile = %q", got)
	}
	if compose := readTestFile(t, h.path("lab")); !strings.Contains(compose, "stackstead/lab-api:latest") {
		t.Errorf("compose lacks the generated image:\n%s", compose)
	}
}

func TestAddServiceOverwritesLeftoverDockerfile(t *testing.T) {
	h := newHarness(t)
	dir := dockerfileStack(t, h, "lab", imageOnlyCompose)
	if err := os.MkdirAll(filepath.Join(dir, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "api", "Dockerfile"), []byte("FROM old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.AddService(context.Background(), "lab", "api", buildCfg("./api"), composeHash(t, h, "lab"),
		&stacks.DockerfileWrite{Content: []byte("FROM new\n"), Hash: "ignored", CheckHash: false})
	if err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, filepath.Join(dir, "api", "Dockerfile")); got != "FROM new\n" {
		t.Errorf("Dockerfile = %q", got)
	}
}

func TestAddServiceNoWriteWhenComposeInvalid(t *testing.T) {
	h := newHarness(t)
	dir := dockerfileStack(t, h, "lab", imageOnlyCompose)
	h.compose.configErr = errors.New("compose says no")
	_, err := h.svc.AddService(context.Background(), "lab", "api", buildCfg("./api"), composeHash(t, h, "lab"),
		&stacks.DockerfileWrite{Content: []byte("FROM alpine\n")})
	if err == nil {
		t.Fatal("want the compose error")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "api")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("api folder exists after a rejected edit: %v", statErr)
	}
}

func TestAddServiceDockerfileRolledBackWhenComposeSaveFails(t *testing.T) {
	h := newHarness(t)
	dir := dockerfileStack(t, h, "lab", imageOnlyCompose)
	hash := composeHash(t, h, "lab")
	stacks.SetBeforeComposeSave(h.svc, func() {
		if err := os.WriteFile(h.path("lab"), []byte(imageOnlyCompose+"# edited elsewhere\n"), 0o644); err != nil {
			t.Error(err)
		}
	})
	_, err := h.svc.AddService(context.Background(), "lab", "api", buildCfg("./api"), hash,
		&stacks.DockerfileWrite{Content: []byte("FROM alpine\n")})
	if !errors.Is(err, composefile.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "api")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("api folder survived a failed compose save: %v", statErr)
	}
}

func TestUpdateServiceRestoresExistingDockerfileWhenComposeSaveFails(t *testing.T) {
	h := newHarness(t)
	dir := dockerfileStack(t, h, "lab", "services:\n  web:\n    build: ./web\n")
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "web", "Dockerfile")
	if err := os.WriteFile(path, []byte("FROM old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stacks.SetBeforeComposeSave(h.svc, func() {
		_ = os.WriteFile(h.path("lab"), []byte("services:\n  web:\n    build: ./web\n# x\n"), 0o644)
	})
	_, err := h.svc.UpdateService(context.Background(), "lab", "web", buildCfg("./web"), composeHash(t, h, "lab"),
		&stacks.DockerfileWrite{Content: []byte("FROM new\n"), Hash: composefile.Hash([]byte("FROM old\n")), CheckHash: true})
	if !errors.Is(err, composefile.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if got := readTestFile(t, path); got != "FROM old\n" {
		t.Errorf("Dockerfile = %q, want the original restored", got)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode after rollback = %v, %v; want 0600", info.Mode().Perm(), err)
	}
}

const devBuildCompose = "services:\n  web:\n    build:\n      context: .\n      dockerfile: Dockerfile.dev\n"

func TestUpdateServiceWritesInPlaceDockerfile(t *testing.T) {
	h := newHarness(t)
	dir := dockerfileStack(t, h, "lab", devBuildCompose)
	path := filepath.Join(dir, "Dockerfile.dev")
	if err := os.WriteFile(path, []byte("FROM old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := composefile.ServiceConfig{Build: &composefile.Build{Context: ".", Dockerfile: "Dockerfile.dev"}}
	hash := composeHash(t, h, "lab")

	_, err := h.svc.UpdateService(context.Background(), "lab", "web", cfg, hash,
		&stacks.DockerfileWrite{Content: []byte("FROM mismatch\n"), Hash: "wrong", CheckHash: true})
	if !errors.Is(err, stacks.ErrDockerfileConflict) {
		t.Fatalf("wrong hash: err = %v, want ErrDockerfileConflict", err)
	}
	if got := readTestFile(t, path); got != "FROM old\n" {
		t.Errorf("Dockerfile changed on a hash mismatch: %q", got)
	}
	if composeHash(t, h, "lab") != hash {
		t.Error("compose changed on a Dockerfile hash mismatch")
	}

	_, err = h.svc.UpdateService(context.Background(), "lab", "web", cfg, hash,
		&stacks.DockerfileWrite{Content: []byte("FROM new\n"), Hash: composefile.Hash([]byte("FROM old\n")), CheckHash: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, path); got != "FROM new\n" {
		t.Errorf("Dockerfile = %q", got)
	}
}

func TestUpdateServiceSwitchImageToManaged(t *testing.T) {
	h := newHarness(t)
	dir := dockerfileStack(t, h, "lab", imageOnlyCompose)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.UpdateService(context.Background(), "lab", "web", composefile.ServiceConfig{Image: "nginx:1", Build: &composefile.Build{Context: "./web"}},
		composeHash(t, h, "lab"), &stacks.DockerfileWrite{Content: []byte("FROM nginx\n"), Hash: "", CheckHash: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, filepath.Join(dir, "web", "Dockerfile")); got != "FROM nginx\n" {
		t.Errorf("Dockerfile = %q", got)
	}
	if got := readTestFile(t, filepath.Join(dir, "notes.txt")); got != "keep" {
		t.Errorf("unrelated file changed: %q", got)
	}
}

func TestUpdateServiceDockerfileOutsideRefused(t *testing.T) {
	h := newHarness(t)
	dockerfileStack(t, h, "lab", imageOnlyCompose)
	outside := t.TempDir()
	_, err := h.svc.UpdateService(context.Background(), "lab", "web", buildCfg(outside), composeHash(t, h, "lab"),
		&stacks.DockerfileWrite{Content: []byte("FROM x\n"), CheckHash: true})
	fields := fieldsOf(t, err)
	if len(fields) != 1 || fields[0].Field != "dockerfile" ||
		fields[0].Message != "a Dockerfile can only be written inside the stack's folder" {
		t.Fatalf("fields = %+v", fields)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "Dockerfile")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("Dockerfile written outside the stack: %v", statErr)
	}
}

func TestUpdateServiceDockerfileSymlinkedFolderRefused(t *testing.T) {
	h := newHarness(t)
	dir := dockerfileStack(t, h, "lab", imageOnlyCompose)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "web")); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.UpdateService(context.Background(), "lab", "web", buildCfg("./web"), composeHash(t, h, "lab"),
		&stacks.DockerfileWrite{Content: []byte("FROM x\n"), Hash: "", CheckHash: true})
	fields := fieldsOf(t, err)
	if len(fields) != 1 || fields[0].Field != "dockerfile" ||
		fields[0].Message != "a Dockerfile can only be written inside the stack's folder" {
		t.Fatalf("fields = %+v", fields)
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Errorf("outside folder was written to: %v", entries)
	}
}

func TestUpdateServiceDockerfileOversizedRefused(t *testing.T) {
	h := newHarness(t)
	dir := dockerfileStack(t, h, "lab", imageOnlyCompose)
	_, err := h.svc.UpdateService(context.Background(), "lab", "web", buildCfg("./web"), composeHash(t, h, "lab"),
		&stacks.DockerfileWrite{Content: make([]byte, stacks.MaxDockerfileBytes+1), CheckHash: true})
	if fields := fieldsOf(t, err); fields[0].Field != "dockerfile" {
		t.Fatalf("fields = %+v", fields)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "web")); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("web folder created for an invalid Dockerfile")
	}
}

func TestUpdateWithoutDockerfileUnchanged(t *testing.T) {
	h := newHarness(t)
	dir := dockerfileStack(t, h, "lab", imageOnlyCompose)
	_, err := h.svc.UpdateService(context.Background(), "lab", "web", composefile.ServiceConfig{Image: "nginx:2"}, composeHash(t, h, "lab"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readTestFile(t, h.path("lab")), "nginx:2") {
		t.Error("compose was not updated")
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if entry.IsDir() || strings.Contains(entry.Name(), "Dockerfile") {
			t.Errorf("unexpected Dockerfile or folder: %s", entry.Name())
		}
	}
}

func TestUpdateServiceDockerfileOnImageServiceRefused(t *testing.T) {
	h := newHarness(t)
	dockerfileStack(t, h, "lab", imageOnlyCompose)
	_, err := h.svc.UpdateService(context.Background(), "lab", "web", composefile.ServiceConfig{Image: "nginx:1"}, composeHash(t, h, "lab"),
		&stacks.DockerfileWrite{Content: []byte("FROM x\n"), CheckHash: true})
	fields := fieldsOf(t, err)
	if len(fields) != 1 || fields[0].Field != "dockerfile" || fields[0].Message != "this service doesn't build an image, so it has no Dockerfile" {
		t.Fatalf("fields = %+v", fields)
	}
}

func TestAddServiceDockerfileOutsideManagedPathNeedsHash(t *testing.T) {
	h := newHarness(t)
	dir := dockerfileStack(t, h, "lab", imageOnlyCompose)
	path := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(path, []byte("FROM mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash := composeHash(t, h, "lab")
	_, err := h.svc.AddService(context.Background(), "lab", "api", buildCfg("."), hash,
		&stacks.DockerfileWrite{Content: []byte("FROM clobber\n")})
	if !errors.Is(err, stacks.ErrDockerfileConflict) {
		t.Fatalf("err = %v, want ErrDockerfileConflict", err)
	}
	if got := readTestFile(t, path); got != "FROM mine\n" {
		t.Errorf("existing Dockerfile changed: %q", got)
	}
	if composeHash(t, h, "lab") != hash {
		t.Error("compose changed")
	}
}

func TestAddServiceSymlinkedServiceFolderNeedsHash(t *testing.T) {
	h := newHarness(t)
	dir := dockerfileStack(t, h, "lab", imageOnlyCompose)
	path := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(path, []byte("FROM mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".", filepath.Join(dir, "api")); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.AddService(context.Background(), "lab", "api", buildCfg("./api"), composeHash(t, h, "lab"),
		&stacks.DockerfileWrite{Content: []byte("FROM clobber\n")})
	if !errors.Is(err, stacks.ErrDockerfileConflict) {
		t.Fatalf("err = %v, want ErrDockerfileConflict", err)
	}
	if got := readTestFile(t, path); got != "FROM mine\n" {
		t.Errorf("existing Dockerfile changed: %q", got)
	}
}
