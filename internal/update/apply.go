package update

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
)

type Plan struct {
	Kind    Kind
	Version Version // target
	// Current is the installed version; HaveCurrent is false for dev builds,
	// where no downgrade or reinstall can be inferred.
	Current                Version
	HaveCurrent            bool
	Asset, Prefix, BaseURL string
	Force, HaveDNF         bool
}

type Runner func(ctx context.Context, name string, args, env []string) error

var (
	ErrBusy            = errors.New("package manager is busy")
	ErrDowngradeManual = errors.New("package downgrades are not done by stackstead upgrade")
)

// DowngradeError carries the manual command for a refused package downgrade.
type DowngradeError struct{ Command string }

func (e *DowngradeError) Error() string {
	return "downgrading a package is not done by stackstead upgrade; run it by hand: " + e.Command
}

func (e *DowngradeError) Is(target error) bool { return target == ErrDowngradeManual }

// Commands is pure: it maps a plan to one fixed argv (no shell) and extra
// environment. Package downgrades are refused even with Force.
func Commands(p Plan) (argv []string, extraEnv []string, err error) {
	if !filepath.IsAbs(p.Asset) {
		return nil, nil, fmt.Errorf("installer asset %q is not an absolute path", p.Asset)
	}
	order := 0 // current relative to target: 1 means installed is newer
	if p.HaveCurrent {
		order = Compare(p.Current, p.Version)
	}
	downgrade, same := order > 0, p.HaveCurrent && order == 0
	switch p.Kind {
	case KindScript:
		if p.Prefix == "" {
			return nil, nil, ErrLayout
		}
		argv = []string{"sh", p.Asset, "--version", "v" + p.Version.String(), "--prefix", p.Prefix}
		if p.Force {
			argv = append(argv, "--force")
		}
		if p.BaseURL != "" {
			argv = append(argv, "--base-url", p.BaseURL)
		}
		return argv, nil, nil
	case KindDeb:
		if downgrade {
			return nil, nil, &DowngradeError{Command: "apt-get install -y --allow-downgrades " + shellQuote(p.Asset)}
		}
		argv = []string{"apt-get", "install", "-y"}
		if p.Force && same {
			argv = append(argv, "--reinstall")
		}
		return append(argv, p.Asset), []string{"DEBIAN_FRONTEND=noninteractive"}, nil
	case KindRPM:
		switch {
		case downgrade:
			return nil, nil, &DowngradeError{Command: "rpm -U --oldpackage " + shellQuote(p.Asset)}
		case p.Force && same:
			return []string{"rpm", "-U", "--replacepkgs", p.Asset}, nil, nil
		case p.HaveDNF:
			return []string{"dnf", "install", "-y", p.Asset}, nil, nil
		}
		return []string{"rpm", "-U", p.Asset}, nil, nil
	case KindPkg:
		return []string{"installer", "-pkg", p.Asset, "-target", "/"}, nil, nil
	}
	return nil, nil, fmt.Errorf("unknown installer kind %q", p.Kind)
}

// Apply runs the native installer once. ctx is handed to the runner unchanged.
func Apply(ctx context.Context, p Plan, run Runner, locked func() (bool, error)) error {
	argv, env, err := Commands(p)
	if err != nil {
		return err
	}
	if p.Kind == KindDeb && locked != nil {
		busy, err := locked()
		if err != nil {
			return fmt.Errorf("checking the dpkg lock: %w", err)
		}
		if busy {
			return ErrBusy
		}
	}
	return run(ctx, argv[0], argv[1:], env)
}
