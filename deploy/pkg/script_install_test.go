//go:build installer

package deploy_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runStatus is run for scripts that may fail: it returns the exit code instead of failing the test.
func (e *env) runStatus(script string, args ...string) (code int, out string) {
	e.t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		e.t.Skip("sh not available")
	}
	_ = os.Remove(e.logFile)
	cmd := exec.Command(sh, append([]string{script}, args...)...)
	cmd.Env = []string{"PATH=" + e.bin, rootEnv + "=" + e.root, "STUB_LOG=" + e.logFile, "STUB_EXISTS="}
	b, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), string(b)
	}
	if err != nil {
		e.t.Fatalf("%s %v: %v", script, args, err)
	}
	return 0, string(b)
}

const (
	scriptUnitMarker = "# Installed by stackstead install.sh\n"
	scriptUnitBody   = "[Service]\nExecStart=/usr/local/bin/stackstead serve --config /etc/stackstead/config.yaml\n"
	packagedUnitBody = "[Service]\nExecStart=/usr/bin/stackstead serve --config /etc/stackstead/config.yaml\n"
)

func (e *env) writeFileUnder(rel, body string) {
	e.t.Helper()
	p := filepath.Join(e.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	mustWrite(e.t, p, body)
}

// A package installed over a script install would leave the /etc unit (which wins over /usr/lib)
// running the old /usr/local/bin binary while the package looks installed (issue #33). The
// pre-install scripts must refuse before changing anything, and say how to migrate without losing data.
func TestPreinstallRefusesScriptInstall(t *testing.T) {
	scripts := []struct {
		script string
		args   [][]string
	}{
		{"deb/preinst", [][]string{{"install"}, {"upgrade", "1.0.0"}}},
		{"rpm/pre", [][]string{{"1"}, {"2"}}},
	}
	layouts := map[string]func(*env){
		"unit with the install.sh marker": func(e *env) {
			e.writeFileUnder("etc/systemd/system/stackstead.service", scriptUnitMarker+scriptUnitBody)
		},
		"legacy unit without a marker": func(e *env) {
			e.writeFileUnder("etc/systemd/system/stackstead.service", scriptUnitBody)
		},
		"leftover binary and helper": func(e *env) {
			e.writeFileUnder("usr/local/bin/stackstead", "old")
			e.writeFileUnder("usr/local/share/stackstead/install.sh", "old")
		},
	}
	for _, s := range scripts {
		for _, args := range s.args {
			for name, setup := range layouts {
				t.Run(s.script+" "+strings.Join(args, " ")+" "+name, func(t *testing.T) {
					e := newEnv(t, true)
					setup(e)
					code, out := e.runStatus(s.script, args...)
					if code == 0 {
						t.Fatalf("pre-install accepted a script-installed layout:\n%s", out)
					}
					for _, want := range []string{"install.sh", "--uninstall", "without --purge"} {
						if !strings.Contains(out, want) {
							t.Errorf("refusal lacks %q:\n%s", want, out)
						}
					}
					if got := only(readLines(e.logFile), "useradd", "usermod", "groupadd", "systemctl"); len(got) != 0 {
						t.Errorf("refusal still changed the system: %q", got)
					}
					for _, keep := range []string{"etc/stackstead/config.yaml", "var/lib/stackstead/stackstead.db", "opt/stacks/keep.txt"} {
						if _, err := os.Stat(filepath.Join(e.root, keep)); err != nil {
							t.Errorf("%s lost: %v", keep, err)
						}
					}
				})
			}
		}
	}
}

// An administrator's own copy of the packaged unit in /etc, a drop-in, or a hand-built binary with
// no helper is not a script install: the package must still install and upgrade over it.
func TestPreinstallAllowsAdministratorOverrides(t *testing.T) {
	layouts := map[string]func(*env){
		"copy of the packaged unit": func(e *env) {
			e.writeFileUnder("etc/systemd/system/stackstead.service", packagedUnitBody)
		},
		"unit drop-in": func(e *env) {
			e.writeFileUnder("etc/systemd/system/stackstead.service.d/override.conf", "[Service]\nRestart=always\n")
		},
		"hand-built binary without the helper": func(e *env) {
			e.writeFileUnder("usr/local/bin/stackstead", "mine")
		},
	}
	for name, setup := range layouts {
		for _, c := range []struct {
			script string
			args   []string
		}{{"deb/preinst", []string{"upgrade", "1.0.0"}}, {"rpm/pre", []string{"2"}}} {
			t.Run(c.script+" "+name, func(t *testing.T) {
				e := newEnv(t, true)
				setup(e)
				if code, out := e.runStatus(c.script, c.args...); code != 0 {
					t.Fatalf("pre-install refused an administrator override (exit %d):\n%s", code, out)
				}
			})
		}
	}
}
