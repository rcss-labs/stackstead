package stacks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/fsguard"
)

const composeFileName = "compose.yaml"

type CreateRequest struct {
	Name          string
	Services      []composefile.NamedService
	IndexedFields bool
}

// Create writes a new stack <StacksDir>/<name>/compose.yaml, validates it with
// compose and registers it. On any failure it removes exactly the file and
// folder this call created; a pre-existing folder is never touched.
func (s *Service) Create(ctx context.Context, req CreateRequest) (Summary, error) {
	if err := ValidateName(req.Name); err != nil {
		return Summary{}, err
	}
	if _, err := s.reg.Get(ctx, req.Name); err == nil {
		return Summary{}, ErrStackExists
	} else if !errors.Is(err, ErrStackNotFound) {
		return Summary{}, err
	}
	dir := filepath.Join(s.stacksDir, req.Name) // lexical until the folder is created
	if _, err := os.Lstat(dir); err == nil {
		return Summary{}, ErrStackExists
	}

	if len(req.Services) < 1 || len(req.Services) > 20 {
		return Summary{}, &ValidationError{Fields: []FieldError{{Field: "services", Message: "a stack needs between 1 and 20 services"}}}
	}
	var services []composefile.NamedService
	var configs []namedConfig
	var indices []int
	fields := make([][]FieldError, len(req.Services))
	var configErr error
	seen := map[string]bool{}
	for i, entry := range req.Services {
		prefix := ""
		if req.IndexedFields {
			prefix = fmt.Sprintf("services[%d].", i)
		}
		collect := func(err error) bool {
			var ve *ValidationError
			if errors.As(prefixFields(err, prefix), &ve) {
				fields[i] = append(fields[i], ve.Fields...)
				return true
			}
			return false
		}
		if i == 0 && entry.Name == "" {
			entry.Name = req.Name
		}
		if entry.Name == "" {
			collect(&ValidationError{Fields: []FieldError{{Field: "service", Message: "service name is required"}}})
			continue
		}
		if err := ValidateServiceName(entry.Name); err != nil {
			collect(err)
			continue
		}
		if seen[entry.Name] {
			collect(&ValidationError{Fields: []FieldError{{Field: "service", Message: "duplicate service name " + entry.Name}}})
			continue
		}
		seen[entry.Name] = true
		cfg, err := s.prepareConfig(req.Name, entry.Name, dir, entry.Config)
		if err != nil {
			if collect(err) {
				continue
			}
			return Summary{}, err
		}
		if err := composefile.ValidateConfig(cfg); err != nil {
			if configErr == nil {
				configErr = err
				if req.IndexedFields {
					configErr = fmt.Errorf("services[%d]: %w", i, err)
				}
			}
			continue
		}
		services = append(services, composefile.NamedService{Name: entry.Name, Config: cfg})
		configs = append(configs, namedConfig{Name: entry.Name, Config: cfg})
		indices = append(indices, i)
	}
	partial, err := s.checkChanges(ctx, changeSet{services: configs, indexed: req.IndexedFields, indices: indices})
	if err != nil {
		var ve *ValidationError
		if !errors.As(err, &ve) {
			return Summary{}, err
		}
		for _, field := range ve.Fields {
			index := 0
			if req.IndexedFields {
				fmt.Sscanf(field.Field, "services[%d].", &index)
			}
			fields[index] = append(fields[index], field)
		}
	}
	var allFields []FieldError
	for _, entryFields := range fields {
		allFields = append(allFields, entryFields...)
	}
	if len(allFields) > 0 {
		return Summary{}, &ValidationError{Fields: allFields}
	}
	if configErr != nil {
		return Summary{}, configErr
	}
	data, err := composefile.New(services)
	if err != nil {
		return Summary{}, err
	}

	if err := s.requireCompose(ctx); err != nil {
		return Summary{}, err
	}
	if err := os.MkdirAll(s.stacksDir, 0o755); err != nil {
		return Summary{}, fmt.Errorf("create stacks directory: %w", err)
	}
	base, err := filepath.EvalSymlinks(s.stacksDir)
	if err != nil {
		return Summary{}, err
	}
	dir = filepath.Join(base, req.Name)
	path := filepath.Join(dir, composeFileName)

	// Mkdir (not MkdirAll) is the atomic "I created it" claim: it fails if the
	// folder appeared since the check above.
	if err := os.Mkdir(dir, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Summary{}, ErrStackExists
		}
		return Summary{}, err
	}
	wroteFile, ok := false, false
	defer func() {
		if ok {
			return
		}
		if wroteFile {
			os.Remove(path)
		}
		os.Remove(dir) // fails harmlessly if somebody put something else in it
	}()
	if err := composefile.Save(path, data, ""); err != nil {
		return Summary{}, err
	}
	wroteFile = true
	if err := s.compose.Config(ctx, path); err != nil {
		return Summary{}, err
	}
	project, err := s.compose.ProjectName(ctx, path)
	if err != nil {
		return Summary{}, err
	}
	s.projectMu.Lock() // see Service for lock order
	defer s.projectMu.Unlock()
	p, err := s.projectAvailable(ctx, project, req.Name)
	if err != nil {
		return Summary{}, err
	}
	partial = partial || p
	err = s.reg.Add(ctx, Entry{Name: req.Name, ComposePath: path, Origin: OriginManaged, Project: project})
	if errors.Is(err, ErrStackExists) {
		// Sync may have adopted our brand-new folder in the meantime; that
		// row is exactly what we were about to write.
		if e, gerr := s.reg.Get(ctx, req.Name); gerr == nil && e.ComposePath == path {
			err = nil
		}
	}
	if err != nil {
		return Summary{}, s.mapProjectTaken(ctx, err, project, req.Name)
	}
	ok = true
	containers, engErr := s.engine.Containers(ctx)
	sum := s.describe(Entry{Name: req.Name, ComposePath: path, Origin: OriginManaged, Project: project}, containers, engErr).Summary
	sum.ChecksPartial = partial
	return sum, nil
}

// projectAvailable refuses a project that a registered stack already uses, or
// that exists on the host without a registered owner (importing is the way to
// adopt it): "up --remove-orphans" in a shared project would delete the other
// stack's containers. partial reports that the host check was skipped because
// the engine is unreachable. Callers hold projectMu.
func (s *Service) projectAvailable(ctx context.Context, project, stack string) (partial bool, err error) {
	owner, taken, err := s.projectOwner(ctx, project, stack)
	if err != nil {
		return false, err
	}
	if taken {
		return false, projectTaken(project, owner)
	}
	containers, err := s.engine.Containers(ctx)
	if err != nil {
		if errors.Is(err, engine.ErrUnavailable) && ctx.Err() == nil {
			return true, nil
		}
		return false, err
	}
	for _, c := range containers {
		if c.Project() == project {
			return false, &ValidationError{Fields: []FieldError{{Field: "name",
				Message: fmt.Sprintf("compose project %s already exists on this host; import it instead", project)}}}
		}
	}
	return false, nil
}

// prepareConfig applies build defaults and confines the build context.
func (s *Service) prepareConfig(stack, service, stackDir string, cfg composefile.ServiceConfig) (composefile.ServiceConfig, error) {
	if cfg.Build == nil {
		return cfg, nil
	}
	b := *cfg.Build
	cfg.Build = &b
	if cfg.Image == "" {
		name := stack
		if service != stack {
			name += "-" + service
		}
		cfg.Image = "stackstead/" + name + ":latest"
	}
	if b.Context == "" {
		b.Context = "."
	}
	cfg.Build.Context = b.Context

	target := b.Context
	if !filepath.IsAbs(target) {
		target = filepath.Join(stackDir, target)
		// Contexts inside the new stack folder need no further check.
		if rel, err := filepath.Rel(stackDir, target); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return cfg, nil
		}
	}
	resolved, err := fsguard.Resolve(s.roots, target)
	if err != nil {
		msg := "build context must be inside an allowed directory"
		if !errors.Is(err, fsguard.ErrOutsideRoots) {
			msg = "build context: " + err.Error()
		}
		return cfg, &ValidationError{Fields: []FieldError{{Field: "build.context", Message: msg}}}
	}
	if filepath.IsAbs(b.Context) {
		cfg.Build.Context = resolved
	}
	return cfg, nil
}

// Import registers an existing compose file without ever writing to it. The
// stack name is the project name compose derives, so it behaves exactly like
// running docker compose in that directory.
func (s *Service) Import(ctx context.Context, path string) (Summary, error) {
	invalid := func(msg string) error {
		return &ValidationError{Fields: []FieldError{{Field: "path", Message: msg}}}
	}
	if !filepath.IsAbs(path) {
		return Summary{}, invalid("path must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Summary{}, invalid("file does not exist")
		}
		return Summary{}, invalid("cannot read file: " + err.Error())
	}
	if info, err := os.Stat(resolved); err != nil || !info.Mode().IsRegular() {
		return Summary{}, invalid("path is not a regular file")
	}
	if o := overrideFiles(resolved); len(o) > 0 {
		return Summary{}, invalid(fmt.Sprintf("%s has an override file %s next to it; Stackstead does not support override files yet", filepath.Base(resolved), o[0]))
	}
	data, _, err := composefile.Load(resolved)
	if err != nil {
		return Summary{}, invalid("cannot read file: " + err.Error())
	}
	if _, err := parseFile(data); err != nil {
		return Summary{}, err
	}
	if err := s.requireCompose(ctx); err != nil {
		return Summary{}, err
	}
	if err := s.compose.Config(ctx, resolved); err != nil {
		return Summary{}, err
	}
	project, err := s.compose.ProjectName(ctx, resolved)
	if err != nil {
		return Summary{}, err
	}
	if err := ValidateName(project); err != nil {
		return Summary{}, err
	}
	entry := Entry{Name: project, ComposePath: resolved, Origin: OriginExternal, Project: project}
	// Importing may adopt a project that runs on the host unregistered; only
	// a registered owner blocks it.
	s.projectMu.Lock() // see Service for lock order
	owner, taken, err := s.projectOwner(ctx, project, "")
	if err == nil && taken {
		err = projectTaken(project, owner)
		if existing, gerr := s.reg.Get(ctx, project); gerr == nil && existing.ComposePath == resolved {
			err = ErrStackExists // the very same file, imported twice
		}
	}
	if err == nil {
		err = s.reg.Add(ctx, entry)
	}
	s.projectMu.Unlock()
	if err != nil {
		return Summary{}, s.mapProjectTaken(ctx, err, project, "")
	}
	containers, engErr := s.engine.Containers(ctx)
	return s.describe(entry, containers, engErr).Summary, nil
}
