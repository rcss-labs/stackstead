package backup

import (
	"crypto/hmac"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"
)

// keyFileName is the archive passphrase, kept in the data directory. It is
// never collected into an archive (writeBase only archives the database,
// config, networks, stack directories and the image library) and never
// returned by the API. It protects archives that leave the host (S3 bucket,
// offsite copies), not against someone with access to the data directory.
const keyFileName = "backup.key"

// minPassphraseLen is the shortest accepted passphrase, in characters.
const minPassphraseLen = 12

const keyFileVersion = 1

// ErrPassphraseTooShort reports a passphrase under minPassphraseLen characters.
var ErrPassphraseTooShort = errors.New("backup: passphrase must be at least 12 characters")

// KeyInfo describes the stored passphrase without revealing it.
type KeyInfo struct {
	KeyID     string
	CreatedAt time.Time
}

type keyFile struct {
	Version    int       `json:"v"`
	Passphrase string    `json:"passphrase"`
	KeySalt    string    `json:"keySalt"`
	Time       uint32    `json:"time"`
	MemoryKiB  uint32    `json:"memoryKiB"`
	Threads    uint8     `json:"threads"`
	KeyID      string    `json:"keyId"`
	CreatedAt  time.Time `json:"createdAt"`
}

// KeyFilePath returns where the passphrase is stored under dataDir.
func KeyFilePath(dataDir string) string { return filepath.Join(dataDir, keyFileName) }

// WriteKeyFile stores passphrase with fresh key parameters, replacing any
// earlier one atomically, with mode 0600.
func WriteKeyFile(dataDir, passphrase string, now time.Time) (KeyInfo, error) {
	if dataDir == "" {
		return KeyInfo{}, errors.New("backup: data directory is not configured")
	}
	if utf8.RuneCountInString(passphrase) < minPassphraseLen {
		return KeyInfo{}, ErrPassphraseTooShort
	}
	params := NewKeyParams()
	key, err := DeriveKey(passphrase, params)
	if err != nil {
		return KeyInfo{}, err
	}
	info := KeyInfo{KeyID: key.ID(), CreatedAt: now.UTC()}
	raw, err := json.MarshalIndent(keyFile{
		Version: keyFileVersion, Passphrase: passphrase, KeySalt: hex.EncodeToString(params.KeySalt[:]),
		Time: params.Time, MemoryKiB: params.MemoryKiB, Threads: params.Threads,
		KeyID: info.KeyID, CreatedAt: info.CreatedAt,
	}, "", "  ")
	if err != nil {
		return KeyInfo{}, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return KeyInfo{}, err
	}
	final := KeyFilePath(dataDir)
	tmp := final + partialSuffix
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return KeyInfo{}, err
	}
	_, werr := f.Write(raw)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return KeyInfo{}, werr
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return KeyInfo{}, err
	}
	return info, syncDir(dataDir)
}

func readKeyFile(dataDir string) (keyFile, bool, error) {
	raw, err := os.ReadFile(KeyFilePath(dataDir))
	if errors.Is(err, fs.ErrNotExist) {
		return keyFile{}, false, nil
	}
	if err != nil {
		return keyFile{}, false, err
	}
	var kf keyFile
	if err := json.Unmarshal(raw, &kf); err != nil {
		return keyFile{}, false, fmt.Errorf("backup: %s is corrupt: %w", KeyFilePath(dataDir), err)
	}
	salt, serr := hex.DecodeString(kf.KeySalt)
	if kf.Version != keyFileVersion || kf.Passphrase == "" || serr != nil || len(salt) != 16 || len(kf.KeyID) != 2*cryptKeyIDLen {
		return keyFile{}, false, fmt.Errorf("backup: %s is corrupt or from a newer version", KeyFilePath(dataDir))
	}
	return kf, true, nil
}

// ReadKeyInfo reports the stored passphrase's key id without deriving anything.
func ReadKeyInfo(dataDir string) (KeyInfo, bool, error) {
	kf, ok, err := readKeyFile(dataDir)
	if err != nil || !ok {
		return KeyInfo{}, false, err
	}
	return KeyInfo{KeyID: kf.KeyID, CreatedAt: kf.CreatedAt}, true, nil
}

// LoadKey derives the stored passphrase's key. It returns ErrNoKeyFile when
// none is stored.
func LoadKey(dataDir string) (*Key, error) {
	kf, ok, err := readKeyFile(dataDir)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNoKeyFile
	}
	p := KeyParams{Time: kf.Time, MemoryKiB: kf.MemoryKiB, Threads: kf.Threads}
	salt, _ := hex.DecodeString(kf.KeySalt)
	copy(p.KeySalt[:], salt)
	key, err := DeriveKey(kf.Passphrase, p)
	if err != nil {
		return nil, fmt.Errorf("backup: %s: %w", KeyFilePath(dataDir), err)
	}
	if want, _ := hex.DecodeString(kf.KeyID); !hmac.Equal(key.id[:], want) {
		return nil, fmt.Errorf("backup: %s is corrupt: key id does not match", KeyFilePath(dataDir))
	}
	return key, nil
}

// RemoveKeyFile deletes the stored passphrase; a missing file is not an error.
func RemoveKeyFile(dataDir string) error {
	err := os.Remove(KeyFilePath(dataDir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
