package stacks

import (
	"context"
	"fmt"
	"slices"

	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/imageref"
)

// LibraryFetcher makes a library image present in the local engine, pulling it
// from the owning server or registry when needed.
type LibraryFetcher interface {
	EnsureLocal(ctx context.Context, ref imageref.Ref, out func(string)) error
}

// serviceLibraryRefs lists the services of f whose image is a library
// reference, in file order. A service whose image merely sits in the library
// domain but does not parse is left to compose.
func serviceLibraryRefs(f *composefile.File) ([]imageref.Usage, []string, error) {
	names, err := f.Services()
	if err != nil {
		return nil, nil, err
	}
	var usages []imageref.Usage
	var plain []string
	for _, name := range names {
		svc, err := f.Service(name)
		if err != nil {
			return nil, nil, err
		}
		if ref, ok := imageref.Parse(svc.Config.Image); ok {
			usages = append(usages, imageref.Usage{Service: name, Ref: ref})
		} else {
			plain = append(plain, name)
		}
	}
	return usages, plain, nil
}

// uniqueRefs returns the distinct references of usages in first-seen order.
func uniqueRefs(usages []imageref.Usage) []imageref.Ref {
	var out []imageref.Ref
	for _, u := range usages {
		if !slices.Contains(out, u.Ref) {
			out = append(out, u.Ref)
		}
	}
	return out
}

// ensureLibrary makes every ref present locally, reporting progress to out.
func (s *Service) ensureLibrary(ctx context.Context, refs []imageref.Ref, out func(string)) error {
	for _, ref := range refs {
		out("ensuring " + ref.String())
		if err := s.library.EnsureLocal(ctx, ref, out); err != nil {
			return err
		}
	}
	return nil
}

// LibraryUsage lists every service, across all stacks, that runs a library
// image. Stacks whose file cannot be read are skipped.
func (s *Service) LibraryUsage(ctx context.Context) ([]imageref.Usage, error) {
	entries, err := s.reg.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []imageref.Usage
	for _, e := range entries {
		_, _, f, err := readFile(e)
		if err != nil {
			continue
		}
		usages, _, err := serviceLibraryRefs(f)
		if err != nil {
			continue
		}
		for _, u := range usages {
			u.Stack = e.Name
			out = append(out, u)
		}
	}
	return out, nil
}

// libraryRefStrings returns the sorted, unique library references of a
// described stack's services.
func libraryRefStrings(services []ServiceDetail) []string {
	var out []string
	for _, sd := range services {
		if ref, ok := imageref.Parse(sd.Config.Image); ok {
			out = append(out, ref.String())
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// actPlan decides what an action runs: the library references to ensure first
// and the compose arguments. With no fetcher the stack acts as before.
func (s *Service) actPlan(f *composefile.File, action string, args []string) ([]imageref.Ref, []string, error) {
	if s.library == nil || (action != "up" && action != "pull") {
		return nil, args, nil
	}
	usages, plain, err := serviceLibraryRefs(f)
	if err != nil {
		return nil, nil, fmt.Errorf("read services: %w", err)
	}
	for _, u := range usages {
		if err := refuseAlwaysPull(f, u.Service); err != nil {
			return nil, nil, err
		}
	}
	refs := uniqueRefs(usages)
	if action == "pull" && len(usages) > 0 {
		if len(plain) == 0 {
			return refs, nil, nil
		}
		args = append([]string{"pull", "--"}, plain...)
	}
	return refs, args, nil
}

// refuseAlwaysPull rejects pull_policy: always on a library service: compose
// would pull stackstead.invalid/... from a registry that does not exist.
func refuseAlwaysPull(f *composefile.File, service string) error {
	policy, ok, err := f.ServiceScalar(service, "pull_policy")
	if err != nil || !ok || policy != "always" {
		return err
	}
	return &ValidationError{Fields: []FieldError{{Field: "services." + service + ".pull_policy",
		Message: fmt.Sprintf("service %s uses a library image, which cannot be pulled from a registry: remove pull_policy: always (missing or never are fine)", service)}}}
}
