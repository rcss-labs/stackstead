package update

import (
	"os"
	"path/filepath"
	"testing"
)

func validTestRegistration() Registration {
	return Registration{SchemaVersion: 1, ID: "installation", Installer: InstallerScript, Prefix: "/usr/local", Binary: "/usr/local/bin/stackstead", Identity: Identity{Version: "1.2.3", Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", OS: "linux", Arch: "amd64"}, Instances: []Instance{{ID: "one", ConfigPath: "/etc/stackstead/config.yaml", DataDir: "/var/lib/stackstead", ControlSocket: "/run/stackstead.sock", Supervisor: SupervisorSpec{Kind: "systemd", Name: "stackstead.service"}}}}
}
func TestTrustStoreRegistrationRejectsAmbiguousInstances(t *testing.T) {
	reg := validTestRegistration()
	if err := ValidateRegistration(reg); err != nil {
		t.Fatal(err)
	}
	reg.Instances = append(reg.Instances, reg.Instances[0])
	if err := ValidateRegistration(reg); err == nil {
		t.Fatal("ambiguous instances accepted")
	}
}

func TestTrustStoreLocatorCanonicalUIDAndAmbiguity(t *testing.T) {
	j := testJournal(t)
	s := &trustStore{root: j.root, anchor: j.root, owner: uint32(os.Getuid()), publicDirs: true}
	config := filepath.Join(j.root, "config.yaml")
	if err := os.WriteFile(config, []byte("config"), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := filepath.EvalSymlinks(config)
	if err != nil {
		t.Fatal(err)
	}
	locator := ClientLocator{InstallationID: "install", InstanceID: "instance", Socket: "/run/control.sock", ConfigPath: config, Enabled: true}
	if err = writeClientLocator(s, locator, 42); err != nil {
		t.Fatal(err)
	}
	got, err := readClientLocator(s, config, 42)
	if err != nil || got != locator {
		t.Fatalf("canonical locator unavailable: %+v %v", got, err)
	}
	if _, err = readClientLocator(s, config, 43); err == nil {
		t.Fatal("wrong uid accepted")
	}
	info, err := os.Stat(filepath.Join(j.root, "42", "instance.json"))
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatalf("public locator mode: %v %v", info, err)
	}
	locator.InstanceID = "second"
	if err = writeClientLocator(s, locator, 42); err != nil {
		t.Fatal(err)
	}
	if _, err = readClientLocator(s, config, 42); err == nil {
		t.Fatal("ambiguous canonical config accepted")
	}
}
func TestTrustStoreLocatorRejectsWritableAndSymlink(t *testing.T) {
	for _, kind := range []string{"writable", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			j := testJournal(t)
			s := &trustStore{root: j.root, anchor: j.root, owner: uint32(os.Getuid()), publicDirs: true}
			config := filepath.Join(j.root, "config")
			if err := os.WriteFile(config, nil, 0600); err != nil {
				t.Fatal(err)
			}
			config, err := filepath.EvalSymlinks(config)
			if err != nil {
				t.Fatal(err)
			}
			l := ClientLocator{InstallationID: "install", InstanceID: "instance", Socket: "/run/control.sock", ConfigPath: config}
			if err = writeClientLocator(s, l, 42); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(j.root, "42", "instance.json")
			if kind == "writable" {
				err = os.Chmod(p, 0666)
			} else {
				err = os.Rename(p, p+".real")
				if err == nil {
					err = os.Symlink(p+".real", p)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = readClientLocator(s, config, 42); err == nil {
				t.Fatal("untrusted locator accepted")
			}
		})
	}
}

func TestTrustStoreRegistrationPrivateRoundTripAndMembership(t *testing.T) {
	j := testJournal(t)
	reg := validTestRegistration()
	if err := j.writeRegistration(reg); err != nil {
		t.Fatal(err)
	}
	got, err := readRegistration(j.store, reg.ID)
	if err != nil || got.ID != reg.ID {
		t.Fatalf("registration roundtrip: %+v %v", got, err)
	}
	path := filepath.Join(j.root, "registrations", reg.ID+".json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private registration mode: %v %v", info, err)
	}
	if err = os.Rename(path, filepath.Join(j.root, "registrations", "other.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = readRegistration(j.store, "other"); err == nil {
		t.Fatal("registration membership mismatch accepted")
	}
	j.store.owner++
	if _, err = j.store.directory("registrations", false); err == nil {
		t.Fatal("different owner accepted")
	}
}
