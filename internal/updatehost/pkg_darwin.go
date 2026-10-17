package updatehost

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rcss-labs/stackstead/internal/update"
)

const pkgIdentifier = "io.github.rcss-labs.stackstead"
const pkgPrivateRoot = "/Library/Application Support/StacksteadUpdater"

// Task 13's trusted producer must prove a complete bounded canonical inventory
// without links/special files and inspect app code only after dropping credentials.
// Before stop and again immediately before installer uses paths, composition must
// prove actual destination ancestry and stability with no-follow/mode/Darwin ACL
// checks. Writable ancestry cannot be made safe by a descriptor installer cannot
// use; refuse setup when this cannot be proved. Lexical inventory is not that proof.
// These values describe evidence from the exact verified artifact, not a blessing.
type pkgExecutableProof struct {
	Identity      update.Identity
	Architectures [2]string
	Slices        [2]update.Identity
}
type pkgPayloadProof struct {
	Artifact                             update.VerifiedArtifact
	Identifier, Version, InstallLocation string
	App                                  pkgExecutableProof
	Helper, Bootstrap                    pkgExecutableProof
	PayloadPaths                         []string
	Metadata                             []payloadMetadataProof
}
type pkgPayloadVerifier func(context.Context, update.Plan, update.VerifiedArtifact) (pkgPayloadProof, error)
type pkgAdapter struct {
	runner       CommandRunner
	verify       func(update.VerifiedArtifact) error
	payload      pkgPayloadVerifier
	destinations func(context.Context, pkgPayloadProof) error
	// Task 16 must prove suppression of user setup/helper replacement for all
	// registered UIDs, bound to this plan and the selected target/rollback artifact.
	// Composition must also prove destination stability immediately before the
	// native installer command, which resolves paths rather than anchored FDs.
	managed func(context.Context, update.Plan, update.VerifiedArtifact) error
}

func NewPKG(runner CommandRunner) PackageAdapter {
	return &pkgAdapter{runner: runner, verify: update.VerifyArtifact, destinations: provePKGDestinations}
}
func (d *pkgAdapter) registration(p update.Plan) error {
	r := p.Registration
	if d.runner == nil || d.verify == nil || r.Installer != update.InstallerPkg || !r.UpdateEnabled || r.Prefix != "/usr/local" || r.Binary != "/usr/local/bin/stackstead" || r.Identity.OS != "darwin" || (r.Identity.Arch != "arm64" && r.Identity.Arch != "amd64") || p.Current.Identity != r.Identity || len(r.Instances) == 0 {
		return update.ErrSetupRequired
	}
	for _, in := range r.Instances {
		if in.UID == 0 || !instanceID.MatchString(in.ID) {
			return update.ErrSetupRequired
		}
	}
	for _, v := range []struct {
		c update.Candidate
		a update.VerifiedArtifact
	}{{p.Current, p.PreviousArtifact}, {p.Target, p.TargetArtifact}} {
		if v.c.Identity.OS != "darwin" || v.c.Identity.Arch != r.Identity.Arch || !nativeStableVersion.MatchString(v.c.Identity.Version) {
			return update.ErrIncompatible
		}
		if err := v.c.Manifest.ValidateIdentity(v.c.Identity); err != nil {
			return err
		}
		asset, err := v.c.Manifest.ResolveAsset(update.InstallerPkg, "darwin", r.Identity.Arch)
		if err != nil || asset != v.c.Asset || asset.Arch != "universal" || asset.Name != v.a.Name {
			return update.ErrIncompatible
		}
	}
	return update.ValidateCompatibility(p.Current.Manifest, p.Target.Manifest, p.Current.Manifest.TargetSchema)
}
func (d *pkgAdapter) run(ctx context.Context, path string, args ...string) (string, error) {
	out, stderr, err := d.runner.Run(ctx, 0, 0, path, args, nil)
	if err != nil {
		return "", err
	}
	if len(out) > commandOutputLimit || len(stderr) != 0 {
		return "", update.ErrIncompatible
	}
	return string(out), nil
}
func pkgArtifactPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && strings.HasPrefix(path, pkgPrivateRoot+"/staging/") && strings.HasSuffix(path, ".pkg") && !strings.ContainsAny(path, "\x00\r\n\t'\"`$")
}
func (d *pkgAdapter) prove(ctx context.Context, p update.Plan, a update.VerifiedArtifact) error {
	expected, err := nativeArtifactIdentity(p, a)
	if err != nil {
		return err
	}
	if !pkgArtifactPath(a.Path) {
		return update.ErrSetupRequired
	}
	if err = d.verify(a); err != nil {
		return err
	}
	if d.payload == nil {
		return update.ErrUnavailable
	}
	proof, err := d.payload(ctx, p, a)
	if err != nil {
		return err
	}
	if proof.Artifact != a || proof.Identifier != pkgIdentifier || proof.Version != expected.Version || proof.InstallLocation != "/" || proof.App.Identity != expected || proof.App.Architectures != [2]string{"amd64", "arm64"} || len(proof.PayloadPaths) == 0 || len(proof.PayloadPaths) > 4096 {
		return update.ErrIncompatible
	}
	for _, binary := range []pkgExecutableProof{proof.App, proof.Helper, proof.Bootstrap} {
		if binary.Identity != expected || binary.Architectures != [2]string{"amd64", "arm64"} {
			return update.ErrIncompatible
		}
		for i, arch := range []string{"amd64", "arm64"} {
			id := expected
			id.Arch = arch
			if binary.Slices[i] != id {
				return update.ErrIncompatible
			}
		}
	}
	metadata := map[string]payloadMetadataProof{}
	for _, m := range proof.Metadata {
		if m.Size != 163 || m.Attribute != "com.apple.provenance" || !pkgPayloadDestination(m.Companion) || m.Path != metadataPath(m.Companion) {
			return update.ErrIncompatible
		}
		if _, ok := metadata[m.Path]; ok {
			return update.ErrIncompatible
		}
		metadata[m.Path] = m
	}
	seen := map[string]bool{}
	size := 0
	for _, path := range proof.PayloadPaths {
		size += len(path)
		if size > commandOutputLimit || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n\t") || seen[path] || (!pkgPayloadDestination(path) && metadata[path].Path != path) {
			return update.ErrIncompatible
		}
		seen[path] = true
	}
	for _, m := range metadata {
		if !seen[m.Path] || !seen[m.Companion] {
			return update.ErrIncompatible
		}
	}
	if !seen[p.Registration.Binary] || !seen["/Library/PrivilegedHelperTools/stackstead-updater"] || !seen["/Library/PrivilegedHelperTools/stackstead-updater-bootstrap"] {
		return update.ErrIncompatible
	}
	if d.destinations == nil {
		return update.ErrUnavailable
	}
	return d.destinations(ctx, proof)
}

// Match only the current Makefile PKG layout, including its directory entries.
// A private-tree denylist cannot enumerate filesystem aliases (such as the Data
// volume). The helper leaves are fixed for the three-binary release layout;
// no arbitrary descendants or alternative spellings are accepted here.
func pkgPayloadDestination(path string) bool {
	switch path {
	case "/Library", "/Library/PrivilegedHelperTools", "/Library/PrivilegedHelperTools/stackstead-updater", "/Library/PrivilegedHelperTools/stackstead-updater-bootstrap", "/Library/LaunchDaemons", "/Library/LaunchDaemons/io.github.rcss-labs.stackstead-updater.plist",
		"/usr", "/usr/local", "/usr/local/bin", "/usr/local/bin/stackstead",
		"/usr/local/share", "/usr/local/share/stackstead",
		"/usr/local/share/stackstead/install.sh",
		"/usr/local/share/stackstead/launchagent.plist.in",
		"/usr/local/share/stackstead/config.example.yaml",
		"/usr/local/share/stackstead/LICENSE":
		return true
	}
	return false
}

func pkgFields(out string, keys ...string) (map[string]string, error) {
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != len(keys) {
		return nil, update.ErrIncompatible
	}
	values := map[string]string{}
	for i, line := range lines {
		prefix := keys[i] + ": "
		if !strings.HasPrefix(line, prefix) {
			return nil, update.ErrIncompatible
		}
		values[keys[i]] = strings.TrimPrefix(line, prefix)
	}
	return values, nil
}
func (d *pkgAdapter) state(ctx context.Context, p update.Plan) (update.Identity, error) {
	out, err := d.run(ctx, "/usr/sbin/pkgutil", "--pkg-info", pkgIdentifier)
	if err != nil {
		return update.Identity{}, errors.Join(update.ErrRecoveryRequired, err)
	}
	receipt, err := pkgFields(out, "package-id", "version", "volume", "location", "install-time")
	if err != nil || receipt["package-id"] != pkgIdentifier || receipt["volume"] != "/" || receipt["location"] != "/" {
		return update.Identity{}, update.ErrRecoveryRequired
	}
	if n, e := strconv.ParseUint(receipt["install-time"], 10, 64); e != nil || n == 0 {
		return update.Identity{}, update.ErrRecoveryRequired
	}
	out, err = d.run(ctx, "/usr/sbin/pkgutil", "--file-info", p.Registration.Binary)
	if err != nil {
		return update.Identity{}, errors.Join(update.ErrRecoveryRequired, err)
	}
	owner, err := pkgFields(out, "volume", "path", "pkgid", "pkg-version")
	if err != nil || owner["volume"] != "/" || owner["path"] != p.Registration.Binary || owner["pkgid"] != pkgIdentifier || owner["pkg-version"] != receipt["version"] {
		return update.Identity{}, update.ErrRecoveryRequired
	}
	for _, id := range []update.Identity{p.Current.Identity, p.Target.Identity} {
		if id.Version == receipt["version"] {
			return id, nil
		}
	}
	return update.Identity{}, update.ErrRecoveryRequired
}
func (d *pkgAdapter) Validate(ctx context.Context, p update.Plan, a update.VerifiedArtifact) error {
	if _, err := nativeArtifactIdentity(p, a); err != nil {
		return err
	}
	if err := d.registration(p); err != nil {
		return err
	}
	// Both exact native packages must be provable before any application stop.
	for _, artifact := range []update.VerifiedArtifact{p.TargetArtifact, p.PreviousArtifact} {
		if err := d.prove(ctx, p, artifact); err != nil {
			return err
		}
	}
	_, err := d.state(ctx, p)
	return err
}
func pkgRepair(a update.VerifiedArtifact, err error) error {
	return fmt.Errorf("%w: native PKG state requires local administrator repair; after resolving the native failure, reinstall the recorded package with sudo /usr/sbin/installer -pkg '%s' -target /, then verify /usr/sbin/pkgutil --pkg-info %s: %v", update.ErrRecoveryRequired, a.Path, pkgIdentifier, err)
}
func (d *pkgAdapter) Install(ctx context.Context, p update.Plan, a update.VerifiedArtifact) error {
	expected, err := nativeArtifactIdentity(p, a)
	if err != nil {
		return err
	}
	if d.managed == nil {
		return update.ErrUnavailable
	}
	if err = d.managed(ctx, p, a); err != nil {
		return err
	}
	if err = d.Validate(ctx, p, a); err != nil {
		return err
	}
	if err = d.prove(ctx, p, a); err != nil {
		return err
	}
	if _, err = d.state(ctx, p); err != nil {
		return err
	}
	if err = d.managed(ctx, p, a); err != nil {
		return err
	}
	if err = d.prove(ctx, p, a); err != nil {
		return err
	}
	if _, err = d.run(ctx, "/usr/sbin/installer", "-pkg", a.Path, "-target", "/"); err != nil {
		return pkgRepair(a, err)
	}
	// A successful installer exit or a matching receipt alone cannot prove the app.
	got, err := d.Inspect(ctx, p)
	if err != nil || got != expected {
		return pkgRepair(a, err)
	}
	return nil
}
func (d *pkgAdapter) Inspect(ctx context.Context, p update.Plan) (update.Identity, error) {
	if err := d.registration(p); err != nil {
		return update.Identity{}, err
	}
	native, err := d.state(ctx, p)
	if err != nil {
		return update.Identity{}, err
	}
	id, err := inspectPlatformExecutable(ctx, d.runner, p, "darwin")
	if err != nil {
		return update.Identity{}, err
	}
	if id != native {
		return update.Identity{}, update.ErrIncompatible
	}
	return id, nil
}
