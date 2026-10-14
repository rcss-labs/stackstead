package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sample() Contexts {
	return Contexts{Version: 1, Current: "prod", Contexts: map[string]Context{
		"prod": {URL: "https://s.example", Username: "admin", Session: "SESSION-VALUE", ExpiresAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
	}}
}

func TestContextsFileIs0600(t *testing.T) {
	e := newTestEnv(t, nil)
	e.ConfigDir = filepath.Join(e.ConfigDir, "nested", "stackstead")
	s := newContextStore(e.Env)
	if err := s.Save(sample()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(s.Path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file %v %v", fi, err)
	}
	di, _ := os.Stat(filepath.Dir(s.Path))
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", di.Mode().Perm())
	}
	if filepath.Base(s.Path) != "contexts.json" {
		t.Fatal(s.Path)
	}
	b, _ := os.ReadFile(s.Path)
	if !strings.HasSuffix(string(b), "\n") || !strings.Contains(string(b), `"version": 1`) && !strings.Contains(string(b), `"version":1`) {
		t.Errorf("content %s", b)
	}
}

func TestSaveIsAtomicAndKeepsMode(t *testing.T) {
	e := newTestEnv(t, nil)
	s := newContextStore(e.Env)
	if err := os.WriteFile(s.Path, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(sample()); err != nil {
		t.Fatal(err)
	}
	// A pre-existing loose file is tightened.
	_ = os.Chmod(s.Path, 0o644)
	if err := s.Save(sample()); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(s.Path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(s.Path))
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
	before, _ := os.ReadFile(s.Path)
	err := s.Update(func(c *Contexts) error {
		c.Current = "changed"
		return os.ErrInvalid
	})
	if err == nil {
		t.Fatal("fn error must propagate")
	}
	after, _ := os.ReadFile(s.Path)
	if string(before) != string(after) {
		t.Fatal("failing fn must leave the file untouched")
	}
	if err := s.Update(func(c *Contexts) error { c.Current = "other"; return nil }); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Load()
	if got.Current != "other" || got.Contexts["prod"].Session != "SESSION-VALUE" {
		t.Fatalf("%+v", got)
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	s := newContextStore(newTestEnv(t, nil).Env)
	c, err := s.Load()
	if err != nil || c.Version != 1 || len(c.Contexts) != 0 || c.Current != "" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestLoadCorruptErrorsNamesPath(t *testing.T) {
	s := newContextStore(newTestEnv(t, nil).Env)
	if err := os.WriteFile(s.Path, []byte(`{"version":1, "contexts": {"a": {"session": "TOPSECRET"`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load()
	if err == nil || !strings.Contains(err.Error(), s.Path) || codeFor(err) != ExitGeneral {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), "TOPSECRET") {
		t.Error("error leaks file content")
	}
	if err := s.Update(func(*Contexts) error { return nil }); err == nil {
		t.Fatal("Update must not overwrite a corrupt file")
	}
	b, _ := os.ReadFile(s.Path)
	if !strings.Contains(string(b), "TOPSECRET") {
		t.Fatal("corrupt file was overwritten")
	}
}

func TestLoadUnknownVersionAndExtraFields(t *testing.T) {
	s := newContextStore(newTestEnv(t, nil).Env)
	_ = os.WriteFile(s.Path, []byte(`{"version":2}`), 0o600)
	if _, err := s.Load(); err == nil || codeFor(err) != ExitGeneral {
		t.Fatalf("%v", err)
	}
	_ = os.WriteFile(s.Path, []byte(`{"version":1,"future":true,"current":"a","contexts":{"a":{"url":"u","extra":1}}}`), 0o600)
	c, err := s.Load()
	if err != nil || c.Contexts["a"].URL != "u" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestWarningsOnLoosePerms(t *testing.T) {
	e := newTestEnv(t, nil)
	s := newContextStore(e.Env)
	if w := s.Warnings(e.Env); len(w) != 0 {
		t.Fatalf("missing file: %v", w)
	}
	_ = os.WriteFile(s.Path, []byte(`{"version":1}`), 0o600)
	if w := s.Warnings(e.Env); len(w) != 0 {
		t.Fatalf("0600: %v", w)
	}
	_ = os.Chmod(s.Path, 0o644)
	w := s.Warnings(e.Env)
	if len(w) != 1 || !strings.Contains(w[0], s.Path) || !strings.Contains(w[0], "0644") || !strings.Contains(w[0], "chmod 600") {
		t.Fatalf("%v", w)
	}
}

func TestResolveContextDefaultsAndErrors(t *testing.T) {
	c := sample()
	name, ctx, err := resolveContext(c, "")
	if err != nil || name != "prod" || ctx.URL != "https://s.example" {
		t.Fatalf("%v %v %v", name, ctx, err)
	}
	if _, _, err := resolveContext(c, "nope"); codeFor(err) != ExitNotFound {
		t.Fatalf("%v", err)
	}
	_, _, err = resolveContext(Contexts{Version: 1}, "")
	if codeFor(err) != ExitAuth || !strings.Contains(err.Error(), "stackstead login <url>") {
		t.Fatalf("%v", err)
	}
	if _, _, err := resolveContext(Contexts{Version: 1, Current: "gone"}, ""); codeFor(err) != ExitNotFound {
		t.Fatalf("dangling current: %v", err)
	}
}

func TestXDGConfigHomeHonoured(t *testing.T) {
	xdg, home := t.TempDir(), t.TempDir()
	e := newTestEnv(t, map[string]string{"XDG_CONFIG_HOME": xdg, "HOME": home})
	e.ConfigDir = ""
	if got, want := newContextStore(e.Env).Path, filepath.Join(xdg, "stackstead", "contexts.json"); got != want {
		t.Errorf("got %s want %s", got, want)
	}
	e = newTestEnv(t, map[string]string{"HOME": home})
	e.ConfigDir = ""
	if got, want := newContextStore(e.Env).Path, filepath.Join(home, ".config", "stackstead", "contexts.json"); got != want {
		t.Errorf("got %s want %s", got, want)
	}
	e.ConfigDir = "/explicit"
	if got := newContextStore(e.Env).Path; got != "/explicit/contexts.json" {
		t.Errorf("got %s", got)
	}
}

func TestContextNames(t *testing.T) {
	for _, ok := range []string{"a", "prod-1", "0x", strings.Repeat("a", 32)} {
		if err := validateContextName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-a", "A", "a_b", "a.b", strings.Repeat("a", 33)} {
		if err := validateContextName(bad); codeFor(err) != ExitUsage {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if got := defaultContextName("https://Stack.example.com:8420/x"); got != "stack-example-com" {
		t.Errorf("got %q", got)
	}
}
