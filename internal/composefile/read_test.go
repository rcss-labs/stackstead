package composefile

import (
	"errors"
	"reflect"
	"testing"
)

const labCompose = `# My home lab
name: lab

services:
  web:
    image: "nginx:1.27"   # pinned
    build:
      context: ./web
      dockerfile: Dockerfile.prod
    ports:
    - "8080:80"
    - 8443:443

    volumes:
      - ./html:/usr/share/nginx/html:ro
    environment:
      TZ: Europe/Berlin
      WORKERS: 4
    networks:
      lan:
        ipv4_address: 192.168.50.10
    restart: unless-stopped
    deploy:
      replicas: 1
      resources:
        limits:
          cpus: "0.50"
          memory: 256M
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost"]
      interval: 30s
      retries: 3
    x-custom: {a: 1}
  db:
    image: postgres:16
    environment:
      - POSTGRES_PASSWORD=secret
    healthcheck:
      test: pg_isready
    network_mode: host

networks:
  lan:
    external: true
`

func mustParse(t *testing.T, src string) *File {
	t.Helper()
	f, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return f
}

func TestServicesInFileOrder(t *testing.T) {
	names, err := mustParse(t, labCompose).Services()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"web", "db"}; !reflect.DeepEqual(names, want) {
		t.Errorf("Services() = %v, want %v", names, want)
	}
}

func TestServiceReadsAllModeledFields(t *testing.T) {
	svc, err := mustParse(t, labCompose).Service("web")
	if err != nil {
		t.Fatal(err)
	}
	want := ServiceConfig{
		Image:       "nginx:1.27",
		Build:       &Build{Context: "./web", Dockerfile: "Dockerfile.prod"},
		Ports:       []string{"8080:80", "8443:443"},
		Volumes:     []string{"./html:/usr/share/nginx/html:ro"},
		Environment: []EnvVar{{"TZ", "Europe/Berlin"}, {"WORKERS", "4"}},
		Networks:    []NetworkAttachment{{Name: "lan", IPv4Address: "192.168.50.10"}},
		Limits:      Limits{CPUs: "0.50", Memory: "256M"},
		Restart:     "unless-stopped",
		Healthcheck: &Healthcheck{Test: []string{"CMD", "curl", "-f", "http://localhost"}, Interval: "30s", Retries: 3},
	}
	if !reflect.DeepEqual(svc.Config, want) {
		t.Errorf("Config =\n%+v\nwant\n%+v", svc.Config, want)
	}
	if len(svc.ReadOnly) != 0 {
		t.Errorf("ReadOnly = %v, want none", svc.ReadOnly)
	}
}

func TestServiceReadsAlternateForms(t *testing.T) {
	svc, err := mustParse(t, labCompose).Service("db")
	if err != nil {
		t.Fatal(err)
	}
	if want := []EnvVar{{"POSTGRES_PASSWORD", "secret"}}; !reflect.DeepEqual(svc.Config.Environment, want) {
		t.Errorf("Environment = %v, want %v", svc.Config.Environment, want)
	}
	if want := []string{"CMD-SHELL", "pg_isready"}; !reflect.DeepEqual(svc.Config.Healthcheck.Test, want) {
		t.Errorf("Healthcheck.Test = %v, want %v", svc.Config.Healthcheck.Test, want)
	}
	if svc.Config.NetworkMode != "host" {
		t.Errorf("NetworkMode = %q, want host", svc.Config.NetworkMode)
	}
}

func TestServiceMarksUnrepresentableFieldsReadOnly(t *testing.T) {
	src := `x-env: &env
  A: b
x-base: &base
  restart: always
services:
  app:
    <<: *base
    image: app
    ports:
      - target: 80
        published: 8080
    environment: *env
    networks:
      lan:
        aliases: [app]
    build:
      context: .
      args:
        VERSION: "1"
    cpus: 0.5
    healthcheck:
      test: ["CMD", "true"]
      disable: true
`
	svc, err := mustParse(t, src).Service("app")
	if err != nil {
		t.Fatal(err)
	}
	want := []Field{FieldBuild, FieldPorts, FieldEnvironment, FieldNetworks, FieldLimits, FieldRestart, FieldHealthcheck}
	if !reflect.DeepEqual(svc.ReadOnly, want) {
		t.Errorf("ReadOnly = %v, want %v", svc.ReadOnly, want)
	}
	if svc.Config.Image != "app" || svc.Config.Ports != nil || svc.Config.Build != nil {
		t.Errorf("read-only fields leaked into Config: %+v", svc.Config)
	}
}

func TestEnvironmentPassThroughIsReadOnly(t *testing.T) {
	for _, src := range []string{
		"services:\n  a:\n    environment:\n      - DEBUG\n",
		"services:\n  a:\n    environment:\n      DEBUG:\n",
	} {
		svc, err := mustParse(t, src).Service("a")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(svc.ReadOnly, []Field{FieldEnvironment}) {
			t.Errorf("%q: ReadOnly = %v, want [environment]", src, svc.ReadOnly)
		}
	}
}

func TestParseRejectsNonComposeDocuments(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want error
	}{
		{"empty", "", ErrNotMapping},
		{"comment only", "# nothing\n", ErrNotMapping},
		{"sequence root", "- a\n", ErrNotMapping},
		{"two documents", "services: {}\n---\nservices: {}\n", ErrMultipleDocuments},
	}
	for _, src := range []string{"services: {}\n---\n", "services: {}\n---\n# comment\n"} {
		if _, err := Parse([]byte(src)); err != nil {
			t.Errorf("Parse(%q) = %v, want success", src, err)
		}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse([]byte(tt.src)); !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
	if _, err := Parse([]byte("services: [\n")); err == nil {
		t.Error("syntax error: want error, got nil")
	}
}

func TestServiceNotFound(t *testing.T) {
	if _, err := mustParse(t, labCompose).Service("nope"); !errors.Is(err, ErrServiceNotFound) {
		t.Errorf("err = %v, want ErrServiceNotFound", err)
	}
}

func TestAnchoredServiceIsReadOnly(t *testing.T) {
	src := `services:
  web: &web
    image: nginx
    restart: always
  web2:
    <<: *web
    ports: ["81:80"]
`
	svc, err := mustParse(t, src).Service("web")
	if err != nil {
		t.Fatal(err)
	}
	want := []Field{FieldImage, FieldBuild, FieldPorts, FieldVolumes, FieldEnvironment, FieldNetworks, FieldNetworkMode, FieldLimits, FieldRestart, FieldHealthcheck}
	if !reflect.DeepEqual(svc.ReadOnly, want) {
		t.Errorf("ReadOnly = %v, want %v", svc.ReadOnly, want)
	}
	if !reflect.DeepEqual(svc.Config, ServiceConfig{}) {
		t.Errorf("Config = %+v, want zero", svc.Config)
	}
}

func TestInheritedLegacyLimitsMakeLimitsReadOnly(t *testing.T) {
	src := "x-base: &base\n  mem_limit: 512m\nservices:\n  app:\n    <<: *base\n    deploy:\n      resources:\n        limits:\n          memory: 256M\n"
	svc, err := mustParse(t, src).Service("app")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(svc.ReadOnly, []Field{FieldLimits}) {
		t.Errorf("ReadOnly = %v, want [limits]", svc.ReadOnly)
	}
	if svc.Config.Limits != (Limits{}) {
		t.Errorf("Limits = %+v, want zero", svc.Config.Limits)
	}
}
