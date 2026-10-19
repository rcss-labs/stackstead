package composefile

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestAddServicePreservesCommentsAndOrder(t *testing.T) {
	f := mustParse(t, labCompose)
	got, err := f.AddService("cache", ServiceConfig{Image: "redis:7", Ports: []string{"6379:6379"}, Networks: []NetworkAttachment{{Name: "lan"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(labCompose, "\nnetworks:\n", "  cache:\n    image: redis:7\n    ports:\n      - \"6379:6379\"\n    networks:\n      - lan\n\nnetworks:\n", 1)
	if string(got.Bytes()) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got.Bytes(), want)
	}
	names, err := got.Services()
	if err != nil || !reflect.DeepEqual(names, []string{"web", "db", "cache"}) {
		t.Fatalf("names %v: %v", names, err)
	}
	if string(f.Bytes()) != labCompose {
		t.Fatal("original modified")
	}
}

func TestAddServiceDeclaresOnlyUndeclaredNetworks(t *testing.T) {
	src := "services:\n  web:\n    image: nginx:1.27\nnetworks:\n  lan: {external: true} # existing\n  internal: {}\n"
	got, err := mustParse(t, src).AddService("cache", ServiceConfig{Image: "redis:7", Networks: []NetworkAttachment{{Name: "lan"}, {Name: "internal"}, {Name: "backend"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := "networks:\n  lan: {external: true} # existing\n  internal: {}\n  backend:\n    external: true\n"
	if !strings.HasSuffix(string(got.Bytes()), want) {
		t.Fatalf("got:\n%s", got.Bytes())
	}
}

func TestAddServiceCreatesMissingAndNullBlocks(t *testing.T) {
	for _, src := range []string{"name: lab\n", "services:\nnetworks:\n", "services:\n  web: {}\n"} {
		t.Run(src, func(t *testing.T) {
			got, err := mustParse(t, src).AddService("cache", ServiceConfig{Image: "redis:7", Networks: []NetworkAttachment{{Name: "lan"}}})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got.Bytes()), "networks:\n  lan:\n    external: true") {
				t.Fatalf("got:\n%s", got.Bytes())
			}
		})
	}
}

func TestAddServiceRejectsExistingNameAndInvalidConfig(t *testing.T) {
	f := mustParse(t, labCompose)
	for _, tt := range []struct {
		name string
		cfg  ServiceConfig
		want error
	}{
		{"web", ServiceConfig{}, ErrServiceExists}, {"", ServiceConfig{}, ErrInvalidServiceName}, {"cache", ServiceConfig{Networks: []NetworkAttachment{{Name: ""}}}, ErrInvalidConfig},
	} {
		if _, err := f.AddService(tt.name, tt.cfg); !errors.Is(err, tt.want) {
			t.Errorf("%q: %v, want %v", tt.name, err, tt.want)
		}
	}
}

func TestAddServiceRefusesAnchoredServices(t *testing.T) {
	for _, src := range []string{
		"services: &services\n  web: {}\n",
		"x-services: &services\n  web: {}\nservices: *services\n",
	} {
		t.Run(src, func(t *testing.T) {
			_, err := mustParse(t, src).AddService("cache", ServiceConfig{Image: "redis:7"})
			if !errors.Is(err, ErrEditRefused) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestRemoveServiceKeepsNeighboursAndTopLevelBlocks(t *testing.T) {
	src := "# My home lab\nservices:\n  web:\n    image: nginx:1.27 # pinned\n  # db\n  db:\n    image: postgres:16\n  worker:\n    image: busybox:1.37\n\nvolumes:\n  data: {}\nnetworks:\n  lan: {}\n"
	for _, tt := range []struct{ name, remove string }{{"db", "  # db\n  db:\n    image: postgres:16\n"}, {"web", "  web:\n    image: nginx:1.27 # pinned\n"}} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mustParse(t, src).RemoveService(tt.name)
			if err != nil {
				t.Fatal(err)
			}
			if want := strings.Replace(src, tt.remove, "", 1); string(got.Bytes()) != want {
				t.Fatalf("got:\n%s\nwant:\n%s", got.Bytes(), want)
			}
		})
	}
}

func TestRemoveServiceKeepsCommentSeparatedByBlankLine(t *testing.T) {
	src := "services:\n  web: {}\n  # keep\n\n  db: {}\n"
	got, err := mustParse(t, src).RemoveService("db")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Bytes()) != strings.TrimSuffix(src, "  db: {}\n") {
		t.Fatalf("got:\n%s", got.Bytes())
	}
}

func TestRemoveServiceRefusesLastAndUnknown(t *testing.T) {
	f := mustParse(t, "services:\n  web: {}\n")
	for _, tt := range []struct {
		name string
		want error
	}{{"web", ErrLastService}, {"missing", ErrServiceNotFound}} {
		if _, err := f.RemoveService(tt.name); !errors.Is(err, tt.want) {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
	if string(f.Bytes()) != "services:\n  web: {}\n" {
		t.Fatal("original changed")
	}
}

func TestRemoveServiceRefusesDependedOn(t *testing.T) {
	for _, dep := range []string{"[db]", "{db: {condition: service_healthy}}"} {
		t.Run(dep, func(t *testing.T) {
			src := "services:\n  db: {}\n  web:\n    depends_on: " + dep + "\n  worker:\n    depends_on: " + dep + "\n"
			f := mustParse(t, src)
			_, err := f.RemoveService("db")
			var dependent *DependentsError
			if !errors.As(err, &dependent) {
				t.Fatalf("got %v", err)
			}
			if dependent.Service != "db" || !reflect.DeepEqual(dependent.Dependents, []string{"web", "worker"}) || dependent.Error() != "service db is required by web, worker (depends_on)" {
				t.Fatalf("got %+v", dependent)
			}
		})
	}
}

func TestDependentsOf(t *testing.T) {
	f := mustParse(t, "services:\n  db:\n    depends_on: [db]\n  web:\n    depends_on: [db, cache]\n  worker:\n    depends_on: {db: {condition: service_started}}\n  other:\n    environment: {db: yes}\n")
	if got := f.DependentsOf("db"); !reflect.DeepEqual(got, []string{"web", "worker"}) {
		t.Fatalf("got %v", got)
	}
	if got := f.DependentsOf("absent"); got != nil {
		t.Fatalf("got %v", got)
	}
}

func TestRemoveServiceRecognizesAliasedDependencies(t *testing.T) {
	for _, deps := range []string{"[db]", "{db: {condition: service_started}}"} {
		t.Run(deps, func(t *testing.T) {
			src := "x-deps: &deps " + deps + "\nservices:\n  db: {}\n  web:\n    depends_on: *deps\n  worker:\n    depends_on: &direct " + deps + "\n"
			f := mustParse(t, src)
			_, err := f.RemoveService("db")
			var dependent *DependentsError
			if !errors.As(err, &dependent) || !reflect.DeepEqual(dependent.Dependents, []string{"web", "worker"}) {
				t.Fatalf("got %v", err)
			}
			if got := f.DependentsOf("db"); !reflect.DeepEqual(got, []string{"web", "worker"}) {
				t.Fatalf("got %v", got)
			}
		})
	}
}

func TestRemoveServiceRefusesUnanalyzableDependencies(t *testing.T) {
	for _, src := range []string{
		"services:\n  db: {}\n  web:\n    depends_on: db\n",
		"x-base: &base\n  depends_on: [db]\nservices:\n  db: {}\n  web:\n    <<: *base\n",
		"x-services: &base\n  web:\n    depends_on: [db]\nservices:\n  <<: *base\n  db: {}\n",
		"services:\n  db: {}\n  web:\n    depends_on: &cycle [*cycle]\n",
	} {
		t.Run(src, func(t *testing.T) {
			f := mustParse(t, src)
			_, err := f.RemoveService("db")
			if !errors.Is(err, ErrEditRefused) {
				t.Fatalf("got %v", err)
			}
			if string(f.Bytes()) != src {
				t.Fatal("original changed")
			}
		})
	}
}

func TestRemoveServiceAllowsUnrelatedAnchors(t *testing.T) {
	src := "x-labels: &labels {owner: lab}\nservices:\n  db: {}\n  web:\n    labels: *labels\n"
	got, err := mustParse(t, src).RemoveService("db")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Bytes()) != strings.Replace(src, "  db: {}\n", "", 1) {
		t.Fatalf("got:\n%s", got.Bytes())
	}
}

func TestDependentsOfScalarNames(t *testing.T) {
	f := mustParse(t, "services:\n  2048: {}\n  web:\n    depends_on: [2048]\n  worker:\n    depends_on: {2048: {condition: service_started}}\n")
	if got := f.DependentsOf("2048"); !reflect.DeepEqual(got, []string{"web", "worker"}) {
		t.Fatalf("got %v", got)
	}
}

func TestRemoveServicePrefersKnownDependenciesToUnsupportedListShapes(t *testing.T) {
	f := mustParse(t, "services:\n  db: {}\n  web:\n    depends_on: [{unsupported: shape}, db]\n")
	_, err := f.RemoveService("db")
	var dependent *DependentsError
	if !errors.As(err, &dependent) || !reflect.DeepEqual(dependent.Dependents, []string{"web"}) {
		t.Fatalf("got %v", err)
	}
}

func TestAddServiceNeverDeclaresTheImplicitDefaultNetwork(t *testing.T) {
	got, err := mustParse(t, "services:\n  web:\n    image: nginx:1.27\n").AddService("cache", ServiceConfig{Image: "redis:7", Networks: []NetworkAttachment{{Name: "default"}, {Name: "backend"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(got.Bytes()), "networks:\n  backend:\n    external: true\n") || strings.Contains(string(got.Bytes()), "default:\n    external") {
		t.Fatalf("got:\n%s", got.Bytes())
	}
}
