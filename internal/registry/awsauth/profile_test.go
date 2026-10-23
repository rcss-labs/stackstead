package awsauth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, creds, cfg string) (string, string) {
	t.Helper()
	d := t.TempDir()
	cp, fp := filepath.Join(d, "credentials"), filepath.Join(d, "config")
	if creds != "" {
		os.WriteFile(cp, []byte(creds), 0o600)
	}
	if cfg != "" {
		os.WriteFile(fp, []byte(cfg), 0o600)
	}
	return cp, fp
}

func TestLoadProfileStaticKeys(t *testing.T) {
	cp, fp := writeFiles(t,
		"[work]\naws_access_key_id=AKIA\naws_secret_access_key=sek\naws_session_token=tok\n",
		"[profile work]\nregion = eu-central-1\n[default]\nregion=us-east-1\naws_access_key_id=D\naws_secret_access_key=S\n")
	c, region, err := LoadProfile("work", cp, fp)
	if err != nil {
		t.Fatal(err)
	}
	if c.AccessKeyID != "AKIA" || c.SecretAccessKey.Reveal() != "sek" || c.SessionToken.Reveal() != "tok" || region != "eu-central-1" {
		t.Errorf("got %+v region %q", c, region)
	}
	// Keys only in config, via [default].
	c, region, err = LoadProfile("default", cp, fp)
	if err != nil || c.AccessKeyID != "D" || region != "us-east-1" {
		t.Errorf("default: %+v %q %v", c, region, err)
	}
	// Keys in config under [profile x].
	cp2, fp2 := writeFiles(t, "", "[profile x]\naws_access_key_id=K\naws_secret_access_key=V\n")
	c, _, err = LoadProfile("x", cp2, fp2)
	if err != nil || c.AccessKeyID != "K" {
		t.Errorf("config profile: %+v %v", c, err)
	}
}

func TestLoadProfileColonDelimiter(t *testing.T) {
	cp, fp := writeFiles(t, "[work]\naws_access_key_id: AKIA\naws_secret_access_key: sek\n", "")
	c, _, err := LoadProfile("work", cp, fp)
	if err != nil || c.AccessKeyID != "AKIA" || c.SecretAccessKey.Reveal() != "sek" {
		t.Errorf("got %+v %v", c, err)
	}
}

func TestLoadProfileExtraSpacesInConfigSection(t *testing.T) {
	cp, fp := writeFiles(t, "[work]\naws_access_key_id=K\naws_secret_access_key=V\n", "[profile  work]\nregion=eu-west-1\n")
	_, region, err := LoadProfile("work", cp, fp)
	if err != nil || region != "eu-west-1" {
		t.Errorf("region %q err %v", region, err)
	}
	cp, fp = writeFiles(t, "", "[profile\twork]\naws_access_key_id=K\naws_secret_access_key=V\n")
	if c, _, err := LoadProfile("work", cp, fp); err != nil || c.AccessKeyID != "K" {
		t.Errorf("tab: %+v %v", c, err)
	}
}

func TestLoadProfileProfileDefaultSection(t *testing.T) {
	cp, fp := writeFiles(t, "", "[profile default]\nregion=eu-west-1\naws_access_key_id=K\naws_secret_access_key=V\n")
	for _, name := range []string{"default", ""} {
		c, region, err := LoadProfile(name, cp, fp)
		if err != nil || c.AccessKeyID != "K" || region != "eu-west-1" {
			t.Errorf("%q: %+v %q %v", name, c, region, err)
		}
	}
}

// The AWS CLI also reads region from the credentials file, and that file wins
// over the config file; this pins the behaviour so it is not "fixed" away.
func TestLoadProfileRegionFromCredentialsFile(t *testing.T) {
	cp, fp := writeFiles(t, "[work]\naws_access_key_id=K\naws_secret_access_key=V\nregion=eu-west-1\n", "[profile work]\nregion=us-east-1\n")
	if _, region, err := LoadProfile("work", cp, fp); err != nil || region != "eu-west-1" {
		t.Errorf("region %q err %v", region, err)
	}
}

func TestLoadProfileUnsupported(t *testing.T) {
	for _, key := range []string{"role_arn", "sso_start_url", "sso_session", "credential_process", "source_profile"} {
		cp, fp := writeFiles(t, "", "[profile p]\n"+key+" = value-"+"zzz\naws_access_key_id=K\naws_secret_access_key=secretzzz\n")
		_, _, err := LoadProfile("p", cp, fp)
		if !errors.Is(err, ErrProfileUnsupported) || !strings.Contains(err.Error(), key) {
			t.Errorf("%s: err = %v", key, err)
		}
		if err != nil && strings.Contains(err.Error(), "secretzzz") {
			t.Errorf("secret in error: %v", err)
		}
	}
}

func TestLoadProfileMissing(t *testing.T) {
	cp, fp := writeFiles(t, "[a]\naws_access_key_id=K\naws_secret_access_key=V\n", "")
	if _, _, err := LoadProfile("nope", cp, fp); !errors.Is(err, ErrProfileNotFound) {
		t.Errorf("err = %v", err)
	}
	if _, _, err := LoadProfile("nope", filepath.Join(t.TempDir(), "x"), filepath.Join(t.TempDir(), "y")); !errors.Is(err, ErrProfileNotFound) {
		t.Errorf("no files: err = %v", err)
	}
}

func TestLoadProfileIncomplete(t *testing.T) {
	cp, fp := writeFiles(t, "[a]\naws_access_key_id=K\n", "")
	_, _, err := LoadProfile("a", cp, fp)
	if err == nil || errors.Is(err, ErrProfileNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestDefaultPaths(t *testing.T) {
	cp, fp := DefaultPaths(func(string) string { return "" }, "/home/u")
	if cp != "/home/u/.aws/credentials" || fp != "/home/u/.aws/config" {
		t.Errorf("%s %s", cp, fp)
	}
	env := map[string]string{"AWS_SHARED_CREDENTIALS_FILE": "/c", "AWS_CONFIG_FILE": "/f"}
	cp, fp = DefaultPaths(func(k string) string { return env[k] }, "/home/u")
	if cp != "/c" || fp != "/f" {
		t.Errorf("%s %s", cp, fp)
	}
}
