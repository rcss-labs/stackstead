package updatehost

import (
	"context"
	"errors"
	"fmt"
	"github.com/rcss-labs/stackstead/internal/update"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func instanceFixture(t *testing.T) (update.Registration, *darwinRunner) {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "stackstead")
	if e := os.WriteFile(binary, []byte("binary"), 0755); e != nil {
		t.Fatal(e)
	}
	var st unix.Stat_t
	if e := unix.Lstat(binary, &st); e != nil {
		t.Fatal(e)
	}
	one := darwinInstance()
	two := one
	two.ID = "two"
	two.UID = 502
	reg := update.Registration{Binary: binary, Identity: update.Identity{OS: "darwin"}, Instances: []update.Instance{one, two}}
	r := &darwinRunner{run: func(c commandCall) ([]byte, []byte, error) {
		switch c.path {
		case "/bin/ps":
			if c.args[0] == "-p" {
				uid := "501"
				if c.args[1] == "102" {
					uid = "502"
				}
				return []byte(c.args[1] + " " + uid + " Sat Oct 17 10:00:00 2026\n"), nil, nil
			}
			return []byte("101 501 Sat Oct 17 10:00:00 2026\n102 502 Sat Oct 17 10:00:00 2026\n"), nil, nil
		case "/usr/sbin/lsof":
			pid := c.args[4]
			uid := "501"
			if pid == "102" {
				uid = "502"
			}
			return []byte(fmt.Sprintf("p%s\nu%s\nftxt\nD0x%x\ni%d\nn%s\n", pid, uid, uint64(st.Dev), st.Ino, binary)), nil, nil
		case "/bin/launchctl":
			if c.args[0] == "print-disabled" {
				return []byte("disabled services = {\n}\n"), nil, nil
			}
			pid := 101
			if c.uid == 502 {
				pid = 102
			}
			return []byte(fmt.Sprintf("service = {\n state = running\n pid = %d\n}\n", pid)), nil, nil
		}
		return nil, nil, errors.New("unexpected command")
	}}
	return reg, r
}
func TestSharedPrefixCoordinatesRegisteredUIDs(t *testing.T) {
	reg, r := instanceFixture(t)
	got, e := registeredInstances(context.Background(), reg, r)
	if e != nil || len(got) != 2 || got[0].UID != 501 || got[1].UID != 502 {
		t.Fatalf("shared membership: %+v %v", got, e)
	}
}
func TestUnknownAgentBlocksBeforeStop(t *testing.T) {
	reg, r := instanceFixture(t)
	base := r.run
	r.run = func(c commandCall) ([]byte, []byte, error) {
		out, stderr, e := base(c)
		if c.path == "/usr/sbin/lsof" && c.args[4] == "102" {
			out = []byte(strings.Replace(string(out), "u502", "u503", 1))
		}
		return out, stderr, e
	}
	if _, e := registeredInstances(context.Background(), reg, r); !errors.Is(e, update.ErrSetupRequired) {
		t.Fatalf("unknown sharing agent accepted: %v", e)
	}
	for _, c := range r.calls {
		if c.path == "/bin/launchctl" && c.args[0] != "print" && c.args[0] != "print-disabled" {
			t.Fatal("stopped before membership proof")
		}
	}
}
func TestUnknownAgentAmbiguousSurvivorBlocks(t *testing.T) {
	reg, r := instanceFixture(t)
	base := r.run
	r.run = func(c commandCall) ([]byte, []byte, error) {
		if c.path == "/usr/sbin/lsof" {
			return nil, nil, commandFailure()
		}
		return base(c)
	}
	if _, e := registeredInstances(context.Background(), reg, r); !errors.Is(e, update.ErrSetupRequired) {
		t.Fatalf("empty process evidence accepted: %v", e)
	}
}

func TestSharedPrefixDiscoveryCommandBoundary(t *testing.T) {
	r := NewCommandRunner().(commandRunner)
	for _, c := range []commandCall{{path: "/bin/ps", args: []string{"-axo", "pid=,uid=,lstart="}}, {path: "/bin/ps", args: []string{"-p", "123", "-o", "pid=,uid=,lstart="}}, {path: "/usr/sbin/lsof", args: []string{"-n", "-P", "-a", "-p", "123", "-d", "txt", "-F", "pufDin"}}, {path: "/usr/sbin/pkgutil", args: []string{"--file-info", "/usr/local/bin/stackstead"}}} {
		if !r.allowedRoot(c.path, c.args) {
			t.Fatalf("fixed read-only discovery command refused: %s %v", c.path, c.args)
		}
	}
	for _, c := range []commandCall{{path: "/bin/ps", args: []string{"-ef"}}, {path: "/usr/sbin/lsof", args: []string{"/etc/passwd"}}, {path: "/usr/sbin/pkgutil", args: []string{"--forget", "stackstead"}}, {path: "/usr/local/bin/stackstead", args: []string{"version"}}, {path: "/bin/launchctl", args: []string{"bootstrap", "system", "/tmp/agent.plist"}}} {
		if r.allowedRoot(c.path, c.args) {
			t.Fatalf("unbounded root command accepted: %+v", c)
		}
	}
}

func TestSharedPrefixAliasAndPIDReuse(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(fmt.Sprint(reuse), func(t *testing.T) {
			reg, r := instanceFixture(t)
			base := r.run
			r.run = func(c commandCall) ([]byte, []byte, error) {
				out, stderr, e := base(c)
				if c.path == "/usr/sbin/lsof" {
					out = []byte(strings.ReplaceAll(string(out), reg.Binary, "/some/hardlink"))
				}
				if reuse && c.path == "/bin/ps" && c.args[0] == "-p" {
					out = []byte(strings.ReplaceAll(string(out), "10:00:00", "10:00:01"))
				}
				return out, stderr, e
			}
			_, e := registeredInstances(context.Background(), reg, r)
			if reuse && !errors.Is(e, update.ErrSetupRequired) {
				t.Fatalf("reused PID accepted: %v", e)
			}
			if !reuse && e != nil {
				t.Fatalf("same-inode alias rejected: %v", e)
			}
		})
	}
}
