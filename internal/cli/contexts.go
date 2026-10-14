package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Context is one saved server login. Session is a credential: it lives only
// in the 0600 contexts file and is never printed or logged.
type Context struct {
	URL       string    `json:"url"`
	Username  string    `json:"username"`
	Session   string    `json:"session"`
	ExpiresAt time.Time `json:"expiresAt"`
	CAFile    string    `json:"caFile,omitempty"`
	Insecure  bool      `json:"insecure,omitempty"`
}

// Contexts is the whole contexts file.
type Contexts struct {
	Version  int                `json:"version"`
	Current  string             `json:"current"`
	Contexts map[string]Context `json:"contexts"`
}

// ContextStore reads and writes the contexts file.
type ContextStore struct{ Path string }

const contextsVersion = 1

// newContextStore locates contexts.json: Env.ConfigDir, else
// $XDG_CONFIG_HOME/stackstead, else $HOME/.config/stackstead.
func newContextStore(env *Env) *ContextStore {
	dir := env.ConfigDir
	if dir == "" {
		if x := env.Getenv("XDG_CONFIG_HOME"); x != "" {
			dir = filepath.Join(x, "stackstead")
		} else {
			home := env.Getenv("HOME")
			if home == "" {
				home, _ = os.UserHomeDir()
			}
			dir = filepath.Join(home, ".config", "stackstead")
		}
	}
	return &ContextStore{Path: filepath.Join(dir, "contexts.json")}
}

// Load returns the saved contexts; a missing file is an empty set. A corrupt
// file is an error naming the path (its content is never echoed).
func (s *ContextStore) Load() (Contexts, error) {
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return Contexts{Version: contextsVersion, Contexts: map[string]Context{}}, nil
	}
	if err != nil {
		return Contexts{}, exitf(ExitGeneral, "read contexts file %s: %v", s.Path, errors.Unwrap(err))
	}
	var c Contexts
	if err := json.Unmarshal(b, &c); err != nil {
		return Contexts{}, exitf(ExitGeneral, "contexts file %s is corrupt (fix or remove it)", s.Path)
	}
	if c.Version != contextsVersion {
		return Contexts{}, exitf(ExitGeneral, "contexts file %s has unsupported version %d", s.Path, c.Version)
	}
	if c.Contexts == nil {
		c.Contexts = map[string]Context{}
	}
	return c, nil
}

// Save writes the file atomically: a 0600 temp file in the same directory,
// then a rename, so a crash never leaves a half-written or loose file.
func (s *ContextStore) Save(c Contexts) error {
	c.Version = contextsVersion
	if c.Contexts == nil {
		c.Contexts = map[string]Context{}
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return exitf(ExitGeneral, "create %s: %v", dir, errors.Unwrap(err))
	}
	tmp, err := os.CreateTemp(dir, ".contexts-*.tmp") // created 0600
	if err != nil {
		return exitf(ExitGeneral, "write contexts file: %v", errors.Unwrap(err))
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return exitf(ExitGeneral, "write contexts file: %v", errors.Unwrap(err))
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return exitf(ExitGeneral, "write contexts file: %v", errors.Unwrap(err))
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return exitf(ExitGeneral, "write contexts file: %v", errors.Unwrap(err))
	}
	if err := tmp.Close(); err != nil {
		return exitf(ExitGeneral, "write contexts file: %v", errors.Unwrap(err))
	}
	if err := os.Rename(tmp.Name(), s.Path); err != nil {
		return exitf(ExitGeneral, "write contexts file %s: %v", s.Path, errors.Unwrap(err))
	}
	return os.Chmod(s.Path, 0o600)
}

// Update loads, applies fn and saves; an error from fn saves nothing.
func (s *ContextStore) Update(fn func(*Contexts) error) error {
	c, err := s.Load()
	if err != nil {
		return err
	}
	if err := fn(&c); err != nil {
		return err
	}
	return s.Save(c)
}

// Warnings reports a contexts file that other users can read.
func (s *ContextStore) Warnings(env *Env) []string {
	fi, err := os.Stat(s.Path)
	if err != nil || fi.Mode().Perm()&0o077 == 0 {
		return nil
	}
	return []string{fmt.Sprintf("contexts file %s is readable by others (mode %04o); run chmod 600 %s",
		s.Path, fi.Mode().Perm(), s.Path)}
}

// resolveContext picks the named context, or the current one.
func resolveContext(c Contexts, name string) (string, Context, error) {
	if name == "" {
		name = c.Current
	}
	if name == "" {
		return "", Context{}, exitf(ExitAuth, "no context: run stackstead login <url>")
	}
	ctx, ok := c.Contexts[name]
	if !ok {
		return "", Context{}, exitf(ExitNotFound, "unknown context %q (see stackstead context ls)", name)
	}
	return name, ctx, nil
}

var contextNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// validateContextName enforces [a-z0-9][a-z0-9-]*, at most 32 characters.
func validateContextName(name string) error {
	if len(name) > 32 || !contextNameRE.MatchString(name) {
		return exitf(ExitUsage, "invalid context name %q: use lowercase letters, digits and hyphens, at most 32 characters, starting with a letter or digit", name)
	}
	return nil
}

// defaultContextName is the URL's hostname with dots replaced by hyphens.
func defaultContextName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.ReplaceAll(u.Hostname(), ".", "-"))
}
