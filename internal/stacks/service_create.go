package stacks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
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
	// Extra is merged into the top level of the rendered file. Dockerfiles
	// holds the content to write for the Services entries (by index) that
	// build from the stack folder.
	Extra       composefile.Extra
	Dockerfiles map[int][]byte
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
	var dockerfiles []createDockerfile
	seen := map[string]bool{}
	for index := range req.Dockerfiles {
		if index < 0 || index >= len(req.Services) {
			return Summary{}, &ValidationError{Fields: []FieldError{{Field: "services", Message: "a Dockerfile was given for a service that is not in the request"}}}
		}
	}
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
		if content, ok := req.Dockerfiles[i]; ok {
			if err := ValidateDockerfile("dockerfile", content); err != nil {
				collect(err)
			}
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
		if !entry.Extra.IsZero() {
			// Merge this service's extra alone so a clash is blamed on it.
			if _, err := composefile.NewStack([]composefile.NamedService{{Name: entry.Name, Config: cfg, Extra: entry.Extra}}, composefile.Extra{}); err != nil {
				if !errors.Is(err, composefile.ErrInvalidExtra) {
					return Summary{}, err
				}
				collect(&ValidationError{Fields: []FieldError{{Field: "extra", Message: err.Error()}}})
				continue
			}
		}
		services = append(services, composefile.NamedService{Name: entry.Name, Config: cfg, Extra: entry.Extra})
		configs = append(configs, namedConfig{Name: entry.Name, Config: cfg})
		indices = append(indices, i)
		if content, ok := req.Dockerfiles[i]; ok {
			dockerfiles = append(dockerfiles, createDockerfile{index: i, service: entry.Name, content: content})
		}
	}

	// Render the whole document now so every check below sees the merged
	// result, including what the extras added.
	var data []byte
	var parsed *composefile.File
	var topFields []FieldError
	if err := CheckTopExtra(req.Extra); err != nil {
		var ve *ValidationError
		errors.As(err, &ve)
		topFields = ve.Fields
	} else if len(services) > 0 {
		rendered, err := composefile.NewStack(services, req.Extra)
		switch {
		case errors.Is(err, composefile.ErrInvalidExtra):
			topFields = []FieldError{{Field: "extra", Message: err.Error()}}
		case err != nil:
			return Summary{}, err
		default:
			if parsed, err = composefile.Parse(rendered); err != nil {
				return Summary{}, err
			}
			data = rendered
		}
	}
	var localNetworks map[string]bool
	needsImage := false
	if parsed != nil {
		localNetworks = parsed.LocalNetworks()
		for position := range configs {
			if services[position].Extra.IsZero() {
				continue
			}
			index := indices[position]
			prefix := ""
			if req.IndexedFields {
				prefix = fmt.Sprintf("services[%d].", index)
			}
			effective, buildFields, err := s.effectiveWithExtra(parsed, dir, configs[position])
			if err != nil {
				fields[index] = append(fields[index], FieldError{Field: prefix + "extra", Message: err.Error()})
				continue
			}
			for _, field := range buildFields {
				fields[index] = append(fields[index], FieldError{Field: prefix + field.Field, Message: field.Message})
			}
			configs[position].Config = effective
			if effective.Build != nil && effective.Image == "" {
				// the extra introduced the build, so the form's image default did not apply
				services[position].Config.Image = defaultImageName(req.Name, configs[position].Name)
				configs[position].Config.Image = services[position].Config.Image
				needsImage = true
			}
		}
		if needsImage && len(topFields) == 0 {
			rendered, err := composefile.NewStack(services, req.Extra)
			if err == nil {
				parsed, err = composefile.Parse(rendered)
			}
			if err != nil {
				return Summary{}, err
			}
			data = rendered
		}
	}
	partial, err := s.checkChanges(ctx, changeSet{services: configs, indexed: req.IndexedFields, indices: indices, localNetworks: localNetworks})
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
	allFields = append(allFields, topFields...)
	if len(allFields) > 0 {
		return Summary{}, &ValidationError{Fields: allFields}
	}
	if configErr != nil {
		return Summary{}, configErr
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
	var rollbacks []func() error
	defer func() {
		if ok {
			return
		}
		for i := len(rollbacks) - 1; i >= 0; i-- {
			if err := rollbacks[i](); err != nil {
				var pathErr *fs.PathError
				if errors.As(err, &pathErr) { // keep absolute paths out of the log
					err = pathErr.Err
				}
				s.log.Error("Dockerfile rollback failed", "stack", req.Name, "err", err)
			}
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
	for _, df := range dockerfiles {
		target, inside, err := dockerfileTarget(dir, parsed, df.service)
		var notEditable *NotEditableError
		if errors.As(err, &notEditable) {
			return Summary{}, dockerfileFieldError(req.IndexedFields, df.index, notEditable.Reason)
		}
		if errors.Is(err, ErrNoBuild) {
			return Summary{}, dockerfileFieldError(req.IndexedFields, df.index, "this service doesn't build an image, so it has no Dockerfile")
		}
		if err != nil || !inside {
			return Summary{}, dockerfileOutsideError(req.IndexedFields, df.index)
		}
		_, rollback, err := writeDockerfile(dir, target, df.content, nil)
		if err != nil {
			if errors.Is(err, ErrOutsideStack) {
				return Summary{}, dockerfileOutsideError(req.IndexedFields, df.index)
			}
			return Summary{}, err
		}
		rollbacks = append(rollbacks, rollback)
	}
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
		cfg.Image = defaultImageName(stack, service)
	}
	if b.Context == "" {
		b.Context = "."
	}
	cfg.Build.Context = b.Context

	confined, err := s.confineBuild(stackDir, b)
	if err != nil {
		return cfg, err
	}
	cfg.Build.Context = confined.Context
	return cfg, nil
}

// defaultImageName is the tag a built service gets when none is given.
func defaultImageName(stack, service string) string {
	name := stack
	if service != stack {
		name += "-" + service
	}
	return "stackstead/" + name + ":latest"
}

const buildContextVariableMessage = "build context must not use variables"

// confineBuild checks that a build context lies inside the stack folder or an
// allowed directory. An absolute context comes back with symlinks resolved.
func (s *Service) confineBuild(stackDir string, b composefile.Build) (composefile.Build, error) {
	if b.Context == "" {
		b.Context = "."
	}
	if hasVariable(b.Context) {
		// compose interpolates after this check, so the value cannot be confined
		return b, &ValidationError{Fields: []FieldError{{Field: "build.context", Message: buildContextVariableMessage}}}
	}
	target := b.Context
	if !filepath.IsAbs(target) {
		target = filepath.Join(stackDir, target)
		// Contexts inside the new stack folder need no further check.
		if rel, err := filepath.Rel(stackDir, target); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return b, nil
		}
	}
	resolved, err := fsguard.Resolve(s.roots, target)
	if err != nil {
		msg := "build context must be inside an allowed directory"
		if !errors.Is(err, fsguard.ErrOutsideRoots) {
			msg = "build context: " + err.Error()
		}
		return b, &ValidationError{Fields: []FieldError{{Field: "build.context", Message: msg}}}
	}
	if filepath.IsAbs(b.Context) {
		b.Context = resolved
	}
	return b, nil
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

// createDockerfile is a Dockerfile to write for the service at Services[index].
type createDockerfile struct {
	index   int
	service string
	content []byte
}

const dockerfileOutsideMessage = "a Dockerfile can only be written inside the stack's folder"

func dockerfileOutsideError(indexed bool, index int) error {
	return dockerfileFieldError(indexed, index, dockerfileOutsideMessage)
}

func dockerfileFieldError(indexed bool, index int, message string) error {
	field := "dockerfile"
	if indexed {
		field = fmt.Sprintf("services[%d].dockerfile", index)
	}
	return &ValidationError{Fields: []FieldError{{Field: field, Message: message}}}
}

// CheckTopExtra rejects a stack-level extra that sets keys the server owns.
func CheckTopExtra(extra composefile.Extra) error {
	for _, key := range extra.Keys() {
		if key == "services" || key == "name" {
			return &ValidationError{Fields: []FieldError{{Field: "extra", Message: "extra may not set services or name"}}}
		}
	}
	return nil
}

// effectiveWithExtra reads a service back from the rendered document, so the
// port and address checks see what its extra added, and confines a build
// context the extra introduced. Problems with the build come back as fields
// named relative to the service.
func (s *Service) effectiveWithExtra(parsed *composefile.File, stackDir string, svc namedConfig) (composefile.ServiceConfig, []FieldError, error) {
	effective, err := parsed.EffectiveService(svc.Name)
	if err != nil {
		return composefile.ServiceConfig{}, nil, err
	}
	var fields []FieldError
	confine := func(b composefile.Build) bool {
		confined, err := s.confineBuild(stackDir, b)
		var ve *ValidationError
		if errors.As(err, &ve) {
			for _, field := range ve.Fields {
				if field.Message == buildContextVariableMessage {
					field.Field = "extra" // the value came from the extra, not the form
				}
				fields = append(fields, field)
			}
			return false
		}
		// The extra is written as typed, so a link that is repointed later
		// would escape the check; require the real path.
		if (filepath.IsAbs(b.Context) && confined.Context != filepath.Clean(b.Context)) ||
			(!filepath.IsAbs(b.Context) && s.relativeContextUsesLink(stackDir, b.Context)) {
			fields = append(fields, FieldError{Field: "build.context", Message: "build context must not be a symlink; use the real path"})
			return false
		}
		return true
	}
	if svc.Config.Build == nil && effective.Build != nil && !confine(*effective.Build) {
		return effective, fields, nil
	}
	ref, ok, err := parsed.BuildOf(svc.Name)
	if err != nil {
		return composefile.ServiceConfig{}, nil, err
	}
	if !ok {
		return effective, fields, nil
	}
	names := make([]string, 0, len(ref.AdditionalContexts))
	for name := range ref.AdditionalContexts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := ref.AdditionalContexts[name]
		if isRemoteContext(value) || hasNonPathScheme(value) {
			continue // fetched by the builder or another service, not read from this host
		}
		if !confine(composefile.Build{Context: value}) {
			break
		}
	}
	return effective, fields, nil
}

// relativeContextUsesLink reports whether a relative context that leaves the
// stack folder reaches its target through a symlink (its resolved path differs
// from the lexical one). Contexts inside the folder are not checked, like in
// confineBuild.
func (s *Service) relativeContextUsesLink(stackDir, context string) bool {
	if context == "" || hasVariable(context) {
		return false
	}
	target := filepath.Join(stackDir, context)
	if rel, err := filepath.Rel(stackDir, target); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	resolved, err := fsguard.Resolve(s.roots, target)
	if err != nil {
		return false // confineBuild reports unresolvable paths
	}
	realStack, err := resolveExisting(stackDir)
	if err != nil {
		return true
	}
	return resolved != filepath.Join(realStack, context)
}

// hasNonPathScheme reports additional-context values that name an image,
// another service or a build target instead of a directory.
func hasNonPathScheme(value string) bool {
	for _, prefix := range []string{"docker-image:", "service:", "target:", "oci-layout:"} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

// RegisterRestored registers a stack whose files a backup restore has just
// written. The name and project are validated, and the project is claimed
// under the project lock like any other registration.
func (s *Service) RegisterRestored(ctx context.Context, e Entry) error {
	if err := ValidateName(e.Name); err != nil {
		return err
	}
	if !filepath.IsAbs(e.ComposePath) {
		return fmt.Errorf("restored compose path %q is not absolute", e.ComposePath)
	}
	if e.Project == "" {
		e.Project = e.Name
	}
	s.projectMu.Lock() // see Service for lock order
	defer s.projectMu.Unlock()
	owner, taken, err := s.projectOwner(ctx, e.Project, e.Name)
	if err != nil {
		return err
	}
	if taken {
		return fmt.Errorf("%w: compose project %q is already used by stack %s", ErrProjectTaken, e.Project, owner)
	}
	return s.reg.Add(ctx, e)
}
