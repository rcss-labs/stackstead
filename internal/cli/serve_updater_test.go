package cli

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/update"
)

func TestProbationTicketLocatorStartupPolicy(t *testing.T) {
	for _, tc := range []struct {
		name       string
		path       string
		locator    update.ClientLocator
		readErr    error
		wantErr    bool
		wantClient bool
	}{{name: "env only"}, {name: "absent", path: "/etc/config", readErr: update.ErrLocatorAbsent}, {name: "unsafe", path: "/etc/config", readErr: update.ErrSetupRequired, wantErr: true}, {name: "disabled", path: "/etc/config"}, {name: "enabled", path: "/etc/config", locator: update.ClientLocator{Enabled: true, Socket: "/nonexistent.sock", InstanceID: "one", InstallationID: "installation"}, wantClient: true}} {
		t.Run(tc.name, func(t *testing.T) {
			l, e := lookupUpdater(tc.path, 42, func(string, uint32) (update.ClientLocator, error) { return tc.locator, tc.readErr })
			if (e != nil) != tc.wantErr {
				t.Fatalf("lookup error %v", e)
			}
			if (l != nil) != tc.wantClient {
				t.Fatalf("locator %+v", l)
			}
			if l != nil {
				p := locatorStartup{locator: *l}
				if _, e = p.StartupProbation(context.Background()); e == nil || errors.Is(e, update.ErrLocatorAbsent) {
					t.Fatalf("enabled unavailable helper bypassed startup: %v", e)
				}
			}
		})
	}
}

func TestProbationTicketOrdinaryServeNegotiatesBeforeDatabase(t *testing.T) {
	te, data := serveTestEnv(t)
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if e := os.WriteFile(cfg, []byte("{}"), 0600); e != nil {
		t.Fatal(e)
	}
	old := updaterLocatorReader
	defer func() { updaterLocatorReader = old }()
	called := false
	updaterLocatorReader = func(path string, uid uint32) (update.ClientLocator, error) {
		called = true
		canonical, _ := filepath.EvalSymlinks(cfg)
		if path != canonical {
			t.Errorf("unresolved config %s", path)
		}
		if _, e := os.Stat(filepath.Join(data, "stackstead.db")); !os.IsNotExist(e) {
			t.Error("database opened before trusted discovery")
		}
		return update.ClientLocator{Enabled: true, Socket: "/nonexistent-helper.sock", InstanceID: "app", InstallationID: "installation"}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e := serve(ctx, te.Env, []string{"--config", cfg})
	if !called || e == nil {
		t.Fatalf("ordinary serve bypassed negotiation: called=%v err=%v", called, e)
	}
	if _, e := os.Stat(filepath.Join(data, "stackstead.db")); !os.IsNotExist(e) {
		t.Fatal("failed negotiation opened database")
	}
}

func TestControlReadinessRequiresApplicationBind(t *testing.T) {
	occupied, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer occupied.Close()
	ready := false
	l, e := listenForReadiness(occupied.Addr().String(), func() error { ready = true; return nil })
	if e == nil {
		l.Close()
		t.Fatal("bind conflict accepted")
	}
	if ready {
		t.Fatal("readiness granted before application bind succeeded")
	}
}

func TestProbationTicketResolvesRelativeConfig(t *testing.T) {
	te, _ := serveTestEnv(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")
	os.WriteFile(config, []byte("{}"), 0600)
	canonical, e := filepath.EvalSymlinks(config)
	if e != nil {
		t.Fatal(e)
	}
	cwd, e := os.Getwd()
	if e != nil {
		t.Fatal(e)
	}
	relative, e := filepath.Rel(cwd, config)
	if e != nil {
		t.Fatal(e)
	}
	old := updaterLocatorReader
	defer func() { updaterLocatorReader = old }()
	updaterLocatorReader = func(path string, _ uint32) (update.ClientLocator, error) {
		if path != canonical {
			t.Errorf("locator saw unresolved path %s, want %s", path, canonical)
		}
		return update.ClientLocator{}, update.ErrSetupRequired
	}
	serve(context.Background(), te.Env, []string{"--config", relative})
}
