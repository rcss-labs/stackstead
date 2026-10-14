package composefile

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func update(t *testing.T, src, name string, change func(*ServiceConfig)) string {
	t.Helper()
	f := mustParse(t, src)
	svc, err := f.Service(name)
	if err != nil {
		t.Fatal(err)
	}
	cfg := svc.Config
	change(&cfg)
	updated, err := f.UpdateService(name, cfg)
	if err != nil {
		t.Fatalf("UpdateService: %v", err)
	}
	got, err := updated.Service(name)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Config, normalize(cfg)) {
		t.Errorf("re-read config =\n%+v\nwant\n%+v", got.Config, normalize(cfg))
	}
	return string(updated.Bytes())
}

func TestUpdateChangesOnlyTheEditedLine(t *testing.T) {
	got := update(t, labCompose, "web", func(c *ServiceConfig) { c.Limits.Memory = "512M" })
	want := strings.Replace(labCompose, "          memory: 256M\n", "          memory: 512M\n", 1)
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestUpdateWithoutChangesKeepsBytes(t *testing.T) {
	f := mustParse(t, labCompose)
	svc, _ := f.Service("web")
	cfg := svc.Config
	cfg.Ports = append([]string{}, cfg.Ports...) // equal content, different slice
	updated, err := f.UpdateService("web", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if string(updated.Bytes()) != labCompose {
		t.Errorf("unchanged update rewrote the file:\n%s", updated.Bytes())
	}
}

func TestUpdateClearingAllLimitsPrunesDeploy(t *testing.T) {
	src := "services:\n  a:\n    image: x\n    deploy:\n      resources:\n        limits:\n          cpus: \"1\"\n          memory: 1G\n    restart: always\n"
	got := update(t, src, "a", func(c *ServiceConfig) { c.Limits = Limits{} })
	if want := "services:\n  a:\n    image: x\n    restart: always\n"; got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestUpdateClearingLimitsKeepsOtherDeploySettings(t *testing.T) {
	got := update(t, labCompose, "web", func(c *ServiceConfig) { c.Limits = Limits{} })
	want := strings.Replace(labCompose, "      resources:\n        limits:\n          cpus: \"0.50\"\n          memory: 256M\n", "", 1)
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestUpdateKeepsEnvironmentListStyle(t *testing.T) {
	got := update(t, labCompose, "db", func(c *ServiceConfig) {
		c.Environment = append(c.Environment, EnvVar{"PGDATA", "/data"})
	})
	want := strings.Replace(labCompose, "      - POSTGRES_PASSWORD=secret\n", "      - POSTGRES_PASSWORD=secret\n      - PGDATA=/data\n", 1)
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestUpdateStaticIPSwitchesNetworksToMapping(t *testing.T) {
	src := "services:\n  a:\n    image: x\n    networks:\n      - lan\n"
	got := update(t, src, "a", func(c *ServiceConfig) {
		c.Networks = []NetworkAttachment{{Name: "lan", IPv4Address: "10.0.0.5"}}
	})
	want := "services:\n  a:\n    image: x\n    networks:\n      lan:\n        ipv4_address: 10.0.0.5\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestUpdateAddsAndRemovesFields(t *testing.T) {
	src := "services:\n  a:\n    image: x # app\n    restart: always\n"
	got := update(t, src, "a", func(c *ServiceConfig) {
		c.Restart = ""
		c.Ports = []string{"80:80"}
	})
	want := "services:\n  a:\n    image: x # app\n    ports:\n      - \"80:80\"\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestUpdateRejectsReadOnlyField(t *testing.T) {
	src := "services:\n  a:\n    image: x\n    ports:\n      - target: 80\n        published: 8080\n"
	f := mustParse(t, src)
	_, err := f.UpdateService("a", ServiceConfig{Image: "x", Ports: []string{"80:80"}})
	var ro *ReadOnlyFieldError
	if !errors.As(err, &ro) || ro.Field != FieldPorts {
		t.Errorf("err = %v, want ReadOnlyFieldError{ports}", err)
	}
}

func TestUpdateAllowsOtherFieldsBesideReadOnlyOnes(t *testing.T) {
	src := "services:\n  a:\n    image: x\n    ports:\n      - target: 80\n        published: 8080\n"
	got := update(t, src, "a", func(c *ServiceConfig) { c.Image = "y" })
	want := "services:\n  a:\n    image: y\n    ports:\n      - target: 80\n        published: 8080\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestUpdateUnknownService(t *testing.T) {
	if _, err := mustParse(t, labCompose).UpdateService("nope", ServiceConfig{}); !errors.Is(err, ErrServiceNotFound) {
		t.Errorf("err = %v, want ErrServiceNotFound", err)
	}
}

func TestUpdateRejectsInvalidConfig(t *testing.T) {
	f := mustParse(t, labCompose)
	svc, err := f.Service("web")
	if err != nil {
		t.Fatal(err)
	}
	cfg := svc.Config
	cfg.Environment = []EnvVar{{"A", "1"}, {"A", "2"}}
	updated, err := f.UpdateService("web", cfg)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want ErrInvalidConfig", err)
	}
	if updated != nil {
		t.Errorf("updated = %v, want nil", updated)
	}
	if string(f.Bytes()) != labCompose {
		t.Errorf("file changed:\n%s", f.Bytes())
	}
}

func TestUpdateAnchoredServiceIsRefused(t *testing.T) {
	src := "services:\n  web: &web\n    image: nginx\n  web2:\n    <<: *web\n"
	f := mustParse(t, src)
	_, err := f.UpdateService("web", ServiceConfig{Image: "other"})
	var ro *ReadOnlyFieldError
	if !errors.As(err, &ro) || ro.Field != FieldImage {
		t.Errorf("err = %v, want ReadOnlyFieldError{image}", err)
	}
}
