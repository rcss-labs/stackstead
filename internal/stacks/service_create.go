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
	Name, Service string // Service defaults to Name
	Config        composefile.ServiceConfig
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

	service := req.Service
	if service == "" {
		service = req.Name
	}
	cfg, err := s.prepareConfig(req.Name, dir, req.Config)
	if err != nil {
		return Summary{}, err
	}
	data, err := composefile.New(service, cfg)
	if err != nil {
		return Summary{}, err
	}

	partial, err := s.checkChanges(ctx, changeSet{
		services: []namedConfig{{Name: service, Config: cfg}},
	})
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
	return Summary{Name: req.Name, Path: path, Project: project, Origin: OriginManaged,
		Status: StatusStopped, Services: 1, ChecksPartial: partial}, nil
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
func (s *Service) prepareConfig(name, stackDir string, cfg composefile.ServiceConfig) (composefile.ServiceConfig, error) {
	if cfg.Build == nil {
		return cfg, nil
	}
	b := *cfg.Build
	cfg.Build = &b
	if cfg.Image == "" {
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
