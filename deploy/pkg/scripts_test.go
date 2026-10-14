//go:build installer

// Package deploy_test exercises the deb/rpm maintainer scripts, the systemd
// unit and the nfpm definition without root, Docker or systemd: scripts run
// under a stubbed PATH that only logs, with STACKSTEAD_PKG_ROOT pointing at a
// temp tree. Nothing here touches the real system.
package deploy_test

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const (
	rootEnv  = "STACKSTEAD_PKG_ROOT"
	unitPath = "../systemd/stackstead.service"
	// markerName is left in the state dir by `prerm remove` so a later reinstall re-enables the service.
	markerName = ".disabled-by-remove"
)

// realTools are the only real binaries the scripts may call; everything else is a logging stub.
var realTools = []string{"mkdir", "chmod", "rm"}

var stubbedTools = []string{"systemctl", "useradd", "usermod", "groupadd", "userdel", "getent", "id", "docker", "chown", "chgrp"}

type env struct {
	t       *testing.T
	root    string
	bin     string
	logFile string
}

// newEnv builds a temp root holding the directories a package install would
// leave behind, and a PATH directory of stubs.
func newEnv(t *testing.T, withSystemctl bool) *env {
	t.Helper()
	tmp := t.TempDir()
	e := &env{t: t, root: filepath.Join(tmp, "root"), bin: filepath.Join(tmp, "bin"), logFile: filepath.Join(tmp, "cmd.log")}
	for _, d := range []string{"etc/stackstead", "var/lib/stackstead", "opt/stacks", "run/systemd/system"} {
		if err := os.MkdirAll(filepath.Join(e.root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(t, filepath.Join(e.root, "etc/stackstead/config.yaml"), "listen: 127.0.0.1:8420\n")
	mustWrite(t, filepath.Join(e.root, "var/lib/stackstead/stackstead.db"), "db")
	mustWrite(t, filepath.Join(e.root, "opt/stacks/keep.txt"), "stack")
	if err := os.MkdirAll(e.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range realTools {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not available: %v", tool, err)
		}
		if err := os.Symlink(p, filepath.Join(e.bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range stubbedTools {
		if tool == "systemctl" && !withSystemctl {
			continue
		}
		body := "#!/bin/sh\necho \"" + tool + " $*\" >> \"$STUB_LOG\"\n"
		switch tool {
		case "getent":
			// STUB_EXISTS lists "group:docker passwd:stackstead" style entries that exist.
			body += "case \" $STUB_EXISTS \" in *\" $1:$2 \"*) exit 0;; esac\nexit 2\n"
		case "docker":
			body += "exit 0\n"
		}
		mustWrite(t, filepath.Join(e.bin, tool), body)
		if err := os.Chmod(filepath.Join(e.bin, tool), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// run executes a maintainer script and returns the stub log lines, filtered to
// the given commands (nil = all) and the script output.
func (e *env) run(script string, existing string, args ...string) (cmds []string, out string) {
	e.t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		e.t.Skip("sh not available")
	}
	_ = os.Remove(e.logFile)
	if strings.Contains(existing, "marker") {
		mustWrite(e.t, filepath.Join(e.root, "var/lib/stackstead/"+markerName), "")
	}
	cmd := exec.Command(sh, append([]string{script}, args...)...)
	cmd.Env = []string{"PATH=" + e.bin, rootEnv + "=" + e.root, "STUB_LOG=" + e.logFile, "STUB_EXISTS=" + existing}
	b, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Fatalf("%s %v failed: %v\n%s", script, args, err, b)
	}
	return readLines(e.logFile), string(b)
}

func readLines(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		lines = append(lines, s.Text())
	}
	return lines
}

func only(lines []string, prefixes ...string) []string {
	var got []string
	for _, l := range lines {
		for _, p := range prefixes {
			if strings.HasPrefix(l, p+" ") || l == p {
				got = append(got, l)
				break
			}
		}
	}
	return got
}

func TestMaintainerScriptBranches(t *testing.T) {
	const none = ""
	const owned = "group:docker group:stackstead passwd:stackstead"
	tests := []struct {
		name     string
		script   string
		existing string
		args     []string
		want     []string // filtered to systemctl/useradd/groupadd/userdel
		note     string   // substring expected in output, if any
	}{
		{"deb preinst install creates group and user", "deb/preinst", none, []string{"install"},
			[]string{"groupadd --system docker", "groupadd --system stackstead",
				"useradd --system --gid stackstead --groups docker --home-dir /var/lib/stackstead --no-create-home --shell /usr/sbin/nologin stackstead"}, ""},
		{"deb preinst upgrade re-adds docker group membership", "deb/preinst", owned, []string{"upgrade", "1.0.0"}, []string{"usermod -aG docker stackstead"}, ""},
		{"deb preinst abort-upgrade does nothing", "deb/preinst", none, []string{"abort-upgrade", "1.0.0"}, nil, ""},
		{"deb postinst fresh enables and starts", "deb/postinst", owned, []string{"configure"},
			[]string{"systemctl daemon-reload", "systemctl enable --now stackstead.service"}, "127.0.0.1:8420"},
		{"deb postinst upgrade try-restarts", "deb/postinst", owned, []string{"configure", "1.0.0"},
			[]string{"systemctl daemon-reload", "systemctl try-restart stackstead.service"}, ""},
		{"deb postinst configure after remove re-enables", "deb/postinst", owned + " marker", []string{"configure", "1.0.0"},
			[]string{"systemctl daemon-reload", "systemctl enable --now stackstead.service"}, "127.0.0.1:8420"},
		{"deb postinst abort does nothing", "deb/postinst", owned, []string{"abort-upgrade", "1.0.0"}, nil, ""},
		{"deb prerm remove stops and disables", "deb/prerm", owned, []string{"remove"},
			[]string{"systemctl stop stackstead.service", "systemctl disable stackstead.service"}, ""},
		{"deb prerm upgrade does nothing", "deb/prerm", owned, []string{"upgrade", "2.0.0"}, nil, ""},
		{"deb postrm remove reloads only", "deb/postrm", owned, []string{"remove"}, []string{"systemctl daemon-reload"}, ""},
		{"deb postrm upgrade does nothing", "deb/postrm", owned, []string{"upgrade", "2.0.0"}, nil, ""},
		{"deb postrm failed-upgrade does nothing", "deb/postrm", owned, []string{"failed-upgrade", "1.0.0"}, nil, ""},
		{"deb postrm purge removes user", "deb/postrm", owned, []string{"purge"},
			[]string{"systemctl daemon-reload", "userdel stackstead"}, ""},
		{"rpm pre 1 creates group and user", "rpm/pre", none, []string{"1"},
			[]string{"groupadd --system docker", "groupadd --system stackstead",
				"useradd --system --gid stackstead --groups docker --home-dir /var/lib/stackstead --no-create-home --shell /sbin/nologin stackstead"}, ""},
		{"rpm pre 2 re-adds docker group membership", "rpm/pre", owned, []string{"2"}, []string{"usermod -aG docker stackstead"}, ""},
		{"rpm pre 3 is treated as an upgrade", "rpm/pre", owned, []string{"3"}, []string{"usermod -aG docker stackstead"}, ""},
		{"rpm post 1 enables and starts", "rpm/post", owned, []string{"1"},
			[]string{"systemctl daemon-reload", "systemctl enable --now stackstead.service"}, "127.0.0.1:8420"},
		{"rpm post 2 try-restarts", "rpm/post", owned, []string{"2"},
			[]string{"systemctl daemon-reload", "systemctl try-restart stackstead.service"}, ""},
		{"rpm post 3 is treated as an upgrade", "rpm/post", owned, []string{"3"},
			[]string{"systemctl daemon-reload", "systemctl try-restart stackstead.service"}, ""},
		{"rpm preun 1 (upgrade) does nothing", "rpm/preun", owned, []string{"1"}, nil, ""},
		{"rpm preun 0 stops and disables", "rpm/preun", owned, []string{"0"},
			[]string{"systemctl stop stackstead.service", "systemctl disable stackstead.service"}, ""},
		{"rpm postun 1 reloads only", "rpm/postun", owned, []string{"1"}, []string{"systemctl daemon-reload"}, ""},
		{"rpm postun 0 reloads only", "rpm/postun", owned, []string{"0"}, []string{"systemctl daemon-reload"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, true)
			cmds, out := e.run(tc.script, tc.existing, tc.args...)
			got := only(cmds, "systemctl", "useradd", "usermod", "groupadd", "userdel")
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("commands = %q, want %q", got, tc.want)
			}
			if tc.note != "" && !strings.Contains(out, tc.note) {
				t.Errorf("output lacks setup hint %q:\n%s", tc.note, out)
			}
			if tc.note == "" && strings.Contains(out, "127.0.0.1:8420") {
				t.Errorf("setup hint printed outside a fresh install:\n%s", out)
			}
		})
	}
}

func TestRemoveLeavesMarkerAndReinstallConsumesIt(t *testing.T) {
	e := newEnv(t, true)
	e.run("deb/prerm", "passwd:stackstead", "remove")
	marker := filepath.Join(e.root, "var/lib/stackstead", markerName)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("prerm remove left no marker: %v", err)
	}
	e.run("deb/postinst", "passwd:stackstead", "configure", "1.0.0")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("marker not consumed by postinst (err=%v)", err)
	}
	e.run("deb/prerm", "passwd:stackstead", "upgrade", "2.0.0")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("prerm upgrade must not leave a marker")
	}
}

func TestPurgeRemovesConfigAndDataButNeverStacks(t *testing.T) {
	e := newEnv(t, true)
	e.run("deb/postrm", "passwd:stackstead", "purge")
	for _, gone := range []string{"etc/stackstead", "var/lib/stackstead"} {
		if _, err := os.Stat(filepath.Join(e.root, gone)); !os.IsNotExist(err) {
			t.Errorf("%s still present after purge (err=%v)", gone, err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(e.root, "opt/stacks/keep.txt")); err != nil || string(b) != "stack" {
		t.Errorf("/opt/stacks content lost on purge: %v", err)
	}
	for _, l := range readLines(e.logFile) {
		if strings.HasPrefix(l, "groupadd") || strings.Contains(l, "groupdel") {
			t.Errorf("purge touched groups: %q", l)
		}
	}
}

func TestNonDestructiveBranchesKeepData(t *testing.T) {
	cases := []struct {
		script string
		args   []string
	}{
		{"deb/postrm", []string{"remove"}}, {"deb/postrm", []string{"upgrade", "2.0.0"}},
		{"deb/postrm", []string{"failed-upgrade", "1.0.0"}}, {"deb/postrm", []string{"abort-install"}},
		{"deb/postrm", []string{"abort-upgrade", "1.0.0"}}, {"deb/prerm", []string{"remove"}},
		{"rpm/postun", []string{"0"}}, {"rpm/postun", []string{"1"}}, {"rpm/preun", []string{"0"}},
	}
	for _, tc := range cases {
		t.Run(tc.script+" "+strings.Join(tc.args, " "), func(t *testing.T) {
			e := newEnv(t, true)
			e.run(tc.script, "passwd:stackstead", tc.args...)
			for _, keep := range []string{"etc/stackstead/config.yaml", "var/lib/stackstead/stackstead.db", "opt/stacks/keep.txt"} {
				if _, err := os.Stat(filepath.Join(e.root, keep)); err != nil {
					t.Errorf("%s lost: %v", keep, err)
				}
			}
			if got := only(readLines(e.logFile), "userdel"); got != nil {
				t.Errorf("user removed: %q", got)
			}
		})
	}
}

func TestPostinstCreatesStacksDirAndFixesOwnership(t *testing.T) {
	for _, tc := range []struct {
		script string
		args   []string
	}{{"deb/postinst", []string{"configure", "1.0.0"}}, {"rpm/post", []string{"1"}}} {
		t.Run(tc.script, func(t *testing.T) {
			e := newEnv(t, true)
			if err := os.RemoveAll(filepath.Join(e.root, "opt/stacks")); err != nil {
				t.Fatal(err)
			}
			cmds, _ := e.run(tc.script, "passwd:stackstead", tc.args...)
			st, err := os.Stat(filepath.Join(e.root, "opt/stacks"))
			if err != nil || !st.IsDir() {
				t.Fatalf("/opt/stacks not created: %v", err)
			}
			if got := st.Mode() & (os.ModePerm | os.ModeSetgid); got != 0o775|os.ModeSetgid {
				t.Errorf("new /opt/stacks mode = %v, want 2775", got)
			}
			joined := strings.Join(cmds, "\n")
			for _, want := range []string{"chown stackstead:docker " + e.root + "/opt/stacks", "chown stackstead:stackstead " + e.root + "/var/lib/stackstead", "chgrp stackstead " + e.root + "/etc/stackstead/config.yaml"} {
				if !strings.Contains(joined, want) {
					t.Errorf("missing %q in:\n%s", want, joined)
				}
			}
		})
	}
}

// Upgrades and reinstalls never touch an existing stacks dir: the admin may
// have chosen another owner or mode for it.
func TestExistingStacksDirIsNeverChanged(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		args         []string
	}{
		{"deb upgrade", "deb/postinst", []string{"configure", "1.0.0"}},
		{"deb fresh with leftover dir", "deb/postinst", []string{"configure"}},
		{"rpm upgrade", "rpm/post", []string{"2"}},
		{"rpm fresh or reinstall", "rpm/post", []string{"1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, true)
			stacks := filepath.Join(e.root, "opt/stacks")
			if err := os.Chmod(stacks, 0o750); err != nil {
				t.Fatal(err)
			}
			cmds, _ := e.run(tc.script, "passwd:stackstead group:docker", tc.args...)
			for _, c := range cmds {
				if strings.HasSuffix(c, " "+stacks) {
					t.Errorf("existing /opt/stacks touched: %q", c)
				}
			}
			if st, err := os.Stat(stacks); err != nil || st.Mode()&(os.ModePerm|os.ModeSetgid) != 0o750 {
				t.Errorf("existing /opt/stacks mode changed: %v %v", st.Mode(), err)
			}
		})
	}
}

func TestDockerWarningWhenDockerMissing(t *testing.T) {
	e := newEnv(t, true)
	if err := os.Remove(filepath.Join(e.bin, "docker")); err != nil {
		t.Fatal(err)
	}
	_, out := e.run("deb/postinst", "passwd:stackstead group:docker", "configure")
	if !strings.Contains(strings.ToLower(out), "docker") {
		t.Errorf("no Docker warning in output:\n%s", out)
	}
	e2 := newEnv(t, true)
	_, out = e2.run("deb/postinst", "passwd:stackstead group:docker", "configure")
	if strings.Contains(strings.ToLower(out), "docker was not found") {
		t.Errorf("Docker warning printed although docker is present:\n%s", out)
	}
}

func TestScriptsIdempotent(t *testing.T) {
	for _, tc := range []struct {
		script string
		args   []string
	}{{"deb/postinst", []string{"configure"}}, {"rpm/post", []string{"1"}}} {
		e := newEnv(t, true)
		if err := os.RemoveAll(filepath.Join(e.root, "opt/stacks")); err != nil {
			t.Fatal(err)
		}
		first, _ := e.run(tc.script, "passwd:stackstead", tc.args...)
		second, _ := e.run(tc.script, "passwd:stackstead", tc.args...)
		// the stacks dir is set up once, when it is created; everything else repeats
		var want []string
		for _, c := range first {
			if !strings.HasSuffix(c, " "+filepath.Join(e.root, "opt/stacks")) {
				want = append(want, c)
			}
		}
		if len(want) == len(first) {
			t.Errorf("%s: the first run did not set up the new stacks dir: %q", tc.script, first)
		}
		if !reflect.DeepEqual(want, second) {
			t.Errorf("%s: second run differs:\n%q\n%q", tc.script, want, second)
		}
		if b, err := os.ReadFile(filepath.Join(e.root, "var/lib/stackstead/stackstead.db")); err != nil || string(b) != "db" {
			t.Errorf("%s touched the database: %v", tc.script, err)
		}
	}
}

func allScripts() map[string][]string {
	return map[string][]string{
		"deb/preinst": {"install"}, "deb/postinst": {"configure"}, "deb/prerm": {"remove"}, "deb/postrm": {"remove"},
		"rpm/pre": {"1"}, "rpm/post": {"1"}, "rpm/preun": {"0"}, "rpm/postun": {"0"},
	}
}

func TestScriptsSurviveMissingSystemctl(t *testing.T) {
	for script, args := range allScripts() {
		t.Run(script, func(t *testing.T) {
			e := newEnv(t, false)
			e.run(script, "passwd:stackstead", args...)
		})
	}
}

func TestScriptsSkipSystemctlWhenNotBooted(t *testing.T) {
	for script, args := range allScripts() {
		t.Run(script, func(t *testing.T) {
			e := newEnv(t, true)
			if err := os.RemoveAll(filepath.Join(e.root, "run/systemd/system")); err != nil {
				t.Fatal(err)
			}
			cmds, _ := e.run(script, "passwd:stackstead", args...)
			if got := only(cmds, "systemctl"); got != nil {
				t.Errorf("systemctl called on a host that is not booted with systemd: %q", got)
			}
		})
	}
}

func TestScriptsArePosix(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	shellcheck, _ := exec.LookPath("shellcheck")
	for script := range allScripts() {
		t.Run(script, func(t *testing.T) {
			b, err := os.ReadFile(script)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(b), "#!/bin/sh\n") {
				t.Errorf("first line must be #!/bin/sh")
			}
			if !strings.Contains(string(b), "\nset -e\n") {
				t.Errorf("script must use set -e")
			}
			if out, err := exec.Command(sh, "-n", script).CombinedOutput(); err != nil {
				t.Errorf("sh -n: %v\n%s", err, out)
			}
			if shellcheck != "" {
				if out, err := exec.Command(shellcheck, "--shell=sh", "--severity=style", script).CombinedOutput(); err != nil {
					t.Errorf("shellcheck: %v\n%s", err, out)
				}
			}
		})
	}
}

// knownUnitKeys is the set of directives the unit may use, all known to
// systemd 245 (Ubuntu 20.04). A typo fails here without needing systemd.
var knownUnitKeys = map[string]map[string]bool{
	"Unit": set("Description", "Documentation", "After", "Wants"),
	"Service": set("Type", "User", "Group", "SupplementaryGroups", "Environment", "ExecStartPre", "ExecStart", "Restart", "RestartSec",
		"StateDirectory", "StateDirectoryMode", "UMask", "NoNewPrivileges", "PrivateTmp", "ProtectSystem",
		"ProtectKernelTunables", "ProtectKernelModules", "ProtectKernelLogs", "ProtectControlGroups", "ProtectClock",
		"ProtectHostname", "RestrictSUIDSGID", "RestrictRealtime", "RestrictNamespaces", "LockPersonality",
		"SystemCallArchitectures", "CapabilityBoundingSet", "AmbientCapabilities"),
	"Install": set("WantedBy"),
}

func set(keys ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return m
}

func parseUnit(t *testing.T) map[string][][2]string {
	t.Helper()
	f, err := os.Open(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sections := map[string][][2]string{}
	section := ""
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";"):
		case strings.HasPrefix(line, "["):
			section = strings.Trim(line, "[]")
		default:
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				t.Fatalf("malformed unit line %q", line)
			}
			sections[section] = append(sections[section], [2]string{k, v})
		}
	}
	return sections
}

func TestUnitHasNoUnknownDirectives(t *testing.T) {
	u := parseUnit(t)
	for _, sec := range []string{"Unit", "Service", "Install"} {
		if len(u[sec]) == 0 {
			t.Errorf("section [%s] missing or empty", sec)
		}
	}
	for sec, kvs := range u {
		allowed, ok := knownUnitKeys[sec]
		if !ok {
			t.Errorf("unexpected section [%s]", sec)
			continue
		}
		for _, kv := range kvs {
			if !allowed[kv[0]] {
				t.Errorf("[%s] unknown directive %q", sec, kv[0])
			}
		}
	}
	get := func(key string) []string {
		var vs []string
		for _, kv := range u["Service"] {
			if kv[0] == key {
				vs = append(vs, kv[1])
			}
		}
		return vs
	}
	// Plan 6 contract: serve subcommand plus a config validation gate.
	if got := get("ExecStart"); !reflect.DeepEqual(got, []string{"/usr/bin/stackstead serve --config /etc/stackstead/config.yaml"}) {
		t.Errorf("ExecStart = %q", got)
	}
	if got := get("ExecStartPre"); !reflect.DeepEqual(got, []string{"/usr/bin/stackstead config validate --config /etc/stackstead/config.yaml"}) {
		t.Errorf("ExecStartPre = %q", got)
	}
	if got := get("SupplementaryGroups"); !reflect.DeepEqual(got, []string{"docker"}) {
		t.Errorf("SupplementaryGroups = %q", got)
	}
	if got := get("ProtectSystem"); !reflect.DeepEqual(got, []string{"full"}) {
		t.Errorf("ProtectSystem = %q, want full (never strict: external compose files)", got)
	}
	if got := get("CapabilityBoundingSet"); !reflect.DeepEqual(got, []string{""}) {
		t.Errorf("CapabilityBoundingSet = %q, want empty", got)
	}
	for key, want := range map[string]string{"NoNewPrivileges": "true", "UMask": "0007", "StateDirectory": "stackstead", "StateDirectoryMode": "0700", "User": "stackstead"} {
		if got := get(key); !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if len(get("ProtectHome")) != 0 {
		t.Errorf("ProtectHome must not be set: compose files may live under /home")
	}
}

type nfpmFile struct {
	Src  string `yaml:"src"`
	Dst  string `yaml:"dst"`
	Type string `yaml:"type"`
	FI   struct {
		Owner string `yaml:"owner"`
		Group string `yaml:"group"`
		Mode  any    `yaml:"mode"`
	} `yaml:"file_info"`
	Packager string `yaml:"packager"`
	Expand   bool   `yaml:"expand"`
}

type nfpmDoc struct {
	Name       string     `yaml:"name"`
	License    string     `yaml:"license"`
	Recommends []string   `yaml:"recommends"`
	Depends    []string   `yaml:"depends"`
	Contents   []nfpmFile `yaml:"contents"`
	Overrides  map[string]struct {
		Recommends []string          `yaml:"recommends"`
		Depends    []string          `yaml:"depends"`
		Scripts    map[string]string `yaml:"scripts"`
	} `yaml:"overrides"`
	Deb struct {
		Scripts map[string]string `yaml:"scripts"`
	} `yaml:"deb"`
	RPM struct {
		Scripts map[string]string `yaml:"scripts"`
	} `yaml:"rpm"`
}

func TestNfpmYamlLists(t *testing.T) {
	b, err := os.ReadFile("../nfpm.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var d nfpmDoc
	if err := yaml.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	if d.Name != "stackstead" || d.License != "AGPL-3.0-only" {
		t.Errorf("name/license = %q/%q", d.Name, d.License)
	}
	if len(d.Recommends) == 0 {
		t.Errorf("recommends missing")
	}
	if len(d.Depends) != 0 {
		t.Errorf("depends must be absent (Docker is a weak dependency), got %v", d.Depends)
	}
	for format, o := range d.Overrides {
		if len(o.Depends) != 0 {
			t.Errorf("override %s sets depends", format)
		}
	}
	byDst := map[string]nfpmFile{}
	for _, f := range d.Contents {
		byDst[f.Dst+"|"+f.Packager] = f
	}
	find := func(dst, packager string) (nfpmFile, bool) {
		f, ok := byDst[dst+"|"+packager]
		if !ok {
			f, ok = byDst[dst+"|"]
		}
		return f, ok
	}
	if f, ok := find("/usr/bin/stackstead", ""); !ok || f.Src == "" {
		t.Errorf("binary entry missing")
	}
	if f, ok := find("/etc/stackstead/config.yaml", ""); !ok || f.Type != "config|noreplace" || f.FI.Owner != "root" || f.FI.Group != "stackstead" {
		t.Errorf("config entry wrong: %+v (ok=%v)", f, ok)
	}
	if f, ok := find("/var/lib/stackstead", ""); !ok || f.Type != "dir" || f.FI.Owner != "stackstead" {
		t.Errorf("state dir entry wrong: %+v", f)
	}
	// /opt is not packageable (lintian) and dpkg/rpm would delete the dir when empty: scripts create it.
	if _, ok := find("/opt/stacks", ""); ok {
		t.Errorf("/opt/stacks must not be in the package payload")
	}
	if _, ok := find("/usr/lib/systemd/system/stackstead.service", ""); !ok {
		t.Errorf("unit entry missing")
	}
	if _, ok := find("/usr/share/doc/stackstead/copyright", "deb"); !ok {
		t.Errorf("deb copyright entry missing")
	}
	if f, ok := find("/usr/share/licenses/stackstead/LICENSE", "rpm"); !ok || f.Type != "license" {
		t.Errorf("rpm license entry missing: %+v", f)
	}
	for _, f := range d.Contents {
		if strings.Contains(f.Src, "${") && !f.Expand {
			t.Errorf("src %s uses a variable but lacks expand: true", f.Src)
		}
		if f.Src != "" && !strings.Contains(f.Src, "${") && f.Dst != "" {
			if f.Src == "LICENSE" { // created by Plan 7 task 1; checked by the package build
				continue
			}
			if _, err := os.Stat(filepath.Join("..", "..", f.Src)); err != nil {
				t.Errorf("src %s does not exist: %v", f.Src, err)
			}
		}
		switch {
		case strings.HasPrefix(f.Dst, "/usr/"), strings.HasPrefix(f.Dst, "/etc/stackstead"), f.Dst == "/var/lib/stackstead":
		default:
			t.Errorf("package installs outside the allowed paths: %s", f.Dst)
		}
	}
	scripts := map[string]map[string]string{"deb": scriptsOf(d, "deb"), "rpm": scriptsOf(d, "rpm")}
	for format, want := range map[string][]string{"deb": {"preinstall", "postinstall", "preremove", "postremove"}, "rpm": {"preinstall", "postinstall", "preremove", "postremove"}} {
		for _, key := range want {
			p := scripts[format][key]
			if p == "" {
				t.Errorf("%s script %s not configured", format, key)
				continue
			}
			if !strings.Contains(p, "pkg/"+format+"/") {
				t.Errorf("%s %s = %s, want a deploy/pkg/%s/ script", format, key, p, format)
			}
			if _, err := os.Stat(filepath.Join("..", "..", p)); err != nil {
				t.Errorf("%s %s: %v", format, key, err)
			}
		}
	}
}

func scriptsOf(d nfpmDoc, format string) map[string]string {
	if s := d.Overrides[format].Scripts; len(s) > 0 {
		return s
	}
	if format == "deb" {
		return d.Deb.Scripts
	}
	return d.RPM.Scripts
}

func TestCopyrightIsDEP5(t *testing.T) {
	b, err := os.ReadFile("copyright")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "\nCopyright: 2026 Stackstead contributors\n") {
		t.Errorf("copyright line lacks the year")
	}
	if !strings.HasPrefix(s, "Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/\n") {
		t.Errorf("copyright does not start with the DEP-5 Format line")
	}
	if !strings.Contains(s, "\nUpstream-Name: stackstead\n") || !strings.Contains(s, "\nLicense: AGPL-3.0-only\n") || !strings.Contains(s, "/usr/share/common-licenses/AGPL-3") {
		t.Errorf("copyright lacks upstream name, AGPL-3.0-only or the common-licenses reference")
	}
}
