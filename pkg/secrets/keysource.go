package secrets

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvMasterKey is the env var holding the master key when no external vault
// is configured.
const EnvMasterKey = "PAPERCLIP_MASTER_KEY"

// KeySource supplies the 32-byte master key (KEK) that wraps the data key.
// Implementations are consulted on every seal/open so that revoking the key
// at its source takes effect without a restart; a source whose fetch is
// expensive (Vault) applies its own TTL cache internally.
type KeySource interface {
	// Name identifies the source in logs and error messages.
	Name() string
	MasterKey() ([32]byte, error)
}

// keySourceFromEnv picks the master-key source:
//
//  1. VAULT_ADDR set → HashiCorp Vault. Deliberately no fallback to the
//     sources below on failure — a configured vault that errors must surface,
//     not silently downgrade to a weaker key source.
//  2. PAPERCLIP_MASTER_KEY set → environment variable.
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

// parseMasterKey turns a user-supplied key string into 32 bytes: 64 hex
// chars or base64 of exactly 32 bytes are used verbatim; anything else is
// treated as a passphrase and hashed with SHA-256.
func parseMasterKey(v string) ([32]byte, error) {
	var key [32]byte
	v = strings.TrimSpace(v)
	if v == "" {
		return key, fmt.Errorf("master key is empty")
	}
	if len(v) == 64 {
		if b, err := hex.DecodeString(v); err == nil {
			copy(key[:], b)
			return key, nil
		}
	}
	if b, err := base64.StdEncoding.DecodeString(v); err == nil && len(b) == 32 {
		copy(key[:], b)
		return key, nil
	}
	key = sha256.Sum256([]byte(v))
	return key, nil
}

// ── env var source ───────────────────────────────────────────────────────────

// envKeySource reads PAPERCLIP_MASTER_KEY on every call — reading an env var
// is free, so there is nothing to cache.
type envKeySource struct{}

func (envKeySource) Name() string { return "env:" + EnvMasterKey }

func (envKeySource) MasterKey() ([32]byte, error) {
	v := os.Getenv(EnvMasterKey)
	if v == "" {
		return [32]byte{}, fmt.Errorf("%s is no longer set", EnvMasterKey)
	}
	return parseMasterKey(v)
}

// ── key file source (zero-config fallback) ───────────────────────────────────

// fileKeySource reads the key file on every call (a local read is cheap and
// keeps "delete the file" an effective kill switch), generating it on first
// use.
type fileKeySource struct{ path string }

func (f *fileKeySource) Name() string { return "file:" + f.path }

func (f *fileKeySource) MasterKey() ([32]byte, error) {
	data, err := os.ReadFile(f.path)
	if os.IsNotExist(err) {
		return f.generate()
	}
	if err != nil {
		return [32]byte{}, err
	}
	return parseMasterKey(string(data))
}

func (f *fileKeySource) generate() ([32]byte, error) {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return key, err
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0755); err != nil {
		return key, err
	}
	if err := os.WriteFile(f.path, []byte(hex.EncodeToString(key[:])+"\n"), 0600); err != nil {
		return key, err
	}
	return key, nil
}
