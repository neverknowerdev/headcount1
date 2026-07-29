// Package secrets provides per-user encryption for user-supplied credentials
// (LLM provider API keys, MCP auth tokens, SSH keys) so they are never persisted
// in raw form — not in the database, not in the filesystem mirror, not in
// backups.
//
// Every secret is sealed under its owning user's data-encryption key (DEK) with
// AES-256-GCM and stored self-describingly as "enc:u1:<userID>:<base64>". A
// user's DEK exists only in the in-memory keyring (see keyring.go), unlocked at
// login by their passkey's WebAuthn PRF output (see prf.go) and evicted on
// logout — so when a user is logged out, no key on the box can open their
// secrets. There is deliberately no server-held master key: the only thing that
// can decrypt a user's data is that user's passkey.
//
// The keyring can be sealed under a boot key across a planned restart (see
// seal.go, pkg/bootkey) to avoid a passkey re-tap; that boot key is unrelated to
// the per-user DEKs and never decrypts them at rest.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"strings"
	"sync"
	"time"

	"agent-orchestrator/pkg/secrets/redact"
)

// secretNamespace is the reserved prefix for every sealed-value format this
// project has ever used. A value in this namespace that is not a format the
// running version understands must be rejected on read, never passed through
// as plaintext.
const secretNamespace = "enc:"

// IsSealed reports whether a stored value is ciphertext produced by this
// package (a per-user "enc:u1:" value). Anything else is treated as legacy
// plaintext and passed through on read.
func IsSealed(v string) bool {
	return strings.HasPrefix(v, PrefixUser)
}

// SecretManager encrypts and decrypts user-owned secret values. It is the single
// component that turns plaintext into stored ciphertext and back — callers hold
// it (via Default) and invoke EncryptForUser/Decrypt only at the exact point a
// secret is written or used, so plaintext never lives on a long-lived struct or
// in the DB layer. Safe for concurrent use.
type SecretManager struct {
	// keyring holds unlocked per-user DEKs in memory (see keyring.go). It is the
	// sole place a user's DEK exists in plaintext, and only while that user has
	// an active session.
	keyring *Keyring
}

func NewManager() *SecretManager {
	return &SecretManager{keyring: NewKeyring()}
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

// Decrypt turns a stored value back into plaintext. A per-user "enc:u1:" value
// is opened with the embedded owner's unlocked DEK (returning ErrLocked when
// that user is currently locked); anything else is legacy plaintext returned
// as-is. Call it at the point of use and use the result immediately — never
// stash it.
func (s *SecretManager) Decrypt(stored string) (string, error) {
	if IsSealed(stored) {
		return s.openUser(stored)
	}
	// A value that carries the reserved "enc:" namespace but is not a format
	// this version can open (e.g. the retired server-master-key "enc:v1:", or a
	// corrupted/truncated sealed value) must NOT be handed back as plaintext —
	// that would ship raw ciphertext to an outbound request in place of the real
	// secret. Fail loudly instead of leaking.
	if strings.HasPrefix(stored, secretNamespace) {
		return "", fmt.Errorf("secrets: unrecognized sealed format, refusing to use as plaintext")
	}
	// Legacy plaintext values are still secrets — register them for
	// log/history redaction just like freshly decrypted ones.
	redact.Register("secret", stored)
	return stored, nil
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
)

// Default returns the process-wide store. All secret material lives in memory
// (the keyring), so there is nothing to configure from the environment.
func Default() *SecretManager {
	defaultOnce.Do(func() {
		defaultManager = NewManager()
	})
	return defaultManager
}

// DefaultKeyring returns the process-wide store's keyring, so auth handlers
// (unlock/lock) and the GORM serializer (which uses Default()) share one
// keyring instance.
func DefaultKeyring() *Keyring { return Default().Keyring() }
