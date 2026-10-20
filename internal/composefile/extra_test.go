package composefile

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestParseExtra(t *testing.T) {
	zero := []string{"", "null", " null\n"}
	for _, raw := range zero {
		e, err := ParseExtra([]byte(raw))
		if err != nil || !e.IsZero() {
			t.Errorf("ParseExtra(%q) = %v, %v; want zero", raw, e, err)
		}
	}
	if e, err := ParseExtra(nil); err != nil || !e.IsZero() {
		t.Errorf("ParseExtra(nil) = %v, %v; want zero", e, err)
	}
	for _, raw := range []string{`[]`, `"x"`, `1`, `true`} {
		if _, err := ParseExtra([]byte(raw)); !errors.Is(err, ErrInvalidExtra) {
			t.Errorf("ParseExtra(%s) err = %v; want ErrInvalidExtra", raw, err)
		}
	}
	keys := map[string][]string{
		`{"labels":{"a":"b"}}`: {"labels"},
		`{"z":1,"a":2}`:        {"z", "a"},
	}
	for raw, want := range keys {
		e, err := ParseExtra([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if e.IsZero() || !reflect.DeepEqual(e.Keys(), want) {
			t.Errorf("ParseExtra(%s).Keys() = %v; want %v", raw, e.Keys(), want)
		}
	}
	if (Extra{}).Keys() != nil {
		t.Error("zero Extra should have no keys")
	}
}

func TestNewStackWithoutExtrasMatchesNew(t *testing.T) {
	fixtures := [][]NamedService{
		{{Name: "web", Config: ServiceConfig{Image: "nginx", Ports: []string{"80:80"}, Networks: []NetworkAttachment{{Name: "lan", IPv4Address: "10.0.0.2"}, {Name: "b"}}}}},
		{
			{Name: "a", Config: ServiceConfig{Image: "x", Environment: []EnvVar{{"K", "true"}}, Limits: Limits{CPUs: "0.5"}}},
			{Name: "b", Config: ServiceConfig{Build: &Build{Context: "."}, Healthcheck: &Healthcheck{Test: []string{"CMD", "true"}}}},
		},
	}
	for _, s := range fixtures {
		want, err := New(s)
		if err != nil {
			t.Fatal(err)
		}
		got, err := NewStack(s, Extra{})
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("NewStack differs from New:\n%s\nvs\n%s", got, want)
		}
	}
}

func mustExtra(t *testing.T, raw string) Extra {
	t.Helper()
	e, err := ParseExtra([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// decode reads rendered compose text into generic maps for value checks.
func decode(t *testing.T, src []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal(src, &m); err != nil {
		t.Fatalf("rendered file is not YAML: %v\n%s", err, src)
	}
	return m
}

func TestNewStackMergesServiceExtra(t *testing.T) {
	extra := mustExtra(t, `{"deploy":{"placement":{"constraints":["node.role==manager"]},"resources":{"limits":{"cpus":"2","pids":100}}},"labels":{"a":"b"}}`)
	src, err := NewStack([]NamedService{{Name: "web", Extra: extra, Config: ServiceConfig{
		Image: "nginx", Limits: Limits{CPUs: "0.5"},
	}}}, Extra{})
	if err != nil {
		t.Fatal(err)
	}
	web := decode(t, src)["services"].(map[string]any)["web"].(map[string]any)
	deploy := web["deploy"].(map[string]any)
	limits := deploy["resources"].(map[string]any)["limits"].(map[string]any)
	if limits["cpus"] != "0.5" {
		t.Errorf("cpus = %v; the form must win", limits["cpus"])
	}
	if limits["pids"] != 100 {
		t.Errorf("pids = %v; want 100", limits["pids"])
	}
	if _, ok := deploy["placement"]; !ok {
		t.Error("placement dropped")
	}
	if web["labels"].(map[string]any)["a"] != "b" {
		t.Error("labels dropped")
	}
	if strings.Index(string(src), "labels:") < strings.Index(string(src), "deploy:") {
		t.Errorf("extra-only keys must follow form keys:\n%s", src)
	}
	svc, err := mustParse(t, string(src)).Service("web")
	if err != nil {
		t.Fatal(err)
	}
	if svc.Config.Image != "nginx" || svc.Config.Limits.CPUs != "0.5" {
		t.Errorf("form fields do not read back: %+v", svc.Config)
	}
}

func TestNewStackKeepsUserNetworkDefinition(t *testing.T) {
	src, err := NewStack([]NamedService{{Name: "web", Config: ServiceConfig{
		Image: "x", Networks: []NetworkAttachment{{Name: "lan"}, {Name: "ext"}},
	}}}, mustExtra(t, `{"networks":{"lan":{"driver":"bridge"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	nets := decode(t, src)["networks"].(map[string]any)
	lan := nets["lan"].(map[string]any)
	if lan["driver"] != "bridge" {
		t.Errorf("lan = %v; want driver bridge", lan)
	}
	if _, ok := lan["external"]; ok {
		t.Errorf("lan must not be external: %v", lan)
	}
	if ext := nets["ext"].(map[string]any); ext["external"] != true {
		t.Errorf("ext = %v; want external true", ext)
	}
}

func TestNewStackTopExtraOrderAndKeys(t *testing.T) {
	src, err := NewStack([]NamedService{{Name: "web", Config: ServiceConfig{
		Image: "x", Networks: []NetworkAttachment{{Name: "lan"}},
	}}}, mustExtra(t, `{"volumes":{"data":{}},"x-common":{"a":1},"secrets":{"s":{"file":"./s"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		t.Fatal(err)
	}
	var keys []string
	m := root.Content[0]
	for i := 0; i < len(m.Content); i += 2 {
		keys = append(keys, m.Content[i].Value)
	}
	want := []string{"services", "networks", "volumes", "x-common", "secrets"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("top keys = %v; want %v\n%s", keys, want, src)
	}
}

func TestNewStackRejectsReservedTopKeys(t *testing.T) {
	for _, raw := range []string{`{"services":{}}`, `{"name":"x"}`} {
		_, err := NewStack([]NamedService{{Name: "web", Config: ServiceConfig{Image: "x"}}}, mustExtra(t, raw))
		if !errors.Is(err, ErrInvalidExtra) || !strings.Contains(err.Error(), "extra may not set services or name") {
			t.Errorf("top %s: err = %v", raw, err)
		}
	}
}

func TestParseExtraRejectsNonJSONObjects(t *testing.T) {
	for _, raw := range []string{
		`a: 1`, `{"a":1} {"b":2}`, `{"a":1,"a":2}`, `{"o":{"a":1,"a":2}}`,
		`{"o":[{"a":1,"a":2}]}`, "&x {\"a\":1}", `{"a": *x}`, `{a: 1}`, `{"a":1,}`,
	} {
		if _, err := ParseExtra([]byte(raw)); !errors.Is(err, ErrInvalidExtra) || err.Error() != "extra must be a JSON object" {
			t.Errorf("ParseExtra(%s) err = %v; want exactly ErrInvalidExtra", raw, err)
		}
	}
}

func TestNewStackServiceNetworksExtraKeepsAliases(t *testing.T) {
	src, err := NewStack([]NamedService{{Name: "web", Extra: mustExtra(t, `{"networks":{"lan":{"aliases":["w"],"ipv4_address":"10.0.0.9"}}}`),
		Config: ServiceConfig{Image: "x", Networks: []NetworkAttachment{{Name: "lan"}, {Name: "b"}}}}}, Extra{})
	if err != nil {
		t.Fatal(err)
	}
	nets := decode(t, src)["services"].(map[string]any)["web"].(map[string]any)["networks"].(map[string]any)
	lan := nets["lan"].(map[string]any)
	if lan["ipv4_address"] != "10.0.0.9" || len(lan["aliases"].([]any)) != 1 {
		t.Errorf("lan = %v; extra aliases/ip lost\n%s", lan, src)
	}
	if _, ok := nets["b"]; !ok {
		t.Errorf("form network b dropped\n%s", src)
	}
}

func TestNewStackKindMismatchIsAnError(t *testing.T) {
	_, err := NewStack([]NamedService{{Name: "web", Extra: mustExtra(t, `{"deploy":"bad"}`),
		Config: ServiceConfig{Image: "x", Limits: Limits{CPUs: "1"}}}}, Extra{})
	if !errors.Is(err, ErrInvalidExtra) || !strings.Contains(err.Error(), "services.web.deploy") {
		t.Errorf("service err = %v", err)
	}
	_, err = NewStack([]NamedService{{Name: "web", Config: ServiceConfig{Image: "x", Networks: []NetworkAttachment{{Name: "lan"}}}}},
		mustExtra(t, `{"networks":["lan"]}`))
	if !errors.Is(err, ErrInvalidExtra) || !strings.Contains(err.Error(), "networks") {
		t.Errorf("top err = %v", err)
	}
}

func TestExtraKeyNamedMergeIsQuotedNotAMergeKey(t *testing.T) {
	extra := mustExtra(t, `{"x-meta":{"<<":{"a":"b"},"keep":"1"}}`)
	src, err := NewStack([]NamedService{{Name: "web", Config: ServiceConfig{Image: "nginx"}}}, extra)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `"<<":`) {
		t.Errorf("key << must be double-quoted:\n%s", src)
	}
	meta := decode(t, src)["x-meta"].(map[string]any)
	if _, ok := meta["<<"]; !ok || meta["keep"] != "1" || meta["a"] != nil {
		t.Errorf("x-meta = %v; << must stay a literal key", meta)
	}
}
