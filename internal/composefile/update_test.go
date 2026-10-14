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

func TestUpdateRefusalsAreErrEditRefused(t *testing.T) {
	tests := []struct {
		name   string
		src    string
		change func(*ServiceConfig)
	}{
		{
			name:   "CRLF line breaks",
			src:    "services:\r\n  a:\r\n    image: x\r\n",
			change: func(c *ServiceConfig) { c.Image = "y" },
		},
		{
			name:   "anchored flow value",
			src:    "services:\n  a: {image: x, labels: &l {k: v}}\n",
			change: func(c *ServiceConfig) { c.Image = "y" },
		},
		{
			name:   "deleting the override of an inherited key",
			src:    "x-base: &base\n  image: base\nservices:\n  a:\n    <<: *base\n    image: own\n",
			change: func(c *ServiceConfig) { c.Image = "" },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := mustParse(t, tt.src)
			svc, err := f.Service("a")
			if err != nil {
				t.Fatal(err)
			}
			cfg := svc.Config
			tt.change(&cfg)
			updated, err := f.UpdateService("a", cfg)
			if err == nil {
				t.Fatalf("want a refusal, got success:\n%s", updated.Bytes())
			}
			if !errors.Is(err, ErrEditRefused) {
				t.Errorf("err = %v, want it to wrap ErrEditRefused", err)
			}
		})
	}
}

func TestApplyEditsWrapsDecodeFailuresInErrEditRefused(t *testing.T) {
	_, err := applyEdits([]byte("a: [\n"), []edit{{path("a"), scalar("b")}})
	if !errors.Is(err, ErrEditRefused) {
		t.Errorf("err = %v, want ErrEditRefused", err)
	}
	_, err = applyEdits([]byte("a: b\n"), []edit{{path("a.c"), scalar("d")}})
	if !errors.Is(err, ErrEditRefused) {
		t.Errorf("path through scalar err = %v, want ErrEditRefused", err)
	}
	err = verifyEdits([]byte("a: [\n"), []byte("a: b\n"), nil)
	if !errors.Is(err, ErrEditRefused) {
		t.Errorf("verifyEdits err = %v, want ErrEditRefused", err)
	}
}

func TestUpdateEditsServiceWithNonStringSiblingKeys(t *testing.T) {
	src := "services:\n  2048:\n    image: a\n  web:\n    image: nginx\n"
	got := update(t, src, "web", func(c *ServiceConfig) { c.Image = "caddy" })
	if want := "services:\n  2048:\n    image: a\n  web:\n    image: caddy\n"; got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

const dupEnvCompose = "services:\n  a:\n    image: x\n    environment: [A=1, A=2]\n"

func TestUpdateIgnoresPreexistingEnvironmentDuplicatesWhenUnchanged(t *testing.T) {
	got := update(t, dupEnvCompose, "a", func(c *ServiceConfig) { c.Image = "y" })
	if want := strings.Replace(dupEnvCompose, "image: x", "image: y", 1); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestUpdateValidatesChangedEnvironmentOnly(t *testing.T) {
	f := mustParse(t, dupEnvCompose)
	svc, _ := f.Service("a")
	cfg := svc.Config
	cfg.Environment = append(cfg.Environment, EnvVar{"B", "3"})
	if _, err := f.UpdateService("a", cfg); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want ErrInvalidConfig", err)
	}
}

func TestUpdateValidatesChangedNetworksOnly(t *testing.T) {
	f := mustParse(t, labCompose)
	svc, _ := f.Service("web")
	cfg := svc.Config
	cfg.Networks = []NetworkAttachment{{Name: "lan"}, {Name: "lan"}}
	if _, err := f.UpdateService("web", cfg); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want ErrInvalidConfig", err)
	}
}

func TestUpdateRejectsInvalidEnvironmentNames(t *testing.T) {
	for _, name := range []string{"B=C", "A B", "A\tB", "A\nB", "A\x00B"} {
		f := mustParse(t, labCompose)
		svc, _ := f.Service("db")
		cfg := svc.Config
		cfg.Environment = []EnvVar{{name, "D"}}
		if _, err := f.UpdateService("db", cfg); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("name %q: err = %v, want ErrInvalidConfig", name, err)
		}
	}
}

func TestUpdateMapEnvironmentAddKeepsExistingLinesByteIdentical(t *testing.T) {
	src := "services:\n  a:\n    image: x\n    environment:\n      # tuning\n      WORKERS: 4\n\n      TZ: Europe/Berlin   # zone\n      NAME: \"quoted\"\n    restart: always\n"
	got := update(t, src, "a", func(c *ServiceConfig) {
		c.Environment = append(c.Environment, EnvVar{"NEW", "1"})
	})
	want := strings.Replace(src, "    restart:", "      NEW: \"1\"\n    restart:", 1)
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestUpdateMapEnvironmentChangeOneValueChangesOnlyThatLine(t *testing.T) {
	src := "services:\n  a:\n    image: x\n    environment:\n      # tuning\n      WORKERS: 4\n\n      TZ: Europe/Berlin   # zone\n      NAME: \"quoted\"\n"
	got := update(t, src, "a", func(c *ServiceConfig) {
		for i := range c.Environment {
			if c.Environment[i].Name == "TZ" {
				c.Environment[i].Value = "UTC"
			}
		}
	})
	if want := strings.Replace(src, "Europe/Berlin", "UTC", 1); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestUpdateMapEnvironmentRemoveDeletesOnlyThatLine(t *testing.T) {
	src := "services:\n  a:\n    image: x\n    environment:\n      WORKERS: 4\n      TZ: UTC\n      NAME: n\n"
	got := update(t, src, "a", func(c *ServiceConfig) {
		c.Environment = []EnvVar{c.Environment[0], c.Environment[2]}
	})
	if want := "services:\n  a:\n    image: x\n    environment:\n      WORKERS: 4\n      NAME: n\n"; got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestUpdateMapEnvironmentReorderFallsBackToRewrite(t *testing.T) {
	src := "services:\n  a:\n    image: x\n    environment:\n      A: \"1\"\n      B: \"2\"\n"
	got := update(t, src, "a", func(c *ServiceConfig) {
		c.Environment = []EnvVar{c.Environment[1], c.Environment[0]}
	})
	if want := "services:\n  a:\n    image: x\n    environment:\n      B: \"2\"\n      A: \"1\"\n"; got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
