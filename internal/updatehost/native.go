package updatehost

import (
	"context"
	"regexp"
	"strings"

	"github.com/rcss-labs/stackstead/internal/update"
)

var nativeStableVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

func nativeRegistration(p update.Plan, kind update.InstallerKind) error {
	r := p.Registration
	if r.Installer != kind || r.Binary != "/usr/bin/stackstead" || r.Prefix != "/usr" || r.Identity.OS != "linux" || (r.Identity.Arch != "amd64" && r.Identity.Arch != "arm64") || p.Current.Identity != r.Identity || len(r.Instances) == 0 {
		return update.ErrSetupRequired
	}
	for _, id := range []update.Identity{p.Current.Identity, p.Target.Identity} {
		if id.OS != "linux" || id.Arch != r.Identity.Arch || !nativeStableVersion.MatchString(id.Version) {
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
func nativeArtifactIdentity(p update.Plan, a update.VerifiedArtifact) (update.Identity, error) {
	if a == p.TargetArtifact && a != p.PreviousArtifact {
		return p.Target.Identity, nil
	}
	if a == p.PreviousArtifact && a != p.TargetArtifact {
		return p.Current.Identity, nil
	}
	return update.Identity{}, update.ErrUnauthorized
}
func nativeCommand(ctx context.Context, runner CommandRunner, path string, args ...string) (string, error) {
	out, _, err := runner.Run(ctx, 0, 0, path, args, nil)
	if err != nil {
		return "", err
	}
	if len(out) > commandOutputLimit {
		return "", update.ErrIncompatible
	}
	return string(out), nil
}
func nativeFields(out string, n int) ([]string, error) {
	fields := strings.Split(strings.TrimSuffix(out, "\n"), "\t")
	if len(fields) != n || strings.Contains(strings.TrimSuffix(out, "\n"), "\n") {
		return nil, update.ErrIncompatible
	}
	return fields, nil
}
