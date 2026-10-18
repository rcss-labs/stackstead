package update

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func mustVer(t *testing.T, s string) Version {
	t.Helper()
	v, err := Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestCommands(t *testing.T) {
	const asset = "/tmp/x/stackstead_0.3.0_amd64.deb"
	debEnv := []string{"DEBIAN_FRONTEND=noninteractive"}
	for _, c := range []struct {
		name string
		plan Plan
		argv []string
		env  []string
	}{
		{"script", Plan{Kind: KindScript, Version: mustVer(t, "0.3.0"), Asset: "/tmp/x/install.sh", Prefix: "/usr/local"},
			[]string{"/bin/sh", "/tmp/x/install.sh", "--version", "v0.3.0", "--prefix", "/usr/local"}, nil},
		{"script force base-url", Plan{Kind: KindScript, Version: mustVer(t, "0.3.0"), Asset: "/tmp/x/install.sh", Prefix: "/usr/local", Force: true, BaseURL: "http://h:1"},
			[]string{"/bin/sh", "/tmp/x/install.sh", "--version", "v0.3.0", "--prefix", "/usr/local", "--force", "--base-url", "http://h:1"}, nil},
		{"deb upgrade", Plan{Kind: KindDeb, Version: mustVer(t, "0.3.0"), Current: mustVer(t, "0.2.0"), HaveCurrent: true, Asset: asset},
			[]string{"/usr/bin/apt-get", "install", "-y", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold", asset}, debEnv},
		{"deb force upgrade has no reinstall", Plan{Kind: KindDeb, Force: true, Version: mustVer(t, "0.3.0"), Current: mustVer(t, "0.2.0"), HaveCurrent: true, Asset: asset},
			[]string{"/usr/bin/apt-get", "install", "-y", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold", asset}, debEnv},
		{"deb dev build force", Plan{Kind: KindDeb, Force: true, Version: mustVer(t, "0.3.0"), Asset: asset},
			[]string{"/usr/bin/apt-get", "install", "-y", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold", asset}, debEnv},
		{"deb same version force", Plan{Kind: KindDeb, Force: true, Version: mustVer(t, "0.3.0"), Current: mustVer(t, "0.3.0"), HaveCurrent: true, Asset: asset},
			[]string{"/usr/bin/apt-get", "install", "-y", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold", "--reinstall", asset}, debEnv},
		{"rpm dnf", Plan{Kind: KindRPM, HaveDNF: true, Version: mustVer(t, "0.3.0"), Current: mustVer(t, "0.2.0"), HaveCurrent: true, Asset: "/tmp/x/s.rpm"},
			[]string{"/usr/bin/dnf", "install", "-y", "/tmp/x/s.rpm"}, nil},
		{"rpm fallback", Plan{Kind: KindRPM, Version: mustVer(t, "0.3.0"), Current: mustVer(t, "0.2.0"), HaveCurrent: true, Asset: "/tmp/x/s.rpm"},
			[]string{"/usr/bin/rpm", "-U", "/tmp/x/s.rpm"}, nil},
		{"rpm same version force", Plan{Kind: KindRPM, Force: true, HaveDNF: true, Version: mustVer(t, "0.3.0"), Current: mustVer(t, "0.3.0"), HaveCurrent: true, Asset: "/tmp/x/s.rpm"},
			[]string{"/usr/bin/rpm", "-U", "--replacepkgs", "/tmp/x/s.rpm"}, nil},
		{"pkg", Plan{Kind: KindPkg, Version: mustVer(t, "0.3.0"), Asset: "/tmp/x/s.pkg"},
			[]string{"/usr/sbin/installer", "-pkg", "/tmp/x/s.pkg", "-target", "/"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			argv, env, err := Commands(c.plan)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(argv, c.argv) {
				t.Errorf("argv = %q, want %q", argv, c.argv)
			}
			if !reflect.DeepEqual(env, c.env) {
				t.Errorf("env = %q, want %q", env, c.env)
			}
		})
	}
}

func TestCommandsRefusesPackageDowngrade(t *testing.T) {
	for _, c := range []struct {
		name string
		plan Plan
		want string
	}{
		{"deb", Plan{Kind: KindDeb, Version: mustVer(t, "0.2.0"), Current: mustVer(t, "0.3.0"), HaveCurrent: true, Asset: "/tmp/x/s.deb"},
			"apt-get install -y --allow-downgrades /tmp/x/s.deb"},
		{"deb force", Plan{Kind: KindDeb, Force: true, Version: mustVer(t, "0.2.0"), Current: mustVer(t, "0.3.0"), HaveCurrent: true, Asset: "/tmp/x/s.deb"},
			"apt-get install -y --allow-downgrades /tmp/x/s.deb"},
		{"rpm force dnf", Plan{Kind: KindRPM, Force: true, HaveDNF: true, Version: mustVer(t, "0.2.0"), Current: mustVer(t, "0.3.0"), HaveCurrent: true, Asset: "/tmp/x/s.rpm"},
			"rpm -U --oldpackage /tmp/x/s.rpm"},
	} {
		t.Run(c.name, func(t *testing.T) {
			argv, _, err := Commands(c.plan)
			if !errors.Is(err, ErrDowngradeManual) {
				t.Fatalf("err = %v, want ErrDowngradeManual", err)
			}
			if argv != nil {
				t.Errorf("argv = %q, want nil", argv)
			}
			var de *DowngradeError
			if !errors.As(err, &de) || de.Command != c.want {
				t.Errorf("command = %+v, want %q", de, c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("message %q does not contain the command", err.Error())
			}
		})
	}
}

func TestCommandsScriptDowngradeIsInstallShsJob(t *testing.T) {
	argv, _, err := Commands(Plan{Kind: KindScript, Force: true, Version: mustVer(t, "0.2.0"), Current: mustVer(t, "0.3.0"), HaveCurrent: true, Asset: "/tmp/x/install.sh", Prefix: "/usr/local"})
	if err != nil || !reflect.DeepEqual(argv[len(argv)-1:], []string{"--force"}) {
		t.Errorf("argv = %q err = %v", argv, err)
	}
}

func TestCommandsRejectsRelativeAsset(t *testing.T) {
	for _, k := range []Kind{KindScript, KindDeb, KindRPM, KindPkg} {
		if _, _, err := Commands(Plan{Kind: k, Version: mustVer(t, "0.3.0"), Asset: "rel/x", Prefix: "/usr/local"}); err == nil {
			t.Errorf("%s: relative asset accepted", k)
		}
	}
}

func TestCommandsScriptNeedsPrefix(t *testing.T) {
	_, _, err := Commands(Plan{Kind: KindScript, Version: mustVer(t, "0.3.0"), Asset: "/tmp/x/install.sh"})
	if !errors.Is(err, ErrLayout) {
		t.Errorf("err = %v, want ErrLayout", err)
	}
}

type call struct {
	name      string
	args, env []string
}

func TestApplyDebBusyRunsNothing(t *testing.T) {
	ran := 0
	err := Apply(context.Background(), Plan{Kind: KindDeb, Version: mustVer(t, "0.3.0"), Asset: "/tmp/x/s.deb"},
		func(context.Context, string, []string, []string) error { ran++; return nil },
		func() (bool, error) { return true, nil })
	if !errors.Is(err, ErrBusy) || ran != 0 {
		t.Errorf("err = %v, ran = %d", err, ran)
	}
}

func TestApplyRunsOnce(t *testing.T) {
	p := Plan{Kind: KindDeb, Version: mustVer(t, "0.3.0"), Asset: "/tmp/x/s.deb"}
	var calls []call
	err := Apply(context.Background(), p,
		func(_ context.Context, name string, args, env []string) error {
			calls = append(calls, call{name, args, env})
			return nil
		},
		func() (bool, error) { return false, nil })
	if err != nil {
		t.Fatal(err)
	}
	argv, env, _ := Commands(p)
	want := []call{{argv[0], argv[1:], env}}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %+v, want %+v", calls, want)
	}
}

func TestApplyRefusesDowngradeRunsNothing(t *testing.T) {
	ran := 0
	err := Apply(context.Background(), Plan{Kind: KindRPM, Force: true, Version: mustVer(t, "0.2.0"), Current: mustVer(t, "0.3.0"), HaveCurrent: true, Asset: "/tmp/x/s.rpm"},
		func(context.Context, string, []string, []string) error { ran++; return nil }, nil)
	if !errors.Is(err, ErrDowngradeManual) || ran != 0 {
		t.Errorf("err = %v, ran = %d", err, ran)
	}
}

func TestApplyPropagatesRunnerError(t *testing.T) {
	want := &exec.ExitError{}
	err := Apply(context.Background(), Plan{Kind: KindPkg, Version: mustVer(t, "0.3.0"), Asset: "/tmp/x/s.pkg"},
		func(context.Context, string, []string, []string) error { return want }, nil)
	if err != error(want) {
		t.Errorf("err = %v, want the runner's error unwrapped", err)
	}
}

func TestDpkgFrontendLockedAbsentFile(t *testing.T) {
	got, err := dpkgFrontendLockedAt(filepath.Join(t.TempDir(), "lock-frontend"))
	if got || err != nil {
		t.Errorf("got %v, %v; want false, nil", got, err)
	}
}

func TestDpkgFrontendLockedUnlocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock-frontend")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := dpkgFrontendLockedAt(path)
	if got || err != nil {
		t.Errorf("got %v, %v; want false, nil", got, err)
	}
}

func holdLock(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	lock := syscall.Flock_t{Type: syscall.F_WRLCK}
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lock); err != nil {
		t.Fatal(err)
	}
}

// A lock held by another process is simulated with a lock on a second
// descriptor in a child; fcntl locks are per process, so use a helper process.
func TestDpkgLocksEitherHeldIsBusy(t *testing.T) {
	if os.Getenv("STACKSTEAD_HOLD_LOCK") != "" {
		holdLock(t, os.Getenv("STACKSTEAD_HOLD_LOCK"))
		os.Stdout.WriteString("held\n")
		io.Copy(io.Discard, os.Stdin) // hold until the parent closes stdin
		return
	}
	for _, held := range []string{"lock", "lock-frontend"} {
		t.Run(held, func(t *testing.T) {
			dir := t.TempDir()
			frontend, lock := filepath.Join(dir, "lock-frontend"), filepath.Join(dir, "lock")
			for _, p := range []string{frontend, lock} {
				if err := os.WriteFile(p, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestDpkgLocksEitherHeldIsBusy$")
			cmd.Env = append(os.Environ(), "STACKSTEAD_HOLD_LOCK="+filepath.Join(dir, held))
			stdin, _ := cmd.StdinPipe()
			stdout, _ := cmd.StdoutPipe()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { stdin.Close(); cmd.Wait() }()
			if line, _ := bufio.NewReader(stdout).ReadString('\n'); line != "held\n" {
				t.Fatalf("helper did not take the lock: %q", line)
			}
			got, err := dpkgLocksHeldAt(frontend, lock)
			if !got || err != nil {
				t.Errorf("with %s held: got %v, %v; want true, nil", held, got, err)
			}
		})
	}
	dir := t.TempDir()
	got, err := dpkgLocksHeldAt(filepath.Join(dir, "lock-frontend"), filepath.Join(dir, "lock"))
	if got || err != nil {
		t.Errorf("absent files: got %v, %v; want false, nil", got, err)
	}
}
