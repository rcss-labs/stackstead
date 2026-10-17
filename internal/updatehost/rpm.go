package updatehost

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rcss-labs/stackstead/internal/update"
)

const rpmTool = "/usr/bin/rpm"
const rpmMetadataFormat = "%{NAME}\t%{ARCH}\t%{VERSION}\t%{RELEASE}\t%{EPOCHNUM}\n"
const rpmStateFormat = "%{NAME}\t%{ARCH}\t%{VERSION}\t%{RELEASE}\t%{EPOCHNUM}\t[%{FILESTATES}]\n"

// --eval adds its own newline; the optional lock macro falls back in RPM itself.
const rpmPathsFormat = "%{_dbpath}\t%{?_rpmlock_path}"

type rpmAdapter struct {
	runner CommandRunner
	verify func(update.VerifiedArtifact) error
	locks  func(string, string) error
	// Task 15 supplies the root-private lifecycle proof bound to this exact plan
	// and artifact. Read-only validation must work before that context is active.
	managed func(context.Context, update.Plan, update.VerifiedArtifact) error
}

func NewRPM(runner CommandRunner) PackageAdapter {
	return &rpmAdapter{runner: runner, verify: update.VerifyArtifact, locks: rpmLocks}
}
func (r *rpmAdapter) registration(p update.Plan) error {
	if r.runner == nil || r.verify == nil || r.locks == nil {
		return update.ErrSetupRequired
	}
	return nativeRegistration(p, update.InstallerRPM)
}
func (r *rpmAdapter) run(ctx context.Context, args ...string) (string, error) {
	return nativeCommand(ctx, r.runner, rpmTool, args...)
}
func rpmArch(arch string) string {
	if arch == "amd64" {
		return "x86_64"
	}
	if arch == "arm64" {
		return "aarch64"
	}
	return ""
}
func rpmMatches(fields []string, id update.Identity) bool {
	return len(fields) >= 5 && fields[0] == "stackstead" && fields[1] == rpmArch(id.Arch) && fields[2] == id.Version && fields[3] == "1" && fields[4] == "0"
}
func (r *rpmAdapter) state(ctx context.Context, p update.Plan) (update.Identity, error) {
	owner, err := r.run(ctx, "-qf", "--queryformat="+rpmMetadataFormat, "--", p.Registration.Binary)
	if err != nil {
		return update.Identity{}, errors.Join(update.ErrRecoveryRequired, err)
	}
	out, err := r.run(ctx, "-q", "--queryformat="+rpmStateFormat, "--", "stackstead")
	if err != nil {
		return update.Identity{}, errors.Join(update.ErrRecoveryRequired, err)
	}
	fields, err := nativeFields(out, 6)
	// Multiple installed headers, missing/replaced files and unknown versions are
	// unprovable partial states. Never attempt generic RPM database repair.
	if err != nil || fields[5] == "" || strings.Trim(fields[5], "0") != "" || owner != strings.Join(fields[:5], "\t")+"\n" {
		return update.Identity{}, update.ErrRecoveryRequired
	}
	for _, id := range []update.Identity{p.Current.Identity, p.Target.Identity} {
		if rpmMatches(fields, id) {
			return id, nil
		}
	}
	return update.Identity{}, update.ErrRecoveryRequired
}
func (r *rpmAdapter) nativeLocks(ctx context.Context) error {
	out, err := r.run(ctx, "--eval", rpmPathsFormat)
	if err != nil {
		return errors.Join(update.ErrSetupRequired, err)
	}
	fields, err := nativeFields(out, 2)
	if err != nil || (fields[0] != "/var/lib/rpm" && fields[0] != "/usr/lib/sysimage/rpm") {
		return update.ErrSetupRequired
	}
	// An absent macro leaves a compiled fallback that cannot be proven from
	// mutable macros. Refuse it until a supported native fixture establishes it.
	if fields[1] != "/var/lib/rpm/.rpm.lock" && fields[1] != "/usr/lib/sysimage/rpm/.rpm.lock" {
		return update.ErrSetupRequired
	}
	return r.locks(fields[0], fields[1])
}
func rpmUpgradeArgs(p update.Plan, a update.VerifiedArtifact, test bool) []string {
	args := []string{}
	if test {
		args = append(args, "--test")
	}
	args = append(args, "--upgrade")
	if a == p.PreviousArtifact {
		args = append(args, "--oldpackage", "--replacepkgs")
	}
	return append(args, a.Path)
}

// Stable numeric triplets are compared without an RPM macro or external tool.
// Reject noncanonical/overflow versions, matching the release client's domain.
func rpmNewer(target, current string) bool {
	var t, c [3]uint64
	for i, s := range []string{target, current} {
		parts := strings.Split(s, ".")
		if len(parts) != 3 {
			return false
		}
		for j, v := range parts {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil || strconv.FormatUint(n, 10) != v {
				return false
			}
			if i == 0 {
				t[j] = n
			} else {
				c[j] = n
			}
		}
	}
	for i := range t {
		if t[i] != c[i] {
			return t[i] > c[i]
		}
	}
	return false
}
func (r *rpmAdapter) Validate(ctx context.Context, p update.Plan, a update.VerifiedArtifact) error {
	if err := r.registration(p); err != nil {
		return err
	}
	expected, err := nativeArtifactIdentity(p, a)
	if err != nil {
		return err
	}
	if !rpmNewer(p.Target.Identity.Version, p.Current.Identity.Version) {
		return update.ErrIncompatible
	}
	if !rpmArtifactPath(a.Path) {
		return update.ErrSetupRequired
	}
	if err = r.verify(a); err != nil {
		return err
	}
	if err = r.nativeLocks(ctx); err != nil {
		return err
	}
	out, err := r.run(ctx, "-qp", "--queryformat="+rpmMetadataFormat, "--", a.Path)
	if err != nil {
		return errors.Join(update.ErrIncompatible, err)
	}
	fields, err := nativeFields(out, 5)
	if err != nil || !rpmMatches(fields, expected) {
		return update.ErrIncompatible
	}
	if _, err = r.state(ctx, p); err != nil {
		return err
	}
	_, err = r.run(ctx, rpmUpgradeArgs(p, a, true)...)
	return err
}
func (r *rpmAdapter) Install(ctx context.Context, p update.Plan, a update.VerifiedArtifact) error {
	if _, err := nativeArtifactIdentity(p, a); err != nil {
		return err
	}
	if r.managed == nil {
		return update.ErrUnavailable
	}
	if err := r.managed(ctx, p, a); err != nil {
		return err
	}
	if err := r.Validate(ctx, p, a); err != nil {
		return err
	}
	if err := r.verify(a); err != nil {
		return err
	}
	if err := r.nativeLocks(ctx); err != nil {
		return err
	}
	if err := r.managed(ctx, p, a); err != nil {
		return err
	}
	// RPM acquires its own native lock; the parent never holds it over the child.
	if _, err := r.run(ctx, rpmUpgradeArgs(p, a, false)...); err != nil {
		return errors.Join(update.ErrRecoveryRequired, err)
	}
	got, err := r.state(ctx, p)
	expected, _ := nativeArtifactIdentity(p, a)
	if err != nil || got != expected {
		return errors.Join(update.ErrRecoveryRequired, err)
	}
	return nil
}
func (r *rpmAdapter) Inspect(ctx context.Context, p update.Plan) (update.Identity, error) {
	if err := r.registration(p); err != nil {
		return update.Identity{}, err
	}
	native, err := r.state(ctx, p)
	if err != nil {
		return update.Identity{}, err
	}
	id, err := inspectExecutable(ctx, r.runner, p)
	if err != nil || id != native {
		return update.Identity{}, errors.Join(update.ErrRecoveryRequired, err)
	}
	return id, nil
}
func rpmArtifactPath(path string) bool {
	const root = "/var/lib/stackstead-updater/staging/"
	return filepath.IsAbs(path) && filepath.Clean(path) == path && strings.HasPrefix(path, root) && strings.HasSuffix(path, ".rpm")
}
