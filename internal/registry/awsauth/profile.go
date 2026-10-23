package awsauth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rcss-labs/stackstead/internal/secret"
)

var (
	ErrProfileNotFound    = errors.New("awsauth: profile not found")
	ErrProfileUnsupported = errors.New("awsauth: unsupported profile type")
)

// DefaultPaths returns the shared credentials and config file locations.
func DefaultPaths(getenv func(string) string, home string) (credentialsPath, configPath string) {
	credentialsPath = getenv("AWS_SHARED_CREDENTIALS_FILE")
	if credentialsPath == "" {
		credentialsPath = filepath.Join(home, ".aws", "credentials")
	}
	configPath = getenv("AWS_CONFIG_FILE")
	if configPath == "" {
		configPath = filepath.Join(home, ".aws", "config")
	}
	return credentialsPath, configPath
}

func readINI(path string) (map[string]map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("awsauth: open %s: %w", path, err)
	}
	defer f.Close()
	m, err := ParseINI(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

// configSection collects the config-file settings of a profile. As in the AWS
// CLI, "[profile NAME]" may have any whitespace after "profile", and the
// default profile is "[default]" or "[profile default]". Plain "[NAME]"
// sections only count for the default profile.
func configSection(conf map[string]map[string]string, name string) (map[string]string, bool) {
	var out map[string]string
	var found bool
	add := func(sec map[string]string) {
		if out == nil {
			out = map[string]string{}
		}
		for k, v := range sec {
			out[k] = v
		}
		found = true
	}
	if sec, ok := conf["default"]; ok && name == "default" {
		add(sec)
	}
	// Map iteration order is random; sort so overlapping sections merge
	// deterministically.
	sections := make([]string, 0, len(conf))
	for s := range conf {
		sections = append(sections, s)
	}
	sort.Strings(sections)
	for _, s := range sections {
		rest, ok := strings.CutPrefix(s, "profile")
		if !ok || rest == "" || strings.TrimLeft(rest, " \t") == rest {
			continue
		}
		if strings.TrimSpace(rest) == name {
			add(conf[s])
		}
	}
	return out, found
}

// LoadProfile reads static keys for a named profile. Settings from the
// credentials file ([name]) win over the config file ([profile name], or
// [default] for the default profile). Profiles that delegate to roles, SSO or
// external processes are refused.
func LoadProfile(name, credentialsPath, configPath string) (Credentials, string, error) {
	if name == "" {
		name = "default"
	}
	cred, err := readINI(credentialsPath)
	if err != nil {
		return Credentials{}, "", err
	}
	conf, err := readINI(configPath)
	if err != nil {
		return Credentials{}, "", err
	}
	cSec, cOK := cred[name]
	fSec, fOK := configSection(conf, name)
	if !cOK && !fOK {
		return Credentials{}, "", fmt.Errorf("%w: %q", ErrProfileNotFound, name)
	}
	merged := map[string]string{}
	for k, v := range fSec {
		merged[k] = v
	}
	for k, v := range cSec {
		merged[k] = v
	}
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic error when several unsupported keys are set
	for _, k := range keys {
		if k == "role_arn" || k == "credential_process" || k == "source_profile" || strings.HasPrefix(k, "sso_") {
			return Credentials{}, "", fmt.Errorf("%w: profile %q uses %s; only static access keys are supported", ErrProfileUnsupported, name, k)
		}
	}
	id, sk := merged["aws_access_key_id"], merged["aws_secret_access_key"]
	if id == "" || sk == "" {
		return Credentials{}, "", fmt.Errorf("awsauth: profile %q needs aws_access_key_id and aws_secret_access_key", name)
	}
	return Credentials{
		AccessKeyID:     id,
		SecretAccessKey: secret.Secret(sk),
		SessionToken:    secret.Secret(merged["aws_session_token"]),
	}, merged["region"], nil
}
