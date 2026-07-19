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
