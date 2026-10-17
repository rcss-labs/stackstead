package stacks_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

func multiRequest(services ...composefile.NamedService) stacks.CreateRequest {
	return stacks.CreateRequest{Name: "lab", Services: services, IndexedFields: true}
}

func TestCreateMultiWritesAllServicesInOrder(t *testing.T) {
	h := newHarness(t)
	req := multiRequest(composefile.NamedService{Name: "web", Config: webCfg()}, composefile.NamedService{Name: "db", Config: webCfg()})
	sum, err := h.svc.Create(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(h.path("lab"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := composefile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	names, err := f.Services()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"web", "db"}) || sum.Services != 2 {
		t.Fatalf("names=%v summary=%+v", names, sum)
	}
	e, err := h.reg.Get(context.Background(), "lab")
	if err != nil || e.ComposePath != h.path("lab") || e.Origin != stacks.OriginManaged {
		t.Fatalf("entry=%+v err=%v", e, err)
	}
}

func TestCreateDefaultsFirstServiceNameToStackName(t *testing.T) {
	h := newHarness(t)
	_, err := h.svc.Create(context.Background(), multiRequest(composefile.NamedService{Config: webCfg()}))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(h.path("lab"))
	f, _ := composefile.Parse(data)
	names, _ := f.Services()
	if !reflect.DeepEqual(names, []string{"lab"}) {
		t.Fatal(names)
	}
}

func TestCreateMultiFieldErrors(t *testing.T) {
	tests := []struct {
		name     string
		services []composefile.NamedService
		fields   []string
		owner    string
	}{
		{"missing", []composefile.NamedService{{Name: "web", Config: webCfg()}, {Config: webCfg()}}, []string{"services[1].service"}, "required"},
		{"duplicate", []composefile.NamedService{{Name: "web", Config: webCfg()}, {Name: "db", Config: webCfg()}, {Name: "web", Config: webCfg()}}, []string{"services[2].service"}, "duplicate service name web"},
		{"ports", []composefile.NamedService{{Name: "web", Config: webCfg("8080:80")}, {Name: "db", Config: webCfg("9090:90")}, {Name: "other", Config: webCfg("8080:80")}}, []string{"services[2].ports[0]"}, "service web"},
		{"ips", []composefile.NamedService{{Name: "web", Config: composefile.ServiceConfig{Image: "nginx", Networks: []composefile.NetworkAttachment{{Name: "lan", IPv4Address: "10.0.0.5"}}}}, {Name: "db", Config: composefile.ServiceConfig{Image: "nginx", Networks: []composefile.NetworkAttachment{{Name: "lan", IPv4Address: "10.0.0.5"}}}}}, []string{"services[1].networks.lan.ipv4_address"}, "service web"},
		{"engine before invalid", []composefile.NamedService{{Name: "web", Config: webCfg("7070:80")}, {Name: "db", Config: composefile.ServiceConfig{Build: &composefile.Build{Context: "/etc"}}}}, []string{"services[0].ports[0]", "services[1].build.context"}, ""},
		{"invalid before engine", []composefile.NamedService{{Name: "bad.name", Config: webCfg()}, {Name: "db", Config: webCfg("7070:80")}}, []string{"services[0].service", "services[1].ports[0]"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.eng.containers = []engine.Container{publishing("other", 7070)}
			h.eng.networks = []engine.Network{{Name: "lan", Subnets: []engine.Subnet{{Prefix: mustPrefix("10.0.0.0/24")}}}}
			_, err := h.svc.Create(context.Background(), multiRequest(tt.services...))
			fs := fieldsOf(t, err)
			var paths []string
			for _, f := range fs {
				paths = append(paths, f.Field)
			}
			if !reflect.DeepEqual(paths, tt.fields) {
				t.Fatalf("fields=%+v", fs)
			}
			if tt.owner != "" && !strings.Contains(fs[0].Message, tt.owner) {
				t.Fatal(fs)
			}
			if _, err := os.Stat(h.path("lab")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("file written: %v", err)
			}
		})
	}
}

func TestCreateRejectsServiceCountBounds(t *testing.T) {
	for _, count := range []int{0, 21} {
		t.Run(string(rune('a'+count)), func(t *testing.T) {
			h := newHarness(t)
			_, err := h.svc.Create(context.Background(), multiRequest(make([]composefile.NamedService, count)...))
			fs := fieldsOf(t, err)
			if len(fs) != 1 || fs[0].Field != "services" {
				t.Fatal(fs)
			}
		})
	}
}

func TestCreateBuildImageDefaultPerService(t *testing.T) {
	h := newHarness(t)
	cfg := composefile.ServiceConfig{Build: &composefile.Build{}}
	_, err := h.svc.Create(context.Background(), multiRequest(composefile.NamedService{Name: "lab", Config: cfg}, composefile.NamedService{Name: "worker", Config: cfg}))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(h.path("lab"))
	f, _ := composefile.Parse(data)
	for name, image := range map[string]string{"lab": "stackstead/lab:latest", "worker": "stackstead/lab-worker:latest"} {
		svc, err := f.Service(name)
		if err != nil || svc.Config.Image != image {
			t.Fatalf("%s: %+v %v", name, svc, err)
		}
	}
	if cfg.Build.Context != "" {
		t.Fatal("mutated caller build")
	}
}

func TestCreateMultiFailureLeavesNoTrace(t *testing.T) {
	h := newHarness(t)
	h.compose.configErr = errors.New("invalid compose")
	_, err := h.svc.Create(context.Background(), multiRequest(composefile.NamedService{Name: "web", Config: webCfg()}, composefile.NamedService{Name: "db", Config: webCfg()}))
	if !errors.Is(err, h.compose.configErr) {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.path("lab")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := h.reg.Get(context.Background(), "lab"); !errors.Is(err, stacks.ErrStackNotFound) {
		t.Fatal(err)
	}
}

// Editing an earlier service must still check a later, untouched sibling.
func TestUpdateServicePortClashWithLaterSiblingStillReported(t *testing.T) {
	h := newHarness(t)
	_, err := h.svc.Create(context.Background(), multiRequest(composefile.NamedService{Name: "a", Config: webCfg()}, composefile.NamedService{Name: "b", Config: webCfg("8080:80")}))
	if err != nil {
		t.Fatal(err)
	}
	_, hash, err := composefile.Load(h.path("lab"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.svc.UpdateService(context.Background(), "lab", "a", webCfg("8080:80"), hash)
	fs := fieldsOf(t, err)
	if len(fs) != 1 || fs[0].Field != "ports[0]" || !strings.Contains(fs[0].Message, "service b") {
		t.Fatal(fs)
	}
}

func TestUpdateServiceStaticIPClashWithLaterSibling(t *testing.T) {
	h := newHarness(t)
	h.eng.networks = []engine.Network{{Name: "lan", Subnets: []engine.Subnet{{Prefix: mustPrefix("10.0.0.0/24")}}}}
	config := func(ip string) composefile.ServiceConfig {
		return composefile.ServiceConfig{Image: "nginx", Networks: []composefile.NetworkAttachment{{Name: "lan", IPv4Address: ip}}}
	}
	_, err := h.svc.Create(context.Background(), multiRequest(composefile.NamedService{Name: "a", Config: config("10.0.0.5")}, composefile.NamedService{Name: "b", Config: config("10.0.0.6")}))
	if err != nil {
		t.Fatal(err)
	}
	_, hash, _ := composefile.Load(h.path("lab"))
	_, err = h.svc.UpdateService(context.Background(), "lab", "a", config("10.0.0.6"), hash)
	fs := fieldsOf(t, err)
	if len(fs) != 1 || fs[0].Field != "networks.lan.ipv4_address" || !strings.Contains(fs[0].Message, "service b") {
		t.Fatal(fs)
	}
}

func TestSaveRawStaticIPUsageIsIndependentPerService(t *testing.T) {
	h := newHarness(t)
	h.eng.networks = []engine.Network{{Name: "lan", Subnets: []engine.Subnet{{Prefix: mustPrefix("10.0.0.0/24")}}}}
	config := func(ip string) composefile.ServiceConfig {
		return composefile.ServiceConfig{Image: "nginx", Networks: []composefile.NetworkAttachment{{Name: "lan", IPv4Address: ip}}}
	}
	_, err := h.svc.Create(context.Background(), multiRequest(composefile.NamedService{Name: "a", Config: config("10.0.0.5")}, composefile.NamedService{Name: "b", Config: config("10.0.0.6")}, composefile.NamedService{Name: "c", Config: config("10.0.0.7")}))
	if err != nil {
		t.Fatal(err)
	}
	_, hash, _ := composefile.Load(h.path("lab"))
	data, err := composefile.New([]composefile.NamedService{{Name: "a", Config: config("10.0.0.8")}, {Name: "b", Config: config("10.0.0.9")}, {Name: "c", Config: config("10.0.0.10")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SaveRaw(context.Background(), "lab", data, hash); err != nil {
		t.Fatalf("independent addresses rejected: %v", err)
	}
}

// Same-file network identity must work before Docker creates project networks.
func TestLocalSiblingStaticIPsBeforeFirstUp(t *testing.T) {
	for _, tc := range []struct {
		name, network, ip string
		conflict          bool
	}{
		{"duplicate", "lan", "10.9.0.5", true},
		{"distinct address", "lan", "10.9.0.6", false},
		{"distinct network", "other", "10.9.0.5", false},
	} {
		for _, backup := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/backup=%t", tc.name, backup), func(t *testing.T) {
				h := newHarness(t)
				setupWeb(t, h)
				before := []byte(localNetworkYAML + "  other:\n    ipam:\n      config:\n        - subnet: 10.9.0.0/24\n")
				if err := os.WriteFile(h.path("web"), before, 0600); err != nil {
					t.Fatal(err)
				}
				bakPath := h.path("web") + ".bak"
				if backup {
					if err := os.WriteFile(bakPath, []byte("keep backup"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				_, hash, err := composefile.Load(h.path("web"))
				if err != nil {
					t.Fatal(err)
				}
				cfg := composefile.ServiceConfig{Image: "nginx", Networks: []composefile.NetworkAttachment{{Name: tc.network, IPv4Address: tc.ip}}}
				_, err = h.svc.AddService(context.Background(), "web", "cache", cfg, hash)
				if !tc.conflict {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				var validation *stacks.ValidationError
				if !errors.As(err, &validation) {
					t.Errorf("expected ValidationError, got %v", err)
				} else if fs := validation.Fields; len(fs) != 1 || fs[0].Field != "networks.lan.ipv4_address" || !strings.Contains(fs[0].Message, "service web") {
					t.Errorf("fields=%+v", fs)
				}
				after, err := os.ReadFile(h.path("web"))
				if err != nil {
					t.Fatal(err)
				}
				if string(after) != string(before) {
					t.Error("refused add changed file")
				}
				bak, err := os.ReadFile(bakPath)
				if backup {
					if err != nil || string(bak) != "keep backup" {
						t.Errorf("backup changed: %q %v", bak, err)
					}
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Errorf("backup created: %q %v", bak, err)
				}
			})
		}
	}
}

func TestLocalSiblingStaticIPConflictOnEditAndRawSave(t *testing.T) {
	for _, action := range []string{"edit", "raw"} {
		t.Run(action, func(t *testing.T) {
			h := newHarness(t)
			setupWeb(t, h)
			before := strings.Replace(localNetworkYAML, "networks:\n  lan:", "  cache:\n    image: nginx\n    networks:\n      lan:\n        ipv4_address: 10.9.0.6\nnetworks:\n  lan:", 1)
			if err := os.WriteFile(h.path("web"), []byte(before), 0600); err != nil {
				t.Fatal(err)
			}
			_, hash, err := composefile.Load(h.path("web"))
			if err != nil {
				t.Fatal(err)
			}
			field, owner := "networks.lan.ipv4_address", "service cache"
			if action == "edit" {
				_, err = h.svc.UpdateService(context.Background(), "web", "web", composefile.ServiceConfig{Image: "nginx", Networks: []composefile.NetworkAttachment{{Name: "lan", IPv4Address: "10.9.0.6"}}}, hash)
			} else {
				field, owner = "services.cache.networks.lan.ipv4_address", "service web"
				_, err = h.svc.SaveRaw(context.Background(), "web", []byte(strings.Replace(before, "10.9.0.6", "10.9.0.5", 1)), hash)
			}
			fs := fieldsOf(t, err)
			if len(fs) != 1 || fs[0].Field != field || !strings.Contains(fs[0].Message, owner) {
				t.Fatalf("fields=%+v", fs)
			}
			after, err := os.ReadFile(h.path("web"))
			if err != nil || string(after) != before {
				t.Fatalf("file changed: %v", err)
			}
		})
	}
}
