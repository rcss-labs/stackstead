package update

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func canonicalAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/"
}
func ValidateRegistration(reg Registration) error {
	if reg.SchemaVersion != 1 || !safeID.MatchString(reg.ID) || !canonicalAbsolute(reg.Prefix) || !canonicalAbsolute(reg.Binary) || !strings.HasPrefix(reg.Binary, reg.Prefix+"/") {
		return ErrIncompatible
	}
	if _, err := stableVersion(reg.Identity.Version); err != nil {
		return err
	}
	if len(reg.Identity.Commit) != 40 && len(reg.Identity.Commit) != 64 {
		return ErrIncompatible
	}
	if _, err := hex.DecodeString(reg.Identity.Commit); err != nil {
		return ErrIncompatible
	}
	switch reg.Identity.OS {
	case "linux":
		if reg.Installer != InstallerScript && reg.Installer != InstallerDeb && reg.Installer != InstallerRPM {
			return ErrIncompatible
		}
	case "darwin":
		if reg.Installer != InstallerScript && reg.Installer != InstallerPkg {
			return ErrIncompatible
		}
	default:
		return ErrIncompatible
	}
	if reg.Identity.Arch != "amd64" && reg.Identity.Arch != "arm64" || len(reg.Instances) == 0 {
		return ErrIncompatible
	}
	ids := map[string]bool{}
	configs := map[string]bool{}
	sockets := map[string]bool{}
	for _, in := range reg.Instances {
		if !safeID.MatchString(in.ID) || ids[in.ID] || configs[in.ConfigPath] || sockets[in.ControlSocket] || !canonicalAbsolute(in.ConfigPath) || !canonicalAbsolute(in.DataDir) || !canonicalAbsolute(in.ControlSocket) {
			return ErrIncompatible
		}
		ids[in.ID] = true
		configs[in.ConfigPath] = true
		sockets[in.ControlSocket] = true
		if in.Supervisor.Name == "" || strings.ContainsAny(in.Supervisor.Name, "\x00\n\r/") {
			return ErrIncompatible
		}
		switch in.Supervisor.Kind {
		case "systemd", "launchd":
			if in.Supervisor.AdapterPath != "" {
				return ErrIncompatible
			}
		case "custom":
			if !canonicalAbsolute(in.Supervisor.AdapterPath) {
				return ErrIncompatible
			}
		default:
			return ErrIncompatible
		}
	}
	return nil
}
func ReadRegistration(root, id string) (Registration, error) {
	if !safeID.MatchString(id) {
		return Registration{}, ErrUnauthorized
	}
	s, err := productionStore(root)
	if err != nil {
		return Registration{}, err
	}
	return readRegistration(s, id)
}
func readRegistration(s *trustStore, id string) (Registration, error) {
	if !safeID.MatchString(id) {
		return Registration{}, ErrUnauthorized
	}
	b, err := s.read("registrations", id+".json")
	if err != nil {
		return Registration{}, err
	}
	var reg Registration
	if err = json.Unmarshal(b, &reg); err != nil {
		return Registration{}, ErrIncompatible
	}
	if reg.ID != id {
		return Registration{}, ErrIncompatible
	}
	if err = ValidateRegistration(reg); err != nil {
		return Registration{}, err
	}
	return reg, nil
}
func WriteRegistration(root string, reg Registration) error {
	if err := ValidateRegistration(reg); err != nil {
		return err
	}
	j, err := OpenJournal(root)
	if err != nil {
		return err
	}
	return j.writeRegistration(reg)
}
func (j *Journal) writeRegistration(reg Registration) error {
	if err := ValidateRegistration(reg); err != nil {
		return err
	}
	lock, err := j.TryLock(reg.ID)
	if err != nil {
		return err
	}
	defer lock.Close()
	b, err := json.Marshal(reg)
	if err != nil {
		return err
	}
	return j.store.atomic("registrations", reg.ID+".json", b, 0600)
}

type ClientLocator struct {
	InstallationID string `json:"installationID"`
	InstanceID     string `json:"instanceID"`
	Socket         string `json:"socket"`
	ConfigPath     string `json:"configPath"`
	Enabled        bool   `json:"enabled"`
}

func validateLocator(l ClientLocator) error {
	if !safeID.MatchString(l.InstallationID) || !safeID.MatchString(l.InstanceID) || !canonicalAbsolute(l.Socket) || !canonicalAbsolute(l.ConfigPath) {
		return ErrIncompatible
	}
	return nil
}
func locatorStore() *trustStore { return &trustStore{root: ClientLocatorRoot, publicDirs: true} }
func ReadClientLocator(configPath string, uid uint32) (ClientLocator, error) {
	return readClientLocator(locatorStore(), configPath, uid)
}
func readClientLocator(s *trustStore, configPath string, uid uint32) (ClientLocator, error) {
	if !canonicalAbsolute(configPath) {
		return ClientLocator{}, ErrSetupRequired
	}
	canonical, err := filepath.EvalSymlinks(configPath)
	if err != nil {
		return ClientLocator{}, fmt.Errorf("%w: canonical config unavailable", ErrSetupRequired)
	}
	dir := strconv.FormatUint(uint64(uid), 10)
	d, err := s.directory(dir, false)
	if err != nil {
		return ClientLocator{}, err
	}
	defer d.Close()
	names, err := d.Readdirnames(1025)
	if err != nil && len(names) == 0 {
		return ClientLocator{}, ErrSetupRequired
	}
	if len(names) > 1024 {
		return ClientLocator{}, ErrIncompatible
	}
	var selected ClientLocator
	found := false
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") {
			return ClientLocator{}, ErrIncompatible
		}
		// Use the already-validated directory descriptor, never reopen via an app path.
		f, e := s.openFile(d, name, 0, 0, false)
		if e != nil {
			return ClientLocator{}, e
		}
		b, e := readBounded(f)
		f.Close()
		if e != nil {
			return ClientLocator{}, e
		}
		var l ClientLocator
		if json.Unmarshal(b, &l) != nil || validateLocator(l) != nil || name != l.InstanceID+".json" {
			return ClientLocator{}, ErrIncompatible
		}
		if l.ConfigPath != canonical {
			continue
		}
		if found {
			return ClientLocator{}, ErrIncompatible
		}
		selected = l
		found = true
	}
	if !found {
		return ClientLocator{}, ErrSetupRequired
	}
	return selected, nil
}
func WriteClientLocator(l ClientLocator, uid uint32) error {
	return writeClientLocator(locatorStore(), l, uid)
}
func writeClientLocator(s *trustStore, l ClientLocator, uid uint32) error {
	if err := validateLocator(l); err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(l.ConfigPath)
	if err != nil || canonical != l.ConfigPath {
		return ErrIncompatible
	}
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	return s.atomic(strconv.FormatUint(uint64(uid), 10), l.InstanceID+".json", b, 0644)
}
func readBounded(f *os.File) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(f, stateLimit+1))
	if len(b) > stateLimit {
		return nil, ErrIncompatible
	}
	return b, err
}
