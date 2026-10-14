# Stackstead Plan 2 — composefile Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Go package that reads the form-editable settings of a compose service and writes changes back as minimal text edits, so a hand-written `compose.yaml` keeps its comments, blank lines, key order, quoting and unmodelled keys byte-for-byte outside the edited lines.

**Architecture:** `internal/composefile` parses with yaml.v3 only to *locate* things. Edits are spliced into the original bytes at the positions yaml.v3 reports, never by re-encoding the whole document (a spike showed re-encoding drops blank lines, re-indents sequences and moves comments). Every edit is then **verified semantically**: original and result are decoded, the intended change is applied to the decoded original, and the two must be deeply equal, or the edit is refused. A typed layer (`ServiceConfig`) maps form fields to edits and marks fields it cannot represent as read-only. A small disk layer does atomic writes, `.bak` backups and optimistic-concurrency checks.

**Tech Stack:** Go 1.26, `go.yaml.in/yaml/v3` (already in go.mod), stdlib `testing` only.

**Spec:** `docs/superpowers/specs/2026-10-14-stackstead-v1-design.md` (sections 4 composefile, 5.1–5.3, 6 File integrity, 8 Testing)

**Provenance:** every code block below was prototyped and run before this plan was written (`go test -race` green, plus a stress run over an Immich-style compose file with anchors, folded scalars, flow sequences and env interpolation). Transcribe it exactly; any deviation needs a reason in your report.

### Plan series (this is Plan 2 of 5)

1. Foundation — merged. 2. **composefile (this plan)**. 3. Stacks & lifecycle (uses this package; adds `docker compose config` validation, the registry, per-stack locking, API). 4. Observability. 5. Web UI.

## Global Constraints

- Package path `github.com/rcss-labs/stackstead/internal/composefile`; no new module dependencies (only `go.yaml.in/yaml/v3` and the stdlib).
- Must build with `CGO_ENABLED=0` for `linux/amd64` and `linux/arm64`.
- Spec: editing preserves comments, key ordering and unknown keys; "writes change only the touched nodes"; fields the forms don't model are read-only in forms and editable via raw YAML.
- Spec: writes are atomic (temp file in same dir → fsync → rename); the previous version is kept as `compose.yaml.bak`; a save is rejected when the file's content hash differs from the one the client loaded.
- Spec: new stacks get `build: {context, dockerfile}` plus an explicit image tag `stackstead/<name>:latest` — Plan 3's caller chooses the tag; this package renders whatever `Image` it is given.
- Tests assert observable behavior (bytes in → bytes out); write each test before its implementation and watch it fail.
- Commits use conventional-commit prefixes and explain why; end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. **A hand-written file edited through a form** must change only the edited lines — blank lines, comments (including a trailing `# pinned` on the edited line), sequence indentation style and quoting elsewhere stay byte-identical. (Tests: Task 2 golden cases; Task 6 `TestUpdateChangesOnlyTheEditedLine`, `TestUpdateKeepsEnvironmentListStyle`.)
2. **Values shared through YAML anchors / merge keys** (`<<: *base`, `environment: *env`) must never be silently changed for other services: such fields are read-only and the editor refuses anchored nodes. (Tests: Task 1 `TestServiceMarksUnrepresentableFieldsReadOnly`; Task 2 `TestApplyEditsRefuses`; Task 4 `TestVerifyCatchesDeleteOfInheritedKey`.)
3. **String values that look like other types** (`"true"`, `"8080"`, `"0.5"`) must stay strings when written, or compose would read a bool/number. (Test: Task 5 `TestNewRendersCanonicalFile` — `DEBUG: "true"`, `cpus: "0.5"`.)
4. **The file changed on disk between load and save** (hand edit, second browser tab) must be detected and nothing overwritten. (Tests: Task 3 `TestSaveDetectsConcurrentChange`, `TestSaveRefusesToCreateOverExistingFile`.)
5. **Clearing all resource limits** must not leave an empty `deploy:` shell, but must keep other `deploy` settings such as `replicas`. (Tests: Task 6 `TestUpdateClearingAllLimitsPrunesDeploy`, `TestUpdateClearingLimitsKeepsOtherDeploySettings`.)

Known limitations accepted for v1: CRLF files and flow-style document roots are refused by the editor (reading still works); an edited flow collection is rewritten in block style; when new nested collections are inserted into a compact-sequence file, their nested sequences use indented style.

## Execution waves (parallel subagent execution)

| Wave | Tasks (parallel within a wave) | Why |
|---|---|---|
| 1 | Task 1 (read), Task 2 (editor), Task 3 (disk) | disjoint files, no shared symbols |
| 2 | Task 4 (verifier), Task 5 (builders + New) | Task 4 needs Task 2's `edit`/`applyEdits`; Task 5 needs Task 1 (`Parse`, `Service`, `mustParse`) and Task 2 (`encode`) |
| 3 | Task 6 (UpdateService) | needs all of the above |

Symbol ownership (never redefine these in another task): Task 1 — `mappingValue`, `isScalar`, `readString`, `readStringList`, `onlyScalarKeys`, test helpers `mustParse`, `labCompose`. Task 2 — `edit`, `applyEdits`, `lookup`, `encode`, `nested`, `layout`, `splice`, test helpers `scalar`, `quoted`, `seqOf`, `path`, `doc`. Task 5 — `normalize`, `strNode`, `quotedNode`, `listNode`, other node builders. Task 6 — `hasOtherKeys`, `scalarEdit`, `listEdit`.

---

## File Structure

```
internal/composefile/
  file.go      Parse, File, Bytes, Services, serviceNode, mappingValue, errors
  model.go     Field constants, ServiceConfig and its parts, Service
  read.go      (*File).Service: typed read + read-only detection
  span.go      line index, entry extents, inline scalar scanning
  render.go    layout detection, node encoding helpers
  edit.go      edit type, applyEdits: text splicing (replace / insert / delete)
  verify.go    verifyEdits: semantic equality check of an edit
  build.go     node builders, normalize, New (render a new one-service file)
  update.go    (*File).UpdateService, ReadOnlyFieldError, limits pruning
  disk.go      Hash, Load, Save (atomic write, .bak, ErrConflict)
  *_test.go    read, edit, verify, build, update, disk (span/render/model are covered through them)
```

---

### Task 1: Parse and typed read

**Wave:** 1 (parallel with Tasks 2 and 3)

**Files:**
- Create: `internal/composefile/model.go`
- Create: `internal/composefile/file.go`
- Create: `internal/composefile/read.go`
- Test: `internal/composefile/read_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  ```go
  var ErrNotMapping, ErrMultipleDocuments, ErrServiceNotFound error
  type File struct { /* src []byte; root *yaml.Node */ }
  func Parse(src []byte) (*File, error)
  func (f *File) Bytes() []byte
  func (f *File) Services() ([]string, error)
  func (f *File) Service(name string) (Service, error)
  func (f *File) serviceNode(name string) (*yaml.Node, error) // used by Task 6
  func mappingValue(m *yaml.Node, key string) *yaml.Node    // nil-safe; used by Tasks 5-6
  type Field string // FieldImage ... FieldHealthcheck
  type ServiceConfig, Build, EnvVar, NetworkAttachment, Limits, Healthcheck, Service
  // test helpers in read_test.go: func mustParse(t *testing.T, src string) *File; const labCompose
  ```

- [ ] **Step 1: Write the failing tests**

Create `internal/composefile/read_test.go`:

```go
package composefile

import (
	"errors"
	"reflect"
	"testing"
)

const labCompose = `# My home lab
name: lab

services:
  web:
    image: "nginx:1.27"   # pinned
    build:
      context: ./web
      dockerfile: Dockerfile.prod
    ports:
    - "8080:80"
    - 8443:443

    volumes:
      - ./html:/usr/share/nginx/html:ro
    environment:
      TZ: Europe/Berlin
      WORKERS: 4
    networks:
      lan:
        ipv4_address: 192.168.50.10
    restart: unless-stopped
    deploy:
      replicas: 1
      resources:
        limits:
          cpus: "0.50"
          memory: 256M
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost"]
      interval: 30s
      retries: 3
    x-custom: {a: 1}
  db:
    image: postgres:16
    environment:
      - POSTGRES_PASSWORD=secret
    healthcheck:
      test: pg_isready
    network_mode: host

networks:
  lan:
    external: true
`

func mustParse(t *testing.T, src string) *File {
	t.Helper()
	f, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return f
}

func TestServicesInFileOrder(t *testing.T) {
	names, err := mustParse(t, labCompose).Services()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"web", "db"}; !reflect.DeepEqual(names, want) {
		t.Errorf("Services() = %v, want %v", names, want)
	}
}

func TestServiceReadsAllModeledFields(t *testing.T) {
	svc, err := mustParse(t, labCompose).Service("web")
	if err != nil {
		t.Fatal(err)
	}
	want := ServiceConfig{
		Image:       "nginx:1.27",
		Build:       &Build{Context: "./web", Dockerfile: "Dockerfile.prod"},
		Ports:       []string{"8080:80", "8443:443"},
		Volumes:     []string{"./html:/usr/share/nginx/html:ro"},
		Environment: []EnvVar{{"TZ", "Europe/Berlin"}, {"WORKERS", "4"}},
		Networks:    []NetworkAttachment{{Name: "lan", IPv4Address: "192.168.50.10"}},
		Limits:      Limits{CPUs: "0.50", Memory: "256M"},
		Restart:     "unless-stopped",
		Healthcheck: &Healthcheck{Test: []string{"CMD", "curl", "-f", "http://localhost"}, Interval: "30s", Retries: 3},
	}
	if !reflect.DeepEqual(svc.Config, want) {
		t.Errorf("Config =\n%+v\nwant\n%+v", svc.Config, want)
	}
	if len(svc.ReadOnly) != 0 {
		t.Errorf("ReadOnly = %v, want none", svc.ReadOnly)
	}
}

func TestServiceReadsAlternateForms(t *testing.T) {
	svc, err := mustParse(t, labCompose).Service("db")
	if err != nil {
		t.Fatal(err)
	}
	if want := []EnvVar{{"POSTGRES_PASSWORD", "secret"}}; !reflect.DeepEqual(svc.Config.Environment, want) {
		t.Errorf("Environment = %v, want %v", svc.Config.Environment, want)
	}
	if want := []string{"CMD-SHELL", "pg_isready"}; !reflect.DeepEqual(svc.Config.Healthcheck.Test, want) {
		t.Errorf("Healthcheck.Test = %v, want %v", svc.Config.Healthcheck.Test, want)
	}
	if svc.Config.NetworkMode != "host" {
		t.Errorf("NetworkMode = %q, want host", svc.Config.NetworkMode)
	}
}

func TestServiceMarksUnrepresentableFieldsReadOnly(t *testing.T) {
	src := `x-env: &env
  A: b
x-base: &base
  restart: always
services:
  app:
    <<: *base
    image: app
    ports:
      - target: 80
        published: 8080
    environment: *env
    networks:
      lan:
        aliases: [app]
    build:
      context: .
      args:
        VERSION: "1"
    cpus: 0.5
    healthcheck:
      test: ["CMD", "true"]
      disable: true
`
	svc, err := mustParse(t, src).Service("app")
	if err != nil {
		t.Fatal(err)
	}
	want := []Field{FieldBuild, FieldPorts, FieldEnvironment, FieldNetworks, FieldLimits, FieldRestart, FieldHealthcheck}
	if !reflect.DeepEqual(svc.ReadOnly, want) {
		t.Errorf("ReadOnly = %v, want %v", svc.ReadOnly, want)
	}
	if svc.Config.Image != "app" || svc.Config.Ports != nil || svc.Config.Build != nil {
		t.Errorf("read-only fields leaked into Config: %+v", svc.Config)
	}
}

func TestEnvironmentPassThroughIsReadOnly(t *testing.T) {
	for _, src := range []string{
		"services:\n  a:\n    environment:\n      - DEBUG\n",
		"services:\n  a:\n    environment:\n      DEBUG:\n",
	} {
		svc, err := mustParse(t, src).Service("a")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(svc.ReadOnly, []Field{FieldEnvironment}) {
			t.Errorf("%q: ReadOnly = %v, want [environment]", src, svc.ReadOnly)
		}
	}
}

func TestParseRejectsNonComposeDocuments(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want error
	}{
		{"empty", "", ErrNotMapping},
		{"comment only", "# nothing\n", ErrNotMapping},
		{"sequence root", "- a\n", ErrNotMapping},
		{"two documents", "services: {}\n---\nservices: {}\n", ErrMultipleDocuments},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse([]byte(tt.src)); !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
	if _, err := Parse([]byte("services: [\n")); err == nil {
		t.Error("syntax error: want error, got nil")
	}
}

func TestServiceNotFound(t *testing.T) {
	if _, err := mustParse(t, labCompose).Service("nope"); !errors.Is(err, ErrServiceNotFound) {
		t.Errorf("err = %v, want ErrServiceNotFound", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/composefile/`
Expected: FAIL — build error: undefined: Parse / ServiceConfig / ...

- [ ] **Step 3: Implement**

Create `model.go` and `file.go` first, then `read.go`.

Create `internal/composefile/model.go`:

```go
package composefile

// Field names a group of service settings the forms edit.
type Field string

const (
	FieldImage       Field = "image"
	FieldBuild       Field = "build"
	FieldPorts       Field = "ports"
	FieldVolumes     Field = "volumes"
	FieldEnvironment Field = "environment"
	FieldNetworks    Field = "networks"
	FieldNetworkMode Field = "network_mode"
	FieldLimits      Field = "limits"
	FieldRestart     Field = "restart"
	FieldHealthcheck Field = "healthcheck"
)

// ServiceConfig is the subset of a compose service that Stackstead edits
// through forms. Empty values mean "not set".
type ServiceConfig struct {
	Image       string
	Build       *Build
	Ports       []string // short syntax, e.g. "8080:80"
	Volumes     []string // short syntax, e.g. "data:/var/lib/data"
	Environment []EnvVar
	Networks    []NetworkAttachment
	NetworkMode string
	Limits      Limits // deploy.resources.limits
	Restart     string
	Healthcheck *Healthcheck
}

type Build struct {
	Context    string
	Dockerfile string
}

type EnvVar struct {
	Name  string
	Value string
}

type NetworkAttachment struct {
	Name        string
	IPv4Address string
}

type Limits struct {
	CPUs   string // e.g. "0.5"
	Memory string // e.g. "512M"
}

type Healthcheck struct {
	Test        []string // exec form; a string test reads as ["CMD-SHELL", test]
	Interval    string
	Timeout     string
	StartPeriod string
	Retries     int
}

// Service is one service as read from a file.
type Service struct {
	Name   string
	Config ServiceConfig
	// ReadOnly lists fields present in a form the editor cannot represent
	// (long syntax, unknown sub-keys, YAML anchors/aliases, or inherited
	// through a merge key). They keep their zero value in Config and can
	// only be changed as raw YAML.
	ReadOnly []Field
}
```

Create `internal/composefile/file.go`:

```go
// Package composefile reads and edits Docker Compose files while preserving
// everything the author wrote that an edit does not touch: comments, blank
// lines, key order, quoting, indentation and keys Stackstead does not model.
package composefile

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"go.yaml.in/yaml/v3"
)

var (
	ErrNotMapping        = errors.New("compose file must be a YAML mapping")
	ErrMultipleDocuments = errors.New("compose file must contain a single YAML document")
	ErrServiceNotFound   = errors.New("service not found")
)

// File is a parsed compose file. It keeps the original bytes so edits can be
// applied as minimal text changes.
type File struct {
	src  []byte
	root *yaml.Node // top-level mapping
}

func Parse(src []byte) (*File, error) {
	dec := yaml.NewDecoder(bytes.NewReader(src))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, ErrNotMapping
		}
		return nil, fmt.Errorf("parse compose file: %w", err)
	}
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case err == nil:
		return nil, ErrMultipleDocuments
	case !errors.Is(err, io.EOF):
		return nil, fmt.Errorf("parse compose file: %w", err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, ErrNotMapping
	}
	return &File{src: src, root: doc.Content[0]}, nil
}

// Bytes returns the file's current content.
func (f *File) Bytes() []byte { return bytes.Clone(f.src) }

// Services returns the service names in file order.
func (f *File) Services() ([]string, error) {
	services := mappingValue(f.root, "services")
	if services == nil {
		return nil, nil
	}
	if services.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%w: services must be a mapping", ErrNotMapping)
	}
	names := make([]string, 0, len(services.Content)/2)
	for i := 0; i < len(services.Content); i += 2 {
		names = append(names, services.Content[i].Value)
	}
	return names, nil
}

func (f *File) serviceNode(name string) (*yaml.Node, error) {
	services := mappingValue(f.root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%w: %s", ErrServiceNotFound, name)
	}
	svc := mappingValue(services, name)
	if svc == nil {
		return nil, fmt.Errorf("%w: %s", ErrServiceNotFound, name)
	}
	if svc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%w: service %s must be a mapping", ErrNotMapping, name)
	}
	return svc, nil
}

// mappingValue returns the value for key in mapping m, or nil (also when m is nil).
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}
```

Create `internal/composefile/read.go`:

```go
package composefile

import (
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// fieldReader parses one field's value node into cfg, returning false (and
// leaving cfg untouched) when the value has a shape the model cannot hold.
type fieldReader struct {
	field Field
	key   string
	read  func(v *yaml.Node, cfg *ServiceConfig) bool
}

var fieldReaders = []fieldReader{
	{FieldImage, "image", func(v *yaml.Node, cfg *ServiceConfig) bool { return readString(v, &cfg.Image) }},
	{FieldBuild, "build", readBuild},
	{FieldPorts, "ports", func(v *yaml.Node, cfg *ServiceConfig) bool { return readStringList(v, &cfg.Ports) }},
	{FieldVolumes, "volumes", func(v *yaml.Node, cfg *ServiceConfig) bool { return readStringList(v, &cfg.Volumes) }},
	{FieldEnvironment, "environment", readEnvironment},
	{FieldNetworks, "networks", readNetworks},
	{FieldNetworkMode, "network_mode", func(v *yaml.Node, cfg *ServiceConfig) bool { return readString(v, &cfg.NetworkMode) }},
	{FieldLimits, "deploy", readLimits},
	{FieldRestart, "restart", func(v *yaml.Node, cfg *ServiceConfig) bool { return readString(v, &cfg.Restart) }},
	{FieldHealthcheck, "healthcheck", readHealthcheck},
}

// legacyLimitKeys set limits outside deploy.resources; editing deploy limits
// while they exist would leave two conflicting sources of truth.
var legacyLimitKeys = []string{"cpus", "mem_limit"}

// Service reads one service's form-editable settings.
func (f *File) Service(name string) (Service, error) {
	node, err := f.serviceNode(name)
	if err != nil {
		return Service{}, err
	}
	svc := Service{Name: name}

	var inherited map[string]any // keys the service gets from merge keys ("<<")
	if mappingValue(node, "<<") != nil {
		var full map[string]any
		if err := node.Decode(&full); err == nil {
			inherited = full
		}
	}

	for _, fr := range fieldReaders {
		v := mappingValue(node, fr.key)
		var readOnly bool
		switch {
		case v == nil:
			_, readOnly = inherited[fr.key]
		case usesAnchors(v):
			readOnly = true
		default:
			readOnly = !fr.read(v, &svc.Config)
		}
		if fr.field == FieldLimits && hasAnyKey(node, legacyLimitKeys) {
			svc.Config.Limits = Limits{}
			readOnly = true
		}
		if readOnly {
			svc.ReadOnly = append(svc.ReadOnly, fr.field)
		}
	}
	return svc, nil
}

func usesAnchors(n *yaml.Node) bool {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return true
	}
	for _, c := range n.Content {
		if usesAnchors(c) {
			return true
		}
	}
	return false
}

func hasAnyKey(m *yaml.Node, keys []string) bool {
	for _, k := range keys {
		if mappingValue(m, k) != nil {
			return true
		}
	}
	return false
}

func isScalar(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.ShortTag() != "!!null"
}

func readString(v *yaml.Node, dst *string) bool {
	if !isScalar(v) {
		return false
	}
	*dst = v.Value
	return true
}

func readStringList(v *yaml.Node, dst *[]string) bool {
	if v.Kind != yaml.SequenceNode {
		return false
	}
	items := make([]string, 0, len(v.Content))
	for _, item := range v.Content {
		if !isScalar(item) {
			return false
		}
		items = append(items, item.Value)
	}
	*dst = items
	return true
}

// onlyKeys reports whether every key of mapping m is in allowed and every
// value is a non-null scalar.
func onlyScalarKeys(m *yaml.Node, allowed ...string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		ok := false
		for _, a := range allowed {
			ok = ok || m.Content[i].Value == a
		}
		if !ok || !isScalar(m.Content[i+1]) {
			return false
		}
	}
	return true
}

func readBuild(v *yaml.Node, cfg *ServiceConfig) bool {
	switch {
	case isScalar(v):
		cfg.Build = &Build{Context: v.Value}
		return true
	case v.Kind == yaml.MappingNode && onlyScalarKeys(v, "context", "dockerfile"):
		b := &Build{}
		readString(orNull(mappingValue(v, "context")), &b.Context)
		readString(orNull(mappingValue(v, "dockerfile")), &b.Dockerfile)
		cfg.Build = b
		return true
	}
	return false
}

func orNull(n *yaml.Node) *yaml.Node {
	if n == nil {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"}
	}
	return n
}

func readEnvironment(v *yaml.Node, cfg *ServiceConfig) bool {
	var vars []EnvVar
	switch v.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(v.Content); i += 2 {
			if !isScalar(v.Content[i+1]) {
				return false // null means "pass through from the host", which the model cannot express
			}
			vars = append(vars, EnvVar{Name: v.Content[i].Value, Value: v.Content[i+1].Value})
		}
	case yaml.SequenceNode:
		for _, item := range v.Content {
			if !isScalar(item) {
				return false
			}
			name, value, ok := strings.Cut(item.Value, "=")
			if !ok {
				return false
			}
			vars = append(vars, EnvVar{Name: name, Value: value})
		}
	default:
		return false
	}
	cfg.Environment = vars
	return true
}

func readNetworks(v *yaml.Node, cfg *ServiceConfig) bool {
	var nets []NetworkAttachment
	switch v.Kind {
	case yaml.SequenceNode:
		for _, item := range v.Content {
			if !isScalar(item) {
				return false
			}
			nets = append(nets, NetworkAttachment{Name: item.Value})
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(v.Content); i += 2 {
			att := NetworkAttachment{Name: v.Content[i].Value}
			switch opts := v.Content[i+1]; {
			case opts.Kind == yaml.ScalarNode && opts.ShortTag() == "!!null":
			case opts.Kind == yaml.MappingNode && onlyScalarKeys(opts, "ipv4_address"):
				readString(orNull(mappingValue(opts, "ipv4_address")), &att.IPv4Address)
			default:
				return false
			}
			nets = append(nets, att)
		}
	default:
		return false
	}
	cfg.Networks = nets
	return true
}

// readLimits reads deploy.resources.limits.{cpus,memory}; other deploy
// settings are left alone and do not make the field read-only.
func readLimits(deploy *yaml.Node, cfg *ServiceConfig) bool {
	var lim Limits
	node := deploy
	for _, key := range []string{"resources", "limits"} {
		if node.Kind != yaml.MappingNode {
			return false
		}
		if node = mappingValue(node, key); node == nil {
			cfg.Limits = lim
			return true
		}
	}
	if node.Kind != yaml.MappingNode {
		return false
	}
	if v := mappingValue(node, "cpus"); v != nil && !readString(v, &lim.CPUs) {
		return false
	}
	if v := mappingValue(node, "memory"); v != nil && !readString(v, &lim.Memory) {
		return false
	}
	cfg.Limits = lim
	return true
}

func readHealthcheck(v *yaml.Node, cfg *ServiceConfig) bool {
	if v.Kind != yaml.MappingNode {
		return false
	}
	h := &Healthcheck{}
	for i := 0; i+1 < len(v.Content); i += 2 {
		key, val := v.Content[i].Value, v.Content[i+1]
		var ok bool
		switch key {
		case "test":
			if isScalar(val) {
				h.Test, ok = []string{"CMD-SHELL", val.Value}, true
			} else {
				ok = readStringList(val, &h.Test)
			}
		case "interval":
			ok = readString(val, &h.Interval)
		case "timeout":
			ok = readString(val, &h.Timeout)
		case "start_period":
			ok = readString(val, &h.StartPeriod)
		case "retries":
			if isScalar(val) {
				n, err := strconv.Atoi(val.Value)
				h.Retries, ok = n, err == nil
			}
		}
		if !ok {
			return false
		}
	}
	cfg.Healthcheck = h
	return true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/composefile/ -v`
Expected: PASS for every test in the package (including tests of earlier, already-merged tasks).

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/composefile/model.go internal/composefile/file.go internal/composefile/read.go internal/composefile/read_test.go
git commit -F - <<'EOF'
feat: parse compose files and read form-editable service settings

Fields stored in a shape the forms cannot represent (long syntax, anchors,
merge-key inheritance) are reported read-only so they are never rewritten.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
EOF
```

---

### Task 2: Minimal-diff text editor

**Wave:** 1 (parallel with Tasks 1 and 3)

**Files:**
- Create: `internal/composefile/span.go`
- Create: `internal/composefile/render.go`
- Create: `internal/composefile/edit.go`
- Test: `internal/composefile/edit_test.go`

**Interfaces:**
- Consumes: nothing (parses with yaml.v3 itself; uses none of Task 1's symbols).
- Produces:
  ```go
  type edit struct { path []string; value *yaml.Node } // value nil = delete key
  func applyEdits(src []byte, edits []edit) ([]byte, error)
  var errUnsupportedLayout, errSharedNode, errPathNotMapping error
  func lookup(m *yaml.Node, name string) (key, val *yaml.Node)
  func encode(n *yaml.Node, unit int) string // used by Task 5
  func nested(path []string, value *yaml.Node) *yaml.Node
  // test helpers in edit_test.go: scalar, quoted, seqOf, path, doc
  ```

- [ ] **Step 1: Write the failing tests**

Create `internal/composefile/edit_test.go`:

```go
package composefile

import (
	"errors"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func scalar(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: v} }

func quoted(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: v, Style: yaml.DoubleQuotedStyle}
}

func seqOf(items ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Content: items}
}

func path(p string) []string { return strings.Split(p, ".") }

// doc trims the leading newline so test YAML can start on its own line.
func doc(s string) string { return strings.TrimPrefix(s, "\n") }

func TestApplyEditsGolden(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		edits []edit
		want  string
	}{
		{
			name: "replace inline scalar keeps comment and spacing",
			in: doc(`
services:
  web:
    image: "nginx:1.27"   # pinned
    restart: always
`),
			edits: []edit{{path("services.web.image"), scalar("nginx:1.28")}},
			want: doc(`
services:
  web:
    image: nginx:1.28   # pinned
    restart: always
`),
		},
		{
			name: "replace compact block sequence keeps surrounding blank lines",
			in: doc(`
# My lab
services:
  web:
    ports:
    - "8080:80"
    - 8443:443

    environment:
      TZ: Europe/Berlin
`),
			edits: []edit{{path("services.web.ports"), seqOf(quoted("9090:80"))}},
			want: doc(`
# My lab
services:
  web:
    ports:
    - "9090:80"

    environment:
      TZ: Europe/Berlin
`),
		},
		{
			name: "replace indented sequence keeps key-line comment",
			in: doc(`
services:
  web:
    ports: # published
      - "80:80"
    restart: always
`),
			edits: []edit{{path("services.web.ports"), seqOf(quoted("81:80"), quoted("443:443"))}},
			want: doc(`
services:
  web:
    ports: # published
      - "81:80"
      - "443:443"
    restart: always
`),
		},
		{
			name: "insert new key after last entry, before trailing blank lines",
			in: doc(`
services:
  web:
    image: nginx

  db:
    image: postgres
`),
			edits: []edit{{path("services.web.restart"), scalar("unless-stopped")}},
			want: doc(`
services:
  web:
    image: nginx
    restart: unless-stopped

  db:
    image: postgres
`),
		},
		{
			name: "create nested path",
			in: doc(`
services:
  web:
    image: nginx
`),
			edits: []edit{{path("services.web.deploy.resources.limits.memory"), scalar("512M")}},
			want: doc(`
services:
  web:
    image: nginx
    deploy:
      resources:
        limits:
          memory: 512M
`),
		},
		{
			name: "four-space indentation is respected",
			in: doc(`
services:
    web:
        image: nginx
`),
			edits: []edit{{path("services.web.deploy.resources.limits.cpus"), quoted("0.5")}},
			want: doc(`
services:
    web:
        image: nginx
        deploy:
            resources:
                limits:
                    cpus: "0.5"
`),
		},
		{
			name: "delete key with block value",
			in: doc(`
services:
  web:
    image: nginx
    volumes:
      - data:/data   # app data
      - ./conf:/conf
    restart: always
`),
			edits: []edit{{path("services.web.volumes"), nil}},
			want: doc(`
services:
  web:
    image: nginx
    restart: always
`),
		},
		{
			name:  "deleting a missing key is a no-op",
			in:    "services:\n  web:\n    image: nginx\n",
			edits: []edit{{path("services.web.restart"), nil}},
			want:  "services:\n  web:\n    image: nginx\n",
		},
		{
			name: "deleting the only key leaves an empty mapping",
			in: doc(`
services:
  web:
    deploy:
      resources:
        limits:
          memory: 512M
`),
			edits: []edit{{path("services.web.deploy.resources.limits.memory"), nil}},
			want: doc(`
services:
  web:
    deploy:
      resources:
        limits: {}
`),
		},
		{
			name: "edit inside flow mapping rewrites it in block style",
			in: doc(`
services:
  web:
    deploy: {resources: {limits: {cpus: "1"}}}
    image: nginx
`),
			edits: []edit{{path("services.web.deploy.resources.limits.memory"), scalar("1G")}},
			want: doc(`
services:
  web:
    deploy:
      resources:
        limits:
          cpus: "1"
          memory: 1G
    image: nginx
`),
		},
		{
			name: "null value becomes nested mapping",
			in: doc(`
services:
  web:
    deploy:
    image: nginx
`),
			edits: []edit{{path("services.web.deploy.resources.limits.memory"), scalar("1G")}},
			want: doc(`
services:
  web:
    deploy:
      resources:
        limits:
          memory: 1G
    image: nginx
`),
		},
		{
			name: "block scalar replaced by inline scalar",
			in: doc(`
services:
  db:
    command: >
      postgres -c max_connections=200
    image: postgres
`),
			edits: []edit{{path("services.db.command"), scalar("postgres")}},
			want: doc(`
services:
  db:
    command: postgres
    image: postgres
`),
		},
		{
			name:  "insert into file without trailing newline",
			in:    "services:\n  web:\n    image: nginx",
			edits: []edit{{path("services.web.restart"), scalar("always")}},
			want:  "services:\n  web:\n    image: nginx\n    restart: always",
		},
		{
			name: "top-level insert goes after trailing comments of deeper entries only",
			in: doc(`
services:
  web:
    image: nginx
# end of services
`),
			edits: []edit{{path("networks.lan.external"), scalar("true")}},
			want: doc(`
services:
  web:
    image: nginx
networks:
  lan:
    external: true
# end of services
`),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := applyEdits([]byte(tt.in), tt.edits)
			if err != nil {
				t.Fatalf("applyEdits: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestApplyEditsRefuses(t *testing.T) {
	tests := []struct {
		name string
		in   string
		e    edit
		want error
	}{
		{
			name: "anchored value",
			in:   "x-base: &base\n  restart: always\nservices:\n  web:\n    <<: *base\n",
			e:    edit{path("x-base.restart"), scalar("no")},
			want: errSharedNode,
		},
		{
			name: "alias value",
			in:   "x-env: &env\n  A: b\nservices:\n  web:\n    environment: *env\n",
			e:    edit{path("services.web.environment.A"), scalar("c")},
			want: errSharedNode,
		},
		{
			name: "path through a scalar",
			in:   "services:\n  web:\n    image: nginx\n",
			e:    edit{path("services.web.image.tag"), scalar("x")},
			want: errPathNotMapping,
		},
		{
			name: "CRLF line endings",
			in:   "services:\r\n  web:\r\n    image: nginx\r\n",
			e:    edit{path("services.web.image"), scalar("x")},
			want: errUnsupportedLayout,
		},
		{
			name: "flow mapping root",
			in:   "{services: {}}\n",
			e:    edit{path("services.web"), scalar("x")},
			want: errUnsupportedLayout,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := applyEdits([]byte(tt.in), []edit{tt.e})
			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/composefile/`
Expected: FAIL — build error: undefined: applyEdits / edit / ...

- [ ] **Step 3: Implement**

Create `span.go` and `render.go` first, then `edit.go`.

Create `internal/composefile/span.go`:

```go
package composefile

import (
	"strings"
	"unicode/utf8"
)

// lines indexes a source document by line so yaml.v3 node positions
// (1-based line and character column) can be turned into byte offsets.
type lines struct {
	src   []byte
	start []int // byte offset where each line begins; index 0 is line 1
}

func splitLines(src []byte) lines {
	l := lines{src: src, start: []int{0}}
	for i, b := range src {
		if b == '\n' && i+1 < len(src) {
			l.start = append(l.start, i+1)
		}
	}
	return l
}

func (l lines) count() int { return len(l.start) }

// text returns line n (1-based) without its newline.
func (l lines) text(n int) string {
	end := len(l.src)
	if n < len(l.start) {
		end = l.start[n]
	}
	return strings.TrimSuffix(string(l.src[l.start[n-1]:end]), "\n")
}

// lineEnd returns the byte offset of the end of line n's content (its newline, if any).
func (l lines) lineEnd(n int) int { return l.start[n-1] + len(l.text(n)) }

// column converts a 1-based character column on line n to a byte index within that line.
func (l lines) column(n, col int) int {
	t := l.text(n)
	i := 0
	for c := 1; c < col && i < len(t); c++ {
		_, size := utf8.DecodeRuneInString(t[i:])
		i += size
	}
	return i
}

func indentOf(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }

// entryEnd returns the last line of the mapping entry whose key is on keyLine
// at keyIndent: every following line indented deeper belongs to it, as do
// compact sequence items ("- x" at the key's own indentation) when the value
// is such a sequence. Trailing blank lines and shallower comments do not.
func (l lines) entryEnd(keyLine, keyIndent int, compactSeq bool) int {
	end := keyLine
	for n := keyLine + 1; n <= l.count(); n++ {
		t := l.text(n)
		if isBlank(t) {
			continue
		}
		ind := indentOf(t)
		rest := t[ind:]
		if ind > keyIndent || (compactSeq && ind == keyIndent && (rest == "-" || strings.HasPrefix(rest, "- "))) {
			end = n
			continue
		}
		break
	}
	return end
}

// inlineEnd returns the index just past the single-line scalar or flow
// collection that starts at index i of line, or -1 if it continues past the line.
func inlineEnd(line string, i int) int {
	if i >= len(line) {
		return i
	}
	switch line[i] {
	case '"':
		for j := i + 1; j < len(line); j++ {
			if line[j] == '\\' {
				j++
				continue
			}
			if line[j] == '"' {
				return j + 1
			}
		}
		return -1
	case '\'':
		for j := i + 1; j < len(line); j++ {
			if line[j] == '\'' {
				if j+1 < len(line) && line[j+1] == '\'' {
					j++
					continue
				}
				return j + 1
			}
		}
		return -1
	case '[', '{':
		depth := 0
		var quote byte
		for j := i; j < len(line); j++ {
			c := line[j]
			switch {
			case quote == '"' && c == '\\':
				j++
			case quote != 0:
				if c == quote {
					quote = 0
				}
			case c == '"' || c == '\'':
				quote = c
			case c == '[' || c == '{':
				depth++
			case c == ']' || c == '}':
				depth--
				if depth == 0 {
					return j + 1
				}
			}
		}
		return -1
	default:
		end := len(line)
		if k := strings.Index(line[i:], " #"); k >= 0 {
			end = i + k
		}
		return i + len(strings.TrimRight(line[i:end], " "))
	}
}

// colonAfterKey returns the index of the ':' that ends the mapping key starting
// at index i of line, or -1.
func colonAfterKey(line string, i int) int {
	j := i
	if c := line[i]; c == '"' || c == '\'' {
		if j = inlineEnd(line, i); j < 0 {
			return -1
		}
	}
	for ; j < len(line); j++ {
		if line[j] == ':' && (j+1 == len(line) || line[j+1] == ' ') {
			return j
		}
	}
	return -1
}

// trailingComment returns the comment ("# ...") that follows only whitespace
// from index i of line, or "".
func trailingComment(line string, i int) string {
	if i < 0 || i > len(line) {
		return ""
	}
	rest := strings.TrimLeft(line[i:], " ")
	if strings.HasPrefix(rest, "#") {
		return rest
	}
	return ""
}
```

Create `internal/composefile/render.go`:

```go
package composefile

import (
	"bytes"
	"strings"

	"go.yaml.in/yaml/v3"
)

// layout is the indentation style detected from a document, so inserted
// text matches what the author wrote.
type layout struct {
	unit       int  // spaces per nesting level
	compactSeq bool // sequence items written at the parent key's indentation
}

func detectLayout(root *yaml.Node) layout {
	lay := layout{unit: 2}
	unitFound, seqFound := false, false
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode && n.Style&yaml.FlowStyle == 0 {
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				if v.Line > k.Line && v.Style&yaml.FlowStyle == 0 {
					switch v.Kind {
					case yaml.MappingNode:
						if d := v.Column - k.Column; d > 0 && (!unitFound || d < lay.unit) {
							lay.unit, unitFound = d, true
						}
					case yaml.SequenceNode:
						if !seqFound {
							lay.compactSeq, seqFound = v.Column == k.Column, true
						}
					}
				}
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(root)
	return lay
}

// encode renders a node the way yaml.v3 would, without the final newline.
func encode(n *yaml.Node, unit int) string {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(unit)
	_ = enc.Encode(n) // encoding an in-memory node tree cannot fail
	_ = enc.Close()
	return strings.TrimSuffix(buf.String(), "\n")
}

// renderInline returns the single-line text for a scalar or flow node, or
// false if the node needs block layout.
func renderInline(n *yaml.Node, unit int) (string, bool) {
	if n.Kind != yaml.ScalarNode && n.Style&yaml.FlowStyle == 0 {
		return "", false
	}
	s := encode(n, unit)
	return s, !strings.Contains(s, "\n")
}

// indentLines prefixes every non-empty line of s with n spaces.
func indentLines(s string, n int) string {
	pad := strings.Repeat(" ", n)
	ls := strings.Split(s, "\n")
	for i, line := range ls {
		if line != "" {
			ls[i] = pad + line
		}
	}
	return strings.Join(ls, "\n")
}

// nested wraps value in mappings keyed by path: nested([a b], v) is {a: {b: v}}.
func nested(path []string, value *yaml.Node) *yaml.Node {
	if len(path) == 0 {
		return value
	}
	return &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Value: path[0]},
		nested(path[1:], value),
	}}
}

// blockCopy deep-copies n, switching flow collections to block style.
func blockCopy(n *yaml.Node) *yaml.Node {
	c := *n
	if c.Kind == yaml.MappingNode || c.Kind == yaml.SequenceNode {
		c.Style &^= yaml.FlowStyle
	}
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, child := range n.Content {
		c.Content[i] = blockCopy(child)
	}
	return &c
}
```

Create `internal/composefile/edit.go`:

```go
package composefile

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// edit is one change to a compose document: set the value at path (a chain
// of mapping keys from the root), or delete that key when value is nil.
type edit struct {
	path  []string
	value *yaml.Node
}

var (
	errUnsupportedLayout = errors.New("unsupported YAML layout")
	errSharedNode        = errors.New("value is shared through a YAML anchor or alias")
	errPathNotMapping    = errors.New("path does not lead through mappings")
)

// applyEdits rewrites src so that only the text belonging to each edited key
// changes; comments, blank lines, ordering and formatting elsewhere are kept
// byte-for-byte. Edits are applied in order.
func applyEdits(src []byte, edits []edit) ([]byte, error) {
	if bytes.Contains(src, []byte("\r\n")) {
		return nil, fmt.Errorf("%w: CRLF line endings", errUnsupportedLayout)
	}
	out := src
	for _, e := range edits {
		var err error
		if out, err = applyEdit(out, e); err != nil {
			return nil, fmt.Errorf("edit %s: %w", strings.Join(e.path, "."), err)
		}
	}
	return out, nil
}

func applyEdit(src []byte, e edit) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode || doc.Content[0].Style&yaml.FlowStyle != 0 {
		return nil, fmt.Errorf("%w: document root must be a block mapping", errUnsupportedLayout)
	}
	l := splitLines(src)
	lay := detectLayout(doc.Content[0])

	m := doc.Content[0]
	var mKey *yaml.Node // key whose value is m; nil for the root
	for i, name := range e.path {
		if m.Anchor != "" {
			return nil, errSharedNode
		}
		if m.Style&yaml.FlowStyle != 0 {
			// Flow mappings cannot be edited line by line; rewrite the whole
			// mapping in block style with the edit applied.
			updated := blockCopy(m)
			setInTree(updated, e.path[i:], e.value)
			return replaceValue(l, mKey, m, updated, lay)
		}
		key, val := lookup(m, name)
		if key == nil {
			if e.value == nil {
				return src, nil
			}
			return insertEntry(l, m, nested(e.path[i:], e.value), lay), nil
		}
		if val.Kind == yaml.AliasNode || val.Anchor != "" {
			return nil, errSharedNode
		}
		if i == len(e.path)-1 {
			if e.value != nil {
				return replaceValue(l, key, val, e.value, lay)
			}
			if len(m.Content) == 2 && mKey != nil {
				// Deleting the only key would leave a null; keep an empty mapping.
				return replaceValue(l, mKey, m, &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}, lay)
			}
			return deleteEntry(l, key, val), nil
		}
		if val.Kind == yaml.ScalarNode && val.ShortTag() == "!!null" {
			if e.value == nil {
				return src, nil
			}
			return replaceValue(l, key, val, nested(e.path[i+1:], e.value), lay)
		}
		if val.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%w: %s", errPathNotMapping, strings.Join(e.path[:i+1], "."))
		}
		m, mKey = val, key
	}
	return nil, fmt.Errorf("%w: empty path", errPathNotMapping)
}

func lookup(m *yaml.Node, name string) (key, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == name {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

// setInTree applies an edit to an in-memory node tree (used for flow mappings).
func setInTree(m *yaml.Node, path []string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != path[0] {
			continue
		}
		switch {
		case len(path) > 1 && m.Content[i+1].Kind == yaml.MappingNode:
			setInTree(m.Content[i+1], path[1:], value)
		case len(path) > 1:
			if value != nil {
				m.Content[i+1] = nested(path[1:], value)
			}
		case value == nil:
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
		default:
			m.Content[i+1] = value
		}
		return
	}
	if value != nil {
		m.Content = append(m.Content, nested(path, value).Content...)
	}
}

func splice(src []byte, start, end int, text string) []byte {
	out := make([]byte, 0, len(src)-(end-start)+len(text))
	out = append(out, src[:start]...)
	out = append(out, text...)
	return append(out, src[end:]...)
}

// replaceValue rewrites the value of the entry whose key node is key.
func replaceValue(l lines, key, old, value *yaml.Node, lay layout) ([]byte, error) {
	keyLine, keyIndent := key.Line, key.Column-1
	line := l.text(keyLine)
	colon := colonAfterKey(line, l.column(keyLine, key.Column))
	if colon < 0 {
		return nil, fmt.Errorf("%w: cannot find ':' after key %q", errUnsupportedLayout, key.Value)
	}
	compact := old.Kind == yaml.SequenceNode && old.Style&yaml.FlowStyle == 0 && old.Column-1 == keyIndent
	end := l.entryEnd(keyLine, keyIndent, compact)
	blockScalar := old.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0
	// "key:" with nothing after it is a null with no text of its own.
	implicitNull := old.Kind == yaml.ScalarNode && old.Value == "" && old.Style == 0 && old.ShortTag() == "!!null"
	valueOnKeyLine := old.Line == keyLine && !blockScalar && !implicitNull

	// Where the old value's text ends on the key line, so a trailing comment can be kept.
	afterValue := colon + 1
	if valueOnKeyLine {
		afterValue = inlineEnd(line, l.column(keyLine, old.Column))
	}
	inline, isInline := renderInline(value, lay.unit)

	// Fast path: single-line value replaced by a single-line value.
	if isInline && end == keyLine && valueOnKeyLine && afterValue >= 0 {
		start := l.start[keyLine-1]
		return splice(l.src, start+l.column(keyLine, old.Column), start+afterValue, inline), nil
	}

	comment := ""
	if afterValue >= 0 {
		comment = trailingComment(line, afterValue)
	}
	if comment != "" {
		comment = " " + comment
	}

	var text string
	switch {
	case value.Kind == yaml.ScalarNode || isInline:
		// A scalar's first line goes after the key; block scalar content follows indented.
		rendered := encode(value, lay.unit)
		first, rest, multi := strings.Cut(rendered, "\n")
		text = " " + first + comment
		if multi {
			text += "\n" + indentLines(rest, keyIndent)
		}
	default:
		indent := keyIndent + lay.unit
		switch {
		case old.Kind == value.Kind && old.Style&yaml.FlowStyle == 0 && old.Line > keyLine:
			indent = old.Column - 1 // replacing a block collection: keep its indentation
		case value.Kind == yaml.SequenceNode && lay.compactSeq:
			indent = keyIndent
		}
		text = comment + "\n" + indentLines(encode(value, lay.unit), indent)
	}
	return splice(l.src, l.start[keyLine-1]+colon+1, l.lineEnd(end), text), nil
}

// insertEntry adds entry (a single-key mapping node) after m's last entry.
func insertEntry(l lines, m, entry *yaml.Node, lay layout) []byte {
	lastKey, lastVal := m.Content[len(m.Content)-2], m.Content[len(m.Content)-1]
	keyIndent := m.Content[0].Column - 1
	compact := lastVal.Kind == yaml.SequenceNode && lastVal.Style&yaml.FlowStyle == 0 && lastVal.Column == lastKey.Column
	end := l.entryEnd(lastKey.Line, lastKey.Column-1, compact)
	text := "\n" + indentLines(encode(entry, lay.unit), keyIndent)
	at := l.lineEnd(end)
	return splice(l.src, at, at, text)
}

// deleteEntry removes the lines of the entry whose key node is key.
func deleteEntry(l lines, key, val *yaml.Node) []byte {
	compact := val.Kind == yaml.SequenceNode && val.Style&yaml.FlowStyle == 0 && val.Column == key.Column
	end := l.entryEnd(key.Line, key.Column-1, compact)
	stop := len(l.src)
	if end < l.count() {
		stop = l.start[end]
	}
	return splice(l.src, l.start[key.Line-1], stop, "")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/composefile/ -v`
Expected: PASS for every test in the package (including tests of earlier, already-merged tasks).

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/composefile/span.go internal/composefile/render.go internal/composefile/edit.go internal/composefile/edit_test.go
git commit -F - <<'EOF'
feat: splice compose edits into the original text

Re-encoding a YAML document drops blank lines, re-indents sequences and moves
comments, so a one-field form edit would rewrite a whole hand-written file.
Edits are applied as minimal text changes at yaml.v3 node positions instead.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
EOF
```

---

### Task 3: Atomic save with backup and conflict detection

**Wave:** 1 (parallel with Tasks 1 and 2)

**Files:**
- Create: `internal/composefile/disk.go`
- Test: `internal/composefile/disk_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  ```go
  var ErrConflict error
  func Hash(data []byte) string // sha256 hex
  func Load(path string) (data []byte, hash string, err error)
  func Save(path string, data []byte, expectedHash string) error // "" = create a new file
  ```

- [ ] **Step 1: Write the failing tests**

Create `internal/composefile/disk_test.go`:

```go
package composefile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSaveCreatesNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.yaml")
	if err := Save(path, []byte("services: {}\n"), ""); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := readFile(t, path); got != "services: {}\n" {
		t.Errorf("content = %q", got)
	}
	if _, err := os.Stat(path + ".bak"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("new file got a backup: %v", err)
	}
}

func TestSaveRefusesToCreateOverExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.yaml")
	os.WriteFile(path, []byte("original\n"), 0o644)
	if err := Save(path, []byte("new\n"), ""); !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
	if got := readFile(t, path); got != "original\n" {
		t.Errorf("file was modified: %q", got)
	}
}

func TestSaveReplacesMatchingVersionAndKeepsBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.yaml")
	os.WriteFile(path, []byte("v1\n"), 0o600)
	_, hash, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(path, []byte("v2\n"), hash); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := readFile(t, path); got != "v2\n" {
		t.Errorf("content = %q, want v2", got)
	}
	if got := readFile(t, path+".bak"); got != "v1\n" {
		t.Errorf("backup = %q, want v1", got)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 preserved", info.Mode().Perm())
	}
}

func TestSaveDetectsConcurrentChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.yaml")
	os.WriteFile(path, []byte("v1\n"), 0o644)
	_, hash, _ := Load(path)
	os.WriteFile(path, []byte("edited by hand\n"), 0o644)
	if err := Save(path, []byte("v2\n"), hash); !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
	if got := readFile(t, path); got != "edited by hand\n" {
		t.Errorf("hand edit was overwritten: %q", got)
	}
}

func TestSaveDetectsDeletedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.yaml")
	if err := Save(path, []byte("v2\n"), Hash([]byte("v1\n"))); !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

func TestSaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	if err := Save(path, []byte("v1\n"), ""); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, []byte("v2\n"), Hash([]byte("v1\n"))); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 2 || names[0] != "compose.yaml" || names[1] != "compose.yaml.bak" {
		t.Errorf("dir contains %v, want [compose.yaml compose.yaml.bak]", names)
	}
}

func TestSaveIntoMissingDirectoryFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "compose.yaml")
	if err := Save(path, []byte("x\n"), ""); err == nil {
		t.Error("want error for missing directory")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/composefile/`
Expected: FAIL — build error: undefined: Save / Load / Hash / ErrConflict

- [ ] **Step 3: Implement**

Create `internal/composefile/disk.go`:

```go
package composefile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrConflict means the file on disk is not the version the caller loaded:
// it was changed (or created) by someone else in the meantime.
var ErrConflict = errors.New("compose file changed on disk since it was loaded")

const newFileMode fs.FileMode = 0o644

// Hash identifies a file version for optimistic concurrency checks.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Load reads a compose file and returns its content and version hash.
func Load(path string) ([]byte, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	return data, Hash(data), nil
}

// Save writes data to path atomically. expectedHash must be the Hash of the
// version the caller loaded, or "" to create a file that must not exist yet;
// otherwise ErrConflict is returned and nothing is written. The previous
// version is kept as path + ".bak". The file's permissions are preserved.
//
// The check and the write are not atomic with respect to other processes;
// callers serialize saves of the same stack.
func Save(path string, data []byte, expectedHash string) error {
	mode := newFileMode
	current, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if expectedHash != "" {
			return ErrConflict
		}
	case err != nil:
		return err
	default:
		if expectedHash == "" || Hash(current) != expectedHash {
			return ErrConflict
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		mode = info.Mode().Perm()
		if err := writeAtomic(path+".bak", current, mode); err != nil {
			return fmt.Errorf("write backup: %w", err)
		}
	}
	return writeAtomic(path, data, mode)
}

// writeAtomic writes to a temporary file in the same directory, syncs it and
// renames it over path, so readers see either the old or the new content.
func writeAtomic(path string, data []byte, mode fs.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Chmod(mode); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}

// syncDir makes the rename durable across a crash.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/composefile/ -v`
Expected: PASS for every test in the package (including tests of earlier, already-merged tasks).

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/composefile/disk.go internal/composefile/disk_test.go
git commit -F - <<'EOF'
feat: save compose files atomically with backup and conflict check

A crash mid-write must never leave a truncated compose file, and a save based
on a stale copy (hand edit, second tab) must not silently overwrite changes.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
EOF
```

---

### Task 4: Semantic verification of edits

**Wave:** 2 (parallel with Task 5)

**Files:**
- Create: `internal/composefile/verify.go`
- Test: `internal/composefile/verify_test.go`

**Interfaces:**
- Consumes: `edit`, `applyEdits` (Task 2); test helpers `path`, `scalar` (Task 2's edit_test.go).
- Produces:
  ```go
  var errVerification error
  func verifyEdits(orig, edited []byte, edits []edit) error
  ```

- [ ] **Step 1: Write the failing tests**

Create `internal/composefile/verify_test.go`:

```go
package composefile

import (
	"errors"
	"testing"
)

func TestVerifyAcceptsExactChange(t *testing.T) {
	orig := []byte("services:\n  web:\n    image: nginx\n    restart: always\n")
	edits := []edit{
		{path("services.web.image"), scalar("nginx:1.28")},
		{path("services.web.restart"), nil},
		{path("services.web.deploy.resources.limits.memory"), scalar("1G")},
	}
	edited := []byte("services:\n  web:\n    image: nginx:1.28\n    deploy:\n      resources:\n        limits:\n          memory: 1G\n")
	if err := verifyEdits(orig, edited, edits); err != nil {
		t.Errorf("verifyEdits: %v", err)
	}
}

func TestVerifyRejectsCollateralChange(t *testing.T) {
	orig := []byte("services:\n  web:\n    image: nginx\n    restart: always\n")
	edits := []edit{{path("services.web.image"), scalar("nginx:1.28")}}
	edited := []byte("services:\n  web:\n    image: nginx:1.28\n") // restart lost
	if err := verifyEdits(orig, edited, edits); !errors.Is(err, errVerification) {
		t.Errorf("err = %v, want errVerification", err)
	}
}

func TestVerifyRejectsInvalidYAML(t *testing.T) {
	orig := []byte("services: {}\n")
	if err := verifyEdits(orig, []byte("services: [\n"), nil); !errors.Is(err, errVerification) {
		t.Errorf("err = %v, want errVerification", err)
	}
}

func TestVerifyCatchesDeleteOfInheritedKey(t *testing.T) {
	// Deleting a key that only exists through a merge key is a no-op for the
	// text editor, so the decoded result still has it: verification must fail.
	orig := []byte("x-base: &base\n  restart: always\nservices:\n  web:\n    <<: *base\n    image: nginx\n")
	edits := []edit{{path("services.web.restart"), nil}}
	edited, err := applyEdits(orig, edits)
	if err != nil {
		t.Fatalf("applyEdits: %v", err)
	}
	if err := verifyEdits(orig, edited, edits); !errors.Is(err, errVerification) {
		t.Errorf("err = %v, want errVerification", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/composefile/`
Expected: FAIL — build error: undefined: verifyEdits / errVerification

- [ ] **Step 3: Implement**

Create `internal/composefile/verify.go`:

```go
package composefile

import (
	"errors"
	"fmt"
	"reflect"

	"go.yaml.in/yaml/v3"
)

var errVerification = errors.New("edited file does not match the intended change")

// verifyEdits checks that edited means exactly what orig means with edits
// applied: both are decoded (resolving anchors and merge keys), the edits are
// applied to the decoded original, and the results must be deeply equal. It
// guards against any text-splicing mistake changing something else.
func verifyEdits(orig, edited []byte, edits []edit) error {
	var want, got any
	if err := yaml.Unmarshal(orig, &want); err != nil {
		return fmt.Errorf("decode original: %w", err)
	}
	if err := yaml.Unmarshal(edited, &got); err != nil {
		return fmt.Errorf("%w: result is not valid YAML: %v", errVerification, err)
	}
	for _, e := range edits {
		var v any
		if e.value != nil {
			if err := e.value.Decode(&v); err != nil {
				return fmt.Errorf("decode edit value: %w", err)
			}
		}
		want = applyToValue(want, e.path, e.value != nil, v)
	}
	if !reflect.DeepEqual(want, got) {
		return errVerification
	}
	return nil
}

// applyToValue sets (or deletes) path in a decoded YAML value, creating
// intermediate mappings as needed when setting.
func applyToValue(node any, path []string, set bool, v any) any {
	m, ok := node.(map[string]any)
	if !ok {
		if !set {
			return node
		}
		m = map[string]any{}
	}
	if len(path) == 1 {
		if set {
			m[path[0]] = v
		} else {
			delete(m, path[0])
		}
		return m
	}
	child, exists := m[path[0]]
	if !exists && !set {
		return m
	}
	m[path[0]] = applyToValue(child, path[1:], set, v)
	return m
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/composefile/ -v`
Expected: PASS for every test in the package (including tests of earlier, already-merged tasks).

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/composefile/verify.go internal/composefile/verify_test.go
git commit -F - <<'EOF'
feat: verify each compose edit changes exactly what was intended

Text splicing is the riskiest part of the package; decoding both versions and
comparing them with the intended change catches any splice that alters
something else, including values inherited through merge keys.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
EOF
```

---

### Task 5: Node builders and new-file rendering

**Wave:** 2 (parallel with Task 4)

**Files:**
- Create: `internal/composefile/build.go`
- Test: `internal/composefile/build_test.go`

**Interfaces:**
- Consumes: `ServiceConfig` and friends, `Parse`, `Service`, test helper `mustParse` (Task 1); `encode` (Task 2).
- Produces:
  ```go
  var ErrInvalidServiceName error
  func New(name string, cfg ServiceConfig) ([]byte, error)
  func normalize(cfg ServiceConfig) ServiceConfig
  func strNode(v string) *yaml.Node
  func quotedNode(v string) *yaml.Node
  func listNode(items []string, item func(string) *yaml.Node) *yaml.Node
  func buildNode(b Build) *yaml.Node
  func environmentNode(vars []EnvVar, asList bool) *yaml.Node
  func networksNode(nets []NetworkAttachment, asMap bool) *yaml.Node
  func healthcheckNode(h Healthcheck) *yaml.Node
  ```

- [ ] **Step 1: Write the failing tests**

Create `internal/composefile/build_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/composefile/`
Expected: FAIL — build error: undefined: New / normalize / ErrInvalidServiceName

- [ ] **Step 3: Implement**

Create `internal/composefile/build.go`:

```go
package composefile

import (
	"errors"
	"strconv"

	"go.yaml.in/yaml/v3"
)

var ErrInvalidServiceName = errors.New("service name must not be empty")

// strNode is a string scalar; the !!str tag makes the encoder quote values
// such as "true" or "8080" that would otherwise read back as other types.
func strNode(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }

func quotedNode(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Style: yaml.DoubleQuotedStyle}
}

func intNode(n int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(n)}
}

func emptyMapNode() *yaml.Node { return &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle} }

// mapNode builds a mapping from alternating key strings and value nodes,
// skipping pairs whose value is nil.
func mapNode(pairs ...any) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode}
	for i := 0; i+1 < len(pairs); i += 2 {
		if v := pairs[i+1].(*yaml.Node); v != nil {
			m.Content = append(m.Content, strNode(pairs[i].(string)), v)
		}
	}
	return m
}

func listNode(items []string, item func(string) *yaml.Node) *yaml.Node {
	s := &yaml.Node{Kind: yaml.SequenceNode}
	for _, it := range items {
		s.Content = append(s.Content, item(it))
	}
	return s
}

// optional returns a node for v, or nil when v is empty.
func optional(v string, node func(string) *yaml.Node) *yaml.Node {
	if v == "" {
		return nil
	}
	return node(v)
}

func buildNode(b Build) *yaml.Node {
	if b.Dockerfile == "" {
		return strNode(b.Context)
	}
	return mapNode("context", strNode(b.Context), "dockerfile", strNode(b.Dockerfile))
}

func environmentNode(vars []EnvVar, asList bool) *yaml.Node {
	if asList {
		s := &yaml.Node{Kind: yaml.SequenceNode}
		for _, v := range vars {
			s.Content = append(s.Content, strNode(v.Name+"="+v.Value))
		}
		return s
	}
	m := &yaml.Node{Kind: yaml.MappingNode}
	for _, v := range vars {
		m.Content = append(m.Content, strNode(v.Name), strNode(v.Value))
	}
	return m
}

// networksNode uses the list form unless an attachment has a static IP or
// asMap asks to keep an existing mapping form.
func networksNode(nets []NetworkAttachment, asMap bool) *yaml.Node {
	for _, n := range nets {
		asMap = asMap || n.IPv4Address != ""
	}
	if !asMap {
		s := &yaml.Node{Kind: yaml.SequenceNode}
		for _, n := range nets {
			s.Content = append(s.Content, strNode(n.Name))
		}
		return s
	}
	m := &yaml.Node{Kind: yaml.MappingNode}
	for _, n := range nets {
		opts := emptyMapNode()
		if n.IPv4Address != "" {
			opts = mapNode("ipv4_address", strNode(n.IPv4Address))
		}
		m.Content = append(m.Content, strNode(n.Name), opts)
	}
	return m
}

func healthcheckNode(h Healthcheck) *yaml.Node {
	var retries *yaml.Node
	if h.Retries != 0 {
		retries = intNode(h.Retries)
	}
	var test *yaml.Node
	if len(h.Test) > 0 {
		test = listNode(h.Test, quotedNode)
		test.Style = yaml.FlowStyle
	}
	return mapNode(
		"test", test,
		"interval", optional(h.Interval, strNode),
		"timeout", optional(h.Timeout, strNode),
		"start_period", optional(h.StartPeriod, strNode),
		"retries", retries,
	)
}

func limitsNode(l Limits) *yaml.Node {
	return mapNode("cpus", optional(l.CPUs, quotedNode), "memory", optional(l.Memory, strNode))
}

// serviceNode renders a whole service in Stackstead's canonical key order.
func serviceNode(cfg ServiceConfig) *yaml.Node {
	cfg = normalize(cfg)
	var build, ports, volumes, env, nets, deploy, health *yaml.Node
	if cfg.Build != nil {
		build = buildNode(*cfg.Build)
	}
	if len(cfg.Ports) > 0 {
		ports = listNode(cfg.Ports, quotedNode)
	}
	if len(cfg.Volumes) > 0 {
		volumes = listNode(cfg.Volumes, strNode)
	}
	if len(cfg.Environment) > 0 {
		env = environmentNode(cfg.Environment, false)
	}
	if len(cfg.Networks) > 0 {
		nets = networksNode(cfg.Networks, false)
	}
	if cfg.Limits != (Limits{}) {
		deploy = mapNode("resources", mapNode("limits", limitsNode(cfg.Limits)))
	}
	if cfg.Healthcheck != nil {
		health = healthcheckNode(*cfg.Healthcheck)
	}
	return mapNode(
		"image", optional(cfg.Image, strNode),
		"build", build,
		"restart", optional(cfg.Restart, strNode),
		"ports", ports,
		"volumes", volumes,
		"environment", env,
		"networks", nets,
		"network_mode", optional(cfg.NetworkMode, strNode),
		"deploy", deploy,
		"healthcheck", health,
	)
}

// normalize maps every "not set" representation to one canonical zero value,
// so configs from forms (empty slices) compare equal to configs read from files (nil).
func normalize(cfg ServiceConfig) ServiceConfig {
	if len(cfg.Ports) == 0 {
		cfg.Ports = nil
	}
	if len(cfg.Volumes) == 0 {
		cfg.Volumes = nil
	}
	if len(cfg.Environment) == 0 {
		cfg.Environment = nil
	}
	if len(cfg.Networks) == 0 {
		cfg.Networks = nil
	}
	if cfg.Build != nil && *cfg.Build == (Build{}) {
		cfg.Build = nil
	}
	if cfg.Healthcheck != nil {
		h := *cfg.Healthcheck
		if len(h.Test) == 0 {
			h.Test = nil
		}
		if h.Test == nil && h.Interval == "" && h.Timeout == "" && h.StartPeriod == "" && h.Retries == 0 {
			cfg.Healthcheck = nil
		} else {
			cfg.Healthcheck = &h
		}
	}
	return cfg
}

// New renders a new compose file holding one service. Networks the service
// joins are declared external: Stackstead attaches services to networks that
// already exist on the host.
func New(name string, cfg ServiceConfig) ([]byte, error) {
	if name == "" {
		return nil, ErrInvalidServiceName
	}
	cfg = normalize(cfg)
	root := mapNode("services", mapNode(name, serviceNode(cfg)))
	if len(cfg.Networks) > 0 {
		nets := &yaml.Node{Kind: yaml.MappingNode}
		for _, n := range cfg.Networks {
			nets.Content = append(nets.Content, strNode(n.Name), mapNode("external", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}))
		}
		root.Content = append(root.Content, strNode("networks"), nets)
	}
	return []byte(encode(root, 2) + "\n"), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/composefile/ -v`
Expected: PASS for every test in the package (including tests of earlier, already-merged tasks).

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/composefile/build.go internal/composefile/build_test.go
git commit -F - <<'EOF'
feat: render new single-service compose files

Plan 3 creates stacks from the form; new files are rendered with the same
node builders the editor uses, so created and edited files share one shape.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
EOF
```

---

### Task 6: UpdateService

**Wave:** 3 (last)

**Files:**
- Create: `internal/composefile/update.go`
- Test: `internal/composefile/update_test.go`

**Interfaces:**
- Consumes: `Service`, `serviceNode`, `mappingValue` (Task 1); `edit`, `applyEdits` (Task 2); `verifyEdits` (Task 4); `normalize` and node builders (Task 5); test helpers `mustParse`, `labCompose`.
- Produces:
  ```go
  type ReadOnlyFieldError struct{ Field Field }
  func (f *File) UpdateService(name string, cfg ServiceConfig) (*File, error)
  ```

- [ ] **Step 1: Write the failing tests**

Create `internal/composefile/update_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/composefile/`
Expected: FAIL — build error: undefined: UpdateService / ReadOnlyFieldError

- [ ] **Step 3: Implement**

Create `internal/composefile/update.go`:

```go
package composefile

import (
	"fmt"
	"reflect"
	"slices"

	"go.yaml.in/yaml/v3"
)

// ReadOnlyFieldError reports an attempt to change a field that can only be
// edited as raw YAML (see Service.ReadOnly).
type ReadOnlyFieldError struct {
	Field Field
}

func (e *ReadOnlyFieldError) Error() string {
	return fmt.Sprintf("field %q can only be edited as raw YAML", e.Field)
}

// UpdateService returns a new File in which service name has the settings in
// cfg. Only fields whose value changed are rewritten, each as a minimal text
// edit; the result is verified to differ from the original in exactly those
// fields before it is returned.
func (f *File) UpdateService(name string, cfg ServiceConfig) (*File, error) {
	cur, err := f.Service(name)
	if err != nil {
		return nil, err
	}
	node, err := f.serviceNode(name)
	if err != nil {
		return nil, err
	}
	old, cfg := normalize(cur.Config), normalize(cfg)
	at := func(keys ...string) []string { return append([]string{"services", name}, keys...) }

	var edits []edit
	for _, fd := range []struct {
		field    Field
		old, new any
		edits    func() []edit
	}{
		{FieldImage, old.Image, cfg.Image, func() []edit { return scalarEdit(at("image"), cfg.Image, strNode) }},
		{FieldBuild, old.Build, cfg.Build, func() []edit {
			if cfg.Build == nil {
				return []edit{{at("build"), nil}}
			}
			return []edit{{at("build"), buildNode(*cfg.Build)}}
		}},
		{FieldPorts, old.Ports, cfg.Ports, func() []edit { return listEdit(at("ports"), cfg.Ports, quotedNode) }},
		{FieldVolumes, old.Volumes, cfg.Volumes, func() []edit { return listEdit(at("volumes"), cfg.Volumes, strNode) }},
		{FieldEnvironment, old.Environment, cfg.Environment, func() []edit {
			if len(cfg.Environment) == 0 {
				return []edit{{at("environment"), nil}}
			}
			current := mappingValue(node, "environment")
			asList := current != nil && current.Kind == yaml.SequenceNode
			return []edit{{at("environment"), environmentNode(cfg.Environment, asList)}}
		}},
		{FieldNetworks, old.Networks, cfg.Networks, func() []edit {
			if len(cfg.Networks) == 0 {
				return []edit{{at("networks"), nil}}
			}
			current := mappingValue(node, "networks")
			asMap := current != nil && current.Kind == yaml.MappingNode
			return []edit{{at("networks"), networksNode(cfg.Networks, asMap)}}
		}},
		{FieldNetworkMode, old.NetworkMode, cfg.NetworkMode, func() []edit {
			return scalarEdit(at("network_mode"), cfg.NetworkMode, strNode)
		}},
		{FieldLimits, old.Limits, cfg.Limits, func() []edit { return limitsEdits(node, at, old.Limits, cfg.Limits) }},
		{FieldRestart, old.Restart, cfg.Restart, func() []edit { return scalarEdit(at("restart"), cfg.Restart, strNode) }},
		{FieldHealthcheck, old.Healthcheck, cfg.Healthcheck, func() []edit {
			if cfg.Healthcheck == nil {
				return []edit{{at("healthcheck"), nil}}
			}
			return []edit{{at("healthcheck"), healthcheckNode(*cfg.Healthcheck)}}
		}},
	} {
		if reflect.DeepEqual(fd.old, fd.new) {
			continue
		}
		for _, ro := range cur.ReadOnly {
			if ro == fd.field {
				return nil, &ReadOnlyFieldError{Field: fd.field}
			}
		}
		edits = append(edits, fd.edits()...)
	}
	if len(edits) == 0 {
		return f, nil
	}

	out, err := applyEdits(f.src, edits)
	if err != nil {
		return nil, err
	}
	if err := verifyEdits(f.src, out, edits); err != nil {
		return nil, err
	}
	return Parse(out)
}

func scalarEdit(path []string, v string, node func(string) *yaml.Node) []edit {
	if v == "" {
		return []edit{{path, nil}}
	}
	return []edit{{path, node(v)}}
}

func listEdit(path []string, items []string, item func(string) *yaml.Node) []edit {
	if len(items) == 0 {
		return []edit{{path, nil}}
	}
	return []edit{{path, listNode(items, item)}}
}

// limitsEdits sets or removes deploy.resources.limits.{cpus,memory}. When
// every limit is cleared it deletes the outermost mapping that existed only to
// hold them, so no empty "deploy:" shell is left behind.
func limitsEdits(svc *yaml.Node, at func(...string) []string, old, new Limits) []edit {
	limitPath := func(key string) []string { return at("deploy", "resources", "limits", key) }
	if new == (Limits{}) {
		deploy := mappingValue(svc, "deploy")
		resources := mappingValue(deploy, "resources")
		limits := mappingValue(resources, "limits")
		switch {
		case hasOtherKeys(limits, "cpus", "memory"):
			return []edit{{limitPath("cpus"), nil}, {limitPath("memory"), nil}}
		case hasOtherKeys(resources, "limits"):
			return []edit{{at("deploy", "resources", "limits"), nil}}
		case hasOtherKeys(deploy, "resources"):
			return []edit{{at("deploy", "resources"), nil}}
		default:
			return []edit{{at("deploy"), nil}}
		}
	}
	var edits []edit
	if old.CPUs != new.CPUs {
		edits = append(edits, scalarEdit(limitPath("cpus"), new.CPUs, quotedNode)...)
	}
	if old.Memory != new.Memory {
		edits = append(edits, scalarEdit(limitPath("memory"), new.Memory, strNode)...)
	}
	return edits
}

// hasOtherKeys reports whether mapping m has any key not in keys.
func hasOtherKeys(m *yaml.Node, keys ...string) bool {
	if m == nil {
		return false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if !slices.Contains(keys, m.Content[i].Value) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/composefile/ -v`
Expected: PASS for every test in the package (including tests of earlier, already-merged tasks).

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/composefile/update.go internal/composefile/update_test.go
git commit -F - <<'EOF'
feat: apply form edits to compose services as minimal verified changes

Only fields whose value changed are rewritten, read-only fields are refused,
and clearing every limit removes the empty deploy shell while keeping other
deploy settings.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
EOF
```

---

### Final check (controller, after Wave 3)

Run: `go build ./... && go vet ./... && test -z "$(gofmt -l .)" && go test -race ./... && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...`
Expected: all packages ok.
