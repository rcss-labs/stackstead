package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/rcss-labs/stackstead/internal/config"
)

const defaultConfigWritePath = "/etc/stackstead/config.yaml"

func configCommand() *Command {
	var lf localFlags
	var initPath, listen, stacksDir, dataDir string
	var force bool
	return &Command{
		Name: "config", Short: "Create, validate and show the config file",
		Sub: []*Command{
			{Name: "init", Short: "Write a commented config file", Usage: "config init [--path P] [--listen A] [--stacks-dir D] [--data-dir D] [--force]",
				Flags: func(fs *flag.FlagSet) {
					fs.StringVar(&initPath, "path", defaultConfigWritePath, "where to write the config file")
					fs.StringVar(&listen, "listen", "", "listen address (default 127.0.0.1:8420)")
					fs.StringVar(&stacksDir, "stacks-dir", "", "stacks directory (default /opt/stacks)")
					fs.StringVar(&dataDir, "data-dir", "", "data directory (default /var/lib/stackstead)")
					fs.BoolVar(&force, "force", false, "overwrite an existing file")
				},
				Run: func(_ context.Context, env *Env, _ *globalFlags, _ []string) error {
					cfg := config.Defaults()
					if listen != "" {
						cfg.Listen = listen
					}
					if stacksDir != "" {
						cfg.StacksDir = stacksDir
					}
					if dataDir != "" {
						cfg.DataDir = dataDir
					}
					return configInit(env, cfg, initPath, force)
				}},
			{Name: "validate", Short: "Check the config file", Usage: "config validate [--config P]", Flags: lf.register,
				Long: "Applies STACKSTEAD_* environment overrides, exactly as serve does. Exit 0 valid, 6 invalid.",
				Run: func(_ context.Context, env *Env, _ *globalFlags, _ []string) error {
					_, path, err := loadLocalConfig(env, lf)
					if err != nil {
						return err
					}
					if path == "" {
						fmt.Fprintln(env.Stdout, "no config file; defaults and environment are valid")
					} else {
						fmt.Fprintf(env.Stdout, "config %s is valid\n", path)
					}
					return nil
				}},
			{Name: "show", Short: "Show effective settings and their sources", Usage: "config show [--config P]", Flags: lf.register,
				Run: func(_ context.Context, env *Env, g *globalFlags, _ []string) error {
					cfg, path, err := loadLocalConfig(env, lf)
					if err != nil {
						return err
					}
					settings, err := config.Effective(cfg, path, env.Getenv)
					if err != nil {
						return err
					}
					p := newPrinter(env, g)
					if p.JSON {
						type row struct {
							Key    string `json:"key"`
							Value  string `json:"value"`
							Source string `json:"source"`
						}
						rows := make([]row, len(settings))
						for i, s := range settings {
							rows[i] = row{s.Key, s.Value, s.Source}
						}
						return p.JSONValue(rows)
					}
					rows := make([][]string, len(settings))
					for i, s := range settings {
						rows[i] = []string{s.Key, s.Value, s.Source}
					}
					p.Table([]string{"key", "value", "source"}, rows)
					return nil
				}},
		},
	}
}

// configInit renders and validates the template, then writes it atomically.
func configInit(env *Env, cfg config.Config, path string, force bool) error {
	if _, err := os.Lstat(path); err == nil && !force {
		return exitf(ExitConflict, "%s already exists (use --force to overwrite)", path)
	}
	data, err := config.Template(cfg)
	if err != nil {
		return &ExitError{Code: ExitValidation, Err: err}
	}
	if err := validateRendered(data); err != nil {
		return &ExitError{Code: ExitValidation, Err: err}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if force {
		if err := os.Rename(tmp.Name(), path); err != nil {
			return err
		}
	} else if err := os.Link(tmp.Name(), path); err != nil { // never replaces what appeared since the check
		if errors.Is(err, fs.ErrExist) {
			return exitf(ExitConflict, "%s already exists (use --force to overwrite)", path)
		}
		return err
	}
	fmt.Fprintln(env.Stdout, path)
	return nil
}

// validateRendered runs the loader's own checks over the rendered file, with
// no environment so the host's STACKSTEAD_* variables cannot interfere.
func validateRendered(data []byte) error {
	dir, err := os.MkdirTemp("", "stackstead-config-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	tmp := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	_, err = config.Load(tmp, func(string) string { return "" })
	return err
}
