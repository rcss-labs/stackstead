package update

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ParseManifest accepts only the schema's exact field names, with no duplicate
// keys or trailing JSON. Missing legacy metadata never enables managed updates.
func ParseManifest(data []byte) (Manifest, error) {
	fields, err := manifestObject(data, "schemaVersion", "identity", "helperMin", "helperMax", "bootstrapProtocol", "journalProtocol", "sourceSchemaMin", "targetSchema", "assets")
	if err != nil {
		return Manifest{}, err
	}
	if _, err := manifestObject(fields["identity"], "version", "commit"); err != nil {
		return Manifest{}, err
	}
	var assets []json.RawMessage
	if err := json.Unmarshal(fields["assets"], &assets); err != nil {
		return Manifest{}, incompatible("invalid assets")
	}
	for _, asset := range assets {
		if _, err := manifestObject(asset, "os", "arch", "installer", "name"); err != nil {
			return Manifest{}, err
		}
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, incompatible("invalid manifest fields")
	}
	m.Identity.Version = strings.TrimPrefix(m.Identity.Version, "v")
	if err := m.validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func incompatible(reason string) error { return fmt.Errorf("%w: %s", ErrIncompatible, reason) }

// manifestObject checks exact keys because encoding/json otherwise accepts
// case-insensitive names and silently uses the last of duplicate fields.
func manifestObject(data []byte, names ...string) (map[string]json.RawMessage, error) {
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		allowed[name] = true
	}
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, incompatible("expected manifest object")
	}
	fields := make(map[string]json.RawMessage, len(names))
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, incompatible("malformed manifest object")
		}
		key, ok := token.(string)
		if !ok || !allowed[key] || fields[key] != nil {
			return nil, incompatible("unknown or duplicate manifest field")
		}
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, incompatible("invalid manifest field")
		}
		fields[key] = raw
	}
	if _, err := d.Token(); err != nil {
		return nil, incompatible("malformed manifest object")
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, incompatible("trailing manifest data")
	}
	if len(fields) != len(names) {
		return nil, incompatible("missing manifest field")
	}
	return fields, nil
}

func (m Manifest) validate() error {
	if m.SchemaVersion != 1 {
		return incompatible("unsupported manifest schema")
	}
	if m.HelperMin < 1 || m.HelperMax < m.HelperMin || m.HelperMin > HelperProtocol || m.HelperMax < HelperProtocol || m.BootstrapProtocol != BootstrapProtocol || m.JournalProtocol != JournalProtocol {
		return incompatible("unsupported updater protocols")
	}
	if m.SourceSchemaMin < 0 || m.TargetSchema < 1 || m.SourceSchemaMin > m.TargetSchema {
		return incompatible("invalid database schema range")
	}
	if _, err := stableVersion(m.Identity.Version); err != nil {
		return err
	}
	if len(m.Identity.Commit) != 40 && len(m.Identity.Commit) != 64 {
		return incompatible("release requires full source commit")
	}
	if _, err := hex.DecodeString(m.Identity.Commit); err != nil {
		return incompatible("invalid source commit")
	}
	if len(m.Assets) == 0 {
		return incompatible("missing release assets")
	}
	names := make(map[string]bool, len(m.Assets))
	platforms := make(map[string]bool, len(m.Assets))
	for _, asset := range m.Assets {
		if asset.Name == "" || asset.Name == "." || asset.Name == ".." || strings.ContainsAny(asset.Name, "/\\\x00\r\n\t ") {
			return incompatible("invalid asset name")
		}
		if asset.OS != "linux" && asset.OS != "darwin" {
			return incompatible("unsupported asset platform")
		}
		switch asset.Installer {
		case InstallerScript:
			if !concreteArch(asset.Arch) {
				return incompatible("unsupported executable architecture")
			}
		case InstallerDeb, InstallerRPM:
			if asset.OS != "linux" || !concreteArch(asset.Arch) {
				return incompatible("invalid Linux package platform")
			}
		case InstallerPkg:
			if asset.OS != "darwin" || asset.Arch != "universal" {
				return incompatible("pkg requires universal Darwin asset")
			}
		default:
			return incompatible("unsupported installer")
		}
		platform := asset.OS + "/" + asset.Arch + "/" + string(asset.Installer)
		if names[asset.Name] || platforms[platform] {
			return incompatible("duplicate or conflicting asset")
		}
		names[asset.Name], platforms[platform] = true, true
	}
	return nil
}

func concreteArch(arch string) bool { return arch == "amd64" || arch == "arm64" }

// stableVersion bounds numeric components to uint32 and refuses prerelease,
// build metadata, leading zeros and development labels.
func stableVersion(version string) ([3]uint64, error) {
	var result [3]uint64
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return result, incompatible("expected stable semantic version")
	}
	for i, part := range parts {
		if part == "" || len(part) > 10 || (len(part) > 1 && part[0] == '0') {
			return result, incompatible("invalid semantic version component")
		}
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return result, incompatible("expected stable semantic version")
			}
		}
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return result, incompatible("semantic version component out of range")
		}
		result[i] = n
	}
	return result, nil
}

// ValidateCompatibility requires a validated updater-enabled rollback release
// as well as a newer target that can migrate the installed database schema.
func ValidateCompatibility(current, target Manifest, schema int) error {
	if err := current.validate(); err != nil {
		return err
	}
	if err := target.validate(); err != nil {
		return err
	}
	if schema < current.SourceSchemaMin || schema > current.TargetSchema || schema < target.SourceSchemaMin || schema > target.TargetSchema {
		return incompatible("database schema outside release range")
	}
	before, _ := stableVersion(current.Identity.Version)
	after, _ := stableVersion(target.Identity.Version)
	for i := range before {
		if after[i] > before[i] {
			return nil
		}
		if after[i] < before[i] {
			return incompatible("target is older than installed release")
		}
	}
	return incompatible("target is already installed")
}

// ResolveAsset selects exactly one native artifact. Universal describes the
// package only; callers keep their concrete OS/arch in Candidate.Identity.
func (m Manifest) ResolveAsset(installer InstallerKind, os, arch string) (AssetSpec, error) {
	if err := m.validate(); err != nil {
		return AssetSpec{}, err
	}
	if !concreteArch(arch) {
		return AssetSpec{}, incompatible("expected concrete runtime architecture")
	}
	for _, asset := range m.Assets {
		if asset.Installer == installer && asset.OS == os && (asset.Arch == arch || (installer == InstallerPkg && asset.Arch == "universal")) {
			return asset, nil
		}
	}
	return AssetSpec{}, incompatible("missing platform installer asset")
}

// ValidateIdentity matches the installed executable to current release metadata.
// Asset selection by installer remains a separate check in ResolveAsset.
func (m Manifest) ValidateIdentity(installed Identity) error {
	if err := m.validate(); err != nil {
		return err
	}
	if strings.TrimPrefix(installed.Version, "v") != m.Identity.Version || installed.Commit != m.Identity.Commit || !concreteArch(installed.Arch) {
		return incompatible("executable differs from release identity")
	}
	for _, asset := range m.Assets {
		if asset.OS == installed.OS && (asset.Arch == installed.Arch || (asset.Installer == InstallerPkg && asset.Arch == "universal")) {
			return nil
		}
	}
	return incompatible("executable platform absent from release")
}

// CandidateID binds immutable release documents and the selected native artifact.
// Length prefixes prevent ambiguous boundaries even for untrusted input strings.
func CandidateID(metadataSHA256, sumsSHA256, assetName string) string {
	h := sha256.New()
	for _, part := range []string{metadataSHA256, sumsSHA256, assetName} {
		fmt.Fprintf(h, "%d:%s", len(part), part)
	}
	return hex.EncodeToString(h.Sum(nil))
}
