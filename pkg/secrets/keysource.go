package secrets

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvMasterKey is the env var holding the master key when no external vault
// is configured.
const EnvMasterKey = "HEADCOUNT1_MASTER_KEY"

// MasterKeyMaterial is what a KeySource yields. When Passphrase is true the
// material is a (potentially low-entropy) human passphrase in Raw that MUST be
// stretched with a salted Argon2id KDF before use as a KEK; otherwise Key holds
// a ready 32-byte high-entropy key used verbatim. Distinguishing the two is what
// lets a leaked keystore + weak passphrase resist an offline dictionary attack.
type MasterKeyMaterial struct {
	Key        [32]byte
	Raw        []byte
	Passphrase bool
}

// KeySource supplies the master key material that (after any KDF) wraps the data
// key. Implementations are consulted on every seal/open so that revoking the key
// at its source takes effect without a restart; a source whose fetch is
// expensive (Vault) applies its own TTL cache internally.
type KeySource interface {
	// Name identifies the source in logs and error messages.
	Name() string
	MasterKey() (MasterKeyMaterial, error)
}

// keySourceFromEnv picks the master-key source:
//
//  1. VAULT_ADDR set → HashiCorp Vault. Deliberately no fallback to the
//     sources below on failure — a configured vault that errors must surface,
//     not silently downgrade to a weaker key source.
//  2. HEADCOUNT1_MASTER_KEY set → environment variable.
//  3. otherwise → auto-generated 0600 key file under dir (zero-config
//     default; protects DB dumps and backups, but not an attacker with full
//     filesystem access as the same user).
func keySourceFromEnv(dir string) KeySource {
	if addr := os.Getenv("VAULT_ADDR"); addr != "" {
		return newVaultKeySourceFromEnv(addr)
	}
	if os.Getenv(EnvMasterKey) != "" {
		return envKeySource{}
	}
	return &fileKeySource{path: filepath.Join(dir, "master.key")}
}

// parseMasterKey classifies a user-supplied key string: 64 hex chars or base64
// of exactly 32 bytes are high-entropy keys used verbatim; anything else is
// treated as a passphrase to be run through Argon2id with a per-install salt
// (see SecretManager.deriveKEK) — never a bare unsalted hash.
func parseMasterKey(v string) (MasterKeyMaterial, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return MasterKeyMaterial{}, fmt.Errorf("master key is empty")
	}
	var m MasterKeyMaterial
	if len(v) == 64 {
		if b, err := hex.DecodeString(v); err == nil {
			copy(m.Key[:], b)
			return m, nil
		}
	}
	if b, err := base64.StdEncoding.DecodeString(v); err == nil && len(b) == 32 {
		copy(m.Key[:], b)
		return m, nil
	}
	m.Passphrase = true
	m.Raw = []byte(v)
	return m, nil
}

// ── env var source ───────────────────────────────────────────────────────────

// envKeySource reads HEADCOUNT1_MASTER_KEY on every call — reading an env var
// is free, so there is nothing to cache.
type envKeySource struct{}

func (envKeySource) Name() string { return "env:" + EnvMasterKey }

func (envKeySource) MasterKey() (MasterKeyMaterial, error) {
	v := os.Getenv(EnvMasterKey)
	if v == "" {
		return MasterKeyMaterial{}, fmt.Errorf("%s is no longer set", EnvMasterKey)
	}
	return parseMasterKey(v)
}

// ── key file source (zero-config fallback) ───────────────────────────────────

// fileKeySource reads the key file on every call (a local read is cheap and
// keeps "delete the file" an effective kill switch), generating it on first
// use.
type fileKeySource struct{ path string }

func (f *fileKeySource) Name() string { return "file:" + f.path }

func (f *fileKeySource) MasterKey() (MasterKeyMaterial, error) {
	data, err := os.ReadFile(f.path)
	if os.IsNotExist(err) {
		return f.generate()
	}
	if err != nil {
		return MasterKeyMaterial{}, err
	}
	return parseMasterKey(string(data))
}

// generate creates the auto key file on first use. It writes with O_EXCL so two
// concurrent first-boot callers can't each generate a DIFFERENT random key and
// race to write — that would leave the file holding one key while another
// caller wrapped the DEK under the other, tripping the fingerprint guard forever
// (unrecoverable). On EEXIST we re-read the winner's key.
func (f *fileKeySource) generate() (MasterKeyMaterial, error) {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return MasterKeyMaterial{}, err
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0700); err != nil {
		return MasterKeyMaterial{}, err
	}
	fh, err := os.OpenFile(f.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			data, rerr := os.ReadFile(f.path)
			if rerr != nil {
				return MasterKeyMaterial{}, rerr
			}
			return parseMasterKey(string(data))
		}
		return MasterKeyMaterial{}, err
	}
	defer fh.Close()
	if _, err := fh.Write([]byte(hex.EncodeToString(key[:]) + "\n")); err != nil {
		return MasterKeyMaterial{}, err
	}
	var m MasterKeyMaterial
	m.Key = key
	return m, nil
}
