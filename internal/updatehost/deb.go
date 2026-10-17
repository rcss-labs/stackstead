package updatehost

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rcss-labs/stackstead/internal/update"
)

const debQueryFormat = "${Package}\t${Architecture}\t${Version}\t${Status}\n"
const debArtifactFormat = "${Package}\t${Architecture}\t${Version}\n"

var debStableVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

type debAdapter struct {
	runner CommandRunner
	verify func(update.VerifiedArtifact) error
	locks  func() error
	// Task 15 must wire the concrete root-private lifecycle proof from composition
	// inside updatehost. Validation is read-only and requires no active context.
	managed func(context.Context, update.Plan, update.VerifiedArtifact) error
}

func NewDeb(runner CommandRunner) PackageAdapter {
	return &debAdapter{runner: runner, verify: update.VerifyArtifact, locks: debLocks}
}
func (d *debAdapter) registration(p update.Plan) error {
	r := p.Registration
	if d.runner == nil || d.verify == nil || d.locks == nil || r.Installer != update.InstallerDeb || r.Binary != "/usr/bin/stackstead" || r.Prefix != "/usr" || r.Identity.OS != "linux" || (r.Identity.Arch != "amd64" && r.Identity.Arch != "arm64") || p.Current.Identity != r.Identity || len(r.Instances) == 0 {
		return update.ErrSetupRequired
	}
	for _, id := range []update.Identity{p.Current.Identity, p.Target.Identity} {
		if id.OS != "linux" || id.Arch != r.Identity.Arch || !debStableVersion.MatchString(id.Version) {
			return update.ErrIncompatible
		}
	}
	for _, in := range r.Instances {
		if in.UID == 0 || !instanceID.MatchString(in.ID) {
			return update.ErrSetupRequired
		}
	}
	return nil
}
func (d *debAdapter) artifactIdentity(p update.Plan, a update.VerifiedArtifact) (update.Identity, error) {
	if a == p.TargetArtifact && a != p.PreviousArtifact {
		return p.Target.Identity, nil
	}
	if a == p.PreviousArtifact && a != p.TargetArtifact {
		return p.Current.Identity, nil
	}
	return update.Identity{}, update.ErrUnauthorized
}
func (d *debAdapter) run(ctx context.Context, path string, args ...string) (string, error) {
	out, _, err := d.runner.Run(ctx, 0, 0, path, args, nil)
	if err != nil {
		return "", err
	}
	if len(out) > commandOutputLimit {
		return "", update.ErrIncompatible
	}
	return string(out), nil
}
func debFields(out string, n int) ([]string, error) {
	fields := strings.Split(strings.TrimSuffix(out, "\n"), "\t")
	if len(fields) != n || strings.Contains(strings.TrimSuffix(out, "\n"), "\n") {
		return nil, update.ErrIncompatible
	}
	return fields, nil
}
func (d *debAdapter) state(ctx context.Context, p update.Plan, rollback bool) (update.Identity, error) {
	out, err := d.run(ctx, "/usr/bin/dpkg-query", "-S", "--", p.Registration.Binary)
	if err != nil {
		return update.Identity{}, errors.Join(update.ErrSetupRequired, err)
	}
	if out != "stackstead: /usr/bin/stackstead\n" {
		return update.Identity{}, update.ErrIncompatible
	}
	out, err = d.run(ctx, "/usr/bin/dpkg-query", "--show", "--showformat="+debQueryFormat, "--", "stackstead")
	if err != nil {
		return update.Identity{}, errors.Join(update.ErrRecoveryRequired, err)
	}
	fields, err := debFields(out, 4)
	if err != nil || fields[0] != "stackstead" || fields[1] != p.Registration.Identity.Arch {
		return update.Identity{}, update.ErrIncompatible
	}
	if fields[3] != "install ok installed" {
		// Only a managed reinstall of the recorded previous artifact can recover
		// a known failed target. Unknown versions/states require operator repair.
		partial := fields[3] == "install ok unpacked" || fields[3] == "install ok half-configured" || fields[3] == "install reinstreq half-installed"
		if !rollback || !partial || fields[2] != p.Target.Identity.Version+"-1" {
			return update.Identity{}, update.ErrRecoveryRequired
		}
	}
	for _, id := range []update.Identity{p.Current.Identity, p.Target.Identity} {
		if fields[2] == id.Version+"-1" {
			return id, nil
		}
	}
	return update.Identity{}, update.ErrIncompatible
}
func (d *debAdapter) Validate(ctx context.Context, p update.Plan, a update.VerifiedArtifact) error {
	if err := d.registration(p); err != nil {
		return err
	}
	expected, err := d.artifactIdentity(p, a)
	if err != nil {
		return err
	}
	if !debArtifactPath(a.Path) {
		return update.ErrSetupRequired
	}
	if err = d.verify(a); err != nil {
		return err
	}
	if err = d.locks(); err != nil {
		return err
	}
	out, err := d.run(ctx, "/usr/bin/dpkg-deb", "--show", "--showformat="+debArtifactFormat, a.Path)
	if err != nil {
		return errors.Join(update.ErrIncompatible, err)
	}
	fields, err := debFields(out, 3)
	if err != nil || fields[0] != "stackstead" || fields[1] != expected.Arch || fields[2] != expected.Version+"-1" {
		return update.ErrIncompatible
	}
	if _, err = d.state(ctx, p, a == p.PreviousArtifact); err != nil {
		return err
	}
	// Read-only preflight must discover native refusals before service stop.
	_, err = d.run(ctx, "/usr/bin/dpkg", "--dry-run", "--force-confdef", "--force-confold", "--install", a.Path)
	return err
}
func (d *debAdapter) Install(ctx context.Context, p update.Plan, a update.VerifiedArtifact) error {
	if _, err := d.artifactIdentity(p, a); err != nil {
		return err
	}
	if d.managed == nil {
		return update.ErrUnavailable
	}
	if err := d.managed(ctx, p, a); err != nil {
		return err
	}
	if err := d.Validate(ctx, p, a); err != nil {
		return err
	}
	// Recheck bytes and native locks immediately before the irreversible command.
	// The native tool takes its own lock; never hold a parent lock across the child.
	if err := d.verify(a); err != nil {
		return err
	}
	if err := d.locks(); err != nil {
		return err
	}
	if err := d.managed(ctx, p, a); err != nil {
		return err
	}
	if _, err := d.run(ctx, "/usr/bin/dpkg", "--force-confdef", "--force-confold", "--install", a.Path); err != nil {
		return errors.Join(update.ErrRecoveryRequired, err)
	}
	got, err := d.state(ctx, p, false)
	expected, _ := d.artifactIdentity(p, a)
	if err != nil || got != expected {
		return errors.Join(update.ErrRecoveryRequired, err)
	}
	return nil
}
func (d *debAdapter) Inspect(ctx context.Context, p update.Plan) (update.Identity, error) {
	if err := d.registration(p); err != nil {
		return update.Identity{}, err
	}
	native, err := d.state(ctx, p, false)
	if err != nil {
		return update.Identity{}, err
	}
	id, err := inspectExecutable(ctx, d.runner, p)
	if err != nil {
		return update.Identity{}, err
	}
	if id != native {
		return update.Identity{}, update.ErrIncompatible
	}
	return id, nil
}
func debArtifactPath(path string) bool {
	const root = "/var/lib/stackstead-updater/staging/"
	return filepath.IsAbs(path) && filepath.Clean(path) == path && strings.HasPrefix(path, root) && strings.HasSuffix(path, ".deb")
}
