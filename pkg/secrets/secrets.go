// Package secrets provides envelope encryption for user-supplied credentials
// (LLM provider API keys, MCP auth tokens) so they are never persisted in raw
// form — not in the database, not in the filesystem mirror, not in backups.
//
// Layout:
//   - Every secret value is encrypted with AES-256-GCM under a random data
//     encryption key (DEK) and stored as "enc:v1:<base64(nonce||ciphertext)>".
//   - The DEK itself is stored wrapped (encrypted) by a master key (KEK) in a
//     small keystore file next to the rest of the headcount1 data. The wrapped
//     DEK is ciphertext, so the keystore file is safe to keep on disk and to
//     include in backups.
//   - The master key comes from a KeySource: HashiCorp Vault (when VAULT_ADDR
//     is set), the HEADCOUNT1_MASTER_KEY env var, or — as a zero-config last
//     resort — an auto-generated 0600 key file.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Prefix marks a value as sealed by this package. Values without it are
// treated as legacy plaintext and passed through on read, which is what makes
// upgrading an existing install a no-op: rows are re-sealed the next time
// they are written.
const Prefix = "enc:v1:"

// IsSealed reports whether a stored value is ciphertext produced by this
// package (root-DEK "enc:v1:" or per-user "enc:u1:").
func IsSealed(v string) bool {
	return strings.HasPrefix(v, Prefix) || strings.HasPrefix(v, PrefixUser)
}

// SecretManager encrypts and decrypts secret values using a DEK wrapped by the
// KeySource's master key. It is the single component that turns plaintext into
// stored ciphertext and back — callers hold it (via Default) and invoke
// Encrypt/EncryptForUser/Decrypt only at the exact point a secret is written or
// used, so plaintext never lives on a long-lived struct or in the DB layer.
// Safe for concurrent use.
type SecretManager struct {
	source       KeySource
	keystorePath string

	mu sync.Mutex
	// wrappedDEK caches the keystore file contents. Only ciphertext is
	// cached — unwrapping still requires the master key on every operation,
	// so revoking the key at its source takes effect immediately (or after
	// the Vault TTL at worst).
	wrappedDEK []byte
	kekFP      string

	// keyring holds unlocked per-user DEKs in memory (see keyring.go). It is
	// the sole place a user's DEK exists in plaintext, and only while that
	// user has an active session.
	keyring *Keyring
}

func NewManager(source KeySource, keystorePath string) *SecretManager {
	return &SecretManager{source: source, keystorePath: keystorePath, keyring: NewKeyring()}
}

// Keyring returns the store's in-memory unlocked-DEK keyring.
func (s *SecretManager) Keyring() *Keyring { return s.keyring }

// userDEKFromKeyring returns a user's unlocked DEK from the keyring, or false
// when the user is locked (logged out, TTL lapsed, or never unlocked).
func (s *SecretManager) userDEKFromKeyring(userID int32) ([32]byte, bool) {
	return s.keyring.Get(userID)
}

// UnlockUser places a user's DEK in the keyring for ttl. Called after a
// successful passkey PRF ceremony (login / unlock / registration).
func (s *SecretManager) UnlockUser(userID int32, dek [32]byte, ttl time.Duration) {
	s.keyring.Put(userID, dek, ttl)
}

// LockUser evicts a user's DEK — their secrets become undecryptable until the
// next unlock. Called on logout.
func (s *SecretManager) LockUser(userID int32) { s.keyring.Evict(userID) }

// IsUnlocked reports whether a user's DEK is currently available. Points of
// use (the LLM proxy, provider test) check this before consuming a secret and
// return a clear "locked — re-authenticate" error instead of a decrypt failure.
func (s *SecretManager) IsUnlocked(userID int32) bool {
	_, ok := s.keyring.Get(userID)
	return ok
}

// Package-level convenience wrappers over the default store's keyring.
func UnlockUser(userID int32, dek [32]byte, ttl time.Duration) {
	Default().UnlockUser(userID, dek, ttl)
}
func LockUser(userID int32)        { Default().LockUser(userID) }
func IsUnlocked(userID int32) bool { return Default().IsUnlocked(userID) }

// SourceName names the active master-key source, for startup logging.
func (s *SecretManager) SourceName() string { return s.source.Name() }

// Encrypt seals a plaintext secret under the root DEK (ownerless "enc:v1:"
// values). Most secrets are user-owned — prefer EncryptForUser. Empty stays
// empty so presence checks (HasToken etc.) keep working, and already-sealed
// values pass through unchanged so a value can never be double-encrypted.
func (s *SecretManager) Encrypt(plaintext string) (string, error) {
	if plaintext == "" || IsSealed(plaintext) {
		return plaintext, nil
	}
	dek, err := s.dek()
	if err != nil {
		return "", err
	}
	blob, err := gcmSeal(dek, []byte(plaintext))
	if err != nil {
		return "", err
	}
	return Prefix + base64.StdEncoding.EncodeToString(blob), nil
}

// Decrypt turns a stored value back into plaintext, routing by its
// self-describing prefix: "enc:u1:" uses the embedded owner's DEK, "enc:v1:"
// the root DEK, and anything else is legacy plaintext returned as-is. Returns
// ErrLocked when a user-owned value's owner is currently locked. Call it at the
// point of use and use the result immediately — never stash it.
func (s *SecretManager) Decrypt(stored string) (string, error) {
	if !IsSealed(stored) {
		return stored, nil
	}
	if strings.HasPrefix(stored, PrefixUser) {
		return s.openUser(stored)
	}
	blob, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, Prefix))
	if err != nil {
		return "", fmt.Errorf("secrets: malformed sealed value: %w", err)
	}
	dek, err := s.dek()
	if err != nil {
		return "", err
	}
	plain, err := gcmOpen(dek, blob)
	if err != nil {
		return "", fmt.Errorf("secrets: cannot decrypt stored secret (was the master key changed?): %w", err)
	}
	return string(plain), nil
}

// ── keystore (wrapped DEK) ───────────────────────────────────────────────────

type keystoreFile struct {
	Version int `json:"version"`
	// WrappedDEK is the data key encrypted with the master key:
	// base64(nonce||ciphertext).
	WrappedDEK string `json:"wrapped_dek"`
	// KEKFingerprint identifies which master key wrapped the DEK, so a key
	// mismatch produces a clear error instead of a bare GCM failure.
	KEKFingerprint string `json:"kek_fingerprint"`
}

// dek returns the unwrapped data key. The wrapped blob is cached in memory;
// the master key is fetched from the source on every call (the Vault source
// applies its own TTL cache internally).
func (s *SecretManager) dek() ([32]byte, error) {
	var zero [32]byte
	kek, err := s.source.MasterKey()
	if err != nil {
		return zero, fmt.Errorf("secrets: master key unavailable from %s: %w", s.source.Name(), err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wrappedDEK == nil {
		if err := s.loadOrCreateKeystoreLocked(kek); err != nil {
			return zero, err
		}
	}
	if fp := fingerprint(kek); fp != s.kekFP {
		return zero, fmt.Errorf("secrets: master key from %s (fingerprint %s) does not match the key that created %s (fingerprint %s) — restore the original master key, or delete the keystore to start over (existing stored secrets will become unreadable)",
			s.source.Name(), fp, s.keystorePath, s.kekFP)
	}
	dekBytes, err := gcmOpen(kek, s.wrappedDEK)
	if err != nil {
		return zero, fmt.Errorf("secrets: failed to unwrap data key with master key from %s: %w", s.source.Name(), err)
	}
	if len(dekBytes) != 32 {
		return zero, fmt.Errorf("secrets: keystore %s contains a data key of unexpected size", s.keystorePath)
	}
	var dek [32]byte
	copy(dek[:], dekBytes)
	return dek, nil
}

func (s *SecretManager) loadOrCreateKeystoreLocked(kek [32]byte) error {
	data, err := os.ReadFile(s.keystorePath)
	if os.IsNotExist(err) {
		return s.createKeystoreLocked(kek)
	}
	if err != nil {
		return fmt.Errorf("secrets: cannot read keystore %s: %w", s.keystorePath, err)
	}
	var ks keystoreFile
	if err := json.Unmarshal(data, &ks); err != nil {
		return fmt.Errorf("secrets: keystore %s is corrupt: %w", s.keystorePath, err)
	}
	wrapped, err := base64.StdEncoding.DecodeString(ks.WrappedDEK)
	if err != nil {
		return fmt.Errorf("secrets: keystore %s is corrupt: %w", s.keystorePath, err)
	}
	s.wrappedDEK = wrapped
	s.kekFP = ks.KEKFingerprint
	return nil
}

func (s *SecretManager) createKeystoreLocked(kek [32]byte) error {
	var dek [32]byte
	if _, err := rand.Read(dek[:]); err != nil {
		return err
	}
	wrapped, err := gcmSeal(kek, dek[:])
	if err != nil {
		return err
	}
	ks := keystoreFile{
		Version:        1,
		WrappedDEK:     base64.StdEncoding.EncodeToString(wrapped),
		KEKFingerprint: fingerprint(kek),
	}
	data, err := json.MarshalIndent(ks, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.keystorePath), 0755); err != nil {
		return err
	}
	// The wrapped DEK is ciphertext, but there's no reason to make it
	// world-readable either.
	if err := os.WriteFile(s.keystorePath, data, 0600); err != nil {
		return fmt.Errorf("secrets: cannot write keystore %s: %w", s.keystorePath, err)
	}
	s.wrappedDEK = wrapped
	s.kekFP = ks.KEKFingerprint
	return nil
}

// fingerprint returns a short non-reversible identifier for a master key.
func fingerprint(key [32]byte) string {
	sum := sha256.Sum256(key[:])
	return hex.EncodeToString(sum[:8])
}

// ── AES-256-GCM primitives ───────────────────────────────────────────────────

func gcmSeal(key [32]byte, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func gcmOpen(key [32]byte, blob []byte) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(blob) < gcm.NonceSize() {
		return nil, fmt.Errorf("ciphertext shorter than nonce")
	}
	return gcm.Open(nil, blob[:gcm.NonceSize()], blob[gcm.NonceSize():], nil)
}

// ── process-wide default store ───────────────────────────────────────────────

var (
	defaultOnce    sync.Once
	defaultManager *SecretManager

	baseDirMu       sync.Mutex
	baseDirResolver func() string
)

// SetBaseDirResolver overrides where the default store keeps its keystore and
// fallback key file. Called by the db package with PaperclipHome so the two
// packages agree on a home directory without an import cycle. Must be called
// before the first Seal/Open; later calls are ignored once the default store
// exists.
func SetBaseDirResolver(f func() string) {
	baseDirMu.Lock()
	defer baseDirMu.Unlock()
	baseDirResolver = f
}

func baseDir() string {
	baseDirMu.Lock()
	f := baseDirResolver
	baseDirMu.Unlock()
	if f != nil {
		return f()
	}
	// Fallback mirrors db.PaperclipHome for standalone use of this package.
	if e2eHome := os.Getenv("E2E_HEADCOUNT1_HOME"); e2eHome != "" {
		return filepath.Join(e2eHome, ".headcount1")
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/.headcount1"
	}
	return filepath.Join(homeDir, ".headcount1")
}

// Default returns the process-wide store, configured from the environment on
// first use (see keySourceFromEnv for the source selection rules).
func Default() *SecretManager {
	defaultOnce.Do(func() {
		dir := baseDir()
		defaultManager = NewManager(keySourceFromEnv(dir), filepath.Join(dir, "keystore.json"))
	})
	return defaultManager
}

// DefaultKeyring returns the process-wide store's keyring, so auth handlers
// (unlock/lock) and the GORM serializer (which uses Default()) share one
// keyring instance.
func DefaultKeyring() *Keyring { return Default().Keyring() }
