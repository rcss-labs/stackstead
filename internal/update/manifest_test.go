package update

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func releaseManifest(version string) Manifest {
	return Manifest{SchemaVersion: 1, Identity: ReleaseIdentity{Version: version, Commit: strings.Repeat("a", 40)}, HelperMin: 1, HelperMax: 1, BootstrapProtocol: 1, JournalProtocol: 1, SourceSchemaMin: 1, TargetSchema: 2, Assets: []AssetSpec{
		{OS: "linux", Arch: "amd64", Installer: InstallerScript, Name: "stackstead_linux_amd64.tar.gz"},
		{OS: "linux", Arch: "arm64", Installer: InstallerScript, Name: "stackstead_linux_arm64.tar.gz"},
		{OS: "darwin", Arch: "amd64", Installer: InstallerScript, Name: "stackstead_darwin_amd64.tar.gz"},
		{OS: "darwin", Arch: "arm64", Installer: InstallerScript, Name: "stackstead_darwin_arm64.tar.gz"},
		{OS: "darwin", Arch: "universal", Installer: InstallerPkg, Name: "stackstead_darwin.pkg"},
	}}
}
func manifestJSON(t *testing.T, m Manifest) []byte {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestManifestRejectsUnknownOrMissingProtocols(t *testing.T) {
	cases := map[string]func(*Manifest){
		"schema":                 func(m *Manifest) { m.SchemaVersion = 2 },
		"missing helper":         func(m *Manifest) { m.HelperMin = 0 },
		"unknown helper":         func(m *Manifest) { m.HelperMin = 2; m.HelperMax = 2 },
		"reversed helper range":  func(m *Manifest) { m.HelperMin = 2; m.HelperMax = 1 },
		"missing bootstrap":      func(m *Manifest) { m.BootstrapProtocol = 0 },
		"unknown bootstrap":      func(m *Manifest) { m.BootstrapProtocol = 2 },
		"missing journal":        func(m *Manifest) { m.JournalProtocol = 0 },
		"unknown journal":        func(m *Manifest) { m.JournalProtocol = 2 },
		"negative source schema": func(m *Manifest) { m.SourceSchemaMin = -1 },
		"schema range":           func(m *Manifest) { m.SourceSchemaMin = 3 },
		"empty assets":           func(m *Manifest) { m.Assets = nil },
		"missing asset name":     func(m *Manifest) { m.Assets[0].Name = "" },
		"asset path":             func(m *Manifest) { m.Assets[0].Name = "../release.tar.gz" },
		"duplicate asset":        func(m *Manifest) { m.Assets = append(m.Assets, m.Assets[0]) },
		"conflicting platform":   func(m *Manifest) { a := m.Assets[0]; a.Name = "other.tar.gz"; m.Assets = append(m.Assets, a) },
		"conflicting name":       func(m *Manifest) { m.Assets[1].Name = m.Assets[0].Name },
		"unsupported platform":   func(m *Manifest) { m.Assets[0].OS = "windows" },
		"nonuniversal pkg":       func(m *Manifest) { m.Assets[4].Arch = "arm64" },
		"invalid installer":      func(m *Manifest) { m.Assets[0].Installer = "zip" },
		"pkg on linux":           func(m *Manifest) { m.Assets[4].OS = "linux" },
		"short commit":           func(m *Manifest) { m.Identity.Commit = "abc123" },
		"nonhex commit":          func(m *Manifest) { m.Identity.Commit = strings.Repeat("z", 40) },
		"prerelease":             func(m *Manifest) { m.Identity.Version = "1.0.0-rc.1" },
		"version overflow":       func(m *Manifest) { m.Identity.Version = "999999999999999999999999.0.0" },
		"leading zero":           func(m *Manifest) { m.Identity.Version = "01.0.0" },
		"build metadata":         func(m *Manifest) { m.Identity.Version = "1.0.0+local" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			m := releaseManifest("1.0.0")
			change(&m)
			if _, err := ParseManifest(manifestJSON(t, m)); !errors.Is(err, ErrIncompatible) {
				t.Fatalf("want ErrIncompatible, got %v", err)
			}
		})
	}
	good := string(manifestJSON(t, releaseManifest("1.0.0")))
	for name, data := range map[string]string{"malformed": "{", "unknown field": strings.Replace(good, `"schemaVersion":1`, `"schemaVersion":1,"unexpected":true`, 1), "nested unknown": strings.Replace(good, `"version":"1.0.0"`, `"version":"1.0.0","arch":"amd64"`, 1), "trailing": good + " {}", "absent v0.2 metadata": "", "duplicate field": strings.Replace(good, `"schemaVersion":1`, `"schemaVersion":1,"schemaVersion":1`, 1), "wrong case": strings.Replace(good, `"helperMin"`, `"HelperMin"`, 1)} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseManifest([]byte(data)); !errors.Is(err, ErrIncompatible) {
				t.Fatalf("want ErrIncompatible, got %v", err)
			}
		})
	}
}

func TestCompatibilityRequiresBothUpdaterEnabledReleases(t *testing.T) {
	for _, schema := range []int{1, 2} {
		if err := ValidateCompatibility(releaseManifest("1.0.0"), releaseManifest("1.1.0"), schema); err != nil {
			t.Errorf("compatible schema %d: %v", schema, err)
		}
	}
	cases := map[string]func(*Manifest, *Manifest, *int){
		"missing current":           func(c, t *Manifest, s *int) { *c = Manifest{} },
		"missing target":            func(c, t *Manifest, s *int) { *t = Manifest{} },
		"current protocol":          func(c, t *Manifest, s *int) { c.JournalProtocol = 2 },
		"target protocol":           func(c, t *Manifest, s *int) { t.BootstrapProtocol = 2 },
		"schema below target range": func(c, t *Manifest, s *int) { *s = 0 },
		"schema above target range": func(c, t *Manifest, s *int) { *s = 3 },
		"schema outside current":    func(c, t *Manifest, s *int) { c.SourceSchemaMin = 2; *s = 1 },
		"downgrade":                 func(c, t *Manifest, s *int) { t.Identity.Version = "0.9.0" },
		"same release":              func(c, t *Manifest, s *int) { t.Identity.Version = "1.0.0" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			c, target := releaseManifest("1.0.0"), releaseManifest("1.1.0")
			schema := 2
			change(&c, &target, &schema)
			if err := ValidateCompatibility(c, target, schema); !errors.Is(err, ErrIncompatible) {
				t.Fatalf("want ErrIncompatible, got %v", err)
			}
		})
	}
	// Numeric comparison must not order 1.10.0 before 1.9.0.
	if err := ValidateCompatibility(releaseManifest("1.9.0"), releaseManifest("1.10.0"), 2); err != nil {
		t.Error(err)
	}
}

func TestCandidateIDBindsAssetAndMetadata(t *testing.T) {
	base := CandidateID(strings.Repeat("a", 64), strings.Repeat("b", 64), "asset.tar.gz")
	if decoded, err := hex.DecodeString(base); err != nil || len(decoded) != 32 {
		t.Errorf("ID must be SHA256 hex: %q", base)
	}
	for _, id := range []string{CandidateID(strings.Repeat("c", 64), strings.Repeat("b", 64), "asset.tar.gz"), CandidateID(strings.Repeat("a", 64), strings.Repeat("c", 64), "asset.tar.gz"), CandidateID(strings.Repeat("a", 64), strings.Repeat("b", 64), "other.tar.gz")} {
		if id == base {
			t.Error("changing metadata/checksums/asset must change ID")
		}
	}
	if CandidateID("ab", "c", "d") == CandidateID("a", "bc", "d") {
		t.Error("ID encoding permits ambiguous field boundaries")
	}
	if got := CandidateID(strings.Repeat("a", 64), strings.Repeat("b", 64), "asset.tar.gz"); got != base {
		t.Error("ID is not deterministic")
	}
}

func TestManifestResolvesAllPlatforms(t *testing.T) {
	m, err := ParseManifest(manifestJSON(t, releaseManifest("v1.0.0")))
	if err != nil {
		t.Fatal(err)
	}
	if m.Identity.Version != "1.0.0" {
		t.Errorf("version not normalized: %q", m.Identity.Version)
	}
	for _, os := range []string{"linux", "darwin"} {
		for _, arch := range []string{"amd64", "arm64"} {
			asset, err := m.ResolveAsset(InstallerScript, os, arch)
			if err != nil {
				t.Errorf("%s/%s: %v", os, arch, err)
				continue
			}
			if asset.Name != "stackstead_"+os+"_"+arch+".tar.gz" {
				t.Errorf("wrong asset: %+v", asset)
			}
			identity := Identity{Version: "1.0.0", Commit: strings.Repeat("a", 40), OS: os, Arch: arch}
			if err := m.ValidateIdentity(identity); err != nil {
				t.Errorf("installed identity: %v", err)
			}
		}
	}
	for name, identity := range map[string]Identity{"wrong commit": {Version: "1.0.0", Commit: strings.Repeat("b", 40), OS: "linux", Arch: "amd64"}, "wrong version": {Version: "0.2.0", Commit: strings.Repeat("a", 40), OS: "linux", Arch: "amd64"}, "unsupported arch": {Version: "1.0.0", Commit: strings.Repeat("a", 40), OS: "linux", Arch: "386"}} {
		t.Run(name, func(t *testing.T) {
			if err := m.ValidateIdentity(identity); !errors.Is(err, ErrIncompatible) {
				t.Errorf("want incompatible identity, got %v", err)
			}
		})
	}
	if _, err := m.ResolveAsset(InstallerDeb, "linux", "amd64"); !errors.Is(err, ErrIncompatible) {
		t.Errorf("missing asset: %v", err)
	}
}

func TestUniversalPkgMatchesBothDarwinArchitectures(t *testing.T) {
	m := releaseManifest("1.0.0")
	m.Assets = m.Assets[4:]
	for _, arch := range []string{"amd64", "arm64"} {
		asset, err := m.ResolveAsset(InstallerPkg, "darwin", arch)
		if err != nil {
			t.Errorf("%s: %v", arch, err)
			continue
		}
		if asset.Arch != "universal" {
			t.Errorf("asset lost universal arch: %+v", asset)
		}
		identity := Identity{Version: "1.0.0", Commit: strings.Repeat("a", 40), OS: "darwin", Arch: arch}
		if err := m.ValidateIdentity(identity); err != nil {
			t.Errorf("concrete identity %s: %v", arch, err)
		}
	}
	if _, err := m.ResolveAsset(InstallerPkg, "darwin", "universal"); !errors.Is(err, ErrIncompatible) {
		t.Errorf("nonconcrete runtime arch: %v", err)
	}
}
