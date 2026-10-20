package composefile

import (
	"errors"
	"reflect"
	"testing"
)

func TestBuildOf(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		want    BuildRef
		wantOK  bool
		wantErr error
	}{
		{"scalar", "services:\n  web:\n    build: ./web\n", BuildRef{Context: "./web"}, true, nil},
		{"mapping", "services:\n  web:\n    build: {context: ., dockerfile: Dockerfile.dev, args: {A: 1}}\n",
			BuildRef{Context: ".", Dockerfile: "Dockerfile.dev"}, true, nil},
		{"inline", "services:\n  web:\n    build: {dockerfile_inline: \"FROM x\"}\n", BuildRef{Inline: true}, true, nil},
		{"merge key", "x-b: &b\n  build: ./m\nservices:\n  web:\n    <<: *b\n", BuildRef{Context: "./m"}, true, nil},
		{"no build", "services:\n  web:\n    image: x\n", BuildRef{}, false, nil},
		{"non-scalar context", "services:\n  web:\n    build: {context: [a]}\n", BuildRef{}, false, ErrNotMapping},
		{"non-scalar dockerfile", "services:\n  web:\n    build: {dockerfile: {a: b}}\n", BuildRef{}, false, ErrNotMapping},
		{"unknown service", "services:\n  web:\n    image: x\n", BuildRef{}, false, ErrServiceNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name := "web"
			if tt.wantErr == ErrServiceNotFound {
				name = "nope"
			}
			got, ok, err := mustParse(t, tt.src).BuildOf(name)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("got %+v ok=%v, want %+v ok=%v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestEffectiveServiceLongPorts(t *testing.T) {
	src := `services:
  web:
    ports:
      - "8080:80"
      - {target: 80, published: 9090, host_ip: 127.0.0.1, protocol: udp}
      - {target: 81}
      - {target: 82, published: "7000-7001", host_ip: "::1"}
`
	f := mustParse(t, src)
	svc, err := f.Service("web")
	if err != nil {
		t.Fatal(err)
	}
	if len(svc.ReadOnly) != 1 || svc.ReadOnly[0] != FieldPorts || svc.Config.Ports != nil {
		t.Fatalf("Service ports not read-only/nil: %+v", svc)
	}
	cfg, err := f.EffectiveService("web")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"8080:80", "127.0.0.1:9090:80/udp", "81", "[::1]:7000-7001:82"}
	if !reflect.DeepEqual(cfg.Ports, want) {
		t.Errorf("Ports = %q, want %q", cfg.Ports, want)
	}
}

func TestEffectiveServiceNetworksWithAliases(t *testing.T) {
	src := "services:\n  web:\n    networks:\n      lan: {ipv4_address: 192.168.1.5, aliases: [a]}\n      b: null\n"
	cfg, err := mustParse(t, src).EffectiveService("web")
	if err != nil {
		t.Fatal(err)
	}
	want := []NetworkAttachment{{"lan", "192.168.1.5"}, {"b", ""}}
	if !reflect.DeepEqual(cfg.Networks, want) {
		t.Errorf("Networks = %+v, want %+v", cfg.Networks, want)
	}
}

func TestEffectiveServiceLongBuild(t *testing.T) {
	src := "services:\n  web:\n    build: {context: /srv/app, args: {X: 1}}\n"
	cfg, err := mustParse(t, src).EffectiveService("web")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Build, &Build{Context: "/srv/app"}) {
		t.Errorf("Build = %+v", cfg.Build)
	}
}

func TestEffectiveServiceMatchesServiceForPlainFiles(t *testing.T) {
	f := mustParse(t, labCompose)
	for _, name := range []string{"web", "db"} {
		svc, err := f.Service(name)
		if err != nil {
			t.Fatal(err)
		}
		got, err := f.EffectiveService(name)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, svc.Config) {
			t.Errorf("%s: effective %+v != service %+v", name, got, svc.Config)
		}
	}
}

func TestUsesExtendsAndInclude(t *testing.T) {
	f := mustParse(t, "include: [x.yaml]\nservices:\n  web:\n    extends: {service: base}\n  db:\n    image: x\n")
	if !f.UsesExtends("web") || f.UsesExtends("db") || f.UsesExtends("nope") {
		t.Error("UsesExtends wrong")
	}
	if !f.UsesInclude() {
		t.Error("UsesInclude = false")
	}
	if mustParse(t, "services:\n  web:\n    image: x\n").UsesInclude() {
		t.Error("UsesInclude = true without include")
	}
}
