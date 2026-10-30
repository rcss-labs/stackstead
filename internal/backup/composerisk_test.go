package backup

import (
	"slices"
	"strings"
	"testing"
)

func TestScanComposeRisks(t *testing.T) {
	r := func(service, kind, detail string) ComposeRisk {
		return ComposeRisk{Stack: "web", File: "compose.yaml", Service: service, Kind: kind, Detail: detail}
	}
	tests := []struct {
		name string
		src  string
		want []ComposeRisk
	}{
		{"privileged bool", "services:\n  app:\n    privileged: true\n", []ComposeRisk{r("app", "privileged", "true")}},
		{"privileged string", "services:\n  app:\n    privileged: \"true\"\n", []ComposeRisk{r("app", "privileged", "true")}},
		{"privileged false", "services:\n  app:\n    privileged: false\n", nil},
		{"cap_add", "services:\n  app:\n    cap_add: [SYS_ADMIN, NET_ADMIN]\n", []ComposeRisk{r("app", "cap_add", "SYS_ADMIN"), r("app", "cap_add", "NET_ADMIN")}},
		{"devices", "services:\n  app:\n    devices:\n      - /dev/sda:/dev/xvda\n", []ComposeRisk{r("app", "device", "/dev/sda:/dev/xvda")}},
		{"root bind", "services:\n  app:\n    volumes:\n      - /:/host\n", []ComposeRisk{r("app", "host_bind", "/:/host")}},
		{"home bind", "services:\n  app:\n    volumes:\n      - ~/data:/x\n", []ComposeRisk{r("app", "host_bind", "~/data:/x")}},
		{"parent bind", "services:\n  app:\n    volumes:\n      - ../other:/x\n", []ComposeRisk{r("app", "host_bind", "../other:/x")}},
		{"long syntax docker socket", "services:\n  app:\n    volumes:\n      - type: bind\n        source: /var/run/docker.sock\n        target: /var/run/docker.sock\n",
			[]ComposeRisk{r("app", "host_bind", "/var/run/docker.sock:/var/run/docker.sock")}},
		{"pid host", "services:\n  app:\n    pid: host\n", []ComposeRisk{r("app", "host_namespace", "pid: host")}},
		{"network_mode host", "services:\n  app:\n    network_mode: host\n", []ComposeRisk{r("app", "host_namespace", "network_mode: host")}},
		{"security_opt", "services:\n  app:\n    security_opt: [seccomp:unconfined]\n", []ComposeRisk{r("app", "security_opt", "seccomp:unconfined")}},
		{"variable source reported verbatim", "services:\n  app:\n    volumes:\n      - ${HOST_DIR}:/x\n", []ComposeRisk{r("app", "host_bind", "${HOST_DIR}:/x")}},
		{"variable absolute source reported verbatim", "services:\n  app:\n    volumes:\n      - /${HOST_DIR}:/x\n", []ComposeRisk{r("app", "host_bind", "/${HOST_DIR}:/x")}},
		{"named volume", "services:\n  app:\n    volumes:\n      - data:/data\n", nil},
		{"relative bind", "services:\n  app:\n    volumes:\n      - ./conf:/etc/x\n", nil},
		{"driver_opts device", "services: {}\nvolumes:\n  data:\n    driver_opts:\n      type: none\n      o: bind\n      device: /srv/secret\n",
			[]ComposeRisk{{Stack: "web", File: "compose.yaml", Kind: "host_bind", Detail: "volume data: device /srv/secret"}}},
		{"invalid yaml", "services: [\n", []ComposeRisk{{Stack: "web", File: "compose.yaml", Kind: "unparseable", Detail: ""}}},
		{"not a mapping", "- a\n- b\n", []ComposeRisk{{Stack: "web", File: "compose.yaml", Kind: "unparseable", Detail: ""}}},
		{"no services", "name: web\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ScanComposeRisks("web", "compose.yaml", []byte(tt.src))
			if len(got) == 1 && got[0].Kind == "unparseable" && len(tt.want) == 1 {
				got[0].Detail = "" // the parser's message is not part of the contract
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("risks = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// The details come from an untrusted file and are printed to a terminal and
// shown in the UI: control characters never get through and long text is cut.
func TestScanComposeRisksCleansText(t *testing.T) {
	src := "services:\n  app:\n    cap_add: [\"A\\e[31mB\\nC\", \"" + strings.Repeat("x", 500) + "\"]\n"
	got := ScanComposeRisks("web", "compose.yaml", []byte(src))
	if len(got) != 2 || got[0].Detail != "A[31mBC" || len([]rune(got[1].Detail)) > 200 {
		t.Errorf("risks = %+v, want control characters dropped and details capped at 200 runes", got)
	}
}

func TestScanComposeRisksBoundsInput(t *testing.T) {
	big := "services:\n  app:\n    privileged: true\n" + strings.Repeat("# pad\n", maxArchivedCompose/6+10)
	got := ScanComposeRisks("web", "compose.yaml", []byte(big))
	if len(got) != 1 || got[0].Kind != "unparseable" {
		t.Errorf("oversized file: risks = %+v, want one unparseable", got)
	}
}

func TestIsComposeFileName(t *testing.T) {
	for name, want := range map[string]bool{
		"compose.yaml": true, "compose.yml": true, "docker-compose.yml": true, "docker-compose.override.yaml": true,
		"compose.prod.yml": true, ".env": false, "config.yaml": false, "compose.txt": false,
	} {
		if got := isComposeFileName(name); got != want {
			t.Errorf("isComposeFileName(%q) = %v, want %v", name, got, want)
		}
	}
}
