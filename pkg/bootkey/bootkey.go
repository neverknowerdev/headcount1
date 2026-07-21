// Package bootkey provides the boot key used to seal the graceful-exit keyring
// snapshot (see pkg/secrets.KeyUnwrapper). Backends, in selection order:
//
//   - Vault / OpenBao Transit (VAULT_ADDR set): encrypt/decrypt where the key
//     never leaves the vault — the recommended off-box option, free when
//     self-hosted.
//   - env key (HEADCOUNT1_BOOT_KEY): an AES key injected at boot, never
//     persisted. Simplest; the operator's choice for a locked-down single box.
//   - none: no boot key → no graceful-exit re-warm; every restart (planned or
//     not) requires a passkey re-tap. Fully zero-knowledge.
//
// Cloud KMS (AWS/GCP/Azure) plug in behind the same interface; add a backend
// file implementing Seal/Unseal via the provider SDK and wire it into FromEnv.
package bootkey

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/argon2"

	"agent-orchestrator/pkg/secrets"
)

// FromEnv selects the boot-key backend from the environment, or returns nil
// when none is configured (graceful-exit re-warm disabled).
func FromEnv() secrets.KeyUnwrapper {
	if addr := os.Getenv("VAULT_ADDR"); addr != "" {
		if u := vaultTransitFromEnv(addr); u != nil {
			return u
		}
	}
	if key := os.Getenv("HEADCOUNT1_BOOT_KEY"); key != "" {
		if u, err := newEnvUnwrapper(key); err == nil {
			return u
		}
	}
	return nil
}

// ── self-managed local boot key ──────────────────────────────────────────────

// LocalBootKeyEnabled reports the HEADCOUNT1_LOCAL_BOOTKEY opt-in for the
// self-managed local boot key (see LocalBootKey). Only consulted when no
// external boot key (Vault / HEADCOUNT1_BOOT_KEY) is configured.
func LocalBootKeyEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("HEADCOUNT1_LOCAL_BOOTKEY"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// LocalBootKey is a zero-config boot key for a single-user/local box. Unlike an
// external boot key (env/Vault, persistent and held off-box), it is a random AES
// key kept only in memory and written to disk ONLY at graceful shutdown — next
// to the keyring snapshot it seals — then consumed and deleted at the next
// startup. So no boot-key material sits on disk while the server is running.
//
// The trade-off it makes explicit: during the offline window between a graceful
// stop and the next start, the key file and the snapshot are on disk together,
// so anyone who reads the disk in that window can decrypt the snapshot. That's
// fine for local dev but is why production should use an external boot key
// (kept off the box) instead — set HEADCOUNT1_BOOT_KEY or VAULT_ADDR.
type LocalBootKey struct {
	*envUnwrapper
	hexKey string
	path   string
}

// LoadOrCreateLocalBootKey returns the self-managed boot key at path. If the
// last graceful shutdown left a key file there, it is loaded (so this boot can
// unseal that snapshot) and immediately deleted — the key must not linger on
// disk during runtime. Otherwise a fresh random key is minted in memory. Call
// Persist at graceful shutdown to write the current key back for the next boot.
func LoadOrCreateLocalBootKey(path string) (*LocalBootKey, error) {
	if raw, err := os.ReadFile(path); err == nil {
		h := strings.TrimSpace(string(raw))
		if u, err := newEnvUnwrapper(h); err == nil {
			// Consume it: no key material stays on disk while we run.
			_ = os.Remove(path)
			return &LocalBootKey{envUnwrapper: u, hexKey: h, path: path}, nil
		}
	}
	var k [32]byte
	if _, err := rand.Read(k[:]); err != nil {
		return nil, err
	}
	h := hex.EncodeToString(k[:])
	u, err := newEnvUnwrapper(h)
	if err != nil {
		return nil, err
	}
	return &LocalBootKey{envUnwrapper: u, hexKey: h, path: path}, nil
}

func (l *LocalBootKey) Name() string { return "local:self-managed (" + l.path + ")" }

// Persist writes the key to disk (0600) so the next startup can unseal the
// snapshot this shutdown produced. Call it ONLY on a graceful exit, right before
// (or alongside) writing the keyring snapshot.
func (l *LocalBootKey) Persist() error {
	return os.WriteFile(l.path, []byte(l.hexKey), 0600)
}

// ── env boot key (AES-256-GCM) ───────────────────────────────────────────────

type envUnwrapper struct{ key [32]byte }

func newEnvUnwrapper(raw string) (*envUnwrapper, error) {
	key, err := parseKey(raw)
	if err != nil {
		return nil, err
	}
	return &envUnwrapper{key: key}, nil
}

func (e *envUnwrapper) Name() string { return "env:HEADCOUNT1_BOOT_KEY" }

func (e *envUnwrapper) Seal(plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(e.key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func (e *envUnwrapper) Unseal(ciphertext []byte) ([]byte, error) {
	gcm, err := newGCM(e.key)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, fmt.Errorf("bootkey: ciphertext shorter than nonce")
	}
	return gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], nil)
}

func newGCM(key [32]byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// parseKey accepts 64-hex, or hashes any other passphrase to 32 bytes.
// bootKeyKDFSalt is a fixed application salt for stretching a passphrase boot
// key. A per-install salt would need somewhere non-secret to live (the boot key
// is injected at runtime, not stored), so a constant is used — it still forces
// an attacker to pay the Argon2id memory-hard cost per guess rather than a
// trivial unsalted hash, which is the point.
var bootKeyKDFSalt = []byte("headcount1-bootkey-kdf-v1")

func parseKey(raw string) ([32]byte, error) {
	var key [32]byte
	if len(raw) == 64 {
		if b, err := hex.DecodeString(raw); err == nil {
			copy(key[:], b)
			return key, nil
		}
	}
	// Not a 32-byte hex key → treat as a (possibly low-entropy) passphrase and
	// stretch it with Argon2id instead of a bare SHA-256, so a leaked transient
	// keyring.sealed blob can't be brute-forced with a cheap unsalted hash.
	dk := argon2.IDKey([]byte(raw), bootKeyKDFSalt, 3, 64*1024, 4, 32)
	copy(key[:], dk)
	return key, nil
}
