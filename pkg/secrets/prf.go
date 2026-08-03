package secrets

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/hkdf"
)

// prfWrapPrefix tags a DEK wrapped under a passkey's PRF output, so the format
// can evolve without ambiguity.
const prfWrapPrefix = "prf1:"

// prfHKDFInfo domain-separates the key derived from a WebAuthn PRF output, so
// the raw PRF bytes are never used directly as an AES key.
const prfHKDFInfo = "headcount1-prf-dek-v1"

// NewUserDEK returns a fresh random 32-byte data-encryption key.
func NewUserDEK() ([32]byte, error) {
	var dek [32]byte
	if _, err := rand.Read(dek[:]); err != nil {
		return dek, err
	}
	return dek, nil
}

// deriveKeyFromPRF turns a WebAuthn PRF output into a 32-byte AES key via
// HKDF-SHA256. The PRF output is high-entropy but context-free; HKDF binds it
// to this use.
func deriveKeyFromPRF(prfOutput []byte) ([32]byte, error) {
	var key [32]byte
	if len(prfOutput) < 16 {
		return key, fmt.Errorf("secrets: PRF output too short (%d bytes)", len(prfOutput))
	}
	r := hkdf.New(sha256.New, prfOutput, nil, []byte(prfHKDFInfo))
	if _, err := io.ReadFull(r, key[:]); err != nil {
		return key, err
	}
	return key, nil
}

// WrapDEKForPRF seals a user's DEK under the key derived from a passkey's PRF
// output, for storage in WebAuthnCredential.WrappedDEK. Each enrolled passkey
// wraps the same DEK under its own PRF value.
func WrapDEKForPRF(dek [32]byte, prfOutput []byte) (string, error) {
	kek, err := deriveKeyFromPRF(prfOutput)
	if err != nil {
		return "", err
	}
	blob, err := gcmSeal(kek, dek[:])
	if err != nil {
		return "", err
	}
	return prfWrapPrefix + base64.StdEncoding.EncodeToString(blob), nil
}

// UnwrapDEKWithPRF recovers a user's DEK from a credential's WrappedDEK using
// the passkey's PRF output produced at login.
func UnwrapDEKWithPRF(wrapped string, prfOutput []byte) ([32]byte, error) {
	var dek [32]byte
	kek, err := deriveKeyFromPRF(prfOutput)
	if err != nil {
		return dek, err
	}
	blob, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(wrapped, prfWrapPrefix))
	if err != nil {
		return dek, fmt.Errorf("secrets: corrupt PRF-wrapped DEK: %w", err)
	}
	dekBytes, err := gcmOpen(kek, blob)
	if err != nil {
		return dek, fmt.Errorf("secrets: PRF output does not open this credential's DEK: %w", err)
	}
	if len(dekBytes) != 32 {
		return dek, fmt.Errorf("secrets: unwrapped DEK has unexpected size %d", len(dekBytes))
	}
	copy(dek[:], dekBytes)
	return dek, nil
}
