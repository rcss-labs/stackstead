package composefile

import (
	"errors"
	"reflect"
	"testing"
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
