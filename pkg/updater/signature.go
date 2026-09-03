package updater

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ArtifactSigningMessage is the stable, newline-free envelope CI signs. The
// digest remains independently checked after download; the signature binds
// the URL to that exact digest and target identity.
func ArtifactSigningMessage(downloadURL, sha256Hex string, target VersionInfo) string {
	return strings.Join([]string{downloadURL, strings.ToLower(strings.TrimSpace(sha256Hex)), target.Version, target.Branch, target.CommitHash, target.BuildDate}, "\n")
}

func VerifyArtifactSignature(downloadURL, sha256Hex string, target VersionInfo, signature, publicKey string) error {
	keyBytes, err := decodeKey(publicKey)
	if err != nil {
		return fmt.Errorf("decode update public key: %w", err)
	}
	sigBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(signature))
	if err != nil {
		return fmt.Errorf("decode update signature: %w", err)
	}
	if len(keyBytes) != ed25519.PublicKeySize || len(sigBytes) != ed25519.SignatureSize {
		return errors.New("invalid update signature or public key length")
	}
	if !ed25519.Verify(ed25519.PublicKey(keyBytes), []byte(ArtifactSigningMessage(downloadURL, sha256Hex, target)), sigBytes) {
		return errors.New("update artifact signature verification failed")
	}
	return nil
}

func decodeKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if b, err := base64.StdEncoding.DecodeString(value); err == nil {
		return b, nil
	}
	if b, err := hex.DecodeString(value); err == nil {
		return b, nil
	}
	return nil, errors.New("expected base64 or hex key")
}
