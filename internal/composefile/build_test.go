package composefile

import (
	"errors"
	"reflect"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestNewRendersCanonicalFile(t *testing.T) {
	got, err := New("web", ServiceConfig{
		Image:       "stackstead/web:latest",
		Build:       &Build{Context: "/home/me/web", Dockerfile: "Dockerfile"},
		Ports:       []string{"8080:80"},
		Volumes:     []string{"data:/data"},
		Environment: []EnvVar{{"TZ", "UTC"}, {"DEBUG", "true"}},
		Networks:    []NetworkAttachment{{Name: "lan", IPv4Address: "192.168.50.10"}},
		Limits:      Limits{CPUs: "0.5", Memory: "512M"},
		Restart:     "unless-stopped",
		Healthcheck: &Healthcheck{Test: []string{"CMD", "true"}, Interval: "30s", Retries: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `services:
  web:
    image: stackstead/web:latest
    build:
      context: /home/me/web
      dockerfile: Dockerfile
    restart: unless-stopped
    ports:
      - "8080:80"
    volumes:
      - data:/data
    environment:
      TZ: UTC
      DEBUG: "true"
    networks:
      lan:
        ipv4_address: 192.168.50.10
    deploy:
      resources:
        limits:
          cpus: "0.5"
          memory: 512M
    healthcheck:
      test: ["CMD", "true"]
      interval: 30s
      retries: 3
networks:
  lan:
    external: true
`
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestNewRoundTripsThroughService(t *testing.T) {
	cfgs := []ServiceConfig{
		{Image: "nginx"},
		{Build: &Build{Context: "."}, Ports: []string{}, Networks: []NetworkAttachment{{Name: "a"}, {Name: "b"}}},
		{Image: "x", NetworkMode: "host", Limits: Limits{Memory: "1G"}, Healthcheck: &Healthcheck{Test: []string{"CMD-SHELL", "true"}}},
	}
	for _, cfg := range cfgs {
		src, err := New("svc", cfg)
		if err != nil {
			t.Fatal(err)
		}
		svc, err := mustParse(t, string(src)).Service("svc")
		if err != nil {
			t.Fatal(err)
		}
		if want := normalize(cfg); !reflect.DeepEqual(svc.Config, want) {
			t.Errorf("round trip:\n got %+v\nwant %+v\nfile:\n%s", svc.Config, want, src)
		}
	}
}

func TestNewRejectsEmptyName(t *testing.T) {
	if _, err := New("", ServiceConfig{Image: "x"}); !errors.Is(err, ErrInvalidServiceName) {
		t.Errorf("err = %v, want ErrInvalidServiceName", err)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  ServiceConfig
	}{
		{"duplicate env name", ServiceConfig{Image: "x", Environment: []EnvVar{{"A", "1"}, {"A", "2"}}}},
		{"duplicate network name", ServiceConfig{Image: "x", Networks: []NetworkAttachment{{Name: "a"}, {Name: "a"}}}},
		{"empty env name", ServiceConfig{Image: "x", Environment: []EnvVar{{"", "1"}}}},
		{"env name with equals", ServiceConfig{Image: "x", Environment: []EnvVar{{"B=C", "D"}}}},
		{"env name with space", ServiceConfig{Image: "x", Environment: []EnvVar{{"A B", "D"}}}},
		{"env name with newline", ServiceConfig{Image: "x", Environment: []EnvVar{{"A\nB", "D"}}}},
		{"env name with control char", ServiceConfig{Image: "x", Environment: []EnvVar{{"A\x00B", "D"}}}},
		{"empty network name", ServiceConfig{Image: "x", Networks: []NetworkAttachment{{Name: ""}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New("svc", tc.cfg); !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("New err = %v, want ErrInvalidConfig", err)
			}
			if err := ValidateConfig(tc.cfg); !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("ValidateConfig err = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestNewKeepsTypeAmbiguousStrings(t *testing.T) {
	for _, v := range []string{"true", "8080", "0.5", "yes", "null", "~", "1e3", "*a", "a: b", "a #b"} {
		t.Run(v, func(t *testing.T) {
			cfg := ServiceConfig{
				Image:       v,
				Restart:     v,
				Environment: []EnvVar{{"K", v}},
				Volumes:     []string{v},
				Limits:      Limits{Memory: v},
			}
			src, err := New("svc", cfg)
			if err != nil {
				t.Fatal(err)
			}
			svc, err := mustParse(t, string(src)).Service("svc")
			if err != nil {
				t.Fatal(err)
			}
			if want := normalize(cfg); !reflect.DeepEqual(svc.Config, want) {
				t.Errorf("round trip:\n got %+v\nwant %+v\nfile:\n%s", svc.Config, want, src)
			}
			var doc map[string]any
			if err := yaml.Unmarshal(src, &doc); err != nil {
				t.Fatal(err)
			}
			env := doc["services"].(map[string]any)["svc"].(map[string]any)["environment"].(map[string]any)
			if got, ok := env["K"].(string); !ok || got != v {
				t.Errorf("env K = %#v, want string %q", env["K"], v)
			}
		})
	}
}
