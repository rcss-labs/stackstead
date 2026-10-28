//go:build integration

package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/actionlog"
	"github.com/rcss-labs/stackstead/internal/composecli"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/peers"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/store"
)

// This test talks to a real Docker engine:
//   go test -tags integration ./internal/backup/ -run RoundTrip
// It skips when no engine answers. Everything it creates carries a random
// "itest<hex>" prefix and is removed in cleanup, even on failure.

func itDockerSocket() string {
	var candidates []string
	if h := os.Getenv("DOCKER_HOST"); strings.HasPrefix(h, "unix://") {
		candidates = append(candidates, strings.TrimPrefix(h, "unix://"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".docker", "run", "docker.sock"))
	}
	candidates = append(candidates, "/var/run/docker.sock")
	for _, c := range candidates {
		conn, err := net.DialTimeout("unix", c, time.Second)
		if err != nil {
			continue
		}
		conn.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err = engine.New(c, 3*time.Second).Ping(ctx)
		cancel()
		if err == nil {
			return c
		}
	}
	return ""
}

func TestStackBackupRestoreRoundTrip(t *testing.T) {
	sock := itDockerSocket()
	if sock == "" {
		t.Skip("no reachable Docker socket")
	}
	if out, err := exec.Command("docker", "compose", "version").CombinedOutput(); err != nil {
		t.Skipf("docker compose plugin unavailable: %v: %s", err, out)
	}
	raw := make([]byte, 3)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	name := "itest" + hex.EncodeToString(raw)
	volume := name + "_data"
	docker := func(args ...string) (string, error) {
		cmd := exec.Command("docker", args...)
		cmd.Env = append(os.Environ(), "DOCKER_HOST=unix://"+sock)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return strings.TrimSpace(out.String()), err
	}
	writeData := func(content string) {
		t.Helper()
		if out, err := docker("run", "--rm", "-v", volume+":/data", HelperImage, "sh", "-c", "echo "+content+" > /data/f"); err != nil {
			t.Fatalf("write volume: %v: %s", err, out)
		}
	}

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stacksDir, dataDir := filepath.Join(root, "stacks"), filepath.Join(root, "data")
	for _, d := range []string{stacksDir, dataDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.Open(filepath.Join(dataDir, "stackstead.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reg := stacks.NewRegistry(db, time.Now)
	eng := engine.New(sock, 30*time.Second)
	ops := stacks.NewOps(5*time.Minute, nil)
	svc := stacks.NewService(stacks.Deps{
		Registry: reg, Ops: ops, Engine: eng,
		Compose:   &composecli.Runner{Env: []string{"DOCKER_HOST=unix://" + sock}, Grace: 5 * time.Second},
		StacksDir: stacksDir, AllowedRoots: []string{root}, Now: time.Now,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		ops.Shutdown(ctx)
		_, _ = docker("compose", "-p", name, "-f", filepath.Join(stacksDir, name, "compose.yaml"), "down", "-v", "--remove-orphans")
		_, _ = docker("volume", "rm", "-f", volume)
	})

	compose := filepath.Join(stacksDir, name, "compose.yaml")
	if err := os.MkdirAll(filepath.Dir(compose), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "services:\n  app:\n    image: " + HelperImage + "\n    command: [\"sleep\", \"3600\"]\n    volumes: [\"data:/data\"]\nvolumes:\n  data: {}\n"
	if err := os.WriteFile(compose, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := reg.Add(ctx, stacks.Entry{Name: name, ComposePath: compose, Origin: stacks.OriginManaged, Project: name}); err != nil {
		t.Fatal(err)
	}
	if err := svc.ActWait(ctx, name, "up", func(string) {}); err != nil {
		t.Fatalf("up: %v", err)
	}
	writeData("original")

	configs := NewConfigStore(db)
	cfg := defaultConfig()
	cfg.Local = LocalDest{Enabled: true, Path: filepath.Join(root, "backups"), KeepLast: 7}
	if err := configs.Save(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := configs.SetSelection(ctx, StackSelection{Stack: name, Volumes: true, StopDuring: true}); err != nil {
		t.Fatal(err)
	}
	runs := NewRunStore(db, time.Now)
	runner := NewRunner(Deps{
		DB: db, Config: configs, Runs: runs, Stacks: svc, Restore: svc, Registry: reg, Engine: eng, Ops: ops,
		Actions: actionlog.NewStore(db, time.Now),
		Identity: func(context.Context) (peers.Identity, error) {
			return peers.Identity{InstanceID: "it", Name: "it host"}, nil
		},
		DataDir: dataDir, AllowedRoots: []string{root}, StacksDir: stacksDir, Version: "dev", Now: time.Now,
	})

	op, err := runner.Start(ctx, "manual", "it")
	if err != nil {
		t.Fatal(err)
	}
	ops.Wait(op.ID)
	run, ok, err := runs.Last(ctx)
	if err != nil || !ok || run.Status != runStatusSuccess {
		t.Fatalf("backup run = %+v (%v)", run, err)
	}

	writeData("changed")
	if err := os.WriteFile(compose, []byte(body+"# edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	op, err = runner.RestoreStack(ctx, StackRestoreRequest{Archive: ArchiveRef{Dest: destLocal, Name: run.Archive}, Stack: name, Files: true, Volumes: true, AllowUnencrypted: true}, "it")
	if err != nil {
		t.Fatalf("RestoreStack: %v", err)
	}
	ops.Wait(op.ID)
	if final, _ := ops.Get(op.ID); final.State != stacks.OpSucceeded {
		lines, _ := ops.Lines(op.ID)
		t.Fatalf("restore = %+v\n%s", final, strings.Join(lines, "\n"))
	}

	out, err := docker("run", "--rm", "-v", volume+":/data", HelperImage, "cat", "/data/f")
	if err != nil || out != "original" {
		t.Errorf("volume after restore = %q (%v), want original", out, err)
	}
	if got, _ := os.ReadFile(compose); string(got) != body {
		t.Errorf("compose after restore = %q, want the archived file", got)
	}
	if out, err := docker("compose", "-p", name, "-f", compose, "ps", "--status", "running", "-q"); err != nil || out == "" {
		t.Errorf("stack is not running after the restore: %q (%v)", out, err)
	}
}
