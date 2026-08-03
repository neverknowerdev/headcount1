package secrets

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// PrefixUser marks a value sealed with a specific user's DEK. The owning
// user's ID is embedded in the value ("enc:u1:<userID>:<base64>") so that
// decryption is self-describing — it never depends on which struct or column
// the ciphertext came from, only on whether that user is currently unlocked.
const PrefixUser = "enc:u1:"

// ErrLocked is returned when an operation needs a user's DEK but the user is
// not unlocked: logged out, session/TTL lapsed, or — after an unexpected
// crash — not yet re-tapped. It is not a decryption failure; callers translate
// it to a clear "vault locked — re-authenticate" response. (The GORM serializer
// never hits this: it stores/loads ciphertext verbatim and never decrypts on
// read — decryption happens only at the explicit point of use.)
var ErrLocked = errors.New("secrets: user vault is locked")

// EncryptForUser seals a secret under the user's in-memory DEK — the primary
// write path for user-owned secrets. Returns ErrLocked when the user is not
// unlocked: a write must NEVER silently fall back to another key, which would
// produce a value the user could not decrypt after re-login. Empty stays empty
// and already-sealed values pass through.
func (s *SecretManager) EncryptForUser(userID int32, plaintext string) (string, error) {
	if plaintext == "" || IsSealed(plaintext) {
		return plaintext, nil
	}
	dek, ok := s.userDEKFromKeyring(userID)
	if !ok {
		return "", ErrLocked
	}
	blob, err := gcmSeal(dek, []byte(plaintext))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%d:%s", PrefixUser, userID, base64.StdEncoding.EncodeToString(blob)), nil
}

// openUser decrypts an "enc:u1:" value using the embedded owner's unlocked
// DEK. Returns ErrLocked when that user is currently locked. This is a pure
// in-memory keyring read — safe inside a GORM row Scan holding the (single,
// SQLite) DB connection.
func (s *SecretManager) openUser(stored string) (string, error) {
	rest := strings.TrimPrefix(stored, PrefixUser)
	idStr, b64, ok := strings.Cut(rest, ":")
	if !ok {
		return "", fmt.Errorf("secrets: malformed user-sealed value")
	}
	userID, err := strconv.ParseInt(idStr, 10, 32)
	if err != nil {
		return "", fmt.Errorf("secrets: malformed user id in sealed value: %w", err)
	}
	blob, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("secrets: malformed user-sealed value: %w", err)
	}
	dek, ok := s.userDEKFromKeyring(int32(userID))
	if !ok {
		return "", ErrLocked
	}
	plain, err := gcmOpen(dek, blob)
	if err != nil {
		return "", fmt.Errorf("secrets: cannot decrypt secret of user %d: %w", userID, err)
	}
	return string(plain), nil
}
