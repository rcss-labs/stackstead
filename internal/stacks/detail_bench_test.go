package stacks_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/store"
)

// benchDetailService builds a Service over a registry holding one stack with
// the given number of services and a container listing with perService
// containers for each plus foreignContainers belonging to other projects, the
// shape of a busy host. Everything is synthetic.
func benchDetailService(b *testing.B, services, perService, foreignContainers int) *stacks.Service {
	b.Helper()
	dir := b.TempDir()
	db, err := store.Open(filepath.Join(dir, "stackstead.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	reg := stacks.NewRegistry(db, time.Now)

	var compose strings.Builder
	compose.WriteString("services:\n")
	for i := 0; i < services; i++ {
		fmt.Fprintf(&compose, "  svc%d:\n    image: nginx:1.%d\n    ports:\n      - \"%d:80\"\n    environment:\n      A: \"1\"\n      B: \"2\"\n    volumes:\n      - /data/%d:/data\n    restart: unless-stopped\n", i, i, 10000+i, i)
	}
	stackDir := filepath.Join(dir, "stacks", "lab")
	if err := os.MkdirAll(stackDir, 0o755); err != nil {
		b.Fatal(err)
	}
	file := filepath.Join(stackDir, "compose.yaml")
	if err := os.WriteFile(file, []byte(compose.String()), 0o644); err != nil {
		b.Fatal(err)
	}
	if err := reg.Add(context.Background(), stacks.Entry{Name: "lab", ComposePath: file, Origin: stacks.OriginManaged, Project: "lab"}); err != nil {
		b.Fatal(err)
	}

	var containers []engine.Container
	label := func(project, service string) map[string]string {
		return map[string]string{"com.docker.compose.project": project, "com.docker.compose.service": service}
	}
	for i := 0; i < services; i++ {
		for j := 0; j < perService; j++ {
			containers = append(containers, engine.Container{ID: fmt.Sprintf("c%d-%d", i, j), Name: fmt.Sprintf("lab-svc%d-%d", i, j),
				State: "running", Status: "Up 2 hours (healthy)", Labels: label("lab", fmt.Sprintf("svc%d", i))})
		}
	}
	for i := 0; i < foreignContainers; i++ {
		containers = append(containers, engine.Container{ID: fmt.Sprintf("f%d", i), Name: fmt.Sprintf("other-%d", i),
			State: "running", Status: "Up 1 hour", Labels: label(fmt.Sprintf("other%d", i%20), "web")})
	}
	return stacks.NewService(stacks.Deps{
		Registry: reg, Ops: stacks.NewOps(time.Minute, nil), Compose: &fakeCompose{},
		Engine: &fakeEngine{containers: containers}, StacksDir: filepath.Join(dir, "stacks"), AllowedRoots: []string{dir},
	})
}

// BenchmarkStackDetail measures Service.Get, the computation behind the stack
// detail page, for stacks of growing size on a host with 200 other containers.
func BenchmarkStackDetail(b *testing.B) {
	for _, services := range []int{10, 50, 200} {
		b.Run(fmt.Sprintf("services=%d", services), func(b *testing.B) {
			svc := benchDetailService(b, services, 2, 200)
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				d, err := svc.Get(ctx, "lab")
				if err != nil || len(d.Services) != services {
					b.Fatalf("Get: %v, %d services", err, len(d.Services))
				}
			}
		})
	}
}
